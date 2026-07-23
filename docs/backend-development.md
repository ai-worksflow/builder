# Backend development

The Go backend lives in `backend/` and uses module
`github.com/worksflow/builder/backend`. It is the platform system of record,
not only an infrastructure shell. Gin exposes HTTP and WebSocket transports;
GORM/PostgreSQL owns transactional metadata and workflow state; MongoDB owns
large immutable payloads; Redis owns bounded ephemeral state and public API
rate limits; NATS JetStream carries outbox-backed realtime events.

The application wires authentication and project RBAC, artifacts and
revisions, reviews and comments, immutable AI manifests and proposals, the
versioned typed workflow runtime, Blueprint Selection, document collaboration,
Design Import, Workbench generation, the data runtime,
quality/export/publish/rollback, GitHub integration, public application data,
and the realtime hub into one process. The architectural contracts are in
`docs/platform-architecture.md`.

## Start the local backend stack

From the repository root:

```sh
docker compose up --build
```

Compose starts PostgreSQL, runs the one-shot `migrate` service to completion,
then starts Redis, MongoDB, NATS with JetStream, a dedicated Docker-in-Docker
quality sandbox, its configured Node and Go images, and the API. The API will
not start until the migration service succeeds.
The default development tags are mutable and therefore not reproducible.
The credentials in `docker-compose.yml` are development-only values. The
frontend is run separately from `frontend/`.

Verify liveness and all required dependencies:

```sh
curl -s http://localhost:8080/health/live
curl -s http://localhost:8080/health/ready
docker compose ps
```

Stop the stack without deleting durable volumes:

```sh
docker compose down
```

Add `--volumes` only when deliberately resetting all local platform data,
published versions, and sandbox state.

## Run the API outside Docker

Start PostgreSQL, Redis, MongoDB and NATS, and make a Docker daemon plus the
configured sandbox images available. Copy `backend/.env.example` to an ignored
environment file and load it in the shell. Apply migrations with the dedicated
migration credential before starting the API:

```sh
cd backend
WORKSFLOW_MIGRATION_POSTGRES_DSN="$MIGRATION_OWNER_DSN" \
WORKSFLOW_MIGRATION_POSTGRES_SCHEMA="${POSTGRES_SCHEMA:-public}" \
  go run ./cmd/migrate
make run
```

In a shared environment, `WORKSFLOW_MIGRATION_POSTGRES_DSN` and `POSTGRES_DSN`
must not identify the same login. The migration process receives DDL/definer
authority; the API process receives only its application DML and approved
function privileges. `POSTGRES_SCHEMA` is the API's sole schema selector; do
not put `search_path`, `role`, identity overrides, service files, or passwords
in the `POSTGRES_DSN` query. Do not export the migration DSN into the API
container.

Configuration is environment-driven and validated before services are opened.
Invalid ports, durations, connection limits, URLs, weak shared-environment
defaults, wildcard credentialed CORS, unsafe resolver endpoints, a missing
encryption key, or an invalid sandbox mount fail startup.

Important startup settings:

- `PLATFORM_ENCRYPTION_KEY` must encode exactly 32 bytes. The documented key is
  local-only and is rejected in staging and production.
- `OPENAI_API_KEY` may be empty for non-AI development. AI routes remain
  installed but generation fails closed as not configured.
- `WORKFLOW_WORKER_ENABLED` controls the durable workflow worker. Heartbeat must
  be shorter than the database lease.
- `DELIVERY_SANDBOX_HOST=tcp://...` requires an absolute
  `DELIVERY_QUALITY_TEMP_ROOT` mounted at the identical path in the API and
  daemon.
- `DELIVERY_SANDBOX_NODE_IMAGE` and `DELIVERY_SANDBOX_GO_IMAGE` may use mutable
  tags only in development/test. Staging and production fail configuration
  validation unless both are pinned as `image@sha256:<64 lowercase hex>`.
- `DELIVERY_PUBLISH_ROOT` must be durable and writable. Use an absolute
  `DELIVERY_PUBLISH_BASE_URL`, such as `http://localhost:8080/published`, when
  published applications need the public data runtime because the provider
  derives an exact browser Origin from it.
- `STARTUP_MIGRATE` is retired and its presence is a configuration error in
  every environment. Use `cmd/migrate` with
  `WORKSFLOW_MIGRATION_POSTGRES_DSN`; the API never applies PostgreSQL DDL.
- `STARTUP_ENSURE_MONGO_INDEXES` and `STARTUP_ENSURE_NATS_STREAM` control the
  remaining non-PostgreSQL startup provisioning. Shared deployments should
  provision those resources deliberately.

## Startup and persistence lifecycle

The API establishes PostgreSQL, Redis, MongoDB and NATS connections first,
verifies the PostgreSQL application-role posture in staging/production, then
applies only enabled non-PostgreSQL startup provisioning under
`STARTUP_TIMEOUT`. It constructs domain services only after those checks
succeed. The NATS event stream must already exist when automatic stream
creation is disabled.

PostgreSQL migrations are embedded in the standalone `migrate` binary, not
executed by the API. The migrator requires an explicit canonical
`WORKSFLOW_MIGRATION_POSTGRES_DSN`, uses a bounded
`WORKSFLOW_MIGRATION_TIMEOUT`, holds a PostgreSQL advisory lock on one dedicated
connection, applies each version in its own transaction, and records its
SHA-256 checksum in `schema_migrations`. Changing an already applied migration
is an error; add a new migration instead. Production and staging must run this
one-shot process before starting or rolling API instances.

The Workflow Input/profile-v3 migrations `000078`/`000079` additionally
require the two-phase advisory-fence rollout documented in
`docs/workflow-input-authority.md` section 8.1.1. Either deploy the fence-only
runtime and drain old writers before migrating, or quiesce all workflow
writers for the migration window; do not combine an unfenced rolling API with
those migrations.

The next qualification rollout has one fixed dependency order:
`000080` Qualification Input Precommit Authority, then `000081` Promotion v2,
then `000082` private Handoff. Migration `000081` must retain the full typed
input-precommit `PromotionBinding` as a required canonical closure member,
separate from Policy-configured independent authorities. The Promotion worker
remains disabled until migration `000080`'s SQL/role/no-bypass canary and the
updated migration `000081` consume canary both pass; passing either migration
or posture check alone is not runtime activation.

The API does not treat a successful ping as schema compatibility. Before
constructing services it reads `schema_migrations` with the application
credential and verifies the exact ordered set of embedded versions and
checksums. A missing version, an unknown newer version or checksum drift fails
startup. This is intentionally strict: run the matching one-shot migrator
before the corresponding API rollout, and do not expect an older binary to
continue after a newer schema head has been installed.

The current migration chain is:

| Version | Scope |
|---|---|
| `000001_platform` | accounts, projects, artifacts, revisions, collaboration, manifests, proposals, workspaces, audit and outbox |
| `000002_data_runtime` | project data tables, records, environments and migrations |
| `000003_delivery` | quality, export and deployment state |
| `000004_workflow_run_history` | versioned workflow definitions, runs, nodes and events |
| `000005_artifact_health_delivery_status` | artifact health and delivery lifecycle state |
| `000006_data_columns_table_fk` | stronger data-column/table ownership constraints |
| `000007_delivery_build_artifacts` | exact quality-to-BuildArtifact-to-deployment relations |
| `000008_public_data_runtime` | default-deny public policies and deployment capabilities |
| `000009_conversation_control_plane` | project conversations, immutable messages, reviewed workflow intents and controlled commands |
| `000010_auth_session_receipts` | transactional sign-up/sign-in/refresh idempotency receipts without persisted cookie secrets |
| `000011_artifact_revision_sources` | ordered immutable source revision, hash, anchor, purpose and required-policy snapshots for every artifact revision |
| `000012_application_build_manifest_lineage` | tenant-bound build-manifest roots, compiler groups/root ordinals, one-child linear rebase lineage and exact workspace pins |
| `000013_design_imports` | immutable untrusted-design snapshots and exact PageSpec/Prototype/manifest/proposal/applied-revision lineage |
| `000014_document_collaboration` | collaboration-state ETags, tenant-safe member responsibilities, recoverable downstream-document commands, and AI provider/model provenance |
| `000015_workbench_generation_fencing` | server-authoritative Workbench generation claims, deterministic conversation Proposal identity, immutable replay inputs, and one-active-Proposal-per-leaf fencing |
| `000016_workflow_execution_profiles` | immutable execution-profile pins on definition versions and runs, with a rolling-deploy-compatible legacy profile |
| `000017_application_build_manifest_slice_identity` | exact durable DeliverySlice pins for current workflow Bundle roots and every derived lineage row |
| `000018_conversation_summary_checkpoints` | independently reviewed immutable prefix summaries, forward-only checkpoint heads, exact conversation/provider-input receipts, and fail-closed proposal/command provenance |
| `000019`–`000022` | project governance mode, artifact-health backfill, Template admission and exact Application BuildContract authority |
| `000023`–`000030` | Repository Candidate/checkpoint authority, Sandbox sessions/processes/terminals, immutable FileBlobs/bootstrap and successor Candidate rebase |
| `000031`–`000038` | Agent task/stream/evidence, exact Merge/Undo and Candidate freeze/Projection/BuildContract guards |
| `000039`–`000042` | Candidate Verification control plane, immutable Receipt, freeze gate and worker lease/fence authority |
| `000043`–`000046` | Canonical quality/verification, complete ReleaseBundle publish gate and Preview/Production/rollback control plane |
| `000047`–`000055` | Sandbox deadlines and Candidate write/abandon reconciliation, verification truncation/cleanup gates, legacy proposal closure, fresh Sandbox baseline and Template artifact authority receipts |
| `000056`–`000061` | durable Release Controller Operation reconciliation, exact-Bundle Preview single-flight, GET-only operator Case, legacy/v3 writer mutex, nested authority recheck and commit-time Run↔Operation binding |
| `000062_repository_exact_tree_literal_index` | immutable project + exact-tree manifest/member/content-hash text-blob index |
| `000063_repository_exact_tree_literal_index_build_claims` | durable expiring single-builder claim before any FileBlob resolve |
| `000064_repository_exact_tree_literal_index_project_quota` | atomic project tree/source-byte/active-build reservation and quota |
| `000065_repository_exact_tree_literal_index_project_gin` | project UUID + body trigram composite GIN tenant fence |
| `000066_repository_exact_tree_literal_index_gc` | bounded exact-CAS retention/GC, immutable capabilities/receipts/tombstones and migration/application/operator privilege split |
| `000067_repository_snapshot_receipts` | append-only exact RepositorySnapshot completion receipts |
| `000068_golden_fault_consume_ledger` | append-only one-shot Golden fault reservation/result CAS ledger and dedicated operator ACL |
| `000069_model_governance_activation_store`–`000070_model_governance_signed_genesis` | append-only Model Governance activation/revocation state and signed genesis authority |
| `000071_qualification_promotion_consume` | append-only exact VerifiedPromotion consumption plus a pending immutable-revision handoff; it does not submit a workflow node |
| `000072_credential_set_event_store` | owner-only, non-secret CredentialSet event/operation/head store with exact append CAS and guarded projection |
| `000073_qualification_evidence_event_store` | owner-only Qualification Evidence event/operation/head store with globally reserved identities, exact append CAS, guarded projection and trusted database time |
| `000074_qualification_plan_authority` | owner-only immutable Qualification Plan authority/identity reservations, exact canonical material freeze/resolve and the Evidence reservation authority guard |
| `000075_qualification_receipt_v3_store` | owner-only snapshot-first Receipt v3 request/observation/completion ledgers, atomic signer-request freeze, authenticated retry generations, exact DSSE completion and v1 history guards |
| `000076_canonical_review_approval_receipt_authority` | database-authored immutable Canonical Review approval receipts, permanent v0 history marking, atomic version-1 close/receipt binding, exact issuer/resolver/probe, source immutability and application ACL boundary |
| `000077_canonical_review_authority_hardening` | append-only review decisions, exact optimistic-concurrency predecessor chain, canonical non-zero UUID/Unicode-trim parity, causal closure and Solo Owner/current-authority hardening |
| `000078_workflow_input_authority` | append-only project/profile Qualification Policy generations plus immutable, raw-byte-retaining Workflow Input freeze/resolve/current-assertion authority with policy-derived revision/review rules, exact activation-event/outbox closure and rolling migration fence |
| `000079_workflow_execution_profile_v3_external_qualification_gate` | frozen workflow-engine/v3 topology and a non-waivable private external-qualification gate; completion remains disabled through the 80→81→82 authority chain |
| `000080_qualification_input_precommit_authority` | append-only source/credential input-composition authority plus owner-reviewed executable heads, distinct verifier/resolver admissions, exact WIA+Policy+Plan binding and an isolated no-bypass role boundary |
| `000081_qualification_promotion_v2` | single-use Receipt-v3 consumption whose canonical closure requires the complete typed migration-80 `inputPrecommit` binding, separate from policy-configured independent authorities; runtime remains disabled pending canaries |
| `000082_qualification_handoff_v1` | private pending-Promotion consumer that creates the same-content immutable output Revision and performs the guarded workflow transition |
| `000083_canonical_review_authority_forward_equivalence` | Canonical Review timestamp/decision forward-equivalence hardening and exact legacy Release helper ACL provenance required by qualified release |
| `000084_workflow_execution_profile_v3_qualified_release` | immutable Controller bootstrap, same-content equivalence, authenticated publish authorization, lease/replay, terminal result and exact healthy-result apply authority |
| `000085_workflow_v3_quality_completion_precommit` | same-transaction v3 Quality completion material, immutable activation Candidate snapshot and WIA precommit boundary |
| `000086_candidate_sandbox_lifecycle_write_gate_v2` | Candidate journal writes require the exact ready SandboxSession projection without allowing stale suspended history to fence a successor Session |
| `000087_sandbox_absolute_ttl_reconciliation` | absolute-TTL reconciliation may append terminal lifecycle evidence without extending operational activity beyond the hard deadline |
| `000088_sandbox_absolute_ttl_transition_boundary` | exact system-owned absolute-TTL cleanup transition and frozen-Candidate authority boundary |
| `000089_sandbox_absolute_ttl_checkpoint_guard` | checkpoint-aware terminal transition guard for absolute-TTL cleanup |

### Production PostgreSQL identities and schema

Production and staging need the original three stable group roles before
migration `000066`, the fourth Golden fault group before `000068`, the fifth
qualification-promotion group before `000071`, and the sixth
qualification-policy group before `000078`. Migration `000080` adds three
isolated Qualification Input groups for composition, source verification, and
credential resolution, and migration `000082` adds the independent Handoff
group. New installations should provision all ten up front:

```sql
-- Run through the platform's privileged PostgreSQL provisioning channel.
CREATE ROLE worksflow_migration_owner NOLOGIN NOSUPERUSER NOCREATEDB
  NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE worksflow_application NOLOGIN NOSUPERUSER NOCREATEDB
  NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE worksflow_repository_index_gc_operator NOLOGIN NOSUPERUSER
  NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE worksflow_golden_fault_operator NOLOGIN NOSUPERUSER
  NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE worksflow_qualification_promotion_operator NOLOGIN NOSUPERUSER
  NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE worksflow_qualification_policy_operator NOLOGIN NOSUPERUSER
  NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE worksflow_qualification_input_precommit_operator NOLOGIN
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE worksflow_qualification_source_verifier_operator NOLOGIN
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE worksflow_qualification_credential_resolver_operator NOLOGIN
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE worksflow_qualification_handoff_operator NOLOGIN
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;

-- The secret/IAM provisioner must create these ten real low-privilege
-- LOGIN identities first; this SQL does not create or set their credentials.
GRANT worksflow_migration_owner TO worksflow_migrator_login;
GRANT worksflow_application TO worksflow_api_login;
GRANT worksflow_repository_index_gc_operator TO worksflow_gc_login;
GRANT worksflow_golden_fault_operator TO worksflow_golden_fault_login;
GRANT worksflow_qualification_promotion_operator
  TO worksflow_qualification_promotion_login
  WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
GRANT worksflow_qualification_policy_operator
  TO worksflow_qualification_policy_login;
GRANT worksflow_qualification_input_precommit_operator
  TO worksflow_qualification_input_precommit_login
  WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
GRANT worksflow_qualification_source_verifier_operator
  TO worksflow_qualification_source_verifier_login
  WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
GRANT worksflow_qualification_credential_resolver_operator
  TO worksflow_qualification_credential_resolver_login
  WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
GRANT worksflow_qualification_handoff_operator
  TO worksflow_qualification_handoff_login
  WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;

REVOKE CREATE ON DATABASE worksflow FROM PUBLIC;
CREATE SCHEMA worksflow AUTHORIZATION worksflow_migration_owner;
REVOKE ALL ON SCHEMA worksflow FROM PUBLIC;
```

Use deployment-specific LOGIN names where required, but preserve the ten exact
group-role names because migration/readiness contracts use them. Do not grant
one group to another, and do not make a LOGIN a member of more than its one
group. None of the ten LOGINs may own the database, inherit its owner, create
schemas in the database, or reach superuser, `BYPASSRLS`, role/database
creation or replication authority. Only the one-shot migrator may, through
`worksflow_migration_owner`, own and create objects in the trusted schema; the
other nine LOGINs must not. The API and operators must never receive the
migration-owner membership.
No membership into one of the ten stable groups may use `ADMIN OPTION`, and
the stable groups must not themselves be members of any other role. Before
`000066`, convert or revoke every explicit column-level ACL in the trusted
schema through a reviewed provisioning change; table-level grants are the only
supported application DML contract.
The migration owner and migrator identities must be dedicated to this database
boundary: `000066` changes their global default routine ACL so future functions
do not silently regain PostgreSQL's default `PUBLIC EXECUTE`.

Migration `000066` starts with a no-mutation preflight. Any partial or unsafe
stable-role set, outgoing stable-role membership, incoming `ADMIN OPTION`, or
explicit trusted-schema column ACL aborts before schema ACL, DDL, ownership or
default-privilege changes. The all-absent role posture is supported only for
isolated local development and installs no stable-role grants. If that local
posture was applied first, creating the roles afterward is not enough: the
checksum/version is already immutable and the conditional blocks will not run
again. Add a reviewed follow-up migration that installs the same ownership and
grants. Never edit the applied SQL, delete its `schema_migrations` row or grant
broad table privileges as a repair.

`000066` installs the exact-tree index/GC privilege boundary and normalizes
trusted-schema ownership: the schema and all of its tables and sequences are
owned by the exact `worksflow_migration_owner` `NOLOGIN` role, as are all 23
controlled routines (the original 22 external/trigger boundaries plus the
Sandbox checkpoint dependency helper). Platform provisioning must separately
grant the application group the complete, reviewed DML needed by all other API
tables/sequences, provision the dedicated database, and inject every
role-specific process DSN as a runtime secret. Do not put DSNs in the
repository, Compose defaults, image
layers, command arguments or logs. The development Compose stack intentionally
reuses one owner credential and the `public` schema; that convenience is
neither a production role test nor a qualification result.
Shared Compose deployments must set `APP_ENV=staging` or `APP_ENV=production`;
the Compose value is interpolated and the development default intentionally
skips role posture only for local use. Injecting a production DSN while leaving
`APP_ENV=development` is not a valid deployment.

Migration `000068` uses conditional grants, so
`worksflow_golden_fault_operator` must exist before it runs. If the migration
was already recorded without that role, creating the role alone does not replay
the grants. Use the exact owner/invoker preflight and narrow repair GRANT shown
in `docs/golden-qualification-control-plane.md` section 5.1; if either table or
either trigger function is not owned by `worksflow_migration_owner`, create a
reviewed follow-up migration instead of altering the immutable migration row.
After `000068`, the production owner boundary contains 12 protected boundary
tables, 28 indexes and 25 controlled routines; the two new trigger functions
are owner-only `SECURITY INVOKER`, so the exact `SECURITY DEFINER` count remains
19. The API/application role has no privilege on either fault table. The
dedicated fault operator has only non-grantable `SELECT, INSERT` on those two
append-only tables plus schema `USAGE`.

Migration `000071` also uses conditional grants, so
`worksflow_qualification_promotion_operator` must exist before it runs. Through
`000071` (before later migrations), the exhaustive production posture baseline
is five stable `NOLOGIN` groups, 17 protected/owned boundary tables, 42 owned
indexes, 33 owned routines, 11 owner-only internal routines, and 24
`SECURITY DEFINER` routines. The qualification-promotion operator receives
only non-grantable schema `USAGE`, non-grantable `SELECT` on its two append-only
tables, and non-grantable `EXECUTE` on the single canonical consume routine.
The application and other operators receive no access. A successful consume
atomically appends a ledger row and a `pending` handoff; that handoff is not an
immutable revision and is not proof that a workflow node was submitted.

After migration `000072`, the current exhaustive production posture is 21
owned boundary tables, 52 owned indexes, 37 owned routines, and exactly 25
`SECURITY DEFINER` routines; the protected-table ACL catalog has 22 entries
because it also includes read-only `schema_migrations`. The four CredentialSet
tables are owned by `worksflow_migration_owner` and allow no non-owner table or
column ACL. Their four routines are owner-only with exact signatures and
catalog contracts: immutable/strict/parallel-safe SQL
`credential_set_sha256(bytea)` with no per-function configuration; volatile
PL/pgSQL reject and head-guard trigger functions with respectively
`pg_catalog` and `pg_catalog, <schema>` search paths; and the sole new
`SECURITY DEFINER`, `append_credential_set_event(...)`, pinned to
`pg_catalog, <schema>, pg_temp`. The events and operations ledgers each have
exactly one statement-level `BEFORE UPDATE OR DELETE OR TRUNCATE` reject
trigger; heads has exactly one statement-level
`BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE` guard; the private projection
authorization table has no user trigger. Any extra CredentialSet table,
overload, trigger or non-owner ACL blocks startup posture.

After migration `000073`, the real PostgreSQL catalog baseline is 25 owned
boundary tables, 59 owned indexes, 41 owned routines, 15 owner-only internal
routines and exactly 26 `SECURITY DEFINER` routines; the protected-table ACL
catalog has 26 entries because it also includes read-only
`schema_migrations`. Qualification Evidence adds four owner-only tables and
four owner-only exact-signature routines without adding a sixth stable role or
posture connection. Its SQL SHA-256 helper is immutable, strict,
parallel-safe and pinned to `pg_catalog`; its PL/pgSQL reject and head-guard
trigger functions are volatile, parallel-unsafe `SECURITY INVOKER` routines
with exact fixed paths; and its append routine is the sole new
`SECURITY DEFINER`, pinned to `pg_catalog, <schema>, pg_temp`. The events and
operations ledgers have the exact statement-level immutable triggers, heads
has the exact guarded-projection trigger, and the transaction-local projection
authorization table has none. Any extra Qualification Evidence table, routine,
overload, trigger, non-owner ACL, owner drift or function-catalog drift blocks
startup posture. This owner-only persistence boundary is not a production
operator or permission for the API/application role to orchestrate evidence.

After migration `000074`, the real migrated baseline is 28 protected tables,
27 owned boundary tables, 67 owned indexes, 46 owned routines, 20 owner-only
internal routines and exactly 27 `SECURITY DEFINER` routines. Qualification
Plan adds exactly two migration-owner-only tables, eight valid/ready indexes,
five owner-only exact-signature routines and three enabled user triggers. The
SHA-256 SQL helper and immutable-mutation reject function use `pg_catalog`;
the invoker resolve and Evidence guard use `pg_catalog, <schema>`; and the sole
new definer, freeze, uses `pg_catalog, <schema>, pg_temp`. Freeze returns the
authority table composite while resolve returns the same set type as a stable
SQL invoker. Neither application nor any operator receives table or routine
privileges, and no sixth role or fifth posture DSN is introduced. Because the
new guard is named `guard_qualification_evidence_plan_authority` and is attached
to `qualification_evidence_events`, the exhaustive named Evidence inventory is
now five functions and four triggers even though migration `000073`'s own exact
contract remains four functions and three triggers. Owner, ACL, index state,
trigger enablement, result type, search path, security-mode or extra named
routine drift blocks production startup.

After migration `000075`, the real migrated baseline is 31 protected tables,
30 owned boundary tables, 81 owned indexes, 53 owned routines, 27 owner-only
internal routines and exactly 30 `SECURITY DEFINER` routines. Receipt v3 adds
three migration-owner-only ledgers, fourteen exact indexes, seven owner-only
routines and five enabled user triggers, including the Evidence-v1 Receipt-tail
and Promotion-v1 history-only guards. Exactly the start/append/complete writers
are definers pinned to `pg_catalog, <schema>, pg_temp`; the hash, immutable
reject and two history guards are invokers with their narrower fixed paths.
No application or existing operator receives table or routine access, and no
sixth stable role or fifth posture DSN is introduced. The owner-only
`PostgresStore` is therefore an internal durable adapter, not a production
Receipt operator identity. Catalog posture rejects owner/ACL/mode/result/path,
index-key/constraint, unconditional-trigger, or extra named-object drift.

After migrations `000076` and `000077`, that historical migrated baseline is
32 protected tables, 31 owned boundary tables, 86 owned indexes, 64 owned
routines, 36 owner-only internal routines and exactly 34 `SECURITY DEFINER`
routines. Canonical Review adds one owner-controlled receipt table, five exact
indexes, four enabled triggers (three ordinary plus one deferred constraint
trigger), twelve functions and four definers. The application has no receipt
table access and can execute only the exact issuer and Boolean probe; the
owner-side resolver and all internal helpers remain owner-only. Migration
`000077` adds three owner-only `IMMUTABLE STRICT PARALLEL SAFE SECURITY
INVOKER` helpers (two SQL predicates plus the PL/pgSQL exact-timestamp
round-trip predicate) and the single append-only decision trigger. Catalog posture
rejects extra objects, overloads, non-owner ACL, result/mode/path drift,
disabled triggers, wrong index bindings, or a changed deferred-trigger
contract. See `docs/canonical-review-approval-authority.md` for the wire,
recovery, rollback, and downstream-consumption contract.

At current head through `000079`, the catalog posture pins 32 protected-table
ACL entries, 31 legacy named owner-boundary tables, 87 named owner-boundary
indexes, 69 named owner-boundary routines, 36 named owner-only internal
routines and exactly 52 `SECURITY DEFINER` routines. Migration `000078`
requires the sixth
stable `worksflow_qualification_policy_operator` role to exist before its
conditional grants run. Its four Qualification Policy and six Workflow Input
tables are an exact migration-owner-only boundary with no non-owner table or
column ACL. The application executable boundary is now exactly 15 functions:
the prior twelve plus Workflow Input freeze, operation inspection and
node-scoped resolution; those three routines also have exact composite/JSONB
set results, volatility, language, strictness, parallel-safety and search-path
contracts. The qualification-promotion boundary is exactly
the consume routine plus the Qualification Policy and Workflow Input
current-authority assertions. The qualification-policy boundary is exactly
the issue, operation-inspection, authority-resolution and current-resolution
routines documented below; it has no relation, column or sequence access and
cannot execute the current-authority assertion. Migration `000079` keeps its
new workflow-engine/v3 guards owner-only. Production posture exact-catalogs
all 23 migration `000078` triggers (12 ordinary and 11 deferred constraint
triggers), all five migration `000079` triggers (three ordinary and two
deferred constraint triggers), and their ten trigger functions. It pins the
enabled/internal state, `tgtype`, exact `UPDATE OF` column ordinals, zero
arguments, function binding, constraint binding/validation/deferral flags and
the complete named inventory; every trigger on the ten dedicated Policy/WIA
tables is counted, even when it has an unrelated name. The functions are
independently pinned to the migration owner, owner-only ACL, zero-argument
trigger signature, PL/pgSQL, exact invoker/definer mode, volatility,
strictness, parallel safety and fixed search path. Any role, owner, ACL,
signature, result, security mode, search-path or total-inventory drift blocks
startup. A second allowlist counts every user trigger on the four shared
`workflow_definition_versions`, `workflow_runs`, `workflow_node_runs` and
`workflow_run_events` tables: its exact total is twelve, including the three
legacy profile/governance triggers from migrations `000016`/`000019`. Those
legacy triggers use the same exact table/name/function/type/column/enablement/
argument/`WHEN` evaluator, and their three invoker functions have independent
signature, language, execution-attribute, path, unreachable-owner and
exact historical owner-plus-application, non-grantable ACL contracts with no
third-party grantee.

At current head through `000082`, the exhaustive baseline is 50 protected
table ACL entries, 49 owned boundary tables, 157 owned boundary indexes, 115
owned boundary routines, 36 owner-only internal routines, and exactly 81
`SECURITY DEFINER` routines. Migration `000080` is independently pinned to
eight tables, 28 indexes, eleven triggers, 24 functions, and eighteen
definers. Migration `000081` retains its frozen eight-table, seven-trigger,
sixteen-function Promotion subset; after `000082`, the total Promotion-named
inventory is twelve tables, eight triggers, and nineteen functions.
Migration `000082` is separately pinned to four owner-only tables, nineteen
exact valid indexes (including PostgreSQL's canonical truncated constraint
index names), seventeen ordinary/deferred triggers, and ten functions. Its
table contract pins every column ordinal/type/nullability/default/collation,
including the explicit top-level creation transaction ID on the binding,
lineage member and completion, plus each exact canonical transaction-ID check
constraint. Exactly
five Handoff functions are definers: mutation rejection, pending dispatch,
completion, inspection, and deferred closure validation. Only completion and
inspection have non-grantable `EXECUTE` for the Handoff group; its other eight
functions are owner-only. The shared Workflow relation trigger allowlist is
therefore fifteen at current head, including the three Handoff closure
triggers on run, node, and event tables.

The startup posture also pins five profile-v3 hash-bearing contracts: the
Workflow Input freeze routine, three profile-v3 enforcement trigger routines,
and the Workflow Input authority table check constraint must contain
`854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104`
and must not retain the pre-activation draft hash. This source cascade does not
authorize rewriting migrations already applied in an environment.

The index-specific boundary is exact:

- `PUBLIC` has no trusted-schema table, sequence or routine privilege. Existing
  predecessor `SECURITY INVOKER` routines are explicitly re-granted to the
  application group so old API operations keep working; its executable
  `SECURITY DEFINER` set is exactly ten Candidate/claim mutation functions.
- The application group has only the documented direct DML on the four index
  tables and read-only `schema_migrations`. It has no GC-private table access
  or Golden fault, CredentialSet, Qualification Evidence, or Qualification
  Plan ledger access and cannot execute a GC or internal trigger/guard
  function. The index-specific executable `SECURITY DEFINER` inventory remains
  exactly ten Candidate/claim functions. At current head `000079`, the two
  narrowly granted Canonical Review functions and three Workflow Input
  functions yield the application posture total of fifteen.
- The operator group has no direct privilege on the index, Candidate or six
  GC-private tables. It can execute only the four GC plan/execute/inspect/
  readiness functions.
- The Golden fault operator is a different group with only schema `USAGE` and
  non-grantable `SELECT, INSERT` on the two append-only Golden fault tables. It
  cannot be reachable from the API/application group.
- The trusted schema has no explicit column ACL. API and operator startup also
  reject any `ADMIN OPTION` held by the session or a reachable role, so neither
  process can delegate its stable group authority.
- All fourteen index/GC exposed functions are `SECURITY DEFINER`, owned by the
  exact `worksflow_migration_owner` role, pinned to the exact trusted search path
  `pg_catalog, <schema>, pg_temp`, and revoked from `PUBLIC`.
- The exact-signature `sandbox_checkpoint_is_exact` dependency is separately
  constrained to SQL/STABLE `SECURITY INVOKER`, a scalar Boolean result, the
  same fixed path, and non-grantable `EXECUTE` for only migration-owner and
  application. After `000068`, the ten internal trigger/guard routines are
  owner-only; the
  migration removes every historical non-owner grantee from both sets.
- The API startup
  posture also rejects a different `session_user`/`current_user`, schema or
  database creation, object ownership, unexpected inherited or `SET ROLE`
  authority, broad table/function grants and a non-exact function result
  contract.

The migration ledger records separate SHA-256 identities for every canonical
up/down pair. General checksum or version drift is never accepted. The
migrator has a narrow, audited recovery table for exact historical identities:
down-only hardening for `000073`–`000075`; the reviewed `000077` repair only
after exact repaired `000083`; old `000082` only after current `000084` is
physically applied; and exact old `000083` only by replaying the complete
current `000083` Up plus the ledger compare-and-swap in one transaction. That
repair requires an owner/grant-option credential and fails closed before DDL
when its catalog preflight is not exact. An earlier byte-identical Candidate
sandbox gate recorded as `000084` may be compare-and-swap relocated to
`000086`; only the temporary states `1..83/84/85 + exact 86` are recoverable.
Unknown versions, other gaps, intermediate `000083` identities, missing down
digests and any checksum drift remain blocking. The API's `VerifyCurrent` path
is read-only and accepts only the complete current head.

Apply the schema with only the migrator identity, then start the API with its
own schema and DSN:

```sh
# Values are injected by the deployment secret manager.
export WORKSFLOW_MIGRATION_POSTGRES_DSN
export POSTGRES_DSN

WORKSFLOW_MIGRATION_POSTGRES_SCHEMA=worksflow go run ./cmd/migrate
POSTGRES_SCHEMA=worksflow make run
```

The migrator, API and role-specific operators each accept a canonical
PostgreSQL URL and a separate canonical lowercase unquoted schema selector.
Identity-changing DSN
query parameters such as `role`, `options`/`search_path`, service files and
password overrides are rejected; schema selection is injected programmatically.

### Standalone production PostgreSQL posture check

`cmd/production-postgres-posture` is an independent, read-only posture checker
for nine concurrently held connections inspected within one bounded check
window: the application LOGIN, the one-shot migrator LOGIN, a separate
qualification/auditor LOGIN, the qualification-promotion and
qualification-policy LOGINs, the three isolated Qualification Input LOGINs,
and the independent Handoff LOGIN.
Each connection's catalog query takes its own PostgreSQL snapshot; the result
is not an atomic cross-identity snapshot. The auditor is not an operator. It
must have only database connectivity and catalog visibility: no membership or
`SET ROLE` path to any of the ten stable groups; no trusted-schema `USAGE`,
data/column/sequence privilege, function execution, or object ownership.

The promotion LOGIN reaches only
`worksflow_qualification_promotion_operator`, has schema `USAGE` without
`CREATE`, no table, column, or sequence access, and `EXECUTE` on exactly
`consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid)`,
`inspect_qualification_promotion_v2_operation(uuid)`,
`inspect_historical_qualification_promotion_v1_operation(uuid)`, and the exact
migration-80 resolver
`resolve_qualification_input_precommit_for_promotion_v1(uuid,uuid)`. The
historical v1 consume and direct WIA/Policy assertions are owner-only. The
handoff resolver/assertion functions are also owner-only and belong to
migration `000082`'s separately postured Handoff identity; the Promotion LOGIN
cannot use them or own any trusted-schema object.

The policy LOGIN reaches only `worksflow_qualification_policy_operator`, has
schema `USAGE` without `CREATE`, and can execute exactly
`issue_qualification_policy_authority_v1(...)`,
`inspect_qualification_policy_operation_v1(uuid)`,
`resolve_qualification_policy_authority_v1(uuid)`, and
`resolve_current_qualification_policy_authority_v1(uuid,text,text)`. It cannot
execute the owner-only
`assert_current_qualification_policy_authority_v1(uuid)`, and has no table,
column, sequence, ownership, other routine, or direct LOGIN ACL.

The input-precommit LOGIN can execute only
`issue_qualification_input_precommit_v1(...)`,
`inspect_qualification_input_precommit_operation_v1(uuid)`, and
`resolve_qualification_input_precommit_authority_v1(uuid)`. The source-verifier
LOGIN can execute only source receipt admit, inspect, and exact-admission
resolve; the credential-resolver LOGIN has the corresponding three credential
receipt routines. None of these three has table, column, sequence, ownership,
or cross-operator routine authority.

The Handoff LOGIN reaches only
`worksflow_qualification_handoff_operator`, has schema `USAGE` without
`CREATE`, no table, column, sequence, ownership, direct LOGIN ACL, Promotion
resolver, or WIA assertion authority, and can execute exactly
`complete_qualification_promotion_v2_handoff(uuid)` and
`inspect_qualification_promotion_v2_handoff_completion(uuid)`. The other
eight Handoff functions are owner-only. No other audited identity may reach
the Handoff group or either runtime routine.

The Promotion, three Qualification Input, and Handoff LOGINs must be
`ROLINHERIT` identities with one direct membership using
`INHERIT TRUE, SET FALSE, ADMIN FALSE`; their functions authenticate the
original `session_user` while requiring `current_setting('role') = 'none'`.
The checker rejects an extra membership, `SET ROLE` path, or substituted
operator grant. Application, migrator, auditor, policy, and Promotion cannot
inherit any Qualification Input or Handoff group.

These nine audited LOGINs are distinct from the ten stable `NOLOGIN` group
roles. The runtime gate remains
`disabled-pending-input-precommit-authority-canary`; a passing posture result is
not activation authority.

Each DSN is loaded from a different absolute credential file. Credential files
must be single-link, non-symlinked, owned by the checker process, and mode
`0400` or `0600`. The nine URLs must name different LOGINs and different
passwords while targeting the same host, port, database, schema and TLS trust
anchor. Production URLs must use `sslmode=verify-full`, one common absolute
`sslrootcert`, and `target_session_attrs=read-write`; client certificate/key
parameters and all identity/session overrides are rejected. The CA file must
be a bounded parseable PEM certificate in a root- or process-owned directory
chain with no symlink, hardlink, or group/world-writable component. The catalog
query also confirms that each live session uses TLS and is neither a recovery
server nor read-only.

```sh
export WORKSFLOW_PRODUCTION_POSTGRES_APP_DSN_FILE=/run/worksflow-secrets/posture-app.dsn
export WORKSFLOW_PRODUCTION_POSTGRES_MIGRATOR_DSN_FILE=/run/worksflow-secrets/posture-migrator.dsn
export WORKSFLOW_PRODUCTION_POSTGRES_QUALIFICATION_DSN_FILE=/run/worksflow-secrets/posture-auditor.dsn
export WORKSFLOW_PRODUCTION_POSTGRES_PROMOTION_DSN_FILE=/run/worksflow-secrets/posture-promotion.dsn
export WORKSFLOW_PRODUCTION_POSTGRES_PROMOTION_SESSION_AFFINITY=direct
export WORKSFLOW_PRODUCTION_POSTGRES_PROMOTION_RUNTIME_GATE=disabled-pending-input-precommit-authority-canary
export WORKSFLOW_PRODUCTION_POSTGRES_POLICY_DSN_FILE=/run/worksflow-secrets/posture-policy.dsn
export WORKSFLOW_PRODUCTION_POSTGRES_INPUT_PRECOMMIT_DSN_FILE=/run/worksflow-secrets/posture-input-precommit.dsn
export WORKSFLOW_PRODUCTION_POSTGRES_INPUT_PRECOMMIT_SESSION_AFFINITY=direct
export WORKSFLOW_PRODUCTION_POSTGRES_SOURCE_VERIFIER_DSN_FILE=/run/worksflow-secrets/posture-source-verifier.dsn
export WORKSFLOW_PRODUCTION_POSTGRES_SOURCE_VERIFIER_SESSION_AFFINITY=direct
export WORKSFLOW_PRODUCTION_POSTGRES_CREDENTIAL_RESOLVER_DSN_FILE=/run/worksflow-secrets/posture-credential-resolver.dsn
export WORKSFLOW_PRODUCTION_POSTGRES_CREDENTIAL_RESOLVER_SESSION_AFFINITY=direct
export WORKSFLOW_PRODUCTION_POSTGRES_HANDOFF_DSN_FILE=/run/worksflow-secrets/posture-handoff.dsn
export WORKSFLOW_PRODUCTION_POSTGRES_HANDOFF_SESSION_AFFINITY=direct
export WORKSFLOW_PRODUCTION_POSTGRES_SCHEMA=worksflow
export WORKSFLOW_PRODUCTION_POSTGRES_POSTURE_TIMEOUT=30s

go run ./cmd/production-postgres-posture > posture-result.json
```

The checker dynamically closes over every trusted-schema table, partition,
sequence, view, index, column ACL, routine and ownable schema object, so new
migration/governance objects do not disappear behind the older exact-tree
static counts. It reuses the API's exhaustive production posture and
additionally requires migrator authority to come only through
`worksflow_migration_owner`; the auditor must remain entirely outside the
trusted data plane; the promotion identity must match its no-data-access,
exact four-routine Promotion-v2-plus-input-resolver boundary; the policy
identity must match its exact four-routine boundary; and each Qualification
Input identity must match its exact three-routine boundary with no data access.
The Handoff identity must match its exact two-routine, no-data-access boundary
and sole direct group membership. No non-migrator identity or reachable group
may own a trusted-schema relation, routine, type,
collation, conversion, operator, operator class/family, text-search object,
extended statistic or extension. It never prints DSNs, secret
values, credential paths, endpoints, or driver errors. Exit `0` means this
standalone posture passed; `2` is invalid configuration/trust material, `3` is
an unsafe privilege posture, and `4` is an operational/inconclusive check.

The JSON uses
`worksflow-production-postgresql-posture-result/v2` and always declares
`evidenceClass=standalone-point-in-time-posture-check` for compatibility and
excludes
`external-qualification-receipt`, `gc-scheduler-qualification`,
`promotion-authority`, `promotion-runtime-activation`, and
`input-precommit-authority-canary`. The Promotion, input-precommit,
source-verifier, credential-resolver, and Handoff DSNs must each be declared
as a direct connection or a pool in session-pooling mode; `transaction-pool`
and missing or unknown affinity values fail configuration validation because
the authority flows require physical-session continuity. The only accepted
runtime gate is
`disabled-pending-input-precommit-authority-canary`. This checker does not
start, register, or authorize a Promotion worker or trigger; changing that
gate requires migration `000080`'s precommit-authority rollout/canary,
migration `000081`'s updated consume canary, and a separate activation review.
A successful
check is therefore not the planned
production PostgreSQL qualification artifact, is not an atomic global
snapshot, does not test the GC scheduler's same-run recovery, and is not
consumed by Qualification Receipt or promotion. External composition must
still obtain a maintenance-window or equivalent target-environment Receipt.

### Exact-tree index retention operator

Migration `000066` adds append-only run, capability, receipt and tombstone
facts plus two private short-lived authorization tables. The latter bind the
exact transaction ID, backend PID, tenant, tree, capability and blob; they are
removed before the successful transaction returns and never become reusable
capabilities. Planning ranks all ready publications before applying protection
filters. Execution takes the exact tree lock and project-quota lock, rechecks
the whole manifest publication under CAS, and returns exactly one immutable
`deleted`, `protected`, `stale` or `expired` receipt.

Every Candidate `current_tree_hash` protects its tree regardless of Candidate
status, and a live build claim also protects its tree. A blob is deleted only
after the target members/manifest are removed and no remaining tree in that
project references the content hash. Tombstones bind the deleted publication
and one-shot capability, so rebuilding the same tree produces a distinct
publication instead of colliding with old deletion evidence.

The policy defaults and hard limits are:

| Input | Default | Database/CLI boundary |
|---|---:|---:|
| retention | 30 days (`720h`) | at least 7 days (`168h`) |
| keep per project | 8 | at least 8 |
| batch size | 25 | 1–100 |
| capability TTL | 10 minutes | greater than zero, at most 15 minutes |

Run the dedicated binary with a distinct low-privilege operator `LOGIN`. The
run ID is a scheduler-supplied, canonical non-zero UUID and must remain stable
across a crash or ambiguous result:

```sh
# Inject this environment value from the secret manager; it has no default.
export WORKSFLOW_REPOSITORY_INDEX_GC_POSTGRES_DSN

RUN_ID=8f0f3d35-4c4d-4b2f-b91d-e5d4d0f45847
go run ./cmd/repository-index-gc \
  -postgres-schema worksflow \
  -run-id "$RUN_ID" \
  -retention 720h \
  -keep-per-project 8 \
  -batch-size 25 \
  -capability-ttl 10m \
  -timeout 5m

# Ambiguous response or process crash: repeat the exact command and RUN_ID.
```

The same operation is available through the opt-in Compose maintenance
profile; it deliberately has no usable DSN, schema or run-ID default:

```sh
export WORKSFLOW_REPOSITORY_INDEX_GC_POSTGRES_DSN
export WORKSFLOW_REPOSITORY_INDEX_GC_POSTGRES_SCHEMA=worksflow
export WORKSFLOW_REPOSITORY_INDEX_GC_RUN_ID=8f0f3d35-4c4d-4b2f-b91d-e5d4d0f45847
docker compose --profile maintenance run --rm repository-index-gc
```

Never create a replacement run while the first run has an unknown outcome.
The same ID with changed policy is rejected; same-ID execution replays the
existing capability/receipt identities. After the original run is fully
inspected and terminal, a later scheduled batch receives a new run ID. A
`protected`, `stale` or `expired` outcome is terminal evidence, not permission
to bypass the guard with manual `DELETE`.

Rollback is intentionally stricter than “no deletion happened.” It first takes
an `ACCESS EXCLUSIVE` fence over all six GC control/audit/authorization tables,
then refuses rollback if any run, capability, receipt, tombstone or transient
authorization row exists. This prevents a concurrent execute from committing
between a clean count and destructive DDL and prevents append-only non-deletion
receipts from being discarded.

Focused real-PostgreSQL canaries cover migration `000066`, the staging/
production API role posture, and the real low-privilege operator LOGIN with
interrupted same-run recovery. The
operator test also rejects superuser `SET ROLE`, unexpected Candidate definer
access and function owner/result/search-path tampering. These results prove
repository-internal contracts only. Production role creation, full application
DML, dedicated schema ownership and secret injection remain external
deployment responsibilities and require their own evidence.

Migration `000012` makes each workflow root unique within
`(project, workflow run, manifest group, root ordinal)`. `manifest_group_key` is
the stable ManifestCompiler NodeRun ID; derived rows inherit it and the root
ordinal. Composite foreign keys keep roots and parents in one project and bind
`(workflow_run_id, project_id)` to the owning run. A partial unique index gives
each parent at most one direct child, while root/workspace uniqueness prevents
the same workspace from appearing twice in a root lineage. Pre-group workflow
rows are deterministically backfilled into the `legacy` group and ordered by
creation identity so in-flight v1 runs remain readable.

Migration `000013` creates `design_imports`. Its triggers make the imported
snapshot identity immutable and require every referenced PageSpec, Prototype,
base revision, InputManifest, OutputProposal and applied Prototype revision to
belong to the same project and exact lineage. Migration `000014` creates
`artifact_collaboration_states` and `document_generation_commands`, extends
OutputProposal metadata with AI provider/model identity, and rejects
cross-project bindings, resolved owners and downstream generation relations.
The generation command's request hash, source-binding ETag and owner snapshot
are immutable so a recovered or replayed command cannot silently change scope.
Migration `000015` intentionally fails with an actionable diagnostic if legacy
data contains multiple active ImplementationProposals on one leaf; it never
guesses which reviewed Proposal to discard. Operators must explicitly reject or
mark obsolete rows stale before retrying the migration.

Migration `000017` is a fail-closed contract migration. It deliberately aborts
if the database already contains a non-`legacy` workflow manifest group and it
does not derive `delivery_slice_id` from `workflow_runs.context` or a compiler
`BuildManifest`: that output is the value the new activation barrier must
independently verify, so copying it would let a historical SliceIDs permutation
self-certify. Before rollout, pause manifest writers and inventory affected rows:

```sql
SELECT project_id, workflow_run_id, manifest_group_key, root_ordinal,
       id, root_manifest_id, derived_from_id, content_ref, content_hash
FROM application_build_manifests
WHERE workflow_run_id IS NOT NULL
  AND manifest_group_key <> 'legacy'
ORDER BY project_id, workflow_run_id, manifest_group_key, root_ordinal,
         created_at, id;
```

For each root, an operator must resolve and hash-check the immutable Workbench
Bundle at `content_ref`, verify its row/root/parent coordinates, and establish
its `DeliverySliceID` from the independently authoritative DeliverySlice and
approved Prototype/Blueprint lineage. Every derived row must inherit that same
identity. Preserve the evidence and mapping as an audited deployment artifact;
never use compiler `SliceIDs` as the source of truth. Environments with no such
rows can apply canonical `000017` directly. Environments with rows must either
quarantine genuinely pre-contract data as `legacy`, archive obsolete groups, or
ship a reviewed environment-specific replacement for `000017` that installs
the verified mapping and the same constraints/triggers atomically. Do not edit
an already recorded migration checksum. Keep writers paused until a swapped-ID
CAS probe is rejected and exact read/export/graph/conversation probes pass.

Large immutable JSON and binary-safe content is stored in MongoDB. PostgreSQL
stores the content reference and SHA-256 relation. Content creation uses a
recoverable pending/finalize protocol; the optional reconciler finalizes
committed pending objects and removes stale orphans without making MongoDB the
transaction coordinator.

Business state and an outbox row are committed together in PostgreSQL. The
outbox worker publishes to JetStream with an event ID for deduplication. The
WebSocket fan-out consumes the durable stream; WebSocket delivery is never the
only copy of a state change.

## AI generation and workflow boundary

Artifact generation is a two-phase protocol:

1. `POST /v1/projects/:projectId/input-manifests` freezes an instruction, an
   optional exact base revision, approved upstream source revisions, their
   content hashes, and the expected output schema.
2. `POST /v1/input-manifests/:manifestId/generate` asks the configured AI
   provider for an OutputProposal. Each proposal operation is accepted or
   rejected separately with `If-Match`; apply rechecks the manifest, proposal
   version, base revision and hashes before writing the validated artifact
   draft.

`requirement_baseline`, `workspace`, `quality_report`, and `test_report` are
system-managed artifacts. Generic artifact create/draft/revision, OutputProposal
create/decide/apply, and review submit/decide paths reject them; the fixed
`REQUIREMENT-BASELINE` and `WORKSPACE-MAIN` keys are reserved as well. For
human-editable targets, generation checks before calling AI that the exact base
is still latest and that any active draft has the same base, status, schema,
content hash, and frozen source lineage. Apply repeats that check while holding
database locks, so schema-only or source-only edits cannot be overwritten.

Application generation uses the same boundary at a larger scale. A frozen
ApplicationBuildManifest pins all requirement, blueprint, PageSpec, prototype
and workspace inputs. Workbench returns an ImplementationProposal; applying it
creates a new immutable WorkspaceRevision rather than mutating the manifest or
silently overwriting current files.

Every workflow-created root also freezes its ManifestCompiler NodeRun as
`manifestGroupKey` and its zero-based `rootOrdinal`. Different compiler nodes
in one WorkflowRun therefore own independent ordered groups and may both have
ordinal zero. A compiler retry after partial success reuses an existing root
only when run/project/started-by/compiler-node, ordinal, delivery slice,
prototype ref, and immutable payload identity all match; otherwise it fails
closed instead of colliding with or adopting another group.

Creating those roots does not publish them to generation. The workflow CAS
must first commit the matching ManifestCompiler NodeRun as `completed` together
with its hash-valid BuildManifest output. The output must pin the complete root
set in exact ordinal order, and the commit transaction locks and compares every
root before succeeding. Bundle reads, lineage, rebase, conversation discovery,
and manual or automatic generation enforce the same activation proof. A
partial retry, terminal compiler failure, or lost lease can therefore leave
recoverable immutable rows, but cannot expose a half-compiled application
input.

If those frozen product inputs must be applied on top of a newer workspace,
`POST /v1/build-manifests/:bundleId/rebase` accepts only
`{"workspaceRevision":{"artifactId":"...","revisionId":"...","contentHash":"sha256:..."}}`.
The service validates that exact revision and creates a derived immutable
ApplicationBuildManifest; it never rewrites the source manifest. The derived
manifest records both its root and direct parent lineage, while its manifest
hash covers the newly pinned workspace. The authenticated command requires
`Idempotency-Key` and returns `201 Created` with the new manifest `Location`
and `ETag`. Clients generate a new ImplementationProposal from that returned
manifest instead of retrying a stale proposal against changed workspace state.
Only the structural leaf may be rebased. Creating its child atomically marks
the parent `invalidated` and persists every non-final parent Proposal as
`stale`; an exact retry returns the existing child, while another workspace for
the same parent conflicts. Database uniqueness and service locks jointly
prohibit sibling children. Generation also compares the leaf's workspace pin
with the project's current approved Workspace before invoking AI.
Once any proposal in a root lineage is applied, that lineage is complete:
consumed manifests reject proposal regeneration and the service rejects further
derived manifests from the same root.

The frontend refresh boundary for that lineage is
`GET /v1/build-manifests/:rootId/lineage-state` (a derived bundle ID is also
accepted and normalized to its root). It must call this endpoint
after reconnect, rebase, or implementation-proposal state changes instead of
selecting a "latest" manifest from local timestamps. The authenticated response
contains `rootBundleId`, the authoritative `activeBundle`, its optional
`currentProposal`, the project's exact latest approved
`currentWorkspaceRevision`, and an ordered `lineage` summary with direct-parent,
workspace, status, creation time and latest non-stale proposal identity. The
frontend uses the top-level workspace ref to decide whether the active bundle
must be rebased; it must not substitute the bundle's older base workspace. A
deterministic strong `ETag` covers the complete returned state, including the
current workspace pin and every lineage proposal identity/status/version; a matching
`If-None-Match` returns `304 Not Modified`. The service authorizes the root
project's view scope and preserves forbidden/not-found semantics, so clients
cannot use lineage refresh to inspect another project.

The returned order follows direct-parent edges, not timestamps; a branch,
cycle, disconnected row, cross-group row, or cross-project row is a conflict.
Publish accepts a BuildManifest ID as a root-lineage selector, not as trusted
producer provenance. For the exact approved WorkspaceRevision, the server
follows its applied ImplementationProposal and resolves the unique consumed,
non-invalidated structural leaf in the selector's project/run/root lineage.
The DeploymentVersion stores that resolved root or derived leaf ID; selectors
from another root, run, project, or a non-consumed/non-leaf producer are
rejected.

Draft lineage and revision lineage have different lifetimes. The ordered rows
in `artifact_draft_sources` describe the sources for the next candidate
revision and may change only through an ETag-guarded draft update. Creating a
revision copies that exact set into `artifact_revision_sources`, including the
source artifact/revision/hash, optional anchor, purpose, required flag and
ordinal. Revision reads project those frozen rows; they never reconstruct
history from the current draft. Every required source also has a revision-level
dependency and matching trace coverage so canonical review can prove the
lineage instead of trusting a client-supplied graph.

### Typed/versioned DAG and built-in definitions

WorkflowDefinition and WorkflowRun are separate persisted records. A run pins
one published Definition Version, so editing or publishing a newer graph never
changes an active run. Definition validation checks the DAG, unique entry,
reachability, typed `fromPort -> toPort` compatibility, complete condition
branches, fan-out/merge pairing and terminal paths. The runtime then creates a
canonical immutable NodeInputEnvelope from only the incoming edge lineage and
validates the target schema again before invoking its runner.

This is the implementation boundary for freely composed processes. AI receives
only a frozen InputManifest or NodeInputEnvelope and returns a typed Proposal or
node output. Artifact writes, human review, conditions, fan-out, merge,
ManifestCompiler, Workbench, quality and publish remain typed nodes controlled
by the server. The built-in full product loop is an installed versioned
template, not a hard-coded worker sequence. New projects receive its historical
v1/v2/v3 versions, the frozen `workflow-engine/v1`-pinned v4, and the published
`workflow-engine/v2`-pinned v5 in the project-creation transaction. The explicit
startup provisioner upgrades existing projects without performing writes in
GET/List handlers or altering old definitions or runs.

`workflow-engine/v2` changes only reconciliation. When a Condition excludes a
FanOut route before it creates any slice, v2 cancels that FanOut's paired Merge
after proving the FanOut has no effective predecessor, so an alternative path
can continue through a shared tail. A valid but unfinished predecessor keeps the
Merge pending. The exact legacy/v0 and v1 descriptors retain their frozen input,
execution, validation, result-apply and old reconcile entry points; they never
dispatch through this v2 rule.

### Blueprint Selection compilation and fan-out

`POST /v1/projects/:projectId/blueprint-selections/compile` is the only route
that compiles client-selected Blueprint node IDs. It requires
`Idempotency-Key`, `If-Match` for the current Blueprint Artifact, an exact
approved Blueprint Revision and 1 to 100 stable node anchors. The service reads
the immutable Blueprint content, sorts the selected nodes and internal edges,
resolves current approved PageSpec/Prototype bindings, includes the Blueprint's
approved source context, and derives `selectionId` from canonical content. It
returns an immutable `blueprint.selection` InputManifest; clients cannot supply
the resulting scope, sources or ID.

The product exposes three operations over that same frozen manifest:

1. A documentation operation creates a document scaffold/revision and a
   `selection.documentation` OutputProposal. Its derived manifest must name the
   parent Selection Manifest and reproduce its scope and exact source set.
2. A prototype operation creates formal Prototype drafts only for selected
   Pages that have an exact PageSpec and no approved Prototype.
3. A Workbench operation starts the published `blueprint-selection-app` v4
   definition only when every selected Page has both exact approved bindings.
   Historical v3 remains pinned to `workflow-engine/v1`; current v4 is pinned to
   `workflow-engine/v2`.

The selection definition runs `artifact_input -> blueprint_selection_page
fan_out -> selection_passthrough -> merge -> manifest_compiler ->
workbench_build -> quality_gate -> publish`. Runtime adapters revalidate the
manifest's root and node sources, reject bindings outside the selected scope,
and require compiled DeliverySlices to match every selected Page exactly. A
different selection therefore requires a new manifest instead of editing an
old one.

### Document collaboration, downstream generation and sync-back

Document collaboration routes are backed by PostgreSQL facts rather than local
editor state:

```text
GET|PUT /v1/artifacts/:artifactId/member-bindings
GET     /v1/projects/:projectId/document-graph
POST    /v1/projects/:projectId/documents/generate-downstream
POST    /v1/projects/:projectId/documents/sync-back
```

Member-binding replacement requires the binding-set ETag, `edit` permission,
only same-project users, no duplicate user/role pair, and at least one Owner.
Supported roles are `owner`, `assignee`, `downstreamOwner`, `reviewer`, and
`watcher`.

Downstream generation accepts only an exact approved document Revision and a
durable command key. The service snapshots the source binding ETag and resolved
downstream owners, deterministically checkpoints the target scaffold Revision,
InputManifest and OutputProposal, and persists provider/model identity. A
matching retry resumes or returns that exact result; reusing the key for a
different request conflicts. The output remains a reviewable Proposal and does
not become an approved document automatically.

Sync-back also returns only an OutputProposal. The target must still be the
document's exact current approved Revision. Provenance can name a current
approved WorkspaceRevision, an applied ImplementationProposal, a consumed
structural-leaf BuildManifest, or a ready Deployment. Resolution requires the
same project, the current Workspace and a unique applied implementation chain;
the resulting workspace, manifest/proposal hashes and optional preview URL are
frozen into a dedicated sync-back InputManifest.

The document graph is a deterministic read projection over artifacts,
dependencies, trace links, member bindings, AI manifests/proposals, workflow
runs, BuildManifests, implementations and deployments. It exposes the platform
input/output chain for collaboration UI but is not independently editable.

### Design Import Center

The implemented Design Import route family is:

```text
GET  /v1/projects/:projectId/design-import-capabilities
GET  /v1/projects/:projectId/design-imports
GET  /v1/design-imports/:designImportId
POST /v1/projects/:projectId/design-imports
POST /v1/design-imports/:designImportId/decision
```

The capability response is authoritative per deployment. It advertises Figma,
Penpot, Excalidraw, tldraw, Storybook, Ladle and generic upload formats, with a
decoded size limit derived from `CONTENT_MAX_BYTES` and capped at 8 MiB.
Remote connectors currently return `remoteEnabled: false`; the service does not
fetch remote URLs, simulate OAuth or accept connector credentials.

Creation requires `edit` and durable idempotency. The service validates the
exported file and active-content policy, stores a content-addressed immutable
snapshot, pins the exact current approved PageSpec in an InputManifest, and
creates a canonical Prototype OutputProposal. A decision requires `review`,
`Idempotency-Key`, `If-Match` and the proposal apply permission. Approval alone
applies the proposal and creates a Prototype Revision with exact PageSpec,
proposal and manifest lineage; rejection leaves the target unchanged. Existing
Prototype updates additionally require an exact required PageSpec source match.

### Governed conversation control plane

The Workbench conversation panel is a command control plane, not a second
workflow executor. Its implemented route family is:

```text
GET|POST  /v1/projects/:projectId/conversations
GET|PATCH /v1/projects/:projectId/conversations/:conversationId
GET|POST  /v1/projects/:projectId/conversations/:conversationId/messages
GET|POST  /v1/projects/:projectId/conversations/:conversationId/summary-checkpoints
GET       /v1/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId
GET       /v1/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId/source-messages
POST      /v1/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId/decision
GET|POST  /v1/projects/:projectId/conversations/:conversationId/intent-proposals
POST      /v1/projects/:projectId/conversations/:conversationId/intent-proposals/generate
GET       /v1/projects/:projectId/conversations/:conversationId/intent-proposals/:proposalId
POST      /v1/projects/:projectId/conversations/:conversationId/intent-proposals/:proposalId/decision
GET       /v1/projects/:projectId/conversations/:conversationId/commands[/:commandId]
POST      /v1/projects/:projectId/conversations/:conversationId/commands/:commandId/execute
POST      /v1/projects/:projectId/conversations/:conversationId/commands/:commandId/reject
```

For AI this control plane is an explicit input/output boundary. Input consists
of the immutable user message plus server-resolved Definition Versions,
the validated Manifest ref, cryptographic source/intent bindings and allowable
Workbench targets. Client-controlled source anchors and manifest purposes are
not copied into the provider view; the server backfills their exact validated
values after generation. Output is `{proposal,message,provider,model}` under a
constrained schema. The model
cannot append its own authoritative assistant record, accept the proposal,
execute a command, mutate an Artifact, or schedule a WorkflowRun.

Discovery never accepts a browser candidate list and never truncates a set as
if it were complete. Compatible definitions are loaded in bounded database
batches and exposed to AI as a compact metadata/I/O/node index; 512 candidates
or a 256 KiB index is an explicit conflict that requires catalog narrowing.
Workbench rows are fully checked through the authoritative lineage/generation
gate before the 100-target limit is applied; a 101st executable target is also
an explicit conflict. Thus a large non-ready fan-out cannot hide a later ready
run or silently steer the model to a different process.

The message context is equally fail-closed. Without a checkpoint, generation
reads the complete, continuous sequence from message 1 through the selected
user trigger. The 200-entry and 128 KiB limits are applied to the actual
canonical provider conversation payload. An over-budget request returns a
typed conflict with the exact recommended cutoff. The client creates an
immutable prefix checkpoint, and a different member with `review` permission
must inspect its exact paginated source delta and approve it with `If-Match`.
Subsequent generation sends only the approved summary plus every continuous
tail message through the trigger; it never sends or silently drops a covered
raw message. Workbench continuation first scopes targets to runs
already executed by this conversation. For a first-time handoff the client may
send an exact `{runId,rootBundleId}` navigation hint, which is revalidated
server-side and never treated as an execution result. Every AI-visible target
also carries server-resolved slice ID/key/title. Duplicate page semantics from
different runs are an explicit ambiguity unless an exact hint resolves them.
The provider instruction treats every descriptive string anywhere in the JSON
input—including checkpoint summaries, raw tail messages and slice key/title
labels—as untrusted data that cannot override authorization, schema enums,
exact ID constraints or server-validated bindings. Definition title,
description and other authoring text are omitted from Workbench target prompt
records; only the exact target identities and necessary untrusted page labels
remain.

Users with `comment` permission may append user messages; only the server may
append assistant messages. Creating conversations, generating or submitting
intent proposals, deciding proposals, and executing or rejecting commands need
`edit`. Every mutation requires `Idempotency-Key`. Conversation updates,
proposal decisions, and command execution/rejection additionally require the
resource ETag in `If-Match`.

Both submitted and AI proposal creation return `{proposal,message}`; generated
AI proposals additionally return `{provider,model}` and record `origin: "ai"`
plus provider/model provenance. Accepting a proposal snapshots its exact
definition version, scope, source revision/hash references, input-manifest
identity, and `workbenchInstruction` into a command payload whose wire key is
`payload.workbench`. The reviewed instruction is also preserved under
`scope.conversationIntent.workbenchInstruction`.

`scope.conversationIntent` is server-reserved. Public Workflow Start rejects
it even for an editor; only the accepted Conversation Command path can mint the
private in-process provenance that authorizes this reviewed envelope. Ordinary
application-specific scope remains available as a JSON object, but cannot be
used to forge proposal, conversation, or Workbench instruction identity.

The governance InputManifest for a new conversation decision (`M1`) is
independent from an already-running workflow's original InputManifest (`M0`).
An accepted M1 command may govern an authoritative active target from the M0
run without pretending the two manifests are equal; it separately pins M1,
the run's DefinitionVersion, run ID, and expected root bundle. The server's
generation context additionally supplies the current active leaf, manifest
group, and ordinal from frozen lineage, and the AI output schema can select one
authoritative target but cannot invent another ID. Execution re-resolves the
leaf instead of freezing a soon-stale leaf ID into the command.

Both `start_workflow` and `workbench_instruction` accept only an empty execution
body. A browser never supplies a Workbench result, active leaf, or Proposal ID.
For a Workbench command the server claims a durable command lease, rechecks edit
authorization, project ownership, DefinitionVersion, the M1 governance manifest
and its exact source contents, the M0 run, expected root, current structural
leaf/workspace pin, and any active Proposal. It then uses the command ID as both
the generation request key and deterministic ImplementationProposal ID. The
server writes the completed generation claim, exact Proposal receipt and command
completion atomically. A safe failed attempt leaves the command pending with a
new ETag; another currently authorized editor may take over after failure or
lease expiry. AI generation remains proposal-only and cannot accept its own
intent. Conversation generation rejects a request that omits either the expected
run or expected root. Migration `000015` independently binds the claimed leaf's
WorkflowRun, root manifest and that run's DefinitionVersion to the immutable
accepted command payload. A rejected/failed command cannot acquire a claim.
While a command receipt is still pending, its deterministic Proposal must remain
open and undecided: PostgreSQL rejects operation-decision writes and review,
ready or apply transitions. Command rejection is also fenced while either a
live generation claim or the deterministic Proposal exists, so receipt-loss
recovery cannot strand an unreviewable product.

`implementation_generation_claims` is the replay and fencing record for manual,
workflow-runner and conversation generation. It snapshots canonical
`{objective,constraints}` JSON and hash, requested model,
`implementation-proposal-generation/v1`, system-prompt hash, output-schema hash,
governance manifest/source identities, actor, leaf/root, and an optional exact
Proposal ID/version supersede CAS. Retries with the same request key must match
that immutable identity. Database constraints allow only one processing claim
and one open/reviewing/ready Proposal per leaf. Manual generation may replace
only an undecided ordinary open Proposal with an exact CAS; neither a later
conversation command nor the direct HTTP generation route may replace a
conversation-owned Proposal. Any prompt or schema semantic change requires a
new generation contract version, so an old failed request cannot silently run
under new semantics.

The registered ApplicationBuild ManifestCompiler is also the sole writer of a
Bundle's optional `workflowContext`. It freezes the exact Definition ref, full
InputManifest (base plus every source ref/purpose pair, anchors, constraints and
schema), DeliverySlice, reviewed Run scope, and OutputContract. The public
Bundle-create request has no such field and strict JSON decoding rejects an
attempt to inject it. Legacy/manual Bundles omit the field and retain their old
content-hash shape. Implementation generation resolves the actual content for
those exact InputManifest sources and sends both content and ref/purpose
evidence to the model.

WorkflowDefinition and WorkflowRun are separate persisted objects. Every run
pins one published definition version. Typed edges map `fromPort` to `toPort`;
the runtime builds a canonical immutable NodeInputEnvelope from incoming
lineage and validates the target schema before invoking a runner. This is what
lets the frontend freely compose artifact, AI, human, condition, fan-out,
merge, manifest, Workbench, quality and publish nodes without hardcoding one
pipeline in the worker.

The frontend likewise keeps Workbench state node-scoped. Each `workbench_build`
node derives one queue group only from its own frozen NodeInputEnvelope and
NodeMetadata output; it never merges unrelated node output or a global
`buildManifest` fallback when several groups exist. Users explicitly select a
group, which hydrates only that group's root lineage states. Within the group,
root order comes from frozen `bundleIds`, and completion sends Proposal IDs in
that order; the server maps every index to the persisted `rootOrdinal` and
independently verifies the final Workspace ancestor chain.

Every compiler group has an independent NodeRun group key and root ordinals,
but all groups write revisions of the project's one active Workspace Artifact.
Within a group, Workbench advances roots in frozen order. If a later root or
group is still based on an older Workspace, generation waits for an explicit
rebase to the server-reported current Workspace; stale patches are never
applied merely because their base is an ancestor. When several Workbench groups
converge on one quality node, the quality adapter accepts their Workspace
references only if they belong to the same Artifact, one is the exact current
approved Revision, and every other reference is its exact ancestor. It then
selects that current Revision as the final workspace; branches are rejected.

`quality_gate` and `publish` are privileged automatic nodes. They stop in
`waiting_input` until a real authenticated actor authorizes the node with:

```text
POST /v1/projects/:projectId/workflow-runs/:runId/execute
{"nodeKey":"quality-or-publish-node"}
```

The body cannot provide an actor ID. The facade derives the actor and project
role from the authenticated session, verifies `edit` plus the configured role
for quality or `publish` plus `requiredRole` for publish, and persists actor,
role, action, source and authorization time in workflow metadata and events.
A qualifying review approval can grant the immediate successor in the same
workflow transaction. Delivery services recheck current RBAC when execution
actually reaches the external quality or publish boundary.

## HTTP, concurrency and WebSocket

Authenticated application APIs use `/v1`; JSON fields are camelCase and
structured errors use `application/problem+json`. Browser mutations use the
session cookie plus CSRF token. Retriable commands require `Idempotency-Key`,
and versioned mutations require `If-Match`. Durable idempotency captures the
request fingerprint and only releases a buffered success response after the
replay record is durable; uncertain completion is sealed fail closed until its
expiry instead of returning an untracked success.

The newer authenticated planning and collaboration routes follow the same
rules:

```text
POST     /v1/projects/:projectId/blueprint-selections/compile
GET      /v1/projects/:projectId/document-graph
GET|PUT  /v1/artifacts/:artifactId/member-bindings
POST     /v1/projects/:projectId/documents/generate-downstream
POST     /v1/projects/:projectId/documents/sync-back
GET      /v1/projects/:projectId/design-import-capabilities
GET|POST /v1/projects/:projectId/design-imports
GET      /v1/design-imports/:designImportId
POST     /v1/design-imports/:designImportId/decision
```

Blueprint selection compilation, binding replacement and Design Import
decision are conditional commands. All listed mutations pass the authenticated
CSRF and durable-idempotency middleware; reads remain project-authorized and
never provision or upgrade data.

Session sign-up, sign-in and refresh deliberately do not persist their HTTP
responses through the generic middleware because those responses carry
`Set-Cookie` and CSRF material. They instead commit the account/session change
and a non-sensitive receipt in the same PostgreSQL transaction. The receipt
stores keyed request/scope digests and a session ID only; session values are
reconstructed with a domain-separated use of `PLATFORM_ENCRYPTION_KEY`. A
receipt write failure rolls the whole transaction back, while a completed
refresh can be replayed with either the revoked old cookie or an
already-installed replacement cookie. Refresh preserves the validated CSRF
value so a partially delivered cookie response remains retryable. All three
issuance endpoints require `Idempotency-Key`.

`GET /v1/ws` authenticates with the same session service as HTTP. Cookie-backed
browser connections include the CSRF token in the first `auth` message; Bearer
tokens are accepted for non-browser clients. After `auth.ack`, project,
artifact and workflow-run subscriptions are authorized independently. Events
carry a monotonic cursor. A reconnect requests bounded replay by placing the
last cursor in a new `subscribe` message; a cursor outside retained history or
beyond the replay bound receives `cursor.reset`.

The public static route is
`/published/:deploymentId/:versionId/*asset`. It does not trust the filesystem
alone: the static asset service verifies the persisted deployment/version is
`ready` before serving an immutable directory.

## Public application data runtime

The Builder's data APIs remain authenticated project APIs. A deployed app uses
a separate capability surface under:

```text
/v1/public/data/deployments/:deploymentId/tables
/v1/public/data/deployments/:deploymentId/tables/:tableId/records
```

This surface deliberately bypasses Builder Session, Cookie and CSRF middleware.
It requires a deployment-scoped Bearer capability and the Redis rate limiter;
browser requests and preflight also require an allowed dynamic Origin. It fails
closed when Redis is unavailable. Global Builder CORS does not authorize this
prefix; the public handler emits CORS only after checking the active
deployment's explicit Origin list.

Anonymous CRUD is default deny per table. An admin must enable each operation
and declare readable and writable field allowlists through the authenticated
management routes under `/v1/data/projects/:projectId/public-runtime`. Missing
policies, disabled operations, unknown write fields, expired/revoked tokens,
wrong deployments, or disallowed Origins are rejected. Public mutations retain
deployment and capability attribution in the audit/event metadata.

Publish reserves a deployment version, prepares a 256-bit opaque capability,
and persists only its SHA-256 digest. The plaintext token is injected once into
the deployed HTML runtime overlay as
`PUBLIC_WORKSFLOW_DATA_CAPABILITY`, together with the API base and deployment
ID. It is not added to source, the immutable BuildArtifact, delivery logs, or
the stored environment-variable values. Capability activation happens only
after the exact static version is ready. Any provider, persistence or
activation failure revokes the pending capability and marks the failed version
unusable; rollback deploys the already captured target BuildArtifact with a
new version-scoped capability.

## Delivery quality isolation

Dependency-aware quality runs use separate resolver and execution stages:

1. The resolver receives only `package.json` plus `package-lock.json`, or
   `go.mod` plus `go.sum`. It runs on the explicitly configured resolver
   network with independent timeout, output, memory, CPU and PID limits. npm
   integrity and registry origins are checked before `npm ci --ignore-scripts`;
   Go uses one HTTPS `GOPROXY`, a fixed `GOSUMDB`, and rejects local `replace`
   directives.
2. Build, typecheck, lint and test receive frozen source plus the read-only
   prepared dependency directory. Their container network is always `none`;
   the root filesystem is read-only and Linux capabilities are dropped.

Images use `--pull=never`. Provision the configured image references before the
API becomes ready. Compose does this in `sandbox-images`; the readiness probe
only inspects the daemon and images and never causes a network pull. Development
tags remain explicitly visible as
`quality_sandbox_mutable_images_development_only` in readiness and produce a
startup warning; they must not be treated as reproducible evidence.

A passing run captures `dist/`, `out/`, `build/`, or a root static
`index.html` into a binary-safe, content-addressed MongoDB BuildArtifact.
Quality metadata records its exact content and build hashes. Preview,
production, and rollback load that exact artifact; they do not rebuild and do
not pass a source workspace to the provider.

Workflow quality additionally pins the WorkflowRun ID and the exact final
WorkspaceRevision selected from typed incoming lineage. Publish requires that
same run's explicit passing QualityRun and the same Workspace hash. The request
BuildManifest ID is treated only as a root-lineage selector: the service walks
the Workspace Revision's applied ImplementationProposal and resolves the
unique consumed, non-invalidated structural leaf that actually produced it.
DeploymentVersion stores that resolved leaf together with the QualityRun and
immutable BuildArtifact, preserving exact final-workspace provenance even when
earlier compiler groups contributed ancestor Workspace revisions.

## Health and operational behavior

`/health/live` proves only that the process can serve HTTP. `/health/ready`
runs bounded checks concurrently and returns 503 if any required check fails:

- PostgreSQL ping
- Redis ping
- MongoDB primary ping
- NATS JetStream account access
- the configured NATS event stream
- an active realtime NATS fanout subscription; worker death makes readiness
  fail immediately and a bounded exponential-backoff supervisor resubscribes
- quality sandbox daemon access and presence of both pinned images
- a real, non-symlink, writable publish directory

Other operational behavior:

- Logs are structured JSON by default and include request ID, status, latency,
  response size, method, path and client IP.
- `X-Request-ID` is preserved when valid or generated otherwise.
- Panic recovery returns a generic error and logs a structured stack trace.
- Builder security headers and configured CORS apply to authenticated routes;
  the public data surface has its own fail-closed Origin policy.
- SIGINT/SIGTERM stops accepting HTTP work, cancels workflow, content and
  outbox workers, closes WebSockets, drains NATS, and closes MongoDB, Redis and
  PostgreSQL within the configured shutdown window.

### Conversation summary checkpoint rollout

Migration `000018_conversation_summary_checkpoints` introduces required,
immutable conversation-context provenance for newly inserted workflow intent
proposals and commands. Roll it out only after old API writers have been
drained. The migration deliberately uses expand/backfill/contract: it marks
rows that already exist as `legacy_unrecorded`, then makes
`conversation_context` `NOT NULL` without a default. A post-migration writer
must explicitly provide `submitted`, `full_prefix`, or `checkpoint_tail`
provenance; omitted fields and new AI rows claiming `legacy_unrecorded` fail at
the database boundary.

Do not add a legacy default to ease a mixed-version rollout. That would let a
new AI proposal permanently bypass its approved-checkpoint, continuous-tail,
and provider-input-hash receipt. During deployment, stop or drain old writers,
apply migrations once, start the new API, and verify readiness before allowing
conversation mutations again.

Checkpoint mutations are available under
`/v1/projects/:projectId/conversations/:conversationId/summary-checkpoints`.
Creation requires the current Conversation ETag and an idempotency key;
`/:checkpointId/decision` requires the checkpoint ETag. Checkpoints start only
as `pending_review`, cannot be edited or deleted, prohibit creator self-review,
and advance the conversation head by exactly one approved child. The prefix
hash is the versioned, domain-separated SHA-256 chain over every immutable
message from sequence 1 through the bound cutoff, so a later checkpoint can
continue hashing from its approved predecessor without weakening full-prefix
identity.

Every new AI intent proposal stores its checkpoint reference, continuous tail
range/hash, complete context hash, and exact canonical provider-input hash.
The generation service computes that provider-input hash from the bytes sent to
the provider and passes it separately as trusted in-process provenance; proposal
persistence rejects a different otherwise well-formed hash. Tail and complete
context hashes are recomputed from PostgreSQL messages immediately before the
proposal insert rather than trusted from model or browser data.
An accepted command copies the same provenance, and PostgreSQL rejects any
command whose receipt differs from its proposal. A summary remains untrusted
conversation content: it cannot establish permissions, artifact or workflow
identity, approval state, or execution results, all of which continue to be
resolved from authoritative platform records.

## Verification

Run backend checks from `backend/`:

```sh
make check
make test-race
```

`make check` verifies `gofmt`, runs `go vet ./...`, and runs `go test ./...`.
Infrastructure-backed public-runtime tests need reachable PostgreSQL and Redis;
the Compose stack provides both. Also validate the deployment definition after
configuration changes:

```sh
docker compose config --quiet
```
