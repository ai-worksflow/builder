# Workflow Execution Profile v3 真实运行与发布闭环合同

状态：`implemented-internal`，默认关闭且未激活。descriptor/dispatch identity/hash
cascade、opt-in v3 runtime、migrations `000083`/`000084`/`000085`、WIA activation
worker、专用 qualified-release publisher/worker 及其配置/readiness 接线已进入仓库；
当前证据覆盖静态合同、定向单元回归，以及 migration `000085` 的 fresh PostgreSQL 16
`up/down/up`、resolver/ACL、SQL/GORM 原子成功与回滚、Activate/exact replay 定向
canary。本地 Builder 开发库也已完成受审的 legacy-lineage 恢复，并记录 exact
`000083`–`000086` head；仓库迁移链随后加入 `000087`–`000089` Sandbox
absolute-TTL hardening，当前 repository migration head 为 `000089`。本地部署证据
仍只覆盖前述 `000086` head；这些事实都不声明跨
`000080`–`000084` 与真实 Controller 的 PostgreSQL full-chain、生产目标部署或
external qualification，也不构成生产发布批准。

本文定义 `workflow-engine/v3` 从 Quality 节点完成，到 Workflow Input
Authority（WIA）激活、资格运行、Promotion v2、Handoff、真实
`ActionPublish`、Release Controller `queued -> healthy`，最后完成 Publish
节点的唯一可实施路径。它补充而不替代：

- [Immutable Workflow Input Authority](./workflow-input-authority.md)；
- [Qualification Input Precommit Authority v1](./qualification-input-precommit-authority-v1.md)；
- [Qualification Promotion v2](./qualification-promotion-v2.md)；
- [Qualification Promotion v2 Workflow Handoff](./qualification-handoff-v1.md)；
- [Golden Qualification Control Plane v2](./golden-qualification-control-plane.md)；以及
- [Worksflow 真实 AI 构造器与用户沙盒架构](./ai-constructor-architecture.md)。

本文所有 `MUST` 都是 fail-closed 条件。表名、schema、hash domain、状态和
角色一旦随实现发布即为不可变合同；若实现需要改变它们，必须先升级本文和对应
canonical schema，而不能在同一版本下扩大解释。

## 1. 当前证据边界

截至本文形成时，仓库内已经存在以下内部 building blocks：

| 边界 | 当前事实 |
| --- | --- |
| profile descriptor | `workflow-engine/v3` 已冻结为 `qualified-release-controller-dispatch/v1` / `854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104`；capability snapshot 和 definition validator 强制 `Workbench -> blocking release Quality -> external-qualification -> production Publish` 的闭合拓扑 |
| runtime registry | built-in registry 默认仍只注册 legacy、v1、v2，`Current` 也不指向 v3；私有 v3 bundle、`sealRuntimeV3`、`executeNodeV3`、`applyResultV3` 和 `reconcileV3` 已实现。只有显式开启 `WORKFLOW_PROFILE_V3_RUNTIME_ENABLED` 且注入 sealed qualified-release binding 时，平台工厂才 opt-in 注册并 seal v3 |
| generic scheduler fence | PostgreSQL 与 memory claim 都排除 `external_qualification_gate` 和 exact v3 production Publish；Engine、Facade、retry、waive、submit、runner 等 generic 入口拒绝控制专用节点，legacy profile 行为保持独立 |
| migration `000078` | WIA PostgreSQL authority 和 Go canonical/Store 边界已存在；`PostgresStore.Freeze` 只加入调用方持有的事务，并不拥有 activation transaction、Workflow 状态更新、event 或 Outbox 编排 |
| migration `000079` | v3 definition/run/node 状态词汇和 external gate database guard 已存在；它不是 runtime registration |
| migration `000080` | Input Precommit authority、三角色 PostgreSQL adapter 和 canonical closure 已存在；生产编排仍未启用 |
| migration `000081` | Promotion v2 可原子形成 consumption 和一个 append-only `pending` handoff |
| migration `000082` | Handoff 可创建 same-content output Revision，完成 external gate，并把唯一 production Publish 置为 `waiting_input`；它不授权或执行 Publish |
| migration `000083` | Canonical Review forward-equivalence hardening、timestamp/decision closure 及 migration-owner 所需 legacy Release helper ACL provenance 已实现；它是 `000084` 的显式前置边界，不是发布成功证据 |
| migration `000084` | immutable Controller bootstrap、same-content equivalence、authenticated authorization、claim/renew、Controller binding、healthy/failure result、Publish apply、owner/ACL/posture 和 non-empty down fence 已实现为数据库 authority；本地 Builder schema 已安装，但尚未注册唯一 Controller bootstrap，生产目标也未批准激活 |
| migration `000085` | v3 Quality completion 的 exact gate input、ContentStore material bytes 和 activation identities 已由 GORM `Commit` 在同一 SERIALIZABLE transaction 内 admit/precommit，并形成 immutable candidate snapshot、closed resolver 和 WIA freeze wrapper；普通非 v3 commit 不走该预提交。fresh PostgreSQL 16 的 `up/down/up`、resolver/ACL、SQL/GORM 原子成功与回滚、Activate/exact replay 定向 canary 已通过，但没有跨越后续 qualification/Handoff/Controller，因此不是 full-chain |
| activation runtime | `workflowqualificationactivation` 已实现 opaque completion-event resolver、独立 WIA operator pool、same-ID commit-unknown Inspect、bounded JetStream worker/quarantine 和 readiness；配置默认关闭，未在目标环境运行 |
| qualified publisher | `qualificationrelease` 已实现独立 operator Store、candidate source、Controller observer、database-time lease、same-identity unknown-outcome reconciliation、terminal result apply、bounded worker concurrency/config/readiness 和 app lifecycle wiring；它不会复用 generic Workflow publisher，配置默认关闭 |
| Release Controller | v3 Operation/Attempt/Observation/Result、`ReconciledDeliveryStore`、worker、commit-unknown reconcile 和 `queued -> healthy` authority 已存在；qualified publisher 只观察并消费该 durable authority，不证明真实 Controller 已部署或健康 |

因此生产闭环当前仍不能完成。仓库内代码处于 `implemented-internal`，repository
migration head 为 `000089`，其中 profile-v3 权威链为 `000083`–`000085`、
Candidate/Sandbox 后续加固为 `000086`–`000089`；本地 Builder 数据库的已记录部署
证据仍为 exact `000086` head，feature gates 仍默认为 false；
生产目标尚未独立批准该 schema、注册唯一 Controller bootstrap、provision 专用
operator LOGIN/DSN/secret，也没有激活完整 Plan/Evidence/Receipt/Input/Promotion/
Handoff 编排。完整
`000085 -> WIA -> 000080 -> 000081 -> 000082 -> 000084 -> Controller -> Publish completed`
的真实 PostgreSQL no-bypass canary 和外部 Golden 证据仍未形成。

任何实现状态说明都必须区分：

```text
implemented-internal
production-wired
externally-qualified
```

`implemented-internal` 只表示代码、SQL 和默认关闭的接线已存在；它不能自动推出
`production-wired`，前两者也都不能自动推出 `externally-qualified`。

## 2. 唯一闭环与 authority 所有权

唯一允许的成功链如下：

```text
Workbench completed
  -> blocking release Quality authorized by authenticated ActionEdit
  -> Quality completed with one exact passing QualityResult
  -> WorkflowQualificationActivationService freezes WIA and activates gate
  -> run + external gate = waiting_qualification
  -> qualification coordinator resolves Plan/Evidence/Receipt
  -> Input Precommit v1
  -> Promotion v2 consumption + pending handoff
  -> durable Handoff worker completes migration-82 handoff
  -> external gate = completed
  -> production Publish + run = waiting_input
  -> authenticated ActionPublish creates migration-84 authorization/equivalence authority
  -> Publish = ready
  -> profile-v3 QualifiedReleaseControllerPublisher claims Publish
  -> one exact Release Controller production operation: queued -> ... -> healthy
  -> migration-84 immutable healthy result
  -> migration-84 dedicated result apply completes Publish and Workflow run
```

每条箭头只有一个写 authority：

| 转换 | 唯一写 authority |
| --- | --- |
| `Quality completed -> external gate waiting_qualification` | `WorkflowQualificationActivationService` 持有的 application/WIA 原子事务 |
| WIA 到资格证据、Input Precommit、Promotion | qualification coordinator 使用各自独立 authority adapter；浏览器不参与 canonical 输入构造 |
| `pending handoff -> gate completed + Publish waiting_input` | migration-82 Handoff capability 与专用 Handoff worker |
| `Publish waiting_input -> ready` | migration-84 qualified-release authorization capability；必须由真实 authenticated `ActionPublish` 触发 |
| `queued -> healthy` | Release Controller v3 worker 和 immutable Operation/Result authority |
| `Publish running -> completed` | migration-84 dedicated result apply；generic `applyResultV3` 不参与，数据库要求 exact migration-84 healthy result |

Event、Outbox、JetStream message、UI 状态、run context、模型输出、HTTP 成功响应和
日志都不是上述 authority 的替代品。

## 3. Profile descriptor identity 决策

### 3.1 首次激活前已替换 draft hash

Phase A 变更前的 draft descriptor 使用：

```text
version            = workflow-engine/v3
RunnerDispatchID   = builtin-runner-dispatch/v1
hash               = aca0fbcc902ad0b51da4beb7df9c5f4ab58036540aa4046a3f62e848728b37ef
```

`builtin-runner-dispatch/v1` 不能精确表达“external gate 永不由 scheduler
推进、Publish 只能走 migration-84 等价 authority 和 Release Controller”的新行为。
在同一 hash 下安装该 publisher 会静默重解释 persisted profile，禁止这样做。

由于 v3 尚未注册或生产激活，仓库 Phase A 保留 version label
`workflow-engine/v3`，并已将唯一变更后的 dispatch identity 固化为：

```text
RunnerDispatchID   = qualified-release-controller-dispatch/v1
new descriptor hash = 854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104
```

新 hash 是在现有 descriptor 只替换上述 `RunnerDispatchID` 后，通过仓库当前
`domain.CanonicalHash` 算出的值。`CoreInterpreterID`、`InputBuilderID`、
`ResultValidatorID`、`ResultApplyID` 和 `ReconcileID` 已分别使用 v3 identity；实现不得
再改变其中任一组件却继续使用该 hash。

### 3.2 迁移和代码 cascade

首次激活变更必须在一个 reviewed change 中原子更新并验证：

1. `internal/workflow/execution_profile.go` 的 v3 descriptor、hash constant 和
   descriptor-byte tests；legacy/v1/v2 bytes/hash 必须零变化。
2. `internal/workflowinputauthority/validation.go` 的 frozen v3 hash。
3. migration `000078` 的 WIA table constraint、freeze profile comparison、fixtures 和
   WIA canonical tests。
4. migration `000079` up/down 的 definition/run/node guards、existing-row scans、tests 和
   production posture allowlist。
5. migration `000082` up/down 替换后的 v3 run/node guards、Handoff tests 和 posture
   expectations。
6. migrations `000080`/`000081` 虽不直接硬编码该 hash，也必须重跑全部 canonical
   closure、replay 和 no-bypass fixtures，因为它们经 WIA 间接绑定 exact profile。
7. migration `000084` 只能接受新 hash，并必须拒绝旧 hash 的 Publish authorization。
8. migration `000085` 的 existing-row fence、Quality completion precommit、material resolver
   和 candidate snapshot 只能接受新 hash，不能为旧 draft fact 补造 activation input。

当前仓库已完成上述 1–6 的 source cascade：v3 descriptor canonical JSON 固定为
4160 bytes，独立 SHA-256 结果为新 hash；WIA golden input/authority vectors 已随 exact
profile identity 更新；显式 up-to-`000082` 的 `000078`/`000079`/`000082` PostgreSQL
canary、`000080 -> 000081 -> 000082` pipeline 和 production posture 均已通过。posture
额外固定四个持久 enforcement function body 与 WIA check constraint，共五个 exact hash
contracts，并拒绝旧 draft hash。该结果不证明任何已部署环境满足下述 immutable-fact
absence preflight。第 7–8 项的 SQL/Go 实现、静态合同与定向单元回归已经进入仓库；
本轮没有把它们写成真实 PostgreSQL full-chain canary 通过。

如果任何环境已经把 `000078`–`000082` 记为已应用，禁止修改已发布 migration
checksum。该环境必须由 `000084` 先证明没有旧 hash 的 definition、run、WIA、Policy、
Promotion、Handoff 或 output Revision，再安装新 guards。只要存在任何不可变旧-hash
事实，就不能把它重标为新 v3；必须分配 `workflow-engine/v4` 和新的完整迁移链。

### 3.3 注册条件

`NewBuiltinWorkflowExecutionProfileRegistry` 只有在下列 readiness 同时成立时才可注册
v3 bundle：

- migrations `000078`–`000084` exact catalog posture 通过；
- WIA activation、Input Precommit、Promotion、Handoff 和 qualified-release worker
  均配置为 durable 模式；
- 所有独立 LOGIN/DSN、session affinity 和 primary read-write 检查通过；
- Release Controller exact ID/version/protocol/trust digest readiness 通过；
- v3 bundle 的 component identity 和 runtime ownership map 与新 descriptor 完全一致；
- full-chain no-bypass canary 已通过，并有单独的 runtime activation 批准。

`CurrentWorkflowExecutionProfile*` 初期仍指向 v2。注册 v3 不等于把新 authoring 或新
run 默认切到 v3；默认切换是独立 rollout 决策。

## 4. Profile-v3 runtime 合同

### 4.1 `reconcileV3`

`reconcileV3` 可以复用 v2 对普通节点、Condition/FanOut/Merge 的冻结语义，但必须拥有
以下不可共享的规则：

1. 在 blocking release Quality completion 的同一个 workflow mutation/数据库事务内，
   `buildNodeInputEnvelopeV3` 必须从 **post-Quality** run state 构造 external gate 的完整
   typed `NodeInputEnvelope`，验证其中唯一 passing `QualityResult` 及其
   Workspace Revision、BuildManifest、BuildContract 和 edge projection，并把 canonical bytes
   写入 `run.context.nodes[externalGateKey].input`。随后 Quality 才可提交为 `completed`；
   `external-qualification` 必须仍保持 `pending`，不能被通用 predecessor-complete 规则改成
   `ready`、`waiting_input` 或 `waiting_qualification`。typed input 不能原子持久化时，整个
   Quality completion 必须回滚。
2. external gate 从不进入 runnable claim 集，不获得 attempt、lease、runner、human
   input、review、waiver 或 retry。
3. gate typed input 一经随 Quality completion 提交即不可覆盖；reconcile replay 只能接受
   exact canonical bytes/hash。后续 WIA activation worker 必须读取并在 locked rows 上重建、
   逐字节核对该输入，不能成为 `run.context` 的第一个 writer，也不能补写、修复或替换它。
   这把 workflow-first 的 Quality mutation 与 WIA-first 的 activation transaction 分成两个
   已提交阶段，避免 worker 先写 workflow context 后再进入 WIA 所造成的反向锁序。
4. migration-82 已原子完成 gate 后，runtime 只能观察到 gate `completed`、Publish
   `waiting_input`；reconcile 不得再次生成 ready/event 或覆盖 Handoff 的 output。
5. Publish 未有 migration-84 authorization 时只能保持 `waiting_input`。仅设置 run
   context 中的 actor JSON 不足以变为 `ready`。
6. Publish 完成只接受 migration-84 healthy result 所导出的 exact `PublishResult`。

因此 Quality completion transaction 只写 workflow state、gate typed input、Quality event 和
outbox；它不调用 WIA `Freeze`。WIA worker 必须等该事务提交后启动，并把 `Freeze` 作为其
独立 transaction 的第一个数据库 authority entrypoint。两个事务之间唯一可传递的输入是
已提交的 stable node/event identity 与 frozen typed-input bytes，而不是可变内存对象。

### 4.2 dispatch ownership

v3 ownership map必须显式覆盖 descriptor 的每个 NodeType：

- `external_qualification_gate` 属于 v3 interpreter，但没有 execute path；
- `publish` 属于 `qualified-release-controller-dispatch/v1`；
- 其他 runner-owned node 必须绑定其冻结 runner；
- 未声明、重复或缺失 owner 使 registry sealing 失败。

legacy `delivery.WorkflowPublisher` 继续服务历史 profile。它要求 Publish Workspace
Revision 与原 Quality report target ID 完全相同，而 migration-82 output 是相同内容的新
metadata generation；不得削弱 legacy 比较来迁就 v3。

### 4.3 v3 result apply

`applyResultV3` 必须在 generic output/schema validation 之外验证：

- lease 仍由同一 worker/attempt 持有；
- execution actor 是 migration-84 authorization 中的 exact authenticated
  `ActionPublish` actor；
- runner result 等于 migration-84 result document 的 `publishResult`；
- ProductionRun 为 `healthy`，并有 exact Controller terminal Result、ProductionReceipt
  和 DeploymentRevision；
- authorization、controller binding 和 result 都属于同一 Handoff output Revision；
- output canonical JSON 没有未知/缺失字段。

CAS 冲突只允许重放已经缓存和验证的 same result；不得再次创建 Release operation。
数据库 trigger 对 v3 Publish `completed` 做最终 no-bypass 检查。

## 5. Quality 完成后的 WIA 激活

### 5.1 唯一 activation service

新增 `WorkflowQualificationActivationService` 是 Quality completed 后唯一可以启动
external qualification 的 application authority。它消费 durable
`node.completed`/outbox 事实或执行同等持久 command，不接受浏览器传入 authority、
operation、event、target、manifest、receipt 或 hash。

`workflowinputauthority.PostgresStore` 不是这个 service；它只是一个必须加入调用方事务
的 sealed Store。生产 activation service 必须拥有事务并按以下顺序执行：

1. 由 immutable Quality completion event 和 `(workflowRunId, nodeRunId)` 派生或读取
   stable operation/authority/activation-event UUIDv4；同一 generation 永不换 ID。
2. 在事务外读取 exact raw materials和已随 Quality completion 持久化的 external-gate
   typed `NodeInputEnvelope`，编译候选；这些预读不是 authority。缺失 input、gate 非
   `pending` 或 bytes/hash 不一致都永久 fail closed，worker 不写 `run.context` 自愈。
3. checkout application primary session，开始事务后把 WIA `Freeze` 作为第一个数据库
   entrypoint；它先取得 shared migration fence，再自行锁定并重验所有事实。
4. `Freeze` 在 locked database facts 上重建 v3 gate input，并与已存 canonical bytes/hash
   exact 比较；只有相等时，才在同一事务内把 external gate
   `pending -> waiting_qualification`，挂接
   `input_authority_id`，把 run 置为 `waiting_qualification`，追加 exact
   `external_qualification_activated` Workflow event 和 Outbox。
5. 触发 deferred WIA/profile closure，COMMIT 后才允许发布 Outbox。

如果在调用 `Freeze` 前访问 workflow relation，会破坏 migration-78 规定的 rollout/
project/WIA 锁序；实现必须在 API 结构上禁止这种调用顺序。

### 5.2 replay 和未知结果

- 重复 Quality event 先按 `(workflow_run_id,node_run_id)` inspect，exact authority
  直接返回；不同 bytes 是 conflict。
- `40001`/`40P01` 表示 definite abort，可以用同一 ID 重试完整事务。
- `BEGIN`、`COMMIT`、`ROLLBACK` 或连接结果不明时，物理连接必须 discard；使用 fresh
  primary connection 按 operation 或 node inspect。
- commit unknown 未判定前不得分配新 authority/event ID，也不得把 gate 单独推进。
- JetStream ACK 只能发生在 exact authority inspection 证明提交之后。

## 6. WIA 到 Handoff 的 durable worker 链

### 6.1 Qualification coordinator

一个 server-owned qualification coordinator 消费 WIA activation，并以同一 durable
orchestration identity 驱动：

```text
WIA
  -> current Qualification Policy
  -> Qualification Plan
  -> Evidence lifecycle + snapshot-first Receipt v3
  -> Input Precommit v1
  -> Promotion v2
```

它不能用一个“大事务”跨外部执行。每个 authority 使用自己的 immutable operation ID、
started/Inspect 协议和 terminal receipt。每一步 ACK/advance 只能来自上一 authority 的 exact
read，不来自进程内 callback。

### 6.2 Input Precommit

Input Precommit 必须使用三条独立 session-affine primary DSN：issue、source receipt
admission、credential receipt admission。三个角色不能共享 LOGIN、secret、pool 或
executable identity。

Coordinator 只从 WIA、current Policy 和 Plan resolver 选择 upstream IDs。Source verifier
和 Credential resolver 的结果先进入各自本地 append-only admission；Issue transaction 再
锁定并重验两条 admission 和全部 upstream closure。commit unknown 只按 exact operation
inspect，不能重新调用外部 verifier 后制造另一 authority。

### 6.3 Promotion v2

Promotion worker 只接受 server-owned `ConsumeCommand`，使用独立 Promotion DSN 和
pre-BEGIN session advisory lock。它必须消费完整 typed `inputPrecommit`，然后在同一
`SERIALIZABLE` transaction 内创建 consumption、identity reservations、revision intent 和
一个 `pending` handoff。

只有 definite `40001`/`40P01` 可以 same-ID retry；commit unknown 按 operation inspect。
`pending` 不表示 workflow gate 或 Revision 已完成。

### 6.4 Handoff worker

Migration `000082` 的 subject 固定为：

```text
worksflow.qualification.promotion-handoff.pending
```

payload 固定只有：

```json
{"handoffId":"<uuid-v4>"}
```

专用 durable consumer 使用 Handoff DSN 调用
`Complete(handoffId)`。重复投递、worker crash 和 ACK 丢失都必须收敛到同一 completion。
只有 Complete 成功，或 fresh-primary Inspect 证明 exact completion 后才能 ACK。它不能用
application/Promotion DSN fallback，也不能扫描表后自行拼装 target。

Handoff 成功终态固定为：

```text
external gate = completed
Publish        = waiting_input
run            = waiting_input
```

此时仍未批准部署。

## 7. Authenticated `ActionPublish`

### 7.1 transport boundary

浏览器请求仍只包含 project/run/node route；actor ID 来自 authenticated session。Facade
对 v3 production Publish 必须路由到 migration-84 authorization service，不能继续直接调用
generic `Engine.AuthorizeNodeExecution`。

authorization service 必须再次从数据库验证：

- actor 对 project 当前拥有 `core.ActionPublish`；
- actor role 满足 frozen Publish `requiredRole`；
- provenance source 是 `authenticated_command`；
- node/run 正处于 migration-82 的 exact `waiting_input` closure；
- Handoff completion、output Revision authority 和 same-content parent projection exact；
- exact ReleaseBundle、passing PreviewReceipt 和 PromotionApproval 已存在且仍互相闭合；
- ReleaseBundle Workspace 指向 qualified parent Revision，而不是接受 caller 声明 output
  与 parent 等价。

只有上述事务提交后，Publish 才可进入 `ready`。

### 7.2 UI allowed action

UI 不得仅由 `node.status == waiting_input` 推导按钮可用。服务端 read model 至少返回：

```text
allowedAction = authorize_publish
authorizationReadiness = ready | blocked | unknown
blockers[] = stable server codes
```

只有 `ready` 且 authenticated allowed action 存在时开放按钮。点击后的 `queued` 只表示
Release operation 已持久化，不表示 Workflow Publish completed；UI 必须展示 Controller
真实状态，`reconcile_wait`/`reconcile_blocked` 不提供“重新发布”按钮。

## 8. Migration `000084`：等价与发布 authority

编号固定为：

```text
000084_workflow_execution_profile_v3_qualified_release
```

它依赖 `000082` 和已占用编号的 `000083_canonical_review_authority_forward_equivalence`，
不得与 Handoff 或 Canonical Review migration 合并。该 migration 既不签发 external
qualification Receipt，也不改变 parent/output Workspace bytes；它只证明“谁在何时批准
哪一个 Handoff output 使用哪组 parent-bound Release authorities 发起哪一个 Controller
operation，以及最终产生了哪个 immutable healthy result”。

### 8.1 表

初始 schema 使用七张私有表：

```text
qualification_release_v1_controller_bootstraps
qualification_release_v1_identity_reservations
qualification_release_v1_authorizations
qualification_release_v1_controller_bindings
qualification_release_v1_lease_claims
qualification_release_v1_results
qualification_release_v1_transaction_grants
```

`transaction_grants` 只允许作为 transaction-local rendezvous；grant 必须绑定 backend PID、
`pg_current_xact_id()`、authorization 和 production run，并在同一事务被原子消费，commit
时表必须为空。其他六张表是 append-only authority。

`lease_claims` 为每个 Workflow lease attempt 保存 server command 提供的 UUIDv4 event ID、
event sequence、owner、initial expiry 与 canonical bytes/hash。相同 event ID exact replay 不分配
第二个 attempt；未过期时不同 ID/owner 必须冲突；只有 DB time 已确认过期才允许新的 attempt。
completion 必须证明 action 后的 claim `1..n` 连续且每个 claim/event/outbox 都 exact，不能只从
可变的 node attempt 推断历史。

`controller_bootstraps` 是 migration/deployment bootstrap authority，不是运行时配置缓存。v1
全库只允许一个 exact record；它必须在任何 v3 registry、authoring、worker 或 subscription
激活前由 migration credential 注册。相同 bootstrap ID 和 canonical bytes 可以 replay；第二个
identity、同 ID 不同 bytes、update/delete/truncate 均拒绝。v1 不支持 Controller identity
rotation；更换 ID/version/protocol/trust digest 必须先形成新的 migration/profile 决策，不能
覆盖该记录。

`qualification_release_v1_authorizations` 至少重复保存并约束：

```text
authorization_id, operation_id, handoff_id
project_id, workflow_run_id, publish_node_run_id, publish_node_key
action_event_id, action_event_sequence, actor_id, actor_role
output_revision_id, parent_revision_id, artifact_id
release_bundle_id/hash, canonical_receipt_id/hash
preview_receipt_id/hash, promotion_approval_id/hash
release_request_key, expected_production_run_id
request_bytes/document/hash
equivalence_bytes/document/hash
authorization_bytes/document/hash
authorized_at, creation_transaction_id
```

唯一约束至少覆盖 operation、handoff、publish node、action event、authorization 和 expected
production run。一个 Handoff output 只能有一个成功 `ActionPublish` generation；terminal
release failure 不自动分配第二个生产 operation。

`controller_bindings` 以 `authorization_id` 为主键，唯一绑定 ProductionRun 和
Controller Operation ID/request hash/identity。`results` 同样每个 authorization 最多一条，
只允许 exact `healthy` result。identity reservation 防止本地 server-owned UUIDv4 在
authorization、operation、event 和其他资格 identity role 间复用。

### 8.2 canonical 和 hash 规则

所有文档使用 strict、closed、BOM-free UTF-8 canonical JSON：拒绝 duplicate/unknown
field、non-integer number、非规范 UUID/hash/time、缺失 nullable member 和 trailing bytes。
nullable scalar 必须以显式 JSON `null` 表示，并用 SQL `IS NOT DISTINCT FROM` 比较；不能
靠 Go zero value 重建。

hash framing 固定为：

```text
SHA256(
  UTF8("worksflow-qualification-release-hash/v1") || 0x00 ||
  UTF8(domain) || 0x00 || exactCanonicalBytes
)
```

domains 固定为：

```text
worksflow.qualification-release.controller-bootstrap/v1
worksflow.qualification-release.request/v1
worksflow.qualification-release.equivalence/v1
worksflow.qualification-release.authorization/v1
worksflow.qualification-release.controller-binding/v1
worksflow.qualification-release.result/v1
```

结果编码为 `sha256:<64 lowercase hex>`。这些 domain 与 WIA、Promotion、Handoff、
Release Controller 既有 domain 不可互换。

### 8.2.1 Controller bootstrap document

closed bootstrap document 固定为：

```json
{
  "bootstrapId": "uuid-v4",
  "controller": {
    "id": "bounded-controller-id",
    "protocol": "worksflow.release-delivery/v3",
    "schemaVersion": "release-delivery-controller-identity/v1",
    "trustKeyDigest": "sha256:...",
    "version": "bounded-exact-version"
  },
  "schemaVersion": "worksflow-qualification-release-controller-bootstrap/v1"
}
```

该文档不含 bearer token、URL、DSN 或 secret path。它由 migration credential 通过
`bootstrap_qualification_release_controller_v1` 创建；函数重建 canonical bytes/hash，并只
允许数据库中首个 identity。运行时 `start_qualification_release_controller_v1` 没有 identity
参数，只能解析这条 authority。进程 readiness 还必须把环境中的 Controller ID/version/
protocol/trust digest 与该记录逐字段 exact 比较；缺失或漂移时 fail closed。

### 8.3 request document

closed request 是 server-owned retained command，不是 HTTP DTO：

```json
{
  "actionEventId": "uuid-v4",
  "authorizationId": "uuid-v4",
  "operationId": "uuid-v4",
  "projectId": "uuid-v4",
  "publishNodeRunId": "uuid-v4",
  "releaseRequestKey": "bounded-stable-server-key",
  "schemaVersion": "worksflow-qualification-release-authorization-request/v1",
  "workflowRunId": "uuid-v4"
}
```

actor、Handoff、Revision、Bundle、Receipt、Approval、Controller 或 hash 不由 request
提供；它们全部从 locked database facts 解析。

### 8.4 equivalence document

closed equivalence document 固定为：

```json
{
  "handoff": {
    "completionHash": "sha256:...",
    "consumptionHash": "sha256:...",
    "handoffId": "uuid-v4",
    "revisionAuthorityHash": "sha256:..."
  },
  "schemaVersion": "worksflow-qualification-release-workspace-equivalence/v1",
  "workspace": {
    "artifactId": "uuid-v4",
    "output": {
      "byteSize": 1,
      "contentHash": "sha256:...",
      "contentRef": "bounded-ref",
      "contentStore": "bounded-store",
      "id": "uuid-v4",
      "implementationProposalId": null,
      "proposalId": null,
      "schemaVersion": 1,
      "sourceManifestId": "uuid-v4"
    },
    "parent": {
      "byteSize": 1,
      "contentHash": "sha256:...",
      "contentRef": "bounded-ref",
      "contentStore": "bounded-store",
      "id": "uuid-v4",
      "implementationProposalId": null,
      "proposalId": null,
      "schemaVersion": 1,
      "sourceManifestId": "uuid-v4"
    }
  }
}
```

`output.id != parent.id`，而两侧 artifact/schema/store/ref/hash/byte-size/source/proposal
projection 必须逐字段相等。Handoff Revision authority 已 hash-bind copied-lineage root/count；
equivalence 不重新复制无界 lineage。ReleaseBundle Workspace 必须 exact 等于 `parent`
identity/content，migration-82 typed QualityResult 必须 exact 等于 `output` identity/content。

### 8.5 authorization document

closed authorization document 固定为：

```json
{
  "authorizationId": "uuid-v4",
  "authorizedAt": "UTC-millisecond-time",
  "equivalenceHash": "sha256:...",
  "operationId": "uuid-v4",
  "release": {
    "buildContract": {"hash": "sha256:...", "id": "uuid-v4"},
    "buildManifest": {"hash": "sha256:...", "id": "uuid-v4"},
    "canonicalReceipt": {"hash": "sha256:...", "id": "uuid-v4"},
    "expectedProductionRunId": "canonical-uuid",
    "previewReceipt": {"hash": "sha256:...", "id": "uuid-v4"},
    "promotionApproval": {"hash": "sha256:...", "id": "uuid-v4"},
    "releaseBundle": {"hash": "sha256:...", "id": "uuid-v4"},
    "requestKey": "bounded-stable-server-key"
  },
  "schemaVersion": "worksflow-qualification-release-authorization/v1",
  "workflow": {
    "actionEvent": {"id": "uuid-v4", "sequence": 1, "type": "node.execution_authorized"},
    "actor": {
      "action": "publish",
      "actorId": "uuid-v4",
      "authorizedAt": "UTC-millisecond-time",
      "role": "owner-or-admin",
      "source": "authenticated_command"
    },
    "executionProfile": {
      "hash": "854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104",
      "version": "workflow-engine/v3"
    },
    "projectId": "uuid-v4",
    "publishNodeKey": "publish",
    "publishNodeRunId": "uuid-v4",
    "workflowRunId": "uuid-v4"
  }
}
```

`owner-or-admin` 表示实际 canonical project role 值，不是一个新 role literal。BuildManifest
和 BuildContract 必须同时等于 WIA/Handoff typed input 与 ReleaseBundle 的 exact refs。

### 8.6 controller binding 和 result documents

Controller binding 固定包含：

```text
schemaVersion = worksflow-qualification-release-controller-binding/v1
authorizationId
productionRun { id, projectId, environment=production, operation=promote, stateAtBind=queued }
controllerOperation { id, requestHash, controller { schemaVersion, id, version, protocol, trustKeyDigest } }
release { releaseBundle, previewReceipt, promotionApproval }
boundAt
```

Result 固定包含：

```text
schemaVersion = worksflow-qualification-release-result/v1
authorizationId
productionRun { id, state=healthy, version }
controllerOperation { id, requestHash, resultHash, controller }
productionReceipt { id, hash }
deploymentRevision { id, hash }
publishResult { url, deploymentId }
completedAt
```

`publishResult.deploymentId` 固定为 exact DeploymentRevision ID。URL 是 Controller terminal
result/ProductionReceipt 中的同一 bounded public URL；caller 不能覆盖。

### 8.7 SQL capability surface

migration/deployment bootstrap credential 额外且仅能调用：

```text
bootstrap_qualification_release_controller_v1(
  bootstrap_id uuid,
  controller_id text,
  controller_version text,
  controller_protocol text,
  controller_trust_key_digest text
)

inspect_qualification_release_controller_bootstrap_v1()
```

bootstrap mutation function 不授权给 runtime operator、application、auditor 或 `PUBLIC`。
它只允许数据库 owner，或以 `session_user` 直接继承
`worksflow_migration_owner`、且 `current_setting('role') = 'none'` 的 canonical migration
login，在显式 deployment bootstrap 进程中调用一次；业务请求和普通服务启动不得代调。
只读 inspect 可以授权给 release operator，用于同一 primary statement 的 readiness exact
comparison。

对 operator 暴露的初始 capability 仅为：

```text
authorize_qualification_release_v1(
  operation_id uuid,
  authorization_id uuid,
  action_event_id uuid,
  release_request_key text,
  project_id uuid,
  workflow_run_id uuid,
  publish_node_run_id uuid,
  actor_id uuid
) RETURNS SETOF jsonb

inspect_qualification_release_operation_v1(operation_id uuid)
resolve_qualification_release_authorization_v1(authorization_id uuid)
resolve_qualification_release_for_publish_v1(workflow_run_id uuid, publish_node_run_id uuid)

claim_qualification_release_publish_v1(
  authorization_id uuid,
  workflow_run_id uuid,
  publish_node_run_id uuid,
  claim_event_id uuid,
  lease_owner text,
  lease_duration_milliseconds integer
)
inspect_qualification_release_publish_claim_v1(claim_event_id uuid)
renew_qualification_release_publish_lease_v1(
  authorization_id uuid,
  workflow_run_id uuid,
  publish_node_run_id uuid,
  claim_event_id uuid,
  lease_owner text,
  lease_attempt integer,
  expected_lease_expires_at timestamptz,
  new_lease_expires_at timestamptz
)

start_qualification_release_controller_v1(
  authorization_id uuid,
  claim_event_id uuid,
  lease_owner text,
  lease_attempt integer
)
inspect_qualification_release_controller_v1(authorization_id uuid)

record_qualification_release_result_v1(authorization_id uuid)
inspect_qualification_release_result_v1(authorization_id uuid)

record_qualification_release_failure_v1(authorization_id uuid)
inspect_qualification_release_failure_v1(authorization_id uuid)

apply_qualification_release_result_v1(
  authorization_id uuid,
  workflow_run_id uuid,
  publish_node_run_id uuid,
  lease_owner text,
  lease_attempt integer
)

apply_qualification_release_failure_v1(
  authorization_id uuid,
  workflow_run_id uuid,
  publish_node_run_id uuid,
  lease_owner text,
  lease_attempt integer
)
```

`claim` 必须先原子 append immutable claim/event/outbox、推进 cursor 并取得 Publish lease；
`renew` 只允许 exact current claim/owner/attempt 做有上限的单调 CAS，不写 Workflow event。
generic Workflow worker 永不 claim 或 renew 新 hash 的 v3 Publish。
claim canonical bytes 的 exact domain 是
`worksflow.qualification-release.lease-claim/v1`（外层仍使用
`worksflow-qualification-release-hash/v1`）。

`start` 只接受 DB time 下仍未过期、run cursor 与完整 claim chain 都 exact 的 current claim，
并要求 caller 显式提交同一 claim event ID、owner 与 attempt，防止旧 epoch 借用新 epoch；
然后从 authorization 解析 exact Bundle/Preview/Approval，并从唯一 immutable bootstrap
authority 解析 Controller identity，在一个事务内创建/replay Reconciled ProductionRun、Operation 和
controller binding；它不发网络请求。claim 丢失后不得再次 start，只能 inspect 已存在的
Operation。Bearer token 永不进入数据库函数或 canonical document。

`record result` 只读取并锁定 exact healthy ProductionRun、Controller Result、
ProductionReceipt 和 DeploymentRevision，然后 append result。`record failure` 只接受三种
closed terminal evidence：failed checks、Controller rejection，或仍为 `prepared`、零 submit/
reconcile attempt、无 attempt/result/receipt/revision 的 pre-submit cancellation。所有 inspect
在一个 read-write-primary statement 内完成“primary check + exact lookup”，避免 replica lag
伪造 not-found。

`authorize` 必须在自己的 transaction 内同时 append authorization、actor/event/outbox，
并通过 transaction-local grant 完成 Publish `waiting_input -> ready` 及相应 run transition；
不得先提交 ledger、再由 generic Engine 单独推进节点。`apply result` 是 v3 completion 的
唯一 mutation capability：它锁定并重验当前 Workflow lease/attempt、读取 immutable
healthy result，写入 exact `PublishResult`/actor/event/outbox，再通过另一种 transaction-local
grant 完成 `running -> completed` 及相应 run transition。`record result` 自身不锁或更新
workflow rows，避免 Release lock path 反向进入 Workflow。

Go adapter 必须 strict decode 返回 bundle，重算每个 canonical bytes/hash 和重复 scalar；
数据库结果不能作为“已经检查过”的不透明 JSON 使用。

### 8.8 trigger 和 no-bypass

Migration `000084` 必须安装并由 production posture exact allowlist 检查：

- 六张 durable authority 表的 `BEFORE UPDATE OR DELETE OR TRUNCATE` immutable guards；
- grant insert/consume/empty-at-commit guards；只有 migration-owner 的
  `authorize_qualification_release_v1` 可签发 `authorize_ready` grant，只有
  专用 claim/renew capability 可签发 `claim_lease`/`renew_lease` grant，只有
  `apply_qualification_release_result_v1` 可签发 `healthy_complete` grant。每条 grant exact
  绑定 backend PID、`pg_current_xact_id()`、project/run/node、authorization、from/to status，
  并由目标 node/run 的 `BEFORE UPDATE` trigger 在同一 transaction 恰好消费一次；
- authorization/action-event/outbox deferred closure；
- authorization/Handoff/revision/Bundle/preview/approval exact closure；
- controller binding 与 ProductionRun/Operation 的双向 deferred closure；
- result 与 Controller Result/Receipt/DeploymentRevision 的双向 deferred closure；
- v3 Publish node/run guard：`waiting_input -> ready` 和对应 run transition 必须同时存在
  immutable authorization 且消费 exact `authorize_ready` grant；`ready -> running` claim
  必须重验同一 authorization/actor 并消费 exact `claim_lease` grant；同 attempt renew 必须消费
  exact `renew_lease` grant；`running -> completed` 和对应 run terminal transition
  必须同时存在 immutable healthy result 且消费 exact `healthy_complete` grant；
- v3 Publish output/event actor 必须等于 authorization/result；
- direct DML、generic Engine authorization、caller-set GUC、`session_replication_role`、
  trigger disable 或伪造 transaction ID 都不能授权。

deferred closure trigger 还必须在 commit 前证明：authorization ↔ authenticated action
event ↔ outbox ↔ actor metadata ↔ ready state，以及 healthy result ↔ Publish output ↔
completion event ↔ outbox ↔ completed state，都是 exact one-to-one；任一半提交都使整个
transaction 失败。generic Engine/GORM mutation、普通 application DML 或仅有一条合法 ledger
row 都不能伪造 grant。这样 ledger 是 transition 的必要 authority，而不是事后审计旁路。

Migration owner 是所有表、index、trigger function 和 SECURITY DEFINER routine 的唯一
owner。所有 routine 使用固定 `pg_catalog,<trusted-schema>` search path，显式 revoke
`PUBLIC`，普通 application、Promotion、Policy、Input、Handoff、auditor 和 Release worker
角色都没有 authority table DML 或 generic routine execute。唯一例外是
canonical migration-owner login 对 bootstrap mutation capability 的 owner execution；它必须
以 `session_user` 继承 migration-owner、保持 `role=none`，且该 membership 不得传给任何
runtime LOGIN，production posture 必须证明这一点。

### 8.9 独立角色

新增稳定 `NOLOGIN` role：

```text
worksflow_qualification_release_operator
```

它只获得 trusted schema USAGE、runtime/inspect capability EXECUTE；不得获得 bootstrap
mutation capability。生产
`worksflow_qualification_release_login` 必须：

- `ROLINHERIT`；
- 只有一个 direct membership，且为 `INHERIT TRUE / SET FALSE / ADMIN FALSE`；
- 不拥有 database/schema/relation/routine；
- 与 application、migrator、auditor、Policy、Input 三角色、Promotion、Handoff 的 LOGIN、
  password 和 pool 全部不同；
- 使用 direct 或明确的 session-pool DSN，拒绝 transaction pooling；
- `current_setting('role') = 'none'`，不依赖 `SET ROLE`。

Production posture 必须增加第十条 credential/LOGIN 检查。一个通过的 posture report 仍不
等于 runtime activation 或 external qualification。

## 9. Migration-84 原子锁序

所有 mutation 使用 pre-BEGIN session advisory lock；等待者不能先 `BEGIN` 再等待，否则
会保留 stale SERIALIZABLE snapshot。

authorization lock key：

```text
worksflow:qualification-release-v1:<workflowRunId>:<publishNodeRunId>
```

锁序固定为：

1. session advisory lock；
2. `BEGIN ISOLATION LEVEL SERIALIZABLE`；
3. shared migration rollout fence；
4. handoff/authorization locator 只读定位 project，不加 row lock；
5. `projects(id) FOR UPDATE`；
6. workflow run 和 affected node rows 按 UUID 排序；
7. Handoff completion/binding、artifact/output/parent rows按稳定 identity；
8. ReleaseBundle、CanonicalReceipt、PreviewReceipt、PromotionApproval；
9. authorization/reservation append、Workflow actor/event/outbox 更新；
10. full closure re-read 后 COMMIT。

Controller start 固定为 project → immutable controller bootstrap → authorization → release environment head →
ProductionRun/Operation。Result append 固定为 project → authorization/controller binding →
ProductionRun/Operation/Result → Receipt/DeploymentRevision。它不反向锁 workflow rows；
Workflow completion trigger 只读取 immutable result，从而避免 Release worker 与 Workflow
apply 互锁。

Authorization capability 的 workflow path 固定为 project → run/node → Handoff/revision →
Release authorities → authorization/event/outbox/grant → node/run transition。Completion apply
path 固定为 project → run/node/lease → immutable authorization/result → output/event/outbox/grant
→ node/run transition；它不锁 Controller mutable rows。两个 path 都由 capability 自己完成，
不能在 capability 返回后再交给 generic Store 写 transition。

unlock 使用 cancellation-independent bounded context，返回值必须为 `true`。lock acquisition、
`BEGIN`、`COMMIT`、`ROLLBACK`、unlock acknowledgement 或 transport 任一未知时，poison 并
discard physical session；不再 unlock 或放回 pool。

## 10. Profile-v3 Release Controller publisher

仓库内 publisher 已命名为：

```text
QualifiedReleaseControllerPublisher
```

它由独立 `qualificationrelease.WorkerService` 调用，不作为 generic `WorkerRunner` 安装到
v3 bundle。v3 runtime 只 seal 一个不可执行的 qualified-release capability marker；对该
marker 调用 `Run` 或从 shared Workflow worker claim exact v3 Publish 都会 fail closed。
专用 worker 的调用流程固定为：

1. 从 `(workflowRunId,publishNodeRunId)` 解析 migration-84 authorization；不接受 caller
   Bundle/Receipt/parent/equivalence flag。
2. 以 server command UUIDv4 调用 `claim_qualification_release_publish_v1`；generic Workflow
   worker 的共享 claim 集必须排除 exact v3 Publish。commit unknown 用同一 claim event ID
   replay/inspect，不得分配第二个 attempt。
3. 只有 caller 提交的 claim ID/owner/attempt 正是 DB time 下未过期且 run cursor/claim chain
   exact 的 current claim 时，`start_qualification_release_controller_v1` 才创建或 exact replay 同一
   queued ProductionRun/Operation。
4. Release Controller worker 异步执行 submit/reconcile；publisher 只 poll durable
   ProductionRun/Operation，不直接调用远端 Controller。
5. 专用 v3 worker 在 publisher 等待期间调用 migration-84 renew capability 持续延长 Workflow
   lease。Controller 或 lease 的 active/blocked 状态都不等于资格化成功；
   `queued/claimed/submitting/reconcile_wait/reconciling/verifying` 都不是成功，外部资格未完成项
   也不得写成已资格化或 Publish completed。
6. `reconcile_blocked` 只允许既有 GET-only operator Case 恢复同一 Operation；不得换 ID 或
   resubmit。publisher 保持等待，并由专用 worker concurrency 防止阻塞所有普通 Workflow。
7. `healthy` 后调用 `record_qualification_release_result_v1`；commit unknown 用同一
   authorization inspect。
8. 调用 migration-84 `apply_qualification_release_result_v1` 或 terminal failure apply
   capability，在数据库内把 immutable result、event/outbox、Publish node 和 Workflow run
   原子闭合；generic `applyResultV3` 对 Publish 保持拒绝。

### 10.1 Workflow lease、进程崩溃和 replay

现有 shared runner path 的默认五分钟 timeout 和单次 max-attempt 不能静默用于 v3
Publish。`executeNodeV3` 是拒绝 generic Publish 的围栏；下列 continuation semantics 由
专用 qualified-release publisher/worker 拥有：

- 只要 Controller operation 是 active/blocked，runner 不把 poll timeout、process shutdown、
  context cancellation 或 lease loss提交为 node failure；
- Worker 正常运行时持续 renew lease；进程退出后 lease 过期，由另一个 v3 worker reclaim；
- reclaim 后只 resolve 同一 authorization/ProductionRun/Operation，再继续 poll；
- claim attempt 是 workflow lease epoch，不是分配新 release operation 的理由；
- Release `failed/error/cancelled` 或 authenticated terminal rejection 才形成 terminal Publish
  failure；自动/manual retry 都不能创建第二个 production mutation。修复需要新的受治理
  Workspace/qualification workflow，或既有 Controller operator reconciliation。

如果实现选择把长等待拆成新的 `waiting_release` 状态，而不是持有 lease，则该状态、
wake-up authority、descriptor component 和 migration 必须先升级本文并产生另一 profile
hash；不得在当前 hash 下临时改变语义。

### 10.2 completion crash windows

以下窗口必须安全：

- crash before start commit：authorization 存在，ProductionRun 不存在；replay 创建一次；
- start commit response lost：inspect authorization/controller binding；不创建新 run；
- Controller mutated remote but HTTP response lost：existing Release `submit_unknown -> GET`
  protocol处理；
- crash after healthy before migration-84 result：replay从 same healthy facts append一次；
- result committed but Workflow apply crashed：reclaimed lease读取同一 result并完成节点；
- Workflow completion commit response lost：读取 node/event/result closure；不调用 Controller。

## 11. 幂等、未知结果和失败分类

所有阶段遵循同一原则：identity 在第一次 side effect 前稳定，unknown 只 Inspect，绝不以
新 identity 猜测补偿。

| 分类 | 行为 |
| --- | --- |
| validation / malformed / cross-project | 永久拒绝，不进入 Store |
| not-ready | 保持当前状态；等待缺失的 exact upstream authority |
| stale | 永久阻塞当前 generation；重新从新的上游流程开始 |
| conflict / corrupt | 隔离并告警；禁止自动修复或重建 canonical bytes |
| PostgreSQL `40001` / `40P01` | definite abort；bounded same-ID full retry |
| commit/transport unknown | discard session；fresh primary same-ID Inspect；未判定前不重试 side effect |
| Controller active/unknown | same Operation ID/hash GET reconcile |
| Controller `reconcile_blocked` | operator Case，永久禁止新 PUT/resubmit |
| Controller terminal unhealthy/rejected | Publish terminal failure；不生成第二个 production op |
| Workflow lease lost | 不改 node terminal state；由同 authorization replay |
| ProductionRun pre-submit cancelled | 冻结 `pre_submit_cancelled` failure；同 authorization 将 Publish/Workflow 原子失败，且不创建第二个 production op |

任何 error 返回到 UI 前都应转换为 stable non-secret code；DSN、SQL、Controller token、
provider response body、raw evidence 或 retained content 不得进入 blocker/detail。

## 12. 配置、DSN 与进程拓扑

### 12.1 fail-closed feature gates

仓库内已经实现并默认关闭以下三个 gate：

```text
WORKFLOW_PROFILE_V3_RUNTIME_ENABLED=false
WORKFLOW_QUALIFICATION_ACTIVATION_WORKER_ENABLED=false
QUALIFICATION_RELEASE_PUBLISHER_ENABLED=false
```

activation 与 release operator 还分别要求
`WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_DSN` 和
`QUALIFICATION_RELEASE_POSTGRES_DSN`；配置校验要求它们不与 application credential 复用、
指向同一 PostgreSQL endpoint/database/TLS authority，并使用有界 pool/retry/lease 参数。
`QUALIFICATION_COORDINATOR_ENABLED` 与 `QUALIFICATION_HANDOFF_WORKER_ENABLED` 仍只是建议的
独立部署 gate，当前配置模型尚未提供，不能伪装成已接线。

任何下游开关为 true 时，所有上游 readiness 必须已通过；非法组合使进程启动失败，不能
降级到 legacy publisher。

现有 Release Controller 配置继续使用：

```text
RELEASE_DELIVERY_WORKER_ENABLED
RELEASE_DELIVERY_CONTROLLER_URL
RELEASE_DELIVERY_CONTROLLER_ID
RELEASE_DELIVERY_CONTROLLER_VERSION
RELEASE_DELIVERY_CONTROLLER_PROTOCOL=worksflow.release-delivery/v3
RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST
```

启动 readiness 必须通过 release operator 的 primary connection 读取唯一 Controller bootstrap，
并把上述四个非 secret identity 字段逐字段 exact 比较。bootstrap 缺失、多条、canonical/hash
不闭合或环境配置漂移时，任何 v3 registry/authoring/worker/subscription 开关均不得启用。
bootstrap 注册仍必须使用 migration credential，runtime readiness 不得自动创建或修复它。

Bearer token 只从 secret file/mount 进入 HTTP provider，不写入日志、database、Outbox 或
canonical authority。

### 12.2 推荐 deployment 拆分

```text
API / Workflow control deployment
  application DSN
  authenticated Facade
  WIA activation coordinator (可独立 deployment)

Qualification coordinator deployment
  Plan/Evidence/Receipt authorities
  Input issue DSN
  Source admission DSN
  Credential admission DSN
  Promotion DSN

Handoff consumer deployment
  JetStream durable consumer
  Handoff DSN only

Profile-v3 workflow worker deployment
  Workflow application Store
  qualified-release DSN
  v3 registry only after readiness

Release Controller worker deployment
  existing ReconciledDeliveryStore capability
  Controller HTTPS credential/trust pin
```

Policy、Input 三角色、Promotion、Handoff、qualified release 均使用独立 `*sql.DB` pool。
指针不同还不够；production posture 必须验证 distinct LOGIN/secret、exact membership、
schema ACL、routine allowlist、primary/read-write 和 session affinity。

### 12.3 durable messaging

- PostgreSQL Outbox 是 publish intent authority；JetStream 是 at-least-once transport。
- consumer name、stream、subject 和 ack policy 必须稳定配置并进入 readiness。
- redelivery 必须只携带 opaque operation/handoff ID。
- ACK 必须晚于 Store success 或 same-ID Inspect reconciliation。
- NATS outage 只积压 Outbox，不触发直接表 scan 或同步 fallback side effect。

## 13. 分阶段 rollout

### Phase A：descriptor 和 runtime fence

- 状态：仓库内已实现；
- v3 dispatch identity/hash cascade、`sealRuntimeV3`、ownership、`reconcileV3` 和
  runtime-disabled 定向测试已存在；
- built-in registry 和 `Current` 仍不注册/指向 v3，只有显式 opt-in 平台工厂可以 seal
  独立 v3 runtime；
- v0/v1/v2 继续使用各自冻结 bundle，不由 v3 publisher 接管。

### Phase B：activation 和 qualification workers

- 状态：部分 `implemented-internal`，未部署；
- migration `000085`、GORM same-transaction Quality precommit、closed candidate resolver、
  WIA activation transaction owner 和 bounded JetStream worker 已实现；
- Plan/Evidence/Receipt/Input/Promotion/Handoff 的目标部署编排、paused subscription 演练
  和 commit-unknown/duplicate/outage full-chain canary 仍待完成。

### Phase C：migration `000084`

- 状态：SQL authority、authenticated adapter 和 strict decoder 已在仓库内实现；
- 表、functions、roles、triggers、posture、old-hash fence、bootstrap/authorization/result
  capability 及 rollback guard 已编码，feature gates 默认仍为 false；
- 目标环境尚未应用 migration，也未由 migration credential 注册唯一 Controller bootstrap；
  release operator Inspect 与部署配置的真实 readiness 尚未形成批准证据。

### Phase D：qualified publisher shadow readiness

- 状态：publisher/store/observer/worker/config/readiness/app lifecycle 已
  `implemented-internal`，默认关闭；
- 目标环境仍需用无外部 mutation 的 dry readiness 验证 Controller identity、专用 DSN、
  canonical decode 和权限姿态；
- dry readiness 不得写 node、authorization 或 ProductionRun。

### Phase E：单项目 canary

- 只允许 reviewed canary project 创建 v3 run；
- 执行完整 `85 -> WIA -> 80 -> 81 -> 82 -> 84 -> queued -> healthy -> Publish completed`；
- 并发、worker kill、NATS redelivery、HTTP response loss、DB commit unknown、lease takeover
  全部注入；
- 验证所有 no-bypass 和 exact cardinality。

### Phase F：受控激活

- 独立批准 v3 registry 和 authoring allowlist；
- 逐项目扩大；
- `Current` alias 仍不自动切换；
- external Golden qualification 完成前，状态仍是 `implemented-internal` 或
  `production-wired`，不能写 `externally-qualified`。

## 14. Rollback

运行时 rollback 与 migration down 是两件事：

1. 先关闭新 v3 definition/start 和新 ActionPublish；
2. 停止 activation/Input/Promotion/Handoff 新消费，但保留 inspect/read；
3. 已有 Controller operation 必须由旧 exact Controller worker对账到 terminal；
4. 只要存在 persisted v3 run，必须保留其 exact runtime replay bundle，不能 unregister 后让
   历史 run 变成不可解释；
5. 只要 migration-84 任一 controller bootstrap/authorization/binding/result、post-authorization Workflow event
   或关联 ProductionRun 存在，`000084.down` 必须拒绝；使用 forward fix；
6. 只要 migration-85 任一 precommit/material/candidate/identity reservation 存在，
   `000085.down` 必须拒绝；不得丢弃不可重建的 Quality completion authority；
7. 空部署 down 先取 exclusive rollout fence，再按 project/workflow/release 锁序证明零 durable
   authority，才可恢复 migration-82 guard；
8. 不删除 immutable history 来让 rollback 成功。

Controller 已发生 remote mutation 时，产品 rollback 必须创建新的 governed
DeploymentRevision/rollback operation；不能把 schema down 当作业务回滚。

## 15. 验证矩阵

### 15.1 static / unit / race / vet

- descriptor canonical bytes、新/旧 hash 和 component identity；
- v0/v1/v2 runtime regression 零变化；
- `reconcileV3` 在 Quality complete 后保持 external gate pending；
- Quality completion 同一事务持久化 gate canonical typed input；缺失/失败整体回滚；
- WIA replay 只能重建并 exact 比较该 input，不能首次写入或覆盖 run context；
- external gate 永不 claim；
- generic PostgreSQL/memory scheduler 永不 claim v3 Publish；v3 Publish 只能绑定 qualified
  publisher 与 migration-84 claim/renew authority；
- strict canonical duplicate/unknown/missing-null/size/time/hash vectors；
- fake-driver unknown lock/BEGIN/COMMIT/ROLLBACK/unlock 全部 poison connection；
- package unit、`-race` 和 `go vet`。

### 15.2 real PostgreSQL

- Controller bootstrap 缺失时 start/readiness fail closed；同 ID exact replay，第二 identity、
  runtime-role bootstrap、update/delete/truncate 和配置漂移全部拒绝；
- fresh WIA activation及按 node replay；
- two activation workers one winner；
- exact `000085 -> WIA -> 000080 -> 000081 -> 000082` chain；
- Handoff duplicate delivery one completion；
- authenticated ActionPublish one authorization/event；
- actor role revoked/stale、wrong node/project、missing Release prerequisite 均无 partial state；
- authorization、controller start、result 三处 commit unknown inspect；
- claim event ID commit unknown replay、未过期竞争冲突、过期 takeover、renew monotonic CAS；
- two publisher workers one ProductionRun/Operation；
- node complete without result、result without healthy Controller、forged equivalence、direct DML、
  old profile hash 全部拒绝；
- authorization/result ledger 已存在但缺 grant、grant 类型/transaction/PID/from-to 不匹配、
  grant 未消费或重复消费、generic Store 直改 node/run 全部回滚；
- exact catalog/owner/ACL/search_path/trigger enabled/deferrable posture；
- empty rollback passes，任一 durable fact blocks down。

### 15.3 crash / transport / controller

- process kill before/after每个 commit；
- Workflow lease loss during `queued`、`reconcile_wait`、`verifying` 和 healthy/result window；
- NATS publish outage、redelivery、ACK loss；
- Controller PUT accepted response loss；
- GET 404 only before acknowledgement permits same-ID resubmit；
- accepted/running 后 history loss进入 `reconcile_blocked`；
- operator Case remains GET-only；
- healthy result replay不发第二个远端 mutation；
- workflow completion CAS conflict只应用 cached result。

### 15.4 end-to-end no-bypass

最终 canary 必须证明 exact cardinality：

```text
1 Quality completion precommit
1 immutable activation candidate snapshot
1 WIA
1 Input Precommit
1 Promotion consumption
1 pending Handoff
1 Handoff completion
1 same-content output Revision
1 immutable Controller bootstrap
1 authenticated ActionPublish authorization
1 Controller ProductionRun
1 Controller Operation terminal Result
1 migration-84 healthy result
1 completed Publish node
1 completed Workflow run
```

同时证明：零 generic external-gate claim、零 legacy publisher call、零 caller equivalence flag、
零第二 production operation、零绕过 event/Outbox、零未授权 UI action。

外部 Golden 还必须在 approved TemplateRelease、真实 Registry/KMS/cluster/Controller、真实
credential set 和 22-case evidence 上独立运行。仓库内上述 canary 不替代该证据。

## 16. 明确非目标

本合同不：

- 让模型、浏览器或 generated application 构造 authority/hash/Receipt/Bundle；
- 把 migration-82 same-content Revision 当作新内容或重新 build；
- 修改 legacy v0/v1/v2 publisher 语义；
- 允许 Preview-only、local static publish 或旧 `/deployments` 作为 production fallback；
- 实现 Registry、KMS、Secret Broker、cluster、canary/blue-green provider；
- 证明 Golden 22-case 已执行或 external qualification 已通过；
- 允许在 terminal failed ProductionRun 上用新 ID“再试一次”；
- 允许 UI 通过隐藏 blocker、刷新或自动重载来推进 authority；
- 允许 down migration 删除 immutable release/qualification history。
- 在同一 v1 authority 下轮换或覆盖 Controller bootstrap identity。

## 17. 实施完成定义

本文当前使用 `implemented-internal` 仅表示新 v3 hash cascade、opt-in runtime、workers、
migrations `000083`/`000084`/`000085`、immutable Controller bootstrap 合同、配置和
readiness 代码已经存在，且静态合同与定向单元回归已通过。该标签不表示 migration 已在
目标数据库执行、真实 Controller 已连接或 full-chain canary 已通过。

升级为 `production-wired` 前仍必须同时满足：

- static/unit/race/vet/real PostgreSQL/rollback/full-chain tests 全部通过；
- no-bypass canary 证明上述 exact cardinality；
- 目标 operator LOGIN/DSN/secret、Controller bootstrap、NATS subscription 和各 feature
  gate 经过独立批准与 readiness 验证；
- 文档账本与部署事实一致，并保留可立即关闭的独立 feature gate。

只有目标环境 Controller/Registry/KMS/cluster/credential/Golden evidence 完成并形成独立
签名 Receipt 后，才能进一步使用 `externally-qualified`。在此之前，UI 的批准/完成状态和
任何产品说明都必须保留这一事实边界。
