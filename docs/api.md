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

`action` 取值：`login`、`logout`、`password_change`、`key.create`、`key.update`、`key.delete`。

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

## 供应商

供应商保存在 SQLite 中，上游 Key 使用 AES-256-GCM 加密。控制台「供应商」页可直接增删改，保存后立即生效。

### 列表

```
GET /admin/api/providers

200 [{
  "id": 1,
  "name": "glm",
  "proxy": "",
  "timeout_ms": 120000,
  "key_count": 2,
  "key_hints": ["****a1b2", "****c3d4"],
  "endpoints": [{
    "name": "openai",
    "protocol": "openai",
    "base_url": "https://open.bigmodel.cn/api/paas/v4",
    "version": "",
    "models": {"glm-4.6": "glm-4.6"}
  }]
}]
```

上游 Key 只返回末四位掩码，永不返回明文。

### 创建

```
POST /admin/api/providers
{
  "name": "glm",
  "proxy": "",
  "timeout_ms": 120000,
  "keys": ["sk-real-upstream-key"],
  "endpoints": [{
    "name": "openai",
    "protocol": "openai",
    "base_url": "https://open.bigmodel.cn/api/paas/v4",
    "models": {"glm-4.6": "glm-4.6"}
  }]
}

201 <provider view>
```

校验失败返回 `400`；名称重复返回 `409`；与现有供应商在同一协议下模型冲突返回 `400`。

### 更新

```
PUT /admin/api/providers/1
{
  "name": "glm",
  "timeout_ms": 180000,
  "endpoints": [...]
}
```

- 省略 `keys` 字段：保留现有上游 Key。
- 传 `"keys": []`：清空。
- 传 `"keys": ["..."]`：整体替换。

### 删除

```
DELETE /admin/api/providers/1   → 204
```

删除后该供应商的模型立即从路由中移除；引用它的请求返回 `404 model_not_found`。

### 与 config.yaml 的关系

首次启动且数据库中没有供应商时，会导入 `config.yaml` 的 `providers`（Key 加密后落库）。之后以数据库为准，`config.yaml` 的 `providers` 不再覆盖页面上的修改。

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
