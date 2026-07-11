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

Compose starts PostgreSQL, Redis, MongoDB, NATS with JetStream, a dedicated
Docker-in-Docker quality sandbox, its configured Node and Go images, and the API.
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
environment file, load it in the shell, then run:

```sh
cd backend
make run
```

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
- `STARTUP_MIGRATE`, `STARTUP_ENSURE_MONGO_INDEXES`, and
  `STARTUP_ENSURE_NATS_STREAM` default on in development/test and off in
  production. Production must provision the same resources separately or opt
  in explicitly.

## Startup and persistence lifecycle

The process establishes PostgreSQL, Redis, MongoDB and NATS connections first,
then applies the enabled startup provisioning under `STARTUP_TIMEOUT`. It
constructs the domain services only after provisioning succeeds. The NATS
event stream must already exist when automatic stream creation is disabled.

PostgreSQL migrations are embedded in the API binary. The migrator holds a
PostgreSQL advisory lock, applies each version in its own transaction, and
records its SHA-256 checksum in `schema_migrations`. Changing an already
applied migration is a startup error; add a new migration instead.

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
