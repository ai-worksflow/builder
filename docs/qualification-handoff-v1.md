# Qualification Promotion v2 Workflow Handoff

Status: migration `000082` and its closed Go/PostgreSQL adapter are implemented;
profile-v3 worker rollout remains deliberately disabled pending the full-chain
canary and Release Controller. This boundary begins only
after migration `000080` has installed the required Qualification Input
Precommit Authority and migration `000081` has durably created one Promotion
v2 consumption plus one immutable pending handoff whose canonical closure
contains that complete typed binding. This contract does not weaken the
`workflow-engine/v3` external-qualification gate installed by migration
`000079`.

## 1. Boundary

The handoff consumer performs the first and only transition that may turn a
terminal Promotion v2 result into workflow state. In one project-first
PostgreSQL transaction it must:

```text
pending handoff + exact Promotion v2 bundle
  -> same-content immutable system Revision on the target Workspace artifact
  -> append-only Revision authority binding
  -> completed external-qualification node with one full typed passing QualityResult
  -> production Publish successor waiting for authenticated ActionPublish
  -> append-only handoff completion + two Workflow events + two completion Outbox events
```

It does not rerun qualification, admit an independent authority, change the
Workspace bytes, call an external content store, approve an unrelated
Revision, or accept caller-authored target or evidence facts.

Handoff does not authorize Publish. After the gate completes, the direct
production Publish successor must be `waiting_input`, the run must be
`waiting_input`, and a later authenticated `ActionPublish` command must provide
the actor provenance required by the frozen profile before any publisher runs.

The public command contains exactly one opaque UUIDv4 `handoffId`. Project,
run, node, target artifact/revision, output revision, Plan, Receipt, Workflow
Input Authority, and Promotion hashes are all resolved from the immutable
handoff bundle. The API allocates no replacement identity after an ambiguous
commit.

## 2. Why the output Revision reuses exact content

`artifact_revisions` stores an external content reference, while the node and
Revision mutation must commit atomically in PostgreSQL. Writing Mongo/S3 before
the transaction can leave an unreachable finalized object; writing it after
the transaction can leave a Revision whose bytes do not exist.

The qualified target is already an immutable Workspace Revision. The
promotion-only output therefore copies these fields exactly from the locked
target parent:

```text
artifact_id
schema_version
content_store
content_ref
content_hash
byte_size
source_manifest_id
proposal_id
implementation_proposal_id
```

It sets:

```text
id                  = handoff.outputRevisionId
parent_revision_id  = handoff.target.targetRevisionId
revision_number     = locked max + 1
workflow_status     = approved
change_source       = system
change_summary      = fixed external-qualification v2 summary
created_by          = locked workflow run started_by
created_at          = one database v_now
approved_at         = the same v_now
promotion_handoff_id = handoff.handoffId
```

The new Revision is a qualified metadata generation of the same application
bytes, not a content edit. Its `content_hash` must equal the parent hash and
the handoff target hash. No canonical wrapper is substituted for Workspace
content and no hash is relabeled.

Migration `000082` replaces the existing `(artifact_id, content_hash)` unique
constraint with an equivalent ordinary-writer partial unique index for rows
whose `promotion_handoff_id IS NULL`. A same-content row is legal only when it
has a unique non-NULL handoff ID, the exact pre-reserved output Revision ID,
the exact parent/content projection above, and a transaction-local private
authorization consumed by the insert trigger. Ordinary application writers
cannot set the handoff column to bypass no-change checks.

The target parent is superseded and the artifact's `latest_revision_id` and
`latest_approved_revision_id` move to the output under an exact CAS. The
parent's immutable source rows and dependency/trace projections are copied to
the output so unchanged content retains unchanged source lineage. Promotion
authority lineage is stored separately; it is never disguised as an artifact
source Revision.

The completed external gate's output is not `{}`, a receipt summary, or only a
Revision ID. It is one profile-v3 `QualityResult` with `passed=true`, the exact
locked qualification run ID, the full output Workspace `ArtifactRef`, and the
full frozen BuildManifest already authenticated by WIA/Plan/Receipt. The
Workspace reference differs from the qualified parent only by the new
same-content output Revision identity and its authority lineage. Findings, if
the frozen output schema admits them, are copied only from authenticated
upstream material. This typed value is written to run context and the node's
output projection so the direct Publish input builder can prove exactly one
passing result without reading a mutable side channel.

## 3. Append-only records

Migration `000082` adds:

```text
qualification_promotion_v2_revision_transaction_grants
qualification_promotion_v2_revision_authority_bindings
qualification_promotion_v2_handoff_lineage_members
qualification_promotion_v2_handoff_completions
```

The transaction-grant table is an owner-only internal rendezvous, not a
durable authority ledger. Its row binds output Revision, handoff, operation,
backend PID, and `pg_current_xact_id()`. The private completion function inserts
the row immediately before the Revision insert. The artifact Revision identity
trigger atomically deletes and returns that exact row while admitting the
already Promotion-owned reservation. No grant may survive the statement or
commit. A deferred closure check and the completion function both require the
table to be empty.

The binding, copied-lineage member, and completion rows each persist the same
explicit top-level `creation_transaction_id` returned by
`pg_current_xact_id()`. The completion routine has a PL/pgSQL `EXCEPTION`
handler, so rows written inside it carry a subtransaction `xmin`; `xmin` is
therefore deliberately not used to recognize fresh deferred events. The
explicit immutable identity lets source/dependency/trace member events be
coalesced while the completion and binding aggregate events still guarantee
full closure checks; the other non-member aggregate guards remain intact. A
later transaction has a different full transaction identity and cannot take
that construction-only path.

Copied lineage is not embedded in one unbounded JSON envelope. Every copied
source, dependency, and trace is represented by one immutable member row with
kind, deterministic ordinal/key, and a domain-separated hash of the complete
copied row. This includes full trace metadata in the member hash. The authority
document binds a folded member root plus exact per-kind counts:

```text
copiedLineage {
  schemaVersion
  rootHash
  sourceCount
  dependencyCount
  traceCount
}
```

Inspection recomputes every frozen member hash and the root/count summary but
does not serialize the member payloads into the response. A trace metadata
object larger than 1 MiB can therefore be frozen without overflowing the
1 MiB authority envelope, and the inspection response remains independent of
that payload size. Relationships added after Handoff are legitimate graph
growth and do not become frozen members; mutation or removal of an already
frozen member is rejected.

Revision authority bindings are immutable. Their canonical document has
schema `worksflow-qualification-promotion-output-revision-authorities/v1` and
contains exactly:

```text
schemaVersion
handoffId
operationId
outputRevisionId
workflowInput { authorityId, authorityHash }
plan { authorityId, authorityHash }
receipt { receiptId, envelopeHash }
promotion { requestHash, closureHash, revisionIntentHash, consumptionHash }
target
revisionStateAtHandoff
copiedLineage { schemaVersion, rootHash, sourceCount, dependencyCount, traceCount }
```

Its domain hash is framed by
`worksflow.qualification-handoff.revision-authorities/v1`. Exact canonical
bytes, parsed JSON, hash, and repeated scalar columns are retained.

Handoff completion is also append-only and keyed by the pending handoff ID. Its
canonical document has schema
`worksflow-qualification-promotion-handoff-completion/v1` and contains exactly:

```text
schemaVersion
handoffId
operationId
consumptionHash
outputRevisionId
outputRevisionContentHash
projectId
workflowRunId
nodeRunId
nodeKey
publishNodeRunId
workflowEvents[] {
  role, eventId, eventSequence, eventType, nodeRunId, nodeKey
}
outboxEvents[] { role, outboxEventId, workflowEventId, eventType }
completedAt
```

Its domain is `worksflow.qualification-handoff.completion/v1`. The completion
row, output Revision, authority binding, node/run mutation, events, and outbox
records all use one database `v_now` rounded to UTC milliseconds. Both arrays
have exactly two members: `gate-completed` binds `node.completed`, and
`publish-authorization-required` binds
`node.execution_authorization_required`. They are sorted by Workflow sequence;
each Outbox member points to its exact Workflow event. A mutable later Publish
authorization or result is not part of this immutable Handoff replay record.

The migration-81 pending handoff row is never updated. `pending` plus the
absence of a completion row means unconsumed; the unique completion row is the
only terminal fact.

## 4. Atomic order

The production adapter checks out one dedicated session-affine PostgreSQL
connection and acquires the handoff-scoped session advisory lock
`worksflow:qualification-handoff-v1:<handoffId>` before `BEGIN`. It then starts
one `SERIALIZABLE` transaction and calls completion on that same backend. The
SQL routine repeats a transaction advisory lock only as defense for private
direct callers; acquiring the first lock after `BEGIN` would allow a waiter to
retain a stale serializable snapshot.

Unlock uses bounded cancellation-independent cleanup and must return `true`.
Unknown lock acquisition, `BEGIN` acknowledgement, `COMMIT` acknowledgement,
`ROLLBACK` acknowledgement, unlock false/error, or transport loss poisons and
discards the physical connection without another unlock or pool return.
Transaction-pooling DSNs are forbidden. Only definite `40001`/`40P01` aborts
retry the complete same-ID attempt; ambiguous commit goes to bounded,
cancellation-independent inspection of the same ID on a fresh primary
connection.

The completion function runs only at `SERIALIZABLE` isolation and performs:

1. Acquire the shared
   `worksflow:workflow-input-authority-migration:v1` rollout fence before the
   first relation read.
2. Inspect completion by `handoffId`. Return an exact immutable replay before
   any current-authority lookup.
3. Read the immutable handoff only as a project locator, without a row lock or
   treating it as validated authority.
4. Lock `projects(id) FOR UPDATE`.
5. Call the transaction-bound Workflow Input Authority assertion. Require the
   exact v3 run and external gate to remain `waiting_qualification`, with the
   same WIA and target.
6. Lock the run, all affected node rows in stable ID order, target artifact,
   target Revision, artifact pointer rows, and relevant lineage rows. Require
   the target to remain latest and latest-approved.
7. Lock and re-read the exact Promotion v2 consumption, pending handoff,
   identity reservation, and absence of completion. Reparse the full store
   bundle and recompute every canonical hash and cross-binding, including the
   complete required `inputPrecommit` binding produced by migration `000080`.
8. Require exactly one direct enabled production Publish successor and no
   alternate v3 path. It must still be `pending` with no output, execution
   actor, lease, attempt, or failure.
9. Insert and immediately consume the exact transaction grant; insert the
   same-content output Revision and copy its content lineage.
10. Insert the Revision authority binding and handoff completion, update the
    artifact pointers, supersede the parent, complete the gate with the exact
    typed passing `QualityResult`, move the Publish successor to
    `waiting_input`, move the run from `waiting_qualification` to
    `waiting_input`, advance the cursor according to the frozen profile, and
    insert the exact gate-completed plus publish-authorization-required
    Workflow/Outbox event pairs.
11. Re-read every row, require the grant table empty, cross-check exact bytes,
    and commit.

No qualification-only writer may acquire the project lock after holding a
Promotion, Evidence, Plan, or Receipt lock. The handoff locator read is safe
only because all future Promotion/workflow DDL first takes the exclusive side
of the rollout fence.

## 5. Database guards and profile v3

Migration `000082` replaces, rather than disables, the migration-79 guard
functions. The revised guards retain every old denial and add one narrow
completed shape:

- the external gate may move only from `waiting_qualification` to `completed`;
- `output_revision_id` must identify the exact same-run consumed handoff
  output and its authority binding;
- the completed gate output/run-context value must be the exact full typed
  passing `QualityResult` described above;
- `completed_at` must equal the completion transaction time;
- attempts, leases, proposal, input manifest, failure, and generic runner
  fields remain absent;
- a completed gate requires exactly one completion row at deferred constraint
  time; and
- a v3 run may leave `waiting_qualification` only with that exact completed
  gate, one direct Publish successor in `waiting_input`, no execution actor,
  and the exact authorization-required event pair.

The application role still cannot complete, approve, waive, retry, lease, or
submit the gate. Trigger disabling, `session_replication_role=replica`, direct
table DML, and a caller-set custom GUC are not authorization mechanisms.

The artifact Revision reservation trigger from migration `000081` is extended
so an ordinary insert still atomically creates an `artifact-revision`
reservation, while a Promotion-owned ID is accepted only by atomically
consuming the exact transaction-grant row. Check-then-insert is forbidden.

### 5.1 Profile-v3 runtime and publisher boundary

`workflow-engine/v3` remains runtime-disabled until migrations
`000080`–`000082` and this handoff canary are sealed. Handoff stops at an
authorization-waiting Publish node; it never calls the publisher itself.

The legacy `delivery.WorkflowPublisher` requires its Workspace Revision to be
identical to the original Quality report target. It therefore cannot publish
the new same-content metadata generation, and its comparison must not be
weakened globally. The profile-v3 runtime needs a dedicated equivalence-aware
Release Controller publisher which, after a real `ActionPublish`
authorization, locks and proves all of these facts:

- the typed input is the exact Handoff-produced `QualityResult`;
- its output Revision has the one migration-82 authority binding/completion;
- its parent is the exact passing Quality report target and its content/store/
  manifest projection is byte-for-byte equivalent;
- the BuildManifest, ReleaseBundle, production approval/receipt prerequisites,
  and Controller result belong to this exact project/run/revision; and
- no caller-supplied equivalence flag, parent ID, Receipt, or Bundle can replace
  those database authorities.

The publisher is installed only in the sealed profile-v3 runtime bundle. If
the frozen descriptor cannot identify this behavior exactly, a new descriptor
version/hash is required before registration; runtime wiring must not silently
reinterpret the existing profile. Missing authority binding, completion,
ReleaseBundle, required production receipt, or Controller authority fails
closed.

The normative downstream runtime, authenticated Publish authorization,
same-content equivalence ledger, Release Controller lease/replay, configuration,
rollout, and no-bypass contract is
[Workflow Execution Profile v3: Qualified Release Runtime Contract](./workflow-execution-profile-v3-runtime.md).
It allocates proposed migration `000084` because `000083` is already occupied by
Canonical Review forward-equivalence hardening; that migration and runtime are
docs-first only and are not implemented, activated, or externally qualified by
this Handoff contract.

## 6. SQL and role surface

The only runtime routines are:

```text
complete_qualification_promotion_v2_handoff(uuid)
  RETURNS SETOF jsonb
  VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE

inspect_qualification_promotion_v2_handoff_completion(uuid)
  RETURNS SETOF jsonb
  STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
```

Both return zero rows for not found and otherwise one closed
`worksflow-qualification-promotion-handoff-completion-bundle/v1` JSON object.
Completion adds only a response `idempotent` flag. The bundle includes the
completion, Revision authority binding, exact immutable output Revision
identity/content/parent projection, stored Handoff-time workflow node/run
cursor projection, and event identities; it contains no Workspace bytes,
Evidence payloads, signatures,
credentials, or secrets. Inspection may additionally report current mutable
Publish state as a clearly non-authoritative diagnostic, but that state is not
part of canonical completion equality.

A new stable `NOLOGIN` role
`worksflow_qualification_handoff_operator` receives schema USAGE and EXECUTE on
exactly those two functions. It has no table, sequence, schema CREATE, generic
function, migration-owner membership, WIA assertion, or Promotion resolver
privilege. The SECURITY DEFINER completion routine invokes owner-held private
assertions internally. Production posture uses a separate LOGIN/DSN for this
role and checks the complete catalog contract.

The migration-82 catalog adds four private tables with sixteen backing indexes,
the constraint-backed `artifact_revisions_promotion_handoff_unique` index on the
existing Revision table, two explicit partial/unique indexes, and seventeen new
triggers: nineteen new indexes in total. It contains exactly ten new Handoff
functions. Production posture pins every column of all four tables by ordinal,
type, nullability, default and collation. In particular, the binding, lineage
member and completion each retain the explicit top-level
`creation_transaction_id` text column and its canonical nonzero transaction-ID
check constraint; dropping, renaming, retyping or weakening any of those facts
fails closed. Mutation
rejection, pending dispatch, completion, inspection, and deferred closure
validation are the five `SECURITY DEFINER` functions; the remaining five are
invokers. All ten are owned by `worksflow_migration_owner`, use the exact fixed
`pg_catalog, <trusted-schema>` path, and are revoked from `PUBLIC`. Only the
two runtime routines above grant non-grantable execution to the Handoff group.

The production Handoff LOGIN must be `ROLINHERIT` and have exactly one direct
membership: `worksflow_qualification_handoff_operator` with `INHERIT TRUE`,
`SET FALSE`, and `ADMIN FALSE`. It must be the group's sole inbound member,
own no database or trusted-schema object, and connect with
`current_setting('role') = 'none'`; direct `SET ROLE` is not a supported path.
The independent posture deployment loads its ninth distinct credential from
`WORKSFLOW_PRODUCTION_POSTGRES_HANDOFF_DSN_FILE` and requires
`WORKSFLOW_PRODUCTION_POSTGRES_HANDOFF_SESSION_AFFINITY=direct` or an explicitly
declared session-pooling connection. A missing Handoff credential or affinity
fails configuration; it never downgrades to the Promotion DSN. The accepted
runtime gate remains `disabled-pending-input-precommit-authority-canary`, so a
passing posture check does not start or authorize a Handoff worker.

Production dispatch is durable. Migration `000082` must append one
`qualification.promotion_handoff.pending` Outbox record, uniquely keyed by
event kind plus handoff ID, for every new migration-81 pending handoff in the
same Promotion transaction, and must
backfill exactly one such record for every pre-existing uncompleted handoff
under the rollout fence before a worker is enabled. The event carries only the
opaque `handoffId`; the pending row remains the authority and completion
remains the idempotency result. An in-memory callback after Promotion commit,
an unbounded table scan through direct operator `SELECT`, or a message
acknowledged before completion/inspection reconciles the same handoff ID is not
an implementation.

The package now contains the transport worker, but it is deliberately not
wired into application startup while the production activation gate remains
disabled. `qualificationhandoff.Worker` consumes through a broker-independent
manual-ack interface and the NATS adapter creates or binds the single explicit
pull durable `worksflow-qualification-handoff-v1` on `WORKSFLOW_EVENTS`. Its
filter is exactly
`worksflow.qualification.promotion-handoff.pending`; the message must also have
exactly one `Worksflow-Event-Type` value equal to
`qualification.promotion_handoff.pending`. The consumer uses explicit ACK,
`DeliverAll`, instant replay, a two-minute ACK deadline, server backoff, 20
maximum deliveries, eight maximum ACK-pending messages, an eight-message
maximum pull request, and bounded pull expiry. Existing consumer configuration
drift fails startup rather than being silently widened or overwritten.
Worker construction also requires that the consumer ACK deadline cover two
bounded Complete/Inspect windows plus terminal recording, preventing a normal
same-ID recovery from expiring its delivery lease mid-operation.

The payload decoder caps the body at 256 bytes and accepts only the closed JSON
object `{"handoffId":"<canonical-uuid-v4>"}`. It rejects duplicate names,
unknown names (including project, workflow, artifact, Revision, target, or
Promotion fields), missing/null members, noncanonical UUID spellings, trailing
JSON, and duplicate event-type headers. The worker passes only that UUID to
`Complete`. It ACKs only after the returned immutable record passes the full
Handoff validator, or after a same-ID `Inspect` proves that exact record after
an outcome-unknown completion. `ErrNotReady`, `ErrRetryable`, an unresolved
`ErrOutcomeUnknown`, and bounded operation timeout NAK the same delivery with a
bounded delay; they never allocate or resolve another ID. Cancellation never
ACKs, NAKs, or TERMs the in-flight message.

Invalid, missing, corrupt, conflicting, and retry-exhausted deliveries are not
discarded. Before TERM, the worker synchronously writes a bounded closed
`worksflow-qualification-handoff-quarantine/v1` observation to
`worksflow.qualification.promotion-handoff.quarantine`, with a deterministic
JetStream message ID derived from the source delivery. The observation retains
at most 256 source-payload bytes as base64 plus the full payload size and
SHA-256 hash, bounded source subject/event-type/message-ID observations with
explicit truncation flags, source stream sequence, delivery attempt, reason,
and the Handoff ID only when it was valid. A failed quarantine publish leaves
the source unacknowledged and NAKs it. Normal retries enter quarantine on
delivery 19, reserving the final server delivery for recovery from a failed
quarantine write. This is an observable DLQ boundary, not a second authority
or a Promotion resolver.

Unit and race tests use the broker-independent interfaces. The optional real
JetStream test is enabled by
`WORKSFLOW_QUALIFICATION_HANDOFF_NATS_TEST_URL`; it proves an exact delivery is
completed and ACKed, while a widened payload is quarantined and never reaches
`Complete`. Startup/configuration wiring and production enablement remain
outside this package and are not implied by these worker files.

## 7. Idempotency and recovery

An exact completion replay returns the immutable bundle even when the WIA is no
longer current because the output Revision itself became current. A missing or
different output, completion, event, cursor, or authority binding is a conflict,
not a repair opportunity.

Replay compares the immutable Handoff-time gate/output/event projections. It
must not require the Publish node or run to remain `waiting_input`: a later
authenticated Publish authorization or terminal Publish result legitimately
changes those mutable rows. Their later state is audited by subsequent
Workflow events and Release Controller authorities, not rewritten into the
historical Handoff completion.

`40001` and `40P01` mean the transaction aborted and may be retried with the
same handoff ID. A client-visible commit error is outcome-unknown; the worker
must call completion inspection with that same ID until it can prove committed
or definitely absent. It must not allocate a new output Revision or create an
ordinary Revision while the result is unknown.

The Go decoder accepts only the closed bundle member sets, proves both retained
documents equal their exact canonical bytes and domain hashes, locks
`copiedLineage` to
`worksflow-qualification-handoff-copied-lineage/v1`, recomputes the nested
BuildManifest hash, and checks every repeated handoff, operation, target,
Revision, workflow, event-cursor, QualityResult, and timestamp scalar. Missing
nullable members are rejected rather than reconstructed from Go zero values.

## 8. Rollback

Migration `000082.down` first takes the exclusive rollout fence, then compatible
relation locks in project/workflow/artifact/Promotion order. It refuses
rollback if any completion, Revision authority binding, copied-lineage member,
Promotion-backed artifact Revision, or transaction grant exists. It also
verifies that no node,
event, artifact pointer, or identity reservation refers to a migration-81
Promotion-owned output materialized by migration `000082`.

Only an empty deployment may restore the original artifact content uniqueness
constraint, remove `promotion_handoff_id`, restore the migration-79 guard
functions, and drop the private role surface. Immutable workflow history is
never deleted to make rollback convenient.

## 9. Verification

Required real PostgreSQL cases include:

- fresh completion and inspect-only replay;
- concurrent exact completion produces one Revision/completion and exactly two
  Workflow/Outbox event pairs;
- target currency, WIA, handoff, bundle, parent content, node, successor, or
  artifact-pointer drift blocks without partial state;
- ordinary same-content Revision remains rejected;
- direct use of a Promotion reservation, forged handoff column, fabricated
  grant, stale backend/transaction ID, or reused consumed grant is rejected;
- commit-unknown inspection never executes completion twice;
- fake-driver fault injection proves unknown pre-BEGIN lock, `BEGIN`, `COMMIT`,
  `ROLLBACK`, and unlock states discard the physical connection without
  unlocking/repooling, and real PostgreSQL proves the dedicated LOGIN can call
  only completion/inspection while direct table reads remain denied;
- one durable pending-dispatch Outbox record exists for both a pre-82 backfill
  and a post-82 Promotion, duplicate delivery converges on exact replay, and
  acknowledgement waits for completion/inspection reconciliation;
- application, Promotion, Policy, auditor, and migrator login roles cannot use
  the handoff surface;
- deferred closure rejects node-only, completion-only, event-only, or
  Revision-only states;
- a trace metadata payload larger than 1 MiB completes and inspects through a
  bounded root/count authority, while mutation of its frozen member is rejected;
- at least 1,024 copied dependency/trace members complete below a hard statement
  timeout, and PG16 function counters prove the full exactness routine is called
  a fixed bounded number of times rather than once per deferred member event;
- empty rollback succeeds and any durable handoff output blocks it; and
- a no-bypass canary proves the exact `000073`–`000080` upstream authority
  chain, migration `000081` Promotion consumption, migration `000082`
  Handoff, one output Revision with unchanged bytes, completed external gate,
  production Publish node waiting for authenticated authorization, and no
  alternate path.

Migration `000082` is not complete until static, unit, race, vet, rollback,
concurrency, real PostgreSQL, and production-role posture tests all pass.
Its rollout cannot precede migration `000080`'s input-precommit canary or
migration `000081`'s updated Promotion no-bypass canary; neither predecessor
canary is itself authorization to execute Handoff.
