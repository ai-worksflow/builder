# Worksflow 全栈协同生成平台架构

版本：v1.2

日期：2026-07-11

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

从 AI 视角看，任何自由组合流程都被收敛为两个边界：输入是服务端冻结的 `InputManifest`、`NodeInputEnvelope` 或 `ApplicationBuildManifest`，输出是带 Schema 的 `OutputProposal`、节点输出或 `ImplementationProposal`。DAG 调度、身份、并发控制、评审、应用和发布都属于平台控制面；模型既不需要理解页面上的固定步骤，也不能把一次生成结果直接声明为项目事实。

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
- RequirementBaseline、Workspace、QualityReport、TestReport 是系统制品；通用 Artifact、Proposal 和 Review 写路径全部拒绝修改，并保留 `REQUIREMENT-BASELINE`、`WORKSPACE-MAIN` 固定键。对其他制品，AI 调用前与 Apply 锁内都要求 Draft 的 base、status、schema、content hash、source lineage 与最新 Revision 完全一致。

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

### 文档协同、下游生成与回写

文档协同建立在正式 Artifact/Revision 之上，不维护浏览器私有副本：

- 成员绑定使用 `owner`、`assignee`、`downstreamOwner`、`reviewer`、`watcher`。整组替换需要独立的 binding-set ETag，所有成员必须属于同一项目，并且至少保留一个 Owner。
- 从文档生成下游文档只接受准确、已批准的文档 Revision。服务端在幂等命令中冻结来源成员绑定 ETag，将 `downstreamOwner` 解析为新文档 Owner；未配置时由发起人承担。新文档 scaffold、准确来源、InputManifest、AI provider/model 和 OutputProposal 都被持久化，重试返回同一结果而不是重复生成。
- 下游变化回写上游时只创建 Proposal。目标必须仍是当前准确的已批准文档 Revision；来源 provenance 只能是准确的 WorkspaceRevision、已应用 ImplementationProposal、已消费且无 child 的 BuildManifest leaf，或 ready Deployment。服务端把解析出的当前 Workspace、实现/Manifest 身份和可用 Preview URL 冻结进 sync-back Manifest。
- Document Graph 是服务端从 Artifact、Revision、成员绑定、Dependency、TraceLink、InputManifest/OutputProposal、WorkflowRun、BuildManifest、ImplementationProposal 和 Deployment 投影出的只读图。它把 AI 的 input/output、实现和交付串在同一视图中，但不成为第二份可编辑事实。

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

### Blueprint Selection

Blueprint Selection 用于从完整蓝图中选定一次工作的精确范围。`POST /v1/projects/:projectId/blueprint-selections/compile` 同时要求当前 Blueprint Artifact ETag 和一个准确、已批准的 Blueprint Revision；服务端验证 1 到 100 个稳定 Node anchor，按确定性顺序冻结节点、内部边、上游语境，以及每个 Page 当前准确的已批准 PageSpec/Prototype 绑定。Selection ID 由这些内容的 canonical hash 得到，并作为不可变 `blueprint.selection` InputManifest 的 `deliverySliceId`，客户端不能提交编译后的 sources、scope 或 selection ID。

当前蓝图编辑器对同一份冻结 Selection 提供三类已实现动作：

1. **Generate docs from selection**：创建文档 scaffold 和精确 Revision，再创建 `selection.documentation` Proposal。派生 Manifest 必须引用父 Selection Manifest，并且冻结 scope 与全部 source refs 必须逐项相等。
2. **Create prototypes from selection**：只为已经绑定准确 PageSpec、但尚无已批准 Prototype 的 Page 创建正式 Prototype Draft，随后仍需正常评审和批准。
3. **Use selection in workflow / Workbench**：只有全部选中 Page 都冻结了准确 PageSpec 和 Prototype 时，才从该 Manifest 启动已发布的 `blueprint-selection-app` WorkflowDefinition。

Selection 工作流使用 `blueprint_selection_page` fan-out，只展开冻结范围内的 Page。运行时重新校验 Selection Manifest 的 root source、Node anchor sources 和 Page bindings；ManifestCompiler 又要求 DeliverySlice 与 Selection 一一相等，拒绝额外页面、缺页、重复页或漂移的 PageSpec/Prototype。Selection 范围改变必须重新编译 Manifest，不能修改旧 Selection。

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

### Design Import Center

Figma、Penpot、Excalidraw、tldraw、Storybook、Ladle 和通用文件都被视为不可信外部输入，而不是项目事实。创建 Design Import 时，服务端先按来源校验导出文件、媒体类型、扩展名、签名、大小和主动内容，再保存不可变、内容寻址的快照；随后把快照及当前准确、已批准的 PageSpec 冻结到 InputManifest，并将规范化 Prototype 转换保存为可评审 OutputProposal。批准后才会应用 Proposal 并创建带 PageSpec、Proposal 与 Manifest 精确血缘的 Prototype Revision；拒绝不修改目标。

`GET /v1/projects/:projectId/design-import-capabilities` 是能力真相源。当前实现支持受 `CONTENT_MAX_BYTES` 动态约束的导出文件上传；远程 connector 明确返回 `remoteEnabled: false`，不会模拟 OAuth、接收 connector 凭证或抓取 URL。更新已有 Prototype 还要求它的准确 required PageSpec source 与本次 PageSpec 完全一致。

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

一整套产品流程因此可以映射为 typed/versioned DAG：`artifact_input` 负责选择准确事实，`ai_transform` 负责 Proposal 生成，`human_edit` 与 `review_gate` 负责人成为事实的门禁，`condition`、`fan_out`、`merge` 负责分支组合，`manifest_compiler` 把任意上游形状归一为应用输入，随后由 `workbench_build`、`quality_gate` 和 `publish` 完成应用生成与交付。每次 Run 固定 Definition Version；模板升级不会改变进行中的 Run。

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

对话治理 Manifest `M1` 与既有 WorkflowRun 启动时的 Manifest `M0` 独立冻结：M1 可以治理仍在运行的 M0，而无需伪造二者相等。服务端从 active WorkflowRun 的 frozen structural leaf 计算权威 Workbench targets（DefinitionVersion、Run、root、active leaf、manifest group、ordinal），AI 只能在约束 Schema 中选择，不能自造身份。执行 `workbench_instruction` 时客户端必须回传 `{runId,bundleId,implementationProposalId}` 精确收据；服务端验证 M1、M0、预期 root、当前 leaf/workspace 及该 leaf 上仍未应用的 open/reviewing/ready Proposal 后才确认命令。

换言之，对话是流程的 input/output 控制面：输入是用户消息与服务端提供的可选、准确目标集合，AI 输出是待审阅 Intent Proposal；人工接受后才形成 Command。对话不承担 DAG 调度，也不直接写 Artifact、Workspace 或 Deployment。

## 9. Workbench 标准输入输出

无论上游怎样自由组合，ManifestCompiler 都必须输出统一的 ApplicationBuildManifest：

```text
project + workflow run
manifest compiler node-run group + ordered roots (persisted root ordinal)
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

`manifestGroupKey` 是 ManifestCompiler NodeRun ID。一个 WorkflowRun 可包含多组 ManifestCompiler，每组独立从 ordinal 0 开始；数据库唯一域为 project/run/group/ordinal，并用 run/project 复合外键隔离租户。部分编译成功后的重试只有在 compiler node、slice、prototype、ordinal 与冻结内容完全一致时才复用旧 root。

Workbench 返回 ImplementationProposal：文件操作、路由、组件、API、数据库迁移、测试、预览、诊断、假设、未实现项以及 `requirement/page/layer -> file/symbol/test` 追踪。应用操作时再次校验基础工作区 Revision，并原子生成新的 WorkspaceRevision。

当同一套冻结需求与设计输入需要继续应用到一个更新后的工作区时，不修改原 ApplicationBuildManifest。客户端调用 `POST /v1/build-manifests/:bundleId/rebase`，只提交一个完整的 `workspaceRevision` 精确引用（artifact、revision、content hash）；服务端重新校验项目归属与内容摘要，并创建新的派生 Manifest。派生 Manifest 保留根 Manifest 和直接父 Manifest 身份，使后续 Proposal、质量与发布可以证明使用的是哪一次 rebase；旧 Manifest 及其已生成结果继续保持可审计。该命令需要认证、编辑权限和 `Idempotency-Key`，响应为 `201 Created`、新 Manifest 的 `Location` 与 `ETag`。

每个 parent 最多一个 child，只有 structural leaf 可继续 rebase。成功创建 child 时 parent 原子变为 `invalidated`，其旧非最终 Proposal 变为 `stale`；相同 workspace 重试复用 child，不同 workspace 不能产生 sibling。Migration 012 用直接父唯一索引、root/workspace 唯一索引、root/parent 同项目复合外键及 workflow run/project 复合外键固化这些约束。

前端刷新、重连或完成 rebase 后，必须请求 `GET /v1/build-manifests/:rootId/lineage-state`（传 root 或任意 derived Bundle ID 均会归一到 root），不能从本地创建时间猜测当前分支。响应返回权威 `activeBundle`、项目唯一 active Workspace 的最新 approved 精确引用 `currentWorkspaceRevision`、可选 `currentProposal`，以及按稳定顺序排列、包含直接父级、工作区、状态和最新非 stale Proposal 身份的 `lineage` 摘要；前端只能用顶层 Workspace 引用判断是否需要 rebase，不能拿 bundle 的旧 base 代替。确定性 `ETag` 覆盖完整响应状态，包括当前 Workspace 精确引用以及整个 lineage 的 Proposal ID、状态与版本；因此任一顺序应用或重新生成都会让旧缓存失效，`If-None-Match` 只有在完整状态不变时才返回 `304`。读取仍由服务端按根 Manifest 所属项目执行 view 鉴权。

前端按 `workbench_build` NodeRun 展示 DAG groups。每组只读取该节点自己的 NodeInputEnvelope 和 NodeMetadata output；多组时不使用全局队列，切组只 hydrate 所选组。组内 Proposal ID 按冻结 `bundleIds` 顺序提交，服务端把每个数组位置映射到持久化 root ordinal，并用最终 Workspace 的 Proposal 祖先链验证顺序；客户端选择不能改变执行次序。

多个 ManifestCompiler/Workbench 组仍共享项目唯一的 active Workspace Artifact，但不共享可变客户端队列。DAG 边决定组间先后；每组的 roots 在组内按 ordinal 串行应用。若后续组的 active bundle 仍钉在旧 Workspace，Workbench 停在 `waiting_input`，客户端必须以服务器 `lineage-state` 返回的当前准确 Workspace 创建 rebase 派生 Manifest 后再生成。多个组汇入同一 QualityGate 时，服务端只在所有 WorkspaceRevision 属于同一个 Artifact、其中一个是当前准确的已批准 Revision、其余都是它的精确祖先时选择最终 Revision；分叉或不相关 Workspace 会 fail closed。

多页面执行严格按根 Manifest 顺序推进：只先生成 `bundle-1@W0`；应用得到 `W1` 后，把第二个根 bundle 派生为 `bundle-2'@W1`，再生成与应用，依次得到 `Wn`。Completion 要求每个原始根 bundle 恰好有一个来自其 root/derived lineage 的已应用 Proposal，并证明最终 Workspace Revision 的祖先链包含这些 Proposal。Apply 要求当前 Workspace 与 Proposal base 完全相等；旧 Proposal 只能标记 stale 并从新的派生 Manifest 重新生成，不能因为旧 base 仍是祖先就把旧 patch 套到新工作区。同一 root lineage 一旦存在 applied/partially-applied Proposal 即视为完成：其 consumed Manifest 不能再次生成 Proposal，也不能再派生第二条可应用分支。

发布请求中的 BuildManifest ID 只是 root lineage selector。服务端从准确 WorkspaceRevision 的 applied Proposal 反查同 project/run/root 的 consumed、未 invalidated 且无 child 的真实 producer leaf，并把该 root/derived ID 写入 DeploymentVersion；other root/run/project、non-consumed 或 non-leaf 均拒绝。

这里有两组相同方向、不同粒度的 AI 契约：

- 制品生成使用不可变 InputManifest 和 OutputProposal。Proposal 中每条 Operation 可单独接受或拒绝，决策和应用均使用版本 ETag；基础 Revision 或 Hash 已变化时拒绝应用。
- 应用生成使用冻结 ApplicationBuildManifest 和 ImplementationProposal。Workbench 只读取清单中钉住的制品与工作区版本，不从当前编辑器状态或任意历史版本补数据。

## 10. 质量、BuildArtifact 与不可变交付

QualityGate 不是只记录一个通过标记。质量服务对准确 WorkspaceRevision 执行固定检查，并在通过时把 `dist/`、`out/`、`build/` 或根静态站点捕获为二进制安全、内容寻址的 BuildArtifact。质量记录同时持久化 Artifact ID、Mongo content ref、content hash、build hash、入口文件、文件数和字节数；失败报告不能关联可发布制品。

依赖解析与构建隔离为两个安全边界：

- Resolver 只接收 lock/manifest 文件。npm 校验 registry 与 SRI 后运行 `npm ci --ignore-scripts`；Go 使用单一固定 HTTPS `GOPROXY`、固定 `GOSUMDB`，并拒绝本地 `replace`。
- Build、typecheck、lint、test 接收冻结源码与只读依赖目录，网络固定为 `none`，并使用只读 root、能力丢弃以及 CPU、内存、PID、超时和输出上限。

Preview、Production 和 Rollback 都加载质量报告绑定的同一个 BuildArtifact，不重新构建，也不把源码交给发布 Provider。发布版本目录不可变；本地 Provider 只对数据库中 `ready` 的准确版本提供静态资源。运行时环境通过 HTML overlay 注入，不反写 BuildArtifact。

工作流 QualityRun 同时钉住 WorkflowRun ID 与最终 WorkspaceRevision。Publish 必须提交同一 Run 的 passing QualityRun、同一准确 WorkspaceRevision 和一个 BuildManifest root selector；发布服务不会信任 selector 是生产者，而是沿 Workspace Revision 的已应用 ImplementationProposal 反查同 root lineage 中唯一 consumed、未 invalidated、无 child 的实际 leaf。DeploymentVersion 保存该实际 producer leaf、QualityRun 和 BuildArtifact 引用，所以最终工作区的实现、质量与发布 provenance 精确相等。

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
/v1/projects/:projectId/blueprint-selections/compile
/v1/projects/:projectId/document-graph
/v1/artifacts/:artifactId/member-bindings
/v1/projects/:projectId/documents/generate-downstream
/v1/projects/:projectId/documents/sync-back
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
/v1/build-manifests/:rootId/lineage-state
/v1/build-manifests/:bundleId/rebase
/v1/build-manifests/:manifestId/generate
/v1/projects/:projectId/implementation-proposals
/v1/implementation-proposals/:proposalId/*
/v1/projects/:projectId/design-import-capabilities
/v1/projects/:projectId/design-imports
/v1/design-imports/:designImportId
/v1/design-imports/:designImportId/decision
/v1/projects/:projectId/quality-runs
/v1/projects/:projectId/deployments
/v1/data/projects/:projectId/public-runtime/*
/v1/public/data/deployments/:deploymentId/*
/v1/projects/:projectId/trace
```

`/v1/public/data/...` 不使用 Builder Session/Cookie/CSRF，而使用部署 capability、动态 Origin 和独立限流；其余项目资源默认位于认证组内。公开静态版本位于 `/published/:deploymentId/:versionId/*asset`，只有 ready 版本可读取。

WebSocket `/v1/ws` 用于 presence、评论/评审通知、制品变更、Proposal/AI 进度、WorkflowRun/NodeRun 状态和 Workbench 日志。客户端先发送认证与订阅消息；服务端返回单调 event cursor。断线后客户端在新的 `subscribe` 消息中携带上次 `cursor` 进行有界恢复；游标超出保留范围或补偿上限时收到 `cursor.reset`。WebSocket 不能作为唯一持久事实源。

新增持久化边界由 Migration `000013_design_imports` 和 `000014_document_collaboration` 固化：前者保存 Design Import 生命周期、不可变快照和跨 PageSpec/Prototype/Manifest/Proposal/Revision 的同项目约束；后者保存 Artifact collaboration ETag、可恢复的下游文档生成命令、解析后的 owner 集合及 AI provider/model，并通过触发器拒绝跨项目成员、制品和生成血缘。

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
Project Brief ArtifactInput (exact checkpoint)
  -> Project Brief AiTransform
  -> Project Brief HumanEdit
  -> Project Brief ReviewGate
  -> Requirements AiTransform
  -> Requirements HumanEdit
  -> Requirements ReviewGate
  -> Blueprint AiTransform
  -> Blueprint HumanEdit
  -> Blueprint ReviewGate
  -> Blueprint Page FanOut
      -> PageSpec AiTransform
      -> PageSpec HumanEdit
      -> PageSpec ReviewGate
      -> Prototype AiTransform
      -> Prototype HumanEdit
      -> Prototype ReviewGate
  -> Merge
  -> ManifestCompiler
  -> WorkbenchBuild
  -> QualityGate
  -> Publish
```

新项目先把当前 Project Brief Draft 固化为准确 checkpoint；它可以尚未批准，但必须同时作为入口 Manifest 的 baseRevision 和 source。对话只产生可审阅的工作流 Intent，人工接受后，首个 AI 节点基于该 checkpoint 与服务端封存的 conversationIntent 生成 Project Brief Proposal。Proposal 应用并形成新 Revision 后，才进入正式文档审批。

Blueprint 批准后，服务端从该准确、当前且已批准的 Blueprint Revision 读取语义 Page 节点，为每页生成带 Page anchor 的分支。每个分支先完成 PageSpec Proposal、人工编辑和审批，再允许 Prototype AI 消费准确 PageSpec；客户端不能在 Blueprint 审批前预拼 DeliverySlice 或 PageSpec。Merge 只在配置的门禁满足时放行，不要求所有非关键 Slice 同时完成。

内置模板当前为不可变 v2。新项目在创建事务中安装 v1 历史版本和已发布 v2；启动期显式 provisioner 为已有项目幂等发布 v2 并撤销有死锁语义的 v1 发布状态。旧 WorkflowRun 仍按原 Definition Version 回放，GET/List 接口不执行安装或升级写操作。

项目还会安装独立的 `blueprint-selection-app` v1 模板：冻结 Selection -> 选中 Page fan-out -> 精确 PageSpec/Prototype passthrough -> merge -> ManifestCompiler -> Workbench -> Quality -> Publish。它证明最小闭环和 Selection 闭环都只是可复用 Definition Version；用户可以保存其他合法 DAG，但所有版本仍受相同端口、血缘和门禁约束。

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
