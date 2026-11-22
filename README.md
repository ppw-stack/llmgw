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
#    admin token 在 data/admin-token.txt
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
# 创建网关 Key（明文只返回一次）
curl -X POST localhost:8080/admin/api/keys \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"laptop"}'

# 查看供应商、日志、用量
curl -H "Authorization: Bearer $ADMIN_TOKEN" localhost:8080/admin/api/providers
curl -H "Authorization: Bearer $ADMIN_TOKEN" "localhost:8080/admin/api/logs?limit=20"
curl -H "Authorization: Bearer $ADMIN_TOKEN" "localhost:8080/admin/api/usage?days=7"
```

`admin_token` 留空时首次启动自动生成，写入数据库旁的 `admin-token.txt`（0600）。

## 目录结构

```
cmd/llmgw/            入口：配置、启动、热更新、优雅退出
internal/config/      YAML 解析、校验、${ENV} 展开
internal/ingress/     路径归一化、协议判定、模型/流式元数据解析
internal/router/      查表路由与快照
internal/keypool/     上游 Key 轮询与失效标记
internal/transport/   转发、SSE 透传、usage 嗅探、错误分类
internal/store/       SQLite、异步日志、用量查询
internal/auth/        网关 Key 生成与校验
internal/observ/      Prometheus 指标
internal/server/      mux、中间件、数据面 handler、管理 API
internal/webui/       内嵌的 Vite 构建产物（go:embed）
web/                  Vue 3 + Vite 控制台源码
docs/                 需求、架构、路由、API、运维
```

## 开发

```bash
make web       # 只重建前端（产物写入 internal/webui/dist）
make build     # 重建前端 + 编译二进制
make test      # go test -race ./...
make vet
make run       # 读取 ./config.yaml
```

修改 `web/` 下的任何文件后，必须执行 `make web`（或 `make build`）才会嵌入新产物。
前端开发时可用 `cd web && npm run dev`，Vite 会把 `/admin/api` 代理到 `localhost:8080`。

依赖：Go 1.26+、Node 20+，无 cgo（SQLite 使用 `modernc.org/sqlite`）。

## 文档

- [需求](docs/requirements.md)
- [架构](docs/architecture.md)
- [路由](docs/routing.md)
- [管理 API](docs/api.md)
- [运维](docs/operations.md)
