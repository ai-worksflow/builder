# Worksflow Builder Prototype

Worksflow Builder combines a Next.js product workbench with a Go control plane. Alongside the original planning, document, Blueprint, Prototype and Review surfaces, the repository now contains the first implementation slice of a governed project-level constructor: exact BuildContract/TemplateRelease binding, durable Repository Candidates, an isolated development Sandbox and a browser IDE.

The main product specification is in `docs/worksflow-generation-workbench-prototype-spec.md`.
The target architecture for project-level AI coding, the interactive user
sandbox, full-stack verification, and deployment is in
`docs/ai-constructor-architecture.md`.
The exact admission findings for the proposed upstream framework repository are
recorded in `docs/template-admission-audit.md`.
The implementable Stage 3 quality, receipt, and Candidate freeze-gate contract
is in `docs/full-stack-quality-profile.md`.

## Current Scope

- Workbench flow: Planning -> Plan Ready -> Building -> Complete.
- Preview, Code, and Database workspace modes.
- Project title menu, more menu, linked document picker, publish/share/connect demo panels.
- Team Collaboration dashboard with project switching.
- Document Graph with draggable nodes, relation filters, free binding, member binding, and Workbench context handoff.
- Document Editor with status changes, members, comments, history, downstream generation, and Workbench use.
- Blueprint Editor with module library, draggable nodes, graph edges, validation, generated docs, and Workbench context creation.
- Prototype Studio with wireframe/design/component/handoff modes, layer editing, states, fixtures, and export-style actions.
- Design Import Center and Review Center mock flows.
- Chinese and English UI copy through the local i18n provider.
- Authenticated Repository Candidate bootstrap from an exact WorkspaceRevision or admitted TemplateRelease source tree.
- Browser IDE backed by server-side Candidate autosave, Monaco diff/diagnostics, xterm PTY, supervised Template commands and isolated HTTP/HMR Preview grants.
- Authenticated exact-head Candidate literal search with generation/root fencing, a durable project-scoped index, runtime project/actor admission, bounded query-shape scanning, binary skipping, strict response validation, stale-result rejection and cancellable browser navigation.
- Production LSP v1 control-plane and Monaco integration through the repository-internal LSP-0–3 implementation baseline; external Golden-server qualification remains separate and incomplete.
- Immutable successor Candidate rebases with deterministic three-way planning, explicit per-file conflict decisions and recoverable checkpoints.
- Governed Agent Attempts with immutable evidence, explicit three-way Merge/Undo, and server-restorable history.
- Atomic Candidate freeze into an exact Implementation Proposal, read-only frozen workspace UX, and full-accept CAS Apply into an immutable WorkspaceRevision.
- Governed Release Controller v3 handoff with durable Operation/Attempt/Result reconciliation, exact-Bundle Preview single-flight, append-only operator reconciliation Cases, exact v2 Preview/Production/Deployment evidence, and fail-closed UI states for unknown controller outcomes.
- Hash-closed Reference AI Conversation Contract bundle with API/Data/AI Runtime v2, project-scoped composite foreign keys, matching nine-branch API/standalone RunEvent schemas, four persistent entities, eleven authenticated/idempotent API operations, six UI states with three-breakpoint presentation evidence, and fifteen one-to-one executable blocking oracles; this is a deterministic internal compiler fixture, not a deployed AI application.

## Mock Boundaries

Some legacy product surfaces remain high-fidelity mock flows. The following capabilities still use local mock state or are not yet connected end to end to the governed constructor services:

- The legacy animated AI plan/build demo. The governed Agent Attempt, Merge and Undo path is server-backed, but remains disabled until an admitted Runner, exact policy hashes and a real model Provider are configured.
- The legacy Supabase connection action. The authenticated platform data plane and database contracts are separate server-backed capabilities.
- Legacy GitHub and publish/share/transfer/export/analytics/knowledge/connector demo actions. The authenticated GitHub and governed Release services are separate; Release mutation remains disabled unless an exact, qualified Controller configuration is enabled.
- Figma, Penpot, Excalidraw, tldraw, Storybook, and upload integrations.
- Legacy notification and presentation-only review widgets. Canonical Review, project permissions and immutable audit used by the constructor are server-backed.
- Legacy demo catalog persistence outside the authenticated platform flows.

The authenticated platform Code/Preview path no longer uses generated textarea files, simulated terminal lines or `srcDoc`; it fails closed when an exact BuildManifest, approved template policy, Candidate or Sandbox cannot be obtained. Candidate freeze/review/apply has a real PostgreSQL service-level closure, but the audited upstream `ai-worksflow/templates` repository is still blocked from Golden TemplateRelease approval and there is no Golden-stack Playwright closure, so a full real-template browser or stage-exit closure must not yet be claimed.

Candidate search requires project view authorization and the selected Candidate's exact generation and root hash. Migration `000062` stores immutable exact-tree manifests, members and content-hash-deduplicated text blobs. Migration `000063` adds a durable expiring single-builder claim before any authoritative FileBlob resolution; `000064` atomically enforces the default per-project quota of 16 ready/reserved trees, 256 MiB of logical source and two active builds; and `000065` replaces global body indexes with project UUID + trigram composite GIN indexes. The index selects candidates only: every result is still checked against the opening tree, reread from Repository authority, content-hash validated and followed by a closing Candidate-head check.

Short/no-trigram and glob requests retain the bounded `2,000 files / 8 MiB / 500 matches` scanner, but quota, index corruption or service failures do not fall back to that path. The runtime now uses one Redis-backed admission authority for both Candidate search and secure exact-tree construction. Every request is normalized, project-view authorized, and charged against query admission before any Candidate Repository I/O, including indexed, short/no-trigram, and glob shapes. Query defaults are project `20/s, burst 40` plus actor `4/s, burst 8`; secure `BuildForActor` charges first-builder defaults of project `1/15s, burst 2` plus actor `1/30s, burst 1` only after the durable PostgreSQL claim identifies that caller as the actual owner and before FileBlob resolution. Ready reuse, waiters, and concurrent followers do not pay a first-builder token. Malformed admission results and Redis timeout, outage, or corrupt state fail closed.

Valid query or first-builder admission denial returns `429` with `Retry-After`; active-build quota denial also returns `429`, retained tree/source-byte quota denial returns `409`, and an otherwise unknown index failure returns `503`. The frontend accepts only a bounded 1–3600 second `Retry-After`, retries once per exact query/head identity, and cancels that timer when the query, head, or component changes. Only `repository_search_head_changed` refreshes the Candidate; quota `409` and infrastructure `503` preserve the Blueprint and dirty editor unchanged. The focused Repository admission/index regression passed against real PostgreSQL and Redis in 95.205s; focused race/`go vet`/unit checks and the full frontend typecheck/unit/lint/production build are green. This is internal implementation evidence only.

Exact-tree GC/retention migration `000066` is now `implemented-internal`. It ranks every project's ready publications, retains at least the newest eight and at least seven days of history, protects a current tree referenced by a Candidate in any status and protects every live build claim. Deletion is bounded by an expiring, full-publication capability and repeats the manifest commitment under exact CAS plus tree/project advisory locks. The executor locks an existing build-claim row before taking a lock-time timestamp, so a renewal that commits first is protected and a renewal that loses the row lock cannot report success after deletion. Shared blobs survive while any remaining tree references them; append-only runs, capabilities, receipts and tombstones preserve the decision and allow a deleted tree to be rebuilt as a distinct publication. The two short-lived authorization tables bind the exact transaction ID, backend PID, tenant, tree, capability and blob, and are cleared in the same transaction.

The runtime defaults are 30-day retention, eight retained trees per project, a batch of 25 and a 10-minute capability TTL. Hard boundaries are retention at least seven days, keep at least eight, batch at most 100 and capability TTL at most 15 minutes. `repository-index-gc` is a separate one-shot binary and Compose `maintenance` profile, not an API worker. Its required canonical `-run-id` is the durable scheduler identity: after an ambiguous result or process crash, rerun the exact command with the same run ID and unchanged policy. A different policy under the same ID is rejected.

Migration `000066` establishes three isolated `NOLOGIN` group roles—`worksflow_migration_owner`, `worksflow_application` and `worksflow_repository_index_gc_operator`—plus three distinct real `LOGIN` credentials and DSNs. Before its first schema, DDL, ownership or default-ACL mutation, migration `000066` rejects a partial or elevated stable-role set, stable roles that are themselves members of another role, any membership into a stable role with `ADMIN OPTION`, and every explicit trusted-schema column ACL. An all-absent role set is accepted only for isolated local development; production requires the exact safe trio and must explicitly remove any pre-existing column grants through a reviewed provisioning change. The migration then removes `PUBLIC` access from every trusted-schema routine, table and sequence, explicitly preserves predecessor `SECURITY INVOKER` execution for the application group, and limits its executable `SECURITY DEFINER` set to exactly ten mutation functions. The operator can execute exactly four GC functions; the API cannot read GC-private tables or execute GC functions, and the operator has no direct object privileges. The trusted schema, all of its tables/sequences and all 23 controlled routines are assigned to the exact migration-owner role. Fourteen exposed definers have a fixed trusted-schema search path; the Sandbox checkpoint dependency is a separately checked SQL/STABLE `SECURITY INVOKER` helper executable only by the owner and application, while the eight internal trigger/guard routines are owner-only. API and operator startup inspect every session-reachable role, including `NOINHERIT` memberships that remain reachable through `SET ROLE`, and reject role-delegation authority, explicit column ACLs, extra ownership, DDL, relation, sequence or routine authority. The three group roles and dedicated schema must exist before applying `000066` in production; real LOGIN memberships and secrets must exist before the corresponding process starts. The migration's grants are deliberately conditional and an already-recorded migration is never rerun. If the group-role prerequisite was missed, create a reviewed follow-up migration; do not edit `000066` or delete its `schema_migrations` row. Migration `000068` adds the fourth isolated `worksflow_golden_fault_operator` group and dedicated LOGIN/DSN; provision that role before `000068`, which grants only schema `USAGE` and non-grantable `SELECT, INSERT` on its two append-only fault-ledger tables. The API cannot inherit that role or access those tables. Its strict one-shot authority/consume ledger is now closed into the Golden Fixture artifact index and immutable Qualification verifier, while real fault adapters and externally produced authority, consume-receipt, and attestation artifacts remain absent.

The migration ledger keeps the historical up checksum unchanged and now records a separate SHA-256 for every canonical down file. A legacy database must run the migrator once before the API: only an exact ordered prefix with unchanged up checksums may establish missing down checksums, after which missing, orphaned or changed rollback files fail closed. `VerifyCurrent` is read-only and never performs this baseline. Migration `000066` rollback takes an exclusive fence over all GC control/audit tables and refuses to discard any run, capability, receipt, tombstone or transient authorization fact.

API, migrator and GC operator DSNs and schemas are separate. Production operators must externally provision the remaining application DML, database/schema ownership and runtime secret injection; the shared local development owner in Compose is only a convenience and is not role-separation qualification. A maintenance invocation supplies no credential defaults:

```sh
# Inject the operator DSN from the deployment secret manager; do not commit it.
export WORKSFLOW_REPOSITORY_INDEX_GC_POSTGRES_DSN
export WORKSFLOW_REPOSITORY_INDEX_GC_POSTGRES_SCHEMA=worksflow
export WORKSFLOW_REPOSITORY_INDEX_GC_RUN_ID=8f0f3d35-4c4d-4b2f-b91d-e5d4d0f45847
docker compose --profile maintenance run --rm repository-index-gc

# Recovery after an ambiguous result: reuse all three values and the same policy.
docker compose --profile maintenance run --rm repository-index-gc
```

Focused real-PostgreSQL canaries cover the migration contract, staging/production API posture, and a real low-privilege operator `LOGIN` including interrupted same-run recovery. These are repository-internal canaries, not proof that a production role/secret deployment was provisioned. Production LSP v1 LSP-0–3 likewise has a repository-internal implementation and automated evidence, but LSP-4/QA-016 still lacks an approved Golden TemplateRelease, real language-server ingress and external browser qualification. Neither capability is externally qualified by the in-repository tests.

The browser validates the complete search DTO and request identity, uses a 350ms debounce, cancels superseded requests, rejects stale results, and on a head-change `409` refreshes only the Candidate/session tree projection. It never overwrites a dirty editor or reloads Blueprint, PageSpec, Prototype, or other governed document state. A bounded `Retry-After` countdown and one automatic retry per exact query/head identity are implemented; changing the query/head or unmounting cancels the timer.

Release delivery migrations `000056`–`000061` close the in-repository duplicate-mutation and authority-projection gaps. They persist one stable v3 Operation before network access, reconcile an unknown `PUT` with `GET`, enforce one nonterminal Preview per exact ReleaseBundle, provide an immutable administrator Case for an exact blocked-version/error resume, serialize the legacy `/deployments` writer with v3 admission, recompute embedded release hashes in PostgreSQL, and require every v2 Run to commit with exactly one same-project, correctly typed Operation. Once any Case exists for an Operation, reconciliation is permanently GET-only; a second Case is possible only after that same Run reaches a new `reconcile_blocked` version. The legacy writer is Preview-only and cannot race an active or blocked v3 Run.

Delivery claim order alternates Production/Preview priority per worker store, while `SKIP LOCKED` and fencing retain cross-replica exclusivity. Claim/renew lease deadlines and Case audit timestamps use PostgreSQL time rather than application-node clocks.

Targeted internal Release/migration tests, race checks, `go vet`, and real PostgreSQL canaries for migrations `000057`–`000061` pass. The final full migration suite, including the `000066` role/helper boundary, raw-chain compatibility and project-scoped GIN planner canaries, also passes against real PostgreSQL (`go test ./migrations -count=1`, 448.286s). This repository still does not ship qualification evidence from a deployed real Controller, Registry/KMS, target cluster, or approved Golden TemplateRelease. Internal control-plane evidence must not be described as an externally approved Preview/Production deployment.

The final full backend regression against real PostgreSQL and Redis also passes: `go test ./... -count=1 -timeout 30m` exited 0. Key package times under cross-package migration-lock contention were `core 896.686s`, `release 437.353s`, `repository 614.687s`, `sandbox 458.522s`, and `migrations 846.769s`. Full `go vet`, all-package compile, focused race checks, the production/maintenance Compose render and the final non-root backend image build also pass. This is in-repository regression evidence only; it does not qualify the missing external Golden Stack or Release Controller.

The ordinary Playwright suite was rerun on 2026-07-18: 87 passed, 0 skipped,
0 failed in 3.8 minutes. Golden is deliberately not selected by this suite;
the separate strict qualification remains unexecuted and does not close a
Golden or stage-exit boundary.

## Project Structure

```text
frontend/
  app/                         Next.js App Router entry
  components/worksflow/        Product prototype UI
  components/worksflow/team/   Team collaboration surfaces
  components/worksflow/workbench/
  lib/i18n/                    Locale config, provider, messages
  lib/worksflow/               Types, mock data, project model, store
  tests/                       Model and flow tests
backend/
  cmd/                         API, migration and operator binaries
  internal/                    Constructor, repository, sandbox, Agent,
                               verification and release control planes
  internal/contracts/reference/ Embedded Reference AI Conversation bundle
  migrations/                  Ordered PostgreSQL authority contracts
agent-runner/                  Ephemeral Codex Agent runtime
sandbox-runner/                Non-root IDE/PTY/Preview runtime
deploy/                        Nginx and local runtime preparation/preflight
docs/
  ai-constructor-architecture.md
  full-stack-quality-profile.md
  template-admission-audit.md
  template-artifact-authority-operator.md
  worksflow-generation-workbench-prototype-spec.md
docker-compose.yml             Local reference topology
Makefile                       Build, contract and local deployment entrypoints
```

## Development

Run commands from `frontend/`.

```sh
pnpm install
pnpm dev
pnpm lint
pnpm typecheck
pnpm test:mock
pnpm test:e2e
pnpm build
```

`pnpm test` runs both mock model tests and Playwright interaction tests.

## Local reference stack

The production frontend, Go API, PostgreSQL, Redis, MongoDB, NATS JetStream,
and isolated local Docker daemon are defined in the root Compose file. Start
the reference stack from the repository root:

```sh
make deploy
```

`make deploy` validates the Compose configuration, rebuilds the frontend and
API images, starts the stack in the background, waits for its health checks,
and prints the resulting service status. Use `make deploy-fresh` when a
no-cache rebuild is required. `make status`, `make logs`, and `make down`
provide the common operational commands; `make down` preserves persisted data.

Open `http://localhost:10000`. Nginx is the only public entry point: `/` maps to
the Next.js frontend, `/api/platform/*` maps to the Go API with that prefix
removed, and `/health/*` plus `/published/*` map directly to the Go API.
Stop the stack without deleting persisted data with `make down`.

This is a local integration topology, not a Stage 1–4 qualification result.
Sandbox, LSP, Agent and Release mutation capabilities remain fail closed until
their exact runtime inputs are enabled. The first-party frontend/backend base
images are digest-pinned and their Dockerfiles reject mutable overrides before
dependency installation. The PostgreSQL, Redis, MongoDB, NATS, Nginx and DinD
service tags in this local Compose file are still development conveniences;
production deployment must replace them with admitted immutable images and
environment-specific network, secret and identity controls.

For a deployment reached through a hostname other than `localhost`, provide
the browser-visible endpoints before building the frontend:

```sh
NGINX_PORT=10000 \
PLATFORM_ALLOWED_ORIGINS=https://app.example.com \
DELIVERY_PUBLISH_BASE_URL=https://app.example.com/published \
make deploy
```

Set `OPENAI_API_KEY` to enable AI-backed routes. Custom gateways may set
`OPENAI_BASE_URL` and `OPENAI_DEFAULT_MODEL`; the base URL is expanded to
`/v1/responses`, while `OPENAI_RESPONSES_URL` remains an explicit override.
When the key is unset, AI requests fail closed as not configured.
`OPENAI_API_KEY` only supplies the server-side Provider credential; it does not
by itself enable the governed Agent or replace `AGENT_ENABLED`, worker identity,
an approved Runner digest and the five reviewed policy hashes.

### Opt-in Sandbox and Codex Agent

Runner images have no mutable defaults. Review exact base-image digests, an
exact Codex CLI version, and the npm package SRI before building. This command
shows the locally verified 2026-07-18 input set; it is an audit snapshot, not a
promise that these are the forever-current or organization-approved inputs:

```sh
RUNNER_GO_IMAGE='golang@sha256:1a478681b671001b7f029f94b5016aed984a23ad99c707f6a0ab6563860ae2f3' \
RUNNER_NODE_IMAGE='node@sha256:16e22a550f3863206a3f701448c45f7912c6896a62de43add43bb9c86130c3e2' \
CODEX_VERSION='0.144.6' \
CODEX_INTEGRITY='sha512-wk+2CWiBNXiJLBoN2D08N9RceWkSBnlgk5g2K1a4CXrP/C0gdlHyRUG7RFzm9y41DCK/7tvCct233JVxyFmznw==' \
AGENT_RUNNER_TAG='registry.example/worksflow/agent-runner:qualification-0.144.6' \
SANDBOX_RUNNER_TAG='registry.example/worksflow/sandbox-runner:qualification-0.144.6' \
make runtime-images
```

The build verifies the exact package tarball SRI, disables npm lifecycle
scripts, asserts the installed `codex --version`, and runs as a non-root user.
Push, scan, attest and admit both resulting images, then configure their
registry `image@sha256:...` references; a local tag is never an admissible
runtime identity. Enabling the full path also requires five reviewed policy
hashes and a real Provider secret:

```sh
SANDBOX_ENABLED=true \
SANDBOX_RUNNER_IMAGE='registry.example/worksflow/sandbox-runner@sha256:<64-hex>' \
AGENT_ENABLED=true \
AGENT_WORKER_ENABLED=true \
AGENT_WORKER_ID='agent-worker-1' \
AGENT_RUNNER_IMAGE='registry.example/worksflow/agent-runner@sha256:<64-hex>' \
AGENT_MODEL_POLICY_HASH='sha256:<64-hex>' \
AGENT_PARAMETERS_HASH='sha256:<64-hex>' \
AGENT_PROMPT_HASH='sha256:<64-hex>' \
AGENT_OUTPUT_SCHEMA_HASH='sha256:<64-hex>' \
AGENT_TOOLCHAIN_HASH='sha256:<64-hex>' \
OPENAI_API_KEY='<secret>' \
make deploy
```

The current executor identity is deliberately limited to `codex-cli` plus
`openai`; alternative evidence labels fail startup. Compose shares the exact
Agent worktree path between API and DinD, creates a dedicated internal Runner
network, and uses two path-confined relays so the isolated Runner can call only
the Responses gateway route. The Provider credential remains in the API and is
not mounted into the Runner. These topology contracts and local DinD canaries
do not replace a live model, approved TemplateRelease, admitted image, browser
Golden run or target-environment security qualification.

### Opt-in Release Controller v3

Release delivery is disabled by default. Enable it only after deploying and
qualifying an HTTPS Controller that implements `worksflow.release-delivery/v3`:

```sh
RELEASE_DELIVERY_WORKER_ENABLED=true \
RELEASE_DELIVERY_WORKER_ID=release-worker-1 \
RELEASE_DELIVERY_CONTROLLER_URL=https://release-controller.example.com \
RELEASE_DELIVERY_CONTROLLER_TOKEN='<secret-at-least-32-characters>' \
RELEASE_DELIVERY_CONTROLLER_ID=production-release-controller \
RELEASE_DELIVERY_CONTROLLER_VERSION='<reviewed-version>' \
RELEASE_DELIVERY_CONTROLLER_PROTOCOL=worksflow.release-delivery/v3 \
RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST='sha256:<leaf-spki-sha256>' \
make deploy
```

Startup validation also bounds `RELEASE_DELIVERY_LEASE_DURATION`,
`RELEASE_DELIVERY_POLL_INTERVAL`, `RELEASE_DELIVERY_RECONCILE_DELAY`,
`RELEASE_DELIVERY_REQUEST_TIMEOUT`, and
`RELEASE_DELIVERY_RESPONSE_MAX_BYTES`. The API requires normal TLS PKI plus the
configured leaf-certificate SPKI pin before it sends the bearer token or a
mutation request, and readiness requires the exact controller ID, version,
protocol, trust digest, Preview single-flight index, operator Case schema,
legacy/v3 cross-writer gate, nested-authority trigger/function, exact migration
`000061`, deferred Run→Operation guards, and an orphan-authority scan. Do not
copy the placeholder token or digest above into an environment, and do not
enable the worker merely to make the UI available. With the worker disabled,
Preview, approval, promotion, rollback, and operator resume remain fail closed;
immutable Bundle/Run/Receipt/Result and reconciliation Case history remains
available through the read API for audit.
Controller verification failure on a worker-enabled process fails startup; it
does not silently downgrade that process to read-only. Use a worker-disabled or
separate read/API deployment when maintenance-mode audit access is required.
The legacy `/deployments` compatibility path is Preview-only and is not a
production fallback.
`make compose-check` verifies both the disabled default and exact passthrough of
all 13 `RELEASE_DELIVERY_*` inputs to the API. That proves local configuration
wiring only; it does not prove a Controller is deployed or qualified.

## Manual Demo Path

1. Open the Workbench.
2. Wait for Planning to advance to Plan Ready.
3. Click `Implement this plan`.
4. Watch the checklist advance to Complete.
5. Switch between Preview, Code, and Database.
6. Open Linked docs, then jump to the Team Document Graph.
7. Use a document or Blueprint selection in Workbench.
8. Sync the completed Workbench result back to team documents.

## Quality Gates

Before considering a change complete, run:

```sh
# Repository root: immutable build inputs and Compose topology.
make compose-check

# Backend; provide real PostgreSQL/Redis test endpoints for integration gates.
cd backend
go test ./... -count=1
go vet ./...

# Frontend.
cd ../frontend
pnpm lint
pnpm typecheck
pnpm test:unit
pnpm build
pnpm test:e2e
```

The production build performs TypeScript validation. Do not re-enable
`ignoreBuildErrors`. A skipped Golden test is not a passing qualification
result; strict qualification must run against an approved endpoint with zero
mock and zero skip.

`qualification/manifest.json` maps every documented AIC/FQP/LSP acceptance ID
exactly once to its test layer and required artifacts. `make
qualification-check` reports internal implementation separately from external
qualification; it must report no pending external suite before any stage-exit
claim. The current `golden-stack.spec.ts` is only a partial health, Message
persistence/idempotency/tenant and browser save/reload smoke. It does not cover
Template bootstrap, Sandbox, Agent, Verification, Release or LSP-QA-016 and
cannot issue an immutable receipt. Because it uses a real Bearer credential,
trace capture is fail closed until credential-safe redaction/encryption and
revocation evidence exist.

The repository now includes an internal, fail-closed verifier for an immutable
external qualification evidence snapshot and threshold-signed DSSE receipt.
It pins the exact workflow node/revision target, short-lived root authority,
source and evidence snapshots, verifier/Git binaries, KMS encryption evidence,
credential revocation, and zero-mock/zero-skip results. This does not mean a
real receipt has been captured or signed: the Golden suites, external KMS and
credential pipeline, and downstream append-only single-consumption ledger are
still absent. The exact trust and recovery boundary is documented in
[`docs/qualification-promotion-operator.md`](docs/qualification-promotion-operator.md).

The Reference bundle is validated by `go test ./internal/contracts/...` and
compiles to the fixed ready BuildContract hash
`675aac0656d005a2b929cb1422a81a373f253fe8d99d1dcc96d483fd0a71099a`.
Constraint Compiler v7 also checks a trusted projection of those exact
FullStack/TemplateRelease authorities against the Deployment v1 service,
mounted path, port, health, migration and required-environment facts. Its
compiler test uses clearly labelled test-only template authorities; an approved
upstream TemplateRelease and real generated-application Gateway run remain
mandatory for external qualification.

After the exact-head Candidate search slice, frontend typecheck, unit tests,
lint, and the production build were rerun successfully on 2026-07-18.
The ordinary Playwright run completed with 87 passed, 0 skipped, and 0 failed
in 3.8 minutes. The separate approved Golden qualification was not run and is
not counted as passing or as external qualification evidence.

## Next Productization Work

- Continue splitting the large store into Workbench, Team, Blueprint, Import, and Prototype domains. Route parsing/path generation already lives in `frontend/lib/worksflow/route-state.ts`.
- Continue splitting Prototype Studio and Blueprint Editor into smaller canvas/tool/inspector components. Shared Prototype Studio helpers already live outside the main component.
- Add URL-backed state for deep links and refresh recovery.
- Replace mock integrations with typed service adapters.
- Extend the data model with document versions, approved snapshots, audit metadata, and external prototype artifacts.
- Add broader browser coverage for drag/drop, responsive layouts, keyboard behavior, and accessibility.
- Provision and qualify the three PostgreSQL identities, GC scheduler and Production LSP against approved target environments without weakening Candidate generation/root/content-hash fences.
