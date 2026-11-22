# 架构设计（v1）

## 1. 总体结构

```
              客户端
   OpenAI SDK / Codex CLI          Anthropic SDK / Claude Code
        │                                    │
        ▼                                    ▼
  /openai/v1/chat/completions          /anthropic/v1/messages
        └──────────────┬─────────────────────┘
                       ▼
        ┌──────────────────────────────┐
        │ ingress                      │
        │  路径归一化 → 协议判定       │
        │  鉴权 → request_id → 错误编码│
        └──────────────┬───────────────┘
                       ▼
        ┌──────────────────────────────┐
        │ router（查表）               │
        │  (protocol, model) → Target  │
        └──────────────┬───────────────┘
                       ▼
        ┌──────────────────────────────┐
        │ transport                    │
        │  取上游 Key（轮询）           │
        │  最小改写：model + 鉴权头     │
        │  转发 + SSE 透传 + usage 嗅探 │
        └──────────────┬───────────────┘
                       ▼
                  上游 API
                       │
              usage/log（异步）→ SQLite / Prometheus
```

## 2. 包结构

```
cmd/llmgw/                 main：加载配置、启动 HTTP、优雅退出
internal/config/           YAML 解析、校验、环境变量展开、热重载
internal/router/           查表路由与 Target
internal/ingress/          路径归一化、协议判定、请求元数据解析
internal/ingress/openai/   OpenAI 请求解析与错误编码
internal/ingress/anthropic/
internal/transport/        转发、SSE 透传、usage 嗅探、失败分类
internal/keypool/          上游 Key 轮询与失效标记
internal/auth/             网关 Key 校验
internal/store/            SQLite schema、仓储、异步日志写入
internal/usage/            价格表与成本计算
internal/admin/            管理 REST API
internal/observ/           metrics、日志
internal/server/           mux 组装、中间件
```

## 3. 请求生命周期

1. **归一化路径**，确定 `protocol`（`openai` | `anthropic`）。
2. **鉴权**：从 `Authorization: Bearer` 或 `x-api-key` 取网关 Key，SHA-256 比对。
3. **解析元数据**：只取 `model` 与 `stream`，用 `jsonparser` 流式读取，不反序列化整个 body。
4. **路由**：`router.Resolve(protocol, model)` → `Target{EndpointID, ProviderID, UpstreamModel}`。
5. **改写 body**：用 `sjson` 仅替换 `model`；OpenAI 流式请求注入 `stream_options.include_usage`。
6. **选 Key**：从 provider 的 Key 池原子轮询，跳过失效 Key。
7. **构造上游请求**：拼接 base_url 与路径，**先全量剥离**客户端鉴权头与 `anthropic-version`，再注入上游鉴权。
8. **转发**：
   - 非流式：读完整响应 → 解析 usage → 写回客户端。
   - 流式：逐行读取上游 SSE，边转发边嗅探 usage，每个事件 flush。
9. **收尾**：记录耗时、TTFT、token、成本；写入异步队列；更新 metrics。

## 4. 关键实现约束

### 4.1 body 最小改写

使用 `github.com/tidwall/sjson` 就地修改，保留其余字节。禁止 `map[string]any` 解码后重新编码，理由：丢数字精度、改字段顺序、丢未知字段。

### 4.2 鉴权头处理

```go
for _, h := range []string{"Authorization", "X-Api-Key", "Anthropic-Version", "Anthropic-Beta"} {
    req.Header.Del(h)
}
req.Header.Set("Authorization", "Bearer "+key)        // OpenAI
req.Header.Set("x-api-key", key)                      // Anthropic
req.Header.Set("anthropic-version", endpoint.Version) // Anthropic，默认 2023-06-01
```

### 4.3 SSE 透传

- 以 `\n` 为单位读取，按空行切分事件；**原样写出**读到的字节，不做事件重建。
- 在写出前用轻量解析提取 `usage`（OpenAI：`usage` 字段；Anthropic：`message_start.message.usage` 与 `message_delta.usage`）。
- 每写完一个事件调用 `http.Flusher.Flush()`。
- 客户端 `ctx.Done()` → 取消上游请求。

### 4.4 usage 嗅探

| 协议 | 非流式 | 流式 |
|---|---|---|
| OpenAI | 响应体 `usage` | 末尾 chunk 的 `usage`（需注入 `stream_options.include_usage`） |
| Anthropic | 响应体 `usage` | `message_start` 的 input_tokens + `message_delta` 的 output_tokens |

上游不支持 `stream_options` 时：转发被拒绝 → 回退为不带该字段重发一次，并标记 `usage_estimated=true`（按字符数/4 估算）。

### 4.5 错误分类

| 情况 | 行为 |
|---|---|
| 连接失败 / DNS | 502 |
| 建连或读超时 | 504 |
| 上游 4xx / 5xx（非 401/403） | 原样透传 status + body |
| 401 / 403 | 标记 Key 失效，换 Key 重试一次；无可用 Key 则透传 |
| 流式首包后错误 | 不重试，写出错误事件并结束 |
| 模型未配置 | 404，遵循入口协议错误体 |

## 5. 并发模型

- 每个请求一个 goroutine，上游请求使用共享 `http.Transport`（连接池）。
- 路由快照用 `atomic.Pointer[Snapshot]`，读路径无锁。
- Key 池用 `atomic.Uint64` 轮询索引；失效状态用 `atomic.Bool`。
- 日志写入：有界 channel（8192）+ 单写协程 + 批量事务；满则丢弃并 `gw_log_dropped_total++`。
- 关闭顺序：停止接收新请求 → 等待在途请求（上限 30s）→ 冲刷日志队列 → 关闭 DB。

## 6. 配置热更新

```
fsnotify 或 1s mtime 轮询
      ↓
解析 + 校验（失败则保留旧配置）
      ↓
构建 Snapshot（索引、端点、Key 池）
      ↓
atomic.Pointer.Store(new)
```

Key 池状态（失效标记、轮询索引）按 `provider name` 迁移到新快照，避免重载后失效 Key 复活。

## 7. 数据库

六张表：`providers` 不落库（来自配置），实际落库的是：

- `api_keys`
- `request_logs`
- `usage_daily`
- `settings`

`providers` / `provider_endpoints` 仅存在于配置快照中，管理 API 只读展示。

## 8. 目录与构建

```
make build   # 构建二进制
make test    # 单元测试 + race
make run     # 本地启动（读取 config.yaml）
```

无 cgo：`modernc.org/sqlite`。交叉编译：

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/llmgw
```
