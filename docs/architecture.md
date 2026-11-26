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
        │ (protocol, model) → []Target│
        └──────────────┬───────────────┘
                       ▼
        ┌──────────────────────────────┐
        │ transport                    │
        │  注入上游 Key（单 Key）       │
        │  最小改写：model + 鉴权头     │
        │  转发 + SSE 透传 + usage 嗅探 │
        │  失败时切下一个候选           │
        └──────────────┬───────────────┘
                       ▼
                  上游 API
                       │
              usage/log（异步）→ SQLite / Prometheus
```

候选链由两层配置编译而来：**Instance**（物理上游接入点）与 **Model**（逻辑模型）。详见 `docs/routing.md`。

## 2. 包结构

```
cmd/llmgw/                 main：加载配置、启动 HTTP、优雅退出
internal/config/           YAML 解析、校验、环境变量展开、热重载
internal/preset/           内置端点目录（MiniMax 编码套餐等）
internal/router/           查表路由与候选链 Target
internal/health/           上游接入点的熔断状态（跨热重载存活）
internal/ingress/          路径归一化、协议判定、请求元数据解析
internal/transport/        转发、SSE 透传、usage 嗅探、失败分类
internal/auth/             网关 Key 生成、bcrypt 密码、会话令牌
internal/clientip/         可信代理下解析真实客户端 IP
internal/store/            SQLite schema、仓储、异步日志写入
internal/server/           mux、中间件、数据面 handler、管理 REST API
internal/webui/            内嵌的 Vite 构建产物（go:embed）
internal/observ/           metrics、日志
web/                       Vue 3 + Vite 控制台源码
```

## 3. 请求生命周期

1. **归一化路径**，确定 `protocol`（`openai` | `anthropic`）。
2. **鉴权**：从 `Authorization: Bearer` 或 `x-api-key` 取网关 Key，SHA-256 后在内存缓存中比对（启动时加载，管理面变更时同步更新），请求路径不查库。
3. **解析元数据**：只取 `model` 与 `stream`，用 `jsonparser` 流式读取，不反序列化整个 body。
4. **路由**：`router.Resolve(protocol, model)` → `[]Target{Instance, Protocol, BaseURL, UpstreamModel}`，按优先级排序的候选链。
5. **改写 body**：用 `sjson` 仅替换 `model`；OpenAI 流式请求注入 `stream_options.include_usage`。
6. **注入上游鉴权**：每个上游接入点一个 Key；空 Key 表示上游无需鉴权。
7. **构造上游请求**：拼接 base_url 与路径，**先全量剥离**客户端鉴权头与 `anthropic-version`，再注入上游鉴权。
8. **转发**：
   - 非流式：读完整响应 → 解析 usage → 写回客户端。
   - 流式：逐行读取上游 SSE，边转发边嗅探 usage，每个事件 flush。
9. **失败切换**：连接失败、超时、`5xx`、`429` 时换下一个候选重试；`4xx` 原样透传。流式响应一旦拿到上游响应头即锁定该上游。
10. **收尾**：记录耗时、TTFT、token；写入异步队列；更新 metrics。

## 4. 关键实现约束

### 4.1 body 最小改写
使用 `github.com/tidwall/sjson` 就地修改，保留其余字节。禁止 `map[string]any` 解码后重新编码，理由：丢数字精度、改字段顺序、丢未知字段。

### 4.2 鉴权头处理

```go
for _, h := range []string{"Authorization", "X-Api-Key", "Anthropic-Version"} {
    req.Header.Del(h)
}
req.Header.Set("Authorization", "Bearer "+key)        // OpenAI
req.Header.Set("x-api-key", key)                      // Anthropic
req.Header.Set("anthropic-version", "2023-06-01")     // Anthropic 固定版本
```

客户端传入的 `Anthropic-Beta` 与供应商会话头（如 `x-opencode-session`）会原样透传给上游。

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
| 连接失败 / DNS | 502，有备实例则切换 |
| 建连或读超时 | 504，有备实例则切换 |
| 上游 5xx | 有备实例则切换，否则原样透传 status + body |
| 上游 429 | 有备实例则切换，否则原样透传 |
| 上游 4xx（含 401/403） | 原样透传 status + body，**不切换** |
| 流式首包后错误 | 不重试，写出错误事件并结束 |
| 模型未配置 | 404，遵循入口协议错误体 |

## 5. 并发模型

- 每个请求一个 goroutine，上游请求使用共享 `http.Transport`（连接池）。
- 超时全局默认 120s（`upstream_timeout`），单个接入点只能调低：非流式请求受 context 总超时约束；流式请求首包受 `ResponseHeaderTimeout` 约束，传输中由 idle watchdog 在超时后关闭上游连接。
- 路由快照用 `atomic.Pointer[Snapshot]`，读路径无锁。
- 熔断状态放在 `internal/health` 的长期对象里，**不进快照**：配置热重载不能把熔断计数清零。
- 网关 Key 用 `sync.RWMutex` 保护的内存缓存；SQLite 只承担启动加载、管理面写入与异步日志。
- 日志写入：有界 channel（8192）+ 单写协程 + 批量事务；满则丢弃并 `gw_log_dropped_total++`。
- 关闭顺序：停止接收新请求 → 等待在途请求（上限 30s）→ 冲刷日志队列 → 关闭 DB。

## 6. 配置热更新

```
fsnotify 或 1s mtime 轮询
      ↓
解析 + 校验（失败则保留旧配置）
      ↓
构建 Snapshot（索引、内置端点解析）
      ↓
atomic.Pointer.Store(new)
      ↓
运行时策略项就地更新（管理面开关、白名单、Key 预算）
```

上游拓扑配置来自数据库；内置端点的端点地址在构建时从代码目录解析，升级即生效。

运行期策略项（`server.admin_enabled`、`server.admin_allowed_cidrs`、`server.key_rpm`、
`server.key_max_concurrent`）随同一次重载就地生效：管理面开关与白名单存在 `Server` 的
`atomic` 字段里，Key 预算在限速器的锁内更新，因此在途请求不受影响。其余配置项只在启动时
读取，改动需要重启。

## 7. 数据库

- `instances`：上游接入点（内置 preset 或自定义端点列表，单个上游 Key 为 AES-256-GCM 密文，附支持的上游模型名与熔断参数）
- `models`：模型（对外名、策略、候选「接入点 + 模型」链）
- `model_instances`：模型与接入点的候选关系（优先级、时间窗口）
- `api_keys`
- `users`、`sessions`、`audit_logs`
- `request_logs`
- `usage_daily`
- `settings`

拓扑在首次启动时从 `config.yaml` 的 `instances` / `models` 导入，之后由控制台/管理 API 维护。旧的 `providers` 表会在启动时自动迁移成实例与模型。

上游 Key 的加密主密钥来自 `server.master_key`，未配置时自动生成在数据库目录的 `master.key`（0600）。丢失该文件将无法解密已保存的上游 Key。

## 8. 目录与构建

```bash
make web        # 构建前端到 internal/webui/dist
make build      # 编译二进制（使用已提交的前端产物）
make build-all  # 重建前端并编译
make test       # 单元测试 + race
make run        # 本地启动（读取 config.yaml）
make docker     # 构建容器镜像
make release    # 跨平台发布包
```

无 cgo：`modernc.org/sqlite`。交叉编译：

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/llmgw
```
