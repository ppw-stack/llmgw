# 本地测试环境

一套开箱可用的 compose 环境：网关 + 确定性的 mock 上游。拓扑与**生产环境一致**
（同名接入点、同样的候选链、同样的会话头与可切换状态码），所有上游指向 mock，
用于验证双协议、流式、故障转移、会话头注入与用量统计。

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

## 接入点（与生产同名）

mock 用 `api_key` 区分行为，所以测试配置能复现生产的两种异常：

| 接入点 | api_key | mock 行为 |
|---|---|---|
| `minimax` | `mock-quota` | 恒返回 `402`，复现「套餐 5 小时窗口打满」 |
| `fangzhou` | `mock-upstream-key` | 正常确定性应答 |
| `opencode-go` | `mock-session-required` | 缺 `x-opencode-session` 返回 `400`，复现 OpenCode Go 的强制要求 |
| `workbuddy` | `mock-upstream-key` | 正常应答，上游模型名带 `workbuddy/` 前缀 |

配置里的两个新字段：

- `minimax.failover_statuses: [402]` —— 命中 402 时切到下一个候选，而不是把 402 透传给客户端
- `opencode-go.session_header: x-opencode-session` —— 客户端没带该头时，网关按「系统提示 +
  首条用户消息」派生 `ses_` + 32 位十六进制注入；客户端自带则原样透传

## 模型（与生产一致）

| 模型 | 候选链 | 预期行为 |
|---|---|---|
| `MiniMax-M3.1-Flash` | `minimax(p0) → opencode-go(p1)` | minimax 恒 402 → 自动切到 opencode-go 并注入会话头 → **200** |
| `deepseek-v4.1-flash` | `opencode-go(p0) → workbuddy(p1) → fangzhou(p2)` | 首选 opencode-go，网关注入会话头 → **200** |
| `glm-5.3-flash` | `fangzhou(p0) → workbuddy(p0) → opencode-go(p1)` | 两个 p0 平局取 fangzhou → **200** |

## 验证

```bash
KEY=sk-gw-...

# 1. 402 触发切换 + 会话头注入（生产里打满额的场景）
curl -s http://localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"MiniMax-M3.1-Flash","messages":[{"role":"user","content":"hi"}]}'
# 请求日志里应显示 provider=opencode-go、status=200

# 2. OpenAI 流式
curl -sN http://localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}'

# 3. Anthropic 入口（minimax 402 同样会切到 opencode-go 的 anthropic 端点）
curl -sN http://localhost:8080/anthropic/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"MiniMax-M3.1-Flash","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"hi"}]}'
```

## 验证会话头的稳定性

同一对话的两次请求应得到同一个注入值（prompt 缓存才有意义）：

```bash
# 在网关容器里看上游收到的头
docker compose -f deploy/docker-compose.test.yml logs -f mock
# mock 会打印每个请求；同一 system+首条用户消息的请求，x-opencode-session 必须一致
```

想验证「客户端自带的头不被覆盖」，直接带一个固定值调用：

```bash
curl -s http://localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -H 'x-opencode-session: ses_from_client' \
  -d '{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}'
```

## 验证出站代理

接入点支持 `proxy_url`（`http` / `https` / `socks5` / `socks5h`），作用于推理转发、
套餐额度查询与模型拉取。测试环境带了一个可选的代理容器，用来验证这条链路真的被走到：

```bash
# 启动代理（默认不在 compose 的默认 profile 里）
docker compose -f deploy/docker-compose.test.yml --profile proxy up -d proxy
```

代理监听 `http://proxy:8888`（HTTP + CONNECT 隧道）与 `socks5://proxy:1080`，
账号密码 `proxyuser` / `proxypass`，每条转发都会打印一行日志。

给接入点配上代理（控制台「上游接入点」页的「网络代理」字段，或 `test-config.yaml`
里的 `proxy_url:`，注意配置文件只在**数据库为空时的首次启动**导入）：

```yaml
proxy_url: "http://proxyuser:proxypass@proxy:8888"
```

然后发起一次请求，确认代理日志里出现转发记录：

```bash
KEY=sk-gw-...
curl -s http://localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}'

docker compose -f deploy/docker-compose.test.yml logs -f proxy
# proxy: POST http://mock:8000/chat/completions auth=True
```

代理密码不会出现在接口响应、控制台或日志里（只回显 `****`）。
指向云元数据地址（`169.254.169.254`）、`metadata.google.internal` 等的代理会被直接拒绝。

## 常用操作

```bash
# 日志
docker compose -f deploy/docker-compose.test.yml logs -f gateway
docker compose -f deploy/docker-compose.test.yml logs -f mock
docker compose -f deploy/docker-compose.test.yml logs -f proxy   # 需 --profile proxy

# 重置数据（删除 Key、日志、用量）
docker compose -f deploy/docker-compose.test.yml down -v
docker compose -f deploy/docker-compose.test.yml up -d

# 停止
docker compose -f deploy/docker-compose.test.yml down
```

## 说明

- mock 上游是确定性的，不调用任何真实模型，不产生费用。
- 网关配置在 `test-config.yaml`，修改后 1 秒内自动热加载，无需重启容器。
- `test-config.yaml` 只在**数据库为空时的首次启动**导入拓扑；启动之后控制台是唯一入口，
  改配置文件不会覆盖控制台里的改动。
- 数据库位于名为 `llmgw-test_llmgw-test-data` 的卷中。
- 若 8080 被占用，修改 compose 里的端口映射即可。
- 生产若新增接入点或模型，把对应条目同步到 `test-config.yaml` 即可保持两边一致。
