# AI Development Rules

本仓库是 Go 网关 + Vue 3 控制台的混合仓库。下面的规则对 AI 编码行为与人工开发同等适用。

## 总体

- 遵循仓库既有架构与命名，不要引入与现有实现重复的功能。
- 不新增非必要依赖。
- 不留下未使用的代码、调试语句或临时文件。
- 不通过关闭 ESLint / TypeScript 规则来绕过错误。
- 需要修改质量配置（`web/eslint.config.mjs`、`web/.prettierrc.json`、`web/tsconfig*.json`）时，先说明原因并取得维护者认可。

## Go

- 代码必须 `gofmt` 规范，提交前执行 `gofmt -l .` 并保证无输出。
- 必须通过 `go vet ./...` 与 `go test -race ./...`。
- 错误信息使用 `fmt.Errorf` 包裹，保留上下文；不要吞掉错误。
- 密钥、Token、密码不得写入日志或返回给客户端。

## 前端（`web/`）

- 技术栈：Vue 3 + TypeScript + Vite。所有 `.vue` 使用 `<script setup lang="ts">`。
- TypeScript 严格模式是强制的：
  - 禁止 `any`；禁止 `@ts-ignore` / `@ts-nocheck`；`@ts-expect-error` 必须写明原因（至少 10 个字符）。
  - 禁止非空断言（`!`）；禁止用类型断言掩盖真实类型问题。
  - `catch` 到的变量是 `unknown`，必须显式处理（参考 `web/src/utils/error.ts`）。
  - 索引访问结果可能为 `undefined`（`noUncheckedIndexedAccess`），必须显式判空。
- Vue 规范：
  - 优先 `<script setup>`；Props 与 Emits 必须显式声明类型。
  - 派生状态用 `computed`，避免无必要的 `watch`。
  - 事件监听与副作用要在 `onUnmounted` 里清理。
  - 组件名必须是多词（`LoginView`、`ProvidersView` …），`App` 除外。
  - 表单控件必须有关联的 `<label for>` / `id`；点击遮罩关闭弹窗时，遮罩要写 `role="presentation"`，并保证有等价的键盘操作入口。
- 禁止 `console` / `alert` / `debugger`；错误信息通过界面状态展示。
- 二次确认统一走 `web/src/utils/confirm.ts`，不要在业务代码里直接调用 `window.confirm`。
- 提交前必须保证 `internal/webui/dist` 与 `web/` 源码一致：改动 `web/` 后执行 `cd web && npm run build` 并提交产物。

## 命令

```bash
# 前端（web/ 目录）
npm ci            # 安装依赖
npm run dev       # 本地开发
npm run lint      # ESLint，0 error / 0 warning
npm run format    # Prettier 格式化
npm run type-check# vue-tsc 类型检查
npm run check     # lint + format:check + type-check 全量检查
npm run build     # 类型检查 + 生产构建，产物输出到 internal/webui/dist

# Go
gofmt -l .
go vet ./...
go test -race ./...
```

改动代码后至少执行 `cd web && npm run check`；涉及前端功能时再执行 `cd web && npm run build`。

## 交付说明

- 说明改动了哪些文件。
- 列出实际执行过的检查命令及结果。
- 说明未解决的问题；不要在检查失败时宣称完成。
