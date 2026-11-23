# 管理 API

所有管理接口以 `/admin/api` 为前缀。

认证方式：

- `POST /admin/api/login`：用户名 + 密码，成功后下发 `llmgw_session` Cookie（HttpOnly / SameSite=Strict / 12h）。
- 其余接口需要有效会话 Cookie。
- 若配置了 `server.admin_token`，也接受 `Authorization: Bearer <token>`（用于脚本与旧版本兼容）。

浏览器之外的调用示例：

```bash
curl -c cookies.txt -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}' \
  https://gw.example.com/admin/api/login

curl -b cookies.txt https://gw.example.com/admin/api/keys
```

## 认证

> 整个管理面（`/admin/api/*`，含登录接口）可以被关掉：配置 `server.admin_enabled: false`
> 后，这些接口与控制台页面一律返回 `403 the management console is disabled by configuration`，
> 数据面不受影响。该开关支持热加载，改配置 1 秒内生效。

### 登录

```
POST /admin/api/login
{"username":"admin","password":"..."}

200
{"username":"admin","expires_at":"2026-11-22T21:13:24Z"}

Set-Cookie: llmgw_session=...; Path=/; HttpOnly; Secure; SameSite=Strict; Max-Age=43200
```

失败：`401 invalid username or password`；连续失败超限：`429` + `Retry-After`。

### 登出

```
POST /admin/api/logout   → 204
```

### 当前用户

```
GET /admin/api/me
200 {"username":"admin","api_token":false}
```

### 修改密码

```
POST /admin/api/password
{"old_password":"...","new_password":"..."}   → 204
```

成功后该用户全部会话失效，需要重新登录。

### 审计日志

```
GET /admin/api/audit?limit=100
200 [{"ts":"...","actor":"admin","action":"login","target":"admin",
      "ip":"203.0.113.9","user_agent":"...","result":"success"}]
```

`action` 取值：`login`、`logout`、`password_change`、`key.create`、`key.update`、`key.delete`、`instance.create`、`instance.update`、`instance.delete`、`model.create`、`model.update`、`model.delete`。

## 网关 Key

### 创建

```
POST /admin/api/keys
{"name": "laptop"}

201
{
  "id": 1,
  "name": "laptop",
  "key": "sk-gw-3f9c...",      // 仅此一次返回明文
  "prefix": "sk-gw-3f9",
  "enabled": true,
  "created_at": "2026-11-22T10:00:00Z"
}
```

### 列表

```
GET /admin/api/keys
200 [{ "id":1, "name":"laptop", "prefix":"sk-gw-3f9", "enabled":true,
       "last_used_at":"...", "created_at":"..." }]
```

### 启停

```
PATCH /admin/api/keys/1
{"enabled": false}
```

### 删除

```
DELETE /admin/api/keys/1    → 204
```

## 上游接入点与模型

上游拓扑保存在 SQLite 中，上游 Key 使用 AES-256-GCM 加密。控制台分「上游接入点」和「模型」两页维护，保存后立即生效。

分成两层的原因：一个账号常常能提供多个模型，一个模型也常常由多个账号提供 —— 这是多对多关系。因此：

- **上游接入点（instance）** 是物理层：一个账号 + 它提供的协议入口 + 熔断参数。
- **模型（model）** 是逻辑层：对外模型名、上游模型名、分流策略、候选接入点链。

模型名全局唯一；同一模型可以挂多个接入点，按 `priority` 组成主备链。

### 内置端点目录

```
GET /admin/api/provider-presets

200 [{
  "id": "minimax-mplan-global",
  "family": "minimax-mplan",
  "family_name": "MiniMax 编码套餐",
  "variant_name": "国际",
  "default_name": "minimax",
  "docs_url": "https://platform.minimax.io/docs/m-plan/other-tools",
  "openai_base_url": "https://api.minimax.io/v1",
  "anthropic_base_url": "https://api.minimax.io/anthropic",
  "models": ["MiniMax-M3.1-Flash"]
}]
```

目录同时提供 `minimax-mplan-cn`（`https://api.minimax.cn/v1` 与 `https://api.minimax.cn/anthropic`），默认名称 `minimax-cn`。国内与国际套餐的 Key 不通用。

另外两个内置端点：

| id | 名称 | OpenAI Base URL | Anthropic Base URL |
|---|---|---|---|
| `opencode-go` | OpenCode Go | `https://opencode.ai/inference/go/openai/v1` | `https://opencode.ai/inference/go/anthropic` |
| `ark-coding-plan-cn` | 火山方舟（Coding Plan） | `https://ark.cn-beijing.volces.com/api/coding/v3` | `https://ark.cn-beijing.volces.com/api/coding` |

网关在 OpenAI 协议上请求 `<base>/chat/completions`，在 Anthropic 协议上请求 `<base>/v1/messages`，所以同一家厂商的两个 Base URL 不一定只差一段路径。

OpenCode Go 与 MiniMax 编码套餐直接用上游 Key 查额度；火山方舟 Coding Plan 需要在接入点上另填一组访问密钥，见「[套餐额度](#套餐额度)」。

使用内置端点时要注意上游自己的要求：

- `opencode-go`：上游强制要求每个请求带 `x-opencode-session`，缺失直接返回 `400 MissingSessionID`。网关原样透传客户端请求头，Claude Code / Codex / ZCode / Pi / Hermes 这类自带会话头的客户端可直接使用；裸 SDK 客户端需要自己带上这个头，值必须在同一对话内保持稳定。
- `ark-coding-plan-cn`：只有 `/api/coding` 系列的地址才扣套餐额度，`/api/v3` 不扣额度而是另外计费；套餐额度也只在编程 / Agent 类工具中生效。

`models` 是该预设的建议模型名，可以在创建接入点时改。OpenCode Go 走 `/responses` 与 Google 端点的模型（GPT、Grok、Muse Spark、Gemini）不在建议列表里——本网关只提供 chat/completions 与 Anthropic Messages 两个入口。

#### 查询上游可用模型

创建内置端点时，可以用待保存的 Key 向上游模型列表接口查询候选模型。该接口只用于本次查询，Key 不会落库。

```
POST /admin/api/preset-models
{
  "preset": "opencode-go",
  "api_key": "sk-..."
}

200 {
  "models": [
    {"id": "glm-5.3", "protocols": ["openai", "anthropic"]},
    {"id": "claude-haiku-5-5", "protocols": ["anthropic"]}
  ],
  "suggested": ["glm-5.3", "..."],
  "warning": ""
}
```

- `models` 是上游返回、且声明支持 `chat/completions` 或 `messages` 的模型。`protocols` 来自上游元数据，为空表示上游未提供该信息。
- `suggested` 是预设的静态建议列表，查询失败时作为兜底。
- 查询失败、超时或没有可用模型时仍返回 `200`，`models` 为空并在 `warning` 中说明原因。
- 不同预设的列表含义不同：
  - `opencode-go`：套餐内模型，可直接使用。
  - `minimax-*`：上游账号可见模型，可能包含已下线模型（例如 `MiniMax-M2`）。
  - `ark-coding-plan-cn`：上游账号可见模型，**不保证在 Coding Plan 套餐内**；不在套餐内的模型调用时返回 `404 UnsupportedModel`。
- 内置端点只支持预设目录里的 `preset`；自定义端点不提供该查询。

### 上游接入点

#### 列表

```
GET /admin/api/instances

200 [{
  "id": 1,
  "name": "minimax",
  "preset": "minimax-mplan-global",
  "preset_name": "MiniMax 编码套餐（国际）",
  "source": "builtin",
  "docs_url": "https://platform.minimax.io/docs/m-plan/other-tools",
  "key_hint": "****9999",
  "endpoints": [
    {"protocol": "openai", "base_url": "https://api.minimax.io/v1"},
    {"protocol": "anthropic", "base_url": "https://api.minimax.io/anthropic"}
  ],
  "models": ["MiniMax-M3.1-Flash"],
  "max_fails": 3,
  "cooldown_secs": 30,
  "timeout_secs": 0,
  "allow_private_upstream": false,
  "created_at": "2026-11-22T10:00:00Z",
  "updated_at": "2026-11-22T10:00:00Z"
}]
```

上游 Key 只返回末四位掩码，永不返回明文。`timeout_secs` 为 `0` 表示使用全局 `upstream_timeout`。`models` 是该接入点**声明支持的上游模型名**（必填），不是"当前被哪些模型引用"——后者由客户端从 `GET /admin/api/models` 自行 join。

#### 创建

内置端点只需 `preset + api_key`：

```
POST /admin/api/instances
{
  "name": "minimax",
  "preset": "minimax-mplan-global",
  "api_key": "sk-cp-...",
  "models": ["MiniMax-M3.1-Flash"]
}
```

自定义接入点手填端点：

```
POST /admin/api/instances
{
  "name": "glm",
  "api_key": "sk-real-upstream-key",
  "endpoints": [
    {"protocol": "openai", "base_url": "https://open.bigmodel.cn/api/coding/paas/v4"},
    {"protocol": "anthropic", "base_url": "https://open.bigmodel.cn/api/anthropic"}
  ],
  "models": ["glm-4.6"],
  "max_fails": 3,
  "cooldown_secs": 30
}

201 <instance view>
```

`models` 必填，至少一项；熔断参数可省略，默认 `max_fails=3`、`cooldown_secs=30`。

`endpoints[].base_url` 默认只接受公网地址：loopback、RFC 1918、link-local（含云元数据 `169.254.169.254`）与 `*.internal` 等特殊域名会返回 `400`。本地 mock 或内网自建上游必须显式放行：

```
{
  "name": "local-mock",
  "api_key": "sk-test",
  "allow_private_upstream": true,
  "endpoints": [{"protocol": "openai", "base_url": "http://127.0.0.1:9000"}],
  "models": ["mock-model"]
}
```

即使放行了，运行期也会在拨号前再校验一次解析结果；域名后来解析到内网同样会被拒绝。

控制台「上游接入点」页的自定义端点表单里有一个「允许内网地址」开关，对应这个字段：
勾选后卡片上会显示「允许内网」标记；编辑时开关的当前值总会一并提交，不会被悄悄关掉。

校验失败返回 `400`；名称重复返回 `409`。内置端点不允许再传 `endpoints`。

#### 更新

```
PUT /admin/api/instances/1
{
  "name": "minimax",
  "api_key": "sk-new-...",
  "max_fails": 5,
  "cooldown_secs": 15
}
```

- 省略 `api_key` 或传空字符串：保留现有上游 Key。
- 传非空 `api_key`：整体替换。
- 省略 `preset`：沿用现有值；显式传入不同的 preset 返回 `400`（需删除重建）。
- 内置端点的端点由 preset 决定，不接受自定义端点。
- `models` 必填，更新时整体替换。
- `allow_private_upstream` **省略即关闭**：这个开关一旦放行内网地址就相当于把网关
  变成内网代理，所以后端按「没带就是关」处理。更新时要保留已放行的状态，必须显式
  带上 `"allow_private_upstream": true`。

#### 删除

```
DELETE /admin/api/instances/1   → 204
```

仍被模型引用的接入点返回 `409`；先从模型的候选链里移除它才能删除。

### 模型

#### 列表

```
GET /admin/api/models

200 [{
  "id": 1,
  "name": "MiniMax-M3.1-Flash",
  "upstream": "MiniMax-M3.1-Flash",
  "strategy": "failover",
  "instances": [
    {"instance": "minimax", "priority": 0, "windows": []},
    {"instance": "minimax-backup", "priority": 1, "windows": []}
  ],
  "timezone": "Asia/Shanghai",
  "server_time": "2026-11-22T18:00:00Z",
  "effective_candidates": [
    {"instance": "minimax", "priority": 0, "base_priority": 0, "active": true},
    {"instance": "minimax-backup", "priority": 1, "base_priority": 1, "active": true}
  ],
  "created_at": "2026-11-22T10:00:00Z",
  "updated_at": "2026-11-22T10:00:00Z"
}]
```

候选按 `priority` 升序返回；数字越小越优先，主实例失败后自动切到下一个。

- `timezone` 是时间窗口的判定时区（对应 `config.yaml` 的 `schedule.timezone`）。
- `server_time` 是计算生效顺序时的服务端时间。
- `effective_candidates` 是**此刻**的候选顺序：`priority` 是当前生效的优先级，`base_priority` 是配置里的优先级，
  `active` 表示该候选此刻是否参与路由，`window` 给出正在命中的时间窗口（如 `"00:00-08:00"`）。

#### 创建 / 更新 / 删除

```
POST /admin/api/models
{
  "name": "claude-sonnet-4-5",
  "upstream": "glm-4.6",
  "strategy": "failover",
  "instances": [{"instance": "glm", "priority": 0, "windows": []}]
}

201 <model view>

PUT  /admin/api/models/1   → 200 <model view>
DELETE /admin/api/models/1 → 204
```

- `upstream` 省略时与 `name` 相同，这就是别名机制：对外叫 `claude-sonnet-4-5`，上游仍是 `glm-4.6`。
- `instances` 至少一项，且引用的接入点必须存在，否则 `400`。
- 同一个模型不能重复引用同一个接入点。
- 模型名重复返回 `409`。
- `strategy` 目前只接受 `failover`。
- 模型的 `upstream` 必须出现在**每个**候选接入点的 `models` 声明里，否则返回 `400`（报出是哪个接入点缺哪一项）。

#### 时间窗口

候选接入点可以带时间窗口，用于峰谷计费：窗口命中时该候选按窗口内的优先级插队，
或者（`only`）只在窗口内参与路由。

```
POST /admin/api/models
{
  "name": "deepseek-chat",
  "upstream": "deepseek-chat",
  "strategy": "failover",
  "instances": [
    {
      "instance": "deepseek",
      "priority": 10,
      "windows": [{"start": "00:00", "end": "08:00", "priority": 0}]
    },
    {
      "instance": "glm",
      "priority": 1,
      "windows": [{"start": "00:00", "end": "08:00", "only": true}]
    }
  ]
}
```

- `start` / `end` 是 24 小时制 `HH:MM`；`start` 晚于 `end` 表示跨天（如 `22:00` – `06:00`）。
- `priority` 是窗口内生效的优先级（非负整数）；`only` 为 `true` 时表示窗口外该候选不参与路由。
- 一个窗口至少要有 `priority` 或 `only` 之一，否则返回 `400`。
- 判定按 `schedule.timezone`（默认 `Asia/Shanghai`）。

### 兼容接口

`GET /admin/api/providers` 保留为只读聚合视图（每个接入点一行，附它承载的模型），供旧脚本使用；不再支持写入。

### 与 config.yaml 的关系

首次启动且数据库中没有拓扑时，会导入 `config.yaml` 的 `instances` / `models`（Key 加密后落库）。之后以数据库为准。旧版的 `providers` 写法仍然可用，加载时自动展开成一个接入点加若干模型；已有数据库中的旧供应商会在启动时自动迁移。

## 日志

```
GET /admin/api/logs?limit=100&key_id=1&model=glm-4.6&since=2026-11-22T00:00:00Z
200 [{
  "id": 1024, "ts":"...", "request_id":"...", "api_key_id":1,
  "ingress_protocol":"openai", "model":"glm-4.6",
  "provider":"glm", "endpoint":"openai", "upstream_model":"glm-4.6",
  "status":200, "latency_ms":812, "ttft_ms":245,
  "prompt_tokens":120, "completion_tokens":300, "cost_usd":0.00042,
  "usage_estimated":false, "error_code":""
}]
```

响应 body 不入库；错误原因只记录分类码（`client_canceled`、`upstream_timeout`、`upstream_5xx` 等）。

`provider` 字段存放**上游接入点**的名字。一次请求切换过上游时，每次尝试各记一条：失败的条目状态码是对应上游的状态，`error_code` 记录失败分类；客户端最终拿到的那条是成功结果。

## 用量

```
GET /admin/api/usage?days=7
200 {
  "daily": [{"day":"2026-11-22","requests":120,"prompt_tokens":10000,
             "completion_tokens":22000,"cost_usd":0.31}],
  "by_model": [{"model":"glm-4.6","requests":90,"cost_usd":0.25}],
  "by_key": [{"key_id":1,"name":"laptop","requests":80,"cost_usd":0.22}]
}
```

## 套餐额度

内置端点如果厂商提供额度接口，网关会用该接入点的上游 Key 查询套餐的额度窗口，控制台在「上游接入点」卡片上直接显示。

```
GET /admin/api/quota

200 {
  "reports": [
    {
      "instance_id": 1,
      "instance": "opencode-go",
      "preset": "opencode-go",
      "supported": true,
      "fetched_at": "2026-11-23T03:20:00Z",
      "error": "",
      "windows": [
        {"id": "5h", "label": "5 小时", "used_pct": 12,
         "resets_at": "2026-11-23T04:23:18Z", "status": "ok"},
        {"id": "weekly", "label": "每周", "used_pct": 78,
         "resets_at": "2026-11-30T00:00:00Z", "status": "ok"},
        {"id": "monthly", "label": "每月", "used_pct": 94,
         "resets_at": "2026-12-20T14:43:31Z", "status": "ok"}
      ]
    },
    {"instance_id": 2, "instance": "custom", "supported": false, "windows": []}
  ]
}
```

- `used_pct` 是**已用**百分比（0–100）；`resets_at` 是窗口重置时间（RFC 3339），上游不返回时为空；`status` 为 `ok` 或 `exhausted`。
- 窗口按厂商实际提供的返回：OpenCode Go 与火山方舟 Coding Plan 有 5 小时 / 每周 / 每月三个窗口，MiniMax 编码套餐只有 5 小时与每周两个窗口（没有月度窗口）。
- 火山方舟的套餐额度只在账号管控面上，推理用的 API Key 读不到：接入点上要另外填一组**额度查询凭证**（火山引擎 Access Key ID + Secret Access Key），网关用火山 V4 签名去查。凭证加密存储，只回显掩码。
- 自定义端点没有额度接口，`supported` 为 `false`。
- 单个接入点查询失败只影响它自己：错误写在该条报告的 `error` 里，其余接入点照常返回，接口整体仍是 `200`。
- 上游 Key 只用于向厂商固定的额度地址发只读请求（火山是签名的 POST），不会出现在响应或日志里。
- 结果按接入点缓存 60 秒，控制台每 60 秒轮询一次。

### 额度查询凭证

接入点可以带一组额外的额度查询凭证，形状与火山引擎的访问密钥一致：

```
POST /admin/api/instances
{
  "name": "ark-coding",
  "preset": "ark-coding-plan-cn",
  "api_key": "ark-...",
  "quota_credential": {
    "access_key_id": "AKLT...",
    "secret_access_key": "..."
  },
  "models": ["ark-code-latest"]
}
```

- 提交后只写不读：列表与详情里的 `quota_credential` 只有 `access_key_id` 与 `secret_hint`（末四位掩码）。
- 编辑时不带这个字段表示保留原凭证；带上就整组替换。

## 健康与指标

```
GET /healthz   → 200 "ok"
GET /readyz    → 200 或 503
GET /metrics   → Prometheus 文本（需 server.metrics_token，否则 404）
```

`/metrics` 默认关闭：未配置 `server.metrics_token` 时接口不存在，任何来源都返回 `404`。
配置令牌后需带 `Authorization: Bearer <token>` 或 `?token=<token>`，令牌不对返回 `401`。
令牌改完 1 秒内热加载生效。

## 错误格式

管理接口统一：

```json
{"error": {"type": "invalid_request", "message": "..."}}
```

数据面错误遵循入口协议（OpenAI 的 `error.type` 或 Anthropic 的 `type: error`）。
