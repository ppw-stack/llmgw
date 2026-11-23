# 安全策略

## 报告漏洞

请不要通过公开 Issue 报告安全问题。优先使用 GitHub 的私密漏洞报告：

https://github.com/ppw-stack/llmgw/security/advisories/new

报告时请包含：受影响版本或提交、复现步骤、影响范围，以及（如果适用）修复建议。

## 支持范围

只有 `main` 分支的最新提交会获得安全修复。发布版本请以最新 tag 为准。

## 部署提醒

- 公网部署必须由反向代理终止 TLS，并配置 `server.trusted_proxies` 与 `secure_cookie: true`。
- 用 `server.admin_allowed_cidrs` 把控制台限制到运维网段；`/metrics` 默认关闭，未设 `metrics_token` 时接口返回 `404`，设了令牌则必须携带该令牌访问。
- 怀疑控制台被拿下时把 `server.admin_enabled` 改成 `false`：1 秒内热加载生效，控制台 API、
  登录接口与控制台页面全部 `403`，数据面不受影响；处置完改回 `true` 即可恢复。
- 设置 `server.key_rpm` / `server.key_max_concurrent`：一把网关 Key 泄漏就等同于上游额度被无限制消耗。
- 上游地址默认拒绝 loopback、内网与云元数据网段。本地 mock 需在实例上显式设置 `allow_private_upstream: true`，不要在公网实例上开。
- `master.key` 与数据库同等敏感，请一并备份；数据库目录以 0700 创建、库文件 0600，备份时保持同样权限。
- 不要把真实上游 Key 写入 `config.yaml` 或提交到仓库；仓库中的 `deploy/test-config.yaml` 只包含本地 mock 的假值。
