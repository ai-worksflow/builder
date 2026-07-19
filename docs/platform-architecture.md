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

每个端口声明输入/输出 Schema。保存 Definition 时校验 DAG、端口兼容、唯一入口、可达性、条件分支、Merge 配对和终止路径。当前 execution profile 的端口校验不是只比较顶层 `type`：服务端会递归证明“任意合法上游输出经 edge mapping 后都能被目标 Schema 接受”，包括嵌套 required/type、enum/const、array items、数值/字符串边界和 `additionalProperties`；无法证明的复合约束一律 fail-closed。旧 profile 仍按冻结的历史校验器回放。运行时按边的 `fromPort -> toPort` 建立不可变 NodeInputEnvelope，记录 canonical payload、输入 Hash、来源节点和准确制品引用；执行前再次验证目标端口 Schema。Fan-out 为每个分支复制独立输入，ManifestCompiler 只汇总当前节点的入边 lineage，不能从全局上下文偷读旁路数据。运行时同时提供幂等、数据库租约、心跳、重试、超时、取消、失败恢复和单调事件游标。

当前 profile 的 `condition` 也只读取确定性上下文：`/inputs` 是当前节点的 immutable NodeInputEnvelope，`/slice` 是当前 fan-out slice 的稳定身份，`/scope` 与 `/run` 只包含冻结的运行身份。`/nodes`、`/values`、`/slices` 等全局可变视图在新 Definition 中被拒绝，避免平行节点提交先后改变分支；迁移前 Run 则由 legacy evaluator 保留原语义。

一整套产品流程因此可以映射为 typed/versioned DAG：`artifact_input` 负责选择准确事实，`ai_transform` 负责 Proposal 生成，`human_edit` 与 `review_gate` 负责人成为事实的门禁，`condition`、`fan_out`、`merge` 负责分支组合，`manifest_compiler` 把任意上游形状归一为应用输入，随后由 `workbench_build`、`quality_gate` 和 `publish` 完成应用生成与交付。每次 Run 固定 Definition Version；模板升级不会改变进行中的 Run。

因此“自由组合”有三层稳定边界：

1. 节点类型与端口 Schema 决定什么可以连接。
2. Revision、Manifest、Proposal 和 BuildArtifact 决定上下游实际消费的准确版本。
3. Review、Quality、Publish 与 RBAC 决定哪些结果可以成为正式输出。

工作流编辑器只是版本化 Definition 的可视化编辑面；执行语义和最终校验始终在服务端，原始 JSON 模式也不能绕过同一组校验。

`quality_gate` 和 `publish` 是特权自动节点。它们在执行前进入 `waiting_input`，直到认证用户通过 `POST /v1/projects/:projectId/workflow-runs/:runId/execute` 授权；质量节点要求 `edit` 权限（默认最低 `editor`），发布节点要求 `publish` 权限及定义中的 `requiredRole`。服务端把实际用户、角色、动作、来源和时间写入 NodeMetadata 与工作流事件，执行器只读取这份可信元数据，绝不从节点输入/输出或 `run.startedBy` 推断身份。直接上游评审通过时，若评审者同时满足后继特权动作，可在同一工作流事务中下放授权；真正执行时底层交付服务仍再次校验当前 RBAC。

对话不能直接修改业务数据。当前控制面只把用户消息转换为两类可审阅意图：

- `start_workflow`：固定已发布 Definition Version、准确来源 Revision/Hash 与 InputManifest；人工接受后形成 Command，执行体为空，服务端以 Command ID 作为 Run ID 幂等启动并回写准确运行身份。
- `workbench_instruction`：固定 `workbenchInstruction.expectedRunId` 与 root `expectedBundleId`；人工接受后形成不可变 Command。浏览器执行时只提交 `{}`，不能提交 leaf、Proposal ID 或生成结果；服务端持有命令租约，重新解析当前准确 leaf、工作区、M0/M1 与编辑权限，然后生成并完成命令。

用户消息是不可变追加记录，`commenter` 可发送；assistant 消息只能由服务端随 Proposal 创建。创建会话、生成/提交 Proposal、接受/拒绝以及执行/拒绝 Command 均需要 `edit`。AI 的职责止于 `{proposal,message,provider,model}`，不能自行接受或执行。接受 Proposal 后，顶层 `workbenchInstruction` 被快照为 Command 的 `payload.workbench`，并在 `scope.conversationIntent.workbenchInstruction` 中保留被审阅的运行范围语义。

`scope.conversationIntent` 是服务端保留字段：公开 Workflow Start 即使由 editor 调用也不能提交它，只有已接受 Conversation Command 的内部构造器能携带不可由 JSON 伪造的 provenance 放行。兼容 Definition 不接受浏览器候选列表，也不会静默截取 UUID 排序的前 N 项；服务端用紧凑索引承载完整候选集合，超过显式数量/字节预算则返回 conflict。Workbench target 同样先做完整可执行性过滤再检查响应上限，因此一个 100-page group 不能吞掉排序靠后的其他 ready run。

对话上下文具有完整的 Summary Checkpoint 闭环。未建立 checkpoint 时，provider 输入必须覆盖 sequence 1 到 trigger 的完整连续历史；实际 canonical payload 超过 200 项或 128 KiB 时，服务端返回带准确 cutoff 的受控冲突。编辑者只能创建不可变的 `pending_review` prefix candidate，另一名具备 `review` 权限的成员必须分页检查准确 source delta，并以 checkpoint ETag 批准；前端还必须核对分页结果最后一项的 message ID 与 checkpoint 的准确 cutoff ID 一致，作者不能自审。批准事务以 CAS 把 conversation head 推进到当前 head 的唯一 child，数据库同时禁止直接 approved insert、回滚、跳链、改写和删除。每个 prefix 用版本化、域隔离的 SHA-256 message chain 绑定 1..N 全量消息，后续 checkpoint 从已批准 hash 继续增长。批准后 provider 只能看到 `{approvedCheckpoint,tailMessages}`：已覆盖原文不再发送，checkpoint 后到 trigger 的消息一条也不能省略。Proposal 固化 checkpoint、tail/context hash 与完整 provider-input hash；tail/context 由 PostgreSQL 原始消息重算，provider-input hash 同时以服务端可信参数独立传入并在落库前比对，Command 再原样复制。任一层篡改都会 fail closed；摘要本身仍是不可信对话内容，不能自证权限、来源身份、批准状态或执行结果。

对话治理 Manifest `M1` 与既有 WorkflowRun 启动时的 Manifest `M0` 独立冻结：M1 可以治理仍在运行的 M0，而无需伪造二者相等。服务端从 active WorkflowRun 的 frozen structural leaf 计算权威 Workbench targets（DefinitionVersion、Run、root、active leaf、manifest group、ordinal、slice ID/key/title），AI 只能在约束 Schema 中选择，不能自造身份。若该会话已有执行过的流程命令，只枚举这些命令关联的 Run；首次接管既有 Run 时，前端可提交当前导航的 `{runId,rootBundleId}` 作为 hint，但服务端必须把它重新解析为唯一、可执行的权威 target。没有关联或 hint 时，两个不同 Run 若映射成相同页面语义则返回 conflict，不允许模型按 UUID 猜测。服务端把解析后的 slice 语义写回待审 Proposal，供人工接受前核对。执行 `workbench_instruction` 时，服务端读取 M1 钉住的实际 Revision 内容，核对 M0 Run、预期 root、当前 leaf/workspace，再以 Command ID 同时作为 generation request key 与确定性 ImplementationProposal ID。Proposal 与 generation claim 完成后才在同一事务中写入命令收据；浏览器不参与任何权威结果选择。

应用生成有独立的持久化 generation claim。它冻结 canonical `{objective,constraints}` 正文及 hash、requested model、生成契约版本、系统提示词 hash、输出 Schema hash、准确治理来源和显式 supersede CAS；同一 request key 的重试必须完全相同。失败或租约过期但尚无产物时，另一名当前具备 `edit` 权限的成员可接管已接受的会话命令；若产物已存在则只恢复同一确定性 Proposal。每个 leaf 最多一个 processing claim 和一个 open/reviewing/ready Proposal，后续会话命令或手工生成绝不能覆盖一个 conversation-owned Proposal。

对话来源的生成请求必须同时携带 `expectedRunId` 与 root `expectedBundleId`。数据库再次把 leaf 的 WorkflowRun、root 和该 Run 的 DefinitionVersion 绑定到已接受 Command 的不可变载荷，并拒绝 rejected/failed Command 创建 claim。Command 收据提交前，确定性 Proposal 只能保持 `open` 且没有 operation decision；数据库禁止它进入 reviewing/ready/applied。只要生成 claim 仍有效或确定性 Proposal 已存在，Command 也不能被拒绝，因此服务崩溃或回执丢失后仍只能恢复同一个产物。

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
typed decision/glossary/reference/change/prototype-flow/fixture context revisions
current workspace revision
acceptance matrix and trace matrix
policies, assumptions and waivers
manifest content hash
```

`manifestGroupKey` 是 ManifestCompiler NodeRun ID。一个 WorkflowRun 可包含多组 ManifestCompiler，每组独立从 ordinal 0 开始；数据库唯一域为 project/run/group/ordinal，并用 run/project 复合外键隔离租户。部分编译成功后的重试只有在 compiler node、slice、prototype、ordinal 与冻结内容完全一致时才复用旧 root。

Root 写入不等于对外激活。只有 ManifestCompiler NodeRun 以准确 lease/CAS 提交为 `completed`，且同一事务验证其 hash-valid BuildManifest 与数据库全部 root 的数量、ID 和 ordinal 完全一致后，该组才可被读取、rebase、对话发现或生成 Proposal。第二个 root 失败、租约丢失或编译节点终态失败时，之前写入的 partial root 始终不可作为 Workbench 输入。

Workflow 创建的 Bundle 还携带 `workflowContext`：准确 Definition ref、完整冻结 InputManifest（包括 base、每个 source 的 ref↔purpose、selection anchors、constraints 和 schema）、DeliverySlice、被审阅的 Run scope，以及 OutputContract。该字段只能由注册的 ManifestCompiler 通过内部构造器注入；公开创建 Bundle 的 HTTP DTO 不接受它。历史/手工 Bundle 不含该可选字段，因此原 v1 内容哈希保持兼容。Workbench AI 同时收到这份上下文与每个 InputManifest source 的实际 Revision 内容，不能只看到一个失去用途关系的引用集合。

Workbench 返回 ImplementationProposal：文件操作、路由、组件、API、数据库迁移、测试、预览、诊断、假设、未实现项以及 `requirement/page/layer -> file/symbol/test` 追踪。应用操作时再次校验基础工作区 Revision，并原子生成新的 WorkspaceRevision。

手工重新生成只能显式提交当前 Proposal 的 ID 与 Version 作为 CAS，并且只允许替换尚无任何决策的普通 open Proposal。Workflow runner 不隐式替换，conversation-owned Proposal 永不可被该接口替换。`implementation-proposal-generation/v1` 是当前生成契约版本；修改系统提示词或输出 Schema 时必须发布新版本。旧失败 claim 不会在新代码下悄悄改变语义，而是因版本/hash 不同而拒绝原 request key 的重放。

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

Release Controller v3 不把一次 HTTP 响应当作发布事实。migration `000056_release_delivery_operation_reconciliation` 为每个 v2 Preview/Production Run 在首次网络请求前原子持久化稳定 Operation ID、canonical request document/hash、exact Controller identity 和完整 Release/expected-head lineage。每次 `submit`/`reconcile`/`resubmit` 是 append-only Attempt，Controller observation 与终态 Result 同样不可变；Worker 崩溃或 lease 接管后从数据库恢复准确请求，不用新 serializer 重建可能漂移的负载。

`PUT /v3/delivery-operations/{operationId}` 超时、断线或无法解析时只能记录 `submit_unknown`，Run 进入 `reconcile_wait`；后续 owner 必须先以同一 Operation ID/hash 执行 `GET`，不得直接重发变更。只有 Controller 对尚未承认的 `prepared`/`submit_unknown` Operation 返回 `404` 时，才可以同一 ID/hash 执行 `resubmit`；已经 `accepted`/`running` 后丢失历史、Result/hash 冲突或 Controller 信任失配都进入 `reconcile_blocked`，需要运维对账，不允许自动重发。

后续数据库权威边界由四个迁移继续收紧：

- `000057_release_preview_singleflight` 对 `(project, ReleaseBundle ID, Bundle hash)` 建立 nonterminal 部分唯一索引；`reconcile_blocked` 也持有锁，只有显式终态才释放同一确定性 Preview namespace。创建 ReleaseBundle 时保留调用方 canonical `createdAt`，数据库只投影 transaction identity。
- `000058_release_delivery_operator_reconciliation` 新增管理员可读取的 blocked snapshot 和 immutable、append-only Case。Case 精确冻结 blocked Run version/error、Operation ID/request hash、Controller identity、最后 Attempt/observation、actor、reason、idempotency key 与 PostgreSQL 时钟产生的审计时间，并以同一事务把 Run 从 `reconcile_blocked` 移回 `reconcile_wait`。它只授权同一 Operation 从到期 `GET` 开始恢复对账，不决定远端结果、不修改 production expected head，也不授权 `PUT`；任一 Case 存在后该 Operation 永久 GET-only，Controller 再次返回 `404` 时只能重新阻塞。只有 Run 形成新的 blocked version 后才能追加第二个 Case，旧 version/error 的 CAS 请求必须失败。
- `000059_legacy_deployment_release_controller_gate` 把旧 `/deployments` 限制为 Preview-only，并让旧 DeploymentVersion insert 与 v3 Run admission 锁同一个 project row。legacy parent 与新 version 都必须处于 `deploying`；parent 或任一 version 的 `deploying` authority 会阻塞 v3，任一 active/uncertain/blocked v3 Run 也会阻塞 legacy Preview。upgrade 在 scan 和 trigger DDL 前锁定四个 writer table，upgrade/readiness 都拒绝 parent/version 分裂或双 writer authority，并检查准确 trigger/function/锁边界。
- `000060_release_delivery_nested_authority` 不只信任 Operation 外层 hash；数据库从 canonical JSON 重新计算内嵌 ReleaseBundle `bundleHash`，以及 production 的 PreviewReceipt、PromotionApproval 和可选 source DeploymentRevision `payloadHash`，任何“内文已变但复制旧 hash”的写入都被拒绝。Helper 对 SQL `NULL`、缺字段和错误 JSON shape 总是返回 false；upgrade 锁定 Operation table 后完成 scan+trigger DDL，避免旧 writer 穿过窗口。
- `000061_release_delivery_run_operation_authority` 补上反向权威：已有 FK/trigger 证明 Operation→Run，新的 deferred constraint trigger 则要求每个 v2 Preview/Production Run 在事务提交时恰好具有一个同 project、同 kind、正确 nullable link 的 Operation。upgrade 先锁 Run/Operation 三张 writer table，再扫描已有 orphan/duplicate；readiness 要求 exact `000061`，核对两个 trigger 绑定准确 function 且均启用、initially deferred，并独立扫描 orphan v2 Run。

Controller 连接在发送 Bearer Token 或变更请求前同时要求正常 PKI 验证和配置的 leaf TLS SPKI SHA-256 pin，readiness 还必须精确匹配 Controller ID、version、`worksflow.release-delivery/v3` protocol 与 trust-key digest。终态 Result 通过 exact `(controllerOperationId, controllerResultHash)` 进入 v2 PreviewReceipt、ProductionReceipt 和 DeploymentRevision；v1 历史 Receipt/Revision 只读且不能伪装 v2 authority。迁移时无法证明远端结果的 legacy v1 nonterminal Run 保守进入 `reconcile_blocked`。

Delivery worker 以 PostgreSQL `statement_timestamp()` 计算 claim/renew lease，而不是信任应用节点时钟；每个 worker store 交替给 Production/Preview 首选权，仍用 `SKIP LOCKED` 防止重复领取，使持续 Preview 压力下 Production 在每个副本最多被一个成功 claim 延后。前端对所有 nonterminal/unknown 状态关闭重复发布操作；mutation capability 关闭时 Preview、批准、晋升、回滚和 operator resume 均 fail closed，但 immutable Bundle/Run/Receipt/Result/Case 历史仍通过只读 API 提供审计。worker-enabled 进程无法校验 pinned Controller 时会启动失败，不会自动退化为 read-only；维护期读服务必须以 worker disabled 或独立 API deployment 运行。

上述 migration `000057`–`000061` 的定向内部 release/migration、race、`go vet` 与真实 PostgreSQL canary 已通过；包含 `000066` 角色/helper 边界、raw-chain 兼容与 project-scoped GIN planner canary 的最终完整 migration suite 也已在真实 PostgreSQL 上通过（`go test ./migrations -count=1`，448.286s）。这些证据只证明仓库内数据库、Provider、Worker、HTTP 与 UI 权威边界；当前仍没有已部署并资格化的真实 Controller、Registry/KMS、目标 cluster 或 approved Golden TemplateRelease 证据，因此不得宣称外部发布已通过、已批准或 Stage 4 已退出。

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

保存用户、会话索引、项目成员、制品元数据、草稿指针、Revision 元数据、依赖、TraceLink、评论、评审、工作流定义/运行、DeliverySlice、Manifest、Proposal 元数据、幂等记录、审计和 Outbox。Production LSP v1 已把 exact TemplateRelease/language-server profile identity 和 ticket/connection/binding/method/violation 审计写入 PostgreSQL；LSP document buffer、diagnostic 和 completion 仍不能成为数据库权威事实。Repository literal search 的持久化边界现由五个 migration 组成：`000062_repository_exact_tree_literal_index` 以 project + exact Candidate tree 为键保存 immutable manifest/member/content-hash-deduplicated text blob；`000063_repository_exact_tree_literal_index_build_claims` 在任何 FileBlob resolve 前建立带 owner/attempt/expiry 的 durable single-builder claim；`000064_repository_exact_tree_literal_index_project_quota` 在同一 claim CAS 内原子预留项目 source bytes 并计算 ready tree、live reservation 与 active build；`000065_repository_exact_tree_literal_index_project_gin` 将 project UUID 与正文 trigram 合入 composite GIN，使 tenant fence 在索引访问内生效；`000066_repository_exact_tree_literal_index_gc` 则以 bounded plan/capability/receipt 把这些派生索引安全回收。这些行只加速候选文件选择，不能替代 Repository tree/file authority。默认硬上限为每项目 `16 trees / 256 MiB logical source bytes / 2 active builds`。

`000066` 先按项目对所有 ready publication 排名，再选出同时超过最短年龄与保留排名的 bounded batch。最短年龄至少 7 天（默认 30 天），每项目至少保留 8 个（默认 8 个），batch 为 1–100（默认 25），capability TTL 大于 0 且不超过 15 分钟（默认 10 分钟）。任意状态 Candidate 的 `current_tree_hash` 和任意 live build claim 都会保护目标；执行时先取 exact tree 与 project quota advisory locks，再对 schema/status/count/bytes/tree/index commitment/publication timestamps 做完整 CAS。仍被其他 tree 引用的 shared blob 不删除。每个 capability 最终只产生一个 append-only `deleted`、`protected`、`stale` 或 `expired` receipt；tombstone 绑定原 publication 与 capability，使同一 tree 后续重建仍是不同事实。

删除 guard 不接受“调用者是 owner”这种模糊授权。两个私有短时 auth table 精确绑定 transaction ID、backend PID、project、tree、capability，以及每个 blob 的 hash/size/type；成功事务返回前删除这些行，异常回滚与业务删除一起消失。Candidate 引用写入使用同一 exact-tree shared advisory lock，GC 使用 exclusive lock，因此“计划后新增 Candidate 引用”不能穿过执行窗口。这个设计关闭的是派生索引 retention，不是 Repository 原始 FileBlob、Git pack/object store 或跨 tree symbol graph 的 GC。

live claim 保护在 claim row lock 后以 `clock_timestamp()` 重判：先提交的续租必然可见，排在 GC 后面的续租因目标行被删除而不能谎报成功。`000066.down.sql` 则先对六张 GC control/audit/auth 表取 `ACCESS EXCLUSIVE` fence，再重读零事实 guard；任一 run、capability、receipt、tombstone 或 auth row 都拒绝回滚，避免并发 execute 或非 deletion 审计被销毁。

`000066` 的生产 PostgreSQL 基础边界使用三个稳定、互不继承的 `NOLOGIN` group role：`worksflow_migration_owner`、`worksflow_application`、`worksflow_repository_index_gc_operator`；migrator、API、GC operator 则各自使用一个只属于对应 group、且没有 `ADMIN OPTION` 的真实低权限 `LOGIN` 与不同 DSN。`000066` 的首个 no-mutation preflight 会拒绝 partial/elevated role trio、stable role 的出向 membership、任意入向 `ADMIN OPTION` 和 trusted schema 中任意显式 column ACL；全缺失角色只作为隔离本地开发姿态。通过后才撤销整个 trusted schema 的 `PUBLIC` table/sequence/routine 权限，将 schema、全部 tables/sequences 与 23 个受控 routines 归属精确 migration-owner，并显式把 predecessor `SECURITY INVOKER` 执行面还给 application。application 的可执行 `SECURITY DEFINER` 集合精确为十个 Candidate/build-claim 函数，operator 只能执行四个 GC plan/execute/inspect/readiness 函数；API 无 GC-private table 权限，operator 无任何对象直权。上述十四个外露 definer 以固定 `pg_catalog, <trusted schema>, pg_temp` 运行；额外的 exact-signature Sandbox checkpoint dependency 必须是 SQL/STABLE `SECURITY INVOKER`、返回单个 boolean、固定同一路径，且只向 migration-owner 与 application 授予不可转授的执行权。八个 internal trigger/guard routine 则为 owner-only；readiness 会拒绝任一历史或漂移 grantee。`000068` 再增加第四个隔离的 `worksflow_golden_fault_operator` group 和独立 LOGIN/DSN；该 role 必须在迁移前存在，只获得 trusted schema `USAGE` 与两张 append-only fault-ledger 表的非转授 `SELECT, INSERT`，API/application 对这两表保持零权限。

`000071` 再增加第五个稳定 `NOLOGIN` group
`worksflow_qualification_promotion_operator`。它必须在迁移前存在，并由独立低权限
LOGIN/DSN 使用；它只能取得 trusted schema `USAGE`、两张 append-only Promotion
ledger/handoff 表的非转授 `SELECT`，以及 exact-signature consume routine 的非转授
`EXECUTE`。消费事务只原子产生 pending handoff，不能冒充已经创建 immutable revision
或已经提交 workflow node。`000072` 的 CredentialSet durable Store 不增加第六个角色或
运行 DSN：四张 credential event/operation/head/projection-authorization 表与四个 exact
routine 当前全部 owner-only，application、Promotion operator 和其他非 owner identity
均为零权限。迁移到 `000072` 后，精确 owner posture 是 22 张 protected table（其中 21 张
为 owned boundary table）、52 个 owned index、37 个 owned routine 和 25 个
`SECURITY DEFINER` routine；CredentialSet 仍须后续 provision 独立 operator、真实 atomic
Secret Broker、KMS/HSM signer 与一次性交付/ACK 控制面，才能成为生产运行路径。
`000073` 的 Qualification Evidence durable Store 同样保持五组稳定 `NOLOGIN` role 与四条
posture 连接，不新增角色或 DSN；四张 event/operation/head/projection-authorization 表、四个
exact routine 与三个 user trigger 全部是 migration-owner-only，API/application 与所有
operator 均为零权限。真实 PostgreSQL catalog 验证后的当前精确 posture 是 26 张 protected
table（其中 25 张为 owned boundary table）、59 个 owned index、41 个 owned routine、15 个
owner-only internal routine 和 26 个 `SECURITY DEFINER` routine；唯一新增 definer 是固定
`pg_catalog, <trusted schema>, pg_temp` 的 append CAS。该持久化边界只提供内部 durable
authority，不等于已经 provision 生产 InputAuthority、Plan/Evidence operator、
Secret Broker/KMS/target adapter 或一次性交付控制面。

`000074` 把 Qualification Plan freeze authority 收入同一生产姿态，但仍保持五组稳定
`NOLOGIN` role 与四条 posture 连接，不新增角色、DSN 或 application/operator grant。它精确
增加两张 owner-only table、八个 valid/ready owned index、五个 owner-only exact-signature
routine 与三个 enabled user trigger；迁移后的真实 catalog 基线为 28 张 protected table
（27 张 owned boundary table）、67 个 owned index、46 个 owned routine、20 个 owner-only
internal routine 和 27 个 `SECURITY DEFINER` routine。五个函数分别固定其 composite result、
SQL/PLpgSQL、volatility、strictness、parallel safety、`SECURITY INVOKER/DEFINER` 与
`pg_catalog` / `pg_catalog, <schema>` / `pg_catalog, <schema>, pg_temp` search path。Evidence
reservation guard 同时命中 Qualification Evidence 的模糊命名集合，因此该集合的真实计数为
五个 function、四个 trigger；`000073` 自身的 exact contract 仍为四个 function、三个
trigger。production startup 会对 owner、ACL、八个 index、trigger enablement、search path、
security mode 与额外命名 routine 任一漂移 fail closed。

API 不执行 DDL：独立 migrator 先写入 schema head；ledger 保留原 up checksum 并为 canonical down pair 单独记录 SHA-256，只有 exact ordered legacy prefix 可由 migrator一次性补齐缺失 down digest。API 再以 application DSN 只读验证完整 up/down pair，缺列、`NULL`、orphan、unknown 或任一 drift 均 fail closed。staging/production role posture 遍历 session login 的全部 inherited/`SET ROLE` reachable roles，拒绝 role delegation、显式 column ACL，以及隐藏角色带来的 DDL、owner、relation、sequence 或 routine authority。GC 是独立 `repository-index-gc` 一次性进程和 opt-in Compose `maintenance` profile，必须显式给出 canonical schema 与稳定非零 run ID；崩溃或响应不确定时只能用相同 run ID 和相同 policy 重放，不能创建替代 authority。API、migrator、operator 的 DSN/schema/secret 生命周期互相隔离，不接受 DSN query 中的 `role`、`options`/`search_path` 或身份覆盖。

原有三个 group/LOGIN 必须在 `000066` 前外部预置，第四个 Golden fault group/LOGIN
必须在 `000068` 前预置，第五个 Qualification Promotion group/LOGIN 必须在 `000071`
前预置；专用 schema/database ownership、完整应用 DML 和 secret injection 同样是生产
部署事实。这三次 migration 的 grants 都是 conditional；若先记录 migration 再创建对应
角色，grants 不会自行重跑，只能增加经审阅的新 migration，不能修改旧 checksum 或手工
放宽表权限。独立 production-posture 检查器在一个有界时间窗口内同时持有 API、migrator、
只读 auditor 与 Promotion operator 四条不同连接；每条 catalog 查询有各自的 PostgreSQL
snapshot，因此它不是跨身份原子快照。GC 与 Golden fault 的实际 operator credential 仍由
各自 focused 检查和外部运行证据覆盖。Compose 本地共享 development owner/`public`
schema 只用于便利，不构成生产角色资格；共享部署必须显式设置
`APP_ENV=staging|production` 才会启用 posture。focused 真实 PostgreSQL canary 覆盖
migration、API posture 和低权限 operator LOGIN 的同 run 崩溃恢复；Golden fault 底座已
接入 Fixture artifact index 和 immutable Qualification verifier，但仍没有真实 fault
adapter 或外部 authority/consume/attestation 产物。这些都只是 `implemented-internal`
证据，不证明任一生产环境已完成上述外部预置或 22 个场景可执行。

### MongoDB

保存大型不可变内容：文档/蓝图/原型快照、场景图、Proposal Operations、Manifest Payload、运行日志块。PostgreSQL 同时保存内容 Hash、集合和 Object ID；写入采用 pending 内容 + PostgreSQL 事务 + outbox + finalize 的可恢复流程。

### Redis

保存 Session 缓存、Presence、临时 GitHub 凭证和公共数据速率限制。Production LSP v1 的专用一次性 ticket digest、admission/request rate counter 和 editor-mode 单持有者 lease 只放 Redis；正式制品、Candidate head、writer lease、TemplateRelease identity、工作流租约与持久幂等事实仍在 PostgreSQL。Candidate literal search 的 Redis admission 已形成原子双层 token bucket 并接入运行时；app 只构造一个 authority，并把同一实例注入 Candidate search 与 secure exact-tree `BuildForActor`。query 默认同时受 project `20/s, burst 40` 和 actor `4/s, burst 8` 限制；first-builder 默认同时受 project `1/15s, burst 2` 和 actor `1/30s, burst 1` 限制。query admission 覆盖 index query、short/no-trigram 和 glob bounded scan；first-builder token 只能在 PostgreSQL durable claim 已确认调用者是首次实际 owner 后、FileBlob resolve 前扣取，ready reuse、waiter 和并发 follower 不扣 build token。Redis 丢失、超时、状态异常或 malformed admission result 必须 fail closed，不能降级为无 admission 搜索或无围栏连接。

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
/v1/projects/:projectId/conversations/:conversationId/summary-checkpoints
/v1/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId
/v1/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId/source-messages
/v1/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId/decision
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
/v1/projects/:projectId/release-capabilities
/v1/projects/:projectId/release-bundles
/v1/projects/:projectId/release-bundles/by-receipt
/v1/projects/:projectId/release-bundles/:bundleId
/v1/projects/:projectId/release-preview-runs
/v1/projects/:projectId/release-preview-runs/:runId
/v1/projects/:projectId/release-preview-receipts/:receiptId
/v1/projects/:projectId/release-promotion-approvals
/v1/projects/:projectId/release-promotion-approvals/by-preview
/v1/projects/:projectId/release-deployment-runs
/v1/projects/:projectId/release-deployment-runs/:runId
/v1/projects/:projectId/release-deployment-runs/promote
/v1/projects/:projectId/release-deployment-runs/rollback
/v1/projects/:projectId/release-production-receipts/:receiptId
/v1/projects/:projectId/release-deployment-revisions/:revisionId
/v1/projects/:projectId/release-delivery-reconciliation-blocks/:runKind/:runId
/v1/projects/:projectId/release-delivery-reconciliation-cases/*
/v1/data/projects/:projectId/public-runtime/*
/v1/public/data/deployments/:deploymentId/*
/v1/projects/:projectId/trace
```

`/v1/public/data/...` 不使用 Builder Session/Cookie/CSRF，而使用部署 capability、动态 Origin 和独立限流；其余项目资源默认位于认证组内。公开静态版本位于 `/published/:deploymentId/:versionId/*asset`，只有 ready 版本可读取。

WebSocket `/v1/ws` 用于 presence、评论/评审通知、制品变更、Proposal/AI 进度、WorkflowRun/NodeRun 状态和 Workbench 日志。客户端先发送认证与订阅消息；服务端返回单调 event cursor。断线后客户端在新的 `subscribe` 消息中携带上次 `cursor` 进行有界恢复；游标超出保留范围或补偿上限时收到 `cursor.reset`。WebSocket 不能作为唯一持久事实源。

新增持久化边界由 Migration `000013_design_imports`、`000014_document_collaboration`、`000015_workbench_generation_fencing`、`000016_workflow_execution_profiles`、`000017_application_build_manifest_slice_identity` 和 `000018_conversation_summary_checkpoints` 固化。`000018` 只把迁移前历史 Proposal/Command 标记为 `legacy_unrecorded`；新 writer 必须显式写入 `submitted`、`full_prefix` 或 `checkpoint_tail`，不能省略来源，也不能把新 AI 行伪装成 legacy。部署前必须排空旧 writer，数据库随后强制 pending-only checkpoint insert、独立审核、单步 head 前进，以及 Proposal 到 Command 的准确上下文/provider-hash 复制。

当前 ordinary Sandbox file read 不是弱一致 GET。请求必须携带 `X-Sandbox-Session-Epoch`、`X-Expected-Candidate-ID`、`X-Candidate-Version`、`X-Candidate-Journal-Sequence`、`X-Writer-Lease-Epoch`、`X-Candidate-Tree-Hash` 和 `X-Expected-File-Hash`；服务端在读取 blob 前和返回 bytes 前重验同一 head，响应回显 Candidate ID/version/journal、writer lease、tree/content hash。Agent patch/evidence 还分别回显 `X-File-Exists`、`X-Byte-Size`、`X-Patch-Content-Hash` 和 `X-Content-Object-Hash`；浏览器必须对实际响应 bytes 重算 `X-Content-Hash`，再核对 storage object/patch 身份，不能只信 JSON 或 header。并发漂移返回 `409 sandbox_file_head_changed`，客户端不得打开旧 bytes。默认 CORS 已 allow/expose 这些请求/响应 headers；配置校验会拒绝漏掉任一安全围栏的 `CORS_ALLOWED_HEADERS` / `CORS_EXPOSED_HEADERS` 覆盖，不能降级为裸响应读取。

Candidate literal search 已接入 durable exact-tree index。无 include glob 且 query 适合 trigram lookup 时，服务先查询 project + opening Candidate tree 的 ready manifest；该 exact tree 首次缺失时，`000063` 的 durable claim 保证只有一个 owner 能在 claim heartbeat/fence 下解析 FileBlob，`000064` 在任何 source resolve 前以 project lock 原子执行 `16 trees / 256 MiB / 2 active builds` 默认配额，随后才从 Repository authority 逐文件重验 hash/size/raw bytes，并原子发布 building→ready 的 immutable manifest/member/text blob。`000065` 的 project-scoped composite GIN 同时约束 project UUID 与 case-sensitive/ASCII-folded trigram posting list，不能先跨租户扫描全局正文再依赖 member join 过滤。

Redis query/first-builder admission 已由主线 app/search/runtime 接线。每个 search 请求严格按 normalize → project view authorization → project + actor query admission 执行，admission 位于任何 Candidate Repository I/O 前；因此 index、short/no-trigram 和 include-glob 的 `2,000 files / 8 MiB / 500 matches` bounded scanner 都受同一 authority 约束。secure `BuildForActor` 只有在 PostgreSQL claim 返回首次实际 owner 时才扣 first-builder token，并且必须在 FileBlob resolve 前完成；ready reuse、claim waiter 和并发跟随者不扣 build token。query admission、first-builder admission、project quota、malformed admission result、Redis outage/timeout/corrupt state、非 ready 冲突、commitment mismatch、tenant drift、tamper 或数据库异常任一失败都必须 fail closed，不能转入 bounded scanner；bounded scanner 只由 short/no-trigram/glob 这一请求形态选择。

两条合法查询路径最终都只对 opening tree 的候选成员重新读取权威 file bytes、重验 content hash，并在返回前再次重验 Candidate head。合法 query/first-builder denial 返回带 `Retry-After` 的 `429`；active-build quota 返回 `429`，retained tree/source-byte quota 返回 `409`，未知 index failure 返回 `503`。前端只接受 1–3600 秒的 bounded `Retry-After`，按 exact query/head identity 最多自动重试一次，并在 query/head/组件变化时取消；只有 `repository_search_head_changed` 会刷新 Candidate，quota `409` 与 infrastructure `503` 均保持 blocked，不重载 Blueprint 或 dirty editor。`000062`–`000066`、Redis admission 和独立低权限 GC operator 均属于 `implemented-internal`：真实 PostgreSQL + Redis 的 focused Repository admission/index 回归已通过（95.205s），GC migration/API posture/operator 也有 focused 真实 PostgreSQL canary，focused `go vet`/unit/race 及完整前端 typecheck/unit/lint/production build 也有对应内部证据。生产 group roles/login/schema/full DML/secret 注入尚须部署方预置；这些内部测试不能替代 approved Golden/LSP-4 资格，也不能证明外部生产数据库姿态已经批准。该能力不是 Git pack/object-store 迁移，也不是 global symbol/reference index。

### 13.1 Production LSP 控制面 v1（LSP-0–3 内部实现基线；LSP-4 未资格化）

生产 LSP 使用独立 `POST /v1/sandbox-sessions/{sessionId}/lsp-tickets` 与 `WSS /v1/sandbox-lsp?ticket=...`，固定协商 `worksflow.sandbox-lsp.v1`；它不复用通用 `/v1/ws`、`/v1/sandbox-stream` ticket 或持久 cursor。ticket 最长 30 秒、一次性、绑定 actor/project/Session/Origin/exact head/恰好一个 profile/mode，Redis 原子 consume，secret query 在代理与 telemetry 中必须脱敏。v1 多语言项目按 profile 建立独立 ticket/WSS，不声称单连接 multiplex。TLS、Origin、RBAC、ready Session、subprotocol、ticket 和 authority head 任一不满足即在 Upgrade 前 fail closed。

跨服务统一只认两个严格围栏：

- `SandboxHeadFence = {projectId, sessionId, sessionEpoch, candidateId, version, journalSequence, writerLeaseEpoch, treeHash}`。Repository Service 是 Candidate version/journal/tree 权威，Sandbox Service 是 Session epoch/state/writer lease 权威；Gateway 不在 Redis 或内存制造第二个 head。
- `DocumentFence = {modelUri, openId, modelVersion, savedContentHash}`。`modelUri` 是稳定的 `worksflow-candidate:` canonical URI；Monaco 是 open/model version 的本地投影，`savedContentHash` 只能来自最近一次成功 Candidate CAS。

Document 首次绑定必须通过上述 ordinary file read contract 取得 bytes 与 exact response fence；不得从 LSP process filesystem、过期 cache 或缺少 Candidate ID/journal 的响应旁路构造 `DocumentFence`。

ticket、bind、open/change/save/close、request/response/diagnostics 和 head rebind 必须携带适用的完整 fence。所有 schema 递归 `additionalProperties=false`，字段缺失、null、alias、widened integer、unknown enum/method、非 canonical URI 或不一致 identity 均拒绝；集合使用 `[]`、map 使用 `{}`，不能靠默认值扩大权限。Gateway 对浏览器只输出 strict `sandbox-lsp-ticket/v1`、`sandbox-lsp-connection/v1`、`sandbox-lsp-binding/v1` 和按 method 解析的 `sandbox-lsp-envelope/v1`，不得透传任意 LSP JSON-RPC/server request。

平台责任边界：

| 组件 | 必须负责 | 明确禁止 |
|---|---|---|
| Template Registry | approved exact TemplateRelease 中冻结 profile、image/executable digest、serverInfo、初始化/config hash、capability allowlist/hash、资源和 network policy | 从 workspace/PATH/tag 自动发现 server，运行 package hook/plugin |
| Repository/Sandbox | 构造并重验统一 head；所有保存继续使用 writer lease、expected file/hash 和 Candidate CAS | 接受 LSP 声明的 head 或写入结果 |
| LSP Gateway | ticket consume、identity/capability negotiation、strict adapter、浏览器 Candidate URI 与容器内固定 `file:///workspace` URI 的双向 canonical 映射、result sanitize、stale-drop、audit/rate/resource enforcement | raw JSON-RPC passthrough、暴露宿主路径、保存源码、代替 Repository 写文件 |
| Language-server runtime | 非 root、只读 Candidate mount、无网络、bounded tmp/cache/CPU/memory/PID/time | 修改 workspace、访问平台/Secret、启动任意子命令 |
| Monaco client | 稳定 URI/open ID/model version、二次 fence 比对、marker/hover/navigation/safe completion 投影 | 旧结果 best-effort apply、reconnect 时 dispose/`setValue`/清空 undo |

v1 是 read-only language intelligence。平台 baseline 可 allowlist diagnostics、hover、signature、document symbol/highlight、definition/reference、semantic token、inlay hint 和受限 completion；completion 只允许当前 DocumentFence 的 plain insert/single text edit。`workspace/applyEdit`、任何 `executeCommand`、rename/prepareRename、format、code action、workspace file operation、dynamic registration、cross-file edit 永久禁止，TemplateRelease 不能放宽。Gateway 与只读 mount 双重拒绝；接受建议后的持久变化仍必须走 Monaco local edit → Candidate CAS → authoritative new head → verified `headRebind`。

同 Session/Candidate/lease 的 CAS 单调后继可在 Gateway 重新读取 authority 后 rebind；project/session/sessionEpoch/candidate/writerLease 变化或未知/倒退 head 必须发送 `lsp_head_stale`、关闭 4409 并签发新 ticket。LSP 流不 replay：重连先 GET Session/tree，复用存活的 Monaco model URI/open ID/model version/undo，再全量 `didOpen`；dirty 或 remote-changed document 进入显式 save/rebase/conflict，不能重新加载 Blueprint、PageSpec、Prototype 或覆盖 editor。

PostgreSQL audit 只记录 ticket/binding identity、fence、method、count/latency/outcome、stale/rate/resource/violation 和 close reason；不记录 secret、源码、unsaved text 或 diagnostic/completion 正文。限流按 tenant/project/actor/session/profile/method 分层，profile cap 与平台 hard cap 取较小值；LSP failure/resource exhaustion 不能阻塞普通编辑、Candidate autosave、PTY、通用事件 WSS 或 bounded literal search。

规范错误族为 `lsp_forbidden`、`lsp_origin_forbidden`、`lsp_ticket_required`、`lsp_ticket_rejected`、`lsp_subprotocol_required`、`lsp_session_not_ready`、`lsp_head_stale`、`lsp_document_stale`、`lsp_profile_not_declared`、`lsp_document_unsupported`、`lsp_server_identity_mismatch`、`lsp_capability_violation`、`lsp_message_malformed`、`lsp_read_only_violation`、`lsp_rate_limited`、`lsp_resource_exhausted`、`lsp_ticket_store_unavailable` 和 `lsp_runtime_unavailable`；HTTP/problem 与 WSS close/action 的准确映射以 `ai-constructor-architecture.md` 12.6 为准。

上述 LSP-0–3 已形成仓库内实现：专用 ticket HTTP/WSS 与 exact subprotocol、Redis 一次性 grant/rate/editor lease、PostgreSQL authority/audit、immutable exact-tree runtime snapshot、digest-pinned read-only container runtime、strict Gateway/method adapter，以及从 exact approved TemplateRelease 自动发现其已声明 profile（缺失即 fail closed）的 Monaco binding、稳定 model/undo、heartbeat/reconnect 和只读 provider 投影。相应 Go 单元/集成、真实 PostgreSQL + Redis 定向验证和前端 typecheck/unit/lint/build 自动化已经执行通过；这些结果只证明 `implemented-internal`。

LSP-4 仍未完成：仓库没有可供该资格运行使用的 approved Golden TemplateRelease language-server profile、目标 ingress/WSS credential、digest-pinned real server 的外部故障注入与浏览器 qualification receipt。协议 fixture、fake server、内部容器、普通 Playwright 或明确 skip 都不能把它写成 `production-qualified`；完整边界以 `full-stack-quality-profile.md` 14.1 的 `LSP-QA-016` 为准。

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
- PostgreSQL DDL 只由独立 migrator 执行；API 启动只核对 exact migration version/checksum head 和生产 role posture。MongoDB 索引与 NATS Stream provisioning 仍按环境控制。已经应用的 PostgreSQL SQL 被修改、缺失或出现未知版本时 fail closed。
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

Blueprint 批准后，服务端从该准确、当前且已批准的 Blueprint Revision 读取语义 Page 节点，为每页生成带 Page anchor 的分支。每个分支先完成 PageSpec Proposal、人工编辑和审批，再允许 Prototype AI 消费准确 PageSpec；客户端不能在 Blueprint 审批前预拼 DeliverySlice 或 PageSpec。应用流程的 Merge 固定使用不可豁免的 `policy=all`：只有同一 fan-out epoch 的全部冻结分支都完成准确 PageSpec、Prototype 与审批后才会放行。

`minimum-product-loop` 内置模板当前为不可变 v5。新项目保留 v1、v2、v3 历史版本以及准确钉住 `workflow-engine/v1` 的 v4，并发布同时带 workflow-level I/O contract 与准确 `workflow-engine/v2` ref 的 v5；启动期显式 provisioner 为已有项目幂等补齐并发布 v5，同时撤销旧版本的发布状态。旧 WorkflowRun 仍按原 Definition Version、定义哈希与 execution-profile descriptor hash 回放，GET/List 接口不执行安装或升级写操作。

项目还会安装独立的 `blueprint-selection-app` v4 模板，并保留 v1、v2 以及准确钉住 `workflow-engine/v1` 的 v3：冻结 Selection -> 选中 Page fan-out -> 精确 PageSpec/Prototype passthrough -> merge -> ManifestCompiler -> Workbench -> Quality -> Publish。当前 v4 同时冻结 `blueprint_selection -> application/deployment` I/O contract 与 `workflow-engine/v2`。它证明最小闭环和 Selection 闭环都只是可复用 Definition Version；用户可以保存其他合法 DAG，但所有新版本仍受相同端口、语义血缘、能力注册表和门禁约束。

Execution profile 不是一个可变的“引擎版本号”。descriptor hash 覆盖能力注册表、分析上限以及 interpreter/input/result/apply/reconcile/runner/compiler/condition/proposal 等组件 ID；定义、运行、运行摘要和 Workbench ApplicationBuildContext 均保存准确 ref。进程启动时，每个 exact profile 还会一次性封存 runner、manifest compiler、condition evaluator、start/input gate、HumanEdit/Workbench/Review validator、proposal dispatcher 与 human-context allowlist；之后替换 Engine 字段也不能重新解释已钉住 Run。迁移前 payload 只映射固定 `legacy-pre-pin/v0`，且不回写 JSON。worker 在领取 lease 前按本进程支持的 profile 过滤；readiness 会报告任何没有本地 bundle 的活动运行，因此 legacy/current worker 可以在滚动阶段共同处理各自已钉住的 Run，而不会静默改用最新解释器。数据库 expand 默认仅兼容旧 HTTP binary 创建 legacy 定义及其 Run；发布 current profile 属于 contract phase，必须等 pre-016 HTTP writers 排空，旧 binary 尝试启动 current 定义会被复合外键拒绝。

当前 `workflow-engine/v2` 只把 reconcile 组件从 `typed-dag-reconcile/v1` 提升为 `typed-dag-reconcile/v2`：当 Condition 未选择的路径通向尚未产生 slice 的 FanOut 时，调度器必须先证明该 FanOut 已没有任何有效前驱，才取消它的配对 Merge，并让已选择路径与该 Merge 汇合后的共享尾继续运行。只要仍有一个有效但尚未完成的前驱，Merge 就保持 pending。legacy/v0 与 `workflow-engine/v1` 的 evaluator、input、result validation/apply 和旧 reconcile 入口全部冻结，历史 Run 不会获得这条新取消语义。

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
11. Release 对同一 exact Bundle、legacy/v3 writer、v2 Run↔Operation 和 blocked Operation 只有一个数据库权威；mutation 维护模式不丢失 immutable history/audit，任何外部部署结论必须另有真实 Controller、Registry/KMS、cluster 与 approved Golden 证据。
12. Production LSP 的统一 head/document fences、一次性专用 ticket、strict read-only Gateway、Candidate-CAS-only save 和 Monaco reconnect/undo 已有 LSP-0–3 内部实现及自动化证据；只有 approved Golden real language-server 的 `LSP-QA-016` 资格矩阵也通过后才算生产闭环。当前只能声明 `implemented-internal`，不能声明 `production-qualified`。
13. Exact-tree retention 与资格消费只有在五组稳定 `NOLOGIN` role 按各自 migration 前置时点存在、production-posture 的 API/migrator/auditor/Promotion 四条独立连接以及 GC/Golden-fault 专用 operator credential、dedicated schema/full DML/secret injection 均已外部预置、精确 posture 通过，并且每个未知 operator 结果都以同一 run ID 对账后才可视为生产可运行；CredentialSet、Qualification Evidence 与 Plan Authority 的 owner-only Store 均不满足各自独立 operator、production InputAuthority、Secret Broker/KMS、target adapter 与一次性交付要求，本地 owner 和 focused canary 也不构成生产资格。
