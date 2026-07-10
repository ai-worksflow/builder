# Worksflow 全栈协同生成平台架构

版本：v1.0  
日期：2026-07-10  
状态：实现基线

## 1. 核心目标

Worksflow 不是把“文档、蓝图、原型、代码”做成四个彼此独立的编辑器，而是提供一套可组合的制品工作流：

```text
对话提出意图
  -> 工作流把意图编译成带类型的节点输入
  -> AI 读取冻结 InputManifest
  -> AI 返回可审阅 OutputProposal
  -> 人工选择、评审和批准
  -> 服务端原子应用并生成不可变 Revision
  -> 下游按准确 Revision + Hash 消费
  -> ManifestCompiler 汇总为 ApplicationBuildManifest
  -> Workbench 产出 ImplementationProposal
  -> 人工接受后写入工作区、测试、预览和发布
```

最小闭环只是系统自带的第一份 WorkflowDefinition。完整闭环打通后，用户可自由组合节点；自由组合不改变制品的数据契约，也不允许 AI 绕开版本、评审和权限门禁。

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

每个端口声明输入/输出 Schema。保存 Definition 时校验 DAG、端口兼容、唯一入口、可达性、条件分支、Merge 配对和终止路径。运行时保证幂等、租约、重试策略、超时、取消、失败恢复和事件游标。

对话不能直接修改业务数据，只能生成并提交以下命令：

- `AnswerOpenQuestion`
- `ApplyProposalOperations`
- `ChangeWorkflowScope`
- `SkipNodeWithWaiver`
- `SubmitRevisionForReview`
- `ApproveRevision`
- `RetryWorkflowNode`
- `FreezeBuildManifest`
- `StartWorkbenchBuild`

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

## 10. 服务边界

### PostgreSQL + GORM

保存用户、会话索引、项目成员、制品元数据、草稿指针、Revision 元数据、依赖、TraceLink、评论、评审、工作流定义/运行、DeliverySlice、Manifest、Proposal 元数据、幂等记录、审计和 Outbox。

### MongoDB

保存大型不可变内容：文档/蓝图/原型快照、场景图、Proposal Operations、Manifest Payload、运行日志块。PostgreSQL 同时保存内容 Hash、集合和 Object ID；写入采用 pending 内容 + PostgreSQL 事务 + outbox + finalize 的可恢复流程。

### Redis

保存短期 Session、Presence、WebSocket 订阅、速率限制、分布式租约、幂等热缓存和失效缓存。Redis 丢失不能破坏正式制品事实。

### NATS JetStream

承载 `artifact.*`、`review.*`、`workflow.*`、`proposal.*`、`workbench.*`、`notification.*` 事件。消费者使用 durable name，并以事件 ID 去重。PostgreSQL Outbox 负责可靠发布。

## 11. HTTP 与 WebSocket

HTTP 使用 `/v1`，JSON 字段统一 camelCase；错误采用 `application/problem+json`。所有修改请求支持 Request ID；需重试的命令必须带 `Idempotency-Key`；草稿修改必须带 `If-Match`。

主要资源：

```text
/v1/auth/*
/v1/projects/*
/v1/projects/:projectId/members
/v1/projects/:projectId/artifacts
/v1/artifacts/:artifactId/drafts
/v1/artifacts/:artifactId/revisions
/v1/revisions/:revisionId/reviews
/v1/revisions/:revisionId/comments
/v1/projects/:projectId/proposals
/v1/projects/:projectId/blueprints
/v1/projects/:projectId/page-specs
/v1/projects/:projectId/prototypes
/v1/projects/:projectId/impact-reports
/v1/projects/:projectId/workflow-definitions
/v1/projects/:projectId/workflow-runs
/v1/workflow-runs/:runId/commands
/v1/projects/:projectId/build-manifests
/v1/build-manifests/:manifestId/builds
/v1/projects/:projectId/trace
```

WebSocket `/v1/ws` 用于 presence、评论/评审通知、制品变更、Proposal/AI 进度、WorkflowRun/NodeRun 状态和 Workbench 日志。客户端先发送认证与订阅消息；服务端返回单调 event cursor。断线后客户端用 `lastEventId` 恢复，不能把 WebSocket 当作唯一持久事实源。

## 12. 权限与责任

项目角色：`owner`、`admin`、`editor`、`commenter`、`viewer`。创建者自动成为 Owner。

制品责任：`owner`、`assignee`、`downstreamOwner`、`reviewer`、`watcher`，与项目权限分开。Project Role 决定能否执行操作；Artifact Responsibility 决定谁负责和谁收到通知。

默认禁止作者批准自己的 Revision。所有审批、强制跳过、强制使用过期制品、权限变更和发布操作写入不可篡改审计事件。

## 13. 一致性和工程要求

- PostgreSQL 事务中写业务数据与 Outbox。
- 每个命令都有操作者、项目、Request ID、Idempotency Key 和审计事件。
- Revision、Manifest、Proposal 内容使用 canonical JSON 后计算 SHA-256。
- API 进程无状态；工作流 Worker 可水平扩展。
- NodeRun 使用数据库租约和 compare-and-swap，避免重复执行。
- 所有外部 AI 调用有超时、取消、重试边界、Token/费用限制和敏感信息过滤。
- 原型预览与生成代码预览在隔离沙箱运行，设置严格 CSP。
- 健康检查区分 live 与 ready；优雅关闭先停止接收请求，再释放订阅和连接。
- Metrics 覆盖 HTTP 延迟/错误、WS 连接、AI 费用、队列延迟、NodeRun 失败、Outbox backlog 和数据库连接池。

## 14. 内置最小闭环模板

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

## 15. 完成定义

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
