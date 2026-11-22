# LLM 网关需求文档（v1）

> 状态：v1 定稿
> 定位：单机、单二进制、面向个人与小团队的轻量 LLM 网关。

## 1. 背景与目标

调用不同厂商的大模型时，baseUrl、鉴权头、请求路径各不相同。本项目提供一个统一出口：

- 一个网关持有所有上游 Key，客户端只配置网关地址与网关 Key；
- 同时对外暴露 **OpenAI 协议** 与 **Anthropic 协议** 两个 URL，覆盖主流编码类客户端；
- 按请求中的模型名查表转发到对应上游，流式透明；
- 记录每次请求的 token、耗时与成本，便于观察用量。

**v1 明确不做**：跨协议转换、负载均衡策略、路由规则引擎、RPM/TPM/预算/白名单、多租户、在线支付、MCP、RAG、护栏。

## 2. 用户与场景

| 用户 | 场景 |
|---|---|
| 个人开发者 | 本机或小服务器跑一个二进制，Claude Code / Cursor / 脚本共用 |
| 小团队（3~20 人） | 管理员配供应商与 Key，成员各自持网关 Key 使用 |

## 3. 对外接口

| 协议 | base_url | 说明 |
|---|---|---|
| OpenAI | `https://gw.example.com/openai/v1` | SDK 在此后拼 `/chat/completions` |
| Anthropic | `https://gw.example.com/anthropic` | SDK 在此后拼 `/v1/messages` |

管理台：`/`；管理 API：`/admin/api/*`；运维：`/healthz`、`/readyz`、`/metrics`。

路径归一化（容忍填错）：

- `/anthropic/messages` ≡ `/anthropic/v1/messages`
- `/openai/chat/completions` ≡ `/openai/v1/chat/completions`
- 连续 `/v1/v1/` 合并为 `/v1/`

## 4. 功能需求

### FR-01 双协议入口

- OpenAI：`POST /openai/v1/chat/completions`、`GET /openai/v1/models`
- Anthropic：`POST /anthropic/v1/messages`
- 两个协议的鉴权头都接受：`Authorization: Bearer sk-xxx`、`x-api-key: sk-xxx`
- 流式（`stream=true`）逐块转发，不缓冲整条响应
- 客户端断开时立即取消上游请求

### FR-02 协议不转换

- 每个上游端点声明自己的协议（`openai` / `anthropic`）。
- 路由候选按协议隔离：OpenAI 请求只能命中 OpenAI 协议端点，反之亦然。
- 模型存在但协议不匹配 → `400`，错误体遵循入口协议，且不产生上游调用：
  `model "claude-..." is only available via the Anthropic endpoint (/anthropic)`。

### FR-03 供应商与端点

- Provider：名称、上游 Key 列表、超时、HTTP 代理（可选）。
- Endpoint：协议、base_url、对外模型名 → 上游模型名映射。
- 一个 Provider 可挂两个端点（openai + anthropic），共享同一批上游 Key。
- 供应商保存在数据库，控制台可直接新增、编辑、删除；上游 Key 加密存储。
- 同一协议下，一个模型名只能属于一个端点；重复则拒绝保存并报出冲突来源。
- 首次启动且数据库为空时，从 `config.yaml` 导入供应商。

### FR-04 路由（v1 最小实现）

- 仅 `(protocol, model) → endpoint` 哈希查表。
- 无规则、无权重、无优先级、无 fallback、无熔断。
- 未配置模型 → `404 model_not_found`，提示该模型可能属于哪个协议。

### FR-05 网关 Key

- `sk-` 前缀，仅存 SHA-256 哈希与前 8 位前缀，明文只在创建时返回一次。
- 支持命名、停用、删除；停用后下一次请求即 `401`。
- 无模型白名单、无 RPM/TPM、无预算限制。

### FR-06 上游 Key 池

- 同一 Provider 的 Key 按原子递增索引轮询。
- 上游返回 `401/403` 时标记该 Key 失效，换下一个 Key 重试一次；全部失效则透传错误。

### FR-07 请求日志与用量

- 每请求记录：时间、request_id、key、入口协议、模型、端点、上游模型、状态码、耗时、TTFT、输入/输出/缓存 token、估算成本、是否估算。
- 请求/响应 body 默认不入库。
- 异步批量写入，队列满时丢弃并计数，不阻塞请求路径。

### FR-08 配置与热更新

- `config.yaml` 为配置源；启动时加载，运行中按 mtime 轮询重载。
- 重载构建新快照并原子替换；进行中的请求继续使用旧快照。
- 校验失败保留旧配置并记录错误日志。

### FR-09 可观测

- `/healthz` 存活，`/readyz` 就绪（配置已加载且 DB 可用）。
- `/metrics`：请求数、延迟直方图、TTFT、token 计数、上游错误数、日志丢弃数。
- 结构化 JSON 日志，日志中带 `request_id`；响应头回传 `X-Request-Id`。

### FR-10 管理面（Web 控制台 + REST API）

Web 控制台（`/`，Vue 3 构建产物内嵌进二进制）：

- 登录：用户名 + 密码，成功后由服务端下发 HttpOnly 会话 Cookie。
- 概览：请求数、输入/输出 tokens、成本，近 1/7/30 天柱状图，按模型/按 Key 聚合。
- 供应商：新增/编辑/删除 Provider 与 Endpoint、管理上游 Key、模型映射。
- 网关 Key：创建（明文仅显示一次）、启停、删除。- 请求日志：按模型过滤，展示状态、延迟、TTFT、tokens、成本、错误码。
- 接入说明：两条 base_url 与各客户端填法，可一键复制。
- 修改密码：成功后所有会话失效。

### FR-11 管理认证

- 管理员密码使用 bcrypt 存储，不存在明文。
- 会话令牌随机 32 字节，数据库只存 SHA-256 哈希，默认 12 小时过期。
- Cookie 属性：`HttpOnly`、`SameSite=Strict`、HTTPS 下 `Secure`。
- 登录失败按客户端 IP 限速（5 分钟 10 次），超限返回 `429` + `Retry-After`。
- 管理写操作写审计日志（登录、登出、改密、Key 增删改）。
- 可选 `server.admin_token` 作为脚本用 Bearer token；未配置则禁用。
- 客户端 IP 仅在直连方属于 `trusted_proxies` 时才采信 `X-Forwarded-For`。

REST API：

- `POST /admin/api/keys` 创建（返回一次明文）、`GET /admin/api/keys` 列表、`PATCH /admin/api/keys/{id}` 启停、`DELETE`。
- `GET /admin/api/providers` 查看当前配置的供应商与端点（只读，来自 config.yaml）。
- `GET /admin/api/logs` 按 key/模型/时间过滤。
- `GET /admin/api/usage` 按天/模型/Key 聚合。
- 管理鉴权：`admin_token`（配置或环境变量），通过 `Authorization: Bearer` 校验。

## 5. 非功能需求

| 编号 | 项 | 目标 |
|---|---|---|
| NFR-01 | 转发开销 | P50 < 3ms、P99 < 10ms（不含上游） |
| NFR-02 | 吞吐 | 单实例 ≥ 2000 RPS（假上游，4C） |
| NFR-03 | 内存 | 空载 < 100MB |
| NFR-04 | 流式 | 逐事件 flush；TTFT 增量 < 5ms |
| NFR-05 | 数据面 | 请求路径无数据库依赖：网关 Key 走内存缓存，日志异步批量写 |
| NFR-06 | 安全 | 上游 Key 仅从配置/环境读取，不回显于任何接口；网关 Key 仅存哈希 |
| NFR-07 | 兼容 | OpenAI/Anthropic 官方 SDK、Claude Code、Codex CLI 可直接指向 |
| NFR-08 | 部署 | 单二进制（darwin/linux, amd64/arm64），无 cgo |
| NFR-09 | 管理认证 | bcrypt 密码、HttpOnly 会话 Cookie、登录限速、审计日志 |
| NFR-10 | 公网 | 反代终止 TLS、trusted_proxies、安全响应头、CSP |

## 6. 验收标准

1. OpenAI SDK 指向 `/openai/v1`，`stream=True` 与 `tools` 正常，usage 记录正确。
2. Anthropic SDK / Claude Code 指向 `/anthropic`，`stream` 与 `tool_use` 正常。
3. 请求体除 `model` 外字节不变（固定 body 做 diff 断言）。
4. 流式响应的事件序列与直连上游一致；仅 OpenAI 可能因注入 `stream_options` 多出末尾 usage chunk。
5. 把 Anthropic 模型打到 OpenAI 入口 → `400`，上游零调用。
6. 模型未配置 → `404`，错误信息包含可用入口提示。
7. 停用网关 Key 后下一次请求 `401`；删除后哈希不再命中。
8. 同一 Provider 两个端点共享 Key；Key 401 后两个端点同时失效。
9. 客户端中途断开，上游在 1s 内取消（日志 `client_canceled`）。
10. 改 `config.yaml` 后 1s 内生效，在途流式请求不受影响。
11. `/metrics` 可读出请求数、延迟、token；日志队列满时丢弃计数递增且请求不受影响。
12. 新建或停用网关 Key 立即生效，无需重启；停用后下一次请求即 `401`。
13. 配置 `server.metrics_token` 后，未携带令牌访问 `/metrics` 返回 `401`。
14. 上游响应超过 64 MiB 时返回 `502`，不会向客户端返回截断的 JSON。
15. 流式上游在 `provider.timeout` 内无新数据时，连接被主动关闭并记录日志。
16. 未上报 usage 的上游响应被标记 `usage_estimated=true` 并给出按字节估算的 token。

## 7. 里程碑

| 阶段 | 内容 |
|---|---|
| M0 | 文档（本目录） |
| M1 | 骨架 + OpenAI 直通 + 查表路由 + Key 池 + 日志 + metrics |
| M2 | Anthropic 入口 + SSE 透传 + usage 嗅探 |
| M3 | Web 控制台（Vue 3 + Vite，go:embed） |
| M4 | Docker 多阶段镜像、`-version`/`-healthcheck`、跨平台发布包 |
