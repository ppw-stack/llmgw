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

## 上游接入点与模型

现在可以直接在控制台配置：打开 http://localhost:8080 → 「上游接入点」新增接入点（内置端点只需填 API Key，自定义端点手填地址），再到「模型」页声明模型挂到哪些接入点。

「内置端点」下拉里可选 MiniMax 编码套餐（国内 `api.minimax.cn` / 国际 `api.minimax.io`）、OpenCode Go 订阅（`opencode.ai/inference/go`）、火山方舟 Coding Plan（`ark.cn-beijing.volces.com/api/coding`）；不同厂商、不同套餐的 Key 不通用。也可以选「自定义端点」手填地址。

其中 OpenCode Go 要求每个请求带 `x-opencode-session`（缺失返回 `400 MissingSessionID`）：网关会原样透传客户端请求头，Claude Code / Codex / ZCode / Pi 等自带会话头的客户端可以直接用。

每个接入点还要填「支持的模型」——这是它声明能服务的上游模型名。模型页把模型挂到接入点上时，会校验模型的上游名是否在这份声明里。

首次启动会把 `test-config.yaml` 里的 `mock` 接入点与三个模型导入数据库（Key 加密），之后以页面为准。`database.path` 指向 `/data/llmgw.db`，主密钥自动生成在 `/data/master.key`。

> 删除数据卷会同时丢失数据库、上游 Key 与主密钥。

## 可用的模型

| 模型 | 入口 | 说明 |
|---|---|---|
| `gpt-test` | 两个入口 | 正常响应，带 usage |
| `gpt-alias` | 两个入口 | 对外名映射为上游 `gpt-upstream-name` |
| `claude-test` | 两个入口 | 正常响应，带 usage |
| `gpt-windowed` | 两个入口 | 时间窗口示例：北京时间 00:00-08:00 走 `mock-night`，其他时段走 `mock` |

mock 支持 `stream: true`，两种协议的 SSE 事件都完整。

## 时间窗口（峰谷计费）

`gpt-windowed` 演示候选接入点的时间窗口：

```yaml
schedule:
  timezone: "Asia/Shanghai"

models:
  - name: gpt-windowed
    upstream: gpt-test
    instances:
      - name: mock
        priority: 1
      - name: mock-night
        priority: 10
        windows:
          - start: "00:00"
            end: "08:00"
            priority: 0
```

窗口命中时 `mock-night` 的生效优先级降到 0，抢到最前；窗口外回到基础优先级，由 `mock` 接管。
两个接入点指向同一个 mock，所以响应内容一样，**请求日志里的「上游接入点」列**能看出当前走了谁。

也可以在控制台「模型」页直接编辑窗口，卡片上会显示当前生效顺序（每 30 秒刷新一次）。
控制台时区来自 `test-config.yaml` 的 `schedule.timezone`。

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
