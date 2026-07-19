# Qualification manifest

`manifest.json` is the machine-readable map from every acceptance ID in the AI
constructor and Full-stack Quality Profile documents to its test layer and
required evidence artifacts.

The manifest deliberately separates `implemented-internal` from
`not-qualified`. A passing unit, PostgreSQL, Redis, Docker or ordinary browser
suite cannot promote an external suite. External suites must use an approved
TemplateRelease and exact runtime identities, allow neither mocks nor skips,
and produce the listed artifacts before a stage-exit receipt can be issued.

Run `make qualification-check` after changing either source document, a test
path or this manifest. The checker rejects missing, duplicate or undocumented
IDs, missing internal test paths, unsafe qualification policy, and an external
suite marked qualified while its executable coverage is incomplete.

`test-inventory.json` uses `worksflow-qualification-test-inventory/v2`. A suite
that owns contract criteria first declares its exact path, schema and
application identity in the manifest's hash-bound `criterionSource`; the
inventory must contain exactly the same source and cannot remove or replace it.
Each reviewed case binds both the document-level AIC/FQP/LSP IDs and the exact
`AC-*` IDs from that contract file. An `external-complete` suite must close every declared test
path, every suite requirement, and every criterion in its source. The
normalized Playwright result repeats both ID sets, and the Go receipt verifier
compares them exactly; a renamed, omitted, reordered, foreign, or newly added
criterion therefore requires a new reviewed inventory and plan digest.

The reviewed business inventory is frozen at exactly 22 Playwright cases:
Sandbox 4, Agent 4, Reference 6, Release 5, and LSP 3. The checker enforces the
exact case IDs, suite ownership, total cardinality, titles, paths, requirements,
and Reference criterion closure; adding a nominal 23rd case cannot expand the
qualification plan implicitly. These executable sources remain
`not-qualified` until a real external run produces the complete immutable
evidence closure.

Reference runtime identity is not inherited from the Agent profile. Its strict
source retrieves the fixture-hash-bound immutable deployment receipt as raw
canonical bytes and recomputes SHA-256 before using its service images,
commands, generated-application gateway/provider/model profile, migration,
admission, rate-limit, and retention facts. Qualification operations likewise
resolve their evidence digests to canonical bytes and recompute the digest;
checking a digest-shaped string or a mutable observation is insufficient.

Every suite declares an explicit `executionKind`. `internal-test` and
`playwright` suites use exact test paths; a `post-run-verifier` instead binds
one existing `verificationContractPath` included in
`qualificationSupportPaths`. Artifact hygiene, ModelProfile governance, and
production PostgreSQL posture are not fabricated as browser cases. Only
`playwright` suites participate in the Golden inventory; post-run controls are
evaluated after raw results exist and before a signed Receipt can be accepted.

The current `frontend/tests/golden-stack.spec.ts` is recorded only as a partial
reference-application smoke. It does not cover Template bootstrap, Sandbox,
Agent, independent verification, Release or LSP-QA-016 and therefore cannot
issue a stage-exit receipt.

The embedded Reference AI Conversation bundle under
`backend/internal/contracts/reference/ai-conversation/` is an internal,
hash-closed compiler fixture. It proves that the complete semantic lineage,
eleven API operations, four persistent entities, API/Data/AI Runtime v2,
project-scoped composite foreign keys, matching nine-branch API/standalone
RunEvent schemas, six UI states with three-breakpoint presentation evidence,
and fifteen one-to-one executable blocking oracles form one deterministic ready
BuildContract. The compiler also checks a trusted projection of the exact
FullStack/TemplateRelease manifests against the representable Deployment v1
service, mounted path, port, health, migration and required-environment facts;
identity, set, link, collision and schema-representation drift fails closed.
Its template authorities are explicitly test-only; it neither deploys the
application nor promotes the external `reference-ai-golden-external` suite.
External evidence must also show that v6 constructor/generation/implementation
mutation nodes were drained before v7 contracts were enabled; the repository
tests do not qualify a mixed-writer rollout.

## Qualification promotion contract

External qualification uses two separate revisions so status cannot sign
itself:

1. A qualification-ready revision contains every executable test path and
   required artifact, with `coverage: external-complete` and
   `status: not-qualified`.
2. The runner computes `worksflow-qualification-plan/v1` from the manifest
   policy, source-document hashes, suite/requirement IDs, commands, executable
   test paths and required artifacts. Mutable promotion fields (`status`,
   `coverage`, blockers and Receipt/trust locations) are excluded.
3. A clean exact commit is exercised against an approved TemplateRelease and
   exact external runtime identities. Playwright discovery and results must be
   non-empty, exactly match the v2 inventory's case/requirement/contract-
   criterion identity, and contain zero mock, skip, expected failure, retry or
   flaky outcomes.
4. The root-bound Golden fixture's complete run-scoped credential set is
   issued and revoked atomically with identical member digest/count evidence.
   Trace and video are encrypted in restricted scratch storage before
   distributable evidence is sealed; plaintext credentials and plaintext
   trace never enter the evidence tree.
5. Every Fixture fault reference closes one indexed canonical direct-DSSE
   `golden-fault-authority` and one canonical plain
   `golden-fault-consume-receipt` whose artifact ID is its unique `resultId`.
   One independent `fault-ledger-attestor` signs the complete run-level,
   authority-sorted terminal ledger. Verification replays short-lived
   authority validity at the attested `reservedAt`, reconstructs reservation
   and result digests, and rejects missing, extra, duplicate, unknown or
   non-terminal evidence. `agent-security-canary` must be `refused`; the other
   twelve closed fault operations must be `applied`. The exact 13-operation set
   is root-bound by the canonical digest of
   `qualification/golden-fault-operation-set.json`; the Fixture cannot select a
   subset, and every run-level `adapterInvocationId` must be unique. Attestor
   keys must remain valid through the independently trusted Receipt issuance
   time so revoked/expired keys cannot mint newly backdated ledger evidence.
6. `worksflow-qualification-artifact-index/v1` binds the run, plan, source,
   TemplateRelease and every regular artifact by normalized path, byte count
   and SHA-256. Symlinks, hard links, devices, unlisted files and files that
   change while hashing are rejected.
7. The exact index digest is the sole subject of an in-toto Statement using
   the `worksflow-qualification-receipt/v2` predicate. A canonical DSSE
   envelope must be signed by distinct trusted `qualification-runner` and
   `release-approver` identities whose keys were valid at completion.
8. A promotion-only revision may add the signed Receipt/trust coordinates and
   set `status: qualified`. The checker recomputes the pre-promotion plan,
   verifies signatures and roles, and rehashes the complete artifact tree.

Changing a source document, test inventory, command, required artifact,
TemplateRelease, runtime identity or compiler rollout evidence requires a new
run and Receipt. An old Receipt remains auditable but cannot authorize the new
plan.

The Node manifest checker deliberately rejects any attempted `qualified`
promotion until the cryptographic verifier accepts this full closure. Its
Golden source policy rejects Playwright route interception, response
fulfilment, `skip`/`fixme`/expected-failure/`only`, and imports whose path is
explicitly labelled mock/fake/fixture/stub. This static check supplements the
signed runtime evidence; it does not by itself prove that an endpoint is real.

Current plan digest is printed by `make qualification-check`. It is planning
identity, not qualification evidence and not a Receipt.

The required root trust policy is `worksflow-qualification-trust-policy/v2`.
In addition to runner, approver, credential issuer and KMS authorities it has
separate threshold sections for `fault-operator` authority signing and the
`fault-ledger-attestor`. Key IDs, identities and public-key fingerprints are
globally non-reusable across every one of those roles. This repository has no
real fault adapters or external attestor/orchestrator artifacts yet; the new
closure remains internal verifier capability and does not change any external
suite status.
