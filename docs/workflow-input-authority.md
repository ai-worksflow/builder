# Immutable Workflow Input Authority

Status: implemented-internal; not production-enabled. Migration `000076`
owns the Canonical Review Approval Receipt authority and migration `000077`
hardens its append-only/OCC verifier boundary. Migration `000078` implements
both the project-scoped Qualification Policy Authority described in
`qualification-policy-authority.md` and the Workflow Input Authority below. It
does not reinterpret historical review rows, workflow execution profiles,
Qualification Receipt v2, or Promotion v1.

This document defines the immutable server authority that closes the boundary
between a passing workflow quality result and a new external-qualification
run. Its purpose is to answer one question without trusting browser state,
mutable workflow metadata, or a later reconstruction:

> Which exact project, workflow definition, run, node, upstream outputs,
> manifests, revisions, Canonical Review receipts, build contract, target, and
> qualification policy were authorized when the external-qualification gate
> became active?

The answer is one immutable Workflow Input Authority. It is committed in the
same PostgreSQL transaction that first moves the dedicated gate to
`waiting_qualification`. A Plan Authority may resolve it as an opaque input,
but it is not itself a Plan, evidence, Receipt, Promotion, immutable output
revision, workflow submission, or deployment approval.

## 1. Context and security boundary

### 1.1 Existing authorities

The implementation must compose, rather than duplicate, these existing
authorities:

- migration `000022` seals `application_build_contracts` and their source,
  template, and obligation projections after the creation transaction;
- migration `000073` owns the Qualification Evidence event store;
- migration `000074` owns the immutable Qualification Plan Authority and its
  opaque `InputAuthority` interface;
- migration `000075` owns the snapshot-first Qualification Receipt v3 durable
  request, observation, and terminal store; and
- migration `000076` owns database-authored, immutable Canonical Review
  Approval Receipts; and
- migration `000077` hardens their append-only decisions, exact OCC chain,
  canonical primitives, causal closure, and Solo Owner verification.

The new implementation order is fixed:

| Migration | Responsibility |
| --- | --- |
| `000076` | Canonical Review Approval Receipt authority; already allocated |
| `000077` | Canonical Review append-only/OCC authority hardening; already allocated |
| `000078` | Qualification Policy Authority and immutable Workflow Input Authority |
| `000079` | Workflow execution profile v3 and dedicated external-qualification gate |
| `000080` | Qualification Input Precommit Authority over exact WIA + current Policy + Plan source/credential authorization |
| `000081` | Qualification Promotion v2 with one mandatory typed `inputPrecommit` canonical closure member and pending handoff |
| `000082` | Workflow-side handoff, same-content immutable revision creation, and exact node completion |

Migration `000081` must not precede migration `000080` and must not manufacture
a Workflow/input-precommit authority from whatever mutable state happens to
exist when a Receipt arrives. Migration `000082` may consume only the updated
Promotion contract.

### 1.2 Facts that are not authorities today

The following existing data is useful input, but is not sufficient durable
lineage proof:

- `NodeInputEnvelope` is retained only under mutable
  `workflow_runs.context -> NodeMetadata.Input`;
- the envelope is currently built by `Engine.ExecuteLease` after a runnable
  node has already been claimed and outside the run-commit transaction;
- `workflow_node_runs` does not persist `definition_node_id` or `slice_id` and
  lacks an immutable composite node identity;
- run and node rows bind an InputManifest ID without also binding the exact
  manifest semantic hash and external content bytes;
- `workflow_run_events` has a sequence uniqueness constraint, but is not an
  append-only hash authority;
- artifact revision content identity does not by itself prove that the
  revision is still the required current/latest-approved upstream revision;
- BuildManifest state and external bytes do not form a complete immutable
  workflow input closure; and
- mutable `review_requests` and `review_decisions` must never be reassembled
  later and called a canonical approval receipt.

The existing `ArtifactReviewGate` approval projection is also not a Workflow
input authority. It now fails closed through the exact version-1 receipt
probe, but that Boolean result neither returns nor binds the expected receipt
hash/bytes. Workflow freeze therefore trusts only the owner-side receipt
resolver, never the projection or Boolean probe as an authority constructor.

`application_build_contracts` and their children are stronger: migration
`000022` validates their creation closure and prevents ordinary mutation.
Their exact IDs, hashes, raw content, and child source set are nevertheless
members of the new snapshot, and any currency policy required at Promotion
time must be revalidated separately.

### 1.3 Trust boundary

Only server code may request or resolve a Workflow Input Authority. A browser,
API DTO, model, runner, workflow worker, or qualification process may not
choose or assert any of the following:

- authority, operation, target revision, receipt, manifest, or profile hash;
- workflow definition version or node identity;
- predecessor set or edge/value mapping;
- Canonical Review request, decision, policy, or receipt;
- BuildManifest, BuildContract, TemplateRelease, Golden, trust, or source
  authority; or
- a flag that says the gate is approved, qualified, waivable, or complete.

The public command may identify only the project/run/node route needed for an
authenticated read or status display. The private freeze command and all hash
material are derived by the workflow service.

## 2. Canonical wire and hash contract

### 2.1 Versions and media types

The initial closed wire uses:

```text
worksflow-workflow-input-freeze-request/v1
application/vnd.worksflow.workflow-input-freeze-request+json;version=1

worksflow-workflow-input/v1
application/vnd.worksflow.workflow-input+json;version=1

worksflow-workflow-input-authority/v1
application/vnd.worksflow.workflow-input-authority+json;version=1
```

The external gate additionally pins these exact downstream contracts:

```text
worksflow-qualification-plan-authority/v1
worksflow-qualification-receipt/v3
worksflow-qualification-promotion-consume/v2
```

No v1 wire field may be added, removed, made optional, or reinterpreted after
release. A widened document requires a new schema and hash domain.

### 2.2 Canonical JSON

The new package should live at
`backend/internal/workflowinputauthority`. It must provide a frozen strict
canonical implementation with the same defensive properties as
`canonicalreviewreceipt` and Qualification Receipt v3:

- BOM-free valid UTF-8;
- strict decode with duplicate and unknown object names rejected;
- object keys ordered by their UTF-8 bytes;
- exact EOF after one JSON document;
- integers only, bounded to the JavaScript-safe integer range;
- bounded strings, arrays, documents, and total retained bytes;
- canonical UUIDv4 lowercase strings where UUIDv4 is required;
- lowercase `sha256:<64 hex>` digests; and
- fixed UTC timestamps with six fractional digits whose last three digits are
  zero when the database authority is millisecond-granular.

Legacy Workflow JSON may contain timestamps, optional values, or number forms
that are not valid members of this new strict wire. Therefore Definition,
run-scope, NodeInputEnvelope, InputManifest, BuildManifest, and BuildContract
raw documents are retained separately as exact bytes. The strict input
document contains their scalar identities, sizes, semantic hashes, and raw
byte digests; it does not embed arbitrary legacy JSON values.

### 2.3 Domain-separated document hashes

New authority documents use this framing:

```text
SHA256(
  UTF8("worksflow-workflow-input-authority-hash/v1") || 0x00 ||
  UTF8(domain) || 0x00 ||
  exactCanonicalBytes
)
```

The result is encoded as `sha256:<64 lowercase hex>`. Initial domains are:

```text
worksflow.workflow-input.freeze-request/v1
worksflow.workflow-input.target/v1
worksflow.workflow-input.input/v1
worksflow.workflow-input.authority/v1
```

The Authority document never contains its own `AuthorityHash`.

Exact retained legacy/external bytes additionally receive an ordinary raw
SHA-256 digest:

```text
RawSHA256(bytes) = "sha256:" + lowercase_hex(SHA256(bytes))
```

Raw byte digests and established semantic hashes are not interchangeable. For
example, a NodeInput binding carries both:

- normalized legacy `NodeInputEnvelope.Hash()` for semantic compatibility;
- `RawSHA256(exact envelope bytes)` for byte identity.

The same rule applies to Definition, run scope, InputManifest,
BuildManifest, BuildContract, and the copied Canonical Review receipt bytes.

## 3. Exact snapshot wire

### 3.1 Server-owned FreezeRequest

`FreezeRequest` is a retained record of the attempted transition, not a public
input DTO:

```json
{
  "authorityId": "uuid-v4",
  "expectedRunCursor": 42,
  "mediaType": "application/vnd.worksflow.workflow-input-freeze-request+json;version=1",
  "nodeKey": "external-qualification",
  "nodeRunId": "uuid-v4",
  "operationId": "uuid-v4",
  "projectId": "uuid-v4",
  "schemaVersion": "worksflow-workflow-input-freeze-request/v1",
  "workflowRunId": "uuid-v4"
}
```

`operationId` and `authorityId` are service-owned. They may be generated once
and retained by the in-memory mutation, or deterministically derived as
RFC-4122 variant/version-4-shaped values from a domain, run ID, node-run ID,
and InputHash. They must never come from the browser. Strong inspection by
`(workflow_run_id, node_run_id)` is mandatory after process restart.

### 3.2 WorkflowInputDocument

The canonical input is a closed scalar/digest document. The following shape is
normative at the field-group level; implementation types must make every
variant closed and must not use untyped extension maps.

```json
{
  "build": {
    "buildContract": {
      "contentHash": "sha256:...",
      "contractHash": "sha256:...",
      "id": "uuid-v4",
      "rawBytesHash": "sha256:...",
      "rawBytesSize": 1,
      "statusAtFreeze": "ready"
    },
    "buildManifest": {
      "contentHash": "sha256:...",
      "id": "uuid-v4",
      "manifestHash": "sha256:...",
      "rawBytesHash": "sha256:...",
      "rawBytesSize": 1,
      "statusAtFreeze": "frozen"
    }
  },
  "definition": {
    "definitionHash": "sha256:...",
    "definitionId": "uuid-v4",
    "definitionVersion": 3,
    "definitionVersionId": "uuid-v4",
    "executionProfileHash": "sha256:...",
    "executionProfileVersion": "workflow-engine/v3",
    "rawBytesHash": "sha256:...",
    "rawBytesSize": 1
  },
  "gate": {
    "activationEventId": "uuid-v4",
    "activationEventSequence": 43,
    "definitionNodeId": "external-qualification",
    "gateName": "external-qualification",
    "nodeKey": "external-qualification",
    "nodeRunId": "uuid-v4",
    "nodeType": "external_qualification_gate",
    "sliceIdentity": {"kind": "root"},
    "stageGate": "external-qualification"
  },
  "inputManifests": [],
  "mediaType": "application/vnd.worksflow.workflow-input+json;version=1",
  "nodeInput": {
    "bindingCount": 1,
    "rawBytesHash": "sha256:...",
    "rawBytesSize": 1,
    "semanticHash": "sha256:..."
  },
  "predecessors": [],
  "project": {
    "governanceMode": "solo",
    "id": "uuid-v4"
  },
  "qualificationPolicy": {
    "authorityHash": "sha256:...",
    "authorityId": "uuid-v4",
    "externalGatePolicy": "external-qualification/v1"
  },
  "qualityResult": {
    "buildManifestHash": "sha256:...",
    "buildManifestId": "uuid-v4",
    "passed": true,
    "qualityRunId": "uuid-v4",
    "workspaceRevisionContentHash": "sha256:...",
    "workspaceRevisionId": "uuid-v4"
  },
  "reviewReceipts": [],
  "revisions": [],
  "run": {
    "id": "uuid-v4",
    "inputManifestHash": "sha256:...",
    "inputManifestId": "uuid-v4",
    "scopeRawBytesHash": "sha256:...",
    "scopeRawBytesSize": 1,
    "startedAt": "2026-07-19T00:00:00.000000Z",
    "startedBy": "uuid-v4"
  },
  "schemaVersion": "worksflow-workflow-input/v1",
  "target": {
    "manifestSubject": "stable-subject",
    "nodeKey": "external-qualification",
    "projectId": "uuid-v4",
    "stageGate": "external-qualification",
    "targetRevisionContentHash": "sha256:...",
    "targetRevisionId": "uuid-v4",
    "workflowRunId": "uuid-v4"
  },
  "targetHash": "sha256:..."
}
```

The literal `[]` arrays above stand for the closed bindings in the following
sections. Empty arrays are permitted only where the profile explicitly allows
them; the external-qualification profile must require at least one
predecessor, one InputManifest, one revision, and all profile-required review
receipts.

### 3.3 Predecessor and NodeInput closure

`predecessors` is sorted by `(edgeId, sourceNodeRunId, sourcePort, targetPort)`
and each item binds:

- edge ID, source/target port, mapping kind, and ordinal;
- source node row ID, node key, definition node ID, node type, and slice;
- exact source status `completed` and output revision number;
- output proposal and proposal InputManifest ID/hash when present;
- output artifact/materialized revision IDs and content hashes;
- delivery-slice references;
- output hash and mapped value hash.

The set must equal the rebuilt v3 `NodeInputEnvelope`; a caller may not omit an
edge or add an unrelated revision. `NodeInputEnvelope` should be built by a
pure v3 input builder from the post-quality in-memory run state before commit,
then rebuilt/cross-checked against locked rows by the store boundary.

The implementation must extend typed output parsing so the passing
`QualityResult.WorkspaceRevision` and BuildManifest identity are explicit
fields. Promotion target values must not be recovered from an arbitrary
`json.RawMessage`, client payload, or artifact-reference guess.

### 3.4 InputManifest bindings

`inputManifests` is sorted by `(role, manifestId)` and includes every distinct:

- run InputManifest;
- predecessor output InputManifest;
- node/proposal-pin InputManifest; and
- profile-required qualification manifest projection.

Each member carries role, ID, project, kind, schema version, established
semantic manifest hash, content store/ref, external content hash, exact raw
byte digest, and size. The freeze service independently decodes the raw
manifest, recomputes its established Go semantic hash, verifies that all
source members are present, and then copies the bytes into PostgreSQL.

No authority read may later fetch a manifest by ID and silently accept a new
content ref or a self-consistent replacement document.

### 3.5 Revision and build closure

`revisions` is sorted by `(purpose, artifactId, revisionId)` and binds:

- purpose and artifact kind;
- artifact ID, revision ID, content hash, content store/ref, schema, and size;
- exact workflow status at freeze;
- exact revision `changeSourceAtFreeze` and BuildContract
  `sourceRequiredAtFreeze` facts;
- source manifest/proposal/implementation-proposal IDs;
- whether the revision was artifact latest/current and latest-approved at
  freeze; and
- the qualification-policy-derived `canonicalReviewRequired` decision and a
  closed currency policy: `exact-approved` or `latest-approved-required`.

The Workspace target must use `latest-approved-required`. Governed source
revisions normally use the same policy; an immutable historical source may
use `exact-approved` only when the v3 qualification policy says so explicitly.
The current active policy authority derives both fields from locked revision
and BuildContract facts. The private candidate repeats those values only as an
expected assertion; neither a browser nor the Workflow caller can lower the
currency or review rule. An exact-approved exception never lowers the review
rule selected for that revision's change source. Promotion v2 applies the
recorded policy rather than inventing a stricter or weaker rule later.

BuildManifest and BuildContract bindings include both their table-level
identity/hash facts and exact finalized content bytes. BuildContract child
source/template/obligation ordinals must be compared with the immutable
relational projection created by `000022`. The passing `QualityResult`,
NodeInputEnvelope, BuildManifest, BuildContract, and target must all resolve to
the same Workspace revision and source closure.

### 3.6 Canonical Review Receipt bindings

Workflow Input Authority must reuse migration `000076`; it must not snapshot
mutable review rows a second time.

For every revision that the v3 qualification policy marks canonical-review
required, `reviewReceipts` contains a sorted binding with:

```text
purpose
reviewRequestId
receiptHash
receiptRawBytesHash
receiptRawBytesSize
projectId
artifactId
revisionId
revisionContentHash
receiptSchemaVersion = worksflow-canonical-review-approval-receipt/v1
```

The `000076` authority has no separate receipt ID. Its durable identities are
the primary-key `review_request_id`, globally unique `receipt_hash`, and
globally unique `revision_id`. Its media type is
`application/vnd.worksflow.canonical-review-approval-receipt+json;version=1`.
`receiptHash` is the established domain-separated digest using prefix
`worksflow-canonical-review-authority-hash/v1` and domain
`worksflow.canonical-review.receipt/v1`; it is opaque to this package and must
not be replaced by `RawSHA256(receipt_bytes)`. `receiptRawBytesHash` is retained
separately for that raw-byte purpose.

The Workflow caller supplies the typed required revision and purpose, not a
receipt ID or expected receipt hash. After acquiring the project mutex, the
owner-owned freeze function selects the unique `000076` receipt for that
project/revision, obtains its stored `receipt_hash`, and calls
`resolve_canonical_review_approval_receipt(project_id, revision_id,
receipt_hash)`. It constructs the canonical review binding from the resolved
row and copies the exact `receipt_bytes` and `receipt_document` into a
Workflow-authority child row. Before commit, Go independently verifies the
returned bytes with `canonicalreviewreceipt.Decode`.

Neither browser input nor ordinary application code gets to choose which
receipt hash is expected. At Promotion time the expected hash comes from the
already-frozen Workflow authority and is resolved again through the same
owner-side function.

Resolver outcomes `WCR01` (invalid shape), `WCR02` (exact authority absent or
conflicting), and `WCR03` (durable receipt corruption) are all blocking. None
may be downgraded to a warning or replaced by the current review projection.

This provides the complete immutable request, revision, policy, decisions,
governance, Solo Owner, approval, and component-digest closure already issued
by `000076`. The new authority must not:

- read current decisions and recompute a substitute decision set;
- accept `review_authority_version = 0` legacy state;
- treat `review_requests.status = 'approved'` as sufficient;
- accept only `canonical_review_approval_receipt_is_exact(...) = true`
  without also retaining the exact receipt identity and bytes; or
- issue a receipt while freezing Workflow input.

Receipt issuance remains part of the atomic Canonical Review close
transaction. Workflow freeze only resolves an already committed receipt.

Migration `000078` should add the exact composite uniqueness needed for a
relational binding to
`canonical_review_approval_receipts(review_request_id, receipt_hash,
project_id, artifact_id, revision_id, revision_content_hash)` if it is not
already available. Application and Promotion roles still receive no direct
table access.

There is one required product decision. Current Implementation Apply creates
a system-derived approved/current Workspace revision without an ordinary
ReviewRequest. The recommended policy is:

- every governed HumanEdit source revision in the BuildContract closure must
  carry its exact `000076` receipt; and
- the Workspace target is the system-derived result of those exact reviewed
  inputs and is separately bound by quality and lineage.

If product policy requires the Workspace target itself to have a Canonical
Review Receipt, the workflow must add a real Workspace review stage before
external qualification. Promotion v2 must not claim that such a receipt exists
under the current Apply behavior.

### 3.7 Authority envelope

The final authority envelope is compact and contains no recursive hash:

```json
{
  "authorityId": "uuid-v4",
  "inputHash": "sha256:...",
  "mediaType": "application/vnd.worksflow.workflow-input-authority+json;version=1",
  "nodeRunId": "uuid-v4",
  "operationId": "uuid-v4",
  "projectId": "uuid-v4",
  "requestHash": "sha256:...",
  "schemaVersion": "worksflow-workflow-input-authority/v1",
  "targetHash": "sha256:...",
  "workflowRunId": "uuid-v4"
}
```

`AuthorityHash` is the domain hash of these exact canonical bytes. The Store
retains request, target, input, and envelope raw bytes, JSONB documents, hashes,
and scalar projections. `frozen_at` is assigned by PostgreSQL and is not part
of InputHash or AuthorityHash.

## 4. Freeze transaction and engine integration

### 4.1 Dedicated transition

Profile v3 introduces exactly one dedicated node:

```text
Workbench
  -> blocking release QualityGate
  -> external-qualification
  -> production Publish
```

The Workflow input freezes when a passing release Quality result is applied
and `reconcileV3` first transitions that external node from `pending` to
`waiting_qualification`.

It must not freeze:

- when a browser opens the node;
- when a user clicks an approval button;
- during autosave;
- in `AuthorizeNodeExecution`;
- when `ClaimRunnable` claims a worker lease;
- in `ExecuteLease`; or
- only after a Plan, Receipt, or Promotion request arrives.

### 4.2 Engine changes

The implementation should add profile-specific paths without changing frozen
v0/v1/v2 behavior:

- `Engine.applyResultV3`, modeled on but separate from
  `applyResultV0V1Frozen`;
- `Engine.reconcileV3`;
- a v3 typed `QualityResult` and NodeInput builder;
- `WorkflowInputFreezeCandidate` on `RunMutation`; and
- a mutation-builder operation that carries exact bytes and scalar
  projections to the store.

`reconcileV3` must derive exactly one target from a typed, passing upstream
Quality result. Zero, multiple, failed, untyped, mismatched BuildManifest, or
missing Workspace revisions block the transition.

The external node is never sent to a generic runner. It has no lease execution
profile and never becomes ordinary `ready`.

### 4.3 Content preflight

Legacy/external content reads happen before beginning the PostgreSQL
transaction so a slow Mongo read does not hold the project mutex. The preflight
must:

1. use the lower-level content Store, not the lossy Workflow adapter;
2. require `Finalized` state;
3. verify project, aggregate type/ID, schema, content ref, and content hash;
4. strictly decode/recompute each established semantic hash;
5. build bounded raw byte copies and their digests; and
6. reject credentials, tokens, cookies, headers, private keys, environment
   values, absolute host paths, or other forbidden secret fields.

Inside PostgreSQL, the freeze transaction locks every pointer row and verifies
that all refs, hashes, sizes, and scalar identities still equal the preflight
candidate. Any drift restarts reconciliation from current state; it is not
patched into the old candidate.

### 4.4 Atomic commit

`GORMStore.Commit` must use one transaction with this order:

```text
lock projects(id) FOR UPDATE
  -> lock and CAS workflow run cursor
  -> lock exact node rows
  -> lock and resolve the current active qualification-policy generation
  -> lock exact artifacts/revisions in stable order
  -> lock manifests and BuildManifest/BuildContract rows in stable order
  -> resolve exact immutable Canonical Review receipts
  -> rederive and validate the complete candidate
  -> freeze Workflow Input Authority
  -> set node waiting_qualification and its authority FK
  -> append activation event and outbox record
  -> commit
```

Authority row, node state, authority FK, and activation event are one atomic
fact:

```text
WorkflowInputAuthority committed
  if and only if
external-qualification gate activated with that exact AuthorityID
```

A rollback leaves neither activation nor authority. A store or service using a
separate database connection/transaction for freeze is invalid.

On a normal CAS conflict, reload the run and rebuild the freeze candidate. The
cached upstream worker result may be reused only if its lease/result identity
is still exact; the snapshot candidate itself is never reused without locked
revalidation. If the authority already committed, inspect and return the
immutable result instead of rebuilding it from current state.

## 5. PostgreSQL authority model

### 5.1 Tables

Migration `000078_workflow_input_authority.up.sql` should create:

```text
workflow_input_authorities
workflow_input_authority_identity_reservations
workflow_input_authority_predecessors
workflow_input_authority_manifests
workflow_input_authority_revisions
workflow_input_authority_review_receipts
```

`workflow_input_authorities` retains:

- authority and operation UUIDs;
- request/input/target/envelope raw bytes, JSONB, and domain hashes;
- project, definition version, execution profile, run, node, gate, target,
  Quality result, BuildManifest, and BuildContract scalar identities;
- Definition, run-scope, and NodeInput exact raw bytes/digests;
- activation event ID/sequence;
- database-authoritative millisecond `frozen_at`; and
- explicit member counts for every child set.

Required uniqueness includes:

```text
PRIMARY KEY (authority_id)
UNIQUE (operation_id)
UNIQUE (workflow_run_id, node_run_id)
UNIQUE (authority_hash)
```

Identity reservations prevent an operation UUID, authority UUID, or other
locally allocated Workflow authority identity from being reused in another
role. Request and authority IDs must be distinct.

Children use `(authority_id, ordinal)` primary keys, contiguous zero-based
ordinals, exact set counts, and stable per-kind uniqueness. Deferred constraint
triggers verify that parent counts, child ordinals, and canonical input arrays
agree before commit.

`workflow_input_authority_review_receipts` stores the exact copied `000076`
receipt bytes/document plus receipt hash, raw bytes hash, and target scalars. It
references the existing receipt identity and never stores reconstructed
mutable decision rows.

All retained Definition, run-scope, NodeInput, manifest, revision,
BuildManifest, BuildContract, and copied receipt bytes are also subject to one
aggregate 128 MiB limit. Per-member limits do not replace this aggregate
check, and the issuer must reject before constructing or persisting an
oversized recovery bundle.

### 5.2 Stable Workflow identity

Migration `000078` should add nullable historical columns to
`workflow_node_runs`:

```text
definition_node_id
slice_id or a closed root/slice discriminator
input_authority_id
```

New profile-v3 nodes require the stable definition identity. The external gate
requires all fields and an exact deferred FK to its Workflow authority. A
trigger prevents mutation of run ID, node key, node type, definition-node ID,
and slice identity after insertion. Historical rows remain readable without
inventing a false backfill.

Because the authority references the exact node and activation event while the
node references the authority, those composite FKs must be `DEFERRABLE
INITIALLY DEFERRED` and validated as a closed cycle at commit. They are not
temporarily disabled or repaired after commit.

The bound activation event's actor and database timestamp are immutable facts
as well as its ID, run, sequence, type, node key, and payload. Later event-row
updates, identity moves, deletes, or delete-and-reinsert replacements must not
make those facts diverge from the already-enqueued outbox payload. The event
identity guard resolves `OLD.id` before `NEW.id` and rejects deletion of a
reserved activation event even though its authority FK is initially deferred.

Authority FKs use `ON DELETE RESTRICT`. An authority must prevent deletion of
its project, workflow run, node run, target revision, manifests, receipts, and
other referenced identities even if older tables normally use cascading
deletes.

### 5.3 Functions

Migration `000078` should expose only narrow, owner-owned functions:

```text
freeze_workflow_input_authority_v1(...)
inspect_workflow_input_authority_operation_v1(operation_id)
resolve_workflow_input_authority_v1(authority_id)
resolve_workflow_input_authority_for_node_v1(workflow_run_id, node_run_id)
assert_current_workflow_input_authority_v1(authority_id)
```

Freeze validates:

- exact canonical raw byte/hash/JSONB equality;
- request/input/target/envelope cross-bindings;
- exact project/run/node/profile/target facts;
- complete predecessor/manifest/revision/receipt member sets;
- copied `000076` receipt validity and target equality;
- finalized external content pointer equality;
- BuildManifest/BuildContract closure; and
- idempotent identity ownership.

The freeze function is the database issuer of the final request, target,
input, and authority documents. It builds them from typed, locked facts and
the bounded exact content supplied by server preflight, canonicalizes them in
PostgreSQL, and returns all raw bytes to Go for independent strict decoding and
hash verification before the surrounding workflow transaction commits. It
does not accept a caller-authored InputHash, AuthorityHash, review receipt
binding, or target document as truth.

`assert_current...` is a read-and-lock primitive for Promotion v2, not a
generic approval function. It applies the frozen currency policy and returns
the exact frozen projection only when every required current fact still
matches.

Every authority table rejects `UPDATE`, `DELETE`, and `TRUNCATE`. The down
migration takes the required exclusive locks and refuses to drop the authority
while any parent, child, reservation, node authority FK, Promotion, or handoff
fact exists.

### 5.4 Ownership and ACL

All tables and functions are owned by `worksflow_migration_owner` when that
role exists. Every function has a fixed, catalog-qualified `search_path`.
Revoke all table and function privileges from PUBLIC and unrelated roles.

Intended capabilities are:

| Principal | Capability |
| --- | --- |
| migration owner | schema ownership and controlled maintenance |
| workflow application runtime | exact freeze/inspect through the workflow transaction only; no direct table DML |
| Plan input operator | resolve one opaque authority through a restricted projection; no raw generic table access |
| Promotion v2 operator | execute `assert_current...` only as part of consume composition |
| browser/API user | authenticated status projection only; no authority command or raw bytes |
| auditor | separately approved catalog/receipt projection if required; never write |

Owner access, a passing migration test, or direct DBA access is not production
posture evidence. Runtime roles, LOGINs, DSNs, secret delivery, function owner,
ACL, trigger wiring, index opclasses, and `search_path` must be checked by the
production PostgreSQL posture authority. That authority requires the exact 23
Policy/WIA triggers from migration `000078`, the exact five profile-v3 triggers
from `000079`, and the ten exact trigger functions. It counts every user trigger
on the ten dedicated authority tables and pins shared-table trigger names,
event/timing/level bits, `UPDATE OF` columns, enablement, routine binding and
deferred `pg_constraint` attributes. Each of the ten migration `000078`/`000079`
trigger functions is independently pinned to its exact owner-only ACL,
signature, return/language, security mode, volatility, strictness, parallel
safety and search path. The four shared
definition/run/node/event tables also have an exhaustive twelve-trigger
allowlist, including the three legacy profile/governance triggers and their
three separately cataloged invoker functions. The historical functions retain
only their exact owner-plus-application, non-grantable ACL with no third-party
grantee; an arbitrary-name trigger on a shared table therefore cannot hide
outside the Policy/WIA/v3 prefixes.

## 6. Profile v3 and the non-waivable gate

### 6.1 Frozen compatibility

`workflow-engine/v0`, `/v1`, and `/v2` descriptors, hashes, capabilities,
validation, reconciliation, and waiver behavior are immutable history. Do not
add fields that change their canonical descriptor bytes. New shared fields
must be omitted from historical descriptors or, preferably, be expressed in a
separate v3 descriptor type.

Profile v3 becomes the new `Current` alias only after migrations `000076`,
`000077`, and `000078`, the production Workflow authority resolver, and the handoff runtime
are sealed and readiness-tested.

Suggested domain additions:

```text
NodeExternalQualificationGate = "external_qualification_gate"
NodeWaitingQualification      = "waiting_qualification"
RunWaitingQualification       = "waiting_qualification"
```

Capabilities move to version 5 and include a structured closed declaration:

```json
{
  "blocking": true,
  "gateName": "external-qualification",
  "inputAuthoritySchema": "worksflow-workflow-input-authority/v1",
  "promotionProtocol": "worksflow-qualification-promotion-consume/v2",
  "receiptSchema": "worksflow-qualification-receipt/v3",
  "waiverPolicy": "never"
}
```

A generic `QualityGates: ["external-qualification"]` label is not sufficient
because current generic QualityGate waiver rules would permit unintended
paths.

### 6.2 Topology validation

`execution_profile_validation.go` must require:

- exactly one dedicated external gate;
- one blocking release QualityGate immediately upstream;
- production Publish strictly downstream;
- every successful terminal path crosses the external gate;
- no condition, fanout, merge, optional edge, or alternative Publish bypass;
- exact input-authority/Receipt/Promotion schema versions; and
- no `AllowWaiver`, manual approval, runner, or retry configuration.

### 6.3 Backend entry-point denial

The gate remains blocked unless the private `000082` handoff consumer supplies
an exact consumed Promotion v2 result and creates the immutable output
revision. The backend must explicitly reject this node type in:

- `Engine.AuthorizeNodeExecution` and `Facade.AuthorizeExecution`;
- `Engine.SubmitHumanInput` and `Facade.Resume`;
- `Engine.RecordProposal`;
- `Engine.ResolveReview` and `Facade.ResolveReview`;
- `Engine.WaiveNode` and `Facade.Waive`;
- `Engine.RetryNode` and `Facade.Retry`;
- `ClaimRunnable`/lease acquisition; and
- every generic node-completion or administrative transition.

`Cancel` may terminate the whole run but must not mark the gate qualified or
allow downstream Publish. No role, including Solo Owner, admin, migration
operator, or project owner, receives a waiver route. `NodeMetadata.Waived`, a
configuration boolean, a reason string, zero blocking comments, or an enabled
UI button is never proof.

UI capability projection should display the exact waiting status and recovery
information, and hide approval/waive/retry/submit controls. This is a usability
projection only; backend denial is the security boundary.

## 7. Plan, Receipt v3, Promotion v2, and handoff binding

### 7.1 Production Plan InputAuthority adapter

`qualificationplanauthority.InputAuthority` expects a complete
`ResolvedInputs`, not merely Workflow hashes. A production adapter should
implement:

```text
WorkflowQualificationInputAuthority.Resolve(workflowAuthorityID)
```

It performs:

```text
resolve and revalidate immutable Workflow Input Authority
  -> resolve its exact qualification-policy authority
  -> resolve QualificationManifest projection
  -> resolve BuildManifest/BuildContract and source authority
  -> resolve reviewed TemplateRelease
  -> resolve Golden fixture/runtime authority
  -> resolve credential, KMS, output, and trust authorities
  -> construct complete qualificationplanauthority.ResolvedInputs
```

The Plan Freeze command's opaque `InputAuthorityID` is the Workflow Authority
UUID. Environment-owned bindings are selected only through the exact
qualification-policy authority frozen in the Workflow input. A resolver must
not pick whatever Template, Golden, KMS, or trust policy is current when a
retry happens.

Plan Authority remains separately immutable and retains its own InputHash,
ProjectionHash, EvidencePlanHash, TargetHash, TrustHash, and AuthorityHash.
Workflow AuthorityHash is not reinterpreted as any of these.

### 7.2 Receipt v3 binding

Receipt v3 must continue to bind its exact Plan Authority, Evidence head,
pre-Receipt snapshot, signer requests/observations, BuildManifest,
BuildContract, source, TemplateRelease, Golden runtime, trust policy, and
PromotionTarget. It must not copy a caller-supplied Workflow snapshot and call
it expected input.

The durable relation is:

```text
Workflow Authority ID/hash
  -> Plan.InputAuthorityID and exact Plan input bytes
  -> migration 000080 Input Precommit binds exact current Policy source/credential
     authorization to that WIA+Plan pair through two distinct admissions
  -> Evidence Plan/head and immutable observations
  -> terminal Receipt v3 payload/envelope
```

### 7.3 Promotion v2 transaction

Migration `000081` implements a new ledger. It does not project Receipt v3 into
the historical `000071` Promotion consumer.

The complete wire, storage, lock, recovery, independent-authority, ACL, and
verification contract is defined in `docs/qualification-promotion-v2.md`.
This section is the cross-plane summary; the dedicated contract is normative
for the migration and Go adapter.

Promotion v2 first locks the project mutex, then locks and verifies:

- Workflow Authority raw bytes/hash and `assert_current...` result;
- exact run/profile and gate node still in `waiting_qualification` with the
  same AuthorityID;
- exact passing Quality result and Workspace target revision/content hash;
- current/latest-approved target and every frozen revision currency policy;
- BuildManifest, immutable BuildContract, and complete source closure;
- every copied `000076` receipt hash/bytes/target;
- Plan Authority raw input, `InputAuthorityID`, target, and AuthorityHash;
- the exact migration `000080` input precommit and both local receipt admissions,
  with the full `PromotionBinding` placed in required typed canonical
  `inputPrecommit`, separate from Policy independent authorities;
- Evidence operation/head/event closure and artifact index;
- all Receipt v3 seal/verify/sign observations and terminal bytes;
- Receipt target equal to both Plan target and Workflow target; and
- any independent ModelProfile or production PostgreSQL posture receipt
  required by the gate policy.

It must not require the entire run context or current event cursor to remain
unchanged: qualification itself advances independent ledgers and status
events. It verifies the frozen activation event and the explicitly bound facts
only.

Any mismatch is stale/blocking. Promotion never rebases an old Receipt onto a
new Workspace, BuildManifest, review receipt, qualification policy, or gate
generation.

On success, Promotion v2 atomically appends one single-use consumption record
and one hash-bound `pending` handoff with preallocated output revision ID. It
does not create the revision and does not complete the workflow node.

### 7.4 Workflow handoff

Migration `000082` and its private workflow-side consumer process the pending
handoff. Its complete same-content Revision, transaction-grant, role, recovery,
and verification contract is defined in
[Qualification Promotion v2 Workflow Handoff](./qualification-handoff-v1.md).
In one project-first PostgreSQL transaction they:

1. inspect or claim the exact immutable handoff;
2. re-resolve Workflow Authority and terminal Promotion v2 result;
3. re-lock target/run/node and apply final CAS checks;
4. create the profile-declared immutable promotion output revision with exact
   sources to Workflow Authority, Plan, Receipt, and Promotion;
5. only after that insert succeeds, complete the external gate with one full
   typed passing `QualityResult` referring to that exact revision;
6. append terminal handoff plus the exact gate-completed and
   publish-authorization-required Workflow/Outbox event pairs; and
7. leave downstream production Publish and the run in `waiting_input` until a
   later authenticated `ActionPublish` command supplies actor provenance.

Revision creation, node completion, terminal handoff, run cursor, and events
commit atomically. It must be impossible to observe `node completed` without
the immutable output revision, or a terminal handoff without exact node
submission.

## 8. Cross-plane locks, content safety, and recovery

### 8.1 Project-first lock order

Any writer capable of changing a fact included in the Workflow snapshot must
take `projects(id) FOR UPDATE` before its first mutable project fact. Required
writers include:

- `GORMStore.CreateRun` and `GORMStore.Commit`;
- artifact draft, revision, lifecycle, and current-pointer writers;
- Proposal manifest creation/application and source-membership writers;
- Canonical Review submit/decision/close/receipt issuance paths;
- Implementation Apply, stale, and quarantine transitions;
- Workbench bundle/rebase and BuildManifest group activation;
- delivery-slice current-pointer writers; and
- project governance and membership writers; and
- Promotion v2 and the Workflow handoff consumer.

The cross-plane order is:

```text
project
  -> workflow run and node
  -> artifacts and revisions, sorted by stable identity
  -> InputManifest, BuildManifest, BuildContract, sorted
  -> immutable Canonical Review receipts, sorted
  -> Qualification Evidence events / operations / heads
  -> Plan authorities / identity reservations
  -> Receipt requests / observations / terminal receipts
  -> Promotion v2 consumption / handoff
```

Qualification-only writers that already lock Evidence, Plan, or Receipt rows
must never attempt to acquire the project mutex afterward. They may finish
without a project lock; Promotion is the first composition point and locks
project before entering those planes. This prevents a project-to-Evidence /
Evidence-to-project cycle.

All multi-row locks use one documented stable ordering. Rollback migrations,
catalog posture checks, and maintenance procedures must follow compatible
relation-lock ordering.

#### 8.1.1 Migration 78/79 rollout fence

Migrations `000078` and `000079` are a deliberate exception to a single-step
"migrate, then roll API" deployment. They alter relations used by live
workflow transactions, while historical `Commit` and the new Workflow Input
paths otherwise have opposite project/run lock acquisition opportunities.
The shared/exclusive advisory key is:

```text
worksflow:workflow-input-authority-migration:v1
```

Use one of these reviewed deployment sequences:

```text
preferred zero-downtime sequence
  -> deploy fence-only runtime code first
     (GORMStore.CreateRun, GORMStore.Commit, Freeze, AssertCurrent take shared)
  -> confirm every old unfenced workflow writer is drained
  -> run the one-shot migrator
     (78/79 up and down take exclusive before any relation)
  -> deploy/enable the v3 external-qualification runtime
```

or:

```text
maintenance sequence
  -> quiesce all workflow writers and qualification operators
  -> wait for their transactions to drain
  -> run the one-shot migrator
  -> start the matching runtime
```

Do not roll an unfenced writer concurrently with either migration and do not
enable profile v3 between 78 and the later Promotion/handoff migrations. The
shared lock is compatible across independent projects; it is not a global
runtime serialization mutex. The exclusive migration side waits for every
shared holder and prevents a project/run/WIA relation-lock cycle.

`PostgresStore.Freeze` takes the shared key before its idempotency inspection;
that inspection is already a WIA relation read. Its caller-owned transaction
must therefore be pristine when passed to `Freeze`, unless the transaction
entrypoint acquired this exact shared key before any project, run, node, or WIA
query. Resolving candidate bytes happens before opening that activation
transaction; a caller must not read workflow rows and only then attempt to join
the fence.

The same rule applies to `AssertCurrentTx`. Promotion must call
`PostgresStore.AcquireMigrationFence` as its transaction's first database
statement, then take the project mutex and only afterward call
`AssertCurrentTx`. The defensive shared-lock call inside `AssertCurrentTx`
cannot repair an already inverted caller transaction.

`InspectOperation`, `ResolveAuthority`, `ResolveNode`, and their SQL resolver
capabilities are standalone, autocommit reads. They do not retain a fence after
the result is returned and must not be followed by project/Plan/Promotion locks
inside the same explicit transaction. A future transaction-bound resolver must
first use `AcquireMigrationFence`; otherwise down-migration project→WIA locks
can be inverted. The production Plan adapter resolves WIA bytes before opening
its separate Plan-freeze transaction.

### 8.2 Content-store transaction crack

`backend/internal/workflow/platform_adapters.go` currently has a dangerous
ordering in `CoreContentStoreAdapter`: it finalizes pending content before the
PostgreSQL row that references it has committed. A subsequent PG failure can
leave a finalized unreachable object; the reconciler scans pending objects and
cannot repair that orphan.

Workflow Input Authority avoids this path:

- it creates no new external snapshot object;
- it accepts only exact referenced `Finalized` objects;
- it validates them before the PG transaction;
- it rechecks pointer rows inside the transaction; and
- it copies all authority-critical raw bytes into immutable PostgreSQL
  `bytea` rows.

The general content writer should later be corrected to:

```text
PutPending -> commit PostgreSQL reference -> Finalize
```

If a new authority object is ever stored externally, its reference table must
be added to reconciler discovery. On ambiguous PG commit, do not abort or issue
a replacement object until strong inspection proves that no reference
committed.

Pending, aborted, missing, wrong-project, wrong-aggregate, ref-drifted, or
hash-mismatched content is blocking for a new qualification. A trusted repair
may finalize the exact already-referenced pending object through the normal
reconciler; freeze must not accept arbitrary pending bytes.

### 8.3 Idempotency and commit-unknown

The store provides both operation inspection and stable node inspection:

```text
InspectOperation(operationId)
InspectNode(workflowRunId, nodeRunId)
```

For a new attempt, inspect by node before resolving content. If an exact
authority exists, independently verify and return it. This makes replay
inspect-only even if the original external content or policy has since been
retired.

Concurrent exact freezes converge on one `(run,node)` authority. A different
InputHash, target, profile, predecessor set, receipt set, or raw byte sequence
for the same node is a conflict, not another generation.

If a commit acknowledgement is lost:

- retain the same IDs while the process is alive;
- after restart, inspect `(run,node)` before allocating anything;
- return the committed authority only after byte/scalar cross-validation;
- if database state is unavailable, return outcome unknown;
- do not activate the gate, create a new authority, or start qualification
  while outcome remains unknown; and
- allocate a new attempt only after a strongly consistent inspection proves
  the old transaction did not commit.

Promotion and handoff use the same discipline with preallocated operation,
handoff, and output revision IDs. An ordinary `not found` from a degraded or
eventually consistent dependency is not proof of `not invoked`.

## 9. Implementation map

### 9.1 New package

Add `backend/internal/workflowinputauthority/` with, at minimum:

- strict wire/domain types;
- canonical JSON and domain/raw hash helpers with golden vectors;
- candidate compilation and cross-binding validation;
- Store interface with Freeze, InspectOperation, ResolveAuthority,
  ResolveNode, and AssertCurrent;
- concurrency-safe memory semantic reference;
- PostgreSQL implementation that can execute on the existing workflow
  transaction; and
- restricted Plan `InputAuthority` adapter or a narrow resolver projection
  consumed by that adapter.

The PostgreSQL freeze method must accept an existing transaction handle. A
Store implementation that silently begins another transaction cannot satisfy
the atomic activation invariant.

### 9.2 Existing Workflow files

Expected implementation areas are:

- `backend/internal/domain/workflow.go`: node type/status and closed config;
- `backend/internal/workflow/types.go`: stable node identity, typed quality
  result, mutation candidate, and immutable authority projection;
- `backend/internal/workflow/input.go`: v3 envelope construction and exact
  typed target extraction;
- `backend/internal/workflow/engine.go`: `applyResultV3`, `reconcileV3`, and
  explicit backend denials;
- `backend/internal/workflow/gorm_store.go`: project-first transaction, locked
  revalidation, freeze, node/event atomic commit, inspect recovery;
- `backend/internal/workflow/execution_profile.go`: literal immutable v3
  descriptor without altering v0/v1/v2;
- `backend/internal/workflow/execution_profile_validation.go`: topology and
  schema checks;
- `backend/internal/workflow/execution_profile_runtime.go`: sealed production
  resolver and handoff runtime;
- `backend/internal/workflow/capabilities.go`: capability schema v5 and
  `waiverPolicy: never`;
- `backend/internal/workflow/platform_adapters.go`: finalized content reader
  and Canonical Review receipt resolver; and
- `backend/internal/workflow/facade.go`, `seed.go`, and
  `blueprint_selection_seed.go`: private routing and new definition versions.

New descriptor fields must use a separate v3 type or be omitted from
historical canonical JSON so existing execution-profile hashes remain exact.

### 9.3 Runtime readiness

`ExecutionProfileRuntime` may advertise profile v3 only when all of the
following are sealed:

- migrations `000076` and `000077` Canonical Review receipt authority and
  hardening;
- migration `000078` Workflow Input Authority store and catalog posture;
- finalized content reader;
- production qualification-policy and Plan InputAuthority resolver;
- private Promotion v2 verifier/consumer; and
- private Workflow handoff consumer.

Missing components fail readiness. Development fallbacks, memory stores,
browser DTOs, direct migration-owner connections, or unqualified operator
identities must not enable v3 in production.

## 10. Failure behavior

| Failure | Required behavior |
| --- | --- |
| No exact `000076` receipt for a required revision | Keep external gate pending/blocking; request the real Canonical Review flow |
| Legacy review authority version 0 | Reject; never backfill or infer a receipt |
| Quality result lacks one typed passing target | Reject transition; do not freeze |
| NodeInput/manifest/build raw bytes or semantic hash disagree | Reject as corrupt/conflict |
| Content object is not finalized | Block and reconcile the exact object; do not accept it optimistically |
| Run cursor CAS conflict before commit | Reload, rebuild, and revalidate current snapshot |
| Authority committed but response lost | Inspect same node/operation and return exact immutable result |
| Snapshot fact changes after freeze | Plan may remain historical, but Promotion v2 returns stale/blocking |
| Receipt v3 target differs from Workflow target | Reject Promotion |
| UI or ordinary API attempts approval/waiver/retry/submit | Backend rejects invalid transition |
| Promotion commits but handoff worker crashes | Leave exact pending handoff; resume with same IDs |
| Immutable revision creation or node CAS fails | Roll back the entire handoff transaction; node remains waiting |

## 11. Non-goals

Workflow Input Authority does not:

- execute qualification or assert that an external run occurred;
- replace Qualification Plan Authority, Evidence, Receipt v3, Promotion v2,
  ModelProfile governance, production PostgreSQL posture, Template admission,
  Golden, KMS, credential, or deployment authorities;
- issue a Canonical Review Approval Receipt or reinterpret mutable review rows;
- approve an unreviewed Workspace revision;
- create the Promotion output revision or submit the workflow node;
- make a generic QualityGate safe by hiding a button;
- add a Solo Owner/admin emergency waiver;
- persist secrets, credentials, cookies, storage state, provider transcripts,
  arbitrary paths, environment variables, or complete mutable workspaces;
- repair the general finalize-before-PG content-store writer, although it
  avoids relying on that unsafe sequence;
- backfill historical workflow runs, review version 0 rows, Receipt v2, or
  Promotion v1 into new trusted state; or
- change frozen workflow-engine v0/v1/v2 descriptors or replay semantics.

## 12. Verification matrix and acceptance criteria

### 12.1 Canonical/domain tests

- Cross-language golden vectors for request, target, input, envelope, and every
  domain hash.
- Duplicate name, unknown field, trailing token, BOM, invalid UTF-8, null in a
  forbidden field, float/exponent, unsafe integer, invalid UUID/digest/time,
  unsorted member, duplicate identity, and oversized input rejection.
- Mutating every scalar, raw byte digest, predecessor, manifest, revision, or
  receipt binding changes InputHash or fails compilation.
- Legacy NodeInput/manifest semantic hash and raw hash are independently
  checked and cannot substitute for each other.
- `canonicalreviewreceipt.Decode` accepts every copied receipt; wrong request,
  revision, content hash, receipt hash, or bytes fails.
- Existing workflow-engine v0/v1/v2 descriptor bytes and hashes remain
  byte-exact.

### 12.2 Engine/profile tests

- v3 accepts exactly `Workbench -> blocking release quality -> external gate
  -> production Publish` and rejects missing, duplicate, reordered,
  non-blocking, branching, conditional, fanout, or bypassed variants.
- Only one typed passing QualityResult produces a freeze candidate.
- Target is derived only from that result and cannot be overridden by API
  input or mutable node metadata.
- Quality completion creates authority and `waiting_qualification` together.
- `AuthorizeNodeExecution`, `SubmitHumanInput`, `RecordProposal`,
  `ResolveReview`, `WaiveNode`, `RetryNode`, and `ClaimRunnable` all reject the
  dedicated gate.
- Cancel does not qualify the gate or release Publish.
- Historical v0/v1/v2 runs replay through their frozen paths.

### 12.3 Real PostgreSQL 16 tests

- Catalog checks cover exact tables, columns, composite keys, index keys and
  opclasses, FKs, deferred constraints, functions, owners, fixed search paths,
  grants, and the exact `23 + 5` trigger inventories. Missing, disabled,
  retimed, rebound, column-list-drifted or wrongly deferred triggers fail
  closed, as do arbitrary extra triggers on a dedicated authority table and
  owner/ACL/language/security/search-path/execution-attribute drift in any of
  the ten migration `000078`/`000079` trigger functions. The same canary pins
  the complete twelve-trigger shared-table allowlist, all three legacy trigger
  bindings and their three separately constrained functions.
- Direct `INSERT/UPDATE/DELETE/TRUNCATE` by application, Plan, Promotion,
  auditor, and browser-facing roles is denied.
- Authority and gate activation commit atomically; failure injection at every
  statement leaves both present or both absent.
- Sixteen and thirty-two concurrent exact freezes create one authority;
  different bytes for the same node conflict.
- Post-commit acknowledgement loss and process restart recover through exact
  operation/node inspection.
- Parent/child count, ordinal, JSONB/raw byte, scalar, copied receipt, and
  content-pointer tamper canaries fail closed.
- Down migration refuses while any authority, child, reservation, node FK,
  Promotion, or handoff row exists.

### 12.4 Race and lock-order tests

Race freeze and Promotion against:

- autosave and artifact draft update;
- proposal application;
- Implementation Apply and Workspace pointer advancement;
- Canonical Review close/receipt issuance;
- BuildManifest activation/invalidation;
- InputManifest content/ref change;
- predecessor completion/output change; and
- project governance/member changes where current policy requires stability.

Every race must observe a complete before-state or return stale/conflict after
the writer; no transaction may freeze a mixed closure. Add deadlock canaries
for project-first Workflow/Core writers running concurrently with Evidence,
Plan, Receipt, Promotion, rollback, and posture checks.

### 12.5 Content and recovery tests

- Finalized exact objects succeed; pending, aborted, missing, wrong-project,
  wrong aggregate/schema, changed ref, changed content hash, and changed raw
  bytes fail.
- A pointer change after preflight but before the PG lock is detected.
- Authority resolution remains possible after external content retirement
  because exact raw bytes are retained in PostgreSQL.
- Commit-unknown never allocates a second qualification generation or starts
  an external call.

### 12.6 Promotion/handoff end-to-end tests

The minimum closed test is:

```text
exact reviewed source revisions with 000076 receipts
  -> passing release Quality result
  -> atomic 000078 Workflow Input Authority + waiting gate
  -> 000074 Plan whose InputAuthorityID is the Workflow Authority ID
  -> 000073 Evidence closure
  -> 000075 terminal Receipt v3
  -> 000080 exact WIA + current Policy + Plan input precommit
  -> 000081 exact single-use Promotion v2 pending handoff whose canonical
     closure contains the full typed inputPrecommit binding
  -> 000082 immutable output revision + gate completion in one transaction
  -> production Publish waits for authenticated ActionPublish
```

Mutation tests independently change Workflow Authority, Plan input,
Evidence head, Receipt bytes, target revision, BuildManifest, BuildContract,
source currency, Canonical Review receipt, qualification policy, node identity,
and handoff IDs. Every mutation must block Promotion or handoff.

The closed-loop acceptance invariant is:

```text
Publish eligible
  only if
one immutable Workflow Input Authority exists for the exact gate generation
  and one exact Plan/Evidence/Receipt v3 closure consumed it through Promotion v2
  and the private handoff atomically created its immutable output revision
  before completing that same non-waivable gate.
```
