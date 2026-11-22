# 贡献流程

`main` 分支受保护：只能通过 Pull Request 合并，且必须通过以下三个检查：

- `test`：gofmt、vet、race 测试、govulncheck
- `frontend`：控制台构建
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
cd web && npm ci && npm run build
```

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
