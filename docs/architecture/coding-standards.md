# 12. 编码规范（Coding Standards）

## 12.1 Go 后端

- **目录布局**: 遵循 [Standard Go Project Layout](https://github.com/golang-standards/project-layout)
- **错误处理**: 显式返回 `error`；用 `errors.Wrap` / `fmt.Errorf("...%w", err)` 添加上下文；禁用 `panic` 在业务路径
- **并发**: context 传递；context.Done() 检查；errgroup 替代 sync.WaitGroup
- **依赖注入**: wire 或手动 constructor
- **测试**: testify + ginkgo（BDD 风格）；每文件配套 _test.go；覆盖率 ≥ 70%
- **Linter**: golangci-lint (含 govet, gosimple, staticcheck, ineffassign, errcheck)
- **格式**: gofumpt（gofmt 严格版）
- **gRPC**: 全部用 connect-go（HTTP/2 + gRPC 双协议，调试友好）

## 12.2 TypeScript 前端

- **风格**: ESLint + Prettier；strict mode TS
- **组件**: 函数组件 + hooks；避免 class
- **命名**: PascalCase 组件 / camelCase 函数 / SCREAMING_SNAKE 常量
- **错误处理**: ErrorBoundary 包裹关键区；Toast 通知用户
- **测试**: Vitest 单元 + Playwright E2E
- **依赖原则**: 慎用大型 lib（每个 dep 评估 bundle 影响）

## 12.3 通用规则

- 所有用户面文案外置 i18n
- 所有 PII 在日志输出前脱敏
- 所有外部输入校验（Zod / proto schema）
- API 字段命名遵循 OpenAI 协议（snake_case）；内部 gRPC 用 camelCase（proto 默认）
- Git commit 用 Conventional Commits + Orchestrix Co-Authored-By 签名

---
