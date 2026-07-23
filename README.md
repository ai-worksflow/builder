# Worksflow Builder

Worksflow Builder combines a Next.js collaborative product workbench with a Go
control plane for governed application construction. The repository includes
planning, document, Blueprint, Prototype and Review surfaces, plus
server-backed Repository Candidates, an isolated development Sandbox, a
browser IDE, verification and release authorities, qualification evidence
controls, and a local Kubernetes isolation reference.

Last synchronized with the repository documentation and implementation:
2026-07-23.

## Project status

The project uses three distinct status levels:

- `implemented-internal`: code, migrations and repository-local evidence exist;
- `production-wired`: the target environment, identities, secrets and external
  services are provisioned and connected;
- `externally-qualified`: the approved Golden suites completed without mocks or
  skips and produced an accepted immutable Receipt.

Current status:

| Area | Status | Current boundary |
| --- | --- | --- |
| Product workbench | Mixed server-backed and prototype surfaces | Core document, Blueprint, Prototype, Review and Workbench experiences exist; some legacy integrations and presentation flows still use local mock state |
| Repository Candidate and browser IDE | `implemented-internal` | Exact-head autosave/search, checkpoints, Monaco, PTY, preview grants, rebase, Agent Merge/Undo, verification, freeze and apply authorities are implemented |
| Sandbox lifecycle | `implemented-internal` | The migration chain reaches `000089`; migrations `000086`–`000089` harden exact Session write projection, absolute-TTL reconciliation, terminal transition boundaries and checkpoint guards |
| Production LSP v1 | `implemented-internal` | LSP-0–3 and Monaco integration exist; LSP-4/`LSP-QA-016` still requires an approved Golden TemplateRelease and real external qualification |
| Template admission | `candidate / blocked` | The audited `ai-worksflow/templates` source is not an approved Golden TemplateRelease |
| Workflow profile v3 | `implemented-internal`, disabled | The private v3 runtime, migrations `000083`–`000085`, WIA activation and qualified-release workers exist, but default registration and feature gates remain off |
| Release Controller v3 | `implemented-internal`, opt-in | Durable Operation/Attempt/Result reconciliation and qualified release authority exist; no deployed, qualified production Controller is claimed |
| Local Kubernetes slice | Local reference only | kind, Cilium, Envoy Gateway, project namespaces, quotas, default-deny policies and route verification are available under `deploy/k8s/` |
| External qualification | `not-qualified` | `make qualification-check` currently maps 94 acceptance IDs: 48 `implemented-internal`, 46 `not-qualified`, 0 external suites qualified and 7 external suites pending |

Repository-local tests and canaries are implementation evidence only. They do
not imply production readiness or external qualification.

## Current capabilities

- Planning -> Plan Ready -> Building -> Complete Workbench flow with Preview,
  Code and Database modes.
- Team dashboard, Document Graph, Document Editor, Blueprint Editor, Prototype
  Studio, Design Import Center and Review Center.
- Chinese and English UI copy through the local i18n provider.
- Authenticated Candidate bootstrap from an exact WorkspaceRevision or admitted
  TemplateRelease source tree.
- Server-side Candidate autosave, immutable checkpoints, deterministic rebase,
  explicit conflict decisions and recoverable history.
- Exact-head, project-scoped literal search with generation/root fencing,
  bounded admission, authoritative-byte rechecks and stale-result rejection.
- Monaco diagnostics/diff, xterm PTY, supervised Template commands and isolated
  HTTP/HMR Preview grants.
- Governed Agent Attempts with immutable evidence, task graph, structured
  results, explicit three-way Merge and Undo.
- Candidate and Canonical verification authorities, immutable Receipts, freeze
  gates and exact Implementation Proposal apply.
- Canonical Review approval, Workflow Input, qualification plan/evidence/
  receipt, input precommit, promotion and handoff authorities.
- ReleaseBundle, Preview, Production, rollback and Release Controller v3
  reconciliation authorities.
- A hash-closed Reference AI Conversation contract bundle used as an internal
  compiler fixture, not as a deployed application.

## Honest boundaries

The authenticated Code/Preview path fails closed when an exact BuildManifest,
template policy, Candidate, Sandbox or runtime identity cannot be obtained. It
does not fall back to generated textarea files, simulated terminal output or
`srcDoc`.

The following areas are not complete end-to-end production capabilities:

- Team collaboration prototype fixtures and some catalog persistence still use
  local compatibility data; the Workbench itself now starts blank and uses the
  configured generation service.
- Supabase, Figma, Penpot, Excalidraw, tldraw, Storybook and upload integrations.
- Legacy GitHub, publish/share/transfer/export/analytics/knowledge/connector
  demo actions.
- A live admitted Agent Runner, real model Provider qualification and complete
  Golden browser evidence.
- Approved TemplateRelease, external KMS/Registry evidence, production
  PostgreSQL role/secret provisioning, and a qualified Release Controller.
- Production LSP external qualification and the complete workflow-v3
  `Quality -> WIA -> Input Precommit -> Promotion -> Handoff -> Controller ->
  Publish` no-bypass run.

Development Preview is not Production, a passing ordinary Playwright suite is
not a Golden qualification, and a skipped external test is not a pass.

## Documentation map

| Topic | Document |
| --- | --- |
| Product prototype and collaboration UX | [`docs/worksflow-generation-workbench-prototype-spec.md`](docs/worksflow-generation-workbench-prototype-spec.md) |
| Platform facts, planes and version model | [`docs/platform-architecture.md`](docs/platform-architecture.md) |
| Governed AI constructor, Candidate, Sandbox, LSP and release architecture | [`docs/ai-constructor-architecture.md`](docs/ai-constructor-architecture.md) |
| Backend startup, migrations, identities and runtime configuration | [`docs/backend-development.md`](docs/backend-development.md) |
| Candidate/Canonical verification and release quality gates | [`docs/full-stack-quality-profile.md`](docs/full-stack-quality-profile.md) |
| Template admission result | [`docs/template-admission-audit.md`](docs/template-admission-audit.md) |
| Template artifact operator contract | [`docs/template-artifact-authority-operator.md`](docs/template-artifact-authority-operator.md) |
| Canonical Review approval authority | [`docs/canonical-review-approval-authority.md`](docs/canonical-review-approval-authority.md) |
| Workflow Input and qualification policy | [`docs/workflow-input-authority.md`](docs/workflow-input-authority.md), [`docs/qualification-policy-authority.md`](docs/qualification-policy-authority.md) |
| Qualification input, promotion, handoff and Receipt | [`docs/qualification-input-precommit-authority-v1.md`](docs/qualification-input-precommit-authority-v1.md), [`docs/qualification-promotion-v2.md`](docs/qualification-promotion-v2.md), [`docs/qualification-handoff-v1.md`](docs/qualification-handoff-v1.md), [`docs/qualification-receipt-v3.md`](docs/qualification-receipt-v3.md) |
| Golden evidence control plane | [`docs/golden-qualification-control-plane.md`](docs/golden-qualification-control-plane.md), [`docs/qualification-promotion-operator.md`](docs/qualification-promotion-operator.md) |
| Workflow profile v3 runtime and release closure | [`docs/workflow-execution-profile-v3-runtime.md`](docs/workflow-execution-profile-v3-runtime.md), [`docs/qualification-release-v1.md`](docs/qualification-release-v1.md) |
| Sandbox product boundary | [`docs/sandbox-preview-product-boundary.md`](docs/sandbox-preview-product-boundary.md) |
| Kubernetes isolation and routing | [`docs/kubernetes-project-isolation-and-routing.md`](docs/kubernetes-project-isolation-and-routing.md), [`deploy/k8s/README.md`](deploy/k8s/README.md) |
| Legacy retirement inventory and deletion gates | [`docs/legacy-retirement.md`](docs/legacy-retirement.md) |
| Executable qualification inventory | [`qualification/README.md`](qualification/README.md), [`qualification/manifest.json`](qualification/manifest.json) |

When a summary here conflicts with a specialized contract, the specialized
document and executable schema/manifest are authoritative.

## Repository structure

```text
frontend/
  app/                         Next.js App Router entry points
  components/worksflow/        Product and Workbench UI
  lib/i18n/                    Locale configuration and messages
  lib/platform/                Typed platform clients and contracts
  lib/worksflow/               Product model and shared feature logic
  tests/                       Unit, interaction and Golden test sources
backend/
  cmd/                         API, migrator and operator binaries
  internal/                    Platform control planes and adapters
  migrations/                  Ordered PostgreSQL authority contracts
agent-runner/                  Ephemeral Codex Agent runtime
sandbox-runner/                Non-root IDE, PTY and Preview runtime
deploy/
  k8s/                         Local Kubernetes isolation vertical slice
  nginx.conf                   Local public gateway
qualification/                 Acceptance manifest, inventory and schemas
docs/                          Product, architecture and authority contracts
docker-compose.yml             Local reference topology
Makefile                       Build, validation and deployment entry points
```

## Quick start

### Frontend only

Use this path for UI work that does not require server-backed control planes:

```sh
cd frontend
pnpm install
pnpm dev
```

Open `http://localhost:3000`.

### Complete local reference stack

Docker Compose defines the frontend, Go API, PostgreSQL, Redis, MongoDB, NATS
JetStream, Nginx and the isolated local Docker runtime. From the repository
root:

```sh
make deploy
```

Open `http://localhost:10000`. Nginx is the only public entry point:

- `/` -> Next.js frontend;
- `/api/platform/*` -> Go API with the prefix removed;
- `/health/*` and `/published/*` -> Go API.

Useful commands:

```sh
make status
make logs
make down
```

`make down` preserves persisted volumes. Use destructive volume removal only
when intentionally resetting all local data.

For a non-localhost browser origin, provide the public values before building:

```sh
NGINX_PORT=10000 \
PLATFORM_ALLOWED_ORIGINS=https://app.example.com \
DELIVERY_PUBLISH_BASE_URL=https://app.example.com/published \
make deploy
```

The credentials and mutable service tags in `docker-compose.yml` are local
development conveniences. They are not acceptable production identities.

## Opt-in runtimes

Sandbox, Agent, workflow-v3 and Release mutation paths are disabled by default
and fail closed until their exact inputs are configured.

### Sandbox and Agent

Runner images require reviewed digest-pinned Go and Node base images, an exact
Codex version and package integrity before they can be admitted:

```sh
RUNNER_GO_IMAGE='golang@sha256:<approved-digest>' \
RUNNER_NODE_IMAGE='node@sha256:<approved-digest>' \
CODEX_VERSION='<reviewed-version>' \
CODEX_INTEGRITY='sha512-<reviewed-integrity>' \
AGENT_RUNNER_TAG='registry.example/worksflow/agent-runner:<version>' \
SANDBOX_RUNNER_TAG='registry.example/worksflow/sandbox-runner:<version>' \
make runtime-images
```

Push, scan, attest and admit the results, then configure runtime references as
`image@sha256:...`. A local or mutable tag is not an admissible runtime
identity. See [`agent-runner/README.md`](agent-runner/README.md),
[`sandbox-runner/README.md`](sandbox-runner/README.md) and
[`docs/ai-constructor-architecture.md`](docs/ai-constructor-architecture.md).

### Release Controller v3

Release delivery requires a qualified HTTPS Controller implementing
`worksflow.release-delivery/v3`, normal PKI plus the configured leaf SPKI pin,
and exact worker/controller identities. Enabling its environment flags proves
configuration only; it does not qualify the Controller. See
[`docs/workflow-execution-profile-v3-runtime.md`](docs/workflow-execution-profile-v3-runtime.md).

### Local Kubernetes slice

```sh
make k8s-bootstrap
make k8s-deploy
make k8s-verify
make k8s-status
make k8s-down
```

This uses a local kind cluster and local routes. Production must provide a
supported cluster, qualified CNI/RuntimeClass, LoadBalancer, wildcard DNS,
certificates, durable controllers and environment-specific identity/secret
controls.

## Validation

Run checks from the directory shown:

```sh
# Repository root: acceptance mapping, build inputs and Compose topology.
make qualification-check
make compose-check

# Backend. Integration coverage needs real PostgreSQL/Redis test endpoints.
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

`pnpm test` runs the unit and ordinary Playwright suites. Golden qualification
is deliberately separate and must run against approved external inputs with
zero mock, skip, expected failure, retry or flaky result. The qualification
rules, current plan digest and required evidence are maintained in
[`qualification/README.md`](qualification/README.md).

## Manual prototype path

1. Open the Workbench.
2. Wait for Planning to reach Plan Ready.
3. Select `Implement this plan`.
4. Watch the checklist advance to Complete.
5. Switch among Preview, Code and Database.
6. Open Linked docs and navigate to the Team Document Graph.
7. Send a document or Blueprint selection to the Workbench.
8. Sync the completed result back to team documents.

## Next work

- Admit an approved Golden TemplateRelease and execute the external
  Sandbox/Agent/Reference/Release/LSP suites with immutable evidence.
- Provision and validate production PostgreSQL roles, LOGINs, DSNs, schema,
  ACLs, secrets and operator workers without widening authority.
- Run the complete workflow-v3 PostgreSQL no-bypass chain with a real Release
  Controller before enabling the profile in a target environment.
- Qualify real language-server ingress and close LSP-4/`LSP-QA-016`.
- Promote the local Kubernetes reference into a reviewed target-environment
  deployment with qualified images, routing, isolation and observability.
- Replace the remaining mock integrations and expand browser accessibility,
  responsive, keyboard and failure-recovery coverage.
