# llmgw

轻量 LLM 网关：一个二进制、一个数据库文件，把多家大模型收敛成两个统一入口。

- **OpenAI 协议**：`https://gateway/openai/v1`
- **Anthropic 协议**：`https://gateway/anthropic`

客户端只配置网关地址和网关 Key，上游的 baseUrl 与 apiKey 全部留在网关里。

## 当前范围（v1）

| 能做 | 不做 |
|---|---|
| OpenAI / Anthropic 双入口，同协议直通 | 跨协议转换 |
| 按 `(协议, 模型)` 查表路由，得到候选链 | 规则引擎、权重轮询 |
| 同一模型挂多个上游，主实例失败自动切换 | 跨实例限流、预算 |
| 上游接入点熔断（连续失败后跳过） | 主动健康检查 |
| 内置端点（MiniMax 编码套餐国内 / 国际）/ 自定义接入点 | RPM/TPM/预算、模型白名单 |
| 上游 401/403 原样透传 | 4xx 自动重试 |
| 请求日志、token 与成本统计 | 多租户、计费、在线支付 |
| 配置热更新 | MCP、RAG、护栏 |
| Web 控制台：概览 / 上游接入点 / 模型 / Key / 日志 / 接入说明 | Kubernetes Operator |
| Prometheus 指标、结构化日志 | |
## 快速开始

```bash
# 1. 构建
make build          # bin/llmgw

# 2. 准备配置
cp config.example.yaml config.yaml
$EDITOR config.yaml # 填上游 Key 与模型（或使用内置端点 preset）

# 3. 启动
./bin/llmgw -config config.yaml

# 4. 打开控制台
open http://localhost:8080
#    用户名 admin，密码在 data/admin-password.txt
```

首次启动会打印一个网关 Key（仅一次）：

```
[BOOTSTRAP] first gateway key: sk-gw-xxxxxxxx
```

验证：

```bash
curl localhost:8080/healthz
curl localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer sk-gw-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-4.6","messages":[{"role":"user","content":"hi"}]}'
```

## 客户端接入

| 客户端 | base_url / 环境变量 |
|---|---|
| OpenAI SDK | `base_url = https://gateway/openai/v1` |
| Codex CLI / Cursor / Cline(OpenAI Compatible) | `https://gateway/openai/v1` |
| Anthropic SDK | `base_url = https://gateway/anthropic` |
| Claude Code | `ANTHROPIC_BASE_URL=https://gateway/anthropic` |
| Cline(Anthropic 模式) | `https://gateway/anthropic` |

两种协议的 Key 都填网关 Key（`sk-gw-` 开头）。

## 配置要点

上游拓扑分两层：**上游接入点**（一个账号 + 它提供的端点）与**模型**（对外名 + 候选接入点链）。

```yaml
instances:
  # 内置端点：端点地址由程序内置，随版本升级自动更新
  - name: minimax-cn
    preset: minimax-mplan-cn          # 国内：api.minimax.cn
    api_key: "${MINIMAX_KEY}"
    models: [MiniMax-M3.1-Flash]      # 必填：这个账号能服务哪些上游模型名
  # - name: minimax
  #   preset: minimax-mplan-global    # 国际：api.minimax.io

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

models:
  # 单实例
  - name: MiniMax-M3.1-Flash
    upstream: MiniMax-M3.1-Flash
    instances:
      - name: minimax-cn

  # 主备：主实例失败后自动切到备份
  - name: MiniMax-M3.1-Flash[1m]
    upstream: MiniMax-M3.1-Flash
    instances:
      - name: minimax-cn
        priority: 0
      - name: minimax-cn-backup
        priority: 1

  # 别名：对外叫 claude-sonnet-4-5，上游仍是 glm-4.6
  - name: claude-sonnet-4-5
    upstream: glm-4.6
    instances:
      - name: glm
```

- 模型名全局唯一；`upstream` 省略时与 `name` 相同，这就是别名机制。
- 接入点的 `models` 必填；模型的上游名必须被每个候选接入点声明，否则拒绝保存/启动。
- 接入点只填一个端点就只开放对应协议；模型同时注册到它能提供的每个协议入口。
- 每个请求按 `(协议, 模型)` 哈希查表，得到一条按 `priority` 排序的候选链。
- 上游返回 5xx / 429 / 超时时自动切到下一个候选；4xx 不切换，原样透传。
- 接入点连续失败 3 次后跳过 30 秒（`max_fails` / `cooldown_secs` 可调）。
- 模型只在另一个协议存在时，请求会得到 `400` 并提示改用哪个 base_url。
- 改配置后 1 秒内自动生效，在途流式请求不受影响。
- 上游超时全局默认 120s，单个接入点只能调低；代理使用进程的 `HTTP_PROXY`/`HTTPS_PROXY` 环境变量。
- 内置端点的文档：MiniMax 编码套餐国内 <https://platform.minimaxi.com/docs/m-plan/other-tools>、国际 <https://platform.minimax.io/docs/m-plan/other-tools>。两边 Key 不通用，按套餐归属选择 `minimax-mplan-cn` / `minimax-mplan-global`。

## 管理 API

```bash
# 登录，拿到会话 Cookie
curl -c cookies.txt -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}' \
  localhost:8080/admin/api/login

# 创建网关 Key（明文只返回一次）
curl -b cookies.txt -X POST localhost:8080/admin/api/keys \
  -H 'Content-Type: application/json' \
  -d '{"name":"laptop"}'

# 查看上游接入点、模型、日志、用量、审计
curl -b cookies.txt localhost:8080/admin/api/instances
curl -b cookies.txt localhost:8080/admin/api/models
curl -b cookies.txt "localhost:8080/admin/api/logs?limit=20"
curl -b cookies.txt "localhost:8080/admin/api/usage?days=7"
curl -b cookies.txt "localhost:8080/admin/api/audit?limit=20"
```

若需要在脚本中使用 Bearer token，可显式配置 `server.admin_token`。

## 公网部署

- 管理员密码 bcrypt 存储；会话 Cookie 为 `HttpOnly` + `SameSite=Strict`，HTTPS 下自动 `Secure`。
- 登录失败按 IP 限速，管理写操作有审计日志。
- 由 nginx/Caddy/云 LB 终止 TLS，并配置 `trusted_proxies` 以正确解析客户端 IP。
- 反代必须关闭缓冲（`proxy_buffering off`），否则流式会被攒成一次性返回。
- nginx 示例、HSTS、`/metrics` 保护等见 [docs/operations.md](docs/operations.md#9-安全与公网部署)。

## 目录结构

```
cmd/llmgw/            入口：配置、启动、热更新、优雅退出
internal/config/      YAML 解析、校验、${ENV} 展开
internal/ingress/     路径归一化、协议判定、模型/流式元数据解析
internal/router/      查表路由与快照
internal/preset/      内置端点目录（MiniMax 编码套餐等）
internal/transport/   转发、SSE 透传、usage 嗅探、错误分类
internal/store/       SQLite、异步日志、用量查询
internal/auth/        网关 Key 生成、密码哈希、会话令牌
internal/clientip/    可信代理解析客户端 IP
internal/observ/      Prometheus 指标
internal/server/      mux、中间件、数据面 handler、管理 API
internal/webui/       内嵌的 Vite 构建产物（go:embed）
web/                  Vue 3 + Vite 控制台源码
docs/                 需求、架构、路由、API、运维
```

## Docker

```bash
# 构建镜像（多阶段：Node 构建前端 → Go 编译 → Alpine 运行时）
make docker

# 或使用 compose
cp config.example.yaml config.yaml
# 把 database.path 改成 /data/llmgw.db，并按需填上游 Key
docker compose up -d --build
docker compose logs llmgw     # 首次启动会打印管理员密码
```

镜像以非 root 用户运行，`/data` 为数据卷，内置 `HEALTHCHECK`（`llmgw -healthcheck`）。详见 [docs/operations.md](docs/operations.md#10-docker-与发布)。

本地测试环境（网关 + 确定性 mock 上游，无需真实 API Key）：

```bash
docker compose -f deploy/docker-compose.test.yml up -d --build
```

访问 http://localhost:8080（`admin` / `test-admin-password`）。详见 [deploy/README.md](deploy/README.md)。

## 版本与发布

```bash
./bin/llmgw -version
# llmgw <version> (commit <sha>, built <utc>)

make release   # dist/llmgw_<version>_{darwin,linux}_{amd64,arm64}.tar.gz + checksums.txt
```

版本号、提交号与构建时间通过 `-ldflags -X` 注入。

## CI / CD

- `.github/workflows/ci.yml`：push 与 PR 时执行 `gofmt` 检查、`go vet`、`go test -race`、`govulncheck`，并验证前端可构建。
- `.github/workflows/release.yml`：推送 `v*` tag 时构建四个平台的发布包，并创建 GitHub Release。
- `.github/workflows/docker.yml`：推送 `v*` tag 时构建多架构镜像并推送到 `ghcr.io/<owner>/llmgw`。

发布一个版本：

```bash
git tag v0.1.0
git push origin v0.1.0
```

## 开发

```bash
make build      # 只编译 Go，使用已提交的前端产物
make build-all  # 重建前端 + 编译（改过 web/ 后用这个）
make web        # 只重建前端
make test       # go test -race ./...
make vet
make run        # 读取 ./config.yaml
```

修改 `web/` 下的任何文件后，必须执行 `make build-all`（或先 `make web` 再 `make build`）才会嵌入新产物。
前端开发时可用 `cd web && npm run dev`，Vite 会把 `/admin/api` 代理到 `localhost:8080`。

依赖：Go 1.27+、Node 22+，无 cgo（SQLite 使用 `modernc.org/sqlite`）。

## 文档

- [需求](docs/requirements.md)
- [架构](docs/architecture.md)
- [路由](docs/routing.md)
- [管理 API](docs/api.md)
- [运维](docs/operations.md)
