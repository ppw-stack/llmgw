# 运维手册

## 1. 构建

```bash
make build            # 产出 bin/llmgw
make test             # 单元测试 + race
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build
```

无 cgo 依赖（SQLite 使用 `modernc.org/sqlite`）。

## 2. 配置

`config.yaml`：

```yaml
server:
  addr: ":8080"
  admin_token: "${LLMGW_ADMIN_TOKEN}"   # 留空则首次启动自动生成并打印
  shutdown_timeout: 30s
  max_body_bytes: 33554432

database:
  path: "./data/llmgw.db"

log:
  level: info
  format: json

providers:
  - name: glm
    keys:
      - "${GLM_KEY_1}"
      - "${GLM_KEY_2}"
    timeout: 120s
    endpoints:
      - name: openai
        protocol: openai
        base_url: https://open.bigmodel.cn/api/paas/v4
        models:
          glm-4.6: glm-4.6
      - name: anthropic
        protocol: anthropic
        base_url: https://open.bigmodel.cn/api/anthropic
        version: "2023-06-01"
        models:
          glm-4.6: glm-4.6

  - name: deepseek
    keys: ["${DEEPSEEK_KEY}"]
    endpoints:
      - name: openai
        protocol: openai
        base_url: https://api.deepseek.com/v1
        models:
          deepseek-chat: deepseek-chat

# 可选：用于成本估算，单位美元 / 百万 token
pricing:
  glm-4.6:       { input_per_mtok: 0.6, output_per_mtok: 2.2 }
  deepseek-chat: { input_per_mtok: 0.27, output_per_mtok: 1.1 }
```

规则：

- `keys` 支持 `${VAR}` 展开；留空表示上游不需要鉴权（如本地 Ollama）。
- `base_url` 是上游的根，OpenAI 会拼 `/chat/completions`，Anthropic 会拼 `/v1/messages`。
- 同一个 Provider 下可以同时挂两个协议的端点，共享 `keys`。
- 同一协议内模型名重复会**拒绝启动**并打印两个来源。

## 3. 首次启动

```
./bin/llmgw -config config.yaml
```

若数据库中没有网关 Key，会生成一个并以如下形式打印一次：

```
[BOOTSTRAP] first gateway key created: sk-gw-xxxxxxxx   (save it now; it is not shown again)
```

随后即可：

```bash
curl $GW/openai/v1/chat/completions \
  -H "Authorization: Bearer sk-gw-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-4.6","messages":[{"role":"user","content":"hi"}]}'
```

## 4. 客户端接入

| 客户端 | 填写 |
|---|---|
| OpenAI SDK | `base_url = https://gw.example.com/openai/v1` |
| Codex CLI / Cursor / Cline(OpenAI Compatible) | 同上 |
| Anthropic SDK | `base_url = https://gw.example.com/anthropic` |
| Claude Code | `ANTHROPIC_BASE_URL=https://gw.example.com/anthropic` |
| Cline(Anthropic 模式) | 同上 |

Key 一律填网关 Key（`sk-gw-` 开头），上游 Key 永远不出网关。

## 5. 可观测

```
GET /healthz    存活
GET /readyz     就绪（配置已加载 + DB 可写）
GET /metrics    Prometheus
```

关键指标：

| 指标 | 含义 |
|---|---|
| `gw_requests_total{protocol,status}` | 请求量与状态码分布 |
| `gw_request_duration_seconds` | 端到端延迟 |
| `gw_ttft_seconds{protocol}` | 首 token 延迟 |
| `gw_tokens_total{protocol,kind}` | token 消耗 |
| `gw_upstream_errors_total{provider}` | 上游错误 |
| `gw_log_dropped_total` | 日志队列溢出丢条数 |

日志为 JSON，一行一条，`request_id` 可与响应头 `X-Request-Id` 对应。

## 6. 备份与升级

- 备份：停机或 `sqlite3 data/llmgw.db ".backup backup.db"`。WAL 模式下不要直接 `cp` 正在写入的库。
- 升级：替换二进制后重启。配置为热加载，但二进制升级需要重启。
- 回滚：保留上一个二进制与 `config.yaml`。

## 7. 常见故障

| 现象 | 排查 |
|---|---|
| `404 model_not_found` | 模型未在对应协议的端点中声明；查 `GET /admin/api/providers` |
| `400 ... only available via the Anthropic endpoint` | 客户端 base_url 用错，改用 `/anthropic` |
| `401 invalid gateway key` | Key 停用/删除；重新创建 |
| `502 upstream unreachable` | 上游域名/DNS/代理问题；查看日志 `upstream_error` |
| `504 upstream timeout` | 调大 provider `timeout`，或检查上游 |
| Anthropic 请求 400 `anthropic-version` | 在端点配置 `version`（默认 `2023-06-01`） |
| 流式无输出 | 检查是否被中间反代缓冲；关闭 nginx `proxy_buffering` |

## 8. 反向代理注意事项

若前置 nginx，必须：

```nginx
proxy_buffering off;
proxy_read_timeout 600s;
proxy_set_header X-Accel-Buffering no;
```

否则 SSE 会被缓冲成一次性返回。

## 9. 安全

- 网关 Key 仅存 SHA-256 哈希，明文只在创建时返回一次。
- 上游 Key 来自环境变量，不写入数据库、不出现在任何 API 响应中。
- 管理接口需要 `admin_token`；建议只在内网或经反代鉴权暴露。
- 生产环境请置于 TLS 之后（Caddy/nginx/云 LB）。
