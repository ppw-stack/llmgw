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

> `test-config.yaml` 里配了 `metrics_token`，所以 `/metrics` 必须带令牌访问，
> 不带返回 `401`。把 `metrics_token` 留空时，只有本机与 `trusted_proxies` 能读，
> 其他来源返回 `403`。

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

## 安全相关配置

`test-config.yaml` 里开了几项生产环境也该开的开关，方便在本地验证：

| 配置 | 值 | 作用 |
|---|---|---|
| `server.admin_enabled` | `true` | 管理面总开关，改成 `false` 会热加载关掉控制台 |
| `server.admin_allowed_cidrs` | `192.168.222.1/32` 等 | 管理面白名单，名单外访问 `/admin/api/*` 返回 `403` |
| `server.key_rpm` / `server.key_max_concurrent` | `600` / `32` | 单把网关 Key 的每分钟请求数与并发上限，超限 `429` |
| `database.retention_days` | `7` | 请求日志、审计日志与按日用量保留 7 天 |

> `192.168.222.1` 是 compose 网络的网关，也就是**宿主机**访问发布端口时容器
> 看到的来源地址。compose 默认网络每次 Docker Desktop 重启都会换网段，所以
> `docker-compose.test.yml` 里固定了 `192.168.222.0/24`，白名单才能写死。

### 验证管理面白名单

```bash
# 宿主机访问：命中白名单，控制台可用
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/admin/api/login

# 从别的容器访问：不在白名单，403
docker run --rm --network llmgw-test_llmgw-test alpine \
  wget -qO- --post-data='{"username":"admin","password":"test-admin-password"}' \
  --header='Content-Type: application/json' http://gateway:8080/admin/api/login
```

### 验证管理面热加载开关

改 `deploy/test-config.yaml` 里的 `admin_enabled`，1 秒内生效，无需重启容器：

```bash
# 改成 admin_enabled: false 后
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/admin/api/login  # 403
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/                   # 403，控制台页面也不给

# 数据面不受影响
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}' \
  http://localhost:8080/openai/v1/chat/completions                               # 200
```

改回 `admin_enabled: true` 即可恢复，已登录的会话继续有效，不用重新登录。

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
  管理面开关、管理面白名单与 Key 限速属于运行期策略，改完立即生效；其余项需要重启容器。
- 配置目录是以 `./` 挂进容器的（不是挂单个文件），这样编辑器改写文件换了 inode 之后，
  容器里仍能看到新内容——单文件绑定会钉住旧 inode，热加载会静默失效。
- 数据库位于名为 `llmgw-test_llmgw-test-data` 的卷中。
- 网关数据目录 `/data` 在容器里以 `llmgw` 用户运行，库文件权限 `0600`、目录 `0700`。
- 若 8080 被占用，修改 compose 里的端口映射即可。
- 若 `192.168.222.0/24` 与你本机已有网络冲突，改 `docker-compose.test.yml` 里的
  `subnet`，并同步改 `test-config.yaml` 的 `admin_allowed_cidrs` 第一项。

> **从旧版本升级的数据卷**：上游拓扑在首次启动时导入数据库，之后以数据库为准。
> 如果卷是在 `allow_private_upstream` 这个开关之前建的，库里的接入点没有该标记，
> 请求本地 mock 会报 `upstream host "mock" resolves only to private or reserved addresses`。
> 处理方式：`docker compose -f deploy/docker-compose.test.yml down -v` 清掉卷重建，
> 或在控制台「上游接入点」里给该接入点打开「允许内网/本地上游」。
