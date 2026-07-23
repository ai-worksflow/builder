# Sandbox 与开发预览产品边界

状态：`implemented-internal`。本文只定义仓库内产品体验和实现边界，不代表
`sandbox-golden-external` 已通过资格验收。

当前迁移链已到 `000089`。`000086` 将 Candidate journal write gate 绑定到 exact ready
Session projection，避免历史 suspended Session 错误阻塞后继 Session；`000087`–`000089`
依次收紧 absolute-TTL reconciliation、系统终止 transition 和 checkpoint guard。上述
内容是仓库内生命周期加固，不改变本文的外部资格边界。

## 用户主路径

Sandbox 只承担一个可理解的开发闭环：

1. 用户显式打开持久 Candidate；平台不因进入页面自动启动运行资源。
2. 代码视图自动保存编辑。检查点、验证和创建 Proposal 只在代码视图出现。
3. 预览视图只要求用户选择服务与 profile，并显式点击一次“Start preview”。
4. 进程启动后，平台轮询声明端口；第一个可预览且健康的端口自动打开。
5. 多端口、手动探测和切换仍可用，但不是完成普通预览的必要步骤。

## 明确分层

| 能力 | 当前产品承诺 | 不承诺 |
| --- | --- | --- |
| Candidate 编辑 | 持久化、自动保存、准确 head/fence、断线恢复 | 直接修改 Canonical Workspace |
| 开发预览 | 隔离运行、声明端口健康检查、短期 capability URL | Production 发布或公网稳定 URL |
| Candidate 验证 | exact checkpoint + VerificationReceipt 后才能创建 Proposal | 预览成功自动等于质量通过 |
| 外部资格 | UI 明示开发预览不是生产发布 | 在 Golden Receipt 缺失时展示“平台已完成” |

## 复杂能力的入口原则

- Preview 页面不展示 Agent、Terminal、验证、冻结、暂停、终止和放弃 Candidate；
  这些能力属于代码视图或运维/治理路径。
- 不为普通预览要求用户理解 Session epoch、Candidate generation、tree hash、
  writer lease 或 capability grant；这些信息只作为折叠的技术证据出现。
- 平台可以在一次显式启动后自动完成端口探测和打开健康预览，因为它们是该操作的
  直接后续；不得自动启动新的进程、重试有副作用的失败操作或选择生产发布。
- 自动选择只适用于第一个按模板声明顺序出现的健康、可预览端口。需要切换服务时，
  用户仍可从端口列表明确选择。

## 完成定义

仓库内体验完成要求：

- Preview 主路径最多一次显式启动操作；健康端口无需再次点击；
- 等待、启动、健康、停止和失败状态均有用户可理解的反馈；
- Preview 不混入 Candidate 完成治理操作；
- 开发预览始终与 Production/外部资格状态区分；
- helper 单元测试、TypeScript、ESLint 和相关浏览器回归通过。

生产资格仍以 `qualification/manifest.json` 中 `sandbox-golden-external` 的状态和签名
Receipt 为唯一依据。approved Golden TemplateRelease、目标环境端点、故障注入、凭据、
加密 trace 和不可变 Receipt 缺失时，状态必须保持 `not-qualified`。
