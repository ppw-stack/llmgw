# 贡献流程

`main` 分支受保护：只能通过 Pull Request 合并，且必须通过以下三个检查：

- `test`：gofmt、vet、race 测试、govulncheck
- `frontend`：ESLint、Prettier、类型检查、控制台构建、内嵌产物一致性校验
- `Kody Code Review`：自动代码审查

三个检查全部通过后才能合并。

## 分支

```
feat/<主题>      新功能
fix/<主题>       缺陷修复
chore/<主题>     构建、文档、依赖
```

## 提交

- 提交信息用中文或英文均可，建议 `type(scope): 摘要`。
- 一个 PR 聚焦一件事，避免混合无关改动。

## 本地检查

```bash
gofmt -l .
go vet ./...
go test -race ./...

cd web
npm ci
npm run check   # ESLint + Prettier + 类型检查
npm run build   # 类型检查 + 构建，产物输出到 ../internal/webui/dist
```

前端质量门禁说明：

- `npm run lint`：ESLint，要求 0 error、0 warning。
- `npm run format:check`：Prettier 格式检查。
- `npm run type-check`：`vue-tsc` 类型检查。
- `npm run check`：以上三项的汇总。

`web/` 使用 Husky + lint-staged：提交时只检查暂存文件（pre-commit），推送前跑全量检查（pre-push）。
Git Hook 可以被绕过，CI 才是最终门禁。

改动 `web/` 后必须重新执行 `npm run build` 并提交 `internal/webui/dist`，
CI 会校验内嵌产物与源码一致。

## 提 PR

```bash
git switch -c feat/xxx
# 改动 + 提交
git push -u origin feat/xxx
gh pr create --fill
```

三个检查全部通过后合并（仓库只允许 squash）：

```bash
gh pr merge --squash --delete-branch
```

## 部署

打 tag 会触发 Release 与 Docker 工作流：

```bash
git tag v0.1.0
git push origin v0.1.0
```
