# Golden Qualification Control Plane v2

Status: reviewed implementation contract; external qualification has not been
executed and no stage-exit revision exists.

本文定义 AI 构造器从真实 Sandbox、Agent、Reference AI Application、Release
Controller 与 Production LSP 形成外部资格证据时所需的控制面。它补充
`ai-constructor-architecture.md`、`full-stack-quality-profile.md` 与
`qualification-promotion-operator.md`；Handoff 之后默认关闭、尚未激活的 profile-v3
发布闭环以
[Workflow Execution Profile v3: Qualified Release Runtime Contract](./workflow-execution-profile-v3-runtime.md)
为规范输入。本文不把仓库内单元测试、fixture、普通
Playwright、Docker smoke 或内部 PostgreSQL canary 写成生产资格结论。

## 1. 目标与信任边界

Golden qualification 必须同时证明：

1. 被测对象是 exact source、BuildContract、approved TemplateRelease 和真实
   部署身份，而不是本地替身；
2. 22 个浏览器/服务场景全部执行，零 mock、零 skip、零 retry、零 flaky；
3. 所有动态 Session、Candidate、Attempt、Run、Ticket 与 LSP binding 都由上一步
   的真实响应产生并逐级追溯，不能预造；
4. 故障由一次性、签名且有界的 fault authority 驱动，不能通过测试专用后门扩大
   权限；
5. 运行使用的全部短时凭据作为一个原子集合签发和吊销，不能只吊销其中一个；
6. trace、video、log 等敏感证据先加密并完成明文处置，再形成精确 artifact
   index，封存并独立验证不含 Receipt 的不可变快照，最后生成双角色签名 Receipt；
7. 最终 Receipt 只授权 exact project/workflow node/immutable revision 的
   external-qualification gate，下游以 append-only CAS 单次消费。

信任关系如下：

```text
root-owned Golden Authority
  -> hash-binds Golden Fixture v2
  -> pins run/plan/expiry and non-secret runtime facts

Golden Fixture v2
  -> pins deployments, principals, profiles, credential-set membership
  -> pins fault-authority references and shared immutable artifacts

runtime responses
  -> create dynamic IDs and fences
  -> chain exact request/response evidence through every scenario

Qualification Plan Authority v1
  -> freezes Golden document bytes, exact target, evidence plan and trust
  -> pins atomic credential-set expectation before execution

Qualification Input Precommit Authority v1 (migration 000080)
  -> binds exact current WIA + Policy + Plan source/credential authorization
  -> retains distinct source-verifier and credential-resolver receipt admissions

Qualification Receipt wire v3
  -> sole in-toto subject is exact verified pre-Receipt snapshot digest
  -> signed by independent runner and release approver

Promotion consumption v2
  -> re-locks exact WIA, Policy, Plan, typed inputPrecommit, Evidence, Receipt
     and current canonical review
  -> atomically creates one pending immutable-revision handoff

workflow handoff consumer
  -> creates the exact immutable revision and leaves Publish waiting_input

qualified release authority (migration 000084; implemented-internal, not activated)
  -> proves parent/output same-content equivalence and binds authenticated ActionPublish

Release Controller v3
  -> replays one stable operation through queued -> healthy
  -> appends one healthy result before Publish and Workflow complete
```

Golden Authority 与 Fixture 是 root-owned、hash-bound 输入，不伪装成独立签名
结论。Plan Authority、source verifier、credential resolver、credential issuer、KMS、
runner、approver、Promotion v2 消费账本与 workflow handoff consumer 共同组成信任链；
各角色不得复用同一公钥、稳定身份或 executable digest。输入预提交是系统固定必需
authority，不属于 Policy 可配置的 independent-authority 列表。历史 wire-v2 Promotion
Authority/Receipt/快照顺序只允许只读验证，不能作为新流程的写入协议。

## 2. Golden Authority v2

Authority 文件采用 canonical JSON、UTF-8、无 BOM、无重复字段、无未知字段，必须
是 root/current service owner 持有的 single-link regular file。建议 exact shape：

```json
{
  "schemaVersion": "worksflow-golden-authority/v2",
  "subject": {
    "authorityId": "<uuid-v4>",
    "issuance": "root-issued-hash-bound",
    "issuedAt": "<RFC3339 UTC>",
    "expiresAt": "<RFC3339 UTC>",
    "runId": "<uuid-v4>",
    "planDigest": "sha256:<hex>",
    "fixtureHash": "sha256:<canonical fixture.subject>"
  }
}
```

`expiresAt - issuedAt` 必须为 2–30 分钟，qualification preflight 时至少剩余 2
分钟。Authority document digest 由 actual bytes 计算；Fixture 顶层的
`authorityHash` 必须等于 canonical authority subject hash，Authority 的
`fixtureHash` 必须等于 canonical fixture subject hash。该双向关系必须无环，不能
把整个互相引用的 document bytes 递归求 hash。

## 3. Golden Fixture v2

### 3.1 顶层结构

Fixture 同样采用 strict canonical JSON。建议 exact shape：

```json
{
  "schemaVersion": "worksflow-golden-fixture/v2",
  "authorityHash": "sha256:<canonical authority.subject>",
  "subject": {
    "fixtureId": "<uuid-v4>",
    "runId": "<uuid-v4>",
    "planDigest": "sha256:<hex>",
    "issuedAt": "<RFC3339 UTC>",
    "expiresAt": "<RFC3339 UTC>",
    "platform": {},
    "credentialSet": {},
    "principals": [],
    "sharedArtifacts": {},
    "sandbox": {},
    "agent": {},
    "release": {},
    "lsp": {},
    "reference": {},
    "faultAuthorities": []
  }
}
```

`runId`、`planDigest` 与有效期必须和 Authority 完全一致。所有 map key、数组顺序、
ID、digest、URL origin、image digest、profile、artifact reference 均采用严格规范
形式；`null`、未知字段、mutable image tag、相对 origin、URL path/query/fragment、
重复值和隐式默认全部拒绝。

### 3.2 Origin 与服务拓扑

Fixture 必须分开声明：

- `platform.webOrigin`：用户 Workbench/Browser IDE 的 HTTPS origin；
- `platform.apiOrigin`：平台 REST/WSS API 的 HTTPS origin；
- `reference.webOrigin`：Generated Reference Application 的 HTTPS origin；
- `reference.apiOrigin`：Reference Application API 的 HTTPS origin；
- `agent.modelGateway`：平台内部受控 model route 的 identity/profile/attestation，
  不是浏览器 origin；
- `release.controller`：内部 Controller identity、image digest、protocol 与 trust-key
  digest，不是浏览器 origin；
- `lsp.gateway`：平台 API origin 下固定 `/v1/sandbox-lsp` ticket/WSS 边界；
- `lsp.runtime`：内部只读 language-server image/profile/capability hash。

所有对外 origin 必须互不混淆。Qualification 只允许 HTTPS；loopback HTTP 仅可用于
明确标注的 partial smoke。远程明文 `http://<ip>:<port>` 既不能保护 body/header，
也通常没有浏览器 `crypto.subtle`，不得作为 Golden entrypoint。

`platform` 还必须绑定 deployment receipt、server build/version、API schema 与 WSS
protocol digest。`reference` 必须绑定 exact application contract bundle、deployment
receipt、web/api image digest、migration identity、typed RunEvent schema 与 retention
policy。动态进程、Session 或 Run ID 不属于根 Fixture。

Fixture v2 的 Reference 上游身份闭包已属于 `implemented-internal`。它在根签发的
`subject.reference` 中精确固定 `reference-deployment-runtime-receipt/v1` payload kind、
独立 Reference Gateway SPIFFE identity、route、attestation/capability digest、
secret-injection receipt、`reference-project-default` provider policy（profile pinned、
fallback forbidden）和独立 ModelProfile（3 attempts、120000 ms timeout）。Reference
profile/provider 不得复用 Agent Model Gateway，Reference Gateway 也不得复用 credential
issuer、runner、LSP 或 Release identity。web/api/migration/retention 的 direct argv、command
identity 和 working directory 是 closed values；shell/launcher、空白或控制字符、path
traversal、重复 command identity 均 fail closed。Retention、rate-limit policy 及六项
qualification operation set 也进入 Fixture subject hash；operation set 的 canonical
commitment 是
`sha256:936f995189a3e6c89c740b6d693c4ba7e8b73b67db61bbf907f9c7fe8be0a2f8`，覆盖
migration rerun、Run execution observation、timeout vector、retention job、rate-limit
observation 与 Reference audit observation。JS preflight 和 Go receipt verifier 对
unknown/missing/null、schema/hash drift、authority reuse 与 operation-set tamper 使用同一
exact shape；Fixture 不承诺任何 gateway URL、secret 或动态运行 ID。

### 3.3 Principals 与认证面

Principal 是 actor/tenant/project/role 的非秘密身份。至少包含两个不同 tenant/project
边界的用户 A/B，以及 owner、admin、fault operator。Platform 与 Reference 使用不同
认证面：

- Platform browser A/B 使用仅限 `platform.webOrigin` 的 Secure HttpOnly session
  storage state；mutation 使用 CSRF；
- Platform API A/B、owner/admin/fault operator 使用 audience-bound Bearer credential，
  不带浏览器 Origin；
- Reference browser A/B 使用仅限 `reference.webOrigin` 的独立 SessionAuth storage
  state；
- Reference API A/B 使用独立、cookie-backed `APIRequestContext`，不能拿 Platform
  Bearer token 代替；
- LSP/terminal/preview connection ticket 由已认证平台调用按需签发，single-use 且
  不进入 Fixture。

Browser A 与 B 必须是两个独立 `BrowserContext`，Reference context 不能复用
Platform cookie jar。storage state 和 token 文件必须是 mode `0600`、single-link、
non-symlink、root/current runner owner；文件、inode 与 credential material 均不得
复用。

### 3.4 原子 credential set

Fixture 只保存非秘密成员承诺：

```json
{
  "setId": "<uuid-v4>",
  "issuer": "<issuer identity>",
  "audience": "<golden stack audience>",
  "credentialSetHandleHash": "sha256:<opaque set handle>",
  "memberBindingsDigest": "sha256:<canonical sorted members>",
  "memberCount": 11,
  "issuedAt": "<RFC3339 UTC>",
  "expiresAt": "<RFC3339 UTC>",
  "issuerAttestationDigest": "sha256:<issuance DSSE payload>",
  "memberBindings": [
    {
      "slot": "platform-browser-a",
      "actorId": "<uuid>",
      "kind": "storage-state",
      "credentialHandleHash": "sha256:<one-way handle>"
    }
  ]
}
```

成员按 `slot` UTF-8 byte order 严格排序。slot、actor/kind/handle 组合必须唯一，所有
handle 也必须唯一；`memberBindingsDigest` 从 canonical complete array 计算，count 必须
精确。签发 DSSE 与吊销 DSSE 均包含相同完整成员数组、set handle、digest/count、
run/issuer/audience/issuedAt/expiresAt；吊销另包含 `revokedAt` 和 `status=revoked`。
任何 partial revoke、成员漂移或第二次使用均失败。

Raw token、cookie、storage state、authorization header、secret broker response 和
credential file path 不得写入 Fixture、predicate、Receipt、日志或证据。Promotion
Authority 与 Qualification Receipt 都必须绑定 Golden authority/fixture document
digest、fixtureId、set handle、member digest/count，以及 issuance/revocation artifact
ID 与 payload digest。

当前 Go `internal/credentialset` 提供了这一协议的内部控制面底座：broker 接口只有
`PrepareSet`、`ActivateSet`、`RevokeSet` 和同 operation ID 的只读 `Inspect`，不存在
逐 member 签发/激活/吊销或非原子 fallback；每个外部 mutation 和签名调用前都先通过
append-only Store/CAS 写入 durable reservation。mutation 或签名结果不确定后只能
Inspect，不能二次调用。服务持久化的只有 set/member 单向 handle hash、完整 binding、
canonical in-toto issuance/revocation DSSE 与状态事件；broker-owned delivery capability
必须自报与 `setHandleHash` 相同的单向承诺，不进入 Store、Event、Snapshot 或 JSON。
Signer 只接收非秘密 payload/PAE 并持有外部私钥。Store 以唯一 event ID 区分 CAS 与
commit-response-lost；`ErrStoreOutcomeUnknown` 后只能用 strongly-consistent `Load` 对账该
exact event ID，未确认时回到外部 `Inspect`，不能重做副作用。事件时间来自 trusted
Store、不得倒退，issuedAt 相对 trusted now 只允许 ±30 秒偏差。

Migration `000072_credential_set_event_store` 与 `PostgresStore` 已把 durable Store 实现到
`implemented-internal`：事件账本与 issue/revoke operation identity 只追加，current head
只能由 owner-only `append_credential_set_event` 受控例程推进；event ID exact replay、set/
version CAS、全局 issue/revoke operation identity、完整状态转换和 snapshot 投影在同一
事务内关闭。Event request、完整 Binding、DSSE Envelope/Payload 都保存有界 canonical raw
bytes 与 SHA-256；SQL 使用 `pgcrypto` 独立重算 hash，逐层拒绝 raw/JSONB 不一致、未知、
`null`、widened field、成员重复/乱序和 member-binding digest 漂移。写入例程先取得账本/
head 锁，再读取 millisecond DB clock，重新执行 ±30 秒 skew、发行有效期和吊销窗口检查；
exact committed event 的重放先于当前时间检查，过时后仍可只读重建。事件与 operation 的
UPDATE/DELETE/TRUNCATE 由 immutable trigger 拒绝，head INSERT/UPDATE/DELETE/TRUNCATE 还需
同事务的私有投影授权；guard 使用固定 trusted-schema search path，不能用 `pg_temp` 同名表
绕过。所有表和入口当前只归 migration owner，PUBLIC 与普通 application 均无权限。

这仍不是生产运行时接线：仓库尚未 provision 独立 CredentialSet operator identity/login/
DSN，也不允许 API 用 migration-owner DSN 代替；真实 atomic Secret Broker、issuer/revoker、
HSM/KMS signer orchestration、外部 issuance/revocation evidence 与运行编排仍不存在。

started-before-call 的恢复边界也必须保持显式：控制面为了禁止重复副作用，会先持久化
`*-started` 再调用 Broker/Signer；若进程恰好在这两步之间崩溃，重试只能 `Inspect` 同一
operation，不能猜测并重发 mutation。生产 authority 必须提供可认证的 `not-invoked`/claim
恢复协议，才能安全地重新取得执行所有权；当前接口没有这一协议，因此该故障点会永久
fail closed，是 liveness blocker，不是 exactly-once 成功证明。

delivery response-loss 目前采取 fail-closed 而不是伪称 exactly-once：只有仍持有本次
activation/只读恢复 capability 的调用可以在 issuance attestation 提交后返回它；状态已是
`issued` 的重放不再调用 broker `Inspect`、不再次返回 bearer capability，而是携带公开
binding/attestation 返回 `ErrDeliveryOutcomeUnknown`。这可以防止重复泄露，但无法判断上一次
HTTP/gRPC response 是否到达用户，也不能恢复一个已丢失的 capability。生产闭环仍需独立的
一次性交付 broker + durable delivery acknowledgement/claim ledger；在其实现与外部验证前，
response-lost credential delivery 是明确 blocker。

该包同时复用了通用 1–64 member validator，并由 `NewGoldenService` 强制上述 exact
11-slot/kind/actor-sharing closure、2–30 分钟寿命、run/fixture/audience/issuer closure。
线程安全内存 Store 只用于测试；durable PostgreSQL Store 已内部实现，但尚无生产 operator
接线、真实 atomic broker/issuer/revoker，也没有生成任何目标环境 credential evidence。

### 3.5 资格证据生命周期编排

`internal/qualificationevidence` 已形成 strict v1 internal control contract，按唯一顺序推进：
冻结非秘密 Plan → atomic CredentialSet issuance → exact run/capture closure → 逐个 restricted
artifact 加密并完成 plaintext disposition → 独立 KMS manifest/pre-revocation artifact-set
attestation → exact same-set revocation → artifact index → 双签承诺 Receipt → immutable snapshot
seal → read-only reopen/verify。该 Receipt→snapshot tail 只保留历史 replay；新 external
qualification 必须走 snapshot-first wire v3。所有 mutation 均先 append `*-started`，未知结果只能用同一
operation UUID 和 canonical request digest 执行 `Inspect`；Store commit unknown 只能用 exact
event UUID 强一致对账。事件结构没有自由 metadata/error/path/header 字段，最大 512 个
restricted artifact 的真实状态机测试已覆盖。

Migration `000073` 与 `PostgresStore` 已把该生命周期的 event/operation/head authority 持久化：
四表、四函数和三个 trigger 全部 owner-only，全局预留所有 operation UUID，以数据库时钟推进
事件，保存并重验 canonical request/event raw bytes 与 hash，Load 从 immutable ledger 重放并
比对 guarded head；真实 PostgreSQL 已覆盖完整 20-event 闭环、并发 Create/CAS、重启、
commit-unknown、ACL/search-path 漂移和 non-empty rollback fence。Memory Store 只保留为测试实现。

这仍不建立外部资格事实。当前已有 owner-only immutable Plan Authority Store 与 opaque
Evidence `Execute` resolver，但没有 production InputAuthority 或专用 Plan/Evidence
operator/login/DSN；
started-before-call 仍需要 authority 的可信 `not-invoked` 恢复协议；post-issuance terminal
rejection 也没有 durable abort→exact-revoke 分支，只能依赖短 TTL 或外部 emergency revoker。
真实 Broker、capture、encryptor、KMS/HSM、indexer、双 signer、sealer、verifier 和 target run
均未接入。完整边界与失败语义见 `docs/qualification-evidence-orchestrator.md`。

### 3.6 共享不可变制品

`sharedArtifacts` 至少精确绑定：

- approved TemplateRelease ID/content hash/approval receipt digest；
- BuildManifest 与 BuildContract ID/content hash；
- exact WorkspaceRevision 与 Canonical Quality receipt；
- source repository commit 与 actual-byte content-tree digest；
- Reference application contract bundle；
- approved runner/verifier/controller/language-server image digests、SBOM、signature 与
  provenance references。

ID 与 digest 必须成对存在。`latest`、branch name、mutable tag、只给 ID、只给 URL、
空 receipt 或客户端自行推导均无效。

## 4. 动态身份与证据链

根 Fixture 不能预造运行中才会产生的 ID。每一步必须保存 exact request、response、
ETag/fence 与关联 artifact，下一步只能使用上一步返回值：

| 动态对象 | 创建来源 | 后续必须绑定 |
| --- | --- | --- |
| SandboxSession | approved template bootstrap | session ID/version/epoch、runner digest、opening Candidate |
| Candidate | session bootstrap/mutation | candidate ID/version/journal、base/current tree、writer lease |
| checkpoint | checkpoint response | exact Candidate tree/version/journal 与 artifact receipt |
| AgentAttempt | Sandbox task create | attempt ID/version/fence、task/context/profile hashes |
| merge/undo | exact patch decision | attempt/patch/tree/merge receipt 与 current head |
| Release Run/Operation | exact Bundle submit | controller identity、request hash、Run↔Operation CAS |
| preview/promotion/rollback receipt | Controller result | same Bundle digest、environment、deployment revision |
| LSP ticket/openId | platform ticket/bind | single-use ticket、session/candidate/head/document fences |
| Reference conversation/message/run | Reference API | tenant/principal/session/idempotency、typed event cursor |

浏览器接受 raw file、patch 或 evidence 前必须对 actual response bytes 重算
`X-Content-Hash`；再核对 object digest、ETag、Candidate/Attempt 身份与所有 fence。
缺 header、CORS 不可读、hash mismatch、strict UTF-8/JSON 失败、unknown/null 字段均阻塞。

## 5. 一次性 fault authority

真实故障不能由普通用户输入任意 shell、resource ID 或内部 URL。每个 fault authority
是独立 fault-operator 签名的 DSSE envelope，具有：

```json
{
  "schemaVersion": "worksflow-golden-fault-authority/v1",
  "authorityId": "<uuid-v4>",
  "fixtureId": "<uuid-v4>",
  "runId": "<uuid-v4>",
  "operationKind": "<closed enum>",
  "resourceSelector": "<fixture-bound selector>",
  "expectedFenceDigest": "sha256:<precondition>",
  "issuedAt": "<RFC3339 UTC>",
  "expiresAt": "<RFC3339 UTC>",
  "maxUses": 1
}
```

允许的 `operationKind` 只能是经过审核的闭集，例如 Sandbox dependency crash、Agent
runner crash/timeout/security canary、Controller timeout/404/conflict/maintenance、LSP
runtime crash/drift/resource pressure、Reference gateway outage/process restart。不能存在
`exec`、任意 URL、任意 namespace、任意 SQL 或任意 signal。

由于真实 resource ID 是动态的，服务在消费 authority 时将 selector 解析为当前 exact
resource，并把 resource ID、head/fence、authority digest 写入 append-only consume row。
消费使用数据库 CAS：`unused -> consumed`，`maxUses=1`；失败或超时后按同一 authority
ID 查询结果，不能签发第二个 authority 掩盖不确定提交。故障服务凭据与普通测试凭据
分离，响应本身成为证据链的一部分。

### 5.1 PostgreSQL operator provisioning and `000068` order

`000068_golden_fault_consume_ledger` 的 GRANT 是条件式的。生产/预发必须在运行该迁移
之前，通过特权 provisioning channel 创建第四个隔离 group role；普通 API、migrator 和
GC operator 都不能继承或 `SET ROLE` 到它：

```sql
CREATE ROLE worksflow_golden_fault_operator NOLOGIN NOSUPERUSER
  NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
GRANT USAGE ON SCHEMA worksflow TO worksflow_golden_fault_operator;
-- 真实 fault service LOGIN 只能继承这一个 group，且不能带 ADMIN OPTION。
GRANT worksflow_golden_fault_operator TO worksflow_golden_fault_login;
```

顺序必须是：安全 NOLOGIN group → 专用 LOGIN/secret → `000068` → API/fault-service
posture/readiness。若 `000068` 已在 role 不存在时记入 immutable migration ledger，不能删
ledger 行或重跑/修改 migration。先由特权 operator 对 exact trusted schema 执行以下检查；
任一检查失败都必须用新的 reviewed follow-up migration 修复 owner/ACL，不能直接扩大授权：

```sql
DO $repair_preflight$
DECLARE
  schema_oid oid := 'worksflow'::regnamespace;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname = 'worksflow_golden_fault_operator'
      AND NOT rolcanlogin AND NOT rolsuper AND NOT rolbypassrls
      AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication
  ) OR EXISTS (
    SELECT 1
    FROM pg_auth_members AS membership
    JOIN pg_roles AS member ON member.oid = membership.member
    JOIN pg_roles AS granted ON granted.oid = membership.roleid
    WHERE member.rolname = 'worksflow_golden_fault_operator'
       OR (granted.rolname = 'worksflow_golden_fault_operator'
           AND (membership.admin_option OR member.rolname IN (
             'worksflow_application',
             'worksflow_migration_owner',
             'worksflow_repository_index_gc_operator'
           )))
  ) THEN
    RAISE EXCEPTION 'Golden fault operator role posture is unsafe';
  END IF;

  IF 2 <> (
    SELECT count(*) FROM pg_class
    WHERE relnamespace = schema_oid
      AND relname IN (
        'golden_fault_consume_reservations',
        'golden_fault_consume_results'
      )
      AND relkind IN ('r', 'p')
      AND relowner = 'worksflow_migration_owner'::regrole
  ) OR 2 <> (
    SELECT count(*) FROM pg_proc
    WHERE pronamespace = schema_oid
      AND proname IN (
        'validate_golden_fault_consume_result',
        'reject_golden_fault_ledger_mutation'
      )
      AND proowner = 'worksflow_migration_owner'::regrole
      AND NOT prosecdef
      AND NOT has_function_privilege('PUBLIC', oid, 'EXECUTE')
  ) THEN
    RAISE EXCEPTION '000068 owner/invoker/owner-only routine contract is not exact';
  END IF;
END;
$repair_preflight$;

REVOKE ALL ON TABLE
  worksflow.golden_fault_consume_reservations,
  worksflow.golden_fault_consume_results
FROM PUBLIC, worksflow_application;
GRANT USAGE ON SCHEMA worksflow TO worksflow_golden_fault_operator;
GRANT SELECT, INSERT ON TABLE
  worksflow.golden_fault_consume_reservations,
  worksflow.golden_fault_consume_results
TO worksflow_golden_fault_operator;
```

当前 Go `internal/goldenfault` 已实现严格 canonical direct-DSSE v1 parser、独立
fault-operator trust、append-only reservation/result CAS ledger、read-after-unknown 和
closed adapter registry；资格专用的 [Golden fault consume HTTP boundary](golden-fault-consume-http.md)
只接受 run-scoped fault-operator Bearer，并从服务端 immutable repository 加载完整 authority；
`internal/qualificationreceipt` 已把该底座接入 Fixture、artifact
index 与 immutable qualification evidence verifier。仓库仍没有任何真实 fault adapter，
main API 也未配置真实 credential authenticator/repository，且没有外部 orchestrator 产出的
authority/consume/attestation artifacts。因此这些仍只是
内部验证能力，不能据此把 22 个场景或任一 external suite 标成可执行/已通过。

Golden fault v1 的 `resolvedResourceId` 不是可事后解释的 opaque adapter ID。每个 closed
operation 必须直接写入被作用的 exact business ID：Agent crash/timeout 写 Attempt ID，
Sandbox dependency 写 Session ID，Reference gateway 写 Run ID、process restart 写
Application ID，Controller fault 写 Delivery Operation ID。该值连同 head/fence 进入
`resolutionDigest` 和 append-before-side-effect ledger；测试必须直接与刚进入 non-terminal
的目标比较。额外的 resolution observation 只能辅助诊断，不能替代该 commitment。若未来
adapter 必须使用 opaque ID，则必须升级 Receipt/ledger/schema，把完整 target binding 纳入
canonical digest 并同步升级 Go verifier 与 migration，不能只增加一个读接口。

### 5.2 Immutable fault evidence closure

Artifact index 对故障证据只接受三个 distributable 类型：

- `golden-fault-authority`：Fixture 中每个 `dsse.artifactId` 恰好对应一个
  `application/vnd.dsse.envelope.v1+json` artifact，actual-byte SHA-256 必须等于 Fixture
  的 `envelopeDigest`，payload type/digest 与 direct-DSSE predicate 也必须一致；
- `golden-fault-consume-receipt`：每个 authority 恰好一个 canonical plain JSON Receipt，
  artifact ID 必须等于唯一 `resultId`，并闭合 authority/fixture/run/operation、动态
  resource/head/fence、adapter invocation、reservation/result observations 与时间；
- 整个 run 恰好一个固定 ID `golden-fault-ledger-attestation`。它是 canonical in-toto/DSSE
  envelope，sole subject 是 exact Fixture document digest，predicate 按 `authorityId`
  严格排序并绑定全部 predicate/envelope/payload、canonical reservation、terminal result
  与 consume-receipt digest，以及可信 `reservedAt/completedAt` 和 `terminal` 状态。

Root trust policy v2 必须分别声明 fault-operator authority keys/threshold/allowed identities
与 `fault-ledger-attestor` keys/threshold/allowed identities。它们的 key ID、identity 和
public-key fingerprint 必须与 runner、approver、credential issuer、KMS encryption
authority 以及彼此全局不复用。Qualification verifier 只用已签 ledger entry 的
`reservedAt` 验证短时 authority 的历史有效性；当前检查时间已超过 authority expiry 并不
否定一个当时有效的消费，但 `reservedAt >= expiresAt`、reserved/unknown、缺失、多余、
重复、签名/时间/digest/Receipt 漂移全部阻塞。closed outcome contract 固定为
`agent-security-canary -> refused`，其余 12 个 fault injection operation -> `applied`；
任何相反结果都不能进入 Qualification Receipt。

全量 Golden run 不能由 Fixture 自己决定需要证明哪些故障。Promotion Authority 与
Qualification Receipt 的 `goldenRuntime.faultOperationSetDigest` 必须固定为
`sha256:50add6d13b4b28587f5ceab1385d85e457cc35489a031ac9d2f3ff217bd1fa9d`；它是
`qualification/golden-fault-operation-set.json` 去掉末尾换行后的 canonical JSON
commitment。Verifier 要求 Fixture、authority artifacts、consume receipts 与 ledger entries
恰好覆盖该文件中的 13 个 operation，不能缺少、增加或重复；所有 authority 的
`adapterInvocationId` 在 run 内也必须唯一，与 `000068` 的数据库 `UNIQUE` 约束一致。

历史时间的信任边界是明确的：`reservedAt/completedAt` 由独立 fault-ledger-attestor 对
append-only ledger 作证，DSSE 签名本身不是外部时间戳。为阻止已撤销或已过期的 attestor
私钥事后回填旧 `issuedAt`，该 key 除了在 ledger `issuedAt` 有效，还必须一直有效且未撤销
到 Promotion Authority 独立固定、runner/approver 签名的 trusted Qualification Receipt
`issuedAt`。Receipt 之后发生的正常 key expiry/revocation 不追溯否定已封存证据；若运行方
不信任 attestor 对数据库时间的陈述，仍必须增加外部 transparency/RFC 3161 或等价可信
timestamp，当前仓库不声称已经提供该外部时间服务。

## 6. 22 个 Golden cases

以下是需要写入 v2 inventory 并经评审的稳定 case 集。它是五个业务 suite 的完整
浏览器/服务场景；artifact hygiene 是 post-run verifier closure，不是伪造的第 23 个
Playwright case。

`qualification-inventory.mjs` 对下列五组 ID 和总数 22 做 exact cardinality 检查；增加、
删除、替换或把 case 移到另一 suite 都会失败，而不是悄悄改变资格计划。

### 6.1 Sandbox（4）

| Case | 场景 | Requirement IDs |
| --- | --- | --- |
| `QG-SANDBOX-001` | approved Template bootstrap 与真实 Browser IDE open | `AIC-E2E-003`, `AIC-E2E-004` |
| `QG-SANDBOX-002` | autosave、checkpoint、断线恢复且不重载 Blueprint/dirty editor | `AIC-E2E-005` |
| `QG-SANDBOX-003` | real process、PTY、port 与 health | `AIC-E2E-006` |
| `QG-SANDBOX-004` | Preview 调用真实 API/DB，租户与 fence 正确 | `AIC-E2E-007` |

### 6.2 Agent（4）

| Case | 场景 | Requirement IDs |
| --- | --- | --- |
| `QG-AGENT-001` | exact task → patch → review → merge → undo | `AIC-E2E-008` |
| `QG-AGENT-002` | 两浏览器并发、head conflict 与无静默覆盖 | `AIC-E2E-009`, `AIC-FAIL-009` |
| `QG-AGENT-003` | Secret/Canonical/Deployment 隔离及 malicious patch 拒绝 | `AIC-FAIL-005`, `AIC-FAIL-010`, `AIC-FAIL-011` |
| `QG-AGENT-004` | runner crash、timeout、cancel/retry 与 bounded event recovery | `AIC-FAIL-007`, `AIC-FAIL-008` |

每个被观察的 Agent Attempt 还必须解析 Golden-run-rooted 的 runtime pointer，再按
`id + contentHash` 获取 immutable `agent-runtime-binding-receipt/v1` 原始 canonical
bytes、重算 SHA-256，并验证 strong ETag/immutable cache。Receipt 必须把 Attempt、
TaskCapsule、configuration、实际 executor、runtime process 与 Fixture 中 exact Agent
Runner identity/profile/image 及 Model Gateway identity/profile/provider/model/revision/
attestation 全字段重新闭合；provider invocation 只能给出 UUID request ID 或明确的
`refused-before-provider`。仅检查 Attempt 返回的 provider/model 显示字段不足以证明实际
模型 revision 或 Gateway/Runner authority。

### 6.3 Reference AI Application（6）

| Case | 场景 | Requirement IDs | Contract criteria |
| --- | --- | --- | --- |
| `QG-REFERENCE-001` | operability、service image/command/health | `AIC-E2E-010`, `FQP-E2E-004` | `AC-AI-009`, `AC-AI-010` |
| `QG-REFERENCE-002` | persistence、idempotent create/replay/read | `AIC-E2E-010` | `AC-AI-001`–`003` |
| `QG-REFERENCE-003` | typed SSE、cursor/reconnect/recovery | `AIC-E2E-010` | `AC-AI-004`, `AC-AI-015` |
| `QG-REFERENCE-004` | cancel、retry、timeout terminal state | `AIC-E2E-010` | `AC-AI-005`, `AC-AI-006`, `AC-AI-013` |
| `QG-REFERENCE-005` | A/B tenant isolation 与 redacted audit | `AIC-E2E-010` | `AC-AI-007`, `AC-AI-008` |
| `QG-REFERENCE-006` | real gateway outage、rate limit、retention | `AIC-E2E-010` | `AC-AI-011`, `AC-AI-012`, `AC-AI-014` |

Reference 的运行身份不得借用 Agent `modelGateway.profileId`。严格 spec 只从 Fixture 已绑定
`deploymentReceipt.contentHash` 的 immutable raw receipt 读取事实：按 exact receipt ID/hash
获取 bytes，客户端重算 SHA-256、验证 canonical JSON，再从文档取得 application、images、
commands、migration/admission、独立 generated-app gateway/provider policy（固定
`reference-project-default`）、ModelProfile、secret-injection receipt、rate limits 与
retention。Receipt 中的 commands、Gateway 全字段、完整 rate-limit policy 和 qualification
operation set 必须与根 Fixture 双向 exact equal；fetch 得到的 receipt 不能成为新的信任根。
六项 operation 都必须由 strict spec 对照该 root-admitted set 后调用。Migration rerun、
Run execution、timeout、retention 和 limiter observation 的
`evidenceDigest` 也必须寻址 exact canonical evidence bytes并由客户端重算，不能只验证
digest 的字符串格式或相信一个后验 JSON projection。

上述 Fixture/解析器闭包只证明资格输入的 Reference 身份与预期值已经内部实现并可
fail closed；它不等于目标部署已返回对应 raw receipt，也不把 Reference suite 提升为
qualified。外部状态仍为 `not-qualified`，直到 exact generated application 完成六个 case
并产生签名、hash-bound 的运行证据。

### 6.4 Release（5）

| Case | 场景组 | Requirement IDs |
| --- | --- | --- |
| `QG-RELEASE-001` | canonical handoff；preview happy/migration fail/health fail/single-flight | `AIC-E2E-014`, `015`, `019`; `AIC-FAIL-012`, `013`, `022`; `FQP-E2E-009`, `010` |
| `QG-RELEASE-002` | same-digest promotion 与 rollback | `AIC-E2E-016`, `017`; `FQP-E2E-010` |
| `QG-RELEASE-003` | submit timeout-after-commit；GET 404/ack drift；controller conflict；operator CAS | `AIC-E2E-018`, `020`; `AIC-FAIL-019`, `020`, `023`, `024`; `FQP-E2E-011` |
| `QG-RELEASE-004` | mutation maintenance；legacy v1 upgrade；legacy/v3 writer race | `AIC-E2E-021`; `AIC-FAIL-021`, `025` |
| `QG-RELEASE-005` | nested authority drift；DB clock skew；orphan Run/Operation | `AIC-E2E-022`; `AIC-FAIL-026`, `027`, `028` |

### 6.5 LSP（3）

| Case | 场景 | Requirement IDs |
| --- | --- | --- |
| `QG-LSP-001` | approved real language server bind/capabilities | `AIC-E2E-025`, `FQP-E2E-014`, `LSP-QA-016` |
| `QG-LSP-002` | stale drop、rebind/reconnect、undo 与 Candidate-CAS-only save | `AIC-E2E-025`, `FQP-E2E-014`, `LSP-QA-016` |
| `QG-LSP-003` | malicious server、crash/drift、Redis/resource/audit privacy matrix | `AIC-FAIL-036`, `FQP-E2E-014`, `LSP-QA-016` |

Inventory case 必须保存 `caseId/suiteId/mode/file/title/requirementIds/
contractCriterionIds` 的 exact identity。Reference suite 的 15 个 `AC-AI-*` 必须全部由
上述 6 case 覆盖；内部 `REQ-*` 只属于 Reference contract source，不写入外部 inventory
的顶层 requirement IDs。

## 7. Post-run artifact hygiene closure

`qualification-artifact-hygiene` 不是浏览器业务测试。Manifest/plan 应显式区分：

- `executionKind: playwright`：上述五个 suite，必须有 exact Golden spec 和 inventory
  case；
- `executionKind: post-run-verifier`：artifact hygiene，只能引用 hash-bound
  verification contract 与 digest-pinned verifier executable，不要求虚假的 Playwright
  case。

Post-run verifier 必须独立检查：

- normalized Playwright result 与 22-case inventory 精确一致；
- trace/video/log 的 KMS recipient、ciphertext descriptor、plaintext disposition；
- Golden Authority/Fixture actual-byte artifacts 与 Promotion Authority digest 一致；
- Fixture 的每个 fault authority、resultId consume receipt 与单个独立签名 run ledger
  attestation 完成 exact cardinality、历史 reservedAt、terminal outcome 和 digest 闭包；
- credential-set issuance/revocation 的相同 member closure 与严格时间链；
- artifact index 的文件、path、size、digest、classification、requirement/suite closure；
- Receipt 的 sole subject/index digest、runner+approver 独立签名与 key lifecycle；
- immutable SquashFS/EROFS exact file closure；
- verifier/git/source tree/executable digest 与 root authority 一致。

Preflight 只在运行前检查五个 `playwright` suite external-complete；post-run suite 在结果
产生后执行并进入同一 Receipt 决策。现有把所有 Golden external suite 都当作
Playwright、同时引用不存在 `qualification/artifact-hygiene.json` 的模型必须升级，不能
用第 23 个空测试绕过。

## 8. 执行、封存与批准顺序

唯一可闭环顺序是：

```text
blocking release Quality completes in the migration-000085 transaction
  -> immutable Quality material/candidate snapshot
  -> activation worker freezes the exact WIA and opens qualification waiting state
  -> reviewed source + qualification-ready revision
  -> compute exact plan/inventory/source-content-tree digests
  -> root issues Golden Authority v2 + Fixture v2
  -> credential issuer atomically issues complete run credential set
  -> strict preflight validates HTTPS, documents, files, profiles, suite closure
  -> execute 22 real cases and capture raw JSON/video/trace/log
  -> normalize exact inventory result (zero mock/skip/retry/flaky)
  -> independent fault-ledger-attestor signs the complete sorted terminal ledger
  -> encrypt restricted artifacts and dispose plaintext
  -> KMS signs encryption attestation
  -> issuer atomically revokes identical credential set
  -> create exact artifact index including issuance/revocation and Golden docs
  -> seal index/artifacts in a pre-Receipt SquashFS or EROFS snapshot
  -> digest-pinned verifier reopens and verifies the pre-Receipt closure
  -> compile exact wire-v3 payload from Plan Authority + verified snapshot
  -> runner + independent approver sign Receipt whose sole subject is snapshot digest
  -> independently verify and persist the canonical DSSE envelope
  -> migration 000080 precommits exact WIA + current Policy + Plan source and
     credential authorization through two distinct immutable admissions
  -> migration 000081 promotion transaction re-locks that exact full typed
     inputPrecommit plus Receipt and all other gates, then CAS-consumes once
     and appends one hash-bound pending handoff with a preallocated revision ID
  -> migration 000082 workflow transaction rechecks target/upstream/review/
     independent gates,
     creates the promotion-only immutable revision, completes the external gate
     and leaves production Publish waiting_input
  -> migration 000084 proves parent/output equivalence and atomically
     authorizes Publish from a real authenticated ActionPublish
  -> one stable Release Controller operation reconciles queued -> healthy
  -> migration 000084 appends the exact healthy result; only then may Publish
     and the Workflow complete
```

Artifact index 必须先于 pre-Receipt snapshot，因为 snapshot 要绑定 index digest/count。
Credential revocation 必须先于 index，因为 revocation DSSE 本身是 indexed artifact；KMS
attestation 必须先于 revocation。新 wire v3 必须先 seal 并独立验证一个不能表示 Receipt 的
snapshot，再让 Receipt 以该 snapshot digest 为唯一 subject；旧 wire v2 的 Receipt→seal
顺序只保留历史读取，不能继续签发。这样避免 Receipt 与包含它的 snapshot 相互哈希。

Promotion Authority/Receipt 的 Golden runtime binding 至少包含：

```text
authorityDocumentDigest + authorityArtifactId
fixtureDocumentDigest + fixtureArtifactId + fixtureId
faultOperationSetDigest (exact closed v1 13-operation commitment)
credentialSetHandleHash + memberBindingsDigest + memberCount
issuanceArtifactId + issuancePayloadDigest
revocationArtifactId + revocationPayloadDigest
```

最终 CLI 输出仍不是全局批准。内部 `qualificationpromotion` 服务与 migration `000071`
已经能由受信 authority resolver + digest-pinned verifier 产生完整 VerifiedPromotion，并在
同一 PostgreSQL 事务中写入 canonical consume ledger 与 `pending` handoff。它绑定 exact
project、workflowRun、nodeKey、target revision ID/content hash、subject/stage gate、nonce、
authority digest 和预分配 output revision ID；同 operation 精确重放是 immutable read，nonce
跨 target/digest 复用会冲突。`pending` 不是 immutable revision，也不是 workflow submit
事实。仍需 Workflow service 在另一事务中锁定并重核 target/upstream/canonical review 与所有
独立门禁，创建 exact promotion-only revision、CAS 提交节点，并记录 terminal handoff
consumption。ModelProfile governance 与 production PostgreSQL identity/posture 是独立门禁，
不能被 Golden Receipt 越权替代。

新生产路径严格按 `000080` Input Precommit → `000081` Promotion v2 → `000082`
Handoff 部署。Migration `000081` 的 canonical closure 必须直接包含完整 typed
`inputPrecommit` `PromotionBinding`，不能只依赖 junction、外键、预查询或 generic
independent-admission hash。Promotion runtime 保持禁用，直到 `000080` 的 SQL/隔离
角色/no-bypass canary 和 `000081` 的更新 consume canary 均通过并另行批准激活；
`000082` 不得抢跑。

## 9. 当前实现状态与剩余外部条件

当前仓库已有：strict qualification plan/inventory、source actual-byte tree、root-owned
Promotion Authority verifier、Golden Authority/Fixture v2 的 strict JS loader、exact
Playwright path/source closure、atomic 11-slot credential-set issuance/revocation Receipt
binding，以及 Go 端对两份 Golden 文档的完整 exact-shape/canonical/bidirectional-hash/
run-plan-fixture/source-TemplateRelease-BuildContract-WorkspaceRevision parser。artifact/index/
encryption/credential/DSSE/snapshot 检查和 Sandbox/Agent/Release/LSP 内部控制面也已存在，
五个 strict spec 与 exact 22-case inventory 已写入 qualification-ready source closure；
Reference spec 也已从 partial smoke 升级为独立的 6-case executable contract；Fixture v2
Reference 上游 identity/profile/policy/command/operation-set 闭包已由 JS/Go exact parser
和强负向向量内部实现。Promotion v2 的内部语义与 atomic
consume/pending-handoff building block 已存在。仓库内 migrations `000083`/`000084`/
`000085` 也已实现：`000083` 固化 Canonical Review forward-equivalence 前置边界；
`000084` 把 authenticated ActionPublish、same-content equivalence、唯一 Controller
operation 与 terminal healthy/failure result 绑定到 Publish 状态迁移；`000085` 则在
Quality completion 的同一 Workflow transaction 内预提交 exact gate input、保留内容材料
并形成 WIA activation candidate。对应的 opt-in v3 runtime、activation worker、独立
qualified-release Store/publisher/worker、配置和 readiness 接线均已进入仓库，静态合同与
定向单元回归已通过；migration `000085` 还已通过 fresh PostgreSQL 16 的
`up/down/up`、resolver/ACL、SQL/GORM 原子成功与回滚、Activate/exact replay 定向
canary。它们默认关闭且未在目标环境激活。该
`000085→WIA→000080→000081→000082→000084→Controller` 链尚未在 approved target
产生加密 artifact、外部签名 Receipt、真实全链 consume ledger 与 workflow-side Publish
completion，因此仍不是 external qualification evidence；migration `000085` 的定向
PostgreSQL canary 也不能写成整条 PostgreSQL full-chain 已通过。

当前不能声明闭环，原因包括：

- exact 22-case source inventory 已存在，但尚未产生一次 approved target 的 22/22、
  zero-skip/zero-retry/zero-flaky normalized result；
- Golden Fixture v2 的 Reference upstream identity blocker 已在内部消除；剩余条件是目标
  generated application 必须实际提供 Fixture 所指 exact immutable deployment receipt bytes，
  并由 6-case external run 产生可重算的 operation/evidence bytes 与签名 Receipt；
- one-shot fault DSSE 的独立 Go strict verifier、append-only CAS ledger，以及
  authority/consume receipt/run-ledger-attestation 的 Fixture/index/Qualification verifier
  闭包已有内部实现，但没有真实 fault adapter、外部 consume Receipt 或 attestor 产物；
- atomic credential-set 的 reservation/CAS、Prepare→Activate、unknown→Inspect、exact
  revoke、canonical issuance/revocation DSSE 和 opaque delivery-handle 内部服务已实现；
  migration `000072` durable PostgreSQL event/operation/head authority 也已通过内部测试和
  真实 PostgreSQL canary；但尚无专用生产 operator/DSN、真实 atomic broker/issuer/revoker
  与外部运行编排器，仓库内通过的仍不是目标环境凭据签发证据；delivery response-loss
  也仍需生产 claim/ack ledger，当前内部服务只会阻止 replay 再次泄露 capability；
- qualification-evidence 的严格 lifecycle、opaque Plan Authority `Execute` 边界，以及
  migrations `000073`/`000074` 的 owner-only durable Evidence event/CAS 与 immutable Plan
  Store 已通过真实 PostgreSQL 并发/replay/raw-hash/rollback canary；migration `000075`
  与 `internal/qualificationreceiptv3` 也已实现 owner-only durable
  request/observation/completion Store、两条签名请求原子冻结、数据库时间、authenticated
  claim/ACK retry generation、commit-unknown 精确恢复、真实 DSSE payload/envelope 持久化和
  immutable ExpectedResolver，并通过并发与重启 canary；但没有生产
  InputAuthority、专用 Plan/Evidence operator/DSN、abort→exact-revoke 终态或真实 capture
  → encrypt → KMS → revoke → index → pre-snapshot seal/verify → sign adapters，因此 trace capture 仍然 fail
  closed；
- 尚无 approved Golden TemplateRelease、live provider ModelProfile、真实 Controller/
  Registry/KMS/target cluster、approved language-server profile；
- production PostgreSQL LOGIN/NOLOGIN roles、dedicated schema/DML、secret injection 与
  scheduler recovery 尚无目标环境 Receipt；
- 下游 exact target/nonce/authority append-only consumption ledger、pending handoff、
  migration `000084` release authorization/result no-bypass ledger、migration `000085`
  Quality completion precommit 和 equivalence-aware Controller publisher 已有内部实现，
  但尚未在目标环境以独立 operator/credential、真实 Controller 和完整
  `000085→WIA→000080→000081→000082→000084` canary 共同形成外部运行证据与 Publish
  闭环。

因此下一步不是把仓库内实现提前写成生产完成，而是按
`000085→WIA→000080→000081→000082→000084→Controller` provision 生产 operator 与
真实 adapters、应用/验证 migrations、注册 Controller bootstrap，并完成 no-bypass
full-chain canary；在真实外部运行产生、验证并单次消费 Receipt 之前，manifest 必须保持
`status: not-qualified`，Workflow 批准按钮必须保持阻塞。

## 10. 实施顺序

1. 升级 Golden Authority/Fixture parser 和严格 credential file loader；
2. 升级 Promotion Authority/Receipt 为 v2 atomic credential-set + Golden binding；
3. 为 manifest/plan 增加 `playwright` 与 `post-run-verifier` 的可机读区分；
4. 已写入 exact 22-case inventory 与五个 strict Golden specs，并保持 external status
   `not-qualified`；任何 case 集变化都必须重新评审 inventory/plan；
5. 保持已完成的 fault authority/consume ledger 与 Fixture/index/Qualification evidence
   exact closure，再实现并审核真实 adapter 和外部 attestor orchestration；
6. 实现 capture/encrypt/KMS/revoke/index/pre-snapshot-seal/verify/sign orchestration；
7. 由目标环境 migration credential 应用并核验 `000083`–`000085`，保持 feature gates
   关闭，注册唯一 Controller bootstrap，provision 专用 operator/DSN 并完成 dry readiness；
8. 对 reviewed canary project 执行 migration `000085` Quality completion precommit，由
   activation worker 以同一 completion-event identity 冻结 exact WIA；
9. 在 approved target 执行 qualification，生成 immutable evidence；
10. 应用 migration `000080`，由不同 source verifier 与 credential resolver 为 exact
   WIA+Policy+Plan 形成 input-precommit，并通过 SQL/角色/no-bypass canary；
11. 应用 migration `000081`，在 canonical closure 中直接消费完整 typed
   `inputPrecommit`，通过更新后的 Promotion canary 后才允许生成 pending handoff；
12. 应用 migration `000082`，由私有 workflow consumer 完成 promotion-only immutable
    revision 与 exact node submit；
13. 由 authenticated `ActionPublish` 触发 migration `000084` authority，再以同一 stable
    operation 执行 Release Controller `queued -> healthy`；append exact result 后才完成
    Publish，验证 lease/replay/crash/full-chain canary；
14. 最后再分别完成 ModelProfile 与 production PostgreSQL 独立治理门禁。

任何模型、Provider 或 Codex 镜像替换都必须复用同一 frozen contract、22-case inventory、
fault matrix 和 post-run verifier；只有这样“一致性”才来自可执行约束，而不是依赖某个
模型恰好不产生幻觉。
