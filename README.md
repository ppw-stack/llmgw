# llmgw

轻量 LLM 网关：一个二进制、一个数据库文件，把多家大模型收敛成两个统一入口。

- **OpenAI 协议**：`https://gateway/openai/v1`
- **Anthropic 协议**：`https://gateway/anthropic`

客户端只配置网关地址和网关 Key，上游的 baseUrl 与 apiKey 全部留在网关里。

## 当前范围（v1）

| 能做 | 不做 |
|---|---|
| OpenAI / Anthropic 双入口，同协议直通 | 跨协议转换 |
| 按 `(协议, 模型)` 查表路由 | 规则引擎、权重、优先级、fallback、熔断 |
| 供应商 → 端点两层模型，Key 池轮询 | RPM/TPM/预算、模型白名单 |
| 上游 401/403 自动换 Key | 自动重试、自动降级 |
| 请求日志、token 与成本统计 | 多租户、计费、在线支付 |
| 配置热更新 | MCP、RAG、护栏 |
| Web 控制台：概览 / 供应商 / Key / 日志 / 接入说明 | Kubernetes Operator |
| Prometheus 指标、结构化日志 | |
## 快速开始

```bash
# 1. 构建
make build          # bin/llmgw

# 2. 准备配置
cp config.example.yaml config.yaml
$EDITOR config.yaml # 填上游 keys 与 models

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

```yaml
providers:
  - name: glm
    keys: ["${GLM_KEY_1}", "${GLM_KEY_2}"]   # 同一供应商共享 Key 池
    endpoints:
      - protocol: openai                    # 上游协议
        base_url: https://open.bigmodel.cn/api/paas/v4
        models:
          glm-4.6: glm-4.6                  # 对外模型名: 上游模型名
      - protocol: anthropic
        base_url: https://open.bigmodel.cn/api/anthropic
        models:
          glm-4.6: glm-4.6
```

- 每个请求按 `(协议, 模型)` 哈希查表，命中唯一端点。
- 同一协议下模型名重复 → 拒绝启动并报出两个来源。
- 模型只在另一个协议存在时，请求会得到 `400` 并提示改用哪个 base_url。
- 改配置后 1 秒内自动生效，在途流式请求不受影响。

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

# 查看供应商、日志、用量、审计
curl -b cookies.txt localhost:8080/admin/api/providers
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
internal/keypool/     上游 Key 轮询与失效标记
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

依赖：Go 1.26+、Node 20+，无 cgo（SQLite 使用 `modernc.org/sqlite`）。

## 文档

- [需求](docs/requirements.md)
- [架构](docs/architecture.md)
- [路由](docs/routing.md)
- [管理 API](docs/api.md)
- [运维](docs/operations.md)
