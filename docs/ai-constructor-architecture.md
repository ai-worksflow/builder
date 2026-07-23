# Worksflow 真实 AI 构造器与用户沙盒架构

版本：v0.2

日期：2026-07-23

状态：目标架构草案；阶段 0–4 的平台控制面已分期落地，但外部资格与各阶段退出条件仍未满足

## 1. 文档定位与权威级别

本文定义 Worksflow 从“受治理的原型/静态实现生成”演进为“可构建真实项目级前后端代码、允许用户在线调整、能够验证并部署”的目标架构。

本文是目标架构 RFC，不代表所有章节均已实现。实施前后的事实边界如下：

- 当前实现事实以代码、数据库约束和 [`Worksflow 全栈协同生成平台架构`](./platform-architecture.md) 为准。
- 本文描述新增对象、状态机、服务和门禁；只有相应代码、迁移和验收完成后，才能把对应章节升级为“实现基线”。
- [`Worksflow AI generation product implementation checklist`](./ai-generation-product-implementation-checklist.md) 是旧产品/原型范围的历史验收记录，其中的完成项不能证明真实 PTY、远程开发沙盒、多服务 Preview 或动态后端发布已经实现。
- [`Worksflow 生成阶段工作台与团队协作原型文档`](./worksflow-generation-workbench-prototype-spec.md) 继续提供信息架构和交互意图；本文替代其中“模拟终端、浏览器内文件和静态 srcDoc Preview”作为产品化实现方式。
- 发生文档冲突时，模型不得自行选择。Constraint Compiler 必须根据本节定义的事实域判定；无法确定时进入 `blocked`。

本文使用以下规范词：

- **必须（MUST）**：不满足时不能进入下一正式状态。
- **应该（SHOULD）**：默认实现，偏离时必须有 DecisionRecord。
- **可以（MAY）**：可选能力，不得被下游误认为已实现。

## 2. 背景与问题陈述

现有平台已经能够证明“某个实现 Proposal 来自哪一组准确、不可变、已批准的需求/蓝图/PageSpec/Prototype”，但还不能证明“该 Proposal 实现了真实业务能力并可作为前后端项目运行和部署”。

此前生成结果中出现过以下明确缺口：

- 没有真实 AI Model 或后端 Conversation Service。
- 输入内容只保存在页面内存，没有服务端持久化。
- 缺少 ContractRevision 和 DataBinding 时，模型仍能返回看似完整的页面。
- `unimplementedItems` 只是说明文字，不会自动阻止 Proposal 进入应用流程。
- 用户应用 Proposal 后不知道下一步，自动保存和上游重新加载的边界不清晰。
- nullable/undefined Wire Payload 导致 `summary.trim`、`questions.length`、`tokenBindings.length`、`contractRevisions.length` 和 spread 等运行时异常。
- WebSocket 和 heartbeat 鉴权/代理问题导致断线、403 或连接建立前关闭。

这些问题不能仅通过增加 Prompt 长度或更换模型解决。目标系统必须把“规范、实现、执行证据、用户修改、质量验证和部署”做成独立、可审计且可恢复的状态机。

## 3. 当前实现基线

### 3.1 可复用能力

以下现有能力是目标架构的控制面基础，必须保留：

1. Requirements、Blueprint、PageSpec、Prototype、Contract、DesignSystem 等准确 Revision 与 content hash。
2. Canonical Review 绑定准确 Revision/Payload Hash，拒绝模糊的“已批准”状态。
3. ApplicationBuildManifest 冻结实现输入和来源血缘。
4. generation claim 的 request key、lease、fencing token、模型/Prompt/Schema hash、幂等和恢复。
5. ImplementationProposal 的文件操作、expected hash、逐项决策和 stale 检测。
6. Apply 以 CAS 原子创建不可变 WorkspaceRevision。
7. QualityReport、BuildArtifact、DeploymentVersion 和 Rollback 的不可变血缘。
8. Workflow 的 typed DAG、NodeRun、等待/失败恢复和人工门禁。
9. Outbox、NATS 领域事件和项目 WebSocket 的 cursor/reconnect 思路。
10. RBAC、项目隔离、敏感字段脱敏和公共数据面 capability。

对应基线详见 [`平台架构第 4、8、9、10、11 节`](./platform-architecture.md) 和 [`Workflow I/O contracts`](./workflow-io-contracts.md)。

### 3.2 当前限制

| 范围 | 当前事实 | 目标差距 |
|---|---|---|
| 实现生成 | 旧 `Provider.Generate` 路径仍存在；受治理路径已具备 TaskCapsule、独立 Agent worktree、Attempt/evidence 和 Merge/Undo | Agent 默认关闭，尚无 approved runner image/Golden Stack 的纵向浏览器资格证据 |
| Proposal 语义 | Constraint Compiler v7 已复用 Core 的 strict semantic authority，对 exact RequirementBaseline→Blueprint Page→PageSpec→Prototype 的 ID、Must/AC 覆盖、状态、fixture、interaction、data binding、权限和 trace 做闭包，并对 Blueprint 拥有的 API operation 与 exact API Contract 做 ID/method/path 闭包；`reference-ai-conversation/v1` 还执行跨 Data/API/Auth/AI Runtime/Deployment/Verification/RunEvent 的 profile closure。Registry 会从 exact FullStack/Release hash authority 投影可信运行时事实，Compiler 再对 TemplateManifest v1 与 Deployment v1 的可表示交集做 fail-closed 闭包 | 仍需 approved Golden 输入和真实项目 corpus 证明该约束集能在外部组合上稳定交付；Deployment v1 无法双向表达的 Template 组合只能阻塞，不能由模型推断或降级 |
| 缺口分类 | Compiler 已输出结构化 Gap/Conflict/Obligation/Waiver，Must 缺口或 Oracle 不完整会阻止 `ready` | optional/deferred waiver 的组织审批策略和完整前端治理体验仍需资格验证，不能退回自由文本放行 |
| Workspace | RepositorySnapshot/Candidate 已使用内容寻址 tree、真实 file bytes、`100644`/`100755` mode、CAS journal 和 immutable checkpoint；服务端 mutation 已支持 upsert/delete/rename，并以 exact fence/checkpoint 原子放弃 Candidate、终止其 Session；exact-head literal search 已接入 migration 000062–000065 的 immutable index、durable single-builder claim、原子 project quota 和 project-scoped composite GIN，默认限制为 `16 trees / 256 MiB / 2 active builds`；Redis query/first-builder admission 已通过同一个 app-level Redis authority 接入 search 与 secure `BuildForActor`；前端已实现 bounded `Retry-After` exact-identity 单次重试与 quota/outage no-refresh；000066 已实现 bounded retention/GC、全状态 Candidate/live-claim 保护、exact CAS/锁、shared-blob 引用保护、receipt/tombstone 和独立低权限 operator | 尚未迁移为大规模 Git pack/对象存储，也没有跨 tree/global symbol/reference index；literal index 不是 Repository authority；当前生产基线的五组稳定 `NOLOGIN` role、API/migrator/auditor/Qualification Promotion 四条独立 `LOGIN`/DSN/连接，以及另行分离的 GC/Golden-fault operator 凭据、dedicated schema、完整 app DML 与 secret injection 仍须外部预置并资格化。CredentialSet 与 qualification evidence 的 durable PostgreSQL Store 当前均 owner-only，未增加第六个生产角色或新的生产 DSN |
| 浏览器 Code | 平台 Workbench 已接入服务端 Candidate、Monaco、Diff、Problems、真实 PTY、进程、恢复检查点、project-scoped head discovery、多 head 显式选择和 strict-DTO Candidate search；已提供 binary 元数据/原字节下载、rename/delete、Candidate abandon，并完成 Production LSP v1 的 LSP-0–3 内部 ticket/Gateway/runtime/Monaco binding | 尚无 approved Golden language-server profile + 真实 ingress/WSS/browser 的 LSP-4/QA-016 资格证据；完整外部浏览器纵向证据仍取决于 approved Golden Stack |
| Preview | Sandbox 运行认证 Template command，并按声明端口签发隔离 HTTP/HMR capability | 尚未用 approved Golden Stack 完成 web/api/database 的真实浏览器纵向验收 |
| Quality | Candidate/Canonical 已有分离的 immutable VerificationPlan/Attempt/Receipt，Plan v2 可表达 Node/Python、ephemeral PostgreSQL、migration/health/tenant/contract 和 Must Oracle 覆盖；bounded output 截断会 fail closed，migration 000052 将执行资源清理提升为 exact Attempt fence 的持久 obligation | 尚无 approved Golden verifier images/attestation、真实 Playwright corpus 和 hidden-suite 生产分发 |
| Artifact | Canonical ReleaseBundle 已 fail closed 要求 deployable artifact、migration、runtime config schema、health/readiness、SBOM、vulnerability、provenance 和 signature refs | 尚无经目标 Registry/KMS 资格验证的真实 OCI/签名制品 |
| Publish | Preview→Approval→ProductionReceipt→DeploymentRevision/rollback 控制面已存在，production current head 按 project/environment single-flight 并 exact CAS；migration 000056–000061 已增加持久 v3 Operation/Attempt/Result、exact-Bundle Preview single-flight、immutable GET-only operator Case、legacy/v3 cross-writer gate、nested-hash 数据库重算和 commit-time v2 Run↔Operation authority | 仓库内没有已部署、已资格化的生产 Release Controller；Registry/KMS、cluster/RuntimeClass、Secret Broker、canary/blue-green 和可观测数据面也仍未配置/资格验证 |
| Agent 模型 | 已有 Codex Executor Adapter 和 server-side Model Gateway；TaskCapsule budget 传入 Runner，Gateway 使用保守 input upper-bound 准入、requested/output usage 证据，Runner 超出 `maxCommands` 会主动取消 | 该 input upper-bound 不是 tokenizer 精确计数；仍缺 approved runner image、真实 Provider 资格认证和跨模型 conformance |

因此，当前已形成多个平台内控制面和定向数据库/浏览器闭环，但它们不等同于通过 approved Golden Stack、真实运行时和供应链资格的项目级软件交付闭环。

## 4. 目标、成功定义与非目标

### 4.1 目标

- **AIC-GOAL-001**：从认证 TemplateRelease 创建真实、可运行的前后端项目。
- **AIC-GOAL-002**：只从准确、不可变、已批准的规范编译实现约束。
- **AIC-GOAL-003**：用户可在浏览器中查看、编辑、运行、调试和预览项目。
- **AIC-GOAL-004**：AI 和用户可在不覆盖彼此修改的前提下共同调整 Candidate Workspace。
- **AIC-GOAL-005**：AI 只能生成 Candidate Patch，不能直接创建 Canonical Revision 或部署。
- **AIC-GOAL-006**：所有正式实现由独立验证器按 exact tree hash 复验。
- **AIC-GOAL-007**：Apply 后形成不可变 WorkspaceRevision，并能构建同源全栈 ReleaseBundle。
- **AIC-GOAL-008**：Preview 验证过的相同 digest 可以晋升生产并可回滚。
- **AIC-GOAL-009**：更换 Model/Provider 后保持相同的行为验收、权限和证据标准。
- **AIC-GOAL-010**：断线、刷新、Worker 崩溃和 lease 接管不破坏 Candidate 或 Canonical Workspace。

### 4.2 成功定义

“生成成功”必须同时满足：

1. 每个 Must Obligation 有准确实现和独立 VerificationReceipt。
2. 没有 blocker、未知引用、越权路径、Secret 泄露或未批准迁移。
3. 前端、后端、数据和部署契约通过。
4. 浏览器关键用户路径通过。
5. Proposal 绑定 exact CandidateSnapshot、Base WorkspaceRevision 和验证证据。
6. Apply 后的 WorkspaceRevision 与通过验证的 tree hash 完全一致。
7. ReleaseBundle 与该 WorkspaceRevision、BuildContract 和 TemplateRelease 完全一致。

模型说“已实现”、模型自己编写的测试通过、页面能渲染或静态 Build 成功，都不能单独满足成功定义。

### 4.3 MVP 非目标

- 不同时认证 `templates` 仓库中的全部 11 个模板。
- 不支持任意基础镜像、任意 Dockerfile、任意云厂商或任意 IaC。
- 不给 Agent 或用户终端 Docker daemon、宿主机、平台数据库或生产凭据。
- 不允许 Agent 自主批准、Apply、Push、合并或部署生产。
- 不提供无限制公网、任意端口反代或长期常驻进程。
- 首版不做多人逐字符 CRDT；采用单写入者租约和多人只读观察。
- 不追求不同模型生成逐字相同的源码。
- 不把 Git commit、浏览器 autosave 或 Candidate checkpoint 当作 Canonical WorkspaceRevision。

## 5. 核心决策

| Decision ID | 选择 | 原因与约束 |
|---|---|---|
| AIC-DEC-001 | Constraint Compiler 的 BuildContract 是编码阶段唯一规范输入 | 模型不得直接在冲突的原始文档之间选边 |
| AIC-DEC-002 | WorkspaceRevision 仍是 Canonical 实现事实；Git tree 是存储和开发载体 | 避免 Git 分支和平台 Revision 形成两套权威 |
| AIC-DEC-003 | Candidate 是服务端可变工作副本，Autosave 不创建 Proposal/Revision | 保证用户体验和正式治理边界分离 |
| AIC-DEC-004 | 用户与 Agent 使用独立 tree/worktree，通过 CAS/三方合并汇入 Candidate | 防止 AI 静默覆盖用户修改 |
| AIC-DEC-005 | MVP 使用单写入者租约；CRDT 后置 | 先保证文件 rename/delete/binary/终端的一致性 |
| AIC-DEC-006 | 首版 Agent Driver 使用短生命周期 `codex exec`；SDK 后置 | CLI 已支持 ephemeral、JSONL、output schema 和 sandbox |
| AIC-DEC-007 | Model 调用统一经过 Model Gateway | Provider Key 不进入 Agent 或用户终端 |
| AIC-DEC-008 | 本地实施使用 Compose；生产沙盒和 Preview 以 Kubernetes Pod/Namespace 为参考目标 | 隔离、配额、端口代理和生命周期可治理 |
| AIC-DEC-009 | Quality Sandbox 与 Interactive Sandbox 分离 | 用户/Agent 执行环境不能给自己的结果签发最终证明 |
| AIC-DEC-010 | Proposal 默认按 Task Changeset 原子批准 | 部分文件接受可能破坏依赖闭包 |
| AIC-DEC-011 | 发布物是 OCI/Static/Migration 组成的 ReleaseBundle | 保留静态发布兼容并支持真实动态服务 |
| AIC-DEC-012 | Preview 到 Production 晋升相同 digest，禁止重新构建 | 避免环境间供应链漂移 |
| AIC-DEC-013 | 首个 Golden Stack 为 FastAPI + React shadcn-style + PostgreSQL | 与模板决策矩阵的 AI/data 产品方向一致 |
| AIC-DEC-014 | OpenAPI 是首个全栈模板组合的接口真相源 | 防止前后端路径、DTO 和错误结构漂移 |

上述决策如需改变，必须创建新的 DecisionRecord，说明迁移、兼容和验收影响。

## 6. 权威事实与冲突处理

不同制品只在自己的事实域内权威：

| 事实域 | 权威来源 |
|---|---|
| 业务目标、Must/Should、验收 | RequirementBaseline 和 AcceptanceCriterion |
| 功能、页面、API/Data/Permission 结构 | Blueprint |
| 页面 route、user goal、状态、数据绑定 | PageSpec |
| 视觉布局、Layer、Breakpoint、声明式交互 | Prototype |
| API 请求响应和错误 | OpenAPI/AsyncAPI ContractRevision |
| 数据实体、字段、索引、迁移 | DataContractRevision |
| 身份、租户、权限、策略 | Auth/Permission ContractRevision |
| AI 会话、流式协议、配额、数据保留 | AI Runtime ContractRevision |
| 工具链、目录、命令、端口、构建输出 | TemplateRelease |
| 环境、健康、迁移顺序、发布策略 | DeploymentContractRevision |

约束：

- PageSpec 与 Prototype 冲突时，PageSpec 对 route/state/data binding 权威；Prototype 对布局权威。无法按事实域拆分时阻塞。
- Requirement/AC 与任何下游制品冲突时阻塞，不允许模型降低 Must 等级。
- Template 默认行为不得覆盖业务、API、数据、认证或部署 Contract。
- 原始文档正文只作为 BuildContract 的可追溯来源；Agent Task 只消费冻结的 BuildContract 和 ContextPack。
- 冲突、缺失 Must Contract 或模板不兼容时，BuildContract 只能进入 `blocked`。

Waiver 规则：

- `must` 默认不可 waiver；只有 Obligation 明确声明 `waivable: true` 时才可申请。
- 租户隔离、Secret、Canonical lineage、Sandbox 边界、生产凭据和精确批准不可 waiver。
- Waiver 必须包含原因、范围、批准者、过期时间和替代验证；BuildContract hash 必须包含 Waiver。
- Solo Owner 可以在项目策略允许时批准普通 Waiver/Proposal，但不能绕过确定性门禁；生产晋升和破坏性迁移必须二次确认、重新认证并填写原因。

## 7. 总体架构

~~~text
批准的 Requirement / Blueprint / PageSpec / Prototype / Contracts
                                |
                       Constraint Compiler
                                |
                ApplicationBuildContract + ObligationGraph
                                |
认证 templates.git -> TemplateRelease Registry -> RepositorySnapshot
                                |
                    Deterministic DevelopmentTask DAG
                                |
          +---------------------+----------------------+
          |                                            |
Browser IDE <-> Sandbox Gateway <-> CandidateWorkspace |
Monaco / PTY / Process / Port / Preview                |
          |                                      Agent Runner
          |                               Codex / future adapter
          +---------------------+----------------------+
                                |
                 Platform-collected Patch / RunReceipt
                                |
        Independent Quality: schema/build/contract/integration/E2E/security
                                |
                    ImplementationProposal
                                |
                 Canonical Review + CAS Apply
                                |
                    Immutable WorkspaceRevision
                                |
          OCI Images + MigrationArtifact + SBOM + Provenance
                                |
                 Preview -> Approval -> Production
~~~

### 7.1 服务边界

| 服务/组件 | 责任 | 不允许做的事 |
|---|---|---|
| Constraint Compiler | 编译 BuildContract 和 ObligationGraph | 运行模型、猜测缺失合同 |
| Template Registry | 认证和提供不可变 TemplateRelease | 运行时跟随 branch tip |
| Repository Service | 保存 Git objects/tree、Candidate 和 Snapshot | 把普通 Git commit声明为 Canonical |
| Sandbox Manager | 创建、暂停、恢复和销毁交互沙盒 | 批准 Proposal 或发布 |
| Sandbox Gateway | 文件、PTY、进程、端口和 Preview 代理 | 暴露 Docker socket/任意 upstream |
| Agent Orchestrator | 生成 TaskCapsule、claim、lease、attempt 和修复循环 | 信任模型自报的 Diff/测试 |
| Model Gateway | Provider 鉴权、模型策略、额度和审计 | 向 Workspace 暴露上游密钥 |
| Quality Service | 独立执行完整验证并签发 Receipt | 读取 Agent 自报结果作为通过事实 |
| Proposal/Revision Service | Review、CAS Apply 和不可变 Revision | 接受 stale base/payload |
| Release Service | 构建 ReleaseBundle、Preview、Promote、Rollback | 从不同源码重建生产版本 |

## 8. 领域模型与不变量

### 8.1 可变与不可变对象

| 对象 | 可变性 | 权威用途 |
|---|---|---|
| TemplateCandidate | 可变/外部 | 待准入模板输入 |
| TemplateRelease | 不可变 | 认证模板事实 |
| ApplicationBuildContract | 不可变 | 实现规范事实 |
| RepositorySnapshot | 不可变 | 项目初始/基准 tree |
| SandboxSession | 有状态、临时 | 交互运行环境 |
| CandidateWorkspace | 可变、可恢复 | 用户与 AI 的开发工作副本 |
| CandidateSnapshot | 不可变、非 Canonical | checkpoint、diff 和恢复 |
| DevelopmentTask/TaskCapsule | 不可变 | 单次编码任务边界 |
| AgentAttempt | append-only | 一次模型/Runner 尝试 |
| VerificationReceipt | 不可变 | exact tree 的验证证据 |
| ImplementationProposal | 版本化、决策后不可改写 | 待审实现 Changeset |
| WorkspaceRevision | 不可变、Canonical | 项目实现事实 |
| ReleaseBundle/Manifest | 不可变 | 可部署制品事实 |
| DeploymentRevision | 不可变 | 环境发布事实 |

### 8.2 TemplateRelease

TemplateRelease 至少包含：

~~~json
{
  "id": "tplrel_...",
  "schemaVersion": "template-release/v1",
  "source": {
    "repository": "https://github.com/ai-worksflow/templates.git",
    "branch": "python-fastapi-template",
    "commit": "1721440b33563b45192ffbb15da724d11f5f158f",
    "treeHash": "sha256:..."
  },
  "services": [],
  "toolchains": [],
  "commands": {},
  "ports": [],
  "healthChecks": [],
  "migration": null,
  "buildOutputs": [],
  "extensionPaths": [],
  "protectedPaths": [],
  "environmentSchema": [],
  "lockfileHashes": [],
  "sbomDigest": "sha256:...",
  "licenseDigest": "sha256:...",
  "evidenceRefs": [],
  "signature": {},
  "status": "approved"
}
~~~

不变量：

- branch 只用于人类可读来源，运行时必须按 commit/tree hash 获取。
- `status=approved` 只能由准入流水线根据证据计算，不能由模板作者直接声明。
- Manifest、命令、端口和工具链镜像必须受 Schema 校验。
- protected paths 由平台强制，不能仅依赖仓库 `AGENTS.md`。

生命周期：

~~~text
candidate -> validating -> approved | rejected
approved -> deprecated | revoked
~~~

TemplateRelease 内容始终不可变；deprecate/revoke 只改变注册表可选策略。`revoked` 默认阻止新项目和新 Release 构建，已发布版本是否继续运行由独立安全 DecisionRecord 决定。

### 8.3 ApplicationBuildContract

~~~json
{
  "schemaVersion": "application-build-contract/v2",
  "compiler": {"version": "...", "hash": "sha256:..."},
  "sourceRevisions": [],
  "templateReleaseRefs": [],
  "routes": [],
  "states": [],
  "apiContracts": [],
  "dataContracts": [],
  "authContracts": [],
  "aiRuntimeContracts": [],
  "prototypeConstraints": [],
  "deploymentContract": {},
  "acceptanceCriteria": [],
  "obligations": [],
  "waivers": [],
  "conflicts": [],
  "forbiddenClaims": [],
  "contentHash": "sha256:..."
}
~~~

每个 Obligation：

~~~json
{
  "id": "OBL-AC-123",
  "level": "must",
  "kind": "api",
  "sourceRevision": {},
  "sourceAnchorId": "AC-123",
  "oracleIds": ["contract-test-api-123"],
  "dependsOn": [],
  "waivable": false,
  "status": "ready"
}
~~~

`must` Obligation 缺少契约、Oracle 或可实现目标时，BuildContract 必须阻塞。

### 8.4 CandidateWorkspace

CandidateWorkspace 至少保存：

- project、BuildManifest 和 exact base WorkspaceRevision。
- base/current tree hash。
- TemplateRelease 和 BuildContract hash。
- FileJournal sequence 和修改归属。
- active writer lease 和 session epoch。
- latest CandidateSnapshot。
- dirty、conflicted、stale、rebaseRequired 独立属性。
- TTL、持久化和归档策略。

`dirty/conflicted/stale` 不是互斥状态，不能压缩为单一枚举。

### 8.5 TaskCapsule

每个 TaskCapsule 必须限定：

~~~json
{
  "schemaVersion": "agent-task-capsule/v1",
  "taskId": "task_...",
  "baseCandidateTreeHash": "sha256:...",
  "buildContractHash": "sha256:...",
  "templateReleaseHashes": [],
  "objective": "...",
  "obligationIds": [],
  "acceptanceCriterionIds": [],
  "readSet": [],
  "writeSet": [],
  "protectedPaths": [],
  "contextRefs": [],
  "preconditions": [],
  "postconditions": [],
  "verificationCommandIds": [],
  "allowedTools": [],
  "networkPolicy": {},
  "budgets": {},
  "outputSchemaHash": "sha256:..."
}
~~~

模型不得改变 Task DAG、TemplateRelease、BuildContract、write-set 或验证命令。

### 8.6 AgentAttempt 与 VerificationReceipt

AgentAttempt 必须记录 provider/model、参数、Runner/Codex/toolchain image digest、Task/Prompt/Context hash、base tree hash、事件流、平台采集 Diff、stdout/stderr 引用、资源消耗、退出原因、parent attempt 和 retry reason。

VerificationReceipt 必须记录 verifier/version/image digest、exact input tree hash、检查项、命令、退出码、日志、AC/Obligation 覆盖、flake/retry、SBOM/Secret/Vulnerability 结果和最终 blocking decision。

模型总结不属于执行事实；Diff、退出码、日志和 hash 必须由平台采集。正常 Candidate/Canonical Receipt 还必须绑定的每个 exact Attempt fence 都已有 `completed` cleanup obligation；进程结束、检查结束或内存中的 defer 成功都不能单独替代该持久事实。

### 8.7 ReleaseManifest

ReleaseManifest 至少包含：

- exact WorkspaceRevision、BuildContract、TemplateRelease。
- 每个服务的 OCI/static artifact digest。
- MigrationArtifact。
- Runtime/Environment Schema 和 Secret References；不能包含 Secret Value。
- SBOM、Vulnerability Report、Provenance、Signature。
- Health/Smoke/E2E 定义和 Receipt。
- Preview DeploymentRevision。
- Promotion 和 Rollback lineage。

### 8.8 存储职责

物理存储可以分阶段替换，但逻辑职责不得混用：

| 存储 | 权威内容 | 禁止用途 |
|---|---|---|
| PostgreSQL | Session/Task/Attempt/Proposal/Revision/Release 元数据、状态、版本、lease、ETag、幂等键和引用 | 保存大体积 workspace/日志正文 |
| Content-addressed Blob Store | Git objects/tree、CandidateSnapshot、Patch、日志、Receipt、SBOM 和制品 | 决定业务状态转换 |
| Redis | 短时连接 ticket、presence、rate limit、热 cursor 和 session cache | 保存不可恢复的 Canonical 状态 |
| NATS/Outbox | append-only 领域事件分发和异步触发 | 作为对象最终状态的唯一来源 |
| Sandbox Volume | 当前 Session 的可写物化 tree、依赖和进程文件 | Session 销毁后的唯一 Candidate 副本 |

MVP 可以复用当前 `content.Store`/Mongo-compatible 内容存储承载 bounded Snapshot/Receipt；项目级 Git pack、OCI 和大日志应通过 Blob Store 接口迁移到 S3/MinIO-compatible 存储。Sandbox Suspend/Terminate 前必须把 dirty journal 和最新 checkpoint 持久化到 Pod 外部。

## 9. Template Release 供应链

### 9.1 本次审计快照

初次审计：2026-07-16；远端 HEAD 与 tree digest 最近复核：2026-07-19。

来源：`https://github.com/ai-worksflow/templates.git`。

- main：`1edacd73910415c0e0e0429e60e09714a873776d`，tree digest
  `sha256:27bc44f3e5f8a5c5cb4effa51ad1933c187386eae578824c263d234e6f4d3f36`。
- FastAPI：`1721440b33563b45192ffbb15da724d11f5f158f`。
- React shadcn-style：`72664c5dc5cced39bc185f2f7e08dc6652a80ee3`。
- main 是索引，11 个模板位于独立分支。
- 索引 commit 与审计时对应 branch HEAD 一致。

当前阻塞项：

1. 索引、README、Usage 和 Schema URL 仍引用 `jfcwrlight/templates`，不是实际仓库。
2. 11/11 `template.json` 未通过仓库自己的 `TEMPLATE_MANIFEST.schema.json`。
3. main CI 没有形成可证明每个模板 commit 的完整准入证据。
4. npm lock 大量引用非目标 Registry；Python 模板缺少可复现 lock/hash。
5. 根 LICENSE、统一 SPDX、SBOM、签名和 attestation 不完整。
6. 只有少数模板有基础 Dockerfile，没有统一生产部署契约。
7. FastAPI 默认持久化仍是内存 Repository；React API Client 很薄。
8. 推荐前后端的 path、CORS、dev proxy 和 OpenAPI 尚未组合闭环。

因此 `templates` 只能作为 TemplateCandidate 来源，不能由 Agent 直接 clone 后声明可用。

### 9.2 准入流水线

~~~text
templates.git exact commit
  -> repository identity / tree hash
  -> manifest schema
  -> license / SPDX
  -> dependency lock / registry policy
  -> install / lint / type / unit / build
  -> start / health / contract smoke
  -> container build / secret / SBOM / vulnerability
  -> signature / attestation
  -> approved TemplateRelease
~~~

准入失败必须产生结构化 finding，不得仅更新一份 `ready` 文本。

### 9.3 FullStackTemplate

首个组合布局：

~~~text
project/
  apps/web/
  services/api/
  contracts/openapi.yaml
  packages/api-client/
  deployment/
  tests/
  AGENTS.md
  templates.lock.json
~~~

`templates.lock.json` 保存 repository、branch、commit、tree hash、Manifest/Profile/lock/SBOM hash、Constructor version、验证证据和签名。

OpenAPI 是 API 唯一真相源。后端实现、前端类型 Client、契约测试和 API drift 检查都从准确 ContractRevision 派生。

## 10. Constraint Compiler 与任务图

### 10.1 编译流程

1. 读取准确且已批准的 RequirementBaseline。
2. 读取选中 DeliverySlice 的 Blueprint/PageSpec/Prototype。
3. 读取 API/Data/Auth/AI/Deployment ContractRevision。
4. 读取认证 TemplateRelease 和当前 WorkspaceRevision。
5. 按事实域解析约束；产生冲突和缺失清单。
6. 为每个 Must AC 生成 Obligation 和 Oracle 引用。
7. 编译 BuildContract 并计算 canonical hash。
8. 只有 `ready` BuildContract 才能进入 Task Planner。

BuildContract 状态机：

~~~text
compiling -> blocked
compiling -> ready
ready -> superseded
~~~

### 10.2 确定性任务图

默认 DAG：

1. 固定 FullStackTemplate 和项目组合。
2. 编译共享 API/Data/Auth/AI Contract。
3. 数据模型和 Migration。
4. 后端 domain/application/ports。
5. 后端 adapter/controller。
6. 平台生成 OpenAPI Client。
7. 前端 route/state/data access。
8. 页面和 Prototype 交互。
9. 集成、浏览器、可访问性和安全测试。
10. Deployment 描述和运行时检查。

只有依赖完成且 write-set 不重叠的 Task 才能并行。MVP 默认串行，以减少 stale/merge 风险。

### 10.3 ContextPack

Agent 不接收“所有文档 + 所有代码”的大 Prompt。ContextPack 必须由服务端按 TaskCapsule 裁剪：

- 精确 source ref/hash 和相关内容。
- 相关代码文件、符号索引和依赖边。
- Template engineering rules 和 extension points。
- API/Data/Auth/AI Contract 片段。
- Prototype 的目标 state/layer/breakpoint。
- AC/Obligation 和验证命令。
- 已知失败日志和上一 Attempt Diff。

向量检索只能辅助定位，不能替代稳定 ID 和 exact Revision。

## 11. Repository 与 Candidate 模型

### 11.1 Canonical 与 Git 的关系

- Repository Service 使用 Git objects/tree 或等价内容寻址存储支持项目级源码、binary 和 file mode。
- WorkspaceRevision 保存 exact tree hash、canonical content hash、父 Revision 和 Proposal lineage。
- Git branch/commit 是开发和外部集成载体；没有平台 Apply 的 Git commit 不是 Canonical。
- 现有 JSON Workspace 可通过确定性 adapter 物化为 Git tree，保持历史 hash 引用。

### 11.2 Candidate 生命周期

~~~text
base WorkspaceRevision Wn
  -> create Candidate C(tree=Wn)
  -> user/AI edits + journal
  -> CandidateSnapshot C1
  -> verification
  -> freeze Proposal P(base=Wn, candidate=C1)
  -> review
  -> CAS apply
  -> WorkspaceRevision Wn+1
~~~

Candidate 可能同时为 dirty、conflicted 和 stale。上游变化只设置 `rebaseRequired`，不能自动替换文件。

显式放弃是另一条 terminal 分支：active Candidate 必须先满足 exact checkpoint 约束，随后由 migration 000050 在一个数据库事务内提交 Candidate `abandoned` control event 和 SandboxSession `ready -> terminating` mutation fence。runtime 清理成功后 Session 才进入 `terminated`；清理失败保留可恢复的 `terminating + abandoned`，由持久化 `abandon_cleanup` lease 接管，而不是重复写入 Candidate terminal event。

### 11.3 保存语义

- **AIC-SAVE-001**：浏览器编辑必须写服务器 Candidate journal，不能只存在页面内存/localStorage。
- **AIC-SAVE-002**：Autosave 不重新读取或替换 Blueprint、PageSpec、Prototype、BuildManifest。
- **AIC-SAVE-003**：Autosave 不创建 Proposal、WorkspaceRevision 或 Canonical Review。
- **AIC-SAVE-004**：Checkpoint 是可恢复 CandidateSnapshot，不是正式 Revision。
- **AIC-SAVE-005**：Freeze 后 Proposal 绑定 exact Candidate tree hash。
- **AIC-SAVE-006**：Freeze 后继续编辑必须形成新 CandidateSnapshot/Proposal payload。
- **AIC-SAVE-007**：Apply 必须验证 base WorkspaceRevision、base tree hash、Proposal version/payload hash。
- **AIC-SAVE-008**：Apply 成功才创建新的 Immutable WorkspaceRevision。
- **AIC-SAVE-009**：用户能撤销 AI Patch、恢复 Checkpoint、查看每项修改归属。

### 11.4 AI 与用户并发

MVP 使用单写入者租约：

- 用户输入和 Agent Patch transaction 不能同时写 Candidate。
- Agent 从 `baseCandidateTreeHash` 创建独立 worktree。
- Agent 运行期间用户可以继续在自己的 Candidate 编辑；Agent 完成后按 base/current/agent 三方合并。
- tree hash 不一致时不得 last-write-wins。
- 同文件冲突进入显式 Conflict UI；解决后产生新的 journal entry 和 tree hash。
- 多人首版为一个编辑者、多个观察者；可请求控制权。

### 11.5 Exact-head literal search 与 durable exact-tree index

`POST /v1/projects/{projectId}/repository-candidates/{candidateId}/search` 是项目源码导航的只读入口。Actor 只取自认证会话并要求 project view 权限；请求必须携带当前 Candidate 的 exact `expectedHeadGeneration` 与 `expectedRootHash`，不能从客户端缓存或“最新时间”推测 head。

Repository Candidate aggregate、canonical tree 和 FileBlob 始终是搜索权威；PostgreSQL index 只是 exact-tree 派生加速器：

- 无 include glob 且 query 具有可用 trigram 时，服务查询 project + opening Candidate tree 的 ready index；exact tree 首次缺少 manifest 时按需构建一次，再执行同一查询。只有纯 `not ready` 能触发构建；持久化冲突、篡改或数据库异常必须 fail closed。
- builder 按 deterministic tree path 解析该 exact Candidate tree 的每个 content-addressed blob，并逐项重验 project、mode、content hash、byte size 和 raw bytes。binary/invalid UTF-8 仍作为 tree member 写入 commitment，但不保存可搜索正文。
- migration `000062_repository_exact_tree_literal_index` 在 PostgreSQL 保存 tenant-scoped manifest/member/content-hash deduplicated text blob，以 advisory transaction lock 原子完成 `building → ready`；ready manifest、member 和 blob 由 trigger 禁止 update/delete/truncate，并在查询时重算完整 member/tree/index commitment。
- migration `000063_repository_exact_tree_literal_index_build_claims` 把 single-builder 协调持久化为 project + tree 的 owner token、attempt 和 expiring lease。claim 必须在任何 FileBlob resolve 前取得，owner 以 heartbeat 续租；waiter 不持有长事务/连接，crash owner 只能在 expiry 后由更高 attempt 接管，旧 owner 无权发布。
- migration `000064_repository_exact_tree_literal_index_project_quota` 在 claim CAS 内、任何 source resolve 前，按 project advisory lock 原子计算 ready manifests 与 live reservations，并预留当前 tree 的 logical source bytes。默认上限固定为每项目 `16 trees / 256 MiB logical source bytes / 2 active builds`；tree、source-byte 或 active-build quota 任一不足都直接拒绝，不进入扫描 fallback。
- migration `000065_repository_exact_tree_literal_index_project_gin` 用 `project_id + body trigram` 和 `project_id + ASCII-folded body trigram` composite GIN 替换跨项目正文 posting list，使 project fence 在索引访问阶段生效；它不放宽最终 member、commitment 或 authoritative-byte 重验。
- migration `000066_repository_exact_tree_literal_index_gc` 增加 append-only run/capability/receipt/tombstone authority，并按项目对所有 ready publication 排名。retention 至少 7 天（默认 30 天）、每项目至少保留 8 个（默认 8 个）、batch 为 1–100（默认 25）、capability TTL 不超过 15 分钟（默认 10 分钟）。所有 Candidate status 的 current tree 和 live build claim 都受保护；执行以 exact tree + project quota advisory lock、完整 manifest/publication CAS 和数据库时钟重新判定资格。
- Redis query/first-builder admission 已形成并接入运行时的原子双层 token-bucket：app 只构造一个 Redis admission authority，并把同一实例同时注入 Candidate search 与 secure exact-tree builder。query 默认 project `20/s, burst 40` + actor `4/s, burst 8`；first-builder 默认 project `1/15s, burst 2` + actor `1/30s, burst 1`。
- index 只返回按 canonical path 排序、受 `500 documents / 8 MiB candidate bytes` 约束的候选文件，不返回可直接信任的位置或 preview。搜索服务把每个候选重新对照 opening tree，再从 FileBlob authority 读取 raw bytes、重验 content hash 并做最终 literal match。
- 只支持 literal query；case-insensitive 模式限 ASCII。每个请求严格按 normalize → project view authorization → project + actor query admission 执行，且 admission 位于任何 Candidate Repository I/O 之前；短 query、无可用 trigram 或带 include glob 的请求也不例外。这些形态通过 admission 后才可进入 deterministic bounded scanner，最多扫描 `2,000` 个文件、`8 MiB` 原始字节并返回 `500` 个 match，触顶返回 `truncated=true`。索引构建只允许走 actor-bound secure `BuildForActor`；只有 durable PostgreSQL claim 返回首次实际 owner 后、且在 FileBlob resolve 前，才扣 first-builder token，ready reuse、waiter 和并发 follower 不扣 build token。
- 两条路径都把非 UTF-8、NUL 或非法 control-byte 内容视为 binary，并在完成后重新读取同一 Candidate；generation 或 root hash 有任何变化均返回结构化 `409 repository_search_head_changed`，不采用旧结果。
- quota/rate denial、malformed admission result、Redis unavailable/timeout/corrupt state、non-ready conflict、commitment mismatch、tenant drift、tamper 和 PostgreSQL 异常都不得选择 bounded scanner；scanner 只是一种 short/no-trigram/glob 查询计划，不是错误降级路径。合法 query/first-builder denial 映射为带 `Retry-After` 的 `429`，active-build quota 映射为 `429`，retained tree/source-byte quota 映射为 `409`，未知 index failure 映射为 `503`。前端仅接受 1–3600 秒的 `Retry-After`，按 exact query/head identity 最多自动重试一次；quota `409` 与 outage `503` 不刷新 Candidate、Blueprint 或 dirty editor。

前端以 strict `repository-candidate-search/v1` DTO 解析完整 head、limits、stats、matches 和每个 content hash；除“无 include filter”的明确 wire 兼容外，missing/null/widened/inconsistent 字段均使整个响应 fail closed。Client 还把 project/Candidate/generation/root/query/case/glob/max-match 与原请求逐项比对。打开 match 前再次比对当前 Candidate generation/root，文件读取返回后再核对 session fence、tree hash 和 match content hash。

Workbench 使用 350ms debounce，并用 `AbortController` 取消被新 query、head/save/rebase 状态取代的请求。dirty、saving 或 Candidate mutation 期间搜索与打开 match 均暂停；`409` 只刷新所选 Session/Candidate/tree 投影、清除 stale results 后重新搜索，不覆盖当前 dirty draft，也不重新加载 Blueprint、PageSpec、Prototype 或 BuildManifest。

GC 的数据删除权威只存在于 operator-only `SECURITY DEFINER` 函数。两个 canonical short auth table 在执行事务内写入 exact xid/backend PID/project/tree/capability/blob 事实，mutation guard 必须逐项匹配；成功返回前清空，失败则随事务回滚。executor 在 exact-tree/project lock 下先锁住 claim row，再以锁后数据库时间判断 lease：先提交的 renew 必须得到 `protected`，排在 GC 后面的 renew 不能在旧 claim 被删后报告成功。删除 member/manifest 后，blob 只有在同项目没有其他 member 引用相同 content hash 时才会删除。terminal outcome 只能是 `deleted`、`protected`、`stale` 或 `expired`；每个 capability 恰好一个 immutable receipt，tombstone 绑定 publication identity + capability，允许同 tree 后续正常重建。down migration 先排他锁住六张 GC control/audit/auth 表，并在任一 run/capability/receipt/tombstone/auth fact 存在时拒绝销毁审计。

`repository-index-gc` 是独立一次性进程和 Compose `maintenance` profile，不在 API worker 内。它要求 operator 专用 DSN、显式 canonical `-postgres-schema` 与 scheduler 生成的稳定非零 `-run-id`。进程崩溃或结果不确定时，必须以相同 run ID 和不变 policy 重放；数据库返回同一 capabilities/receipts，并拒绝 same-ID/different-policy。旧 run 尚未对账前不能创建替代 run；完全终态后的下一调度批次才使用新 ID。

PostgreSQL 生产边界固定为 `worksflow_migration_owner`、`worksflow_application`、`worksflow_repository_index_gc_operator` 三个互相隔离的 `NOLOGIN` group role，以及分别只继承一个 group、没有 `ADMIN OPTION` 的 migrator/API/operator 真实 `LOGIN`。`000066` 在任何 mutation 前拒绝 partial/elevated stable-role trio、stable role 出向 membership、任意入向 `ADMIN OPTION` 以及 trusted schema 的任意显式 column ACL；全缺失角色只允许隔离本地开发。通过后才撤销 trusted schema 的全部 `PUBLIC` relation/sequence/routine ACL，把 schema、全部 tables/sequences 和 23 个受控 routines 归属精确 migration-owner。application 继续获得 predecessor `SECURITY INVOKER` 执行面，但只拥有四个 index table 的准确 DML、`schema_migrations` 只读和十个可执行 Candidate/build-claim `SECURITY DEFINER` 函数；operator 没有直接对象权限，只能执行四个 GC plan/execute/inspect/readiness 函数。外露十四个 definer 使用固定 `pg_catalog, <trusted schema>, pg_temp`；第 23 个 exact-signature Sandbox checkpoint dependency 是 SQL/STABLE `SECURITY INVOKER`、返回单 boolean、固定同一路径，只向 migration-owner 与 application 授予不可转授的执行权。八个 internal trigger/guard routine 仅 owner 可执行，readiness 会拒绝任一额外 grantee。

API 不运行 migration。migrator 使用独立 DSN/schema 先建立 ledger：原 up checksum 保持兼容，每个 canonical down pair 另存 SHA-256；旧库只有 exact ordered prefix 可一次性补齐 `NULL` down digest。API 以自己的 DSN/schema 纯只读验证完整 pair，缺列、orphan、unknown 或 drift 均阻塞；role posture 与 operator 还遍历 login 的全部 inherited/`SET ROLE` reachable roles，验证零 role-delegation/column ACL、owner、DDL、relation/sequence/routine ACL 和 exact RETURNS TABLE contract。DSN query 不能覆盖 `role`、`options`/`search_path` 或凭据身份。生产三个 group role 必须在应用 `000066` 前由外部 provisioner 建立，因为 all-absent 本地姿态下的 migration grants 是 conditional 且已记录版本不会重跑；若顺序错误，只能补新的 reviewed migration，不能编辑旧 SQL/checksum。三个 LOGIN、dedicated schema/database ownership、全量 app table-level DML 和 secret injection 也必须外部预置。本地 Compose 的共享 owner/`public` schema 不构成生产角色资格；共享 Compose 必须显式设置 `APP_ENV=staging|production` 才启用 posture。

`000062`–`000066`、已经接线的 Redis admission 和独立 GC operator 共同构成 durable、immutable、project-bounded Candidate-tree literal search/retention 的 `implemented-internal` 能力。真实 PostgreSQL + Redis 的 focused Repository admission/index 回归已通过（95.205s）；focused GC migration、API posture和真实低权限 operator LOGIN/中断同-run恢复也有对应 canary。它们仍只是仓库内证据，不证明目标生产数据库的角色、secret 或完整应用 DML 已配置。索引不是 Git pack/object-store、跨 tree code graph 或 global symbol/reference index；symbol/reference/diagnostic 等只读语义由 12.6 的 LSP 内部实现提供，且这些内部能力不能替代 approved Golden 或 LSP-4/QA-016 外部资格证据。

`000068` 另增加第四个隔离的 `worksflow_golden_fault_operator` group 与独立 LOGIN/DSN，只允许 schema `USAGE` 和两张 append-only fault-ledger 表的非转授 `SELECT, INSERT`；API/application 不能继承该 role，也不能访问两表。第四个 group/LOGIN 必须在迁移前通过特权 provisioning channel 建立；conditional grant 顺序错误只能以 reviewed follow-up migration 修复。当前 strict direct-DSSE verifier、独立 signer trust、one-shot CAS/read-after-unknown ledger 和 closed adapter registry 已接入 Golden Fixture artifact index 与 immutable Qualification verifier，并对 authority/reservation/result/consume-receipt/run-ledger 执行 exact closure。仓库仍没有真实 fault adapter 或外部 authority/consume/attestation 产物，因此不会把 22 个 Golden case 提升为 executable/passed。

## 12. 用户可控 Interactive Sandbox

### 12.1 UI 形态

~~~text
+----------------+------------------------------+------------------+
| Files/Search   | Monaco Tabs / Diff / Problems| Live Preview     |
| Changed        |                              | Services / Ports |
+----------------+------------------------------+------------------+
| xterm PTY | Process Output | Agent Events | Quality | Resources     |
+---------------------------------------------------------------------+
~~~

顶部持续显示 Base Revision、Candidate tree、dirty/conflict/stale、Agent 状态、连接状态、CPU/内存/磁盘和 TTL。

必须提供三个不同动作：

1. `创建候选检查点`。
2. `冻结为实现提案`。
3. `申请评审 / 应用为 Revision`。

### 12.2 SandboxSession

SandboxSession 核心字段：

- projectId、actorId、buildManifestId。
- exact base WorkspaceRevision 和 Candidate ID/tree hash。
- TemplateRelease/BuildContract/Runner image digest。
- session epoch、writer lease、state、TTL 和 quota。
- allowed service/port/profile。

状态机：

~~~text
provisioning -> starting -> ready
ready -> suspending -> suspended
suspended -> resuming -> ready
ready/suspended -> terminating -> terminated
ready -- abandon Candidate --> terminating -> terminated
any running state -> failed
~~~

规则：

- 浏览器断开不等于 Session 终止。
- `ready` 才能创建 PTY、启动开发进程和运行 Agent。
- Suspend 前必须持久化 Candidate journal/checkpoint。
- TTL 到期先 checkpoint/hibernate，再按保留策略归档；不得静默丢弃 dirty Candidate。
- session epoch 变化后旧 WebSocket/lease 不能继续写。
- Candidate abandon 必须有原因和显式确认；dirty Candidate 先形成 exact checkpoint。成功后前端仅清除旧 Session/Candidate/runtime 本地指针并重新执行 Candidate discovery/bootstrap，不重新加载或替换 Blueprint、PageSpec、Prototype 等受治理文档。

初始可配置策略建议：2 vCPU、4 GiB RAM、10 GiB workspace、256 PID、3 个 Preview port、30 分钟 idle hibernate、8 小时单次运行上限。生产值由项目/租户计划配置，不写死在前端。

### 12.3 Sandbox Gateway

控制类 REST：

- 创建、查询、暂停、恢复、终止 Session。
- tree/list/read/search/batch save。
- checkpoint、diff、restore、rebase、conflict resolve。
- create/cancel AgentAttempt。
- freeze Candidate to Proposal。
- list/start/stop processes 和 ports。

独立 WSS 数据流：

~~~text
control | fs | pty | process | port | preview-log | agent | resource
~~~

每帧必须包含 `sessionEpoch`、`channel`、`seq`、`ack`、`requestId`。文件写入必须携带 `baseTreeHash` 和 `expectedFileHash`；旧 epoch/hash 返回 409。

WebSocket 要求：

- 使用 WSS 和短时、单用途连接 ticket；长期 Token 不放 URL。
- 校验 Origin、Session、Project 和 RBAC。
- heartbeat 必须和连接使用同一权威 Session，而不是独立匿名请求。
- 支持 cursor/lastAckSeq 断线续传、重复事件去重和 reset/snapshot。
- 401/403 返回机器可读 blocking reason。
- 领域事件总线与高频 PTY/文件流分离，避免互相阻塞。

#### 12.3.1 逻辑 REST 接口

路径是 v1 目标契约；实现可以按现有 transport 分层，但资源关系、并发字段和动作语义不得改变：

| Method/Path | 用途 | 关键并发/权限 |
|---|---|---|
| `POST /v1/projects/{projectId}/sandbox-sessions` | 从 exact WorkspaceRevision 创建 Session | Idempotency-Key、`sandbox.create` |
| `GET /v1/sandbox-sessions/{sessionId}` | 读取状态、quota、allowedActions | project view |
| `POST /v1/sandbox-sessions/{sessionId}:suspend` | 持久化并暂停 | If-Match、`sandbox.control` |
| `POST /v1/sandbox-sessions/{sessionId}:resume` | 恢复为新 epoch | If-Match、`sandbox.control` |
| `POST /v1/sandbox-sessions/{sessionId}:terminate` | checkpoint 后终止 | If-Match、原因 |
| `POST /v1/sandbox-sessions/{sessionId}:abandon` | 原子放弃 exact Candidate，并清理其 Session runtime | Session/Candidate/lease fences、exact checkpoint、Idempotency-Key、必填原因 |
| `GET /v1/sandbox-sessions/{sessionId}/tree` | 获取 exact Candidate tree | tree hash/ETag |
| `GET /v1/sandbox-sessions/{sessionId}/files/{path}` | 读取 ordinary file | 完整 Session/Candidate head fence、expected file hash、opening/closing recheck |
| `PUT /v1/sandbox-sessions/{sessionId}/files/{path}` | 写入单文件 | If-Match file/tree、writer lease |
| `POST /v1/sandbox-sessions/{sessionId}/file-operations` | batch create/update/rename/delete | 原子 batch、base tree hash |
| `POST /v1/sandbox-sessions/{sessionId}/checkpoints` | 创建 CandidateSnapshot | Idempotency-Key、current tree |
| `POST /v1/sandbox-sessions/{sessionId}:rebase` | 对新 Canonical head 三方合并 | base/current/candidate refs |
| `POST /v1/sandbox-sessions/{sessionId}/conflicts:resolve` | 提交显式冲突决策 | expected conflict set hash |
| `POST /v1/sandbox-sessions/{sessionId}/ptys` | 创建 PTY | `terminal.open`、quota |
| `POST /v1/sandbox-sessions/{sessionId}/processes` | 启动 Template command | command ID、`sandbox.control` |
| `GET /v1/sandbox-sessions/{sessionId}/ports` | 列出允许的 Preview ports | project view |
| `POST /v1/sandbox-sessions/{sessionId}/agent-attempts` | 创建 AgentAttempt | TaskCapsule hash、`agent.run` |
| `POST /v1/agent-attempts/{attemptId}:cancel` | 取消 Attempt | If-Match、`agent.cancel` |
| `POST /v1/sandbox-sessions/{sessionId}:freeze` | 冻结 exact tree 为 Proposal | checkpoint/tree/receipt refs |

文件 path 必须按逐段 URL 编码并由服务端 Sanitize；客户端传入的 path 不能参与本地文件系统拼接。大型/binary 文件可以使用预签名 Blob Transfer，但最终 tree mutation 仍必须经过 Repository Service CAS。

ordinary file read 的当前 P0 wire contract 已要求请求同时携带 `X-Sandbox-Session-Epoch`、`X-Expected-Candidate-ID`、`X-Candidate-Version`、`X-Candidate-Journal-Sequence`、`X-Writer-Lease-Epoch`、`X-Candidate-Tree-Hash` 和 `X-Expected-File-Hash`。服务端在解析 blob 前与返回 bytes 前各重验一次 Session/Candidate head，并返回包括 `X-Candidate-ID`、`X-Candidate-Version`、`X-Candidate-Journal-Sequence`、`X-Writer-Lease-Epoch`、`X-Candidate-Tree-Hash`、`X-Content-Hash` 在内的响应 fence；前端逐项比对后才打开内容。opening/closing 之间任一 head 漂移返回结构化 `409 sandbox_file_head_changed`，旧 bytes 不进入 Monaco。

默认 CORS 已 allow 上述请求 headers 并 expose 响应 headers。Agent patch/evidence 响应额外要求浏览器可读 `X-File-Exists`、`X-Byte-Size`、`X-Patch-Content-Hash` 与 `X-Content-Object-Hash`，并对实际 response bytes 重算 `X-Content-Hash` 后再接受内容。配置校验会拒绝缺少任一安全围栏的 `CORS_ALLOWED_HEADERS` / `CORS_EXPOSED_HEADERS` 覆盖；不能通过回退为无围栏读取解决。

#### 12.3.2 WSS 事件 Envelope

~~~json
{
  "schemaVersion": "sandbox-stream/v1",
  "sessionId": "...",
  "sessionEpoch": 3,
  "channel": "pty",
  "eventType": "pty.output",
  "sequence": 1024,
  "aggregateVersion": 18,
  "requestId": "...",
  "correlationId": "...",
  "timestamp": "2026-07-16T00:00:00Z",
  "payload": {}
}
~~~

约束：

- `sequence` 在 session epoch/channel 内单调递增；客户端按 channel 去重。
- `aggregateVersion` 用于状态型对象的 CAS，不替代 sequence。
- PTY binary 数据使用有类型的 binary frame header，不能伪装成 JSON。
- `fs.changed` 必须携带 old/new tree hash、path、operation 和 attribution。
- `agent.*` 只传运行事件；Proposal/Revision 最终状态仍通过领域 API/事件读取。
- Gateway 只补发有界 ring/cursor；超过窗口返回 `stream.reset`，客户端重新读取 Session/tree。

#### 12.3.3 Session 状态与 Allowed Actions

| State | 允许动作 | 禁止动作 |
|---|---|---|
| provisioning/starting | view、cancel | edit、PTY、Agent、freeze |
| ready | edit、PTY、process、Agent、checkpoint、verify、freeze、abandon、suspend、terminate | 直接创建 Revision/Deployment |
| suspending | view | 新写入和新进程 |
| suspended | view、resume、terminate | edit、PTY、Agent、freeze |
| resuming | view、cancel | 使用旧 epoch 写入 |
| failed | view、terminate | 继续使用失败容器 |
| terminated | view | 所有运行和写入 |

服务端必须返回精确 `allowedActions` 和 `blockingReasons`；前端不得根据字符串状态自行推演 Workflow/Review 权限。

`view_logs`、`restore_checkpoint`、`new_session`、`view_audit` 和 `view_snapshots` 目前没有对应的完整控制路由与 UI 消费链，因此服务端投影不得提前广告这些动作。后续只有在端到端能力存在并有服务端授权时，才可把它们加入 `allowedActions`。

### 12.4 PTY、Process 和 Port

- xterm.js 连接非 root PTY，支持 create/input/resize/signal/detach。
- 用户可以运行沙盒内部命令，但权限只限该 Pod/VM 和 workspace。
- 输出有大小、速率和 ring buffer 上限，并进行 Secret 脱敏。
- 开发进程由 Process Supervisor 启停，不能通过 shell 获得平台管理权限。
- 端口必须来自 TemplateRelease/Session policy 或经过显式登记。
- Gateway 只代理当前 Session namespace 的 loopback，拒绝用户指定任意 upstream，防止 SSRF。
- HMR WebSocket 通过同一 Port Gateway。

### 12.5 Preview 隔离

- Preview 使用独立 origin，例如 `*.preview.sandbox.example`。
- Preview 不携带平台 Session Cookie，不能读取平台 localStorage。
- iframe 使用严格 sandbox/CSP，并限制 `postMessage` source/origin/channel。
- Preview URL 使用短时授权并绑定 project/session/port。
- 前端、后端和临时数据库通过 Sandbox 内部网络通信；浏览器只访问 Gateway。

### 12.6 Production LSP Control Plane v1（LSP-0–3 内部实现；LSP-4 未资格化）

本节既定义边界，也记录当前实现。截至 2026-07-18，LSP-0–3 已在仓库内落地：专用 ticket API/WSS、exact `worksflow.sandbox-lsp.v1` subprotocol、Redis 原子 ticket/rate/editor lease、PostgreSQL authority/audit、immutable exact-tree runtime snapshot、digest-pinned read-only container runtime、strict Gateway/method DTO，以及自动 profile discovery、稳定 Monaco model/undo、heartbeat/reconnect 和只读 providers。Go 单元/集成、真实 PostgreSQL + Redis 定向验证与前端 typecheck/unit/lint/build 自动化已通过，因此状态是 `implemented-internal`。尚缺的是 LSP-4：approved Golden TemplateRelease language-server profile、目标 ingress/credential、digest-pinned real server 的浏览器/故障注入 `LSP-QA-016` 资格回执；不得写成 `production-qualified`。

#### 12.6.1 权威围栏

所有 ticket/connection 必须携带完整 `SandboxHeadFence`；binding 必须携带该 head 和有序 `DocumentFence[]`；任何 document-scoped 同步、请求、响应或 diagnostics 必须同时携带完整 head 与单个 document fence。只有 schema 明确列出的 connection-level ping/pong/error 才可没有 document fence。不得只比较 tree hash，也不得把现有 Sandbox stream 的 `sessionEpoch` 当作完整源码 head。

`SandboxHeadFence` 的 wire 字段固定如下；`version` 明确表示 Candidate aggregate version，不允许用 `candidateVersion`、Session version 或本地递增值作为别名：

~~~json
{
  "projectId": "uuid",
  "sessionId": "uuid",
  "sessionEpoch": 3,
  "candidateId": "uuid",
  "version": 18,
  "journalSequence": 42,
  "writerLeaseEpoch": 7,
  "treeHash": "sha256:..."
}
~~~

服务端从 Repository/Sandbox authority 构造该对象。八个字段必须逐项相等；project、session、session epoch、Candidate、writer lease 任一变化都要求新连接，Candidate version、journal sequence、tree hash 的同 Session 单调后继只能经服务端重验的 `client.headRebind` 接受。

`DocumentFence` 的 wire 字段固定如下：

~~~json
{
  "modelUri": "worksflow-candidate://project-uuid/candidate-uuid/apps/web/page.tsx",
  "openId": "uuid",
  "modelVersion": 27,
  "savedContentHash": "sha256:..."
}
~~~

- `modelUri` 由 project、Candidate 和逐段 percent-encoded canonical repository path 确定，不含 Session、head version、query 或 fragment；不得接受 `file:`、`untitled:`、path traversal、反斜杠、空段或非 canonical escape。
- `modelUri` 只存在于浏览器/Gateway 能力边界。普通 language server 不理解该 scheme，Gateway 必须把已验证 URI 单向映射为容器内固定 `file:///workspace/<canonical-path>`，initialize root 只允许 `file:///workspace[/<service-root>]`；server 返回的 file URI 必须严格反向映射为同一 Candidate URI 后才进入 sanitizer。host、`file:/` alias、query/fragment、`/workspace` 外路径、protected path 或非 canonical escape 一律终止/丢弃，任何宿主 workspace 路径都不得上 wire。
- URI 在同一 Candidate/path 的 autosave、head rebind 和 LSP reconnect 中保持稳定，使 Monaco 复用同一个 model。rename 不属于 v1；删除必须先 close/dispose，重新创建按新的 `openId` 处理。
- `openId` 是该 Monaco model 本次打开生命周期的 UUID；model 被 dispose 后重新打开必须换 ID。`modelVersion` 是正的、单调递增 safe integer，并与 Monaco 当前 model version 精确对应。
- `savedContentHash` 始终是最近一次成功 Candidate CAS 返回的 exact file content hash；dirty change 只前进 `modelVersion`，不能伪造 saved hash。v1 只给已经成功写入 Candidate 的 UTF-8 文本文件开启 LSP；新建但尚未首次 CAS、binary 或超限文件不开启 LSP，因此无需以 null 或空串制造 hash。
- 首次 document open 的 bytes 必须复用上文 ordinary file read 的完整 request/response fence 与 opening/closing recheck；LSP Gateway 不能用裸文件系统读取、旧缓存或只校验 content hash 的旁路建立 `DocumentFence`。

#### 12.6.2 TemplateRelease 与 language-server identity

Language server 不能由工作区依赖、PATH 自动发现或浏览器任意选择。一个 approved、exact `TemplateRelease` 必须内嵌 immutable `language-server-profile/v1`，至少冻结：

- profile ID/content hash、language IDs、canonical file globs 和 LSP protocol version。
- exact TemplateRelease ID/content hash、digest-pinned runtime image、server executable path 与 executable digest、无 shell argv 和工作目录策略。
- expected `serverInfo.name/version`、初始化参数 hash、平台提供的 workspace configuration hash，以及是否要求 versioned diagnostics。
- 平台基线与模板声明交集形成的 method/capability allowlist 及其 canonical hash。
- startup/request/shutdown timeout，CPU、memory、PID、临时盘、打开文档、同步字节、请求速率和结果数量上限。
- `network=none`、只读 workspace/source mount、独立 bounded tmp/cache，以及禁止从 workspace 加载 executable plugin、动态 SDK、任意配置命令或 package-manager hook 的策略。

ticket 与 binding 必须返回上述 exact identity。启动后 Gateway 比对实际 image、executable digest、`serverInfo` 和 initialize capabilities；缺 profile、TemplateRelease 非 approved、digest/tag 漂移、未知 capability 或服务器尝试 dynamic registration 均 fail closed。Readiness 只校验已配置 profile/镜像元数据和 runtime 可达性，不拉取镜像，也不能把本地 mutable image 标为生产可用。

#### 12.6.3 专用 ticket、WSS 与连接建立

LSP 不复用 `/v1/sandbox-stream` ticket、通用 `/v1/ws` 或其 cursor。目标接口固定为：

| Method/Path | 用途 | 关键门禁 |
|---|---|---|
| `POST /v1/sandbox-sessions/{sessionId}/lsp-tickets` | 签发一次性 LSP ticket | authenticated actor、project membership、ready Session、完整 `SandboxHeadFence`、Origin、恰好一个请求 profile、strict DTO |
| `WSS /v1/sandbox-lsp?ticket={opaque}` | 建立专用浏览器通道 | TLS、exact Origin、原子 consume、再次重验完整 head、subprotocol `worksflow.sandbox-lsp.v1` |

ticket TTL 最长 30 秒，只授权一个 actor/project/session/head、一个 Origin、恰好一个 profile ID 和 `mode=snapshot|editor`。v1 不伪装单连接 multiplex：多语言项目必须为每个 profile 分别签发 ticket 并建立独立 WSS/binding，任何含多个 profile ID 的请求都失败关闭。Redis 只保存 secret digest 与 bounded grant，使用原子 consume；Redis 不可用时 fail closed。secret 在 Upgrade 前烧毁，即使后续 head 重验或 runtime 启动失败也不能重放；重连必须重新走 authenticated HTTP。反向代理、访问日志和 tracing 必须删除 `ticket` query，浏览器不得把 ticket 放入 localStorage、日志、错误报告或 telemetry。

`sandbox.view` 可签发只分析已保存内容的 `snapshot` ticket；发送 unsaved `didChange` 的 `editor` ticket 还要求 `sandbox.edit`、actor 当前 writer lease 和完整 writer fence。ticket 不是 Candidate write capability。WSS Upgrade 必须精确协商 `worksflow.sandbox-lsp.v1`，拒绝缺失/多义 subprotocol、非 HTTPS 对应的生产连接、Origin 漂移和 Cookie/长期 Bearer 替代。Upgrade 后 5 秒内未完成 bind 即关闭。

严格 `sandbox-lsp-ticket/v1` 响应包含 ticket ID/secret、固定 WSS path/subprotocol、expiry、原样 `SandboxHeadFence`、exact TemplateRelease ref、按 canonical profile ID 排列的 language-server identities 和 effective limits。前端必须逐项比对请求身份；missing、null、unknown/widened、重复、乱序或额外 top-level 字段都拒绝，不能填默认值继续连接。

#### 12.6.4 Connection、binding 与 message envelope

Gateway 面向 language server 使用受控 LSP JSON-RPC/stdio，面向浏览器使用四个自有 strict schema；绝不把 raw JSON-RPC 或任意 server request 直通浏览器：

1. `sandbox-lsp-ticket/v1`：authenticated HTTP 连接 grant。
2. `sandbox-lsp-connection/v1`：Upgrade 后的 `server.hello`、connection identity、ticket scope 与 bind deadline。
3. `sandbox-lsp-binding/v1`：一个 WSS connection 内某个 exact profile/runtime 与 document 集合的 bind/bound 事实。
4. `sandbox-lsp-envelope/v1`：绑定后的 request、response、notification、cancel、head rebind、ping/pong 和结构化 error。

连接顺序固定为：atomic ticket consume/head recheck → `server.hello` → `client.bind` → profile runtime start/identity check → `server.bound` → document open/sync → bounded requests。`server.hello` 产生新的 connection UUID；该单-profile connection 产生一个 binding UUID，`profiles` 数组在 v1 中必须恰好包含一个元素。`client.bind` 必须回显 ticket scope 的完整 head、该 profile identity 与有序 `DocumentFence[]`；`server.bound` 返回同一 head、实际 server identity、effective capability allowlist/hash、limits 和 accepted documents。任一不一致不进入 ready。

connection/binding envelope 的最小形状分别为：

~~~json
{
  "schemaVersion": "sandbox-lsp-connection/v1",
  "kind": "server.hello",
  "connectionId": "uuid",
  "ticketId": "uuid",
  "sequence": 0,
  "sandboxHeadFence": {},
  "templateRelease": {"id": "uuid", "contentHash": "sha256:..."},
  "profiles": [],
  "limits": {},
  "bindDeadlineAt": "2026-07-18T00:00:05Z"
}
~~~

~~~json
{
  "schemaVersion": "sandbox-lsp-binding/v1",
  "kind": "server.bound",
  "connectionId": "uuid",
  "bindingId": "uuid",
  "sequence": 1,
  "sandboxHeadFence": {},
  "languageServer": {
    "profileId": "typescript",
    "profileContentHash": "sha256:...",
    "runtimeImageDigest": "registry.example/lsp@sha256:...",
    "executableDigest": "sha256:...",
    "serverName": "...",
    "serverVersion": "...",
    "capabilityAllowlistHash": "sha256:..."
  },
  "documents": [],
  "effectiveCapabilities": [],
  "limits": {}
}
~~~

`client.bind` 使用同一 binding schema，但 `kind=client.bind`、`bindingId=null`，并发送请求 profile identity 和有序 documents；只有该 kind 允许 null binding ID。所有 ticket/connection/binding/message schema 都递归使用 `additionalProperties=false`，而不是只检查 top-level。

所有绑定后 envelope 的公共部分固定为：

~~~json
{
  "schemaVersion": "sandbox-lsp-envelope/v1",
  "connectionId": "uuid",
  "bindingId": "uuid",
  "sequence": 12,
  "messageId": "uuid",
  "replyTo": null,
  "kind": "client.request",
  "method": "textDocument/hover",
  "sandboxHeadFence": {},
  "documentFence": {},
  "payload": {}
}
~~~

- `sequence` 在每个 connection/direction 内单调递增；旧 connection ID、重复/倒退 sequence 或未知 binding 一律 stale-drop。LSP 流不做持久 replay，sequence 只用于当前连接去重与审计。
- `replyTo`、`documentFence` 只有 schema 明确允许的 message kind 才可为 null；集合总是 `[]`，map 总是 `{}`。所有整数必须是有界 safe integer，字符串、URI、method、数组和递归深度都有上限。
- 每个 `method` 使用独立 `additionalProperties=false` payload schema。Gateway 解析、归一化并过滤内部 LSP 结果后才构造浏览器 DTO；malformed、oversized、非 workspace URI、未知字段或未 allowlist method 不得进入 Monaco。
- response/error 必须回显原 request 的两个 fence，而不是发送完成时的“当前”值。diagnostics 必须带可验证的 document version；不支持 versioned diagnostics 的 server profile 不能进入 production v1。
- bind、head rebind、open/change/save/close、request/cancel 都由 Gateway 重验 mode、method allowlist、head/document fence 和资源预算。`didChange` 内容只是该隔离 language-server process 的短期 overlay，不写 journal、不成为恢复事实。

#### 12.6.5 只读 capability 边界与 Candidate CAS

平台 v1 基线可允许 diagnostics、hover、signature help、document highlight/symbol、definition/declaration/type-definition/implementation、references、semantic tokens、inlay hints，以及受限 completion。Template profile 只能从该基线缩小。导航结果 URI 必须落在 exact Candidate tree；跨文件读取/导航可以返回，跨文件修改不可以返回。

completion 只允许 plain `insertText` 或当前 `DocumentFence` 内的单一 `textEdit`；Gateway 拒绝 command、`additionalTextEdits`、snippet command、workspace edit 和当前文档外 URI。浏览器接受 completion 后只产生普通 Monaco local edit，随后仍走 Candidate CAS。

以下能力和方法在 v1 无条件禁止，不能由模板放宽：

- `workspace/applyEdit`、`workspace/executeCommand` 及任何 `executeCommand`。
- `textDocument/rename`、`textDocument/prepareRename`、workspace file operation、create/rename/delete file。
- code action、resolve 后携带 edit/command 的 completion、formatting/range/on-type formatting、`willSaveWaitUntil`。
- dynamic capability registration、任意 client command、任意 workspace configuration 读取、跨文件 edit 和直接文件系统 mutation。

Gateway 对 forbidden server request 返回内部 MethodNotFound/RequestFailed、记录 violation 并丢弃；重复或高风险 violation 终止 binding。language-server process 使用非 root 身份、只读 Candidate mount、无网络和独立 tmp/cache，即使 server 绕过协议也不能修改 Candidate。

唯一源码写路径保持不变：Monaco local edit → 现有 autosave/file-operation API → Repository Service 以完整 `SandboxHeadFence`、expected file hash 和 writer lease 执行 Candidate CAS → 返回新 head/content hash → 浏览器发送 `client.headRebind` 和新 `DocumentFence`。CAS 冲突、lease/session 漂移或保存失败时不得让 LSP 重试写入、调用 applyEdit 或覆盖 editor；进入明确 stale/conflict 状态。

#### 12.6.6 Stale-drop、rebind、reconnect 与 Monaco

- Gateway 在 ticket mint、consume、bind、head rebind、document open/save 和每个 request admission 重验 authority。浏览器在应用 response/diagnostics 前再比较 connection、binding、完整 head、model URI、open ID、model version 和 saved hash；任一漂移都 drop，不允许“尽量应用”。
- 同 actor/session/candidate/writer lease 下成功 CAS 产生的单调 Candidate 后继可用 `client.headRebind` 更新。Gateway 独立读取 Repository authority，只接受 exact current `(version,journalSequence,treeHash)`；接受前取消或丢弃旧 head 的所有 in-flight 结果。
- project、session、session epoch、Candidate 或 writer lease 变化，以及非单调/未知 head，返回 `lsp_head_stale` 并以 4409 关闭，要求新 ticket。前端先 GET Session/tree；dirty model 不覆盖、不 `setValue`，而进入显式 save/rebase/conflict 路径。
- 网络断开不 replay LSP 消息。前端取消旧 pending request，获取 fresh Session/tree 与 ticket，复用仍存活的 Monaco model、相同 canonical URI、open ID、model version 和 undo stack，再以全量文本重新 `didOpen`。只有 connection/binding ID 更新。
- LSP reconnect、diagnostic refresh 或 head fence refresh 均不得 dispose model、调用 `setValue`、重建 tab 或清空 undo。若 clean model 的 saved hash 仍与服务器相同，只重绑；若服务器文件已改变，暂停该 document binding 并展示 Diff/Conflict。浏览器进程硬刷新后只能从已成功保存的 Candidate 恢复，不得声称恢复尚未 CAS 的内存 undo。
- Monaco markers 使用稳定的 binding/profile owner key；只对 exact `DocumentFence` 调用 marker API。marker、hover decoration、semantic token 和 inlay hint 都是可丢弃 UI 投影，不改变 model 内容或 Candidate authority。

#### 12.6.7 审计、限流与资源

append-only audit 至少记录 ticket issue/consume/replay/reject、actor/project/session/head、Origin hash、connection/binding open/rebind/close、TemplateRelease/profile/runtime identity、capability hash、request method/count/latency/outcome、stale-drop、rate/resource/forbidden-method violation 和终止原因。不得记录 ticket secret、源码、unsaved text、completion/hover 正文、diagnostic message 正文或任意 Secret；路径按租户策略 hash/缩减，日志先脱敏。

平台 hard cap 与 Template profile cap 取更小值，并在 ticket/binding 中返回 exact effective limits。v1 参考上限为：ticket 30s、bind 5s、每 actor/session/profile 一个 editor connection、每连接恰好 1 个 profile binding/最多 32 个文档、单文档 1 MiB/同步总量 8 MiB、JSON frame 512 KiB、response 1 MiB、32 个并发 request、30 request/s（burst 60）、每文档 2,000 diagnostics、completion 500 项、navigation 5,000 location。runtime 必须声明并强制 CPU/memory/PID/tmp、20s startup、10s request 和 5s shutdown 上限；默认无网络，随 Sandbox suspend/terminate/freeze/abandon 强制退出。

限流至少按 tenant/project/actor/session/profile/method 分层；Redis rate-limit 或 ticket store 异常时拒绝新 ticket/请求，不能无限放行。resource exhaustion 先取消最老低优先级请求，再关闭 offending binding；不能拖垮通用 Sandbox stream、autosave、PTY 或平台 `/v1/ws`。

规范错误码与动作：

| Code | HTTP/WSS | 客户端动作 |
|---|---|---|
| `lsp_forbidden` / `lsp_origin_forbidden` | 403 / 4403 | 不重试，显示权限或 Origin 阻塞 |
| `lsp_ticket_required` / `lsp_ticket_rejected` | 401 / 4401 | 丢弃 ticket，经 authenticated HTTP 重新签发；replay 不复用 |
| `lsp_subprotocol_required` | 400 / 4400 | 客户端/代理配置错误，不降级到 raw WS |
| `lsp_session_not_ready` / `lsp_head_stale` / `lsp_document_stale` | 409 / 4409 | GET Session/tree，保留 Monaco；dirty 时进入冲突流程 |
| `lsp_profile_not_declared` / `lsp_document_unsupported` | 422 / binding error | 关闭该语言能力，不猜测 server |
| `lsp_server_identity_mismatch` / `lsp_capability_violation` | 503 / 4503 | 隔离 binding，记录供应链/协议 finding |
| `lsp_message_malformed` / `lsp_read_only_violation` | 400 / 4400 | 丢弃消息；高风险或重复行为终止 binding |
| `lsp_rate_limited` / `lsp_resource_exhausted` | 429 / 4429 | bounded backoff；不得绕过 limits 新建连接 |
| `lsp_ticket_store_unavailable` / `lsp_runtime_unavailable` | 503 / 4500 | fail closed，保持编辑与 CAS 可用，LSP 显示不可用 |

#### 12.6.8 分阶段实施与验收

实施顺序不可用 UI mock 倒逼放宽后端边界：

1. **LSP-0 Contract/Admission**：批准 schema、URI canonicalization、method schemas、TemplateRelease profile 和 capability baseline；准备 digest-pinned Golden language-server release。没有 profile 时 API 明确返回 `lsp_profile_not_declared`。
2. **LSP-1 Authority/Ticket**：实现统一 fences、strict DTO、RBAC/Origin、Redis atomic ticket、WSS/subprotocol、audit/rate limit；用 protocol fixture 验证，不接 Monaco。
3. **LSP-2 Runtime/Adapter**：启动 exact read-only server，校验 identity/capability，完成 method-specific sanitize、resource isolation、stale-drop/head rebind；恶意 fake server 必须无法把 write request 发到浏览器或磁盘。
4. **LSP-3 Monaco**：稳定 model URI/open ID、diagnostics/hover/navigation/safe completion、CAS-only save、dirty conflict、reconnect/undo 保留；LSP 故障不影响普通编辑、autosave 和 bounded search。
5. **LSP-4 Qualification**：在真实 ingress/WSS、approved Golden TemplateRelease 和真实 digest-pinned language server 上执行浏览器、断线、并发、资源和安全矩阵；形成可审计 qualification receipt 后才可把 LSP 标记为 `production-qualified`。LSP-0–3 的 `implemented-internal` 状态与该外部资格结论分开记录。

验收标准：

| ID | 必须结果 |
|---|---|
| AIC-LSP-ACC-001 | 两个 fence 的字段缺失、null、widened、alias、unknown 或任一不相等均 fail closed |
| AIC-LSP-ACC-002 | ticket 专用、短期、Origin/head/profile-bound 且并发 consume 恰好一个成功；Redis 异常拒绝连接 |
| AIC-LSP-ACC-003 | WSS 只接受 exact subprotocol；bind 前不启动 server、不读取 document |
| AIC-LSP-ACC-004 | TemplateRelease、image、executable、serverInfo 和 capability hash 全部 exact；漂移隔离 |
| AIC-LSP-ACC-005 | diagnostics/navigation/safe completion 只应用于 exact DocumentFence；旧结果全部 stale-drop |
| AIC-LSP-ACC-006 | applyEdit/executeCommand/rename/format/code-action/cross-file edit 在 Gateway、runtime mount 和浏览器三层均不可达 |
| AIC-LSP-ACC-007 | 接受 completion 后的唯一持久化路径仍是 Candidate CAS；冲突零覆盖、零隐式重试 |
| AIC-LSP-ACC-008 | LSP reconnect/head rebind 保留同 Monaco model URI、open ID 和 undo；dirty/remote change 进入显式 conflict |
| AIC-LSP-ACC-009 | 跨 project/session/Candidate/epoch/lease 消息、URI 与结果均拒绝，旧连接不能污染新 Session |
| AIC-LSP-ACC-010 | 速率、frame/result/document/runtime 限额可观测且 fail closed，不阻塞 autosave/PTY/通用事件流 |
| AIC-LSP-ACC-011 | audit 足以重建 identity/fence/method/outcome，但不含 ticket、源码、unsaved text 或 Secret |
| AIC-LSP-ACC-012 | approved Golden real language-server 经真实 WSS/浏览器/故障矩阵通过；mock/fake/local tag/skip 均不能替代 |

当前 LSP-0–3 与 AIC-LSP-ACC-001–011 的仓库内边界已有实现和自动化证据；这只允许声明 `implemented-internal`。AIC-LSP-ACC-012 对应的 LSP-4/`LSP-QA-016` 外部 Golden 资格尚未执行，因此不能声明 production-qualified、Stage 1 外部退出或真实语言服务器组合已获批准。

## 13. Agent Runner 与模型接口

### 13.1 统一 Executor 协议

~~~text
Run(TaskCapsule) -> EventStream + CandidatePatch + StructuredResult
Cancel(AttemptId)
~~~

Codex、未来其他 Coding Agent 或 Provider 都是 Adapter。平台状态机、TaskCapsule、Diff 提取、验证和审批不依赖具体模型。该协议目标保持模型无关；当前已接线实现只接受 `codex-cli + openai`，其他 adapter/provider 标签会在启动时失败，直到 versioned executor registry、对应 Adapter 和 conformance corpus 完成。

### 13.2 Codex MVP

首版使用 digest-pinned Runner Image 内的固定 Codex CLI：

~~~text
codex
  --ask-for-approval never
  exec
  --ephemeral
  --sandbox workspace-write
  --strict-config
  --ignore-user-config
  --ignore-rules
  --json
  --output-schema /input/result.schema.json
~~~

- `/input` 只读：TaskCapsule、BuildContract、ContextPack 和 Schema。
- `/workspace` 可写：准确 base tree 的临时 worktree。
- `/output`：结构化结果、事件和平台采集引用。
- `--ask-for-approval` 是 Codex 全局参数，必须位于 `exec` 前；非交互
  Runner 不等待人工批准，越出 `workspace-write` 的动作直接失败。参数顺序和
  exact CLI 版本必须由镜像 canary 验证。
- Runner 不读取可变的 `$CODEX_HOME/config.toml` 或用户/项目 execpolicy
  `.rules`；平台在容器、网络、挂载和服务端 capability 层独立实施强制策略。
- 仓库 `AGENTS.md` 提供工程指导；平台策略、protected paths 和 tool policy 优先级更高。
- 需要多轮 thread/resume 和更细控制时，后续可用独立 Node Runner 封装 Codex SDK；这不改变 Executor 协议。

Runner 构建的 Go/Node 基础镜像必须是完整 `image@sha256:<64 lowercase hex>`；Codex 必须是 exact SemVer 和单一 npm sha512 SRI。构建先验证所有输入，再用 `npm pack --ignore-scripts` 取得 exact package tarball并核对 integrity，只安装该本地 tarball、禁用 lifecycle scripts，最后断言 exact `codex --version`。range、dist-tag、mutable base、SRI 漂移或版本输出不一致必须在形成 Runner image 前失败。

参考：

- [Codex non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode)
- [Codex SDK](https://learn.chatgpt.com/docs/codex-sdk)
- [Codex approvals and security](https://learn.chatgpt.com/docs/agent-approvals-security)
- [AGENTS.md guidance](https://learn.chatgpt.com/docs/agent-configuration/agents-md)

### 13.3 安全边界

Runner 必须：

- 非 root、read-only rootfs、cap-drop ALL、no-new-privileges、seccomp/AppArmor。
- 仅 workspace/tmpfs 可写，限制 CPU、RAM、PID、磁盘、时间、日志和 token。
- 不挂任意宿主路径、Docker socket、Git credentials、云凭据、生产账号；平台拥有并按 exact Attempt 建立的 `/input`、`/workspace`、`/output` 物化路径是受围栏的例外，不允许调用方自选 source path。
- 不能连接 Canonical DB、Template Registry 写接口或 Release Control Plane。
- 默认禁止公网；依赖解析通过 allowlisted Registry Proxy/Cache。
- Agent 编码阶段只连接专用 internal Runner network，并且只能经 path-confined Model Relay 调用 Responses Gateway；不得连接默认网络或公网。独立最终验证默认 `network=none`，所需数据库/服务只能由 exact VerificationPlan 建立隔离网络。
- 文档、源码、AGENTS、依赖说明和网页都视为可能包含 Prompt Injection 的不可信数据。

现有 Compose 的 privileged DIND 只可作为受控本地质量实现细节，不能暴露给浏览器/Agent，也不是生产 Interactive Sandbox。

### 13.4 Model Gateway

- 上游 Provider Key 永远不进入 Runner、用户终端、源码、日志或 BuildArtifact。
- Runner 获取绑定 tenant/project/attempt/model/budget/expiry 的单任务 capability。
- Gateway 限制模型、token、并发、超时、可用 API 和审计字段。
- capability 泄露不能读取项目其他数据或调用部署/平台管理 API。
- Constructor Model Plane 与 Generated Application AI Plane 使用不同身份、密钥、配额和审计域。

本地参考拓扑是：Runner → internal-network 内层 Relay → DinD host-gateway → 与 DinD 共 network namespace 的外层 Relay → API Model Gateway。Runner 不持有 Docker socket 或 Provider Key；API 与 DinD 只在相同绝对路径共享平台拥有的 `agent-worktrees`。生产可以替换传输实现，但不能扩大 path、identity 或 credential 边界。

### 13.5 Attempt 状态与重试

~~~text
pending -> ready -> queued -> claimed -> running
running -> patch_ready
patch_ready -> validating
validating -> review_ready | verification_failed
running/validating -> failed | timed_out | cancelled
any non-final result -> stale
~~~

必须区分：

- Agent 进程执行完成。
- Patch 成功采集。
- 独立验证通过。
- Task 的 Must Obligation 完成。

Retry 必须创建新 Attempt，记录明确 retry reason、parent Attempt 和新 fencing token。更换模型、Prompt、Context、Template 或 Runner 不是 retry，而是显式 supersede/new attempt。

request key 至少绑定：

~~~text
project
+ base Workspace/Candidate tree
+ BuildContract hash
+ TemplateRelease hash
+ task ID
+ Executor image
+ model policy
+ Prompt/Context/Schema hash
+ toolchain version
~~~

## 14. Patch、Proposal 与 Canonical Review

### 14.1 平台采集 Patch

Agent 返回的 Operations、总结和“测试通过”声明不能直接成为 Proposal。Orchestrator 必须从 worktree 自行计算：

- add/update/delete/rename。
- file mode、symlink/special file。
- old/new content hash 和 expected hash。
- 路径、大小、文件数、binary 和 sensitive path。
- 实际命令、退出码和日志。

禁止生成/提交 `.env`、凭据、`.git`、依赖缓存、构建缓存和未经策略批准的二进制。

### 14.2 Changeset 原子性

- 默认一个 DevelopmentTask 形成一个原子 Changeset。
- Proposal 可以包含多个有序 Changeset，但必须保存依赖图。
- 人工部分接受只有在 accepted subset dependency-closed 时才允许继续。
- 任何部分接受都必须对 exact accepted subset 重新物化 tree，并重新执行全部适用验证。
- 验证过完整 Patch 后直接应用未复验的子集是禁止状态转换。

### 14.3 Proposal 状态

~~~text
draft -> ready -> review_pending
review_pending -> approved | rejected
approved -> applied
draft/ready/review_pending/approved -> stale
~~~

任何 base tree、CandidateSnapshot、operation decision、payload hash、BuildContract 或验证 Receipt 改变，都使原批准失效。

WorkspaceRevision Review：

~~~text
created -> review_pending -> canonically_approved
created/review_pending -> rejected | superseded
~~~

前端 Workflow 批准按钮只使用服务端 `allowedActions`。精确上游 Revision 未 Canonically Approved 时，服务端和 UI 都必须阻止批准。

## 15. 独立质量与证据闭环

### 15.1 两个沙盒

| 环境 | 用途 | 是否能签发最终证据 |
|---|---|---|
| Interactive Sandbox | 用户编辑、Agent 开发、即时测试和 Preview | 否 |
| Quality Sandbox | 从 exact immutable tree 独立复验 | 是 |

Quality Sandbox 不复用 Interactive Sandbox 的 node_modules、进程或“通过”状态。依赖缓存必须按 lock/hash/toolchain 隔离并只读挂载。

### 15.2 检查矩阵

- Wire/JSON Schema 和 nullable compatibility。
- 路径、Secret、License、dependency policy。
- 前端 lint、typecheck、unit、build。
- 后端 lint/type/unit。
- OpenAPI/Client codegen drift 和 contract test。
- PostgreSQL 空库 migration、已有库升级、重复执行和兼容检查。
- Auth、tenant、permission integration。
- 多服务启动、health/readiness。
- Playwright 关键用户路径。
- PageSpec 的 loading/empty/error/ready 及业务状态。
- Accessibility/axe。
- Prototype 语义断言和允许范围内的视觉差异。
- SAST、dependency vulnerability、SBOM 和 container scan。
- Preview smoke 和运行时错误。

### 15.3 测试信任等级

1. 平台根据 Contract/AC 确定性生成的测试。
2. Agent/开发者编写的实现测试。
3. Agent 不可读取的隐藏验收测试。

同一个模型生成实现和测试后，不能仅靠该测试自证正确。隐藏测试只在 Quality Service 的受信环境读取。

### 15.4 Verification Gate

- 每个 Receipt 绑定 exact tree hash 和 verifier image digest。
- Must Obligation 覆盖率必须为 100%。
- blocker 数量必须为 0。
- stdout/stderr 超出 bounded capture 时，未捕获尾部属于未知证据：executor 和 Candidate/Canonical worker 必须将结果归一为 `error`、移除 exit code 并加入 `output_truncated` blocker，即使进程实际以 0 退出也不能通过。
- Receipt 领域构造器不得接受 `passed + truncated` check；migration 000051 在 Candidate/Canonical check shape 和 deferred Receipt gate 上再次拒绝该组合，防止绕过 worker 伪造通过事实。
- migration 000052 要求 Run、Attempt 与 exact-fence cleanup `registered` 在 claim/reclaim 的同一事务中提交；DEFERRABLE transaction guard 会在 commit 时拒绝滚动升级期间仍按旧顺序写入或漏写 obligation 的 worker。
- 正常 Candidate/Canonical Receipt 只有在其全部 `attemptIds` 对应 cleanup 均为 `completed` 后才能插入。active cancel 将 obligation 转为可重试 `pending`；lease loss、cleaner crash 和 cleanup failure 通过独立 lease/fence、takeover 与 backoff 收敛，不能伪造质量通过。
- retry/flake 不能删除失败历史；最终报告必须显示尝试次数和不稳定性。
- `unimplementedItems` 升级为结构化 Gap：

~~~json
{
  "id": "gap_...",
  "severity": "blocker",
  "obligationId": "OBL-...",
  "reason": "...",
  "waiverRef": null
}
~~~

只有 should/optional 且有有效 Waiver 的 Gap 可以 deferred。

质量分为两个绑定不同权威对象的阶段：

1. **Candidate Verification**：绑定 CandidateSnapshot tree，决定 Proposal 是否可进入 review。
2. **Canonical QualityRun**：Apply 后从 exact WorkspaceRevision 重新物化并签发，决定是否可构建/发布。

若 accepted subset 与已验证 Candidate 不同，必须先重新生成 Candidate Verification。Canonical QualityRun 可以按 hash 复用只读依赖缓存和确定性中间制品，但不能直接把 Candidate 的“passed”字段复制为 WorkspaceRevision 质量事实。Publish 仍必须要求与 exact WorkspaceRevision、BuildManifest lineage 和 Release input 一致的 passing Canonical QualityRun。

## 16. 模型无关与一致性

“模型一致”定义为语义和验收一致，不是源码字节一致：

- Must Obligation 100% 覆盖。
- 未知 Revision/API/字段/组件引用为 0。
- protected path 违规为 0。
- Secret 和跨租户访问为 0。
- 相同 Contract-derived/hidden tests 通过。
- 相同 build/integration/E2E/security gates 通过。
- 相同 deployment health contract 通过。

### 16.1 ModelProfile

每个可用 ModelProfile 保存：

- Provider、Model、版本/别名解析策略。
- 支持的 tool/schema/context 能力。
- reasoning、token、timeout 和成本策略。
- 合格的 Executor/Prompt/Schema 组合。
- conformance corpus 结果和启用范围。
- fallback 和禁用条件。

模型不能仅因为 API 可调用就进入 Build Policy。

当前仓库的 `backend/internal/modelgovernance/` 已实现内部 v1 权威契约：严格
canonical ModelProfile/FrozenCorpus/ProviderRouteAuthority，五类独立角色 DSSE，
全链 hash/时间/撤销验证，append-only generation/fence CAS 服务接口、未知结果
same-operation 检查、历史 shadow baseline 解析，以及每次调用都重验 primary +
fallback exact activation registry 的 `ResolveActive`。不可变 signer policy hash 覆盖
key、identity、role 与有效期；运行时撤销由独立的短时累计 epoch/hash 权威承载，并由
Store 持久化最高已观察 epoch，避免一次局部撤销替换全部历史 receipt，也拒绝 rollback、
同 epoch 分叉或删除既有撤销。receipt 通过其绑定 hash 加载历史 signer policy，只有
current policy 可签发新 candidate。完整边界见 `docs/model-governance-authority.md`。

这仍只可记为 `implemented-internal`：当前只有并发测试过的内存 Store，没有
PostgreSQL 实现、生产 authority/disable-state 接线、真实 corpus runner/provider，
也没有封闭签名的 bootstrap artifact/policy，也没有跨 route/disable/revocation/head
的共同事务 epoch 与数据面原子消费。因而空 registry 的首发激活会主动失败；仓库没有任何 active/approved/qualified ModelProfile，也没有获得 provider
网络调用权限。Model Governance authority 与 external Golden Qualification Receipt
彼此独立，任一方都不能替代另一方门禁。

### 16.2 Conformance Suite

新模型、模型版本、Prompt、Runner 或工具升级前，必须在同一 frozen golden corpus 运行：

- Schema conform。
- first-pass/repair-pass gate。
- AC/Obligation 覆盖。
- unsupported claim。
- protected path/Secret policy。
- build/contract/E2E。
- 成本、时延和稳定性。

关键安全和 Must Gate 为绝对阈值；质量、成本和时延可相对当前基线评估。未达到已批准基线的组合不得激活。

## 17. 全栈构建、Preview 与生产发布

### 17.1 ReleaseBundle

~~~text
ReleaseBundle
  web static artifact or OCI digest
  api OCI digest
  worker OCI digests (optional)
  migration artifact
  runtime/config schema
  health/readiness contract
  SBOM/vulnerability/provenance/signature
  exact WorkspaceRevision
  exact BuildContract/TemplateRelease
~~~

现有静态 BuildArtifact 保留为一种 service artifact，不再代表整个全栈 Release。

### 17.2 构建

- 从 canonically approved WorkspaceRevision 构建。
- 使用 rootless BuildKit 或等价受控 OCI Builder；Agent 不接触 Builder daemon。
- 基础镜像必须 digest pin。
- 依赖仅从认证 Proxy/Registry 获取。
- 每个 service 产出不可变 digest、SBOM、Provenance 和签名。
- Preview/Production 不重新构建。

### 17.3 Preview

- 每个 Release/项目使用独立 Namespace 或等价信任域。
- 使用临时数据库/schema、Redis 和 Provider fake。
- Secret 通过 Vault/KMS/Capability Broker 在运行时注入，源码和模型不可见。
- 执行 Migration、health、smoke、contract 和 Playwright。
- 真实模型只在 staging smoke 中通过 Generated Application Model Gateway 调用。
- Preview 未通过时不开放生产批准。

### 17.4 Production 与 Rollback

Deployment 状态：

~~~text
queued -> building -> preview_deploying -> preview_ready
preview_ready -> approval_pending -> promoting -> healthy
any active state -> failed
healthy -> rolling_back -> rolled_back
~~~

- Promotion 使用 Preview 已验证的相同 digest。
- 支持 canary/blue-green、健康阈值和自动回滚。
- 数据库采用 expand/contract；破坏性 migration 需要独立人工门禁。
- 应用回滚不能假设数据库可以自动倒退。
- 回滚创建新的 DeploymentRevision，引用旧 ReleaseBundle，不改写旧版本。
- 运行故障创建受治理 Repair Task，不能直接在线修改生产。

### 17.5 Release Controller v3 持久对账

外部 Controller 可能在已完成变更后才让平台超时，因此“没收到 HTTP 响应”不等于“没有发布”。每个 Preview/Production v2 Run 必须在访问网络前与稳定 Release Delivery Operation 同事务持久化。Operation 保存 canonical `release-delivery-operation-document/v3`、独立 request hash、exact Controller identity、完整 ReleaseBundle/Preview/Approval/rollback lineage 和 production expected head；崩溃恢复只读这份冻结请求。

交付协议必须满足：

- 首次提交是按客户端稳定 Operation ID 的 `PUT /v3/delivery-operations/{id}`，`Idempotency-Key` 与 request hash 在重试间不变。
- 超时、断线、5xx 或不合法响应只产生 `submit_unknown`，Run 进入 `reconcile_wait`。lease 接管后使用同一 ID/hash `GET`，不能创建新发布。
- `GET` 只有对尚未被 Controller 承认的 `prepared`/`submit_unknown` 返回 `404` 时，才能以同一 ID/hash `resubmit`。已经 `accepted`/`running` 后历史丢失、Controller identity/result/hash 冲突则隔离为 `reconcile_blocked`，禁止自动再发布。
- `submit`/`reconcile`/`resubmit` Attempt、递增 sequence 的 observation 和唯一 terminal Result 均 append-only。Result 以 exact Operation ID/result hash 绑定 v2 PreviewReceipt、ProductionReceipt 和 DeploymentRevision，不允许降级或为 legacy v1 补造 authority。
- HTTPS 先做正常 PKI 验证，再在发送 Token/变更前验证 leaf certificate SPKI SHA-256 pin。readiness 要求 Controller ID、version、`worksflow.release-delivery/v3` 和 trust-key digest 全部精确相等，不跟随 redirect。

migration `000056_release_delivery_operation_reconciliation` 实施这些数据库不变量，并将无 exact Operation/Result 的历史 v1 nonterminal Run 保守迁移到 `reconcile_blocked`。UI 对 `reconcile_wait`/`reconciling` 显示“正在确认控制器结果，禁止重新发布”；`reconcile_blocked` 只提供运维对账信息，不提供普通用户“重试发布”按钮。

migration `000057_release_preview_singleflight` 进一步按 exact `(project, ReleaseBundle ID, Bundle hash)` 建立 nonterminal Preview 部分唯一索引。`queued`、正在处理、结果不确定和 `reconcile_blocked` 都占用同一个确定性 Preview namespace；只有显式终态才释放锁。迁移同时修正 ReleaseBundle projection：调用方给出的非零、非未来 canonical `createdAt` 保持不变，数据库只写 creation transaction identity。

migration `000058_release_delivery_operator_reconciliation` 为无法自动证明的 v2 隔离操作提供受治理恢复：

- Owner/Admin 先读取无副作用 blocked snapshot，再以 exact Run version、quarantine error code、reason 和 Idempotency-Key 创建 Case；普通项目成员只能读取 Case audit。
- Case 是 immutable、append-only canonical evidence，冻结 Run/Operation/request hash、Controller identity、Attempt/observation 计数与最后隔离证据、actor/reason，以及由 PostgreSQL `statement_timestamp()` 产生的审计时间。Case insert、Operation 恢复和 Run `reconcile_blocked → reconcile_wait` 必须同一事务提交。
- Case 不声明远端成功、不补造 Result/Receipt、不改 production expected head，只让同一 Operation ID/hash 在原 pinned Controller 上执行到期 `GET`。一旦 Operation 存在任何 Case，Worker 和数据库都永久禁止 `resubmit`/`PUT`。
- 若 GET 再次返回 `404` 或仍无法证明结果，Operation 重新隔离。只有 Run 已形成新的 `reconcile_blocked` version 后才能追加新 Case；旧 expected version/error 的 CAS 或同 version 第二个 Case 均失败。legacy v1 没有 exact v3 Operation，因此始终不可通过该入口恢复。

migration `000059_legacy_deployment_release_controller_gate` 关闭旧 `/deployments` 与 v3 两个 writer 的交叉竞争。旧路径仅允许 Preview，production Publish/Rollback 被服务层和数据库拒绝；legacy DeploymentVersion insert 与 v3 Run admission 都先锁同一个 project row。新 legacy version 与 parent 必须同时为 `deploying`；parent 或任一 version 的 `deploying` authority 会阻塞 v3，任一 active/uncertain/blocked v3 Run 也会阻塞 legacy Preview。升级在 scan 与 trigger DDL 前锁住四张 writer table，遇到 parent/version 分裂或双 writer authority 会 fail closed；readiness 另行核对 migration、trigger/function、共享锁语义和分裂状态。

migration `000060_release_delivery_nested_authority` 关闭“外层 Operation hash 正确、内嵌文档却复制旧 hash”的缺口。数据库按 canonical JSON 将目标 hash 字段置空后重新计算 ReleaseBundle `bundleHash`，并对 production 的 PreviewReceipt、PromotionApproval 与可选 source DeploymentRevision 重算 `payloadHash`；Helper 对 SQL `NULL`、缺字段和错误 shape 总是 false。upgrade 锁住 Operation table 后完成 scan+trigger DDL；已有坏 authority 会阻塞升级，新写入由 trigger 拒绝。

migration `000061_release_delivery_run_operation_authority` 补齐 inverse authority。已有外键和 Operation insert guard 证明 Operation→exact Run；新的 initially-deferred constraint trigger 要求每个 v2 Preview/Production Run 在事务提交时恰好有一个同 project、同 kind、正确 nullable link 的 Operation。upgrade 先锁两张 Run table 和 Operation table，再扫描 orphan/duplicate，不修补或猜测 authority。Release mutation readiness 要求 exact `000061`，核对 nested 与 Run→Operation trigger/function 的准确身份、enabled/deferrable 模式，并独立扫描 orphan v2 Run。

Delivery worker 的 claim/renew lease 到期时间由 PostgreSQL `statement_timestamp()` 计算，不信任各应用节点时钟。每个 worker store 在 Production/Preview 队列间交替首选权，并继续用 `SKIP LOCKED` 排除重复领取；在单副本领取序列中，持续 Preview 压力最多把可领取 Production 延迟一个成功 claim。Release mutation capability 关闭时 Preview、批准、晋升、回滚和 operator resume 全部 fail closed，但 immutable Bundle、Run、Receipt、Result 与 Case 历史仍由只读 API 提供审计。worker-enabled 进程无法验证 pinned Controller 时会启动失败而非静默降级；维护期只读访问必须使用 worker-disabled 或独立 API deployment。

这套协议关闭的是平台内“不确定结果导致重复变更”和 authority projection 缺口，不是外部部署证据。migration `000057`–`000061` 的定向内部 release/migration、race、`go vet` 与真实 PostgreSQL canary 已通过；包含 `000066` 角色/helper 边界、raw-chain 兼容与 project-scoped GIN planner canary 的最终完整 migration suite 也已在真实 PostgreSQL 上通过（`go test ./migrations -count=1`，448.286s）。本仓库当前仍没有真实 Controller deployment/qualification、Registry/KMS、目标 cluster 或 approved Golden TemplateRelease 的 Preview→Production→rollback 黑盒证据，所以不得签署 Stage 4 退出或宣称外部发布已批准。

Workflow Handoff 之后的专用 profile-v3 接线以
[Workflow Execution Profile v3: Qualified Release Runtime Contract](./workflow-execution-profile-v3-runtime.md)
为规范：它冻结 Quality completion 内 gate typed input、唯一 WIA activation authority、
authenticated `ActionPublish`、migration `000084` 的 equivalence/authorization/result
no-bypass ledger，以及同一 Release Controller operation 的 lease/replay/crash 语义。仓库内
已实现 migration `000083` 的 Canonical Review forward-equivalence 前置边界、migration
`000085` 的同事务 Quality material/precommit/candidate snapshot、opt-in v3 runtime 与
activation worker，以及 migration `000084`、专用 qualified publisher/worker、独立
operator 配置和 readiness 接线；静态合同与定向单元回归已通过。该链仍默认关闭、未在
目标环境激活。migration `000085` 的 fresh PostgreSQL 16 `up/down/up`、resolver/ACL、
SQL 与真实 `GORMStore.Commit` 原子成功/回滚、WIA Activate/exact replay 定向 canary
已经通过；它没有跨越 `000080`–`000084` 和真实 Controller，因此不是 PostgreSQL
full-chain 或外部资格证据。

## 18. 浏览器/API Wire Contract

### 18.1 空值规范

为避免历史 nullable/undefined 崩溃：

- API 集合字段必须 required，空集合序列化为 `[]`。
- Map/Record 空值序列化为 `{}`。
- 必填字符串由服务端验证非空；可空字段在 Schema 中显式声明。
- Wire 边界使用 runtime validator 和生成 Client。
- 历史 nullable payload 只在兼容 adapter 规范化，不向下扩散。
- UI 不得对未验证值直接调用 `trim`、`length` 或 spread。
- 每个 Schema 有版本号、兼容窗口和回归测试。

### 18.2 状态转换接口

所有写接口必须包含或派生：

- current state/version 或 ETag。
- actor/capability。
- Idempotency-Key。
- exact base/payload/tree hash。
- transition reason。
- correlation ID。

服务端返回：

~~~json
{
  "state": "...",
  "version": 1,
  "allowedActions": [],
  "blockingReasons": [
    {"code": "...", "message": "...", "sourceRef": null}
  ]
}
~~~

前端可以提前隐藏/禁用按钮，但服务端门禁是唯一权威。

## 19. RBAC、威胁模型与审计

### 19.1 权限

建议新增动作：

- `sandbox.create/view/edit/control/terminate`
- `terminal.open/control`
- `agent.run/cancel`
- `candidate.checkpoint/freeze/rebase`
- `proposal.review/apply`
- `quality.run/view`
- `release.build`
- `deployment.preview/promote/rollback`

Solo Owner 策略不得降低 exact hash、质量或 Canonical Review 条件。

### 19.2 必须覆盖的威胁

- 跨项目/租户读取文件、PTY、事件、Preview。
- path traversal、symlink escape、special device。
- PTY 越权、fork bomb、磁盘/日志耗尽。
- Port Proxy SSRF 和内部网探测。
- WebSocket ticket 重放、Origin 绕过、heartbeat 403 漂移。
- Production LSP 专用 ticket/subprotocol 绕过、foreign model URI、stale diagnostics，以及恶意 server 的 applyEdit/executeCommand/rename/cross-file edit 或 executable plugin 注入。
- Preview 读取平台 Cookie/localStorage。
- 恶意 `postMessage`。
- 依赖安装脚本和供应链污染。
- Repository/AGENTS/文档中的 Prompt Injection。
- Agent 获取 Docker socket、宿主文件或云 metadata。
- Secret 出现在 env、Prompt、日志、Diff、artifact 或浏览器存储。

### 19.3 审计

必须 append-only 记录：

- Session/lease/terminal/process/port 生命周期。
- Candidate journal、Checkpoint、Patch attribution。
- LSP ticket/connection/binding/head rebind、exact TemplateRelease/server/capability identity、method outcome、stale/rate/resource/read-only violation；不记录源码或 unsaved text。
- Agent Task/Attempt/Model/Prompt/Context/Tool。
- Verification command/receipt。
- Proposal/Review/Apply。
- Build/Release/Deployment/Promotion/Rollback。
- Release Delivery Operation/Attempt/observation/Result，以及每次 blocked snapshot 所产生的 immutable operator reconciliation Case。

终端和 Prompt 保留期由租户策略配置；Secret 扫描和脱敏在写入日志前执行。

## 20. 失败恢复、并发与幂等

- Worker lease 过期可接管，旧 fencing token 永远不能回写。
- Candidate/Canonical worker 在成功 claim 后为所有返回路径执行 bounded、与原请求取消解耦的清理。workspace、容器和运行标记以 exact Attempt/fence 标识；跨进程 Attempt lock 串行清理与重新物化，旧 fence 只能删除自身资源，只有仍持有 runtime marker 且不存在新 fence 时才能删除共享 network。cancel、lease loss 或 transition error 均不能让旧 worker 清理新 owner 的资源。
- migration 000052 的持久 cleanup obligation 状态为 `registered -> pending/cleaning -> completed`。worker 先领取 due cleanup，再领取新 execution；双 cleaner 通过 `SKIP LOCKED` 和 lease epoch 只产生一个 owner，崩溃后新 cleaner 可接管，旧 lease 的完成写入被拒绝。
- Canonical Run 若仍为 `queued`、profile policy 已 inactive、且从未创建 Attempt/cleanup/runtime，可由 system reconciliation 直接收敛为 `cancelled`；该路径不得创建虚假的资源或 cleanup 事实。已领取资源的 inactive execution 必须先完成 exact cleanup，再终止收敛。
- Agent 崩溃不修改 base Candidate；只丢弃未提交 worktree。
- 已存在 CandidatePatch/Receipt 时，幂等重试恢复相同结果。
- 新 Model/Prompt/Template/Contract 创建新 Attempt/Proposal，不复用旧语义。
- Canonical head 前进后 Candidate/Proposal 进入 stale/rebaseRequired。
- Rebase 使用 base/current/candidate 三方合并；冲突不自动解决。
- WebSocket 断线通过 cursor/ack 恢复；cursor 过期重新获取 snapshot。
- 重复 create/checkpoint/freeze/apply/deploy 请求使用 Idempotency-Key，不产生重复对象。
- Apply 前锁内重验 Proposal payload、base Revision/tree、accepted subset 和 Receipt。
- Preview admission 以 exact `(project, ReleaseBundle ID, Bundle hash)` single-flight；unknown、reconciling 和 blocked authority 不能因本地 lease 丢失而释放 namespace。
- Delivery Run claim/renew 的 lease deadline 使用数据库时钟；各 worker store 交替 Production/Preview 首选队列，数据库 `SKIP LOCKED` 与 fence 仍是跨副本唯一写入边界。
- blocked v2 Operation 只能由 Owner/Admin 针对 side-effect-free snapshot 的 exact version/error CAS 追加 Case。Case 之后永远只 GET；再次 blocked 必须先产生新 Run version 才能创建新 Case，任何路径都不能强制改 head 或补造 Controller Result。
- 旧 `/deployments` 只允许 Preview，并与 v3 admission 共用 project-row mutex；任一 writer 已 active/uncertain/blocked 时，另一个 writer fail closed。
- v2 Run 与 immutable Operation 必须在同一事务形成双向 exact authority；commit-time deferred guard 拒绝 Run-only direct SQL，upgrade/readiness 也拒绝已有 orphan 或非唯一匹配。

## 21. 首个参考闭环：真实 AI 对话应用

为了直接覆盖此前“无真实 AI、无持久化”的缺口，MVP Reference Project 必须实现：

### 21.1 业务能力

- 创建 Conversation。
- 发送 Message，创建 AI Run。
- SSE 或 WebSocket 流式返回 typed events。
- 取消、失败恢复和显式 retry reason。
- PostgreSQL 持久化 Conversation/Message/Run。
- 页面刷新和服务重启后恢复历史。
- 身份认证、project/tenant filter 和跨租户拒绝。
- Generated Application Model Gateway、额度、超时和审计。
- 前端永远不接触 Provider Key。
- loading/empty/error/ready/streaming/cancelled 状态。

### 21.2 前置 Contract

必须存在：

- Conversation/Message/Run DataContract。
- create/list/send/cancel API Contract。
- 流式 Event Schema。
- Auth/Tenant/Permission Contract。
- AI Provider Port 和 Gateway Contract。
- Retry、rate limit、timeout 和 retention policy。
- Deployment/health/migration Contract。

任何一项缺失时 BuildContract 必须阻塞，不允许生成“页面内存 demo”并声称完成。

截至 2026-07-18，仓库已加入 hash-closed 的 `reference-ai-conversation/v1` 内部 fixture：Requirement Baseline、Blueprint、PageSpec、覆盖 desktop/tablet/mobile 的六状态 Prototype、11 个 project-scoped API operation、Conversation/Message/Run/RunEvent 持久实体、Permission、AI Runtime v2、standalone typed RunEvent Schema、Deployment 和 15 个 blocking Oracle。Constraint Compiler v7 会要求 API/Data v2、project-scoped composite foreign key、Provider Port 精确 schema 引用、API 与 standalone 九分支 typed RunEvent 同构、所有 Page binding 为 required 且由 Prototype 实现、状态/断点 presentation evidence、exact deployment environment allowlist，以及一 AC 一 executable Oracle。它还把 exact FullStack/TemplateRelease hash 所承诺的 Manifest/Layout 投影与 Deployment 做严格 v1 bridge：每个 component 必须恰好一个同 role service、port、health 和 build output，全栈最多一个 migration；mounted source/output、port、health、migration 和 required environment 不一致，跨 release 冲突或 HTTPS/多输出等不可表示组合都会阻塞。删除或弱化矩阵 fail closed，正向结果稳定为 ready BuildContract。该 fixture 的 FullStackTemplate/TemplateRelease authority 明确为 test-only，尚未生成或部署真实应用，因此只属于 `implemented-internal`，不满足本节的运行闭环或阶段退出。

## 22. 分阶段实施

### 阶段 0：RFC 与可信输入

- 批准本文关键决策。
- 修复模板 repository identity、Manifest Schema 和 CI。
- 补 License、lock、SBOM、签名和准入流水线。
- 建立 TemplateRelease/FullStackTemplate。
- 实现 BuildContract/ObligationGraph Schema 和 compiler。
- 将 Must Gap 改为阻塞状态。

退出条件：Golden Stack TemplateRelease approved；Reference Project BuildContract 可稳定编译并对缺失 Contract fail closed。

### 阶段 1：用户可控 Sandbox

- Repository Service/Candidate/Checkpoint。
- Sandbox Manager/Gateway。
- Monaco、文件树、Diff、Problems。
- exact-head literal search 已接入 migration 000062–000065 的 immutable index、single-builder claim、原子 project quota 与 project-scoped composite GIN；同一个 Redis authority 已为 index 与 short/no-trigram/glob 请求执行 query admission，并为 secure `BuildForActor` 的实际 claim owner 执行 first-builder admission；前端 bounded `Retry-After` exact-identity 单次重试与 quota/outage no-refresh 也已实现。000066 的 bounded retention/GC、三角色 definer 边界和独立 operator 已内部实现；生产 group role/login/schema/full DML/secret 仍须外部预置，状态仍为 `implemented-internal`。Production LSP v1 的 LSP-0–3 已内部实现，LSP-4/QA-016 仍须独立外部资格化。Git pack/object-store 与 global symbol index 仍是后续能力。
- xterm PTY、Process Supervisor、2-3 个 Port/HMR Preview。
- 独立 Preview origin。
- Autosave/断线恢复/Rebase/Conflict。
- 单写入者租约。

退出条件：用户可从 exact WorkspaceRevision 打开 Sandbox，编辑和运行前后端；刷新/断线后 Candidate 恢复；Autosave 不重载 Blueprint。

### 阶段 2：AI 编码闭环

- Task Planner/TaskCapsule/ContextPack。
- Codex ephemeral Runner 和 Model Gateway。
- Attempt events、cancel、timeout、retry、fencing。
- 平台 Diff 提取、Changeset、AI/user merge。
- Candidate freeze to Proposal。

退出条件：AI 在独立 worktree 完成一个纵向 Task，用户能审查、合并、撤销；Agent 无权接触 Canonical/部署/Secret。

### 阶段 3：全栈质量

- Node + Python + PostgreSQL 多服务 profile。
- Contract-derived 和 hidden tests。
- Migration、integration、Playwright、axe、安全和 SBOM。
- exact tree VerificationReceipt。
- accepted subset 重验。

退出条件：Reference Project 的真实对话、持久化、认证和租户隔离全部独立验证通过。

### 阶段 4：Release 与部署

- OCI Builder/Registry、MigrationArtifact、ReleaseBundle。
- Preview Namespace、runtime Secret Broker、health/E2E。
- Production Promotion、canary/blue-green、rollback。
- Release/Deployment provenance 和观测；commit-time v2 Run↔Controller Operation authority、exact-Bundle Preview single-flight、unknown-outcome reconciliation，以及 immutable Case 驱动的 GET-only operator recovery。

退出条件：同一 digest 从 Preview 晋升 Production；健康失败可阻止/回滚，且不改写旧版本。

### 阶段 5：模型与模板扩展

- 多 Provider/Agent Adapter。
- ModelProfile、conformance corpus、shadow comparison。
- 第二个认证 FullStackTemplate。
- CRDT 和多人实时协作（独立 RFC）。

### 实施状态快照（2026-07-18）

本节是实施账本，不改变本文的目标契约，也不把“代码已存在”等同于“阶段退出条件已满足”。后续实现者必须先核对本节、迁移 canary 和验收矩阵，再继续扩展。

已实现并验证：

- Template admission、immutable `TemplateRelease`、policy 和 exact `FullStackTemplate` Registry；外部模板只有通过全部证据、License、lock、SBOM 和签名门禁后才可选择。
- `ApplicationBuildContract` compiler、Obligation gate 和 exact BuildManifest/Contract/FullStack binding；实现生成、手工 Proposal、Review 和 Apply 都在服务端重验同一个 exact contract。
- Constraint Compiler v7 已通过 Core 共享的 `ValidateExactSemanticAuthority` 对 frozen RequirementBaseline→Blueprint Page→PageSpec→Prototype 做 strict closure：校验 exact revision/hash、DeliverySlice/Page identity、Must requirement 和 acceptance set、canonical states、fixture/interaction/data binding/trace、Page→API→Permission/role 关系；另外把 Blueprint-owned operation 与 exact API Contract 的 ID/method/path 双向闭包，并拒绝 Contract 多出的 cross-slice operation。API、Data 与 AI Runtime schema registry 均按 `(kind, schemaVersion)` 选择：历史 v1 保持可读审计，新 BuildContract 要求 API/Data/AI Runtime v2；v2 分别强制合法 Response/local-ref 边界、tenant-safe composite foreign key，以及 Generated Application Provider Port/Gateway、durable typed event schema、cancel/retry、tenant-actor fail-closed rate limit、timeout/retention 和 redacted audit。Registry adapter 从 exact FullStack/Release authority 投影 mount、service、command name、port、health、migration、build output 和 environment；`template-deployment-runtime-closure/v1` 只比较 TemplateManifest v1 与 Deployment v1 共同能表示的事实并拒绝 identity、集合、链接和路径漂移。Health method/status、port exposure、environment default 与 Layout 没有 Deployment v1 双向字段，仍由各自 exact hash 或 Deployment Contract 单独承诺，不能声称完整同构。历史 v6 BuildContract 保持不可变可审计，但 generation/implementation 的 `RequireReady` 只接受当前 v7 compiler identity；活动 BuildManifest 必须重新编译，不能用旧 ready 状态绕过新门禁。`reference-ai-conversation/v1` profile validator 再对 11 个 source、4 个实体、11 个 API、SessionAuth/Idempotency、6 个 UI state和三断点 presentation evidence、API/standalone typed RunEvent、migration/exact env allowlist 和 15 个一对一 executable Oracle 做跨合同闭包。`go test ./internal/contracts/... ./internal/core ./internal/constructor -count=1` 已通过；这仍是内部输入权威证据，不是 Golden 运行证据。
- v7 compiler identity 切换属于 mutation contract phase：必须先从写流量排空 v6 API/generation/implementation 节点，再让 v7 节点签发或消费新 BuildContract。旧二进制不含 current-identity gate，不能把 v6/v7 混写窗口描述为安全滚动兼容；若目标环境无法证明旧 writer 已排空，就必须保持构造与下游 mutation 关闭。
- `RepositorySnapshot`、`CandidateWorkspace`、append-only journal、control event、lease/session epoch、exact checkpoint、freeze/abandon gate 的领域模型与 PostgreSQL 约束（migration 000023）。
- `SandboxSession` immutable config/lineage、event projection、quota/TTL、service/port allowlist、dirty lifecycle checkpoint gate 和 resume epoch rotation（migration 000025）。
- Repository tree CAS、真实 file-byte CAS、tenant-scoped immutable file catalog（migration 000026）、动态 TemplateRelease path policy，以及 `PutPending -> SQL commit -> Finalize` 崩溃恢复。
- exact TemplateRelease source materializer：只读取准入记录绑定的 repository/commit/tree，校验 Git tree digest、mount/path/mode、文件与总量上限，并生成 `templates.lock.json`；Repository bootstrap 可由 exact WorkspaceRevision 或 exact admitted template source 创建 RepositorySnapshot，不跟随 branch tip。
- Repository bootstrap 只有在每个 FileBlob 和 tree object 都完成结算、且 migration 000067 的 append-only completion marker 已写入后才返回成功；丢失确认或对象存储暂不可用时返回 `503`，调用方必须使用同一 `Idempotency-Key` 恢复。成功响应携带 compact `repository-snapshot-receipt/v1`：其 content hash 覆盖 BuildManifest、签发时由 current-v7 gate 认可的 exact BuildContract、FullStackTemplate、可选 WorkspaceRevision、tree/object commitment 及 web/api TemplateRelease 的 source/SBOM/signature/Artifact Authority receipt。精确 GET 必须同时提交 Snapshot ID 与 receipt hash，并在返回前重新解析 tree、读取每个 authoritative file byte、重算模板权威投影；错误 hash、未结算、历史篡改分别 fail closed。应用角色只拥有 completion table 的 `SELECT/INSERT`，没有 `UPDATE/DELETE`。
- Content Reconciler 能识别 RepositorySnapshot、Candidate、journal、checkpoint、file catalog、Candidate/Canonical Verification Plan/Receipt，以及 ReleaseBundle、PreviewReceipt、PromotionApproval、ProductionReceipt、DeploymentRevision 的 SQL 可达引用；查询故障、release delivery tables 部分迁移或命名参数解析失败时 fail closed，不会把已提交 pending object 当孤儿删除。
- immutable successor Candidate rebase（migration 000030）：旧 Candidate 原子标记 `stale/rebaseRequired`，新 Candidate 从 exact target BuildManifest 启动，base/current/target 产生 canonical plan hash；无歧义路径自动合并，双边不同修改必须逐文件显式解决，普通写入在 conflict/stale 期间被拒绝。
- public-IP 同源 CORS/WebSocket 校验使用请求的 effective scheme/host；localhost allowlist 不再错误阻断经受信反向代理进入的同源请求，跨源仍需显式 allowlist。

已实现并有定向验证：

- Candidate/Sandbox PostgreSQL service adapter、authenticated façade、bootstrap/lease/checkpoint/read/write API。
- Sandbox lifecycle deadline 控制面（migration 000047）已增加服务端 activity projection、idle/absolute TTL deadline、持久 lease/fence/takeover/retry worker；Sandbox stream 建连和消息会在 exact session epoch 下 touch activity，due worker 对 dirty Candidate 先 checkpoint 再 suspend，absolute TTL 则进入 terminate。
- Candidate journal 与 Sandbox lifecycle 的提交边界（migration 000049）已改为数据库串行化：journal insert 按稳定顺序锁定所有关联的 nonterminal Session，只接受 `ready`；suspend/terminate 先提交时，已在飞行的写入会等待后被 `40001` 拒绝。`failed`/`terminated` 仅作 immutable audit history，不会污染同 Candidate 的新 `ready` Session；真实 PostgreSQL canary 已同时覆盖 lifecycle 竞争和 terminal-history→successor Session 写入。
- Sandbox runtime manager、短时单次 WSS ticket、Redis cursor/replay 和 epoch fencing。
- exact Template command 解析、持久化 Process Supervisor、进程日志/信号 API。
- 非 root PTY、固定 runner helper、append-only terminal audit、typed binary frame、输入/resize/signal/detach 和断线 cursor 补发。
- exact Session port projection、真实端口健康探测、Redis 短期 Preview grant、capability 子域反向代理，以及同一 Preview origin 上的 HTTP/HMR WebSocket；Gateway ingress 默认只绑定 `127.0.0.1`，Compose 的 DinD 例外只绑定其私有 `sandbox-net`。
- Preview 代理会逐请求重验 project membership、Session epoch/state、Candidate projection、Runtime identity 和声明端口；平台 Cookie/控制头不会进入用户应用，父域 `Set-Cookie` 被丢弃，响应带独立 iframe CSP。
- Browser IDE 已接入生产 Repository/Sandbox API：文件树、Monaco、Diff、Problems、xterm、Template service/profile、进程启停、声明端口 Preview、900ms 服务端 Candidate autosave、丢失响应的同一操作恢复，以及 exact Candidate checkpoint。
- ordinary Candidate file read 已升级为完整 request/response fence：请求要求 Session epoch、Candidate ID/version/journal sequence、writer lease epoch、tree hash 与 expected file hash；服务端做 opening/closing head recheck，响应回显 Candidate ID/journal/head/content hash，漂移返回 `409 sandbox_file_head_changed`。默认 CORS 已 allow/expose 所需 headers；自定义 CORS 部署必须同步配置。
- Browser IDE 的真实 Candidate 文件操作已包含 rename/delete；binary 文件不会被 UTF-8 decode 或塞入 Monaco，而是显示 exact hash/mode/byte size 并以原字节下载。Candidate abandon UI 会等待本地保存，对 dirty Candidate 先创建并绑定 exact checkpoint，再以完整 Session/Candidate/lease fences 和必填原因提交；成功后只清理源码工作区指针并发现/创建 successor，不重载 Blueprint/PageSpec/Prototype。
- Candidate exact-head literal search 已实现 generation/root 请求围栏，并严格按 normalize → view authorization → query admission → Candidate Repository I/O 执行；app 将同一个 Redis authority 注入 search 与 secure `BuildForActor`。migration 000062–000065 已提供 immutable manifest/member/blob、durable single-builder claim、原子 project quota 与 project-scoped composite GIN；默认 quota 为 `16 trees / 256 MiB / 2 active builds`。query 默认 project `20/s burst 40` + actor `4/s burst 8`，覆盖 index、short/no-trigram 和 glob；first-builder 默认 project `1/15s burst 2` + actor `1/30s burst 1`，只有 durable claim 的实际 owner 在 FileBlob resolve 前扣 token。ready reuse、waiter 和 follower 不扣 build token。quota/rate/outage/tamper 不得 fallback；malformed/outage fail closed，合法 rate denial 为 `429 + Retry-After`，active-build quota 为 `429`，retained tree/source-byte quota 为 `409`，未知 index failure 为 `503`。两条合法路径都对候选重新核对 opening tree、读取并重验 authoritative raw bytes、跳过 binary，并做 closing head recheck。migration 000066 已实现 bounded retention/GC：默认 `30d / keep 8 / batch 25 / capability 10m`，下限/上限为 `retention >= 7d / keep >= 8 / batch <= 100 / capability <= 15m`，并保护所有 Candidate status/live claim、exact CAS/锁、shared blobs、immutable receipt/tombstone。独立 operator 要求显式 schema 与稳定 run ID；当前平台稳定边界已扩展为五组 `NOLOGIN` role，production-posture 主检查器同时使用 API、migrator、只读 auditor 与 Qualification Promotion operator 四条独立 `LOGIN`/DSN/连接，GC 与 Golden fault 的实际 operator 凭据另行分离；这些身份、dedicated schema、完整 app DML/secret injection 仍须外部预置。CredentialSet 与 qualification evidence 的 durable PostgreSQL Store 均保持 owner-only，未增加第六个生产角色或新的生产 DSN。真实 PostgreSQL + Redis focused Repository 回归（95.205s）以及 GC migration/posture/operator canary、focused `go vet`/unit/race、完整前端 typecheck/unit/lint/production build 已通过。前端 strict DTO/request identity、350ms debounce/cancel、match-open content-hash fence 和 bounded `Retry-After` exact-identity 单次重试均 fail closed；只有 head-changed `409` 刷新 Candidate，quota/outage 不刷新，dirty editor、autosave 与 Blueprint/PageSpec/Prototype 不会被搜索结果覆盖。它们仍只是 `implemented-internal`，不是 Git pack/global symbol index，也不替代 Golden/LSP-4 或生产数据库资格。
- Production LSP Control Plane v1 的 LSP-0–3 已从 docs-first 升级为内部实现：专用 ticket/WSS、Redis ticket/rate/editor lease、PostgreSQL authority/audit、immutable snapshot、digest-pinned read-only runtime、strict Gateway adapter 和 Monaco binding/providers 均已接线；Go、真实 PostgreSQL + Redis 与前端自动化提供内部证据。LSP-4/QA-016 的 approved Golden real language-server、真实 ingress/WSS/browser qualification 仍未完成，当前只能写 `implemented-internal`，不能写 `production-qualified`。
- migration 000050 将 Candidate `abandoned` control event 与 SandboxSession `terminating` 投影作为同一数据库事务提交；runtime/process/PTY/workspace 清理成功后追加 `candidate.abandon_completed`。失败会保留可恢复的 `terminating + abandoned` 状态，同一原请求不会产生第二个 Candidate terminal event，持久化 `abandon_cleanup` lifecycle lease 允许后台 worker 接管并完成终止。
- migration 000051 将 verification output truncation 变为 Candidate/Canonical 的数据库不变量；executor 与两类 worker 同时把任何 truncated outcome 归一为带 `output_truncated` blocker 的 `error`，领域 Receipt 构造和数据库门禁均拒绝 `passed + truncated`。这关闭的是“不完整日志仍通过”的内部证据缺口，不证明 verifier image 或 Golden corpus 已获批准。
- Candidate/Canonical verification worker 在 exact claim 后对正常完成、取消、lease loss 和中途错误统一执行 bounded cleanup；materializer 以 Attempt lock、fence-qualified workspace/runtime marker 防止旧 owner 删除新 fence，Docker cleanup 仅选择 exact Attempt+fence labels，共享 network 只允许当前 marker owner 删除。Canonical release-artifact collection 也在 heartbeat 保护下执行。
- migration 000052 新增 Candidate/Canonical 共用的 `verification_execution_cleanups` exact-fence 持久 obligation。claim/reclaim 必须在同一事务提交 Run、Attempt 和 `registered` cleanup，四个 DEFERRABLE guard 使旧 writer 在滚动升级时 fail closed；正常 Receipt 的每个 Attempt fence 都必须先 `completed`。Candidate active cancel 会将 obligation 置为可重试 `pending`，cleanup failure 有 bounded backoff，lease crash 可被新 epoch 接管并拒绝 stale completion；Canonical queued 且 policy inactive 时则在零 Attempt、零 cleanup、零 runtime 前提下 system-cancelled。
- 真实 PostgreSQL 51→52 upgrade canary 证明：仍持 live lease 的旧 generation 回填为 `registered`，expired、terminal 和已有 Receipt 的 generation 均保守回填为 `pending`，从不伪造 `completed`；双 cleaner 竞争只有一个 claim，lease crash 后 epoch-2 takeover 成功且 epoch-1 stale completion 被拒；包含 completed/cleaning 事实的 non-empty down migration 可回滚。Candidate 与 Canonical Receipt canary 均覆盖 cleanup 未完成时数据库拒绝、完成后接受。
- Sandbox `allowedActions` 只广告已存在服务端控制路径的能力；尚无完整路由/UI 消费的 `view_logs`、`restore_checkpoint`、`new_session`、`view_audit`、`view_snapshots` 保留为兼容枚举但不出现在任何状态投影中。
- Browser IDE 使用 project-level active Candidate 指针区分文档/Blueprint 状态与源码工作区；BuildManifest 前进时创建 successor 并进入 conflict editor，不在每次 autosave 时重新读取或替换 Blueprint、PageSpec、Prototype 或 BuildManifest。
- Repository 提供 project-scoped Candidate head discovery；清空站点数据或跨浏览器进入时可恢复唯一 head，并保留 incoming rebase 关联。`localStorage` 只作为快速缓存；多个可用 head 时客户端进入显式选择器，展示 exact manifest/tree/fence/conflict 状态，用户选择前不按时间或偶然顺序猜测工作区。
- conflict editor 读取三个 immutable file blob 的准确字节，允许选择 target、predecessor 或编辑后的 current；每项决策携带 conflict version，最后一项解决后才清除 successor 的 conflict flag 并恢复 Sandbox。
- Agent control plane（migration 000031–000033）已实现确定性 `TaskCapsule`/`ContextPack`、不可变 `AgentAttempt`、版本/fence 状态机、取消/超时/带必填原因的 exact retry、Redis claim queue、outbox/stream relay，以及只读 finalized evidence bridge；浏览器不直接读取 runner workspace 或 pending object。
- Agent execution 已接入 digest-pinned ephemeral runner contract、Codex adapter、Model Gateway、精确上下文 materializer、受限工具/网络策略，以及由平台捕获的 patch、structured result、stdout/stderr 和 Stage 2 integrity validation。Runner request/execution v3 将 TaskCapsule token/command budget exact 传给 Runner/Model capability；Gateway 按规范化 request bytes + 协议/JSON-node allowance 做保守 input upper-bound 准入，限制 requested output 并记录 provider output usage；Runner 独立重解析 Codex JSONL 的 usage/command identity，第一个超出 `maxCommands` 的命令会主动 cancel。Agent Runner 单测、backend Agent 真实 Redis 定向测试和 app/transport 回归已通过；这仍不是 tokenizer 精确计数，也不构成生产模型资格证据。Provider key 只由服务端 gateway 使用，runner 和浏览器均不持有。
- 本地 Compose 的 Agent 纵向拓扑已补齐：API 与 DinD 在相同绝对路径共享 `agent-worktrees` volume；预加载器只接受 digest-pinned Runner，在嵌套 daemon 中创建 `Internal=true` 的专用网络和 exact-image 内层 relay；外层 relay 与 DinD 共用 network namespace，并且两层都只允许 Responses gateway 路径。真实临时 DinD canary 已证明精确镜像预加载、内部网络、双归属 relay、internal-only probe、禁用后的清理和两跳可达；这只证明本地拓扑，不证明真实模型调用或目标环境隔离资格。
- 当前 Agent executor identity 被收窄为 `codex-cli + openai`，配置其他 provider/adapter 会失败，避免不可变 Attempt evidence 标注与实际执行不一致。Runner 构建输入强制精确 Go/Node image digest、精确 SemVer 和 npm sha512 SRI；2026-07-18 已用 Codex CLI `0.144.6` 的 exact package SRI 完成 Agent/Sandbox 两个非 root 镜像构建、版本断言和 Sandbox PTY/Preview real-Docker canary。该本地镜像尚未由组织 Registry 扫描、签名和批准，不能作为生产 Runner identity。
- 原子 Agent patch merge（migration 000034）使用 Attempt base、当前 Candidate、proposed tree 的三方比较；任一 affected path 并发变化会持久化 immutable conflict plan 且写入数为零，成功时按单一 journal batch 更新 Candidate 并同步 SandboxSession projection。
- Agent patch undo（migration 000035）只恢复该次 merge 影响的路径并保留无关后续修改；路径漂移时同样零写入。Merge/Undo plan 和 application receipt 均不可变、可按同一 Idempotency-Key 恢复。
- Browser IDE 已加入独立 Project Agent 面板：创建/轮询/取消/retry、生命周期事件、operation manifest、结构化结果、validation 和日志审阅；只有 exact Attempt 为 `review_ready` 且 integrity decision 为 `reviewable` 时才开放显式 Merge。Agent 创建、Merge 和 Undo 与 autosave 共用串行变更队列，执行期间编辑/process/PTY/checkpoint 被暂时门禁，成功后只刷新 Candidate tree，不重载 Blueprint 或文档。
- Agent review 不再只信任 operation manifest：`GET /v1/agent-attempts/{attemptId}/patch-file?path=&side=base|proposed` 在 project authorization 和 finalized PatchValidationReceipt 之后，仅对 exact patch 声明路径返回经 tree/hash/size/mode 重验的原字节。前端会对每个 operation 同时加载 base/proposed，文本用 fatal UTF-8 解码后进入 read-only Monaco diff，binary 只显示元数据和下载；用户必须逐 operation 确认 exact 内容，Attempt/patch hash 改变即清空确认，全部确认前 Merge 始终关闭。
- `GET /v1/agent-attempts/{attemptId}/merges` 在 project-view 鉴权后返回服务端不可变 Merge/Application/Undo 历史；刷新、清站点数据或换浏览器都能恢复 Undo 控件，不依赖 `localStorage` 作为权威事实。
- exact Candidate freeze 实现已完成（migration 000036–000038）：服务端在一个 PostgreSQL 事务中锁定 Session/Candidate/checkpoint/fence，冻结 Candidate、撤销 writer lease、同步 Sandbox projection、生成完整文件 Changeset、写入 Proposal 和不可变 freeze receipt；相同 Idempotency-Key 只恢复同一结果。
- Candidate Proposal 绑定 exact RepositorySnapshot、CandidateSnapshot、base WorkspaceRevision/tree、Candidate tree、BuildManifest、ApplicationBuildContract 和 FullStackTemplate。任何操作未接受时 Apply 均 fail closed；全量接受后 CAS Apply 创建的 immutable WorkspaceRevision 会再次计算并核对 exact Candidate tree hash。
- Browser IDE 的 `Create Proposal` 只在 active Candidate 已保存且 exact checkpoint/fence 可冻结时开放；冻结成功后编辑、Agent、PTY、process 和 checkpoint 控件立即按服务端 `allowedActions` 关闭，并在本地加入 Proposal review queue，不重新加载 Blueprint、PageSpec、Prototype 或 BuildManifest。
- `TestCandidateFreezeReviewApplyClosurePostgres` 已在真实迁移后的 PostgreSQL 上覆盖 freeze→intrinsic replay→逐项 review→Apply→immutable WorkspaceRevision，并验证 Session/Candidate 版本、receipt identity 和最终 tree hash；migration 000038 修复了 `candidate_freeze` 被旧 Build Contract generation-claim 触发器误拒绝的问题。
- Candidate Verification control plane（migration 000039–000042）已实现 server-owned Profile→immutable Plan→Run/Attempt→Check/Coverage→Receipt 状态机；claim、heartbeat、transition、超时接管、terminal commit 使用 lease 与 fence，旧 worker 不能写回。
- Candidate freeze 现在必须绑定 exact passed Candidate Verification Receipt；数据库在同一事务提交边界重验 project、CandidateSnapshot、tree、checkpoint、BuildContract、TemplateRelease/Profile/Plan 和 Receipt hash。真实 PostgreSQL canary 已重新覆盖 checkpoint→verification→freeze→review→immutable WorkspaceRevision，失败、过期或 payload 漂移的 Receipt 均不能产生 Proposal。
- 独立质量物化器只读取 Plan 绑定的 immutable CandidateSnapshot tree/file CAS，按 Attempt/fence 建立只读 workspace；执行器只消费 Plan 内 digest-pinned image、argv、working directory 和 runtime policy，限制 capability、PID/CPU/memory、输出并在持久化日志前扫描/脱敏 Secret。
- Canonical quality materializer 会从 exact WorkspaceRevision 保留每个文件的 `100644`/`100755` 语义，分别物化为只读 `0400`/可执行 `0500`；未声明、非 regular 或非法 mode 会 fail closed，不再把脚本统一降级为不可执行文件。
- Full-stack Plan v2 已把 approved TemplateRelease 的 Node/Python toolchain digest、lock digest、Registry、固定 resolver argv 和 `lock+toolchain+profile` cache key 编入 canonical hash。resolver 只挂载 manifest/lock，检查与服务只读挂载缓存并离线执行；PostgreSQL 使用每 Attempt 随机短时凭据和 internal bridge，API 服务及 migration/health/tenant/contract 检查共享该隔离网络。单元失败矩阵证明任一环节失败都不能形成 Freeze authority；本轮 opt-in 真实 Docker fixture 使用 digest-pinned Node/PostgreSQL images，通过 API health、连续两次幂等 migration 和隔离 contract check，结束后确认无残留 fixture container 或 network。
- Canonical Quality 与 Release 控制面（migration 000043–000046）已实现独立 WorkspaceRevision 重验、ReleaseBundle 完整 artifact set、隔离 Preview、exact PromotionApproval、ProductionReceipt、DeploymentRevision 与新修订式 rollback。Preview passed 必须覆盖 migration/health/smoke/contract/Playwright；Production passed 必须覆盖 health/rollout。失败健康检查同样形成不可变 ProductionReceipt，但不能形成 DeploymentRevision；终态绕过、Receipt/Revision 篡改和不完整 Bundle 均由 PostgreSQL 拒绝。
- Production current-head fencing（migration 000048）已将发布按 `(project, environment)` 串行化：Run 创建时锁定并持久 exact expected DeploymentRevision/ProductionReceipt，部分唯一索引保证单个 nonterminal Run，healthy 终态必须先由 exact verifying Run 完成 head generation + 1 CAS。真实 PostgreSQL 定向 canary 已覆盖旧 healthy head 回填、stale expected head 拒绝、两副本 single-flight、缺 CAS 的 healthy 拒绝、stale CAS 零行以及历史 DeploymentRevision 不覆写。
- Release delivery reconciliation（migration 000056）已将 v2 Run 与准确 Controller Operation 在首次网络请求前同事务冻结，持久 canonical request/hash、append-only Attempt/observation/result 和 exact Controller identity。超时不再立即失败并释放 authority，而是 `submit_unknown → reconcile_wait → GET reconcile`；只有尚未承认的 Operation 才能 same-ID/hash resubmit，冲突或远端历史丢失则 `reconcile_blocked`。v2 PreviewReceipt/ProductionReceipt/DeploymentRevision 精确持有 Operation/Result hash，历史 v1 不可升格。HTTPS Provider 在发送请求前以正常 PKI + leaf SPKI pin 验证 Controller，readiness 再比对 exact id/version/protocol/trust digest。
- migration 000057 已对 exact `(project, ReleaseBundle ID, Bundle hash)` 强制 Preview single-flight，并让 uncertain/blocked Run 持续占用锁；ReleaseBundle canonical `createdAt` 不再被 SQL statement time 覆盖。真实 PostgreSQL 并发 canary 覆盖一胜一冲突、终态释放、blocked 保锁与固定 canonical 时间。
- migration 000058 已实现 side-effect-free blocked snapshot、Owner/Admin exact version/error CAS、immutable append-only Case 和同事务 `reconcile_blocked → reconcile_wait`。Case 审计时间由数据库产生，任何 Case 都使该 Operation 永久 GET-only；GET 404 只会重新阻塞，同一 Operation 只有在新 blocked Run version 下才可追加 Case。项目成员可在 mutation worker 关闭时继续读取 Case/历史，v1 保持不可恢复。
- migration 000059 已把 legacy `/deployments` 限定为 Preview-only，并通过共享 project-row lock 串行 legacy DeploymentVersion 与 v3 Run admission；parent/version 必须一致为 `deploying`，upgrade table locks 与 readiness 会拒绝 split/cross-writer authority。migration 000060 以 NULL-total helper 在数据库重新计算内嵌 ReleaseBundle/PreviewReceipt/PromotionApproval/source Revision hash，并以 table lock 关闭 scan+trigger DDL 窗口。
- migration 000061 已用 initially-deferred constraint trigger 强制每个 v2 Run 在 commit 时具有唯一、同项目且正确 kind/link 的 Operation；upgrade 锁表扫描 orphan/duplicate，readiness 核对 exact migration/trigger/function/deferrable mode 并独立扫描 orphan Run，不允许 Worker 静默跳过并永久占用 single-flight。
- Release claim/renew lease 使用 PostgreSQL authority time，不受应用节点时钟漂移影响；每个 worker store 交替 Production/Preview claim priority，并以 `SKIP LOCKED` 保持多副本互斥。Release UI 会从所有 Run 计算最风险状态，unknown 状态 fail closed；mutation capability 关闭时仍加载 immutable Preview/Production/Case audit，不把只读维护模式误当作无历史。旧静态 Publish UI 只提供 Preview，不能作为 production fallback。
- Compose 已完整透传 Release Controller v3 的 worker、controller identity、trust、lease/reconcile/request 边界，并由机器契约同时验证默认禁用和完整 opt-in 投影；这关闭的是“配置写在文档却到不了 API”的本地接线缺口，不代表真实 Controller 已部署或已通过黑盒故障注入。
- `internal/qualificationevidence` 已实现严格 v1 内部资格证据生命周期：公共 `Execute` 只接受 opaque Plan Authority UUID，重验 authority/hash/artifact/exact Evidence Plan bytes/TrustBindings 后，按唯一顺序推进 atomic CredentialSet issuance、run/capture closure、restricted-artifact encryption/plaintext disposition、KMS attestation、exact same-set revoke、artifact index、双签 Receipt、immutable seal 与 read-only reopen/verify；每个 mutation 都先记录 `*-started`，未知结果只能以同一 operation ID/canonical request digest `Inspect`。该 Receipt→snapshot tail 只保留历史 replay。新的 `internal/qualificationreceiptv3` 已实现 snapshot-first canonical/DSSE verifier 与 append-only control service：pre-Receipt snapshot 先 seal/独立验证，Receipt 再以 snapshot digest 为 sole subject；opaque ExpectedResolver 与 keyful canonical trust-policy digest 阻止 wire 自证和换钥。migrations `000073`/`000074`/`000075` 已分别提供 owner-only durable Evidence event/CAS Store、immutable Plan Authority Store，以及 Receipt v3 request/observation/completion Store；后者以数据库时间、精确 raw/JSONB/scalar 闭合、两签请求原子冻结、claim/ACK retry generation、commit-unknown Inspect 和重启恢复保留真实 DSSE payload/envelope，并只从两条冻结签名请求解析 expected payload。这项内部闭包仍不等于真实资格运行。生产接线仍缺 InputAuthority、专用 Plan/Evidence/Receipt operator/login/DSN、终态失败后的 durable abort→exact revoke、一次性交付 claim/ACK、Promotion v2 与 workflow consumer，以及真实 capture/Broker/KMS/HSM/signer/seal/verifier adapters。
- migrations `000076`–`000079` 进一步补齐了 Canonical Review approval receipt authority、review authority hardening、项目/执行配置级 Qualification Policy Authority、完整 Workflow Input Authority，以及 `workflow-engine/v3` 唯一且不可豁免的 external-qualification gate。WIA 以 project-first 事务冻结 definition/run/node/predecessor/Quality/BuildManifest/BuildContract/source Revision/Canonical Review 原字节与 hash，并在同一提交中挂接 node 和 activation event/outbox；Policy 与 WIA PostgreSQL adapters、并发/迁移围栏、commit-unknown 恢复和 production-role posture 已有真实 PostgreSQL 验证。该 posture 会精确核对 `000078` 的 23 个触发器、`000079` 的 5 个触发器及其 10 个执行函数，同时对 definition/run/node/event 四张共享表执行包含 3 个历史 profile/governance 触发器在内的 12 项全量 allowlist，并约束其全部 13 个不同执行函数；缺失、禁用、错误 timing/event/列集/函数绑定、延迟约束属性、任意名称的额外触发器，以及执行函数的 owner/ACL/语言/security/search_path/执行属性漂移都会 fail closed。后续生产链固定为 `000080` Qualification Input Precommit Authority、`000081` Promotion v2、`000082` private Handoff：Promotion closure 必须直接包含完整 typed `inputPrecommit`，并与 Policy independent authorities 分开。三个合同分别见 `docs/qualification-input-precommit-authority-v1.md`、`docs/qualification-promotion-v2.md`、`docs/qualification-handoff-v1.md`。在 `000080` SQL/隔离角色/no-bypass canary、`000081` 更新 consume canary 与 `000082` 原子创建输出 Revision/完成节点全部通过且 runtime activation 另行批准以前，UI 和后端都必须保持批准/完成动作关闭。
- Handoff 后的 profile-v3 发布链当前为 `implemented-internal`、默认关闭：`workflow-engine/v3` 已固化为 `qualified-release-controller-dispatch/v1` / `854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104`；private opt-in runtime、migration `000083` Canonical Review forward-equivalence、migration `000084` qualified-release authority、migration `000085` Quality completion precommit/candidate snapshot、WIA activation worker、独立 operator pool、qualified publisher/worker 及 app/config/readiness 接线均已存在。built-in registry 和 `Current` 仍不启用 v3，feature gates 默认 false。migration `000085` 的 fresh PostgreSQL 16 `up/down/up`、resolver/ACL、SQL/GORM 原子成功与回滚、Activate/exact replay 定向 canary 已通过；本地 Builder 开发库也已从 exact old `000083` + legacy Candidate `000084` 谱系恢复到 exact `000086`，并完成幂等重放。仓库迁移链随后加入 `000087`–`000089` Sandbox absolute-TTL 加固，当前 repository migration head 为 `000089`；本地部署证据仍只覆盖 `000086`。这些事实不等于生产闭环。生产目标仍需 old-hash absence、operator LOGIN/DSN/secret、共享 Workflow 表最小 DML、唯一 Controller bootstrap、跨 `000080`–`000084` 的 PostgreSQL no-bypass full-chain canary 与外部 Golden 资格证据；在此之前不得宣称闭环或资格通过。
- 前端、后端和 relay 的第一方应用 Dockerfile 已要求 digest-pinned Node/Go/Alpine base；正向生产构建通过，传入 `node:22` 或 `golang:1.22` 会在依赖安装前失败。Compose 中 PostgreSQL/Redis/Mongo/NATS/Nginx/DinD 等第三方服务当前仍使用本地开发 tag，生产供应链和部署清单尚未收口。
- Workbench 的 Implementation 视图持续保留 Proposal review、节点切换与 `Complete Workbench` 控制；已应用 Workspace 在 Preview 视图进入持久 Candidate 沙盒，沙盒不会再遮挡多组 Workflow 的后续节点。E2E 平台夹具提供 exact ready ApplicationBuildContract/FullStackTemplate authority，不通过关闭门禁来伪造生成能力。

已完成代码链路、等待外部闭环验收：

- AIC-E2E-004/005/006 的 API、数据库、runner 和前端组件链路均已存在并通过分层测试；仍需使用真正 `approved` 的 Golden TemplateRelease 执行 Playwright 浏览器验收，才能签发阶段退出证据。
- Sandbox/Preview 的产品边界和低摩擦主路径见 `docs/sandbox-preview-product-boundary.md`：Preview 只保留显式启动、健康等待和自动打开，代码视图承载验证/冻结/Agent/生命周期操作；开发预览不表示 Production 或外部资格通过。
- Stage 2 的 Attempt→exact patch-file evidence→逐 operation acknowledgement→three-way Merge→server-history→Undo，以及 Candidate freeze→Proposal review→immutable WorkspaceRevision 代码链路已完成；相关 Go/前端定向回归、真实 PostgreSQL exact-tree 闭环，以及 migration 000047/000049 的 lifecycle/activity/commit-boundary 竞争 canary 已通过。这仍不是 Stage 2 退出签字，原因见下列阻塞项。
- FQP-1 的代码与真实 PostgreSQL 闭环已经覆盖到 migration 000042；FQP-2 的 Plan/worker/materializer/executor、ephemeral PostgreSQL、服务 health、迁移/tenant/contract fail-closed 和只读依赖缓存已有单元、真实 PostgreSQL 与独立 DinD 分层证据。由于尚无 approved Golden release 和真实 verifier image attestations，该 DinD fixture 仍不能冒充生产资格证据，不能签署 FQP-2 退出。
- FQP-4 与 Stage 4 的平台侧 authority/control-plane 已通过 migration 000043–000046 的真实 PostgreSQL Canonical→Bundle→Preview→Approval→Promotion/failed health→Rollback canary；migration 000048 另外通过了 production current-head 回填、single-flight 和 exact CAS 定向 canary。migration 000056–000061 及 v3 Provider/Worker/API/UI 已形成持久 Operation 对账、exact-Bundle Preview single-flight、GET-only operator Case、legacy/v3 cross-writer mutex、nested-hash 重算与 commit-time v2 Run↔Operation 数据库边界；定向内部测试、race、`go vet` 和真实 PostgreSQL canary 已通过。但这些内部回归与目标 Controller 故障注入/黑盒资格是两组不同证据；本文不把前者预先记为后者通过。真实 Controller、Registry/KMS、生产 cluster、Secret Broker、canary/blue-green controller、approved Golden TemplateRelease 和签名 artifact 仍需在目标环境资格验证，当前不能据此签署 Stage 4 退出。
- 前端普通 Playwright 已于 2026-07-18 在分离 Golden entrypoint 后重跑完成：87 passed、0 skipped、0 failed，耗时 3.8m。Golden 不在普通 suite 的选择集中；独立 strict qualification 尚未执行且当前会因外部覆盖/Receipt/credential-safe trace 不完整而 fail closed。87 项仓库内浏览器回归不替代 Golden Stack 资格证据或阶段退出签字。

本轮包含 migration 000050–000052 的稳定态回归已经完成：真实 PostgreSQL + Redis 环境下 `go test ./... -count=1` 全部通过，完整 `internal/verification` race suite 通过，`go vet ./...` 通过；前端 unit、lint、严格 TypeScript typecheck 和 production build 均通过。标准 backend image build 通过，并验证 image 内非 root `worksflow` 用户可执行 Docker CLI 与 `podman-remote`；Podman 只是 remote client，必须显式配置受校验的 daemon host，并通过 `CONTAINER_HOST` 连接。

Release authority 的后续定向验证已覆盖 migration 000057–000061：release/migration unit、release/transport race、`go vet` 与真实 PostgreSQL canary 通过，包含 exact-Bundle 并发、DB-clock lease/Case audit、双管理员 CAS、GET 404 后新 version Case、legacy parent/version 与 v3 writer 竞争、nested NULL/hash 篡改拒绝，以及 orphan v2 Run 的 upgrade/commit/readiness 拒绝。包含后续 `000066` 边界的最终完整 migration suite 随后也在真实 PostgreSQL 上通过：`go test ./migrations -count=1`，448.286s。该结果不构成外部 Controller/环境资格证据。

Candidate exact-head search 合入后，前端严格 TypeScript typecheck、unit、lint 与 production build 已于 2026-07-18 再次全绿。该四项及本轮非 Golden 浏览器回归只验证当前前端代码与协议消费，不替代 approved Golden Stack conformance 或真实外部运行时资格证据。

最终 backend full regression 已在真实 PostgreSQL + Redis 环境执行 `go test ./... -count=1 -timeout 30m` 并以 exit 0 全绿；跨包 migration-lock 竞争下关键包耗时为 `core 896.686s`、`release 437.353s`、`repository 614.687s`、`sandbox 458.522s`、`migrations 846.769s`。隔离完整 migrations 包、全仓 `go vet`/编译、focused race、production/maintenance Compose render 与最终非 root backend image build 也通过。它补强当前仓库内服务/数据库不变量证据，但不替代独立且尚未执行的 approved Golden Stack qualification，也不替代真实外部 Controller/目标环境资格验证。

这些结果证明仓库内控制面、真实 PostgreSQL/Redis、一个真实 Docker fixture 和本地 exact image 的当前不变量，不构成阶段退出。尚未资格化的条件包括：真实 Podman daemon、远端 container daemon 的 delayed mutation/fault injection、共享验证卷的跨副本 advisory `flock`/atomic rename 语义、approved Golden TemplateRelease、credential-backed live model/provider、组织 Registry 批准并带 SBOM/签名/attestation 的 runner/verifier image、Registry/KMS、目标 cluster/RuntimeClass、Secret Broker 及生产 Release Controller。

仍为阻塞项，因此当前不得宣称阶段 1 已闭环：

- 审计的 `https://github.com/ai-worksflow/templates.git` 仍没有符合 v1 admission contract 的可选择 Golden release；详见 `docs/template-admission-audit.md`。
- 没有 approved Golden release 时，生产 bootstrap 必须 fail closed；因此目前无法诚实完成“从 approved TemplateRelease 打开 Browser IDE、启动 web/api/database、Preview 调用真实 API/DB”的 AIC-E2E-003 至 007 连续证据。
- `frontend/tests/golden-stack.spec.ts` 目前只是不使用 mock 的 partial smoke：health、Message create/replay/read、跨租户拒绝和浏览器 save/reload。普通 Playwright 明确排除它；独立 strict entrypoint 缺配置会失败而不是 skip，并在 Template bootstrap、Sandbox/PTTY/Preview、Agent、Verification、Release、LSP-QA-016、credential-safe trace 和 immutable Receipt 全部实现前拒绝 qualification。`qualification/manifest.json` 已将 94 个验收 ID 唯一映射到测试层和必须 artifact，但当前仍报告 0 个 external suite qualified，因此清单不是闭环证据。Binary 查看/下载、rename/delete、Candidate abandon、migration 000062–000066 的 durable exact-tree literal index/claim/quota/project-GIN/retention、query-shape bounded scan、同一 Redis authority 驱动的 query/actual-owner first-builder admission、独立低权限 GC operator，以及前端 bounded `Retry-After` 单次重试均已内部实现。生产 PostgreSQL role/login/schema/full DML/secret injection 仍未由目标环境证明；Production LSP v1 的 LSP-0–3 已内部实现，阶段 1 仍缺 approved Golden Stack 与 LSP-4/QA-016 外部资格，不得宣称外部闭环。

仍为阻塞项，因此当前不得宣称阶段 2 已闭环：

- `AGENT_ENABLED`/worker 默认关闭；生产 `Agent Runner` 镜像必须由部署方选择精确 Codex/工具链版本和 npm SRI、构建、扫描/签名/准入并以 registry digest 配置。当前代码和 build contract 拒绝 mutable image、版本 range/tag 和 SRI 漂移；本地验证的 `0.144.6` snapshot 仍不是组织批准事实。
- Agent budget 的当前实现是保守 input upper-bound 准入、requested/output usage evidence 和 `maxCommands` 超限主动取消；它明确不是 provider tokenizer 精确计数。这一保守策略仍需与选定模型/Provider 协议一同资格验证，不能单独充当生产证据。
- 缺少 approved Golden TemplateRelease 时 authoritative PlanningSource 会 fail closed，真实 AgentAttempt 无法取得可执行 template facts；不能用测试 fixture 或旧 demo Candidate 冒充纵向 Task。
- 尚无连接真实 model gateway、runner、Sandbox 和 approved Golden Stack 的浏览器 E2E，未验证网络故障后的同 key 恢复、cancel fence、跨浏览器 Merge/Undo 恢复及 Agent 无权访问 Secret/Canonical/Deployment 的完整黑盒证据。
- Compose DinD 的共享 worktree、私有 Agent network 和两跳 path-confined relay 已完成本地真实 DinD preloader/connectivity canary；runner image、模型路由、Gateway/Preview 隔离和失败恢复仍需在目标部署环境完成资格验证，本地 canary 不能替代该部署证据。
- 当前 Docker fixture 没有覆盖真实 Podman daemon、远端 daemon delayed mutation/fault injection，亦未证明部署共享卷在多个 worker 副本之间提供一致的 advisory `flock` 与 atomic rename；这些条件必须在目标环境单独资格化。

当前最低验证集：

~~~sh
make compose-check

cd backend
WORKSFLOW_TEST_POSTGRES_DSN='postgres://...' \
WORKSFLOW_TEST_REDIS_ADDR='127.0.0.1:6379' \
  go test ./... -count=1
go test -race ./internal/verification -count=1
go vet ./...

cd ../frontend
pnpm typecheck
pnpm test:unit
pnpm lint
pnpm build
pnpm test:e2e

# 仅作当前 partial smoke；token file 必须 0600、短时且由外部凭证系统签发
WORKSFLOW_GOLDEN_STACK_URL='https://...' \
WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_ID='uuid' \
WORKSFLOW_GOLDEN_TEMPLATE_RELEASE_HASH='sha256:...' \
WORKSFLOW_GOLDEN_E2E_TOKEN_FILE='/secure/ephemeral-token' \
WORKSFLOW_GOLDEN_TOKEN_EXPIRES_AT='<ISO-8601, 2-30 minutes from now>' \
WORKSFLOW_QUALIFICATION_RUN_ID='uuid-v4' \
WORKSFLOW_QUALIFICATION_ARTIFACT_DIR='/new/absolute/evidence/run-id' \
pnpm test:e2e:golden-smoke

# 当前会因 executable coverage/Receipt/credential-safe trace 未完成而 fail closed
pnpm qualification:golden
~~~

Repository/Sandbox PostgreSQL canary 需设置 `WORKSFLOW_TEST_POSTGRES_DSN`；未设置时相关测试会明确 skip，不能把 skip 记录当作数据库闭环证据。

## 23. 端到端验收矩阵

### 23.1 正常链路

| ID | 场景 | 必须证据 |
|---|---|---|
| AIC-E2E-001 | 冻结批准的需求/蓝图/PageSpec/Prototype/Contracts | exact refs/hashes |
| AIC-E2E-002 | 编译 BuildContract | ready、content hash、Obligation 100% 可实现 |
| AIC-E2E-003 | 从认证 TemplateRelease 创建 RepositorySnapshot | 全部 FileBlob/tree 已结算、append-only completion marker、exact RepositorySnapshotReceipt ID/hash、BuildManifest/当前 BuildContract/FullStackTemplate/WorkspaceRevision、commit/tree/SBOM/signature/Artifact Authority receipt，并由精确 GET 重验 authoritative bytes |
| AIC-E2E-004 | 浏览器打开 Sandbox | session epoch、base Revision/tree |
| AIC-E2E-005 | 编辑、Autosave、刷新/断线恢复 | exact journal/head/checkpoint/session epoch，且 Blueprint/PageSpec/Prototype 不重载 |
| AIC-E2E-006 | PTY 启动 web/api/database | process/port/health events |
| AIC-E2E-007 | Preview 调用真实 API/DB | browser/runtime logs 和集成测试 |
| AIC-E2E-008 | AI 完成独立 Task | TaskCapsule、Attempt、平台 Diff |
| AIC-E2E-009 | 用户并发修改 | 三方合并或显式 conflict |
| AIC-E2E-010 | 真实 AI 对话 | stream、persist、refresh、auth、tenant tests |
| AIC-E2E-011 | 独立质量通过 | exact tree VerificationReceipt |
| AIC-E2E-012 | Freeze/Review/Apply | Proposal payload、Canonical Review、CAS |
| AIC-E2E-013 | 创建 WorkspaceRevision | exact applied tree 和 lineage |
| AIC-E2E-014 | 构建 ReleaseBundle | service/migration digests、SBOM、signature |
| AIC-E2E-015 | Preview 通过 | 持久 v3 Operation/request hash/result hash、health/smoke/E2E 的 v2 PreviewReceipt |
| AIC-E2E-016 | 晋升生产 | 相同 digest、approval、expected-head CAS、v2 ProductionReceipt/DeploymentRevision 的 exact Controller Result ref |
| AIC-E2E-017 | 回滚 | 新 DeploymentRevision 指向旧 ReleaseBundle，但具有自己的稳定 Operation/Result |
| AIC-E2E-018 | 提交超时后恢复 | `submit_unknown→GET reconcile`，零新 Operation/零重复部署 |
| AIC-E2E-019 | 同一 exact Bundle 并发创建 Preview | 数据库仅接受一个 nonterminal Run；uncertain/blocked 保持锁，显式终态后才可创建 successor |
| AIC-E2E-020 | 隔离 Operation 的受治理恢复 | exact blocked snapshot/version/error、immutable Case、同 ID/hash GET、零 PUT；再次 blocked 后仅新 Run version 可追加 Case |
| AIC-E2E-021 | Release mutation 维护模式 | Preview/批准/晋升/回滚/resume 全部关闭，但 Bundle/Run/Receipt/Result/Case audit 可读取 |
| AIC-E2E-022 | v2 Run 与 Controller Operation 原子创建 | 同一事务内 Operation→Run FK/insert guard 与 deferred Run→Operation guard 双向成立，commit 后恰好一组 exact authority |
| AIC-E2E-023 | exact-head Candidate literal search/retention | normalize→view auth→query admission 位于 Candidate Repository I/O 前，000062–000065 exact index/claim/quota/project-GIN、默认 quota、同一 Redis authority 覆盖 index/short/no-trigram/glob、secure `BuildForActor` 仅由实际 claim owner 扣 first-builder token、malformed/outage/tamper fail-closed、rate `429 + Retry-After`、active quota `429`、retained quota `409`、unknown index `503`、authoritative-byte/binary/closing-head recheck、严格 DTO/request identity、bounded exact-identity 单次重试、quota/outage no-refresh，以及 match-open generation/root/content hash 全部一致；000066 另需证明 retention/keep/batch/capability 边界、全状态 Candidate/live-claim 保护、exact CAS/locks、shared blob、canonical short auth、receipt/tombstone、三角色 ACL 和相同 run-id 崩溃恢复 |
| AIC-E2E-024 | exact ordinary Candidate file read | 七个 request headers、opening/closing head recheck、完整 response Candidate/journal/head/content fence 与 CORS expose；漂移返回 409 且旧 bytes 不进入 Monaco |
| AIC-E2E-025 | Production LSP v1 read-only binding | 专用 ticket/WSS、统一 head/document fences、exact server identity/capability、stale-drop/reconnect/undo、Candidate-CAS-only save 与 approved Golden real-server receipt |

### 23.2 必测失败链路

| ID | 故障 | 预期 |
|---|---|---|
| AIC-FAIL-001 | 缺 API/Data/Auth/AI Contract | BuildContract blocked |
| AIC-FAIL-002 | Template Manifest/License/lock 不合格 | TemplateRelease 不得 approved |
| AIC-FAIL-003 | 上游 Revision 改变 | Candidate/Proposal stale，要求显式 rebase |
| AIC-FAIL-004 | Proposal payload/decision 改变 | 原批准失效 |
| AIC-FAIL-005 | Agent 修改 protected path | Attempt/verification failed |
| AIC-FAIL-006 | 编造 API/字段/Revision | contract/trace gate failed |
| AIC-FAIL-007 | Agent timeout/cancel/crash | base Candidate 不损坏，新 Attempt 才能重试 |
| AIC-FAIL-008 | WebSocket 中断 | cursor 续传或 snapshot reset |
| AIC-FAIL-009 | 用户/Agent 同文件修改 | 显式 conflict，不静默覆盖 |
| AIC-FAIL-010 | 非允许 Registry/公网 | egress denied 且记录 finding |
| AIC-FAIL-011 | Secret 出现在 Diff/log/artifact | blocker |
| AIC-FAIL-012 | Migration 失败 | Preview/Production 不晋升 |
| AIC-FAIL-013 | Preview health/E2E 失败 | production action 不允许 |
| AIC-FAIL-014 | accepted subset 未重验 | Apply 被拒绝 |
| AIC-FAIL-015 | 重复 Idempotency-Key | 返回相同对象，不重复创建 |
| AIC-FAIL-016 | 新模型未过 conformance | ModelProfile 不激活 |
| AIC-FAIL-017 | nullable historical payload | compatibility adapter 规范化，UI 不崩溃 |
| AIC-FAIL-018 | 非 Canonical 上游批准 | Workflow approval action 不允许 |
| AIC-FAIL-019 | Controller PUT 超时/断线 | 持久 unknown 并以同一 ID/hash GET，UI 禁止重新发布 |
| AIC-FAIL-020 | 首次自动对账 GET 404 或 Controller identity/result 冲突 | 尚无 Case 且仅 `prepared`/`submit_unknown` 可 same-ID/hash resubmit；已承认/冲突操作进入 `reconcile_blocked` |
| AIC-FAIL-021 | migration 000056 遇到 legacy v1 nonterminal Run | 保守变为 `reconcile_blocked`，不补造 v2 Result/Receipt authority，也不能走 v2 operator resume |
| AIC-FAIL-022 | 同 project/exact Bundle 已有 nonterminal、unknown 或 blocked Preview | API 返回 conflict，数据库唯一索引拒绝第二权威；不能以新 idempotency key 绕过 |
| AIC-FAIL-023 | 非 Owner/Admin、旧 Run version、错误 quarantine code 或漂移 Controller identity 提交 Case | 零 Case、零 Run/Operation 变化，返回授权或 CAS conflict |
| AIC-FAIL-024 | 任一 Case 后 Controller GET 返回 404 | Operation 重新 `reconcile_blocked`，永久不 PUT/resubmit；只有新 blocked Run version 可追加下一 Case |
| AIC-FAIL-025 | legacy production、parent/version 状态分裂或 legacy/v3 writer 竞争 | service 与数据库 fail closed；upgrade table lock/project-row lock 串行竞态，readiness 拒绝 split authority |
| AIC-FAIL-026 | Operation 外层 hash 正确，但内嵌 Bundle/Receipt/Approval/Revision 为 SQL NULL、缺字段或 payload/hash 漂移 | NULL-total helper 令 migration upgrade 或 insert trigger 拒绝，不形成 Controller authority |
| AIC-FAIL-027 | 应用节点时钟大幅超前/落后 | claim/renew lease 与 Case audit 仍采用 PostgreSQL 时间，旧 fence 不能写回 |
| AIC-FAIL-028 | direct SQL 只创建 v2 Run，或 upgrade/readiness 发现 orphan/非唯一 Operation | deferred commit、migration scan 或 readiness fail closed；不领取、不补造 Operation/Result |
| AIC-FAIL-029 | Candidate search 请求 head 已 stale、扫描中 head 漂移，或 match 打开前 generation/root/content hash 改变 | 服务端或浏览器 fail closed；closing drift 返回 409，清除 stale result 并只刷新 Candidate/Session/tree 投影，不覆盖 dirty editor 或 Blueprint/PageSpec/Prototype |
| AIC-FAIL-030 | Candidate search DTO 缺字段、nullable/widened/请求身份不一致，或 binary 被误作 UTF-8 文本 | strict parser 拒绝响应；binary 跳过并计数，不渲染不可信结果；dirty/saving/mutation 期间暂停搜索与打开 |
| AIC-FAIL-031 | ordinary file read 任一 request/response fence 缺失、非法、不一致，opening/closing head 漂移，或自定义 CORS 漏 allow/expose header | fail closed；并发漂移返回 `409 sandbox_file_head_changed`，旧 bytes 不进入 Monaco，不降级为裸 GET |
| AIC-FAIL-032 | LSP ticket 过期/重放、Origin/RBAC/profile scope 漂移、Redis unavailable 或 WSS subprotocol 不准确 | Upgrade 前拒绝或关闭；ticket 已烧毁，必须经 authenticated HTTP 重新签发，不复用通用 WS |
| AIC-FAIL-033 | LSP 任一 SandboxHeadFence/DocumentFence stale、旧 connection/binding/sequence，或 reconnect 遇到 dirty/remote change | deterministic stale-drop；head/lease等漂移 4409 后 fresh ticket，复用 Monaco model/undo且进入显式 conflict，不覆盖 |
| AIC-FAIL-034 | language-server identity/capability 漂移，或请求 applyEdit/executeCommand/rename/format/code action/cross-file edit | binding 隔离；Gateway 不转发、只读 mount 零变化、Candidate 仍只能经 CAS 写入 |
| AIC-FAIL-035 | LSP DTO/method/URI/result malformed/unknown/oversized，或触发 rate/runtime resource limit | strict adapter 丢弃并返回规范 error；bounded backoff/终止 offending binding，不拖垮 autosave、PTY、search 或通用 WS |
| AIC-FAIL-036 | 只有 fake/mock/local-tag/普通 Playwright 或 skipped Golden language-server test | 最多形成 `implemented-internal` 测试证据；不得标记 LSP-4/QA-016 passed、`production-qualified` 或 Stage 1 外部退出 |
| AIC-FAIL-037 | 000066 前缺任一 NOLOGIN group role、API/migrator/operator 复用 LOGIN/DSN、session/current role 不同、schema/DB 可写或 owner、operator/API 获得错误表/definer 权限 | migration conditional grant 不得被假设会补跑；API/operator readiness fail closed。用新 reviewed migration 修复，禁止编辑旧 checksum/手工 DELETE；未知 GC 结果只可 same run-id/policy 对账 |

## 24. 代码落点建议

新增包建议：

~~~text
backend/internal/constructor/   BuildContract、Obligation、Task Planner
backend/internal/contracts/     Machine Contract schema、version registry、Application Profile closure
backend/internal/contracts/reference/  hash-closed Reference AI Conversation fixture
backend/internal/templates/     Template admission/registry/release
backend/internal/repository/    Git tree、Candidate、Snapshot、journal
backend/internal/sandbox/       Session manager、gateway、PTY/process/port
backend/internal/agent/         Driver、Attempt、Model Gateway client
backend/internal/verification/  多服务 profile、Receipt、hidden tests
backend/internal/release/       OCI、ReleaseBundle、Preview/Promotion

frontend/components/worksflow/ide/
frontend/lib/sandbox/
frontend/lib/agent/
frontend/lib/repository/
~~~

现有扩展入口：

- `backend/internal/generation/service.go`：从单次实现生成迁移到 Task Orchestrator。
- `backend/internal/core/workbench.go`：BuildContract/TemplateRelease 引用和 lineage。
- `backend/internal/core/implementation.go`：Changeset、Receipt、Gap 和 CandidateSnapshot。
- `backend/internal/delivery/quality.go`、`sandbox.go`：保留受信质量边界，扩展多服务 profile。
- `backend/internal/delivery/build_artifact.go`、`publish.go`：扩展 ReleaseBundle/OCI Provider。
- `backend/internal/workflow/platform_adapters.go`：新增 Task/Sandbox/Verification/Release 节点能力。
- `frontend/components/worksflow/workbench/platform-workbench.tsx`：由静态 textarea/srcDoc 迁移到真实 IDE/Sandbox。
- `frontend/lib/platform/websocket.ts`：保留领域事件连接；Sandbox 高频通道使用独立 Gateway。
- `docker-compose.yml`：仅承担本地参考实施；生产沙盒不复用 privileged DIND。
- `backend/cmd/agent-model-relay/`：两跳 path-confined Model Relay 的可执行实现。
- `agent-runner/`：Codex ephemeral Runner、契约和 exact output schema。
- `deploy/prepare-sandbox-runtime.sh`：嵌套 daemon 的 digest preload、internal network 和 relay reconciliation。
- `deploy/check-compose-contracts.sh`：Release/Agent passthrough、共享 worktree 和 relay topology 的机器检查。
- `sandbox-runner/validate-runner-build-args.sh`：Runner base digest、Codex SemVer/SRI 与 Dockerfile wiring 合同。
- `qualification/manifest.json`：验收 ID → 测试层 → 必须 artifact 的唯一机器映射；外部未覆盖项保持 `not-qualified`。
- `backend/internal/qualificationreceipt/` 与 `backend/cmd/qualification-receipt/`：历史 wire-v2 external-only 资格 Receipt 的严格验证边界；现有 Receipt→snapshot 形状只读，不得继续签发或原地升格。
- `backend/internal/qualificationreceiptv3/`：snapshot-first wire-v3 canonical predicate、exact decoded payload digest、opaque ExpectedResolver、keyful trust-policy digest与独立双角色 DSSE verifier；它还没有 durable signing Store 或真实 adapter。
- `backend/internal/qualificationevidence/` 与 `backend/internal/qualificationplanauthority/`：资格证据的 strict v1 lifecycle、started/Inspect 恢复边界、exact hash closure、opaque immutable Plan Authority，以及 Memory test Store 与 owner-only durable PostgreSQL Store；它们不提供 production InputAuthority/operator 或真实外部 adapter。
- `docs/qualification-evidence-orchestrator.md`：证据生成顺序、歧义结果恢复、abort-revoke/plan-resolution/claim-ACK 缺口和 `complete` 的非批准语义。
- `docs/qualification-promotion-operator.md`：外部资格证据的信任根、时间链、加密分类、封存、恢复和不得越权为全局节点批准的操作规范。
- `docs/golden-qualification-control-plane.md`：Golden Authority/Fixture v2、五类真实运行时、22-case inventory、一次性故障权威、原子凭据集与 post-run artifact hygiene 的完整执行契约。
- `docs/workflow-execution-profile-v3-runtime.md`：Handoff 后 authenticated Publish、same-content equivalence、migrations 83/84/85、Release Controller lease/replay/crash 与 rollout 的 implemented-internal/activation/qualification 边界合同。
- `backend/internal/contracts/reference/ai-conversation/`：Reference AI Conversation 的 immutable component manifest、完整 semantic/machine Contract 与 typed RunEvent fixture；测试模板 authority 不得作为外部 admission evidence。

代码包名在实施前可通过 ADR 调整，但领域边界、权威关系和验收 ID 不得无记录改变。

## 25. 开放问题与后续子文档

本文保持 Draft。以下问题批准前不得宣称 `production-qualified` 或签署阶段退出；这不否定实施账本中已明确限定的 `implemented-internal` 状态：

1. 生产 Sandbox RuntimeClass 选择 gVisor、Kata 还是 Firecracker。
2. Candidate Snapshot、PTY 和 Agent 日志的精确保留期/成本策略。
3. Solo Owner 在 Production/破坏性 Migration 的组织级策略。
4. 首个 Generated Application AI Streaming 选择 SSE 还是 WebSocket。
5. 视觉验证阈值和可维护的 Prototype Oracle。
6. 第二个 FullStackTemplate 选择。
7. CRDT 多人编辑方案。
8. OCI Registry、KMS/Vault 和 Kubernetes Provider 的具体实现。
9. 首个 approved Golden TemplateRelease 采用哪些 exact TypeScript/Python language-server image、server version 和 capability profile；该选择不能由 PATH 或 mutable tag 默认决定。

建议后续拆分并批准：

- Template Release Contract。
- Application Build Contract/Obligation JSON Schema。
- Sandbox Session/Candidate Protocol。
- Production LSP Control Plane/Language Server Profile Contract。
- Agent Runner/Model Adapter Contract。
- Full-stack Quality Profile。
- Release/Deployment Specification。
- Threat Model。
- Reference AI Conversation executable application、真实 Gateway 与外部 qualification specification。

## 26. 相关资料

- [Worksflow 全栈协同生成平台架构](./platform-architecture.md)
- [Workflow I/O contracts and executable capabilities](./workflow-io-contracts.md)
- [Backend development](./backend-development.md)
- [Full-stack Quality Profile 与 VerificationReceipt 实施规格](./full-stack-quality-profile.md)
- [Worksflow 生成阶段工作台与团队协作原型文档](./worksflow-generation-workbench-prototype-spec.md)
- [Design Import Center](./design-import-center.md)
- [templates repository](https://github.com/ai-worksflow/templates)
- [templates index at audited main commit](https://github.com/ai-worksflow/templates/blob/1edacd73910415c0e0e0429e60e09714a873776d/TEMPLATE_INDEX.json)
- [FastAPI audited template](https://github.com/ai-worksflow/templates/tree/1721440b33563b45192ffbb15da724d11f5f158f)
- [React shadcn-style audited template](https://github.com/ai-worksflow/templates/tree/72664c5dc5cced39bc185f2f7e08dc6652a80ee3)
