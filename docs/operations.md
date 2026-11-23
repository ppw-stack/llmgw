# 运维手册

## 1. 构建

```bash
make build            # 只编译 Go，使用已提交的前端产物
make build-all        # 重建前端并编译
make test             # 单元测试 + race
make release          # 四个平台的发布包
```

交叉编译（无 cgo）：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build
```

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

# 上游调用超时（全局默认，单个接入点只能调低）
upstream_timeout: 120s

# 上游接入点（物理层）：一个账号 + 它提供的协议入口 + 熔断参数
instances:
  # 内置端点：端点地址由程序内置，升级自动更新
  # 国内用 minimax-mplan-cn，国际用 minimax-mplan-global（两者 Key 不通用）
  # 另外还有 opencode-go（OpenCode Go 订阅）、ark-coding-plan-cn（火山方舟 Coding Plan）
  - name: minimax-cn
    preset: minimax-mplan-cn
    api_key: "${MINIMAX_KEY}"
    models: [MiniMax-M3.1-Flash]

  # 同一个上游的第二个账号，作为主实例的备份
  - name: minimax-cn-backup
    preset: minimax-mplan-cn
    api_key: "${MINIMAX_KEY_BACKUP}"
    models: [MiniMax-M3.1-Flash]

  # 自定义端点：一个 Key + 1~2 个端点
  - name: glm
    api_key: "${GLM_KEY}"
    endpoints:
      - protocol: openai
        base_url: https://open.bigmodel.cn/api/coding/paas/v4
      - protocol: anthropic
        base_url: https://open.bigmodel.cn/api/anthropic
    models: [glm-4.6]

  - name: deepseek
    api_key: "${DEEPSEEK_KEY}"
    endpoints:
      - protocol: openai
        base_url: https://api.deepseek.com/v1
    models: [deepseek-chat]

# 模型（逻辑层）：对外名 + 上游名 + 候选接入点链
models:
  # 单实例
  - name: MiniMax-M3.1-Flash
    upstream: MiniMax-M3.1-Flash
    strategy: failover
    instances:
      - name: minimax-cn

  # 主备：主实例失败后自动切到备份
  - name: MiniMax-M3.1-Flash[1m]
    upstream: MiniMax-M3.1-Flash
    strategy: failover
    instances:
      - name: minimax-cn
        priority: 0
      - name: minimax-cn-backup
        priority: 1

  - name: glm-4.6
    upstream: glm-4.6
    instances:
      - name: glm

  - name: deepseek-chat
    upstream: deepseek-chat
    instances:
      - name: deepseek

# 可选：用于成本估算，单位美元 / 百万 token
pricing:
  MiniMax-M3.1-Flash: { input_per_mtok: 0.3, output_per_mtok: 1.2 }
  glm-4.6:            { input_per_mtok: 0.6, output_per_mtok: 2.2 }
  deepseek-chat:      { input_per_mtok: 0.27, output_per_mtok: 1.1 }
```

规则：

- `api_key` 支持 `${VAR}` 展开；留空表示上游不需要鉴权（如本地 Ollama）。
- 端点的 `base_url` 是上游的根：OpenAI 会拼 `/chat/completions`（通常以 `/v1` 结尾），Anthropic 会拼 `/v1/messages`（不要带 `/v1`）。
- 自定义接入点至少填一个端点；只填一个就只开放对应协议。
- 内置端点（`preset`）不能再填端点；地址由程序内置目录提供。
- 模型名全局唯一，重复会**拒绝启动**。
- 接入点的 `models` 必填；模型的上游名必须被每个候选接入点声明，否则拒绝启动。
- `priority` 数字越小越优先；上游返回 5xx / 429 / 超时时自动切到下一个候选，4xx 不切换。
- 熔断：接入点连续失败 `max_fails`（默认 3）次后跳过 `cooldown_secs`（默认 30 秒）。熔断状态在进程内，配置热重载不会清零。
- 代理走 `HTTP_PROXY`/`HTTPS_PROXY` 环境变量。
- 旧的 `providers` 写法仍可用，加载时自动展开成一个接入点加若干模型；数据库中的旧供应商会在启动时自动迁移。

## 3. 首次启动

```
./bin/llmgw -config config.yaml
```

若数据库中没有管理员用户，会创建一个并生成随机密码（仅打印一次，同时写入 `data/admin-password.txt`，权限 0600）：

```
[BOOTSTRAP] admin username: admin
[BOOTSTRAP] admin password: <generated>
[BOOTSTRAP] also written to ./data/admin-password.txt
```

若数据库中没有网关 Key，会生成一个并以如下形式打印一次：

```
[BOOTSTRAP] first gateway key: sk-gw-xxxxxxxx   (save it now; it is not shown again)
```

浏览器打开 `http://<host>:8080/`，用用户名与密码登录；登录成功后下发 HttpOnly 会话 Cookie，默认 12 小时过期。

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
| `gw_upstream_errors_total{instance}` | 上游错误（按上游接入点） |
| `gw_log_dropped_total` | 日志队列溢出丢条数 |

`/metrics` 默认公开。公网部署时设置 `server.metrics_token`，之后需以
`Authorization: Bearer <token>` 或 `?token=<token>` 访问；也可在反代层限制来源 IP。

日志为 JSON，一行一条，`request_id` 可与响应头 `X-Request-Id` 对应。

## 6. 备份与升级

- 备份：停机或 `sqlite3 data/llmgw.db ".backup backup.db"`。WAL 模式下不要直接 `cp` 正在写入的库。
- **同时备份 `master.key`**（或配置里的 `server.master_key`）。丢失它，数据库中已加密的上游 Key 将无法恢复。
- 升级：替换二进制后重启。配置为热加载，但二进制升级需要重启。
- 回滚：保留上一个二进制与 `config.yaml`。
- **自动迁移**：新版本打开旧库时会执行 `ALTER TABLE ... ADD COLUMN`（如 `model_instances.windows_json`，`TEXT NOT NULL DEFAULT '[]'`）。
  SQLite 加列只改表结构、不重写行数据，持锁时间与表规模无关（毫秒级），大库也不会长时间阻塞写请求；但升级前仍建议先 `.backup` 留一份快照。
  迁移失败时网关启动即报错退出，不会带病运行。
- **迁移后的回退路径**：旧二进制不认识新列，但 SQLite 会忽略未使用的列，旧版本可以直接打开迁移后的库继续工作（只是不再使用窗口数据）。
  要彻底还原，用升级前的快照覆盖 `data/llmgw.db`（并保留 `master.key`），再换回旧二进制即可。

## 7. 常见故障

| 现象 | 排查 |
|---|---|
| `404 model_not_found` | 模型未声明；查 `GET /admin/api/models` |
| `400 ... only available via the Anthropic endpoint` | 客户端 base_url 用错，改用 `/anthropic` |
| `401 invalid gateway key` | Key 停用/删除；重新创建 |
| `502 upstream unreachable` | 上游域名/DNS/代理问题；查看日志 `upstream_error` |
| `504 upstream timeout` | 调大 `upstream_timeout`，或调低该接入点的 `timeout_secs` |
| `503 no upstream instance is currently available` | 所有候选接入点都在熔断冷却期内；看日志 `skip upstream instance in cooldown` |
| 请求变慢且日志里有 `trying the next candidate` | 主接入点持续失败；检查主实例健康，或调低其 `timeout_secs` |
| Anthropic 请求 400 `anthropic-version` | 网关固定发送 `2023-06-01`；确认上游支持该版本 |
| 流式无输出 | 检查是否被中间反代缓冲；关闭 nginx `proxy_buffering` |

## 8. 反向代理注意事项

若前置 nginx，必须：

```nginx
proxy_buffering off;
proxy_read_timeout 600s;
proxy_set_header X-Accel-Buffering no;
```

否则 SSE 会被缓冲成一次性返回。

## 9. 安全与公网部署

### 9.1 管理台认证

- 管理员账号存在 `users` 表，密码使用 bcrypt 哈希。
- 登录成功后下发 `llmgw_session` Cookie：`HttpOnly`、`SameSite=Strict`，走 HTTPS 时自动带 `Secure`，默认 12 小时过期（`server.session_ttl`）。
- 登录失败按客户端 IP 限速：5 分钟内最多 10 次，超出返回 `429`；同时最多 4 个登录请求并发执行 bcrypt。
- 修改密码后该用户的所有会话立即失效；通过 `server.admin_password` 重置密码同样会撤销全部会话。
- 过期会话在启动时清理，并每小时清理一次。
- 管理操作（登录、登出、改密、Key 增删改）写入 `audit_logs`，可在控制台或 `GET /admin/api/audit` 查看。
- `server.admin_token` 仅作为脚本/兼容用途的可选 Bearer token；不配置即禁用。

### 9.2 反向代理要求

推荐由 nginx/Caddy/云 LB 终止 TLS。若使用 nginx：

```nginx
server {
    listen 443 ssl http2;
    server_name gw.example.com;
    ssl_certificate     /etc/ssl/gw.crt;
    ssl_certificate_key /etc/ssl/gw.key;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # SSE 必须关闭缓冲，否则流式会被攒成一次性返回
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 600s;
        proxy_set_header X-Accel-Buffering no;
    }
}
```

然后配置：

```yaml
server:
  trusted_proxies: ["127.0.0.1/32"]   # 反代地址；只有它传来的 XFF 才会被信任
```

`X-Forwarded-Proto: https` 会让会话 Cookie 自动带上 `Secure`，并触发 HSTS 响应头。

### 9.3 其他

- 网关 Key 仅存 SHA-256 哈希，明文只在创建时返回一次；泄露后请在控制台删除并重新创建。
- 网关 Key 在进程内缓存，创建/停用/删除立即生效；启动时从数据库加载。
- 上游 Key 来自环境变量或控制台，AES-256-GCM 加密后写入数据库，不出现在任何 API 响应中（仅返回末四位掩码）。
- 加密主密钥来自 `server.master_key` 或数据库目录的 `master.key`（0600）。请与数据库一起备份，并限制文件权限。
- `/metrics` 未内置鉴权；公网暴露时请在反代层限制来源 IP 或加 Basic Auth。
- 控制台已设置 CSP、`X-Frame-Options: DENY`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`。
- 建议定期轮换管理员密码（控制台“修改密码”）与上游 Key。

## 10. Docker 与发布

### 10.1 镜像

```bash
make docker          # 构建 llmgw:<version>
# 等价于
docker build -t llmgw:local .
```

三阶段构建：`node:24-alpine` 构建控制台 → `golang:1.27-alpine` 静态编译 → `alpine:3.22` 运行时。

运行时特性：

- 非 root 用户 `llmgw`，`/data` 为数据卷；
- 内置 `HEALTHCHECK CMD ["llmgw","-healthcheck"]`，探针不依赖 curl；
- 二进制静态链接，无 cgo；
- 配置默认读取 `/etc/llmgw/config.yaml`。

### 10.2 compose 部署

```bash
cp config.example.yaml config.yaml
# 修改 database.path: "/data/llmgw.db"
# 上游 Key 用环境变量注入
export GLM_KEY_1=xxx
docker compose up -d --build
docker compose logs -f llmgw
```

首次启动会把管理员密码写入 `/data/admin-password.txt`（容器内），可通过：

```bash
docker compose exec llmgw cat /data/admin-password.txt
```

compose 默认启用 `read_only: true`、`no-new-privileges`、独立数据卷 `llmgw-data`。

### 10.3 发布产物

```bash
make release
```

生成四个平台包与校验和：

```
dist/llmgw_<version>_darwin_arm64.tar.gz
dist/llmgw_<version>_darwin_amd64.tar.gz
dist/llmgw_<version>_linux_arm64.tar.gz
dist/llmgw_<version>_linux_amd64.tar.gz
dist/checksums.txt
```

每个包内含二进制、`config.example.yaml`、`README.md`。

### 10.4 健康检查

```bash
llmgw -healthcheck                    # 默认探测 http://127.0.0.1:8080/healthz
LLMGW_HEALTHCHECK_URL=http://127.0.0.1:9000/healthz llmgw -healthcheck
```

退出码 0 表示健康。修改了监听端口时，在容器环境变量里覆盖 URL。

