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

# 模型（逻辑层）：对外名 + 候选链（接入点 + 该接入点中的具体模型）
models:
  # 单接入点单模型
  - name: MiniMax-M3.1-Flash
    strategy: failover
    # 可选：对外声明的元数据，通过 /openai/v1/models 与 /anthropic/v1/models
    # 返回给客户端。留空的字段回退到内置端点的官方参数。
    metadata:
      display_name: MiniMax M3.1 Flash
      context_length: 1000000
      max_output_tokens: 512000
      capabilities: [tools, reasoning, vision, video]
      # 同一模型在某个协议端点上的能力不同时，按协议整体覆盖。
      protocols:
        anthropic:
          capabilities: [tools, reasoning, thinking, vision, video]
    instances:
      - name: minimax-cn
        upstream: MiniMax-M3.1-Flash

  # 主备：主实例失败后自动切到备份
  - name: MiniMax-M3.1-Flash[1m]
    strategy: failover
    instances:
      - name: minimax-cn
        upstream: MiniMax-M3.1-Flash
        priority: 0
      - name: minimax-cn-backup
        upstream: MiniMax-M3.1-Flash
        priority: 1

  - name: glm-4.6
    instances:
      - name: glm
        upstream: glm-4.6

  - name: deepseek-chat
    instances:
      - name: deepseek
        upstream: deepseek-chat
```

规则：

- `api_key` 支持 `${VAR}` 展开；留空表示上游不需要鉴权（如本地 Ollama）。
- `instances` / `models` 可以为空：**空拓扑也能正常启动**，网关此时不转发任何请求，
  在控制台里新增第一个上游接入点即可；写错的模型（指向不存在的接入点）仍然拒绝启动。
- 端点的 `base_url` 是上游的根：OpenAI 会拼 `/chat/completions`（通常以 `/v1` 结尾），Anthropic 会拼 `/v1/messages`（不要带 `/v1`）。
- 自定义接入点至少填一个端点；只填一个就只开放对应协议。
- 内置端点（`preset`）不能再填端点；地址由程序内置目录提供。
- 模型名全局唯一，重复会**拒绝启动**。
- 接入点的 `models` 必填；每个候选的 `upstream` 必须被该候选接入点声明，否则拒绝启动。
- `priority` 数字越小越优先；上游返回 5xx / 429 / 超时时自动切到下一个候选，4xx 不切换。
- 个别厂商把账号级问题放在 4xx 上：MiniMax 套餐额度耗尽返回 `402`，OpenCode Go 缺会话头返回 `400`。
  这类状态码可以按接入点声明在 `failover_statuses` 里（如 `[402]`），命中后同样会尝试下一个候选
  并计入熔断；默认的 429 / 5xx 不受影响，该列表只做追加。
- 上游要求会话粘性时（OpenCode Go 的 `x-opencode-session`），接入点用 `session_header` 声明头名。
  客户端没带该头时，网关按「系统提示 + 首条用户消息」派生 `ses_` + 32 位十六进制，同一对话的后续
  轮次保持一致，prompt 缓存才能命中；客户端自带则原样透传。内置端点由 preset 自带该头名。
- 熔断：接入点连续失败 `max_fails`（默认 3）次后跳过 `cooldown_secs`（默认 30 秒）。熔断状态在进程内，配置热重载不会清零。
- 代理走 `HTTP_PROXY`/`HTTPS_PROXY` 环境变量。
- 旧的 `providers` 写法仍可用，加载时自动展开成一个接入点加若干模型；数据库中的旧供应商会在启动时自动迁移。

### 模型元数据与预设兜底

模型可以声明 `metadata`，它是可选的，整体替换：

| 字段 | 含义 |
|---|---|
| `display_name` | 客户端展示的名称 |
| `context_length` | 上下文窗口（输入 + 输出 token 上限） |
| `max_output_tokens` | 单次最大输出 token 数 |
| `capabilities` | 能力标签：`tools` / `vision` / `video` / `audio` / `reasoning` / `json` / `cache` / `search` / `doc` |
| `protocols` | 按 `openai` / `anthropic` 覆盖前三个字段 |

- 未填写的字段按候选链顺序回退到内置端点的官方参数；自定义接入点不兜底。
- 显式值优先；`capabilities` 留空表示继承，非空表示整体替换（不追加）。
- 控制台的「模型」编辑页与 `POST/PUT /admin/api/models` 使用同一套字段。

内置兜底数据集（按厂商官方文档核实，2026-11-24；厂商调整后需要同步更新 `internal/preset/catalog.go`）：

| 上游模型 | 上下文 | 最大输出 | 能力 |
|---|---|---|---|
| `glm-5.3` | 1,048,576 | 131,072 | tools, reasoning, json, cache |
| `glm-5.3-flash` | 1,048,576 | 131,072 | tools, reasoning, json, cache, vision, video, doc |
| `kimi-k3` / `kimi-k3.1-pro` | 1,048,576 | 131,072 | tools, reasoning, json, cache, vision, video |
| `deepseek-v4-pro` | 1,048,576 | 393,216 | tools, reasoning, json, cache |
| `MiniMax-M3.1-Flash` | 1,000,000 | 512,000 | tools, reasoning, vision, video |
| `mimo-v2.6-pro` | 1,048,576 | 131,072 | tools, reasoning, json, cache, search, vision, video, audio |
| `LongCat-2.5` | 1,048,576 | 262,144 | tools, reasoning, vision, video |
| `doubao-seed-2.1-pro` | 262,144 | 262,144 | tools, reasoning, json, cache, vision, video |
| `doubao-seed-evolving` | 1,048,576 | 262,144 | tools, reasoning, json, cache, doc, vision, video |
| `ark-code-latest` | — | — | 控制台动态别名，参数随所选模型变化，不提供兜底 |

`kimi-k3` 系列官方允许把输出上限设到 1,048,576，但默认是 131,072；兜底采用默认值，避免客户端按最大值预留预算。
`MiniMax-M3.1-Flash` 官方只公布了上下文窗口，512,000 的输出上限来自 models.dev。

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

若数据库中没有网关 Key，会生成一个并以如下形式打印：

```
[BOOTSTRAP] first gateway key: sk-gw-xxxxxxxx
```

该 Key 也会加密入库，登录控制台后可在「网关 Key」页回显并复制。

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
GET /metrics    Prometheus（需 server.metrics_token，否则接口关闭）
```

关键指标：

| 指标 | 含义 |
|---|---|
| `gw_requests_total{protocol,status}` | 请求量与状态码分布 |
| `gw_request_duration_seconds` | 端到端延迟 |
| `gw_ttft_seconds{protocol}` | 首 token 延迟 |
| `gw_tokens_total{protocol,kind}` | token 消耗；`kind` 为 `prompt`、`completion`、`cache_read`、`cache_write` |
| `gw_upstream_errors_total{instance}` | 上游错误（按上游接入点） |
| `gw_log_dropped_total` | 日志队列溢出丢条数 |

未设置 `server.metrics_token` 时，`/metrics` **整个接口关闭**，任何来源都返回 `404`。
要抓取指标必须先设置令牌，之后以 `Authorization: Bearer <token>` 或 `?token=<token>` 访问；
令牌不对返回 `401`。改配置后 1 秒内热加载生效，无需重启。

### 判断 prompt 缓存命中

请求日志页的「缓存」列直接显示每次请求：`命中 X%`（附读/写 token）、`写入 N`、`未命中`，或 `-`（上游未返回 usage，信息不可用）。概览页会汇总所选区间的缓存读/写 token；按日累计从升级后开始，升级前的历史行为 0。

也可以直接查库做聚合：

```sql
SELECT model, ingress_protocol,
       SUM(cache_read_tokens) AS cache_read,
       SUM(cache_write_tokens) AS cache_write
FROM request_logs
WHERE status >= 200 AND status < 300 AND usage_estimated = 0
  AND ts >= strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-7 day')
GROUP BY model, ingress_protocol;
```

命中率按入口协议分开计算：Anthropic 用 `cache_read / (prompt + cache_read + cache_write)`，OpenAI 用 `cache_read / prompt_tokens`（`prompt_tokens` 已包含 cached tokens）。OpenAI 兼容上游如果返回 `prompt_cache_hit_tokens`（DeepSeek 风格），网关也会识别。

日志为 JSON，一行一条，`request_id` 可与响应头 `X-Request-Id` 对应。

## 6. 备份与升级

- 备份：停机或 `sqlite3 data/llmgw.db ".backup backup.db"`。WAL 模式下不要直接 `cp` 正在写入的库。
- **同时备份 `master.key`**（或配置里的 `server.master_key`）。丢失它，数据库中已加密的上游 Key 将无法恢复。
- 升级：替换二进制后重启。配置为热加载，但二进制升级需要重启。
- 回滚：保留上一个二进制与 `config.yaml`。
- **自动迁移**：新版本打开旧库时会执行 `ALTER TABLE ... ADD COLUMN`（如 `model_instances.windows_json`，`TEXT NOT NULL DEFAULT '[]'`；以及 `instances.allow_private`，`INTEGER NOT NULL DEFAULT 0`）。
  SQLite 加列只改表结构、不重写行数据，持锁时间与表规模无关（毫秒级），大库也不会长时间阻塞写请求；但升级前仍建议先 `.backup` 留一份快照。
  本版本还会一次性为 `api_keys` 补齐治理列：`rpm_limit` / `max_concurrent`（`INTEGER NOT NULL DEFAULT 0`）、`allowed_models_json`（`TEXT NOT NULL DEFAULT '[]'`）以及可空的 `expires_at`（NULL 表示永不过期）。存量 Key 因此沿用“不限流、不限模型、不过期”的旧行为；`expires_at` 保持可空是为了区分“从未设置”和“设置后清除”，其余列使用常量默认值避免启动时回填大表。
  所有 `SELECT` 都显式列出列名，旧二进制可以打开迁移后的库继续读写原有字段；要彻底回退，停机后用升级前的快照覆盖 `data/llmgw.db`（并保留 `master.key`），再换回旧二进制即可。
  迁移失败时网关启动即报错退出，不会带病运行。
- **迁移后的回退路径**：旧二进制不认识新列，但 SQLite 会忽略未使用的列，旧版本可以直接打开迁移后的库继续工作（只是不再使用窗口数据）。
  要彻底还原，用升级前的快照覆盖 `data/llmgw.db`（并保留 `master.key`），再换回旧二进制即可。
- **移除成本统计的升级注意**（本版本起网关不再估算请求成本）：
  - 配置文件里若仍有 `pricing:` 段，严格解码会拒绝启动与热加载并提示该段已移除，升级前先删除。
  - 新版本打开旧库时会执行 `ALTER TABLE ... DROP COLUMN cost_usd`（`request_logs` 与 `usage_daily`），
    历史成本数据被永久删除，旧二进制无法再读回该列；需要保留历史成本时，升级前先 `.backup`，回滚用快照覆盖 `data/llmgw.db`。
- **删掉 `instances.allow_private` 列**（要彻底回退这一列时，先停机并备份数据库）：
  - SQLite 3.35 及以上直接删列：

    ```sql
    ALTER TABLE instances DROP COLUMN allow_private;
    ```

  - 更老的 SQLite 没有 `DROP COLUMN`，重建表（行与 id 原样保留）：

    ```sql
    BEGIN;
    CREATE TABLE instances_new (
        id              INTEGER PRIMARY KEY AUTOINCREMENT,
        name            TEXT    NOT NULL UNIQUE,
        preset          TEXT    NOT NULL DEFAULT '',
        key_enc         TEXT    NOT NULL DEFAULT '',
        quota_cred_enc  TEXT    NOT NULL DEFAULT '',
        endpoints_json  TEXT    NOT NULL DEFAULT '[]',
        models_json     TEXT    NOT NULL DEFAULT '[]',
        max_fails       INTEGER NOT NULL DEFAULT 3,
        cooldown_secs   INTEGER NOT NULL DEFAULT 30,
        timeout_secs    INTEGER NOT NULL DEFAULT 0,
        created_at      TEXT    NOT NULL,
        updated_at      TEXT    NOT NULL
    );
    INSERT INTO instances_new
        SELECT id, name, preset, key_enc, quota_cred_enc, endpoints_json,
               models_json, max_fails, cooldown_secs, timeout_secs,
               created_at, updated_at
        FROM instances;
    DROP TABLE instances;
    ALTER TABLE instances_new RENAME TO instances;
    COMMIT;
    ```

  删列后旧二进制照常工作；新二进制会在下次启动时把这一列重新加回来（存量行默认 0），
  所以要真正停用这个特性，还要把实例配置里的 `allow_private_upstream` 一并去掉。
- **保留清理不可逆**：超过 `database.retention_days` 的请求日志、审计日志与按日用量会被直接删除，没有回收站。
  打开这个选项前先备份数据库（`sqlite3 data/llmgw.db ".backup backup.db"`，备份文件权限收紧到 0600），
  并核对天数符合预期；第一次清理在启动约 30 秒后执行。误删后只能用备份恢复
  （`sqlite3 data/llmgw.db ".restore backup.db"` 或覆盖 `data/llmgw.db`），网关本身无法撤销。

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
  secure_cookie: true                 # 反代没传 X-Forwarded-Proto 时也能锁死 Cookie
```

只有来自 `trusted_proxies` 的 `X-Forwarded-Proto: https` 才会被采信，它会让会话 Cookie
带上 `Secure` 并触发 HSTS 响应头；客户端自己伪造这个头不会生效。
反代务必用 `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` 追加真实地址，
否则登录限速与审计里的来源 IP 不可信。

### 9.3 其他

- 网关 Key 仅存 SHA-256 哈希，明文只在创建时返回一次；泄露后请在控制台删除并重新创建。
- 网关 Key 在进程内缓存，创建/停用/删除立即生效；启动时从数据库加载。
- 上游 Key 来自环境变量或控制台，AES-256-GCM 加密后写入数据库，不出现在任何 API 响应中（仅返回末四位掩码）。
- 加密主密钥来自 `server.master_key` 或数据库目录的 `master.key`（0600）。请与数据库一起备份，并限制文件权限。
- `/metrics` 在未配置 `server.metrics_token` 时整个接口关闭（`404`）；公网抓取请设令牌，访问时带 `Authorization: Bearer <token>`。
- 管理面可用 `server.admin_allowed_cidrs` 限制到运维网段，控制台 API 与登录接口一并生效。
- 怀疑控制台被拿下时，把 `server.admin_enabled` 改成 `false`：1 秒内热加载生效，
  控制台 API、登录接口与控制台页面全部返回 `403`，数据面不受影响（`/metrics` 由
  `server.metrics_token` 单独控制）。改回 `true` 立即恢复，已登录的会话继续有效。
  这是应急拉闸手段，不要当常规开关用。
- 数据面按 Key 限速：`server.key_rpm`（每分钟请求数）与 `server.key_max_concurrent`（并发上限），
  默认示例值 600 / 32；两者都为 0 表示不限制，公网部署不建议留空。
- 转发到上游的请求头会剥离 `Cookie`、`Authorization`、`X-Forwarded-*`、`X-Real-IP` 等敏感头
  （控制台与数据面同源，浏览器会把管理会话 Cookie 带到 API 请求上）；其余厂商需要的头照常透传。
  上游响应里的 `Set-Cookie`、CSP、HSTS 等头会被丢弃，避免上游覆盖网关自身的安全头。
- 上游地址默认禁止 loopback、内网与云元数据网段，防止控制台被拿下后成为内网代理；
  本地 mock 需在实例上设置 `allow_private_upstream: true`。控制台「上游接入点」页的
  自定义端点表单里有「允许内网地址」开关，勾上的接入点在卡片上带「允许内网」标记；
  只在可信的内网上游上打开。
- 请求日志、审计日志与按日用量可用 `database.retention_days` 自动清理（0 表示永久保留）。
  清理分批进行（每批 5000 行），批与批之间释放写锁，不会长时间阻塞异步日志写入；
  启动后约 30 秒执行第一次清理，之后每 24 小时一次。
  清理不可逆：打开这个选项前先按第 6 节备份数据库，确认天数无误再开。
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

