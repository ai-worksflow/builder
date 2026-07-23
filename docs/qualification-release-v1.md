# Qualification Release v1

## Purpose

Qualification Release v1 is the database authority that closes the final
`workflow-engine/v3` production Publish transition. It accepts one exact
Handoff output, one authenticated `ActionPublish`, one uniquely qualified
Release Bundle, and one healthy Release Controller result. It then permits one
immutable Workflow completion. A caller assertion, a generic Workflow
mutation, or a Controller response alone is never sufficient authority.

Migration `000084_workflow_execution_profile_v3_qualified_release` implements
this boundary for the not-yet-published v3 descriptor hash
`854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104`.
Persisted facts carrying the superseded draft hash are rejected during rollout
instead of being reinterpreted.

## Required order

The only successful order is:

1. Migration 82 completes the external qualification Handoff and leaves the
   Workflow run and Publish node in `waiting_input`.
2. `authorize_qualification_release_v1` validates the exact frozen definition,
   dynamic Publish node key, frozen `requiredRole`, current owner/admin actor,
   Handoff lineage, Workspace equivalence, and one unique Bundle/v2
   Preview/Approval chain. It appends `node.execution_authorized` and moves the
   run/node to `running`/`ready` in the same transaction.
3. `claim_qualification_release_publish_v1` creates one immutable lease claim,
   appends `node.claimed`, advances the run cursor, and moves the Publish node
   to `running` in the same transaction.
4. `start_qualification_release_controller_v1` requires that exact current
   claim to still be active according to database time. It creates or replays
   one Controller-bound Production Run and Operation; it performs no remote
   I/O.
5. The Release Controller worker drives the durable Operation to a terminal
   state. `record_qualification_release_result_v1` freezes a healthy Result,
   Production Receipt, Deployment Revision, and `PublishResult`. An exact
   failed production check, Controller rejection, or pre-submit cancellation
   is instead frozen by `record_qualification_release_failure_v1`.
6. The matching apply capability revalidates the current claim, worker,
   attempt, cursor, complete claim chain, and immutable result. Healthy release
   appends `node.completed` and completes the Publish node and Workflow run.
   Terminal release failure appends `node.failed` and fails both atomically.

Starting the Controller before claim is rejected. Generic claim, renew,
completion, failure, cancellation, cursor, context, and output mutations are
rejected by database triggers. The shared Go scheduler must exclude the exact
v3 Publish node so an ineligible generic candidate cannot starve normal work.

## Durable authority

Seven private tables hold the closure:

- `qualification_release_v1_controller_bootstraps`: singleton trusted
  Controller identity;
- `qualification_release_v1_identity_reservations`: immutable IDs allocated by
  authorization;
- `qualification_release_v1_authorizations`: authenticated Publish and exact
  upstream equivalence;
- `qualification_release_v1_controller_bindings`: authorization to the one
  Production Run and Controller Operation;
- `qualification_release_v1_lease_claims`: append-only owner of every Workflow
  lease epoch;
- `qualification_release_v1_results`: exact healthy result and `PublishResult`,
  or one exact terminal failure (`production_failed`, `controller_rejected`,
  or `pre_submit_cancelled`);
- `qualification_release_v1_transaction_grants`: transaction-local transition
  grants that must be consumed before commit.

Each lease claim binds a server-supplied UUIDv4 event ID, authorization,
project, run, Publish node/key, event sequence, attempt, owner, initial expiry,
canonical bytes, and domain-separated hash. `(authorization_id, attempt)` and
`(workflow_run_id, event_sequence)` are unique. Claim facts, their Workflow
events, and their Outbox envelopes are immutable.

For attempt `n`:

```text
claim_event_sequence = authorization_action_sequence + n
completion_sequence  = latest_claim_event_sequence + 1
```

The completion predicate requires claims `1..n` with no gap and validates each
claim document, event, and Outbox envelope. It does not infer history from the
mutable node attempt.

The canonical claim bytes use the exact hash domain
`worksflow.qualification-release.lease-claim/v1` under the common
`worksflow-qualification-release-hash/v1` envelope.

## Claim, renew, replay, and takeover

```sql
claim_qualification_release_publish_v1(
  authorization_id uuid,
  workflow_run_id uuid,
  publish_node_run_id uuid,
  claim_event_id uuid,
  lease_owner text,
  lease_duration_milliseconds integer
)

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
```

Claims and renewals require SERIALIZABLE isolation, a read-write primary, the
release operator credential, the shared rollout fence, and the per-run/node
advisory lock. Lease duration is 1 second through 5 minutes.

- Replaying the same claim event ID with identical inputs returns the immutable
  claim and never allocates another attempt.
- A different event ID or owner while the current lease is unexpired returns a
  not-ready conflict and writes nothing.
- Only database-observed expiry permits `attempt + 1` and a new claim event.
- Renew is a compare-and-swap from an exact millisecond expiry to a strictly
  later expiry no more than five minutes beyond database time.
- Renew does not append a Workflow event or advance the cursor.
- A same-expiry renew replay succeeds only while the exact claim is still
  current and unexpired. It cannot revive an expired epoch.
- `inspect_qualification_release_publish_claim_v1` resolves commit-unknown
  claims without mutation. An old claim remains inspectable but is reported
  inactive after takeover.

Controller start binds its caller to the explicit current claim ID, owner, and
attempt, so an expired worker cannot borrow a successor's lease. The binding
retains the initial claim as immutable audit evidence. After takeover, the new
current claim may replay that same binding and Operation but cannot create a
second Production Run or Operation. Without any active claim, callers may only
inspect the already-created immutable Operation through
`inspect_qualification_release_controller_v1`.

## Locks and rollout

Every mutating function takes the shared
`worksflow:workflow-input-authority-migration:v1` advisory fence before relation
access. Claim, renew, and apply then serialize on the exact run/node advisory
key. Relation order is project, Workflow run, all Workflow nodes in ID order,
then immutable release authorities. Controller start joins the same run/node
fence before its Controller-specific fence and release-environment locks.

The up migration takes the exclusive rollout fence and rejects any persisted
old v3 hash. The down migration takes the same exclusive fence, restores
migration 82's deny-completion guard, and succeeds only when all seven authority
tables are empty. Once any bootstrap, authorization, claim, binding, or result
exists, rollback fails with `55000`; use a forward migration.

Migration `000083` is the owner-executed compatibility bridge for the three
historical Release Delivery helpers still referenced by legacy table checks and
triggers. It grants the migration-owner group only missing `EXECUTE`
capabilities and records provenance. Its down migration revokes only grants
introduced by `000083`, preserving every pre-existing deployment ACL.

## Privileges

All tables and functions revoke privileges from `PUBLIC` and ordinary
application roles. The migration owner owns every private object. The NOLOGIN
`worksflow_qualification_release_operator` receives only the sixteen runtime
entry points needed to authorize, claim, renew, inspect, start, record, and
apply healthy or failed outcomes. It has no direct table privileges. Controller
bootstrap is reserved for an exact canonical migration-owner login running
without `SET ROLE`; it cannot be invoked by the runtime operator.

## Verification

The PostgreSQL canaries cover:

- empty rollback, non-empty rollback refusal, singleton bootstrap, and the
  bootstrap-versus-down rollout fence;
- old v3 hash refusal with no partially installed authority;
- dynamic Publish keys and owner/admin versus frozen `requiredRole`;
- a v1 Preview sibling being ignored and two exact v2 Preview/Approval chains
  being rejected as ambiguous;
- concurrent fresh/replay authorization, claim, Controller start, result, and
  apply;
- active-claim conflict, renew CAS, expiry, takeover, stale epoch rejection,
  and commit-unknown inspection/replay;
- direct DML bypass rejection for run, node, lease, event, and Outbox identity;
- exact claim-chain and final completion closure;
- exact failed-check, rejected, and pre-submit-cancelled terminal closure with
  no second Production Operation;
- canonical migration-owner execution and ACL-preserving `000083` rollback;
- exact catalog counts and zero function execution for `PUBLIC` and the normal
  application role.
