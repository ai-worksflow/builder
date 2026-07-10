# Worksflow 全栈协同生成平台架构

版本：v1.1

日期：2026-07-10

状态：实现基线

## 1. 核心目标

Worksflow 不是把“文档、蓝图、原型、代码”做成四个彼此独立的编辑器，而是提供一套可组合的制品工作流：

```text
对话提出意图
  -> 控制平面把意图转换为可审阅、可授权的命令
  -> 工作流把命令编译成带类型且带 Hash 的节点输入
  -> AI 读取冻结 InputManifest/NodeInputEnvelope
  -> AI 返回可审阅 OutputProposal，而不是直接修改事实
  -> 人工选择、评审和批准
  -> 服务端原子应用到带 ETag 的 Draft
  -> 人工提交并形成不可变 Revision
  -> 下游按准确 Revision + Hash 消费
  -> ManifestCompiler 汇总为 ApplicationBuildManifest
  -> Workbench 产出 ImplementationProposal
  -> 人工接受后写入工作区、测试、预览和发布
```

最小闭环只是系统自带的第一份、随项目初始化的 WorkflowDefinition 模板，不是写死在页面或执行器里的固定顺序。完整闭环打通后，用户可自由增加、删除、连接和配置类型节点；自由组合改变的是编排，不改变制品的数据契约，也不允许 AI 绕开版本、评审、质量和权限门禁。

## 2. 四个平面与三个图

### 四个平面

| 平面 | 责任 | 典型对象 |
|---|---|---|
| 控制平面 | 把对话转换为明确命令 | Conversation、WorkflowIntentProposal、Command |
| 编排平面 | 决定节点何时运行、等待和失败恢复 | WorkflowDefinition、WorkflowRun、NodeRun |
| 数据平面 | 保存团队事实及准确版本 | Artifact、Draft、Revision、TraceLink |
| 执行平面 | 生成、验证和交付应用 | ApplicationBuildManifest、ImplementationProposal、WorkspaceRevision |

### 三个图

1. 制品依赖图：由 Revision 消费关系和 TraceLink 自动投影，不能人工维护第二份事实。
2. 产品蓝图：描述 Feature、Page、Component、API、Data、Permission 的结构关系。
3. 工作流图：描述 AI 转换、人工编辑、评审、条件、并行、合并、质量和发布的执行关系。

## 3. 权威事实边界

| 功能 | 回答的问题 | 权威事实 |
|---|---|---|
| 文档 | 为什么做、为谁做、怎样验收 | Requirement、AC、Rule、Constraint、Decision |
| 蓝图 | 产品由什么组成、怎样连接 | Feature、PageSpec、Component、API、Data、Permission |
| 原型 | 页面怎样呈现和交互 | Scene、State、Breakpoint、Layer、Interaction、Fixture、Token |
| Workbench | 怎样实现和验证 | FileOperation、Route、Migration、Test、Preview、ImplementationTrace |

PageSpec 是蓝图 Page 的一等制品。“页面拆分文档”只是 PageSpec 的只读投影，不再保存一份可独立漂移的正文。

## 4. 统一版本模型

所有正式制品共享以下不变量：

- Artifact 是长期逻辑身份。
- Draft 是可变工作副本，并使用 ETag/版本号做乐观并发。
- Revision 是不可变快照，包含内容 Hash、父版本和来源。
- Review 必须绑定具体 Revision 与 Hash。
- 已批准 Revision 不允许更新或删除；回滚通过创建新 Revision 完成。
- 下游只保存准确 `artifactId + revisionId + contentHash + anchorId`。
- AI 只提交 Proposal；只有服务端可验证并应用 Proposal。

标准引用：

```json
{
  "artifactId": "art_...",
  "revisionId": "rev_...",
  "contentHash": "sha256:...",
  "anchorId": "REQ-001"
}
```

## 5. 文档契约

正式文档采用稳定 Block ID，而不是只有 Markdown：

- `projectBrief`
- `productRequirements`
- `decisionRecord`
- `glossaryPolicy`
- `referenceSource`
- `changeRequest`

主要 Block：`richText`、`goal`、`actor`、`userJourney`、`requirement`、`acceptanceCriterion`、`businessRule`、`constraint`、`nonFunctionalRequirement`、`metric`、`openQuestion`、`decision`、`sourceReference`。

Requirement 与 Acceptance Criterion 使用跨版本稳定业务键（如 `REQ-001`、`AC-001`）。AI 的修改输出为基于 Block ID 和 expectedHash 的操作集合，可逐项接受并在一个事务中应用。

文档审批门禁：

- 必填区块完整。
- 每个 Must Requirement 至少包含一条 AC。
- 没有阻塞问题和阻塞评论。
- 必需评审人已经决策。
- 候选 Hash 与当前 Revision 完全一致。

批准后确定性生成 RequirementBaseline，蓝图只消费该基线。

内置流程把这一步作为 `decompose_pages` 节点契约的一部分：节点先用全部准确、已批准的需求来源确定性编译或复用 RequirementBaseline，再冻结仅包含该 Baseline Revision 的 AI InputManifest。自定义流程可复用同一 job contract，不能让蓝图 AI 退回读取可变草稿或绕过 Baseline。

## 6. 蓝图契约

可编辑语义节点：

- `feature`
- `page`
- `component`
- `apiOperation`
- `dataEntity`
- `permission`

只读投影引用：`requirementRef`、`acceptanceCriterionRef`、`prototypeRevisionRef`、`implementationRef`、`testCaseRef`。

边采用有限词表：`drives`、`satisfied_by`、`contains`、`navigates_to`、`uses`、`calls`、`reads`、`writes`、`requires`、`realized_by`、`implemented_by`、`verified_by`。

布局独立保存，移动节点不会制造语义 Revision。服务端拒绝悬空边、非法方向、`contains` 环、重复 Method/Path、无 Feature 的 Page、缺少权限保护以及 Must Requirement 覆盖缺口。

“从文档生成蓝图”产生 BlueprintProposal，不直接覆盖 Draft。需求新版本批准后由确定性图遍历生成 ImpactReport，并将受影响 DeliverySlice 标为 `needs_sync` 或 `blocked`。

## 7. 原型契约

一个 PageSpec 对应一个逻辑 PrototypeArtifact；每个 PrototypeArtifact 可有多个不可变 Revision。原型包含：

- PageSpec 声明的所有页面状态，不硬编码四种状态。
- desktop/tablet/mobile 或项目定义的 breakpoint。
- 一份语义图层树与状态/断点 Override。
- 声明式交互白名单，禁止任意 JavaScript。
- 版本化 Fixture、DesignSystem、TokenSet、ComponentRegistry。
- 属性级来源和 AI 策略：`replaceable`、`suggestOnly`、`preserve`。

AI Proposal 按 base/current/proposal 三方合并。人工保护内容不得被静默覆盖。评论可锚定 Revision、State、Breakpoint、Layer、Property 或 Region。

只有批准且非 `needs_sync` 的 PrototypeRevision 默认可生成 WorkbenchHandoffBundle。

## 8. 通用工作流

WorkflowDefinition 与 WorkflowRun 分离。Run 固定引用 Definition Version，定义更新不影响正在运行的实例。

P0 节点类型：

- `artifact_input`
- `ai_transform`
- `human_edit`
- `review_gate`
- `condition`
- `fan_out`
- `merge`
- `quality_gate`
- `manifest_compiler`
- `workbench_build`
- `publish`

NodeRun 状态：`pending`、`ready`、`running`、`waiting_input`、`waiting_review`、`completed`、`failed`、`cancelled`、`stale`。

每个端口声明输入/输出 Schema。保存 Definition 时校验 DAG、端口兼容、唯一入口、可达性、条件分支、Merge 配对和终止路径。运行时按边的 `fromPort -> toPort` 建立不可变 NodeInputEnvelope，记录 canonical payload、输入 Hash、来源节点和准确制品引用；执行前再次验证目标端口 Schema。Fan-out 为每个分支复制独立输入，ManifestCompiler 只汇总当前节点的入边 lineage，不能从全局上下文偷读旁路数据。运行时同时提供幂等、数据库租约、心跳、重试、超时、取消、失败恢复和单调事件游标。

因此“自由组合”有三层稳定边界：

1. 节点类型与端口 Schema 决定什么可以连接。
2. Revision、Manifest、Proposal 和 BuildArtifact 决定上下游实际消费的准确版本。
3. Review、Quality、Publish 与 RBAC 决定哪些结果可以成为正式输出。

工作流编辑器只是版本化 Definition 的可视化编辑面；执行语义和最终校验始终在服务端，原始 JSON 模式也不能绕过同一组校验。

`quality_gate` 和 `publish` 是特权自动节点。它们在执行前进入 `waiting_input`，直到认证用户通过 `POST /v1/projects/:projectId/workflow-runs/:runId/execute` 授权；质量节点要求 `edit` 权限（默认最低 `editor`），发布节点要求 `publish` 权限及定义中的 `requiredRole`。服务端把实际用户、角色、动作、来源和时间写入 NodeMetadata 与工作流事件，执行器只读取这份可信元数据，绝不从节点输入/输出或 `run.startedBy` 推断身份。直接上游评审通过时，若评审者同时满足后继特权动作，可在同一工作流事务中下放授权；真正执行时底层交付服务仍再次校验当前 RBAC。

对话不能直接修改业务数据。当前控制面只把用户消息转换为两类可审阅意图：

- `start_workflow`：固定已发布 Definition Version、准确来源 Revision/Hash 与 InputManifest；人工接受后形成 Command，执行体为空，服务端以 Command ID 作为 Run ID 幂等启动并回写准确运行身份。
- `workbench_instruction`：在相同冻结输入上增加必须的 `workbenchInstruction.expectedRunId`，可选固定 `expectedBundleId`；人工接受后，前端必须先载入并核对 Run、Definition Version、Manifest ID/Hash、冻结 Bundle 与 Bundle→Run 关联，才允许请求 Workbench 生成 Proposal。服务端在命令完成前再次验证相同身份。

用户消息是不可变追加记录，`commenter` 可发送；assistant 消息只能由服务端随 Proposal 创建。创建会话、生成/提交 Proposal、接受/拒绝以及执行/拒绝 Command 均需要 `edit`。AI 的职责止于 `{proposal,message,provider,model}`，不能自行接受或执行。接受 Proposal 后，顶层 `workbenchInstruction` 被快照为 Command 的 `payload.workbench`，并在 `scope.conversationIntent.workbenchInstruction` 中保留被审阅的运行范围语义。

## 9. Workbench 标准输入输出

无论上游怎样自由组合，ManifestCompiler 都必须输出统一的 ApplicationBuildManifest：

```text
project + workflow run
exact requirement revisions
exact blueprint revision and selected slices
exact PageSpec revisions
exact prototype revisions
API/data/permission contracts
design system and component registry
current workspace revision
acceptance matrix and trace matrix
policies, assumptions and waivers
manifest content hash
```

Workbench 返回 ImplementationProposal：文件操作、路由、组件、API、数据库迁移、测试、预览、诊断、假设、未实现项以及 `requirement/page/layer -> file/symbol/test` 追踪。应用操作时再次校验基础工作区 Revision，并原子生成新的 WorkspaceRevision。

这里有两组相同方向、不同粒度的 AI 契约：

- 制品生成使用不可变 InputManifest 和 OutputProposal。Proposal 中每条 Operation 可单独接受或拒绝，决策和应用均使用版本 ETag；基础 Revision 或 Hash 已变化时拒绝应用。
- 应用生成使用冻结 ApplicationBuildManifest 和 ImplementationProposal。Workbench 只读取清单中钉住的制品与工作区版本，不从当前编辑器状态或任意历史版本补数据。

## 10. 质量、BuildArtifact 与不可变交付

QualityGate 不是只记录一个通过标记。质量服务对准确 WorkspaceRevision 执行固定检查，并在通过时把 `dist/`、`out/`、`build/` 或根静态站点捕获为二进制安全、内容寻址的 BuildArtifact。质量记录同时持久化 Artifact ID、Mongo content ref、content hash、build hash、入口文件、文件数和字节数；失败报告不能关联可发布制品。

依赖解析与构建隔离为两个安全边界：

- Resolver 只接收 lock/manifest 文件。npm 校验 registry 与 SRI 后运行 `npm ci --ignore-scripts`；Go 使用单一固定 HTTPS `GOPROXY`、固定 `GOSUMDB`，并拒绝本地 `replace`。
- Build、typecheck、lint、test 接收冻结源码与只读依赖目录，网络固定为 `none`，并使用只读 root、能力丢弃以及 CPU、内存、PID、超时和输出上限。

Preview、Production 和 Rollback 都加载质量报告绑定的同一个 BuildArtifact，不重新构建，也不把源码交给发布 Provider。发布版本目录不可变；本地 Provider 只对数据库中 `ready` 的准确版本提供静态资源。运行时环境通过 HTML overlay 注入，不反写 BuildArtifact。

## 11. 发布应用公共数据面

工作台内的认证数据管理 API 与已发布应用的公共数据 API 是两个不同安全域。公共域默认拒绝：没有显式表策略时，匿名应用不能读写；策略分别声明 read/create/update/delete 和可读、可写字段白名单，返回内容也按白名单裁剪。

每次部署版本准备一个 256-bit opaque capability，明文只在准备阶段返回一次，PostgreSQL 只保存 SHA-256 digest。Capability 绑定 project、deployment、deployment version、显式 HTTPS Origin 列表（本地开发允许 localhost HTTP）和有效期，并按 `pending -> active -> revoked` 生命周期管理：

1. 发布预留准确 DeploymentVersion，并生成 pending capability。
2. Provider 把 API base、capability 和 deployment ID 作为 `window.__WORKSFLOW_ENV__` 运行时 overlay 注入；源码、BuildArtifact、日志和浏览器持久存储都不得保存 Token。
3. 只有静态版本落盘并持久化为 ready 后才激活 capability。
4. Provider、数据库或 capability 激活失败时撤销 pending token、禁用失败版本并恢复原 ready 版本；回滚为目标 BuildArtifact 创建新的部署版本和新的 capability。

公共请求使用 `Authorization: Bearer <capability>`，同时校验部署绑定、摘要、状态、有效期和动态 Origin。Redis 按 capability、客户端和读写类型限流；Redis 不可用或返回异常时 fail closed。管理策略、查看运行时和撤销部署仍走认证项目 API 与 RBAC。

## 12. 服务边界

### PostgreSQL + GORM

保存用户、会话索引、项目成员、制品元数据、草稿指针、Revision 元数据、依赖、TraceLink、评论、评审、工作流定义/运行、DeliverySlice、Manifest、Proposal 元数据、幂等记录、审计和 Outbox。

### MongoDB

保存大型不可变内容：文档/蓝图/原型快照、场景图、Proposal Operations、Manifest Payload、运行日志块。PostgreSQL 同时保存内容 Hash、集合和 Object ID；写入采用 pending 内容 + PostgreSQL 事务 + outbox + finalize 的可恢复流程。

### Redis

保存 Session 缓存、Presence、临时 GitHub 凭证和公共数据速率限制。正式制品、工作流租约与持久幂等事实仍在 PostgreSQL；Redis 丢失不能改变正式制品内容，依赖 Redis 的公共请求会 fail closed。

### NATS JetStream

承载 `artifact.*`、`review.*`、`workflow.*`、`proposal.*`、`workbench.*`、`notification.*` 等领域事件。PostgreSQL Outbox 负责可靠发布并携带事件 ID；实时连接消费新事件，断线补偿按 JetStream 全局 sequence 有界读取。

## 13. HTTP 与 WebSocket

HTTP 使用 `/v1`，JSON 字段统一 camelCase；错误采用 `application/problem+json`。所有修改请求支持 Request ID；需重试的命令必须带 `Idempotency-Key`；草稿修改必须带 `If-Match`。

主要资源：

```text
/v1/session/*
/v1/projects/*
/v1/projects/:projectId/members
/v1/projects/:projectId/artifacts
/v1/artifacts/:artifactId/draft
/v1/drafts/:draftId
/v1/artifacts/:artifactId/revisions
/v1/revisions/:revisionId/reviews
/v1/revisions/:revisionId/comments
/v1/projects/:projectId/proposals
/v1/projects/:projectId/blueprints
/v1/projects/:projectId/page-specs
/v1/projects/:projectId/prototypes
/v1/projects/:projectId/impact-reports
/v1/projects/:projectId/conversations
/v1/projects/:projectId/conversations/:conversationId/messages
/v1/projects/:projectId/conversations/:conversationId/intent-proposals/*
/v1/projects/:projectId/conversations/:conversationId/commands/*
/v1/projects/:projectId/input-manifests
/v1/input-manifests/:manifestId
/v1/projects/:projectId/output-proposals
/v1/output-proposals/:proposalId/decisions
/v1/output-proposals/:proposalId/apply
/v1/projects/:projectId/workflow-definitions
/v1/projects/:projectId/workflow-runs
/v1/projects/:projectId/workflow-runs/:runId/*
/v1/projects/:projectId/build-manifests
/v1/build-manifests/:manifestId/generate
/v1/projects/:projectId/implementation-proposals
/v1/implementation-proposals/:proposalId/*
/v1/projects/:projectId/quality-runs
/v1/projects/:projectId/deployments
/v1/data/projects/:projectId/public-runtime/*
/v1/public/data/deployments/:deploymentId/*
/v1/projects/:projectId/trace
```

`/v1/public/data/...` 不使用 Builder Session/Cookie/CSRF，而使用部署 capability、动态 Origin 和独立限流；其余项目资源默认位于认证组内。公开静态版本位于 `/published/:deploymentId/:versionId/*asset`，只有 ready 版本可读取。

WebSocket `/v1/ws` 用于 presence、评论/评审通知、制品变更、Proposal/AI 进度、WorkflowRun/NodeRun 状态和 Workbench 日志。客户端先发送认证与订阅消息；服务端返回单调 event cursor。断线后客户端在新的 `subscribe` 消息中携带上次 `cursor` 进行有界恢复；游标超出保留范围或补偿上限时收到 `cursor.reset`。WebSocket 不能作为唯一持久事实源。

## 14. 权限与责任

项目角色：`owner`、`admin`、`editor`、`commenter`、`viewer`。创建者自动成为 Owner。

制品责任：`owner`、`assignee`、`downstreamOwner`、`reviewer`、`watcher`，与项目权限分开。Project Role 决定能否执行操作；Artifact Responsibility 决定谁负责和谁收到通知。

默认禁止作者批准自己的 Revision。所有审批、强制跳过、强制使用过期制品、权限变更和发布操作写入不可篡改审计事件。

## 15. 一致性和工程要求

- PostgreSQL 事务中写业务数据与 Outbox。
- 每个命令都有操作者、项目、Request ID、Idempotency Key 和审计事件。
- Revision、Manifest、Proposal 内容使用 canonical JSON 后计算 SHA-256。
- API 进程无状态；工作流 Worker 可水平扩展。
- NodeRun 使用数据库租约和 compare-and-swap，避免重复执行。
- 所有外部 AI 调用有超时、取消、重试、输入/输出字节与输出 Token 上限，并在送出前过滤敏感信息。
- 生成代码的质量执行在隔离沙箱中运行；发布页面使用严格 CSP，并仅放行由已验证公共环境推导的连接 Origin。
- 健康检查区分 live 与 ready。当前 readiness 同时探测 PostgreSQL、Redis、MongoDB、NATS JetStream、事件 Stream、实时 fanout 订阅、质量沙箱及固定镜像、发布目录可写性；探测不拉取镜像。fanout 异常退出会立即置为 not-ready，并由指数退避 supervisor 重建订阅。
- 开发/测试可显式使用可变 sandbox tag（readiness 名称和启动日志会标注不可复现）；staging/production 必须使用 `image@sha256:<digest>`，否则配置校验失败。
- 启动按环境控制 PostgreSQL 迁移、MongoDB 索引和 NATS Stream provisioning。迁移嵌入二进制、使用 advisory lock 和 checksum，已经应用的 SQL 被修改时启动失败。
- 优雅关闭先停止接收请求，再关闭工作流/内容/outbox Worker，释放 WebSocket、NATS、MongoDB、Redis 和 PostgreSQL。
- 当前可观测性基线为结构化请求/错误日志、Request ID、健康明细和持久运行事件；HTTP 延迟、WS 连接、AI 费用、队列延迟、NodeRun 失败、Outbox backlog 与连接池 Metrics 是生产可观测性的后续验收项，不能以日志替代。

## 16. 内置最小闭环模板

```text
Project Brief HumanEdit
  -> Requirements AiTransform
  -> Requirements HumanEdit
  -> Requirements ReviewGate
  -> Blueprint AiTransform
  -> Blueprint HumanEdit
  -> Blueprint ReviewGate
  -> PageSpec FanOut
      -> Prototype AiTransform
      -> Prototype HumanEdit
      -> Prototype ReviewGate
  -> Merge
  -> ManifestCompiler
  -> WorkbenchBuild
  -> QualityGate
  -> Publish
```

Requirements 批准后，可按 DeliverySlice 并行推进页面原型。Merge 只在配置的门禁满足时放行，不要求所有非关键 Slice 同时完成。

## 17. 闭环验收不变量

实现只有同时满足以下条件才算闭环完成：

1. 团队事实由服务端持久化，换浏览器、重启和切项目不丢失、不串项目。
2. 文档、蓝图、PageSpec、原型、Workspace 均有 Draft、Revision、Hash、Diff、评论、评审与审批。
3. AI 从冻结 Manifest 读取并只返回 Proposal；支持部分接受、过期检测与冲突处理。
4. 批准需求能生成蓝图 Proposal；批准 PageSpec 能生成原型 Proposal；批准原型能生成 Build Manifest。
5. 需求改变可得到完整影响路径，且不静默覆盖下游。
6. 自由工作流可保存、版本化、校验和运行，运行实例可恢复。
7. Workbench 能根据对话发出的受控命令，从 Manifest 产出并应用应用实现。
8. HTTP 与 WebSocket 均可用；服务端权限、审计、幂等和乐观并发有效。
9. Postgres、Mongo、Redis、NATS 的职责清晰，并提供迁移、容器、本地开发和健康检查。
10. 单元、API、权限、并发、恢复、前端集成和端到端闭环测试全部通过。
