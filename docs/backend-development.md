# Backend development

The Go backend lives in `backend/` and uses module
`github.com/worksflow/builder/backend`. It is the platform system of record,
not only an infrastructure shell. Gin exposes HTTP and WebSocket transports;
GORM/PostgreSQL owns transactional metadata and workflow state; MongoDB owns
large immutable payloads; Redis owns bounded ephemeral state and public API
rate limits; NATS JetStream carries outbox-backed realtime events.

The application wires authentication and project RBAC, artifacts and
revisions, reviews and comments, immutable AI manifests and proposals, the
versioned typed workflow runtime, Workbench generation, the data runtime,
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

Application generation uses the same boundary at a larger scale. A frozen
ApplicationBuildManifest pins all requirement, blueprint, PageSpec, prototype
and workspace inputs. Workbench returns an ImplementationProposal; applying it
creates a new immutable WorkspaceRevision rather than mutating the manifest or
silently overwriting current files.

### Governed conversation control plane

The Workbench conversation panel is a command control plane, not a second
workflow executor. Its implemented route family is:

```text
GET|POST  /v1/projects/:projectId/conversations
GET|PATCH /v1/projects/:projectId/conversations/:conversationId
GET|POST  /v1/projects/:projectId/conversations/:conversationId/messages
GET|POST  /v1/projects/:projectId/conversations/:conversationId/intent-proposals
POST      /v1/projects/:projectId/conversations/:conversationId/intent-proposals/generate
GET       /v1/projects/:projectId/conversations/:conversationId/intent-proposals/:proposalId
POST      /v1/projects/:projectId/conversations/:conversationId/intent-proposals/:proposalId/decision
GET       /v1/projects/:projectId/conversations/:conversationId/commands[/:commandId]
POST      /v1/projects/:projectId/conversations/:conversationId/commands/:commandId/execute
POST      /v1/projects/:projectId/conversations/:conversationId/commands/:commandId/reject
```

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

`start_workflow` accepts an empty execution body and creates a run whose ID is
the command ID. `workbench_instruction` requires
`workbenchInstruction.expectedRunId` and an execution body containing the exact
`workbenchResult.runId` and `bundleId`. Before accepting that result, the server
rechecks project ownership, definition version, manifest ID/hash, frozen bundle
status, and bundle-to-run linkage. AI generation remains a proposal-only step;
it cannot accept or execute its own intent.

WorkflowDefinition and WorkflowRun are separate persisted objects. Every run
pins one published definition version. Typed edges map `fromPort` to `toPort`;
the runtime builds a canonical immutable NodeInputEnvelope from incoming
lineage and validates the target schema before invoking a runner. This is what
lets the frontend freely compose artifact, AI, human, condition, fan-out,
merge, manifest, Workbench, quality and publish nodes without hardcoding one
pipeline in the worker.

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
