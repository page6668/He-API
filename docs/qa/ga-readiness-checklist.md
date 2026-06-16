# He-API — GA 前 staging 验证清单

> 来源:Phase C 全 10 Epic 冒烟测试(`docs/qa/evidence/smoke-test-epic-*.md`)汇总。
> 截至 2026-06-16,代码层面 10 Epic / 31 故事全部交付、QA 全过、Phase C 冒烟 10/10 PASS。
> 本清单是**唯一遗留**:在**本地无 docker / k8s / staging / 真实凭据**环境下无法验证、必须在 staging/CI 上跑的项。**均为环境门控,非产品缺陷。**

## 图例
- 🔴 GA 阻塞(必须在 GA 前通过)· 🟡 强烈建议 · ⚪ CI 已覆盖/可选

---

## A. 基础设施部署(Epic 1)
| ID | 级别 | 验证项 | 怎么做 |
|----|----|----|----|
| SMOKE-1-002 | 🔴 | `terraform validate` + staging `terraform plan`;K8s 部署 smoke;live DB 迁移 | 跑已存在的 `infra-lint.yml` + `db-migrate-check.yml`(183 个 live-infra AC 本地 skip)|

## B. 实时集成(真实依赖,非 mock)
| ID | 级别 | 验证项 | 怎么做 |
|----|----|----|----|
| SMOKE-5-01 | 🟡 | 限流计数 / 撤销缓存失效 against **live Redis**(本地用 miniredis)| staging 集成 lane |
| SMOKE-5-03 | 🟡 | 月上限熔断邮件 **真实 SMTP** 投递(本地仅 handler 单测)| staging notification 流水线 |
| SMOKE-2-002 | 🟡 | OAuth 真实 Google/GitHub IdP 往返 + 真实邮件验证(本地 mock)| staging 各跑一次 |
| SMOKE-4-03 | 🔴 | **6 家模型真实上游调用** + token 用量误差 <1%(Epic 4 DoD)| 夜跑 `contract-tests-live.yml` |
| SMOKE-7-001 | 🔴 | **5 支付通道 sandbox** 真实 webhook 往返 + 余额入账(Stripe/PayPal/Coinbase/支付宝/微信)| staging 逐通道 sandbox smoke |
| SMOKE-8-001 | 🟡 | 部署网关对真实上游跑一次 **流式内容安全替换** SSE | staging 单次 live SSE smoke |
| SMOKE-9-002 | 🟡 | 多模态真实上游各一次(Vision 图入 / ASR 音入 / TTS)| 本地用 fake-upstream,staging 跑真 Qwen-VL/GLM-4V/Doubao |

## C. 浏览器 E2E(Playwright,本地无运行栈)
| ID | 级别 | 验证项 | 怎么做 |
|----|----|----|----|
| SMOKE-2-001 | 🔴 | console 8 个 e2e(注册/登录/OAuth/2FA/资料/导出/**注销** + 2 套安全攻击)| staging console 跑 Playwright |
| SMOKE-9-001 | 🟡 | console 4 个 e2e(9.1 仪表盘 / 9.1b 趋势 / 9.2 日志 / 9.3 导出)| staging console |
| SMOKE-5-02 | ⚪ | 5.5 Keys 页 e2e + a11y(71 vitest 组件测试已绿)| CI/console lane |
| SMOKE-4-02 | ⚪ | 能力矩阵页 e2e | CI/console lane |
| SMOKE-6-02 | ⚪ | 6.5 路由策略 happy-path(选→存→刷新持久化)| 可选,有 live console 时加 |
| SMOKE-10-004 | 🟡 | docs 站浏览器渲染(本地仅验构建产物 `apps/docs/build/` 15 语)| serve 构建产物跑 10.5 e2e |

## D. SDK 打包/分发
| ID | 级别 | 验证项 | 怎么做 |
|----|----|----|----|
| SMOKE-10-001 | 🔴 | **Python SDK clean-room wheel 安装**(本地 pip 因 pyexpat/libexpat 符号冲突跑不了;wheel 已构建 + twine-check OK)| GitHub Actions 干净 Python 环境跑 `test_10_2_int_001` |
| (TS/Go SDK) | ⚪ | TS 39 测试绿 + npm pack 仅含 dist;Go 53 测试绿 — 已本地验证 | release-sdk-{typescript,go} 发布前确认 |

---

## E. 测试卫生(真实问题,非环境;CI 红但非产品缺陷)
> 这些不是 staging 验证项,是**应清理的测试债**,建议各起一个 housekeeping 故事。

| ID | 级别 | 问题 | 怎么修 |
|----|----|----|----|
| SMOKE-1-001 | 🟡 | 4 个 Epic-1 快照测试是**过期假阴性**(`1.2-UNIT-004/009`、`1.6-UNIT-128/147`)—— 冻结了早期不变量(精确文件数/lint 范围/占位位置),代码已正确演进超出 | 刷新 4 处断言到当前仓库形态(lint 含 ./packages;迁移数 ≥1;冷启动断言移到 `coldstart_test.go`)|
| SMOKE-3-001 | 🟡 | 流式断连测试 `3.4-INT-012` **flaky**(8 跑 2 过)—— 无延迟 mock 流在 client close 传播前就发完 10 chunks 输掉竞态;产品断连检测本身可靠(`ContextCancellation` 5/5 稳)| mock 上游在首 chunk 后阻塞直到观测到 client close(`<-released` 通道),再断言 `client_disconnected=true` |
| SMOKE-2-003 / 10-003 | ⚪ | console build 面预存在问题(非 async `maskEmail` 在 `next build`/`next lint`)| 作为 launch checklist(10.7)一部分确认 `next build`/`next lint` 干净 |

---

## 汇总:GA 真正阻塞项(🔴)
1. 基建:terraform/k8s/DB 迁移 staging 验证(SMOKE-1-002)
2. 6 家模型真实上游 token 误差 <1%(SMOKE-4-03)
3. 5 支付通道 sandbox 真实往返(SMOKE-7-001)
4. console 核心 e2e 含注销/安全攻击套件(SMOKE-2-001)
5. Python SDK clean-room 安装 CI 验证(SMOKE-10-001)

> 其余为 🟡 强烈建议 / ⚪ CI 已覆盖。所有项的 CI 工作流(`infra-lint`/`db-migrate-check`/`contract-tests-live`/`release-sdk-*`)均已存在,只需在有凭据/环境的 staging 上触发。
