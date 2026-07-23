# Qualification Promotion v2 and Pending Handoff

Status: implementation contract for migration `000081`. This document defines
the first transaction that may consume a snapshot-first Qualification Receipt
v3. It requires the Qualification Input Precommit Authority installed by
migration `000080`, is intentionally separate from historical migration
`000071`, and does not perform the workflow-side handoff owned by migration
`000082`.

No state described here is evidence that an external qualification has run.
Promotion v2 can succeed only after all referenced immutable authorities and
the terminal Receipt v3 already exist. Consume-time currentness applies to the
Qualification Policy, WIA project/workflow/target/review/build closure, and
each policy-required independent authority. Plan, Evidence, and Receipt are
exact immutable/terminal records rather than mutable heads; their frozen facts
are revalidated, but no unsupported generic "current" label is invented for
Golden, KMS, signer, or trust hashes. Until migration `000080` and its
production SQL/role/no-bypass canary, migration `000081` and its production
adapter, and every policy-required independent authority adapter are deployed
and qualified, the external-qualification node must remain blocked.

## 1. Purpose and boundary

Promotion v2 is the atomic composition point for these authority planes:

```text
Qualification Policy Authority
  -> Workflow Input Authority
  -> Qualification Plan Authority
  -> fixed system-required Qualification Input Precommit Authority
  -> Qualification Evidence event ledger
  -> Receipt v3 durable-control ledger and terminal envelope
  -> policy-required independent authority receipts
  -> one single-use Promotion v2 consumption
  -> one pending workflow handoff
```

It proves that one terminal Receipt v3 still describes the exact current
workflow input and target that were activated for one non-waivable external
qualification gate. It then consumes that capability once.

Promotion v2 does not:

- run qualification, seal evidence, call a signer, or verify a browser payload;
- create or approve an artifact revision;
- complete the workflow node or advance downstream Publish;
- reinterpret a historical Receipt v2 or migration `000071` consumption;
- treat a hash label, mutable current lookup, or standalone diagnostic result
  as an authority; or
- accept a caller-authored target, receipt, evidence, review, policy, or
  independent-gate projection.

The only output is an immutable consumption record and an immutable `pending`
handoff with a preallocated output revision identity. Migration `000082` owns
the later workflow mutation.

## 2. Generations and compatibility

The protocol and wire generations are:

```text
worksflow-qualification-promotion-consume/v2
worksflow-qualification-promotion-consume-request/v2
worksflow-qualification-promotion-closure/v2
worksflow-qualification-promotion-consumption/v2
worksflow-qualification-promotion-handoff/v2
worksflow-qualification-promotion-revision-intent/v2
worksflow-qualification-promotion-evidence-event-set/v2
worksflow-qualification-promotion-independent-authority-admission/v1
worksflow-qualification-promotion-store-bundle/v2
```

Migration `000071` is a historical Receipt-v2 consumer. Migration `000075`
already prevents new v1 consumption after Receipt v3 activation. Migration
`000081` creates new tables and functions; it must not add v3 columns to the
v1 tables, synthesize a v1 `VerifiedPromotion`, or remove historical reads.

The workflow definition must use `workflow-engine/v3`, the external gate must
declare `worksflow-qualification-receipt/v3`, and its promotion protocol must
equal `worksflow-qualification-promotion-consume/v2`. Older profiles remain
readable but cannot enter this transition.

## 3. Opaque command and server-owned resolution

The internal command contains exactly five preallocated UUIDv4 identities:

```text
operationId
workflowInputAuthorityId
planAuthorityId
handoffId
outputRevisionId
```

All five IDs are pairwise distinct. In particular, none of the three newly
allocated operation/handoff/output identities may alias the Workflow Input or
Plan authority identity. Go validation, the SQL entrypoint, canonical request
validation, and identity reservations all enforce the same rule.

The API must allocate these identities server-side. No public/browser DTO may
add a receipt ID, target, revision hash, evidence digest, signer identity,
policy value, independent receipt, expiry, status, or canonical document.

`receiptId` is resolved through the unique terminal Receipt v3 for the exact
Plan Authority. The Workflow run/node/target and Qualification Policy are
resolved through the Workflow Input Authority. The required input precommit is
resolved by the exact `(workflowInputAuthorityId, planAuthorityId)` pair; the
caller does not select or submit its ID/hash. Evidence identities and the four
durable-control requests/observations are resolved through the Plan and
terminal Receipt. This makes mismatched caller-selected combinations
impossible before the database performs its own equality checks.

An exact operation replay is inspected before any current-authority or
external dependency resolution. A committed result therefore remains
recoverable after policy supersession, target retirement, or evidence archive.
A changed replay using any different command identity is a conflict.

## 4. Canonical wire and hashes

All documents use the repository canonical JSON rules: strict UTF-8, no
unknown or duplicate member names, exact lower-case UUIDv4 strings, UTC
millisecond timestamps, JavaScript-safe integers, sorted bounded arrays, and
no `null` where an empty closed collection is required.

Hashes use domain separation:

```text
SHA256(
  UTF8("worksflow-qualification-promotion-hash/v2") || 0x00 ||
  UTF8(domain) || 0x00 || exactCanonicalBytes
)
```

The closed domains are:

```text
worksflow.qualification-promotion.request/v2
worksflow.qualification-promotion.closure/v2
worksflow.qualification-promotion.consumption/v2
worksflow.qualification-promotion.handoff/v2
worksflow.qualification-promotion.revision-intent/v2
worksflow.qualification-promotion.evidence-event-set/v2
worksflow.qualification-promotion.independent-authority/v1
```

Every durable document retains exact `bytea`, its parsed `jsonb` projection,
and its domain hash. PostgreSQL and Go both recompute every value. JSONB
equality is never a replacement for exact raw-byte equality.

### 4.1 Consume request

The request has this closed shape:

```text
schemaVersion
operationId
workflowInputAuthorityId
planAuthorityId
handoffId
outputRevisionId
```

It deliberately contains no timestamp and no resolved authority facts. The
database can decide exact replay before trusted time or mutable-state checks.

### 4.2 Promotion closure

The closure is a bounded hash projection over exact upstream records. It
contains:

```text
schemaVersion
workflowInput { authorityId, authorityHash, inputHash, targetHash,
                qualificationPolicyAuthorityId,
                qualificationPolicyAuthorityHash }
plan { authorityId, authorityHash, inputAuthorityId, inputHash,
       projectionHash, evidencePlanHash, targetHash, trustHash,
       orchestrationId, qualificationRunId }
inputPrecommit {
  kind = qualification-input-precommit
  authorityId, authorityHash
  workflowInputAuthorityId, workflowInputAuthorityHash
  qualificationPolicyAuthorityId, qualificationPolicyAuthorityHash
  qualificationPlanAuthorityId, qualificationPlanAuthorityHash
  sourceRequestHash, sourceReceiptHash, sourceAdmissionHash
  credentialRequestHash, credentialReceiptHash, credentialAdmissionHash
}
evidence { headVersion, phase, lastEventId, lastEventHash, commandHash,
           trustBindingsDigest, evidenceClosureDigest,
           artifactIndexDigest, eventSetDigest }
receipt { receiptId, envelopeHash, payloadHash, paeHash, completionHash,
          snapshotRequestHash, snapshotObservationHash,
          verificationRequestHash, verificationObservationHash,
          runnerRequestHash, runnerObservationHash,
          approverRequestHash, approverObservationHash }
target { projectId, workflowRunId, nodeRunId, nodeKey, artifactId,
         revisionId, revisionContentHash, subject, stageGate }
independentAuthorities[] { kind, authorityId, authorityHash,
                           admissionRecordHash, sourceReceiptHash,
                           receiptSchemaVersion }
```

`eventSetDigest` is the domain hash of this canonical closed document:

```text
schemaVersion = worksflow-qualification-promotion-evidence-event-set/v2
orchestrationId
headVersion
events[] { version, eventId, eventHash }
```

The array is bounded to 2048 members, has exactly `headVersion` entries, and
uses consecutive versions `1..headVersion`. Its last ID/hash must equal the
locked head and last event. This prevents a head-only projection from hiding a
missing, extra, or reordered event. The closure also retains the terminal
event identity/hash and the artifact-index digest so the same facts can be
checked without trusting the aggregate digest alone.

`inputPrecommit` is the complete `PromotionBinding` defined by
[Qualification Input Precommit Authority v1](./qualification-input-precommit-authority-v1.md).
It is one fixed, typed, system-required canonical closure member. It must not
be omitted, reduced to an opaque admission hash, or inserted into the Policy's
configurable `independentAuthorities[]` list. Promotion locks the exact
precommit and both of its local receipt admissions, recomputes all canonical
bytes/hashes, and proves that its WIA, current Policy, and Plan projections are
the same locked records already used by the rest of the closure.

The independent-authority list remains a separate policy-configured plane and
must exactly equal the sorted list in the Qualification Policy Authority.
Empty is valid only when the policy contains an explicit empty list.

### 4.3 Canonical target comparison

Workflow Input, Plan, and Receipt use intentionally different target wire
shapes. Promotion normalizes them into this internal `PromotionTargetV2`
projection before equality checks:

```text
projectId
workflowRunId
nodeRunId
nodeKey
targetArtifactId
targetRevisionId
targetRevisionContentHash
subject
stageGate
```

WIA `manifestSubject` equals Plan/Receipt `promotionTarget.subject`; WIA's flat
revision ID/hash equal Plan/Receipt's nested `targetRevision.id/contentHash`.
The seven common target fields must match exactly. `nodeRunId` and
`targetArtifactId` come from the locked WIA row, and Promotion independently
checks that the locked target revision belongs to that artifact. Plan and
Receipt cannot invent those two fields.

Migration `000074` stores the Plan Authority's canonical authority digest in
the `envelope_hash` column; Receipt v3 calls that value
`planAuthorityHash`. Migration `000081` must not look for or add a second
`authority_hash` column.

### 4.4 Consumption and handoff

The hash graph is deliberately acyclic:

```text
request + locked upstream authorities
  -> closure
  -> revision intent
  -> consumption
  -> handoff
```

The revision intent has this closed shape:

```text
schemaVersion
requestHash
closureHash
outputRevisionId
revisionKind = external-qualification-promotion/v2
target
workflowInput { authorityId, authorityHash }
plan { authorityId, authorityHash }
receipt { receiptId, envelopeHash }
```

The consumption document has this closed shape:

```text
schemaVersion
operationId
requestHash
closureHash
revisionIntentHash
consumedAt
```

It deliberately does not contain a handoff hash. The handoff document binds:

```text
schemaVersion
handoffId
operationId
state = pending
outputRevisionId
revisionIntentHash
workflowInputAuthorityId
planAuthorityId
receiptId
consumptionHash
target
createdAt
```

One database `v_now`, canonicalized to UTC milliseconds after all locks, is
used for both `consumedAt` and `createdAt`.

The revision intent is sufficient for migration `000082` to construct only
the profile-declared promotion output. It contains no user content and cannot
authorize a different artifact or node. Atomic insertion plus the request's
prebound handoff ID gives the consumption its one handoff without introducing
a consumption↔handoff or consumption↔intent hash cycle.

## 5. Independent authority receipts

Qualification Policy v1 may require exactly these independent kinds:

```text
model-profile-activation
production-postgresql-posture
```

A policy binding is only a requirement; it is not proof that the authority
exists or is current. Migration `000081` therefore needs an append-only
promotion-facing registry of verified independent receipts. It retains the
kind-specific signed source receipt separately from a canonical Promotion
admission record.

The source receipt fields are:

```text
kind
authorityId
authorityHash
receiptSchemaVersion
sourceReceiptHash
sourceReceiptBytes
sourceReceiptDocument
issuedAt
expiresAt
verifiedAt
```

`sourceReceiptHash` is the kind-specific receipt's own exact hash; it is not
the Promotion domain hash. The canonical admission record has schema
`worksflow-qualification-promotion-independent-authority-admission/v1` and
contains exactly:

```text
schemaVersion
kind
authorityId
authorityHash
receiptSchemaVersion
sourceReceiptHash
issuedAt
expiresAt
verifiedAt
sourceLinkage
```

`sourceLinkage` is a strict tagged union. A Model Profile admission binds the
workload, activation operation, generation, fence, signed governance receipt,
and future common authority epoch. A PostgreSQL admission binds the deployment
epoch, signed posture receipt identity, and exact catalog-contract hash. The
admission record's canonical bytes and
`worksflow.qualification-promotion.independent-authority/v1` domain hash are
stored as `admission_record_bytes` and `admission_record_hash`. Promotion
closure members bind both `admissionRecordHash` and `sourceReceiptHash`; the
two layers are never called by the same ambiguous `receiptHash` name.

The registry key uses the policy's bounded stable text `authorityId`; it is not
a UUID column. The registry is not a generic JSON admission API. A
server-installed, kind-specific verifier must produce a package-private
verification grant before the store can append a record:

- Model Profile verification re-resolves the exact signed activation history,
  current head, trust/revocation anchors, route authority, disable state, and
  complete fallback graph. The current `ResolvedActive` result is explicitly
  non-atomic across those dependencies and is not such a receipt. A future
  common-epoch/data-plane composition authority must sign and durably bind the
  exact observation with a bounded validity window.
- Production PostgreSQL verification requires an independently signed
  composition receipt over the exact multi-identity check and deployment
  epoch. The current `productionpostgres.Result` is a useful point-in-time
  diagnostic, but it spans separate catalog snapshots and is unsigned. It must
  not be admitted directly or hashed and relabeled as an authority.

Promotion locks the exact registry rows required by policy and checks their
hashes, schemas, time windows, non-revocation/currentness projection, and
kind-specific source linkage. A missing adapter, missing record, expired
record, superseded Model Profile head, changed posture epoch, or any mismatched
ID/hash blocks consumption.

Until those kind-specific production verifiers exist, a non-empty independent
requirement list must fail closed. Tests may use semantic fakes only in the
in-memory reference; a production PostgreSQL canary must use the real verifier
boundary or an explicitly empty reviewed policy.

## 6. Atomic PostgreSQL transition

The production adapter first checks out one dedicated, session-affine database
connection and acquires the operation-scoped session advisory lock
`worksflow:qualification-promotion-v2:operation:<operationId>` **before**
`BEGIN`. It then starts the `SERIALIZABLE` transaction and invokes consume on
that same physical PostgreSQL backend. This ordering is required: a
transaction-level lock taken as the first statement of a serializable
transaction does not refresh the transaction snapshot after waiting. The SQL
routine repeats the equivalent transaction advisory lock as a defense for
direct callers, but that repeated lock is not a substitute for the adapter's
pre-transaction lock.

The adapter must release the session lock with a bounded
`context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)` cleanup and
require `pg_advisory_unlock` to return `true`. On false or error it marks the
physical driver connection bad and discards it; returning a possibly locked
connection to `database/sql` is forbidden. The Promotion DSN must provide
session affinity and must not pass through a transaction-pooling proxy.
An error while acquiring `pg_advisory_lock` is also lock-outcome-unknown: the
backend may have acquired the session lock before cancellation or transport
failure hid the result. The adapter must poison and discard that physical
connection before returning the error; it must never merely call `Close` and
return the session to the pool.

An unclassified `BEGIN`, `COMMIT`, `ROLLBACK`, or unlock result likewise makes
the physical session unsafe. The adapter marks it `driver.ErrBadConn`, skips
any further unlock on that backend, and never returns it to the pool. Only a
linear PostgreSQL error chain ending in SQLSTATE `40001` or `40P01` proves the
complete attempt aborted and permits a bounded same-command retry; joined,
transport, cancellation, and generic driver errors are never retry signals.
An ambiguous `COMMIT` is never retried. The service instead inspects the same
`operationId` on a fresh primary connection under a bounded
`context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)` context, even
when the command context was cancelled while commit was in flight.

The consume routine performs this order in one `SERIALIZABLE` transaction:

1. Acquire the shared rollout fence
   `worksflow:workflow-input-authority-migration:v1` before the first relation
   read.
2. Inspect `operationId`. Return only an exact immutable replay; reject changed
   command identities.
3. Resolve the Workflow Authority's project ID without locking another
   authority plane, then lock `projects(id) FOR UPDATE`.
4. Call the transaction-bound Workflow Input Authority assertion. It re-locks
   the project idempotently, locks the run/node/upstream rows in platform order,
   and verifies policy, profile, gate, Quality, current target, BuildManifest,
   BuildContract, sources, revisions, and Canonical Review receipt closure.
5. Lock the current source-verifier and credential-resolver executable-binding
   generations in the same stable role order used by migration `000080`.
   Promotion later proves that the locked input precommit and both admissions
   bind these exact generations; it must not first lock Plan/admissions and
   then invert the migration-80 order.
6. Read only the immutable Plan row to locate its orchestration ID, without
   taking a Plan row lock or treating the read as validated authority. Then
   acquire explicit `ROW SHARE` relation locks in the established
   `qualification_evidence_events` -> `qualification_evidence_operations` ->
   `qualification_evidence_heads` order before taking the Evidence head row,
   ordered operation rows, and complete ordered event set. This relation-lock
   order is distinct from the later aggregate-validation order. Replay and
   require the Receipt-v3 cut point `phase=artifact-indexed`; recompute the
   event-set, closure, and artifact-index bindings. Migration `000075`
   deliberately makes the Plan-linked v1 receipt/sign/seal tail history-only,
   so requiring the legacy `complete` phase here would make every Receipt v3
   unconsumable. Every future Evidence/Plan/Receipt DDL path takes the exclusive
   rollout fence before requesting relation locks, so the immutable locator
   read cannot form a Plan-to-Evidence DDL lock cycle.
7. Lock the exact Plan Authority and identity rows, re-read the locator, and
   reject any mismatch. Recompute all raw byte/hash/JSON projections and
   require `Plan.InputAuthorityID == WorkflowInputAuthorityID` and the locked
   Evidence identity/plan/trust bindings to match.
8. Resolve and lock the one Qualification Input Precommit Authority selected
   by the exact WIA+Plan pair, then lock its source and credential receipt
   admissions. Recompute its request, admission, authority bytes and hashes;
   require the complete `PromotionBinding` to match the locked WIA, current
   Policy, Plan, verifier/resolver generations, and admitted external receipt
   hashes. Missing, multiple, stale, aliased, or reduced bindings fail closed.
9. Lock the four Receipt v3 requests, their exact terminal committed
   observations, and the unique terminal receipt. Re-run the closure checks
   used by `complete_qualification_receipt_v3`, including exact payload, PAE,
   envelope, signatures, observation chronology, latest-terminal status, and
   Plan/Evidence bindings.
10. Lock and verify the exact independent authority receipts required by the
   current Qualification Policy.
11. Require Workflow target = Plan target = Receipt target. Compare WIA to Plan
   for the exact BuildManifest and BuildContract IDs/content hashes; compare
   Plan to Receipt for source, build, TemplateRelease, Qualification Manifest,
   Golden runtime, credential-set, target, trust, and Evidence Plan; and compare
   every Policy profile member which the v1 Plan input actually carries.
12. Require the input precommit's full source and credential authorization
    edges to match the Policy and Plan facts. Do not compare or relabel the
    source-policy, source-tree, request-set, or member-bindings digest domains.
13. Build canonical closure, revision intent, consumption, and then handoff
    bytes from the locked rows. Insert exactly one consumption and one
    `pending` handoff.
14. Reload both rows, cross-check raw bytes/hash/JSON/scalar columns, and commit.

The routine must not require the workflow run event cursor or context to equal
the activation-time value. Qualification legitimately advances independent
ledgers and may append status events. Only the frozen activation event and the
explicitly authority-bound facts are compared.

Qualification-only writers never acquire the project lock after Evidence,
Plan, or Receipt locks. Promotion is allowed to read those planes only because
it acquired the fence and project lock first.

### 6.1 Mandatory input-precommit closure

The v1 Plan input does **not** carry the Policy profile's
`sourcePolicyDigest` or `credentialProfile.memberRequestSetDigest`. WIA also
does not carry the repository commit plus
`worksflow-source-content-tree/v1` digest. These values are different digest
domains and must never be equated with the WIA input hash, revision content
hash, Plan source tree digest, or resolved credential member-binding digest.

Migration `000080` closes this representational gap with an immutable input
precommit authority that binds one exact WIA, Policy, and Plan to:

- the clean repository source projection and the exact source-policy digest;
- the reviewed credential request-set digest and the exact resolved credential
  set projection; and
- the source-verifier/credential-resolver identities, executable digests, and
  canonical authority bytes/hash.

Migration `000081` treats that record as a fixed system prerequisite and puts
its complete typed `PromotionBinding` in canonical `inputPrecommit`; it never
models the record as an optional Policy independent authority. Zero or
multiple records for the exact WIA+Plan pair, missing local receipt admissions,
stale Policy binding, or any scalar/raw-byte/hash mismatch blocks consumption.

Production posture must keep the Promotion worker/trigger disabled until the
`000080` SQL, isolated roles and no-bypass canary are deployed and the updated
`000081` consume path proves this required member in PostgreSQL. This is a hard
rollout prerequisite, not a preflight-only query or a documentation follow-up.

## 7. PostgreSQL schema and API

Migration `000081` creates at least:

```text
qualification_promotion_v2_independent_receipts
qualification_promotion_v2_consumptions
qualification_promotion_v2_consumption_independent_receipts
qualification_promotion_v2_handoffs
qualification_promotion_v2_identity_reservations
artifact_revision_identity_reservations
```

The consumption aggregate repeats the exact input-precommit authority ID/hash
as scalar columns and retains the full typed projection inside closure bytes.
Deferred closure checks resolve migration `000080`'s authority and both local
receipt admissions, then prove scalar/document/raw-byte/hash equality. A
junction row or foreign key which is absent from the canonical closure is not
an acceptable substitute.

All Promotion tables are append-only. `UPDATE`, `DELETE`, and `TRUNCATE` fail
through immutable triggers. UUID identities share one Promotion reservation
namespace for operation, handoff, and output revision IDs so a local cross-role
collision cannot be reinterpreted. The junction table copies the exact sorted
independent receipt set consumed by each closure; a mutable registry query is
not historical evidence.

A trigger which merely checks another table cannot reserve an output revision
against a concurrent insert: both transactions could observe absence. Migration
`000081` therefore creates the shared
`artifact_revision_identity_reservations(id PRIMARY KEY, owner_kind,
owner_operation_id, reserved_at)` write point. Under an exclusive rollout
lock it backfills every existing `artifact_revisions.id` as
`owner_kind=artifact-revision` with `owner_operation_id IS NULL`. The closed
owner constraint permits only `(artifact-revision, NULL)` or
`(qualification-promotion-v2, non-NULL UUIDv4)`. Thereafter:

- every ordinary revision insert first inserts its ID into this shared table;
- Promotion first inserts its output ID as
  `owner_kind=qualification-promotion-v2`; and
- the primary key, not a check-then-insert race or advisory lock, chooses one
  owner atomically.

Migration `000082` adds a transaction-local, handoff-scoped authorization that
permits its private consumer to insert the exact artifact revision using the
already-owned Promotion reservation. Any other insert using that ID is
rejected. The shared reservations are immutable; rollback may discard only
derived `artifact-revision` backfill after proving that no Promotion-owned
reservation or history exists.

The migration-81 handoff row is only an immutable pending intent; its `state`
never updates. Migration `000082` adds a separate append-only handoff
completion/event table. Its private consumer locks the pending aggregate and
requires no completion row before creating the revision and terminal record in
one transaction. Thus two consumers cannot both complete one pending handoff.
The complete migration-82 contract, including its same-content Workspace
Revision strategy and transaction-local reservation grant, is defined in
[Qualification Promotion v2 Workflow Handoff](./qualification-handoff-v1.md).

The supported routines are:

```text
consume_qualification_promotion_v2(
  uuid, uuid, uuid, uuid, uuid
)
  RETURNS SETOF jsonb
  VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE

inspect_qualification_promotion_v2_operation(uuid)
  RETURNS SETOF jsonb
  STABLE CALLED ON NULL INPUT PARALLEL UNSAFE

resolve_qualification_promotion_v2_handoff(uuid)
  RETURNS SETOF jsonb
  STABLE CALLED ON NULL INPUT PARALLEL UNSAFE

assert_pending_qualification_promotion_v2_handoff(uuid)
  RETURNS SETOF jsonb
  VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE

inspect_historical_qualification_promotion_v1_operation(uuid)
  RETURNS SETOF jsonb
  STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
```

Each resolver returns zero rows for not found and otherwise one closed
`worksflow-qualification-promotion-store-bundle/v2` JSON object. Consume and
operation inspection return the exact consumption/handoff scalar projections,
domain hashes, and raw-byte hex needed by Go to reparse and cross-check both
records; consume alone adds the response-only `idempotent` flag. All four v2
routines return the same stored bundle fields. The handoff resolvers look up by
handoff ID but still include the exact revision-intent bytes/document required
by migration `000082`, together with their request, closure, and consumption
hash projections. They never return underlying Evidence event payloads,
Receipt signer material, or other upstream raw authority bytes.

The common bundle member set is exact:

```text
schemaVersion
operationId
workflowInputAuthorityId
planAuthorityId
receiptId
evidenceEventSet { hash, bytesHex, document }
request          { hash, bytesHex, document }
closure          { hash, bytesHex, document }
revisionIntent   { hash, bytesHex, document }
consumption      { hash, bytesHex, document, consumedAt }
handoff          {
  handoffId, hash, bytesHex, document, state, outputRevisionId, createdAt
}
```

Only the consume response adds the exact boolean `idempotent`; stored
canonical documents and hashes never contain that response field.

Historical v1 inspection returns zero rows when the operation does not exist
and exactly one closed
`worksflow-qualification-promotion-v1-history-bundle/v1` object otherwise. It
contains exactly `schemaVersion`, `operationId`, `qualificationAuthorityId`,
`requestHash`, `targetDigest`, `verifiedPromotionHash`, `consumedAt`,
`handoffId`, `state`, `outputRevisionId`, `revisionIntentDigest`, and
`createdAt`. It never returns retained raw request, verifier, evidence,
signature, or revision-intent bytes. A NULL or non-v4 operation ID returns zero
rows; it does not widen into a listing API.

`consume_qualification_promotion_v2` rejects unless
`current_setting('transaction_isolation') = 'serializable'`. A SECURITY
DEFINER function cannot upgrade an already-open transaction, so the Go adapter
must use `BeginTx(..., sql.LevelSerializable)` before invoking it. Every
routine explicitly validates NULL rather than relying on STRICT's silent NULL
return.

The migration uses these closed SQLSTATEs:

```text
WPV01 invalid command, non-serializable transaction, or malformed store bytes
WPV02 immutable identity/byte conflict or corrupt durable aggregate
WPV03 prerequisite or terminal Receipt not ready
WPV04 current Policy/WIA/target/independent authority is stale
```

`40001`/`40P01` mean the transaction definitely aborted and may be retried
with the same operation; a client-visible commit error remains
`outcome-unknown` until exact operation inspection succeeds.

Independent-receipt admission uses separate kind-specific service methods and
must not be folded into `consume_qualification_promotion_v2`. This keeps an
external verifier call outside a database transaction while ensuring the
later consume transaction locks and revalidates the resulting immutable
record.

Migration `000081` deliberately creates no independent-admission SQL routine
and grants no role a registry write path. Its registry therefore remains empty,
and consume accepts only a reviewed policy whose independent-authority list is
explicitly empty. Any non-empty list fails closed before registry resolution.
A later separately reviewed migration must define the two exact `sourceLinkage`
tagged-union shapes, kind-specific signed verifiers, SQL signatures, role
grants, and rollout/rollback tests before it can admit either authority kind.

Every SECURITY DEFINER routine has an exact signature, result type, owner,
volatility, strictness, parallel mode, and fixed
`pg_catalog, <trusted-schema>, pg_temp` search path. PUBLIC, application,
qualification, policy, auditor, and migration login roles receive no direct
table access and no unexpected routine execution.

Migration `000081` is also the runtime privilege transition for the existing
`worksflow_qualification_promotion_operator`. After old workers are drained it
revokes the historical v1 two-table `SELECT`, v1 consume execution, and direct
WIA/Policy assertion execution. A new owner-defined
`inspect_historical_qualification_promotion_v1_operation(uuid)` returns the
closed read-only historical projection without restoring table access.

The Promotion operator then receives exactly three functions: v2 consume, v2
operation inspection, and historical v1 operation inspection. The v2
SECURITY DEFINER consume routine invokes the owner-held WIA/Policy assertions
internally. Handoff resolve/assert are reserved for the separately reviewed
private workflow consumer and are not granted to the Promotion login. The
Promotion-v2 schema migration has no independent-admission routine at all; a
later dedicated migration must introduce and posture any such API. The
operator receives no table DML, schema CREATE, migration-owner membership, or
generic function execution.
Platform and five-session production posture checks must change atomically
with the Promotion-v2 schema migration and enumerate this exact contract rather
than accept a minimum count.

The production posture result is deliberately not an activation receipt. It
accepts only `direct` or `session-pool` as the Promotion DSN affinity signal;
transaction pooling is rejected because the operation advisory lock must stay
on one physical session across `BEGIN`, consume, commit, and unlock. It also
requires the exact runtime gate
`disabled-pending-input-precommit-authority-canary`. The Promotion-v2 schema
migration, posture checker, and this package do not register a route, worker,
database scheduler, or workflow trigger. The Promotion runtime stays disabled
until migration `000080`'s SQL/role canaries and migration `000081`'s updated
consume/no-bypass canary pass and a separately reviewed activation change
exists. Handoff resolution remains a distinct migration-82 identity and must
not be enabled by adding its functions to the Promotion DSN.

## 8. Idempotency, conflict, and uncertain commit

The identities have these meanings:

- `operationId` identifies one immutable consume request and result;
- `workflowInputAuthorityId` may be consumed by at most one Promotion v2
  operation;
- `planAuthorityId` and terminal `receiptId` may each be consumed once;
- the input precommit is uniquely resolved from that WIA+Plan pair and its
  exact ID/hash/full binding may appear in only that immutable consumption;
- `handoffId` identifies one immutable pending intent and, after migration
  `000082`, at most one append-only terminal completion record;
- `outputRevisionId` is globally reserved for that handoff and cannot be reused
  by another revision or workflow operation.

Concurrent exact attempts through the session-affine adapter converge on one
fresh result plus idempotent replays. Other serialization/deadlock conflicts
return `40001`/`40P01` and are retried only with the same five IDs.
Any different operation, authority, receipt, handoff, output revision, target,
closure, or request bytes conflict. No retry may rebase a stale receipt onto a
new revision or newer policy.

If commit acknowledgement is lost, the adapter inspects the same operation on
a new strongly consistent transaction. It returns success only when every
stored byte and scalar equals the prepared command and reconstructed record.
`not found` from an unavailable or ambiguous dependency is not proof that the
transaction did not commit. While outcome is unknown, no new operation,
handoff, output revision, or receipt consumption may be allocated.

## 9. Go package boundary

Add `backend/internal/qualificationpromotionv2/` rather than changing the
historical `qualificationpromotion` package. It contains:

- closed command, request, closure, consumption, revision-intent, handoff, and
  independent-receipt types;
- strict canonical encoding, cloning, hash, secret-scan, and equality helpers;
- a deterministic in-memory semantic reference;
- a PostgreSQL adapter that calls only the reviewed routines and then
  cross-validates exact bytes/JSON/scalars;
- operation and handoff inspection methods for commit-unknown recovery; and
- one mandatory typed input-precommit resolver/binding path that rejects every
  missing, ambiguous, stale, or reduced `PromotionBinding`; and
- an explicit empty-independent-policy path plus fail-closed rejection of
  every non-empty requirement list.

Package-private verification grants and independent-receipt admission belong
to the later dedicated admission migration, not the `000081` package surface.

The production consume path must not assemble a closure from a sequence of
autocommit repository reads. Resolution and consumption happen inside the one
database function/transaction. The in-memory resolver exists only to specify
semantics and test races; it is not a production authority implementation.

The adapter also verifies `NOT pg_is_in_recovery()` and
`transaction_read_only=off` inside the exact SERIALIZABLE consume attempt.
Operation inspection performs the same test and immutable lookup in one SQL
statement, returning `outcome-unknown` rather than `not-found` whenever the
backend is not a read-write primary. This runtime check complements, but does
not replace, the production DSN requirement for
`target_session_attrs=read-write` and the posture canary.

No HTTP route is required for the first implementation. When exposed later,
the route accepts only the opaque command, requires server authorization, and
returns a safe operation/handoff projection. It never returns raw evidence,
signatures, encrypted artifact metadata, source bytes, or internal diagnostic
details.

## 10. Failure classes

The domain maps database details to a closed set:

```text
invalid          malformed opaque command or corrupt prepared bytes
not-found        no exact immutable operation/handoff during inspection
not-ready        qualification/Receipt/independent authority is incomplete
stale            current policy, WIA, target, review, model, or posture drifted
conflict         identity or immutable-byte reuse differs
retryable        the complete same-ID transaction definitely aborted with 40001/40P01
outcome-unknown  commit may have succeeded; inspect the same operation
```

Production responses do not expose SQL, relation names, DSNs, evidence paths,
signer material, or raw external errors. Logs use stable reason codes and
opaque operation IDs only.

## 11. Rollout and rollback

The rollout order is:

```text
1. deploy the already-fenced v3 workflow runtime
2. apply migrations 000078 and 000079
3. deploy/verify QPA and WIA operator posture
4. apply migration 000080 for the Qualification Input Precommit Authority
5. deploy its isolated composition/verifier/resolver roles and adapters, then
   pass its production-role no-bypass canary
6. apply migration 000081 for Promotion v2
7. deploy the updated Promotion v2 adapter with no public trigger and keep its
   worker disabled
8. issue or reuse a reviewed policy with an explicit empty independent list
9. run a no-bypass Promotion v2 canary and prove missing, ambiguous, stale, or
   reduced inputPrecommit bindings and every non-empty independent list fail
10. apply migration 000082 and deploy the private handoff consumer only after
    the Promotion canary passes
11. deploy the profile-v3 Release Controller publisher and run the complete
    workflow canary
12. only then expose/enable the external-qualification action for that policy
```

Qualification of independent authority adapters, the dedicated admission
migration, a new non-empty policy generation, and its full no-bypass canary are
a later rollout. They cannot be folded into the initial `000081` deployment.

Migration `000081.down` takes compatible `ACCESS EXCLUSIVE` locks and refuses
rollback when any Promotion-owned identity reservation or any independent,
consumption, junction, or handoff history exists. Pure derived
`artifact-revision` backfill rows may be discarded only after the guard proves,
while holding the complete lock set, that no Promotion-owned reservation or
history exists. Immutable production history is never discarded to make a
rollback convenient.

The `000073`–`000075` down migrations acquire the exclusive WIA rollout fence
before their first Evidence/Plan/Receipt relation. This is required even after
an empty `000081.down`: a draining old Promotion process can otherwise hold a
Plan locator `AccessShare` lock while waiting for Evidence, as the rollback
holds Evidence and waits for Plan. Operators still drain v2 workers and roll
back in strict reverse order; the fence makes an accidental overlap blocking
rather than deadlocking.

Migration `000080.down` may run only after `000082` and `000081` are absent and
its own input-precommit/admission history guard proves the deployment empty.
Rollback never removes an input authority authenticated by a surviving
Promotion closure.

## 12. Verification matrix

### 12.1 Canonical and semantic tests

- golden vectors for all seven domain hashes and PostgreSQL parity;
- strict rejection of unknown, duplicate, null, unsorted, oversized, and
  non-canonical documents;
- secret scanner rejects URL credentials, DSNs, tokens, cookies, headers,
  private keys, environment values, and filesystem secret paths;
- every single cross-binding mutation fails independently;
- explicit empty independent policy succeeds; missing/non-exact non-empty
  requirements fail closed.

### 12.2 Real PostgreSQL tests

- exact fresh consume and inspect-only replay;
- concurrent exact consume yields one insert and identical replays;
- changed operation/authority/handoff/output identity conflicts;
- fence/project-first lock-order canary with concurrent WIA migration locks;
- policy supersession, WIA drift, node status drift, target currency drift,
  Quality drift, BuildManifest/Contract drift, source/review drift;
- Plan raw input/hash/target/WIA mismatch;
- missing/reordered/corrupt Evidence event, head, operation, closure, or index;
- each missing/non-terminal/changed Receipt request or observation, wrong
  signer, wrong PAE/envelope/payload/completion, and receipt target mismatch;
- explicit empty independent policy succeeds, while every non-empty list fails
  before registry lookup regardless of ID, kind, hash, or registry contents;
- commit-unknown reconciliation, database unavailable, and inspect ambiguity;
- fake-driver proof that the session lock precedes `SERIALIZABLE BEGIN`, only
  definite `40001`/`40P01` attempts retry, and every ambiguous session state is
  discarded rather than unlocked or pooled;
- an independently authenticated Promotion LOGIN can invoke exactly consume,
  operation inspection, and historical-v1 inspection, but has no direct table
  access and cannot invoke either private handoff routine;
- exact owner/ACL/search-path/function contract and direct DML denial;
- empty rollback succeeds and any durable history blocks rollback.

### 12.3 No-bypass closure canary

The production canary must use normal migrations and runtime roles. It may not
set `session_replication_role=replica`, disable triggers, insert authority rows
directly, run as migration owner, or substitute an in-memory verifier.

It constructs and consumes this exact chain:

```text
approved reviewed sources + TemplateRelease
  -> BuildManifest + BuildContract + applied implementation
  -> passed Quality target
  -> workflow-engine/v3 activation + WIA
  -> Plan Authority + exact source/credential input-precommit authority
  -> exact artifact-indexed Evidence ledger at the Receipt-v3 cut point
  -> four authenticated Receipt v3 terminal observations
  -> independently verified terminal Receipt v3
  -> reviewed explicit-empty independent-authority policy for migration 000081
  -> Promotion v2 single-use consumption + pending handoff
```

The later independent-admission migration replaces the explicit-empty step
with the exact policy-required signed authority receipts and owns that expanded
no-bypass canary.

The canary must prove that no output revision exists and the workflow node is
still `waiting_qualification` after Promotion v2. That is the expected boundary,
not an incomplete commit. Migration `000082` tests own the later atomic
revision-and-node transition.

## 13. Acceptance criteria

Migration `000081` is complete as a disabled production building block only
when:

- historical v1 records remain readable and cannot receive new v3 state;
- the five-ID opaque command is the only consume input;
- all upstream authorities are locked and revalidated in the documented order;
- the full migration-80 `PromotionBinding` is one required typed
  `inputPrecommit` closure member, separate from policy-configured independent
  authorities;
- every currentness or exact-byte mismatch blocks;
- exactly one consumption and one pending handoff commit atomically;
- exact replay is inspect-only and commit-unknown is recoverable;
- no revision or workflow completion is created by Promotion v2;
- direct table access and unrelated routine execution are denied;
- static, unit, race, vet, migration, rollback, concurrency, and real
  PostgreSQL migration tests pass;
- production posture proves the consume worker remains disabled until both
  the migration-80 input-precommit and migration-81 updated consume no-bypass
  canaries pass; and
- product/operator documentation still reports external qualification as
  blocked unless real external evidence and every independent authority exist.
