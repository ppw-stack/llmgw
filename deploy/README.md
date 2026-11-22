# 本地测试环境

一套开箱可用的 compose 环境：网关 + 确定性的 mock 上游，用于验证双协议、流式、路由映射与用量统计。

> 里面的密码与 Key 仅用于本地测试，不要用于任何生产部署。

## 启动

```bash
docker compose -f deploy/docker-compose.test.yml up -d --build
docker compose -f deploy/docker-compose.test.yml ps
```

两个容器都应为 `healthy`：

- `llmgw-test`：网关，`http://localhost:8080`
- `llmgw-mock`：mock 上游，仅在 compose 网络内可达（`http://mock:8000`）

## 访问

| 项 | 值 |
|---|---|
| 控制台 | http://localhost:8080 |
| 用户名 | `admin` |
| 密码 | `test-admin-password` |
| OpenAI base_url | `http://localhost:8080/openai/v1` |
| Anthropic base_url | `http://localhost:8080/anthropic` |
| metrics | `http://localhost:8080/metrics`，Bearer `test-metrics-token` |

网关 Key 在控制台「网关 Key」页创建，或通过 API：

```bash
curl -c /tmp/llmgw-cookies -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"test-admin-password"}' \
  http://localhost:8080/admin/api/login

curl -b /tmp/llmgw-cookies -H 'Content-Type: application/json' \
  -d '{"name":"local-dev"}' \
  http://localhost:8080/admin/api/keys
```

## 供应商

现在可以直接在控制台配置：打开 http://localhost:8080 → 「供应商」→ 新增/编辑。

首次启动会把 `test-config.yaml` 里的 `mock` 供应商导入数据库（Key 加密），之后以页面为准。`database.path` 指向 `/data/llmgw.db`，主密钥自动生成在 `/data/master.key`。

> 删除数据卷会同时丢失数据库、上游 Key 与主密钥。

## 可用的模型

| 模型 | 协议 | 说明 |
|---|---|---|
| `gpt-test` | OpenAI | 正常响应，带 usage |
| `gpt-alias` | OpenAI | 对外名映射为上游 `gpt-upstream-name` |
| `claude-test` | Anthropic | 正常响应，带 usage |

mock 支持 `stream: true`，两种协议的 SSE 事件都完整。

## 验证

```bash
KEY=sk-gw-...

# OpenAI 非流式
curl -s http://localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}'

# OpenAI 流式
curl -sN http://localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-test","stream":true,"messages":[]}'

# Anthropic 流式
curl -sN http://localhost:8080/anthropic/v1/messages \
  -H "x-api-key: $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"claude-test","stream":true,"max_tokens":32,"messages":[]}'
```

日志与用量在控制台「请求日志」「概览」页查看。

## 常用操作

```bash
# 日志
docker compose -f deploy/docker-compose.test.yml logs -f gateway
docker compose -f deploy/docker-compose.test.yml logs -f mock

# 重置数据（删除 Key、日志、用量）
docker compose -f deploy/docker-compose.test.yml down -v
docker compose -f deploy/docker-compose.test.yml up -d

# 停止
docker compose -f deploy/docker-compose.test.yml down
```

## 说明

- mock 上游是确定性的，不调用任何真实模型，不产生费用。
- 网关配置在 `test-config.yaml`，修改后 1 秒内自动热加载，无需重启容器。
- 数据库位于名为 `llmgw-test_llmgw-test-data` 的卷中。
- 若 8080 被占用，修改 compose 里的端口映射即可。
