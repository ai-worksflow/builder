# Worksflow Full-stack Quality Profile 与 VerificationReceipt 实施规格

版本：v0.1

日期：2026-07-18

状态：实施基线草案；Candidate/Canonical/Release 平台边界已分期落地，尚未构成 Stage 3/4 外部退出证据

## 1. 文档定位

本文把 [`Worksflow 真实 AI 构造器与用户沙盒架构`](./ai-constructor-architecture.md) 第 15 节细化为可直接实现的服务、数据库、API、UI 和验收契约。它解决一个明确边界：Stage 2 的 `PatchValidationReceipt` 只能证明平台准确采集并约束了 Agent Patch，不能证明项目能构建、满足验收、可部署或可在生产运行。

本文初稿曾以“Candidate `VerificationReceipt` 尚未存在”作为实施起点；该句只是历史背景，不是 2026-07-18 的当前事实。Candidate/Canonical Verification 对象、Receipt 和数据库门禁的当前状态以第 2 节和第 13 节的实施账本为准。

本文仍不得被解释为以下事实：

- 已有 Candidate/Canonical Receipt 代码就等于 FQP 或 Stage 3/4 已通过生产资格验证。
- Candidate Receipt 可以发布，或可以复用为不同 WorkspaceRevision 的 Canonical Receipt。
- Agent 自报的命令、退出码或测试总结可以签发受信证据。
- 没有 approved Golden TemplateRelease 时可以用测试 fixture 签署阶段退出。
- 本地 Compose DinD、mutable image tag、开发机缓存或保守 token upper-bound 可作为生产质量边界。

若本文与主架构冲突，以主架构的权威关系、安全边界和阶段退出条件为准；实现细节冲突则先更新本文并记录 Decision ID，不能在代码中形成隐式分叉。

## 2. 当前事实与目标差距

### 2.1 已存在的事实

- `internal/agent.PatchValidationReceipt` 绑定 exact Attempt、TaskCapsule、base/proposed tree、平台采集 Patch、路径策略和 runner identity，并明确设置 `independentQualityRequired=true`。
- Agent exact patch-file review API 仅从 finalized evidence 解析 exact patch 声明的 base/proposed 路径，对 tree/hash/size/mode/raw bytes 重验；前端逐 operation 加载、文本只读 diff/binary 原字节下载并要求逐项 acknowledgement，全部确认前 Merge 关闭。它仍只是 Patch integrity/review evidence，不是独立质量 Receipt。
- Candidate 已有内容寻址 tree、真实文件字节、`100644`/`100755` mode、checkpoint、lease/session fence、原子 Merge/Undo、freeze receipt，以及独立 Candidate VerificationPlan/Run/Attempt/Receipt。
- Candidate exact-head literal search 已要求请求中的 generation/root 双围栏，并严格按 normalize → project view authorization → query admission → Candidate Repository I/O 执行。migration 000062–000065 提供 PostgreSQL immutable exact-tree index、durable single-builder claim、原子 project quota 与 project-scoped composite GIN；默认 quota 为每项目 `16 trees / 256 MiB logical source bytes / 2 active builds`。app 把同一个 Redis authority 注入 search 与 secure `BuildForActor`；query project `20/s burst 40` + actor `4/s burst 8` 覆盖 index 与 short/no-trigram/glob 请求，first-builder project `1/15s burst 2` + actor `1/30s burst 1` 只由 durable claim 的首次实际 owner 在 FileBlob resolve 前扣取，ready reuse/waiter/follower 不扣。malformed/outage 与 quota/rate/tamper 均 fail closed，不得进入 bounded fallback。合法 rate denial 为 `429 + Retry-After`，active-build quota 为 `429`，retained tree/source-byte quota 为 `409`，未知 index failure 为 `503`。两条合法路径最终都重新读取并重验 authoritative bytes 和 closing head。前端以 bounded `Retry-After` 按 exact identity 最多重试一次，且 quota/outage 不刷新 Candidate、Blueprint 或 dirty editor。migration 000066 已内部实现 bounded retention/GC、全状态 Candidate/live claim 保护、exact CAS/locks、shared blob 引用保护、receipt/tombstone、三角色 definer ACL 和同 run-id 恢复；生产角色/schema/DML/secret 仍须外部预置。该索引不是 Git pack/global symbol index，Production LSP v1 的 LSP-0–3 另有内部实现，但 LSP-4/QA-016 尚未资格化。
- ordinary Candidate file read 已要求完整 Session/Candidate/file request fence，并在 blob 解析前、返回 bytes 前重验 head；响应回显 Candidate ID/journal/head/content hash，漂移返回 `409 sandbox_file_head_changed`。默认 CORS 已覆盖新 headers，自定义 CORS 部署必须显式 allow/expose 完整集合。
- Candidate freeze→Candidate VerificationReceipt→Implementation Proposal→逐项 review→CAS Apply→immutable WorkspaceRevision 已通过真实 PostgreSQL 服务闭环；Receipt 的 project/snapshot/tree/checkpoint/BuildContract/TemplateRelease/Profile/Plan/hash 在事务提交边界重验。
- Candidate 与 Canonical 已有分离的 server-owned Verification Plan/Run/Attempt/Check/Coverage/Receipt 状态机；旧 `/quality-runs` 只作 Canonical 兼容读取，不能作为第二套发布权威。
- Candidate/Canonical executor 与 worker 将任何 bounded output 截断归一为带 blocker 的 `error`；Receipt 构造器和 migration 000051 的数据库约束共同拒绝 `passed + truncated`。
- migration 000052 为两类 worker 增加 exact-fence 持久 cleanup obligation；claim/reclaim 与 `registered` obligation 同事务提交，正常 Receipt 必须等待其全部 Attempt cleanup `completed`。
- 两类 worker 在 exact claim 后对正常完成、取消、lease loss 和中途错误统一执行 exact-fence cleanup；旧 fence 无权删除新 owner 的 workspace、容器或共享 runtime，失败 cleanup 可由持久 lease 重试/接管。
- Full-stack Plan v2 已编译 Node/Python toolchain、lock/Registry/resolver argv/cache identity、ephemeral PostgreSQL/internal service network、migration/health/tenant/contract 检查和 Oracle→Obligation Must 100% 覆盖。Canonical materializer 保留 file mode，分别以只读 `0400`/可执行 `0500` 物化。
- migration 000043–000046 已建立 Canonical Receipt→ReleaseBundle→PreviewReceipt→PromotionApproval→ProductionReceipt→DeploymentRevision/rollback 的 exact authority；migration 000048 又把 production current head 按 project/environment single-flight 并以 exact expected-head CAS 前进。
- migration 000056 为新 v2 Preview/Production Run 原子持久稳定 Release Delivery Operation、canonical v3 request/hash、exact Controller identity、append-only Attempt/observation 和唯一 terminal Result。未知 PUT 结果必须先用同一 ID/hash GET 对账；只有尚未承认的 Operation 在 404 后可 same-ID/hash resubmit。
- v2 PreviewReceipt、ProductionReceipt 和 DeploymentRevision 必须持有 exact Controller Operation/Result hash；v1 历史事实只读，legacy nonterminal Run 在 migration 56 进入 `reconcile_blocked`，不伪造已知远端结果。
- Content Reconciler 已将 Candidate/Canonical Plan/Receipt 和 ReleaseBundle/Preview/Approval/Production/Deployment content refs 纳入 SQL 可达性；release delivery tables 部分迁移或查询故障时 fail closed。
- staging/production 配置要求 verifier/runner image 使用 registry digest；Agent/Sandbox runtime identity 已拒绝 tag，Runner build 与第一方 frontend/backend/relay base 也拒绝 mutable override。但本地 Compose 的第三方服务以及部分 quality/resolver 默认仍使用开发 tag，因此整个 Compose 仍不能签发生产供应链证据。

### 2.2 初稿差距的当前闭合状态

下表保留初稿的七个 gap 作为历史账本。它们不再可以被原样引用为“当前代码不存在”；“代码边界已闭合”也不等于“外部生产资格已通过”。

| 初稿 gap | 2026-07-18 代码状态 | 仍未完成的外部/组织条件 |
|---|---|---|
| CandidateSnapshot 无独立 Receipt | 已关闭；migration 000039–000042 和 freeze deferred gate 绑定 exact Candidate subject/lineage/profile/plan/receipt | approved Golden 多服务资格运行仍缺 |
| QualityRun 只支持单根 Node/Go | 平台执行链路已关闭；Plan v2 已支持 Node/Python、ephemeral PostgreSQL、migration/health/tenant/contract | approved Python/Playwright verifier images、真实 Golden Stack 和浏览器 corpus 仍缺 |
| 缺 Obligation/Oracle 稳定 ID 和 Must 100% | 已关闭；Plan、Check、Coverage、Receipt 和 PostgreSQL passed gate 都绑定稳定 ID | hidden corpus 的正式内容和批准仍缺 |
| profile/image/command/hidden/cache identity 未入 Receipt | profile、digest-pinned image、command/plan hash 和 cache identity 已进入不可变计划/证据链 | 加密 hidden-test broker、真实 image attestation 和保留策略仍缺 |
| Candidate/Canonical 质量事实未分离 | 已关闭；两套 subject/state machine/Receipt 数据库约束分离，Publish 只接受 Canonical | 无（仍需持续做回归和数据迁移监控） |
| Proposal 未绑定 passed Candidate Receipt | 已关闭；freeze 事务重验 exact passed Receipt，Apply 要求全部 operation decision 并重算 exact tree | 无（外部 Golden 只影响 Receipt 资格，不放宽 binding） |
| Release/Publish 只有静态 BuildArtifact | 平台 authority 已关闭；Canonical Receipt、完整 ReleaseBundle、Preview/Promotion/Production/Deployment/rollback 已绑定，migration 000048 对 current head single-flight + CAS，migration 000056 对未知 Controller 结果持久对账并将 v2 Receipt/Revision 绑定 exact Result | 仓库内没有真实 Release Controller deployment/qualification；Registry/KMS/Secret Broker、cluster 和 rollout 资格仍缺 |

## 3. 规范目标与非目标

### 3.1 目标

- **FQP-GOAL-001**：从 exact BuildContract、FullStackTemplate 和 TemplateRelease 编译确定性 VerificationPlan。
- **FQP-GOAL-002**：Quality Sandbox 只从 immutable CandidateSnapshot 或 WorkspaceRevision 物化源码，不复用 Interactive Sandbox 状态。
- **FQP-GOAL-003**：Receipt 绑定 exact tree、profile、镜像、命令、输入、日志和覆盖结果，且不可变。
- **FQP-GOAL-004**：每个 Must Obligation 必须由至少一个实际通过的 Oracle 覆盖，覆盖率必须为 100%。
- **FQP-GOAL-005**：Candidate Receipt 只决定 Proposal 是否可进入 review；Canonical Receipt 只决定 Release 是否可构建/发布。
- **FQP-GOAL-006**：任何 accepted subset、tree、BuildContract、TemplateRelease 或 verifier profile 改变都使旧 Receipt 不可复用。
- **FQP-GOAL-007**：错误、超时、取消和 flake 历史 append-only；重试创建新 Attempt，不改写旧结果。
- **FQP-GOAL-008**：用户在前端能看到当前阶段、exact identity、阻塞检查、日志和明确下一步。

### 3.2 本阶段非目标

- 不允许 Agent 或 Interactive Sandbox 签发最终 Receipt。
- 不执行任意用户 shell 字符串；只能执行 approved TemplateRelease 的 argv command 或平台内建 verifier。
- 不在首个实现中支持任意语言、任意数据库或任意容器编排。
- 不把 Candidate Verification 的 build output 直接发布或提升为 Canonical BuildArtifact。
- 不允许 warning、flake 或模型解释覆盖 blocker。
- 不在 Receipt、日志或浏览器 DTO 中保存 Secret value。

## 4. 两类权威质量对象

| 对象 | 输入权威 | 决定的动作 | 禁止用途 |
|---|---|---|---|
| CandidateVerificationReceipt | exact CandidateSnapshot tree | `candidate.freeze` / Proposal 进入 review | 发布、生产晋升、证明 Canonical Revision 质量 |
| CanonicalQualityReceipt | exact approved WorkspaceRevision | ReleaseBundle build / Preview deploy | 证明不同 Candidate 或不同 Revision 通过 |

二者可以共享 VerificationPlan、只读依赖缓存和相同 verifier images，但必须分别执行并保存独立 Receipt。Candidate 的 `passed` 字段不能复制为 Canonical 结果。

## 5. 核心对象

### 5.1 VerificationProfile

VerificationProfile 是由平台版本化、可激活的质量策略，不属于项目可编辑源码。

~~~json
{
  "schemaVersion": "verification-profile/v1",
  "id": "fullstack-react-fastapi-postgres-v1",
  "version": 1,
  "profileHash": "sha256:...",
  "supportedTemplateRoles": ["web", "api"],
  "verifierImages": [
    {"role": "node", "image": "registry.example/quality-node@sha256:..."},
    {"role": "python", "image": "registry.example/quality-python@sha256:..."},
    {"role": "playwright", "image": "registry.example/quality-browser@sha256:..."}
  ],
  "builtInChecks": [],
  "limits": {},
  "networkPolicy": {},
  "hiddenTestBundle": null,
  "state": "active"
}
~~~

不变量：

- `profileHash` 包含完整 JSON、verifier image digest、平台 verifier binary/version 和内建规则版本。
- staging/production 中所有 image 必须为 registry digest，不接受 tag、local image ID 或隐式 latest。
- profile 更新创建新版本/hash；旧 Receipt 仍可读但不能冒充新 profile 结果。
- hidden-test bundle 只保存 immutable encrypted reference/hash，不进入 Agent ContextPack 或用户终端。

### 5.2 VerificationPlan

Plan 由以下 exact 输入编译：

~~~text
scope (candidate | canonical)
+ project
+ CandidateSnapshot tree 或 WorkspaceRevision
+ BuildManifest id/hash/root lineage
+ ApplicationBuildContract id/hash
+ FullStackTemplate id/hash
+ ordered TemplateRelease ids/content hashes/subject hashes
+ VerificationProfile id/version/hash
+ ordered obligations/oracles
+ runtime policy
~~~

Plan 至少包含：

- 按 FullStackTemplate role 排序的 service roots。
- 每个 service 的 TemplateRelease、toolchain image 和 mount path。
- 依赖准备命令、验证命令、迁移命令和 health check。
- Oracle ID → command/check 的确定性映射。
- check DAG、超时、资源、network、artifact 和 log 上限。
- PostgreSQL 临时 schema/database、Redis namespace 和 Provider fake 策略。
- hidden test refs；返回给浏览器的 DTO 只能显示 opaque suite ID/hash。

Plan 是 immutable content object，`planHash` 必须由 canonical JSON 计算。数据库只保存 bounded metadata 和 content reference。

### 5.3 VerificationAttempt

状态机：

~~~text
queued -> claimed -> materializing -> preparing -> running
running -> collecting -> passed | failed | error
queued/claimed/materializing/preparing/running/collecting -> cancelled | timed_out
terminal -> retry(new Attempt, explicit reason)
~~~

约束：

- claim 使用 lease + fencing token；旧 worker 不能写回。
- claim/reclaim 必须在同一事务内为 exact Attempt fence 建立持久 cleanup obligation；normal Receipt 只能引用 cleanup 已完成的 Attempt。
- `failed` 表示检查正常执行且发现 blocker；`error` 表示平台/基础设施无法形成判断。
- retry 必须有非空原因并创建新 Attempt；Receipt 保留所有 Attempt 引用和最终选择理由。
- 相同 Idempotency-Key + 相同 request hash 恢复同一 Run；相同 key 不同 hash 返回 409。

### 5.4 VerificationReceipt

~~~json
{
  "schemaVersion": "verification-receipt/v1",
  "id": "uuid",
  "scope": "candidate",
  "projectId": "uuid",
  "subject": {
    "candidateId": "uuid",
    "candidateSnapshotId": "uuid",
    "candidateVersion": 4,
    "journalSequence": 2,
    "treeHash": "sha256:..."
  },
  "buildManifest": {"id": "uuid", "contentHash": "sha256:..."},
  "buildContract": {"id": "uuid", "contentHash": "sha256:..."},
  "fullStackTemplate": {"id": "uuid", "contentHash": "sha256:..."},
  "verificationProfile": {"id": "...", "version": 1, "contentHash": "sha256:..."},
  "plan": {"id": "uuid", "contentHash": "sha256:..."},
  "attemptIds": ["uuid"],
  "checks": [],
  "obligationCoverage": [],
  "mustCount": 1,
  "mustPassedCount": 1,
  "blockerCount": 0,
  "warningCount": 0,
  "decision": "passed",
  "payloadHash": "sha256:...",
  "createdAt": "RFC3339"
}
~~~

Receipt 必须记录的 check 事实：

- stable check ID、kind、service/command ID、依赖检查 ID。
- exact argv、working directory 和 verifier image digest。
- start/end/duration、exit code、timeout/cancel/flake 次数。
- stdout/stderr bounded blob refs、content hashes、truncation 和 redaction count。
- diagnostics、artifact refs、SBOM/vulnerability/secret summary。
- Oracle IDs、Acceptance Criterion IDs 和 Obligation IDs。

最终决策只有 `passed`、`failed`、`error`；`passed` 必须满足：

1. exact subject、lineage 和 profile 仍有效。
2. 所有 required checks 实际执行且通过。
3. Must Obligation 覆盖率 100%。
4. blocker、unknown reference、secret leak 和 unapproved migration 均为 0。
5. 所有输出/日志 refs 已 finalized 且 content hash 可验证。
6. 所有 required check 的 stdout/stderr evidence 均未截断；截断表示未捕获尾部不可判断，必须形成 `error` 和 `output_truncated` blocker，而不能依赖进程 exit code 放行。
7. `attemptIds` 中每个 exact Attempt fence 的持久 cleanup obligation 均为 `completed`；内存 defer 或进程退出不构成替代证据。

## 6. VerificationPlan 编译规则

1. 读取 exact ready ApplicationBuildContract，不从原始文档猜测检查。
2. 读取 contract obligation projections；`status != ready` 的 Must 直接阻塞编译。
3. 每个 Must Obligation 的每个 Oracle 必须解析为：
   - approved TemplateRelease command；或
   - VerificationProfile 内建 check；或
   - exact hidden suite case。
4. Oracle command ID 不存在、role/mount 不唯一、TemplateRelease policy 非 approved 或 image 非 digest pin 时 fail closed。
5. DAG 必须无环，依赖闭包必须完整，排序使用 stable ID 而非 map/数据库偶然顺序。
6. Candidate scope 禁止 release/promotion 命令；Canonical scope 才能捕获可发布 artifact。
7. profile/runtime 不能扩大 TemplateRelease 声明的 writable roots、ports、environment 或 network。

## 7. 首个 Full-stack Profile

首个目标组合固定为 React + FastAPI + PostgreSQL，不同时支持任意栈。

建议目录：

~~~text
apps/web/
services/api/
contracts/openapi.yaml
packages/api-client/
deployment/
~~~

检查顺序：

1. `source-policy`：路径、mode、binary、Secret、License、lock 和 generated-output policy。
2. `contract-schema`：OpenAPI/Data/Auth/AI/Deployment Contract schema。
3. `dependency-prepare:web`：只挂载 lockfiles，受限 registry network，禁用 install scripts。
4. `dependency-prepare:api`：hash-locked Python dependencies，受限 index network。
5. `lint/type/unit:web`。
6. `lint/type/unit:api`。
7. `openapi-drift`：server routes、generated client 和 exact ContractRevision。
8. `migration:empty`、`migration:upgrade`、`migration:repeat`。
9. `integration:auth-tenant-permission`。
10. `services:start` 与 health/readiness。
11. `contract/hidden acceptance`。
12. `playwright`：loading/empty/error/ready/streaming/cancelled 和关键业务路径。
13. `axe/runtime-console/network-policy`。
14. `sbom/vulnerability/container-policy`（Canonical scope required）。

依赖准备和检查容器默认无网络；只有 resolver step 可访问精确 allowlisted HTTPS registry。测试服务网络与平台 control plane、Docker daemon、metadata、生产网络完全隔离。

## 8. 数据库与内容存储

本规范定义以下逻辑表族；当前实现已在 migration 000039–000046 中按 Candidate/Canonical scope 拆分为独立表和约束，migration 000051 补充两类 check/Receipt 的 output-truncation gate，migration 000052 再增加 exact-fence cleanup obligation。物理表名不必与下列概念名完全一致：

- `verification_profiles` / `verification_profile_versions`。
- `verification_plans`。
- `verification_runs`。
- `verification_attempts`。
- `verification_receipts`。
- `verification_check_results`。
- `verification_obligation_coverage`。
- `verification_execution_cleanups`：Candidate/Canonical 共用，主键为 scope + Attempt + fence，状态为 `registered/pending/cleaning/completed`。

关键约束：

- exact FK 优先使用 `(id, content_hash)`；所有 subject 与 project/lineage 必须同项目。
- Receipt、Plan、Attempt terminal facts、check result 和 coverage insert 后 immutable。
- `scope='candidate'` 必须填 CandidateSnapshot/tree/fences 且 WorkspaceRevision 为空；Canonical scope 反之。
- passed Receipt 的 `must_passed_count = must_count`、`blocker_count = 0`。
- Candidate/Canonical check 均不允许 `status='passed' AND truncated=true`；deferred Receipt gate 在事务提交时再次拒绝 passed Receipt 关联 required truncated check。
- Run、Attempt 和 exact-fence `registered` cleanup 必须在 claim/reclaim 的同一事务完成；四个 DEFERRABLE claim guard 在 commit 时拒绝旧滚动升级 writer 的拆分写入或漏写。
- Candidate/Canonical Receipt 插入前，数据库要求其每个 `attempt_id` 对应 exact fence cleanup 已 `completed`。
- Proposal 的 Candidate source 必须引用 exact passed Candidate Receipt；数据库 deferred constraint trigger 在事务提交时重验 receipt/subject/freeze identity。
- Canonical `quality_runs` 在迁移期保留兼容读取；新写入最终由 Canonical Verification Receipt 投影，不能出现两套可发布权威。
- 日志、Plan 和 Receipt 正文写 content store；SQL 事务只引用 pending object，提交后 finalize，Reconciler 按 SQL 可达性保留。

## 9. API 契约

### 9.1 Candidate Verification

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/sandbox-sessions/{sessionId}/verification-runs` | 对 exact latest checkpoint 创建/恢复 Run |
| GET | `/v1/verification-runs/{runId}` | 获取状态、allowedActions 和 bounded summary |
| GET | `/v1/verification-runs/{runId}/checks` | 分页获取检查结果 |
| GET | `/v1/verification-runs/{runId}/events` | cursor 分页；高频事件可复用独立 WSS stream |
| POST | `/v1/verification-runs/{runId}:cancel` | version/fence + reason |
| POST | `/v1/verification-runs/{runId}:retry` | terminal run + 必填 reason + Idempotency-Key |
| GET | `/v1/verification-receipts/{receiptId}` | 获取 immutable Receipt |

创建请求必须包含：

~~~json
{
  "projectId": "uuid",
  "candidateId": "uuid",
  "checkpointId": "uuid",
  "expectedSessionVersion": 3,
  "expectedSessionEpoch": 1,
  "expectedCandidateVersion": 4,
  "expectedWriterLeaseEpoch": 1,
  "verificationProfile": {"id": "...", "version": 1, "contentHash": "sha256:..."},
  "reason": "Verify before proposal review"
}
~~~

服务端返回集合字段一律为 `[]`，map 一律为 `{}`；前端不得直接信任 nullable 历史 payload。

### 9.2 Freeze 变更

`POST /v1/sandbox-sessions/{sessionId}:freeze` 增加 exact：

~~~json
{
  "verificationReceiptId": "uuid",
  "verificationReceiptHash": "sha256:..."
}
~~~

门禁顺序：

1. 先按 freeze Idempotency-Key 查 committed receipt/proposal，允许原结果恢复。
2. 对新请求重验 Session/Candidate/checkpoint/fences。
3. 重验 Candidate Verification Receipt 为 passed，subject tree/checkpoint/lineage/profile 完全一致。
4. 在原子 freeze 事务中把 Receipt identity 写入 Proposal source 和 freeze receipt。
5. 任一不一致返回结构化 409/422，Candidate 仍 active 且不产生半成品 Proposal。

### 9.3 Canonical Quality

现有 `/quality-runs` 在兼容期保持路径不变，但响应增加 exact profile、plan、receipt 和 lineage。Publish 最终只接受 passed Canonical Receipt ID/hash，不接受浏览器提供的 `passed=true` 或不同 Revision 的旧 run。

## 10. 前端体验

Candidate 工具栏状态：

~~~text
editing -> checkpoint required -> verification running
verification failed -> inspect/fix/retry
verification passed -> Create Proposal
proposal created -> read-only + review queue
~~~

要求：

- Verify 与 Create Proposal 分开显示；不能用一个长请求隐藏检查阶段。
- 每个按钮同时满足本地安全预判和服务端 `allowedActions`；服务端仍是唯一权威。
- autosave 只前进 Candidate journal，不重新加载 Blueprint/文档，也不自动启动 Verification。
- literal search 只针对当前 exact Candidate head；前端先严格解析 DTO 并核对 project/candidate/generation/root/query/options 的请求身份，350ms debounce 后发起请求，并取消被新 query、save、rebase 或 head mutation 取代的请求。
- dirty、saving 或 Candidate mutation 期间暂停搜索与 match 打开。服务端返回 `409 repository_search_head_changed` 时，前端清除 stale result、只刷新所选 Session/Candidate/tree 投影，再基于新 exact head 搜索；不得覆盖 dirty editor，也不得重新加载 Blueprint、PageSpec、Prototype 或 BuildManifest。
- ordinary file open 必须发送 Session epoch、Candidate ID/version/journal sequence、writer lease epoch、tree hash 与 expected file hash，并逐项验证响应 fence；`sandbox_file_head_changed` 或 CORS 不可见响应 header 时不得把 bytes 放入 Monaco。
- Candidate tree 前进后旧 passed Receipt 显示 `stale`，Create Proposal 立即关闭。
- 检查面板显示 exact tree/profile、进度、blocker、日志截断和 retry reason；不得显示 Secret 或 hidden-test 正文。
- 刷新/换浏览器后从服务器恢复 Run/Receipt，不以 localStorage 为权威。
- freeze 成功后保持当前 tree 可读、关闭编辑/Agent/PTY/process/checkpoint，并明确下一步是 Proposal review/apply。

## 11. 安全与隔离

- Quality worker 使用独立队列、service account、namespace/runtime class 和 egress policy。
- API/Agent/用户终端均不能传入 image、argv、mount、network 或 Secret value。
- source mount 只读；临时输出和 dependency cache 分离，cache key 包含 lock/toolchain/profile hash。
- resolver 容器只看到 lock/manifest，不先挂载源码；禁用 lifecycle/install scripts。
- test runtime 只获得短时、最小权限的 fake/ephemeral service credentials。
- 日志写入前执行流式 Secret 扫描和脱敏；发现 Secret 本身是 blocker，不能仅靠遮盖后继续 passed。
- Preview/Production、平台数据库、Mongo、Redis、NATS、Docker socket、cloud metadata 和 host filesystem 默认不可达。
- symlink、device、socket、setuid、capability、fork bomb、磁盘/日志/端口耗尽必须在物化或 runtime policy 阻止。

## 12. 并发、恢复与幂等

- Verification 读取 immutable checkpoint；用户可继续编辑 active Candidate，但新 tree 不影响正在运行的 Attempt。
- Run 完成时若 Candidate head 已前进，Receipt 仍保存但投影状态为 stale，不能 freeze 新 head。
- worker lease 到期后新 fence 接管；旧 worker 的 check/result/finalize 全部拒绝。
- worker 在成功 claim 后必须用 bounded `WithoutCancel` cleanup context 覆盖所有返回路径，包括 cancel、lease loss 和 transition error；清理目标必须绑定 project/run/Attempt/fence，而不是仅绑定 Attempt。
- materialize/prepare/cleanup 通过跨进程 Attempt lock 与 runtime-fence marker 串行；旧 fence 只能删除自身 workspace 和带 exact Attempt+fence label 的容器，只有仍拥有 marker 且不存在更新 fence 时才能删除 attempt-wide network。
- active Candidate cancel 将对应 obligation 转为 due `pending`，确认执行已 quiesced 后可立即领取；普通失败按 bounded backoff 重试。cleaner claim 使用 `SKIP LOCKED`、version 与 lease epoch，崩溃后可接管，旧 lease completion fail closed。
- Canonical `queued` Run 若 policy inactive 且从未产生 Attempt、cleanup 或 runtime，由 system reconciliation 直接 `cancelled`；已 claim 的 inactive execution 必须等待 lease 过期、exact cleanup `completed` 后再收敛，不能制造无资源 cleanup 事实。
- 同 request key 同 hash 返回同一 Run；不同 hash 返回 `idempotency_conflict`。
- cancel 与 complete 竞争由版本/fence CAS 决定，不能同时形成 passed 和 cancelled terminal facts。
- content finalize 失败返回可恢复的 `content_not_ready`；数据库已提交事实不得重复创建。
- WebSocket 仅传进度；最终状态必须通过 GET snapshot/receipt 验证。

## 13. 分阶段代码落点

### FQP-1：Receipt 基础与 Candidate 门禁

- 新建 `backend/internal/verification/`：domain、plan、store、service、worker contract。
- 新增 PostgreSQL migration：immutable run/attempt/receipt/check/coverage。
- 暴露 Candidate Verification API 和 typed frontend client。
- Proposal/freeze receipt 增加 exact Candidate Verification binding。
- 前端加入 Verify→inspect→Create Proposal 状态。

退出条件：真实 PostgreSQL 覆盖 checkpoint→verification→freeze→review→Revision，tree/receipt/hash 全部相同；失败/stale receipt 不产生 Proposal。

### FQP-2：多服务执行

- 从 approved FullStackTemplate/TemplateRelease 编译命令 DAG。
- 新增 digest-pinned Python 和 Playwright verifier image 配置。
- PostgreSQL ephemeral environment、migration/health/contract checks。
- Node/Python resolver 分离与只读 cache。

退出条件：认证 fixture 可执行 web/api/database profile；任一服务、migration、tenant 或 contract failure 均阻塞。

截至 2026-07-18 的实施状态：FQP-1 数据库/API/UI/worker/freeze gate 已完成 migration 000039–000042，并通过真实 PostgreSQL 的 checkpoint→verification→freeze→review→Revision canary。FQP-2 已实现 Plan v2 的 Node/Python toolchain、lock/Registry、固定 resolver argv 和 cache identity，resolver/source/cache 隔离、Attempt 级 PostgreSQL、internal service network、health，以及 migration/tenant/contract fail-closed 矩阵。本轮 opt-in 真实 Docker fixture 使用 digest-pinned Node/PostgreSQL images，通过 health、连续两次幂等 migration 和隔离 contract check，结束后无残留 fixture container/network；但它不是 approved Golden TemplateRelease 或生产 verifier attestation，在 Golden release 和真实 Playwright corpus 到位前仍不签署 FQP-2 退出。

### FQP-3：Obligation 与 hidden tests

- Oracle→check 稳定映射和 Must coverage 投影。
- hidden suite broker、日志保密和 conformance。
- flake/retry 统计与 policy。

退出条件：Must 覆盖率不是 100% 时数据库不能形成 passed Receipt。

截至 2026-07-18 的实施状态：Oracle→check→Obligation coverage、Must 100% 门禁、Attempt append-only/fence/retry reason 和 Receipt 投影已在 Candidate/Canonical 两套状态机中完成并有 PostgreSQL canary。加密 hidden suite broker、跨租户分发和组织级 flake policy 仍等待 FQP-DEC-OPEN-003/004，不能由默认代码替代决策。

### FQP-4：Canonical Quality 与 Release handoff

- 复用 Plan 编译但从 immutable WorkspaceRevision 重新执行。
- 兼容迁移现有 `quality_runs`，建立 Canonical Receipt 单一发布权威。
- 输出 ReleaseBundle builder 所需 service/migration/SBOM refs。

退出条件：Publish 只接受 exact passed Canonical Receipt；Candidate Receipt 永远不能发布。

截至 2026-07-18 的实施状态：migration 000043–000046 已完成 Canonical Plan/Run/Attempt/Receipt、完整 ReleaseBundle、PreviewReceipt、PromotionApproval、ProductionReceipt、DeploymentRevision 和 immutable rollback。ReleaseBundle 必须包含 deployable artifact、migration、runtime config schema、health/readiness contract、SBOM、vulnerability report、provenance 与 signature；Preview 的 passed 回执必须覆盖 migration/health/smoke/contract/e2e，Production 的 passed 回执必须覆盖 health/rollout。Publish/rollback 同时在服务层和 PostgreSQL 绑定 exact Bundle/Receipt/Workspace/Manifest/artifact；真实 PostgreSQL canary 已覆盖通过、健康失败、终态绕过、篡改和回滚。migration 000048 又增加 `(project, environment)` current-head projection、单个 nonterminal Run 和 exact expected-head CAS；真实 PostgreSQL 定向 canary 已覆盖旧 head 回填、stale 拒绝、双副本 single-flight、healthy 前 CAS、stale CAS 零行和历史 Revision 不覆写。

migration 000056 进一步把 Release Controller v3 交付设计为可恢复状态机：Run 与 canonical request Operation 在首次网络请求前同事务提交，之后只追加 `submit`/`reconcile`/`resubmit` Attempt、observation 和唯一 terminal Result。PUT 超时进入 `submit_unknown/reconcile_wait`，新 lease owner 必须 GET 同一 ID/hash；404 只能让未承认 Operation same-ID/hash resubmit，已承认操作的历史丢失、identity/hash/result 冲突则进入 `reconcile_blocked`。Provider 在发送 Token/变更前做正常 PKI + leaf SPKI pin，readiness 校验 exact Controller id/version/v3 protocol/trust digest。v2 PreviewReceipt/ProductionReceipt/DeploymentRevision 必须持有该 Operation/Result exact ref，legacy v1 不能补造 authority。UI 在 `reconcile_wait`/`reconciling` 告知“正在确认控制器结果，禁止重新发布”，`reconcile_blocked` 则要求运维对账。

上述 migration 56/Provider/Worker/UI 是仓库内控制面契约；其最终回归必须单独证明同 key 恢复、timeout-after-commit、lease takeover、404 same-ID/hash resubmit、conflict quarantine、v1 回填和 v2 Result binding。当前没有已部署、已资格化的真实 Controller 或目标 cluster 黑盒证据，所以即使仓库内回归通过也不能签署 FQP-4/Stage 4 外部退出。外部 Release Controller 未配置时 capability 明确关闭，UI 不提前开放操作。

### 跨阶段完整性补强（2026-07-18）

- migration 000047 已增加 Sandbox activity projection、idle/absolute TTL、deadline lease/fence/takeover/retry worker；exact epoch 的 stream activity 会延长 idle deadline，dirty Candidate 在 idle suspend 前先 checkpoint。
- migration 000049 在数据库提交边界串行 Candidate journal 与所有关联 nonterminal SandboxSession，只有 `ready` 可写；`failed`/`terminated` 是不阻塞 successor Session 的审计历史。真实 PostgreSQL 定向竞争测试同时覆盖 suspend 先提交的零写入失败和 terminal-history→新 `ready` Session 可继续写。
- Constraint Compiler v7 已调用 Core 共享 strict semantic authority，对 exact RequirementBaseline→Blueprint Page→PageSpec→Prototype 的 Must/AC、canonical states、fixture/interaction/data binding/trace、API ownership/permission/role 做闭包，并将 Blueprint API ID/method/path 与 exact API Contract 双向比对，拒绝 Contract 多出的 cross-slice operation。AI Runtime v2、API Contract v2、Data Contract v2 与 `reference-ai-conversation/v1` profile 又对 Provider Port/Gateway、九分支 typed RunEvent、project-scoped composite foreign key、四个持久实体、11 个 SessionAuth API、idempotency、六状态/三断点 presentation evidence、exact deployment environment allowlist 和 15 个一对一 executable blocking Oracle 做跨合同 fail-closed；历史 v1 仍可读取，新 BuildContract 不再接受。新增 `template-deployment-runtime-closure/v1` 从 exact FullStack/TemplateRelease authority 读取可信 Manifest/Layout 投影，严格校验可由 Deployment v1 表示的 mounted service/output、port、health、单 migration 与 required environment；identity、集合、链接、跨 release 冲突及无法表示的 HTTPS/多 service/多 output 都会阻塞。Health method/status、port exposure、environment default 与 Layout 不被误称为双向等价。历史 BuildContract 可继续审计，但 downstream ready gate 只接受当前 v7 compiler identity，必须重新编译活动 BuildManifest。完整 bundle 的 deterministic ready hash 已由 `backend/internal/contracts/reference` 固定。`go test ./internal/contracts/... ./internal/core ./internal/constructor -count=1` 已通过。这是质量 Plan 前的内部输入权威门禁，不是 Golden 运行证据或真实 AI 应用闭环。
- v7 是 BuildContract mutation contract phase，不是旧 writer 可共同写入的 expand 阶段。部署方必须先 drain v6 API/generation/implementation mutation 节点；未提供旧 writer 排空证据时，构造和下游 mutation 必须保持关闭，不能把当前二进制的 current-identity 校验外推成混跑安全性。
- Canonical materializer 保留 `100644`/`100755` 并以 `0400`/`0500` 只读物化；Content Reconciler 保留 Candidate/Canonical quality 与 Release delivery 全链路 content refs，部分迁移时 fail closed。
- Browser IDE 已支持 Candidate rename/delete、binary 元数据/原字节下载和显式 Candidate abandon；binary 不会被当作 UTF-8 文本编辑。abandon 会先等待保存并为 dirty Candidate 创建 exact checkpoint，migration 000050 原子提交 Candidate terminal event 与 Session mutation fence，runtime 清理失败由持久化 `abandon_cleanup` lease 后台接管。不得再把 binary/rename/delete/abandon 列为当前 gap。
- Candidate search 已实现 generation/root 请求围栏与 normalize → view authorization → query admission → Candidate Repository I/O 顺序。migration 000062–000065 提供 PostgreSQL immutable exact-tree literal index、durable single-builder claim、原子 project quota 与 project-scoped composite GIN；默认 quota 为 `16 trees / 256 MiB / 2 active builds`。app 使用同一个 Redis authority 驱动 search 与 secure `BuildForActor`；query project `20/s burst 40` + actor `4/s burst 8` 覆盖 index、short/no-trigram 和 glob，first-builder project `1/15s burst 2` + actor `1/30s burst 1` 只允许 durable claim 的首次实际 owner 在 resolve 前扣 token，ready reuse/waiter/follower 不扣。malformed/outage 与 quota/rate/tamper 不得 fallback。index 只给出受限候选，服务仍对 opening tree、authoritative raw bytes/content hash 和 closing head 做最终重验；binary skip 与 `truncated` 语义不变。浏览器使用严格 DTO/request identity 校验、350ms debounce/cancel 和 match-open fence；只有 head-changed `409` 刷新 Candidate/Session/tree 投影，quota `409` 与 `503` 保持 blocked；bounded `Retry-After` 按 exact identity 最多自动重试一次，不覆盖 dirty editor 或 Blueprint/PageSpec/Prototype。000066 已以 `implemented-internal` 落地 retention/GC；该索引不是 Git pack/global symbol index，后续变更不得弱化 exact-head/content-hash 围栏或 Golden/LSP-4 诚实边界。
- ordinary file read 已实现完整 request/response head + file fence、opening/closing recheck、`409 sandbox_file_head_changed` 和默认 CORS allow/expose；Agent patch/evidence 还要求对 raw bytes 重算 `X-Content-Hash` 并核对 `X-Content-Object-Hash` / patch 元数据。配置校验会把漏掉这些 header 的 `CORS_ALLOWED_HEADERS` / `CORS_EXPOSED_HEADERS` 覆盖作为启动错误 fail closed，不能回退为裸 GET。
- Repository search 的 PostgreSQL quota 与 Redis admission 是安全准入而不是性能提示。默认 quota 为 `16 trees / 256 MiB / 2 active builds`；query 默认 project `20/s burst 40` + actor `4/s burst 8`，first-builder 默认 project `1/15s burst 2` + actor `1/30s burst 1`。short/no-trigram/glob bounded scan 也必须先通过 query admission；只有 durable claim 的首次实际 owner 扣 first-builder token。quota/rate denial、malformed admission result、Redis unavailable/timeout/corrupt state、index tamper 或数据库异常全部 fail closed，不得以 bounded scan 掩盖。合法 query/first-builder denial 返回 `429 + Retry-After`；active-build quota 返回 `429`，retained tree/source-byte quota 返回 `409`，未知 index failure 返回 `503`。
- Redis admission、exact-tree GC/retention migration 000066 和独立 `repository-index-gc` 已完成主线内部实现，当前只能标记为 `implemented-internal`。GC policy 固定 `retention >= 7d`（默认 30d）、`keep >= 8`（默认 8）、`batch 1–100`（默认 25）、`capability TTL <= 15m`（默认 10m）；任意 Candidate status 的 current tree 与 live claim 都保护目标。execute 在 exact tree/project quota advisory lock 下锁住 claim row，再用锁后数据库时间重判 lease 并重验完整 publication CAS；先提交续租必受保护，排在 GC 后的续租不能在 row 被删除后报告成功。它只删除无剩余 member 引用的 blob，并用 append-only capability/receipt/tombstone记录 `deleted/protected/stale/expired`。两个 canonical short auth table 只保存本事务 xid/backend/project/tree/capability/blob 删除事实，成功前清空、失败同事务回滚；down 在销毁对象前排他锁定全部 GC control/audit/auth 表，任一事实非空即拒绝回滚。
- PostgreSQL 对外边界是十个 application-only Candidate/build-claim `SECURITY DEFINER` 函数和四个 operator-only GC plan/execute/inspect/readiness 函数；application 无 GC-private table/GC EXECUTE，operator 无直接对象权限或 Candidate definer EXECUTE。`000066` 在任何 mutation 前拒绝 partial/elevated stable-role trio、stable role 出向 membership、入向 `ADMIN OPTION` 和任意 trusted-schema column ACL；通过后才撤销 trusted schema 全部 PUBLIC relation/sequence/routine ACL，把 schema、全 tables/sequences 和 23 个受控 routines 精确归属 NOLOGIN migration-owner，并仅向 application 重授 predecessor `SECURITY INVOKER` 执行面。第 23 个是 exact-signature Sandbox checkpoint dependency：SQL/STABLE `SECURITY INVOKER`、单 boolean、固定路径，且只向 migration-owner 与 application 授予不可转授的执行权；八个 internal trigger/guard routines 则严格 owner-only。API 与 operator 分别检查真实 `session_user=current_user` LOGIN 及所有 inherited/`SET ROLE` reachable roles、role delegation、零 column ACL、schema/database/object ownership、ACL、签名和返回 contract。API、migrator、operator DSN/schema 分离；API 只读核对 exact schema head，不执行 migration。ledger 保留 up checksum 并为每个 canonical down pair 单列 SHA-256；legacy NULL 只能由 migrator在 exact ordered prefix 上一次性建基线。生产三个 NOLOGIN group role 必须在 000066 前存在，否则 all-absent 本地姿态下的 conditional grants 不会随之后建角色而重跑；只能补 reviewed migration，不能改旧 checksum。
- `000068` 新增第四个隔离的 `worksflow_golden_fault_operator` NOLOGIN group、两张 append-only one-shot reservation/result 表和两个 owner-only `SECURITY INVOKER` guard。该 role 必须在迁移前外部预置，只获得 schema `USAGE` 与两表非转授 `SELECT, INSERT`；API/application 保持零表权限。严格 direct-DSSE verifier、独立 fault-operator trust、动态 resource/head/fence、CAS/read-after-unknown 和 closed adapter registry 已接入 Golden Fixture artifact index 与 immutable Qualification verifier，并闭合 authority/reservation/result/consume-receipt/run-ledger 证据。仓库仍没有任何真实 fault adapter 或外部产物，不能据此声明 22 个 case 可执行或通过。
- 真实 PostgreSQL + Redis focused Repository admission/index 回归已通过（95.205s）；focused GC migration、staging/production API posture和真实低权限 operator LOGIN 的 interrupted same-run recovery也有对应 canary，另有 focused `go vet`/unit/race 与完整前端 typecheck/unit/lint/production build 证据。生产原有三个 group/LOGIN 必须在 `000066` 前、第四个 Golden fault group/LOGIN 必须在 `000068` 前外部预置；dedicated schema/database ownership、完整 app DML 与 secret injection 也仍须目标部署提供。本地 Compose shared owner 不是生产角色资格。
- Production LSP Control Plane v1 的 LSP-0–3 已形成仓库内实现：专用 ticket/WSS、exact subprotocol/fences、Redis grant/rate/editor lease、PostgreSQL authority/audit、immutable runtime snapshot、digest-pinned read-only runtime、strict Gateway/method adapter 与 Monaco binding/providers 均已接线。Go 单元/集成、真实 PostgreSQL + Redis 定向验证和前端 typecheck/unit/lint/build 自动化已通过；这些只构成 `implemented-internal` 证据。approved Golden real language-server 的真实 ingress/WSS/browser qualification（LSP-4/QA-016）仍未完成。
- abandon 成功后 Workbench 只清除旧 Session/Candidate/pending-save/verification/runtime 本地指针，并从相同受治理输入发现或创建 clean successor；Blueprint、PageSpec、Prototype 不会因该操作重新加载。服务端也不再广告没有完整路由/UI 消费的 `view_logs`、`restore_checkpoint`、`new_session`、`view_audit`、`view_snapshots`。
- migration 000051 将 output truncation 设为 Candidate/Canonical 双重 fail-closed：executor 和 worker 输出 `error + output_truncated blocker`，领域 Receipt 构造器与数据库 check/deferred gate 拒绝 `passed + truncated`，即使被丢弃尾部未出现于 Secret 扫描样本或进程 exit code 为 0 也不放行。
- Candidate/Canonical worker 在 exact claim 后对 normal/cancel/lease-loss/error 路径统一执行独立 bounded cleanup；Attempt lock、fence-qualified workspace、runtime marker 和 exact Docker labels 保证旧 fence 不会清理新 owner，共享 network 只有当前 marker owner 可删除。Canonical release-artifact collection 同样受 heartbeat 保护。
- migration 000052 将上述清理变为持久 exact-fence obligation：claim/reclaim 同事务注册，DEFERRABLE guards 令旧 writer fail closed，normal Receipt 必须 cleanup completed；active cancel、lease loss、cleanup retry/takeover 均可恢复，Canonical queued + inactive policy 则只允许零资源 system-cancelled 收敛。
- 真实 51→52 upgrade matrix 证明 live generation 回填 `registered`，expired/terminal/receipted generation 保守回填 `pending`，不制造 `completed`；双 cleaner 只有一个 claim，crash 后新 lease epoch 接管且 stale completion 被拒，non-empty down rollback 成功。Candidate 与 Canonical Receipt 数据库 canary 均覆盖 cleanup 未完成拒绝、完成后接受。
- Agent Runner request/execution v3 的 budget 边界只能记录为保守 input upper-bound 准入、requested/output usage evidence，以及超出 `maxCommands` 时的 active cancel。Agent Runner 单测、backend Agent 真实 Redis 定向测试和 app/transport 回归已通过；该 upper-bound 不是 tokenizer 精确计数，且未经 credential-backed live Provider 与 registry-admitted Runner image qualification，不能写成生产通过。
- Agent/Release 的本地部署接线已经补强：Compose 完整透传 Release Controller 与 Agent opt-in 配置，API/DinD 共享 exact Agent worktree，预加载器在嵌套 daemon 中建立 internal-only Runner network 和 exact-image relay，两跳 relay 只暴露 Responses 路径；真实临时 DinD canary 已覆盖预加载、网络、relay health/connectivity 和禁用清理。executor identity 目前严格限定为 `codex-cli + openai`。这些是本地 topology/identity 证据，尚无真实 Provider、approved Runner digest 或浏览器 AgentAttempt qualification。
- Runner build contract 强制 Go/Node base digest、精确 Codex SemVer 和 npm sha512 SRI，并在本地使用 Codex CLI `0.144.6` 完成两个非 root 镜像、版本断言及 Sandbox PTY/Preview real-Docker canary。前端/后端/relay 第一方应用 base 也已 digest-pinned，mutable override 会在依赖安装前失败；外部 Compose 服务、SBOM/provenance/signature、组织 Registry admission 和目标环境 RuntimeClass/NetworkPolicy 仍未资格化。

本轮前端普通 Playwright 已于 2026-07-18 在分离 Golden entrypoint 后完成：87 passed、0 skipped、0 failed，耗时 3.8m。常规用例继续覆盖 BuildContract fail-closed、Workbench 恢复、应用后继续完成 Workflow 与持久 Candidate 沙盒；Golden 不在普通 suite 的选择集中，独立 strict qualification 尚未执行且当前 fail closed。87 项仓库内浏览器回归不得计作外部资格或阶段退出证据。

包含 migration 000050–000052 的本轮稳定态回归已经补录：真实 PostgreSQL + Redis 环境下 `go test ./... -count=1` 全部通过，完整 verification race suite 通过，`go vet ./...` 通过；前端 unit、lint、严格 TypeScript typecheck、production build 均通过。digest-pinned backend image build 通过，image 内非 root 用户可执行 Docker CLI 与 `podman-remote`；Podman 必须显式提供受校验的 daemon host，并通过 `CONTAINER_HOST` 连接。上述真实 Docker fixture 也通过且无残留 container/network。

Candidate exact-head search 合入后，前端严格 TypeScript typecheck、unit、lint 与 production build 已于 2026-07-18 再次全绿。该四项与上述普通 Playwright 结果只验证当前前端代码与协议消费；未执行的 approved Golden Stack qualification 仍是未满足的独立资格边界。

最终 backend full regression 已在真实 PostgreSQL + Redis 环境执行 `go test ./... -count=1 -timeout 30m` 并以 exit 0 全绿；跨包 migration-lock 竞争下关键包耗时为 `core 896.686s`、`release 437.353s`、`repository 614.687s`、`sandbox 458.522s`、`migrations 846.769s`。隔离执行的完整 migrations 包另以 448.286s 通过；全仓 `go vet`、全包编译、focused race、production/maintenance Compose render 与最终非 root backend image build 也全部通过。该结果是仓库内控制面与持久化实现的回归证据，不构成 approved Golden Stack、真实 Release Controller 或目标部署环境的资格证明。

## 14. 验收矩阵

| ID | 场景 | 必须证据 |
|---|---|---|
| FQP-E2E-001 | exact checkpoint 创建 Run | Session/Candidate/checkpoint/fence/profile hash |
| FQP-E2E-002 | 同 key 重试 | 同一 Run，零重复行/对象 |
| FQP-E2E-003 | independent materialize | file bytes/mode/tree 与 CandidateSnapshot 一致 |
| FQP-E2E-004 | 多服务检查 | exact argv/images/exit/log refs |
| FQP-E2E-005 | Must coverage | obligation/oracle/check 闭包 100% |
| FQP-E2E-006 | passed Receipt freeze | Proposal/receipt/subject exact binding |
| FQP-E2E-007 | accepted Proposal Apply | WorkspaceRevision tree 等于 Candidate tree |
| FQP-E2E-008 | Canonical rerun | 新 Receipt 绑定 exact WorkspaceRevision |
| FQP-E2E-009 | Release handoff | Candidate Receipt 被拒，Canonical Receipt 被接受 |
| FQP-E2E-010 | Preview/Production Controller handoff | v2 Run 与 canonical v3 Operation 先持久，terminal Result 以 exact hash 进入 v2 Receipt/Revision |
| FQP-E2E-011 | unknown outcome reconciliation | timeout-after-commit 后用同一 ID/hash GET 恢复，不创建第二个远端变更 |
| FQP-E2E-012 | exact-head Candidate literal search/retention | normalize→view auth→query admission 位于 Candidate Repository I/O 前、请求 generation/root、000062–000065 index/single-builder/quota/project-GIN、默认 quota、同一 Redis authority 覆盖 index/short/no-trigram/glob、secure `BuildForActor` 只有实际 claim owner 的 first-builder charge、malformed/outage/tamper fail-closed、rate `429 + Retry-After`、active quota `429`、retained quota `409`、unknown index `503`、authoritative-byte/binary/closing-head recheck、严格 DTO/request identity、bounded exact-identity 单次重试、quota/outage no-refresh、match-open generation/root/content hash；000066 需另证 retention/keep/batch/capability 界限、全状态 Candidate/live-claim、exact CAS/lock、shared blob、short auth、receipt/tombstone、三角色 ACL 与 same run-id recovery |
| FQP-E2E-013 | exact ordinary Candidate file read | 七个 request headers、opening/closing head recheck、完整 response fence/CORS expose；漂移为 `409 sandbox_file_head_changed` 且旧 bytes 不进入 Monaco |
| FQP-E2E-014 | Production LSP v1 read-only binding | 专用 ticket/WSS、统一 head/document fences、exact server identity/capability、stale-drop/reconnect/undo、Candidate-CAS-only save 与真实 Golden server evidence |
| FQP-E2E-015 | immutable runtime build contract | Go/Node/app base 均为 exact digest、Codex exact SemVer+SRI、non-root version smoke；mutable/range/tag/SRI 漂移在依赖安装前失败 |
| FQP-E2E-016 | qualification artifact hygiene | acceptance→test→artifact 唯一映射、零 mock/skip、短时凭证、credential-safe trace/video 和不可变 Receipt；Bearer/Secret 不得进入可分发证据 |

失败链路至少覆盖：

- stale checkpoint/tree/profile/BuildContract/TemplateRelease。
- missing/failed/foreign-project Receipt。
- mutable/unapproved image、unknown command/oracle、DAG cycle。
- resolver 越界、network denied、Secret、symlink/device、output exhaustion/truncation（包括 exit 0 的截断输出）。
- migration/health/contract/tenant/Playwright failure。
- cancel/complete race、worker fence takeover、cancel/lease-loss exact-fence cleanup、pending content recovery。
- 51→52 upgrade 的 live/expired/terminal/receipted 回填、双 cleaner、crash takeover/stale completion、non-empty down rollback。
- 55→56 upgrade 将 legacy v1 nonterminal Run 保守转为 `reconcile_blocked`，不伪造 Operation/Result；v2 Run 必须与 exact Operation 同事务提交。
- Controller PUT timeout/disconnect、GET reconcile、404 same-ID/hash resubmit、accepted/running 后 404、request/result/identity 冲突隔离、lease takeover 和 verifying-only finalize。
- TLS 正常 PKI 通过但 leaf SPKI pin 失配时，必须在 Token/请求 body 发出前 fail closed。
- nullable collections 和刷新/跨浏览器恢复。
- Candidate search 请求 head 已 stale、索引/扫描期间 head 漂移或打开 match 前 generation/root/content hash 改变时必须 fail closed；closing drift 返回结构化 409，旧结果不得渲染或打开。
- exact-tree index 缺失只能对同一个 opening Candidate tree 首次按需构建；000063 claim 必须在 FileBlob resolve 前选出唯一 owner，000064 必须原子执行默认 `16 trees / 256 MiB / 2 active builds` quota，000065 必须使用 project-scoped composite GIN。所有 index/short/no-trigram/glob 请求均按 normalize→view auth→query project+actor admission→Candidate Repository I/O 执行，secure `BuildForActor` 只有首次实际 claim owner 再通过 first-builder project+actor admission。non-ready、quota/rate denial、malformed admission result、Redis outage/timeout/corrupt state、tamper、commitment mismatch、foreign member 或数据库异常必须 fail closed，不能静默扫全仓；bounded scanner 只能由 short/no-trigram/glob 形态选择，index 返回的每个候选仍须通过 opening tree membership 与 authoritative raw-byte/hash 复验。
- 000066 必须覆盖 plan 后 Candidate 任意状态引用插入/切换、live claim 新建/续租、rank/cutoff/publication/commitment 漂移、capability 过期、shared blob 仍被引用、duplicate execute 和 response-loss/crash。只有 exact-CAS `deleted` 可改变 index；其余必须形成零删除的 immutable `protected`/`stale`/`expired` receipt。删除 guard 缺任一 xid/backend/project/tree/capability/blob short-auth 字段均拒绝，事务结束不得遗留 auth row。
- 任一生产 NOLOGIN group role 在 000066 前缺失、API/migrator/operator 共用 LOGIN/DSN、`session_user != current_user`、schema/database 可写/owned、unexpected role reachability、direct operator table grant、API GC EXECUTE、函数 owner/search-path/signature/RETURNS TABLE 漂移，都必须使 API 或 operator readiness fail closed。conditional grants 未安装时只能新增 reviewed migration；禁止编辑旧 migration/checksum、删除 `schema_migrations` 记录或手工执行表 DELETE。
- Candidate search malformed/missing/widened DTO 必须被严格拒绝；binary 必须跳过并计数，不能按 UTF-8 搜索。dirty/saving 时不得搜索或打开；409 refresh 只更新 Candidate/Session/tree 投影，不覆盖 editor draft 或任何 governed document。
- ordinary file read 任一 request header 缺失/非法、response Candidate ID/journal/head/content hash 不一致、opening/closing race 或自定义 CORS 漏 allow/expose header 时，必须 fail closed；不得以 content hash 相同为由忽略 head 漂移。
- LSP ticket replay/Origin/subprotocol 漂移、任一 head/document fence stale、server identity/capability 漂移、forbidden write method、恶意/超限 DTO、Redis/runtime failure 和 reconnect dirty conflict。

### 14.1 Production LSP Control Plane v1 内部测试与外部资格矩阵

本节验证 `ai-constructor-architecture.md` 12.6 的控制面。LSP-0–3 已实现，并已执行对应 Go 单元/集成、真实 PostgreSQL + Redis 定向验证和前端 strict DTO/session/Monaco provider 自动化；这覆盖的是 LSP-QA-001–015 的仓库内实现证据，状态只能是 `implemented-internal`。LSP-QA-016 尚无 approved Golden profile、真实 ingress/credential/server/browser 故障矩阵，未执行且不能由 Monaco、Problems、literal search、ordinary file read、fake server 或普通 Playwright 自动勾选。

测试向量必须固定 WSS subprotocol `worksflow.sandbox-lsp.v1` 以及四个 strict wire schema：`sandbox-lsp-ticket/v1`、`sandbox-lsp-connection/v1`、`sandbox-lsp-binding/v1`、`sandbox-lsp-envelope/v1`。任何 `workspace/applyEdit`、`workspace/executeCommand`、rename 或跨文件 edit 都属于无条件失败样本，不能因 Template profile 声明而改成允许。

| ID | 层级 | 场景 | 必须证据 |
|---|---|---|---|
| LSP-QA-001 | schema/unit | `SandboxHeadFence` 每个字段 missing/null/alias/widened/unknown/边界值 | strict parser 与 server validator 同样拒绝；`version` 不接受 `candidateVersion` 别名 |
| LSP-QA-002 | schema/unit | `DocumentFence` 与 canonical `worksflow-candidate:` URI | traversal、file/untitled URI、非 canonical escape、旧 open ID/model version/saved hash 全部拒绝 |
| LSP-QA-003 | HTTP + Redis | ticket RBAC/Origin/head/profile/mode/TTL 及并发 consume | 同一 secret 恰好一次成功；过期、replay、Redis outage fail closed 且无 raw secret log |
| LSP-QA-004 | WSS transport | TLS、exact subprotocol、bind deadline、ticket burn 后 head 二次漂移 | Upgrade/bind 结构化拒绝；失败 ticket 不能重放或降级到通用 WS |
| LSP-QA-005 | PostgreSQL integration | 八个 head 字段逐个 stale、same-session 单调 CAS successor 与 epoch/lease/Candidate rotation | 仅经 Repository authority 验证的 successor 可 head rebind；其余 4409 + fresh ticket |
| LSP-QA-006 | runtime supply chain | TemplateRelease/profile/image/executable/serverInfo/init/config/capability hash 漂移 | binding fail closed 并记录 identity finding；不从 workspace/PATH/tag 回退 |
| LSP-QA-007 | malicious server | advertise/发送 applyEdit、executeCommand、rename、format、code action、dynamic registration、cross-file edit | Gateway 不转发，read-only mount 零文件变化，violation audit/termination 符合策略 |
| LSP-QA-008 | method adapter | diagnostics/hover/navigation/semantic/inlay/safe completion malformed、unknown URI、oversized/extra fields | 按 method strict sanitize；只输出 allowlist 内 bounded DTO |
| LSP-QA-009 | concurrency | autosave CAS 与 diagnostics/completion/definition response 交错 | 只应用 exact request head + DocumentFence；旧结果 deterministic stale-drop |
| LSP-QA-010 | write authority | 用户接受 completion、CAS success/conflict/lease loss | 唯一持久写是现有 Candidate CAS；冲突零覆盖，LSP 零 applyEdit/隐式重试 |
| LSP-QA-011 | Monaco/browser | head rebind、socket loss、server crash/restart、clean/dirty remote change | 存活 model URI/open ID/model version/undo 保留；不 dispose/`setValue`；dirty 进入显式 conflict |
| LSP-QA-012 | tenant/security | foreign project/session/Candidate URI、old connection/binding sequence、Origin change | 数据/diagnostic/navigation 零泄漏；旧连接不能污染 successor Session |
| LSP-QA-013 | rate/resource | ticket/request burst、frame/document/result/diagnostic 上限、CPU/memory/PID/tmp/startup/request timeout | exact effective limits 可观测且 fail closed；autosave/PTY/通用 WS/bounded search 继续可用 |
| LSP-QA-014 | audit/privacy | issue→consume→bind→rebind→request→stale/violation→close | 可重建 identity/fence/method/outcome，且无 ticket、源码、unsaved text、diagnostic/completion正文或 Secret |
| LSP-QA-015 | lifecycle | Session suspend/resume/freeze/abandon/terminate 与 writer lease rotation | runtime 按 fence 退出；旧 process/connection 永远不能回写或重绑 |
| LSP-QA-016 | external Golden | approved exact TemplateRelease + digest-pinned real language server，经真实 ingress/WSS/浏览器和故障注入执行参考项目 | 0 skip/0 mock 的 qualification receipt，包含 server/image/capability hash、浏览器录像/trace 和资源/安全结果 |

ordinary file read 作为 LSP document open 前置还必须单独覆盖：请求 `X-Sandbox-Session-Epoch`、`X-Expected-Candidate-ID`、`X-Candidate-Version`、`X-Candidate-Journal-Sequence`、`X-Writer-Lease-Epoch`、`X-Candidate-Tree-Hash`、`X-Expected-File-Hash` 逐项缺失/非法，expected file hash 不匹配，opening/closing head race，响应 `X-Candidate-ID`/`X-Candidate-Journal-Sequence`/head/content hash 漂移，以及默认与自定义 CORS preflight/expose。任何失败都不得构造 `DocumentFence`。

实施/签署顺序：

| Gate | 可开始条件 | 退出证据 | 当前状态 |
|---|---|---|---|
| LSP-0 Contract/Admission | schema、method baseline 和 Template profile 审阅 | canonical vectors、URI/fence/schema tests；仍不宣称 runtime | 已内部实现并自动化验证 |
| LSP-1 Authority/Ticket | LSP-0 approved | LSP-QA-001–005 的 unit、真实 PostgreSQL + Redis、race/Origin/subprotocol 证据 | 已内部实现；真实 PostgreSQL + Redis 定向验证通过 |
| LSP-2 Runtime/Adapter | exact digest profile 可用 | LSP-QA-006–010、恶意 server 与只读 filesystem/egress 证据 | 已内部实现；runtime/adapter/security 自动化通过 |
| LSP-3 Monaco | strict browser DTO/client ready | LSP-QA-011–015 与普通 edit/autosave/search 非回归 | 已内部实现；前端 typecheck/unit/lint/build 自动化通过 |
| LSP-4 Production Qualification | approved Golden release、真实 ingress/credential/runtime | LSP-QA-016 通过且零 skip；此前只能写 implemented-internal，不能写 production-qualified | 未执行；缺 approved Golden profile/endpoint/credential/real-server receipt |

`LSP-QA-001–015` 全绿最多证明仓库内控制面实现；只有 `LSP-QA-016` 的真实、approved Golden language-server evidence 才能签署 production LSP 资格。mutable local image、协议 fixture、fake server、普通 Playwright pass 或明确 skip 均不可升格。

## 15. 当前外部阻塞与诚实声明

即使 FQP-1 至 FQP-4 代码完成，下列条件满足前仍不能签署 Stage 3 退出：

1. `ai-worksflow/templates` 尚无符合 admission v1 的 approved Golden React/FastAPI release。
2. 已用 exact base digest、Codex CLI `0.144.6` 和 package SRI 构建 Agent/Sandbox 两个非 root 本地镜像并完成运行 canary；但尚无组织 Registry 中批准的 digest、SBOM、漏洞结论、签名、provenance/attestation，Verifier 组合也仍未批准。
3. `frontend/tests/golden-stack.spec.ts` 目前只是 health、Message create/replay/read、跨租户拒绝和浏览器 save/reload 的 partial smoke；普通 Playwright 明确排除它，独立 `qualification:golden` 在完整 executable coverage、不可变 Receipt signing 和 credential-safe trace pipeline 到位前 fail closed，不以 skip 冒充通过。它尚未覆盖 Template bootstrap、Sandbox、Agent、完整 AI stream/retry、Verification、Release 或 LSP-QA-016；加密 hidden-test corpus 也尚未批准和接入。
4. 当前真实 Docker fixture 只证明本轮固定组合；真实 Podman daemon、远端 daemon delayed mutation/fault injection、共享验证卷跨副本 advisory `flock`/atomic rename、生产 RuntimeClass/NetworkPolicy 仍需目标环境资格验证。
5. 真实 Release Controller deployment/qualification、Preview/Production cluster、health/rollout 观测和 canary/blue-green/rollback 数据面尚未配置；migration 000048 的 current-head single-flight/CAS 与 migration 000056 的持久 Operation 对账只能证明平台 authority，不证明外部部署成功、已通过资格验证或已获批准。
6. Registry/KMS、Secret Broker、credential-backed live model/provider、registry-approved runner/verifier image 与供应链 attestation 尚未资格化。
7. Production LSP v1 的 LSP-0–3 已有专用 Gateway/runtime/Monaco 内部实现和真实 PostgreSQL + Redis/前端自动化证据；尚无可用于资格运行的 approved TemplateRelease language-server profile、目标 ingress/credential、digest-pinned real server 或 LSP-QA-016 browser qualification receipt，因此不能升格为 production-qualified。
8. 当前平台边界共有五组稳定 `NOLOGIN` role；production-posture 主检查器必须在同一有界窗口持有 API、migrator、只读 auditor 与 Qualification Promotion operator 四条独立 `LOGIN`/DSN/连接，GC 与 Golden fault operator 的实际凭据则由各自 focused 检查和外部运行证据独立覆盖。目标生产环境仍未提交这五组 role、上述连接/专用凭据、dedicated schema/database ownership、完整应用 DML、secret 注入和周期调度/same-run 对账的资格证据。CredentialSet 与 qualification-evidence 的 PostgreSQL Store 均保持 owner-only，没有增加第六个运行角色或新的生产 DSN；本地 Compose owner 不能替代专用 operator 和目标环境证据。

测试 fixture、fake sandbox、单元测试、容器内 Go 回归和临时 PostgreSQL canary 可以证明代码不变量，但不能替代上述外部资格证据。

`qualification/manifest.json` 已将本文与主架构文档的每个验收 ID 唯一映射到内部测试层或外部计划套件及必须 artifact；`make qualification-check` 会拒绝缺失、重复、无测试路径或把 incomplete external suite 标成 qualified 的变化。当前机器结果仍是 `0 external suites qualified`，因此清单本身不是 Receipt。Golden Bearer 只允许从权限收紧的短时 token file 读取；在可靠脱敏/加密与撤销证据实现前 trace capture 被预检主动拒绝，不能把含 Authorization 的 trace 归档。

仓库内现已实现 external-only Receipt 验证边界：root-owned 短时 authority 精确绑定 project/workflow run/node/target Revision、source/evidence immutable snapshot、Git 与 verifier executable digest；artifact index 对额外明文和 symlink fail closed，trace/video/unstructured evidence 必须有 KMS 加密与 plaintext disposition attestation，凭据撤销必须先于独立 runner/approver DSSE Receipt 签发。该实现只验证已形成的证据，尚未提供真实 Golden capture/encrypt/sign/seal 服务，也没有下游 `(exact target, nonce, authority digest)` append-only 单次消费 ledger；因此当前外部状态仍为零通过，且 ModelProfile/生产 PostgreSQL 等独立治理门禁不能由此 Receipt 替代。详见 `qualification-promotion-operator.md`。

`internal/qualificationevidence` 已将“冻结 exact 非秘密 Plan → 原子签发完整 CredentialSet → capture closure → restricted artifact 加密与明文处置 → KMS attestation → exact same-set revoke → artifact index → 双签 Receipt → immutable seal → read-only reopen/verify”实现为严格的 v1 内部生命周期；每个 mutation 都先向 Store 追加 `*-started`，未知结果只允许用相同 operation ID 和 canonical request digest 调用 `Inspect`。该 Receipt→snapshot tail 现在只允许历史 replay，不能作为新的外部格式继续签发。`internal/qualificationreceiptv3` 已增加纯 Go snapshot-first canonical/DSSE verifier：先 seal 并独立验证不能包含 Receipt 的 snapshot，再让 Receipt 以它为唯一 subject；验证只接受 opaque authority/receipt ID，并通过 server resolver取得 exact expected payload，同时把两个实际 Ed25519 key/identity/role 的 canonical policy digest与冻结 trust policy绑定。公共 Evidence `Execute` 只接受 opaque Plan Authority UUID，并重验 authority/hash/artifact/exact Evidence Plan bytes/TrustBindings；migration `000074` 与 `qualificationplanauthority.PostgresStore` 又把 server-resolved input/projection/trust/target/envelope 变为 owner-only durable raw/hash/JSONB/scalar 闭包。与 migration `000073` 的 Evidence event/CAS Store 一样，这关闭的是内部持久化、自认证 Plan 和纯验证规则，不是生产资格运行。生产闭环仍缺 Receipt v3 durable request/observation/terminal Store、真实 immutable ExpectedResolver、production InputAuthority、专用 Plan/Evidence/Receipt operator/login/DSN、终态失败后的 durable abort→exact revoke、能够证明 `not-invoked` 并安全续跑的 authority 协议、一次性交付 claim/ACK，以及真实 capture、Broker、KMS/HSM、signer、seal/verifier adapters；详见 `qualification-evidence-orchestrator.md`、`qualification-plan-authority.md` 与 `qualification-receipt-v3.md`。

当前 Candidate search 已包含 migration 000062–000065 的 durable immutable index、single-builder claim、原子 project quota 与 project-scoped composite GIN；同一个 Redis runtime authority 已执行 query 与 actual-owner first-builder admission，前端也已实现 bounded `Retry-After` 单次重试与 quota/outage no-refresh。000066 的 bounded retention/GC、三角色 SECURITY DEFINER 边界和独立 same-run operator 已内部实现并通过 focused real-PostgreSQL canary，但生产 role/login/schema/full DML/secret/scheduler 仍无目标环境资格证据，因此整体仍只能写 `implemented-internal`。上述能力不是 Git pack/object-store 或 global symbol/reference index，也不等同于 LSP-4，不能替代 approved Golden Stack 浏览器闭环、LSP-QA-016、真实 Controller deployment/qualification 或目标环境证据。

## 16. 需要显式批准的后续决策

- **FQP-DEC-OPEN-001**：生产 Quality RuntimeClass 采用 gVisor、Kata 还是 Firecracker。
- **FQP-DEC-OPEN-002**：Python lock 标准采用 uv lock、pip-tools hashes 或组织私有策略。
- **FQP-DEC-OPEN-003**：hidden-test bundle 的加密、分发和租户隔离实现。
- **FQP-DEC-OPEN-004**：flake 阈值、最大 retry 和 warning promotion policy。
- **FQP-DEC-OPEN-005**：Receipt、完整日志、SBOM 和中间 artifact 的保留期与成本上限。

这些开放项不允许通过代码默认值悄悄决定生产策略；在进入对应实现阶段前必须形成 DecisionRecord。
