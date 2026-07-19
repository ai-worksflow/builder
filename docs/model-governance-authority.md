# Model Governance authority v1

Status: implemented-internal authority contract; no model is activated or
qualified by this repository state.

This authority is independent of the external Golden Qualification Receipt.
Golden evidence cannot approve a model, and a Model Governance receipt cannot
complete an external qualification or workflow review gate.

## Exact authority closure

One activation candidate is closed by canonical, digest-fenced bytes for the
following objects:

- `ModelProfile` and its frozen conformance corpus;
- threshold policy, harness, verifier, immutable Codex runner and clean source
  tree digests;
- commitment-only `ProviderRouteAuthority` containing a registry route ID,
  endpoint/TLS/egress digests and validity window, but no executable URL or
  network permission;
- distinct DSSE-signed conformance, shadow, profile approval, activation and
  `ModelGovernanceReceipt` payloads.

Generation one instead uses the distinct signed Genesis decision/receipt chain
documented below; it deliberately omits Shadow and cannot be parsed as an
ordinary activation.

The immutable signer policy has its own canonical bytes. Its hash covers every
key ID, public key, signer identity, closed role and validity interval. Reusing a
`PolicyHash` with substituted signer material is rejected. Policies remain
loadable by the exact hash bound into each receipt, while only the current
policy can sign a new activation candidate. This lets planned signer-policy
rotation preserve historical verification instead of invalidating every old
receipt. The conformance verifier, shadow verifier, profile approval signer,
activation approver, Genesis approver and receipt issuer have distinct
identities and public keys. In particular, no existing identity or key can
substitute for the independent Genesis role.

Operational digest and signer-key revocations are a separate canonical,
short-lived, cumulative authority. Its hash covers its epoch, validity window,
and every revocation time/reason/target. It is intentionally not part of
`PolicyHash`: adding one unrelated revocation therefore does not replace every
signed subject. `ActivationStore.ObserveGovernanceRevocationAuthority` is the
durable monotonic anchor. A lower epoch, same-epoch different hash, or higher
epoch which deletes a previously observed revocation fails closed. A production
authority adapter must provision the initial trusted epoch. The PostgreSQL
implementation durably persists observations, while the in-memory
implementation remains the deterministic reference behavior; neither one
manufactures a trusted authority.

Migration `000070_model_governance_signed_genesis` also fences the exact
current signer-policy hash to the exact durable revocation epoch/hash. A policy
cannot change within one revocation epoch: the owner-only observation routine
rejects same-epoch policy equivocation and epoch rollback. Planned policy
rotation must publish a higher cumulative revocation epoch before observing
the new policy. The Genesis append locks and checks both durable anchors in the
same transaction; it does not treat a caller echo as current authority.
The same migration installs an owner-only insert trigger on the immutable
registry, so ordinary generation-greater-than-one appends also lock/check the
current trust hash, matching revocation epoch/hash and post-lock database time
at the atomic insert. `ActivationService` observes the policy/revocation pair
before both ordinary activation and runtime resolution; a half-published
authority rotation fails closed.

The strict time chain is:

```text
conformance start < completion <= issuance
conformance completion <= shadow start < shadow completion <= shadow issuance
conformance completion/issuance <= approval issuance
conformance completion/issuance + shadow completion/issuance + approval issuance <= activation issuance
activation issuance < receipt issuance
```

For runtime authorization, every artifact and signer must also be current at
trusted time. Trusted time is normalized once at the boundary to canonical UTC
milliseconds. Expiry, current cumulative revocation, reference/hash drift,
wrong role, non-canonical JSON or incomplete subject closure fails closed.
Approval and shadow work may overlap only after conformance completed;
activation waits for both completed/issued evidence and the approval.

## Activation registry and runtime

`ActivationService` is the ordinary transition boundary;
`GenesisBootstrapService` is the separate generation-one boundary.
`ActivationStore` is an append-only persistence contract: an operation ID is bound to one exact
request hash, workload heads move through generation/fence CAS, and history and
the exact-profile index remain immutable. Success, idempotent replay and an
unknown append outcome are all reconstructed from the same operation, history,
exact-profile and workload-head projections; unknown is inspected without a
second append. A later head is accepted only when its complete immutable
generation/fence chain descends from the exact operation.

Migration `000069_model_governance_activation_store` and
`PostgresActivationStore` now implement this contract internally. One immutable
activation row is simultaneously the operation, generation-history and exact
profile projection through three unique keys. A separate workload head contains
only a composite foreign key to that exact row. The reviewed append function
locks the head, enforces generation/fence CAS, inserts the immutable row and
moves the head in one database transaction. Ordinary appends still reject an
empty head. Only migration `000070`'s distinct owner-only Genesis routine may
atomically insert generation one after the signed Genesis receipt, exact
trust/revocation anchors and explicit non-zero empty-head fence close. Every returned
or loaded row is NULL-total and is revalidated through the same activation
append validator. A new append must use the canonical PostgreSQL UTC-millisecond
clock observation within the 30-second governance skew; that clock is refreshed
after acquiring the workload lock, so lock waiting cannot authorize stale
backfill. A commit acknowledgement failure is reported as an unknown activation
outcome and must be reconciled through the same operation ID. Known CAS and
constraint failures are conflicts.

The PostgreSQL revocation anchor stores the highest epoch/hash together with its
exact canonical bytes and parsed document. Its owner-only observe function
serializes even the initially empty singleton, recomputes SHA-256 over the
bytes, checks that those bytes are the supplied JSON document, validates epoch
and window closure, and rejects rollback, same-epoch equivocation, or deletion
of any prior digest or signer-key revocation. Trusted time is refreshed after
the singleton lock before currentness is accepted. Go first builds and parses
the canonical document and verifies its hash, uniqueness and ordering; caller
JSON is never treated as an independent authority.

Migrations `000069` and `000070` deliberately grant neither the ordinary
`worksflow_application` role nor `PUBLIC` any table or routine access. Their
four SECURITY DEFINER entrypoints and tables are owner-only and have fixed
`pg_catalog, <trusted-schema>, pg_temp` lookup paths. The migration owner is for
migrations and maintenance only and must never be used as a runtime DSN. A
future independent NOLOGIN Model Governance operator group, a narrowly scoped
login/DSN, exact grants, and a real trusted authority adapter must be provisioned
and added to the production posture contract before `PostgresActivationStore`
can be wired. If the stable migration-owner or application roles did not exist
when `000069` ran, provisioning them later does not retroactively repair object
ownership or ACLs; operators must apply a separately reviewed ownership/ACL
repair before startup qualification.

### Signed Genesis/bootstrap closure

Genesis is not an ordinary activation with a fabricated Shadow. It uses two
distinct DSSE payload types: `GovernanceGenesisArtifact` and
`ModelGovernanceGenesisReceipt`, with the closed decision `bootstrap`. The
chain binds the canonical ModelProfile, corpus, route, runner, clean source,
current trust policy, exact signed conformance/profile-approval references,
`generation=1`, `previousGeneration=0`, an explicit digest-shaped empty-head
fence, a distinct new fence, and the exact initial cumulative revocation
authority ID/hash/epoch. The receipt issuer closes the Genesis, conformance and
approval envelope/payload digests. Ordinary verification rejects Genesis
payload types, and Genesis verification rejects ordinary payload types.

`GenesisBootstrapStore.AppendGenesis` is separate from `AppendActivation`.
Exact replay is idempotent; same ID with different request or projection bytes
fails. Memory and PostgreSQL stores serialize concurrent first writers and
atomically create operation/history/exact-profile/head projections. PostgreSQL
recomputes the request hash, reads UTC-millisecond database time after locking
the empty-head and both authority anchors, and maps lost commit acknowledgement
to inspect-the-same-operation semantics. Both rollback layers fence concurrent
writers before testing emptiness: `000070.down` takes `ACCESS EXCLUSIVE` on
records, heads, and the revocation/trust anchor before refusing any Genesis or
trust-policy state; `000069.down` takes the same three locks and refuses any
activation record, head, or revocation anchor. A migration-owner action is not
treated as permission to discard immutable governance history.

Every registry row has a closed `authority_kind`. Genesis rows project the
exact Genesis envelope/payload and initial revocation binding; ordinary rows
must leave those fields absent. Runtime resolution, fallback traversal and
later ordinary activation branch on this kind. A Genesis predecessor is
reverified through the Genesis chain, never through Shadow. The first ordinary
activation must name the exact Genesis `(profile, receipt, generation, fence)`
as its signed Shadow baseline.

A shadow baseline is not trusted because a shadow signer named it. Before an
activation, the service resolves the exact `(profile, generation, fence,
receipt)` both as the current workload head and as immutable activation history,
then fully re-verifies that receipt. A disabled predecessor or one whose old
route is no longer current may still be checked historically at its persisted
`ActivatedAt` solely to authorize replacement; that result is never returned as
runtime authority. Candidate and fallback nodes still require current route,
disable, time and revocation checks. An empty registry rejects ordinary
activation and can be closed only through the signed Genesis service; no
unsigned insertion or fake Shadow is exposed as a production API.

Every fallback is resolved recursively through the append-only exact-profile
activation registry, fully re-verifies its own receipt/materials, receipt-bound
signer policy, current revocations and disable state, and then passes
`ValidateModelProfileGraph`. Missing or
wrong-workload targets, hash/receipt drift and cycles fail closed. The same
exact `(workload, profile ID, profile hash)` cannot later be rebound to a
different activation or receipt; a changed authority requires a new immutable
profile hash.

`ResolveActive` is a per-provider-call check, not a cacheable token. It always
reloads exact materials and receipt-bound signer policies, repeats full
signature/hash/time/current-revocation/provider-route verification for the
primary and every fallback, requires the bound route bytes to remain the current
route-registry authority, checks exact generation/fence/receipt projection and
current disable state, and double-reads the workload head and graph
dependencies.

These reads detect drift observed during resolution, but the route registry,
disable authority, signer-policy registry, revocation ledger and activation
store do not yet expose one common transactional epoch. A route, disable state,
or head can change after its closing read and before a provider data-plane call.
Consequently `ResolvedActive` is not proof that all concurrent drift was
rejected. It exposes the normalized observation time and exact cumulative
revocation epoch/hash alongside the generation/fence/receipt and graph route
hashes, but the future data plane must consume/check those observations
atomically immediately before execution. This repository does not claim that
external composition yet.

Disable-state answers are exact-receipt scoped and valid for at most five
minutes. Missing, stale, future, non-canonical, overly long, undeclared or
non-empty conditions all disable execution.

## Deliberate remaining blockers

The repository does not contain a real corpus runner, live provider execution,
approved model/corpus/route/runner artifacts, signed production governance
receipts, a production-signed Genesis receipt or provisioned initial
revocation/trust authority, the independent production Model Governance operator and
trusted authority adapters described above, an authoritative disable-state
service, or a common authority epoch/data-plane consume fence. The durable store
is implemented-internal but intentionally unwired. No sample profile is active,
approved, qualified or permitted to make a provider call. Those are independent
external and production integration obligations; neither in-memory nor
PostgreSQL storage tests can satisfy them.

Focused internal verification:

```sh
go test ./internal/modelgovernance
go test -race ./internal/modelgovernance
go vet ./internal/modelgovernance

# Opt-in real PostgreSQL closure (also exercises migrations 000069/000070,
# Genesis, authority anchors, rollback fence and ACLs)
WORKSFLOW_TEST_POSTGRES_DSN=... go test -run \
  TestPostgresActivationStoreRealPostgresAuthorityClosure \
  ./internal/modelgovernance
```
