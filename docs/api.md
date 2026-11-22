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
    {"instance": "minimax", "priority": 0, "weight": 100},
    {"instance": "minimax-backup", "priority": 1, "weight": 100}
  ],
  "created_at": "2026-11-22T10:00:00Z",
  "updated_at": "2026-11-22T10:00:00Z"
}]
```

候选按 `priority` 升序返回；数字越小越优先，主实例失败后自动切到下一个。

#### 创建 / 更新 / 删除

```
POST /admin/api/models
{
  "name": "claude-sonnet-4-5",
  "upstream": "glm-4.6",
  "strategy": "failover",
  "instances": [{"instance": "glm", "priority": 0, "weight": 100}]
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

## 健康与指标

```
GET /healthz   → 200 "ok"
GET /readyz    → 200 或 503
GET /metrics   → Prometheus 文本
```

## 错误格式

管理接口统一：

```json
{"error": {"type": "invalid_request", "message": "..."}}
```

数据面错误遵循入口协议（OpenAI 的 `error.type` 或 Anthropic 的 `type: error`）。
