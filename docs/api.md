# 管理 API

所有管理接口以 `/admin/api` 为前缀，使用 `Authorization: Bearer <admin_token>`。

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

## 供应商（只读，来自 config.yaml）

```
GET /admin/api/providers

200 [{
  "name": "glm",
  "endpoints": [
    {"name":"openai","protocol":"openai","base_url":"https://...","models":{"glm-4.6":"glm-4.6"}},
    {"name":"anthropic","protocol":"anthropic","base_url":"https://...","models":{"glm-4.6":"glm-4.6"}}
  ],
  "keys": 2
}]
```

上游 Key 永不返回明文或哈希，只返回数量。

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
