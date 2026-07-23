# 历史遗留与退役清单

状态日期：2026-07-23。

本清单区分可以立即删除的无主内容、仍承担历史读取或滚动升级职责的兼容边界，以及
尚未具备安全删除前提的旧入口。迁移文件、不可变历史记录和用于证明 fail-closed
行为的回归测试不是垃圾文件，不得为了“清理”而改写或删除。

## 已完成清理

- [x] 删除未被代码引用的占位 Logo、用户头像和占位 SVG。
- [x] 删除无生产契约引用的 YouTube production-contract fixture 及其孤立测试。
- [x] 删除前端启动时自动生成的 Taskflow 示例工作区和本地假 AI generator；工作台
  现在从空白项目开始，仅在真实生成响应到达后展示 Plan、任务和版本。
- [x] 删除 `v0.app` 生成器元数据，并把前端包名从模板名改为项目名。
- [x] 删除硬编码的 `acme` 团队路由；路由生成和加载同时校验 `teamId` 与
  `projectId`，并对路径段编码。
- [x] 清除仓库内构建探针、旧二进制和已经核对过的临时 worktree；本地 RSA 私钥
  保留，但权限限制为 owner-only 且通过 ignore 规则隔离。
- [x] 纠正前端源文件误标为可执行文件的 Git mode，只保留真实脚本的执行权限。

## 仍需保留的兼容边界

| 边界 | 当前用途 | 删除前提 |
| --- | --- | --- |
| `backend/internal/core/prototype_proposal_compat.go` | 读取历史 Prototype Proposal，并把旧事实限制在兼容表示内 | 所有持久化历史记录完成离线迁移，且读取指标连续一个发布周期为零 |
| `legacy-pre-pin/v0`、workflow v1/v2 profiles | 让已经钉住旧 descriptor 的 Run 在滚动升级期间按原语义完成 | 活跃与可恢复旧 Run 均为零，归档工具和审计导出已验证 |
| migration `000053`、`000059` 及其 guards/tests | 约束旧 AI Proposal 和 legacy deployment writer，防止与新 authority 双写 | 永久保留已应用 migration；只有新 schema baseline 仓库可折叠，且必须保留等价约束 |
| 前端内容解析器中的 `legacy*` 字段 | 只读导入历史 Blueprint、Prototype、Conversation 和 Proposal 数据 | 服务端完成 canonical rewrite，客户端遥测证明旧字段读取为零 |

这些内容不能按普通死代码处理。删除它们会改变历史事实的解释，或重新打开已经关闭的
双写边界。

## 待退役入口

### P0：测试专用的无治理实现生成路径

- [ ] 移除 `generation.Service.allowUngovernedImplementationForTests` 以及它守护的
  provider-to-Proposal 实现。
- [ ] 将 claim recovery、幂等与 AI output decoding 覆盖迁移到 governed Candidate
  入口测试，避免为了测试旧实现而在 production binary 中保留不可达 writer。
- [ ] 删除前证明所有生产构造仍然 fail closed，并确保
  `ErrGovernedCandidateRequired` 契约由当前 Candidate API 覆盖。

这一项不能只删布尔字段：现有 recovery 测试依赖其后的完整实现。直接删除会丢失关键
并发/幂等覆盖，因此必须与测试迁移在同一变更完成。

### P1：旧质量与发布 API

- [ ] 将 `frontend/lib/quality/client.ts` 从 `/quality-runs` 迁移到 Candidate /
  Canonical Verification authority。
- [ ] 将 `frontend/lib/delivery/client.ts` 的 production 行为迁移到 qualified
  Release Controller v3；旧 `/deployments` 仅允许 Preview 和历史读取。
- [ ] 增加服务端调用指标和弃用响应头；连续一个发布周期无受支持客户端写入后，
  先删除 mutation route，再在下一周期删除兼容 read route。
- [ ] 保留 `/v1/public/data/deployments/:deploymentId/*`。它是已发布数据面的资源
  地址，不等同于旧管理面 deployment writer。

### P2：本地原型与目录兼容

- [ ] 把 `frontend/lib/worksflow/mock-data.ts` 中仍被团队协作原型使用的 fixture
  收敛到测试或 Storybook；在对应 surface 接入服务端事实前不得伪装为生产数据。
- [ ] 在 catalog 持久化版本升级后移除 `legacyProject` 迁移器；必须先验证旧
  localStorage payload 能被一次性迁移或明确丢弃。
- [ ] 删除不再被界面调用的 demo 文案键；以静态引用检查和双语 key 对齐测试为准。

## 删除门禁

每个待退役项必须同时满足：

1. 新 authority 已经覆盖读、写、重试、恢复和审计语义。
2. 生产调用指标在约定窗口内为零，且没有受支持客户端依赖。
3. 数据迁移具备可重复校验和回滚/归档方案。
4. fail-closed、并发和历史读取测试已经迁移，而不是被一并删除。
5. 文档、前端客户端、HTTP route、服务注册和 readiness 检查在同一变更中更新。
