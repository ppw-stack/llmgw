# 安全策略

## 报告漏洞

请不要通过公开 Issue 报告安全问题。优先使用 GitHub 的私密漏洞报告：

https://github.com/ppw-stack/llmgw/security/advisories/new

报告时请包含：受影响版本或提交、复现步骤、影响范围，以及（如果适用）修复建议。

## 支持范围

只有 `main` 分支的最新提交会获得安全修复。发布版本请以最新 tag 为准。

## 部署提醒

- 公网部署必须由反向代理终止 TLS，并配置 `server.trusted_proxies`。
- `master.key` 与数据库同等敏感，请一并备份并限制文件权限。
- 不要把真实上游 Key 写入 `config.yaml` 或提交到仓库；仓库中的 `deploy/test-config.yaml` 只包含本地 mock 的假值。
