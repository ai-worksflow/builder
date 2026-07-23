# Qualification Input Precommit Authority v1

Status: implemented migration `000080`, in-memory semantic reference, and
three-role production PostgreSQL Store/clock adapter. This
authority closes two inputs that Workflow Input Authority (WIA), Qualification
Policy v1, and Qualification Plan v1 cannot represent as one durable edge. It
is a prerequisite for migration `000081` Promotion v2, not a Promotion
approval, Receipt, migration `000082` workflow handoff, immutable output
revision, or deployment authorization.

The Go contract lives in `backend/internal/qualificationinputauthority`.
Migration `000080` implements the PostgreSQL authority and production role
posture described in sections 8 and 9. Its package-level fake-driver fault
suite and real PostgreSQL independent-LOGIN canary pass; runtime activation
remains disabled until the remaining upstream resolver/runtime wiring and the
complete `000080` -> `000082` no-bypass suite pass.

## 1. The gap this authority closes

The current authorities intentionally retain different facts:

- WIA binds the exact workflow activation, target, BuildManifest,
  BuildContract, reviewed revisions, and current Qualification Policy ID/hash.
- Qualification Policy freezes `planInputProfile.sourcePolicyDigest` and the
  complete non-secret `credentialProfile`, including
  `memberRequestSetDigest`.
- Qualification Plan binds the WIA as its input authority and freezes the
  clean repository commit/tree projection plus the resolved credential-set
  projection.

Plan v1 has no member for `sourcePolicyDigest` or
`credentialProfile.memberRequestSetDigest`. WIA has no member for the clean
repository tree or resolved credential set. Therefore Promotion cannot prove
either of these relationships by comparing existing v1 columns:

```text
current Policy.sourcePolicyDigest
  -> independently verified Plan.source(commit, tree schema, tree digest, clean)

current Policy.credentialProfile(memberRequestSetDigest included)
  -> independently resolved exact Plan.credentialSet
```

Those digest domains are not interchangeable. In particular, source policy is
not a source-tree digest, a member request-set digest is not a member-bindings
digest, and neither may be replaced by the WIA input hash, Plan input hash, or
artifact revision content hash.

## 2. Security boundary and non-authorizations

Only a trusted server composition service may issue or resolve an input
precommit. The browser, a model, generated application code, workflow context,
or a public API DTO may not submit any canonical member, upstream hash,
verifier result, credential result, receipt hash, executable digest, operation
ID, authority ID, or timestamp.

The authority carries no credential member, token, cookie, header, private
key, connection string, environment value, URL, filesystem path, storage
state, or other secret. Credential issuance and delivery stay inside the
credential authority. The only credential facts retained here are the
reviewed profile, the non-secret resolved Plan expectation, and an immutable
receipt hash proving that resolution.

Issuance does not say that qualification executed, Evidence reached a
terminal state, Receipt v3 is valid, ModelProfile/PostgreSQL posture passed,
the target is still current at a later time, or a workflow node can complete.
Promotion must still compose all of those independent gates.

## 3. Exact upstream closure

An input precommit binds one exact five-identity set. All identities are
lowercase RFC-4122 UUIDv4 values and pairwise distinct:

```text
operationId                  server allocation; idempotency key
authorityId                  server allocation; immutable record identity
workflowInputAuthorityId     exact WIA
qualificationPolicyAuthorityId exact current active Policy generation
qualificationPlanAuthorityId exact Plan
```

The immutable projection contains:

- WIA ID, AuthorityHash, and InputHash;
- Policy ID, AuthorityHash, PlanInputProfileHash, SourcePolicyDigest, and the
  complete exact CredentialProfile;
- Plan ID, AuthorityHash, InputAuthorityID, and InputHash;
- the exact reviewed Source Verifier and Credential Resolver stable identities
  plus their distinct executable digests;
- the exact Plan source projection: clean 40-hex commit,
  `worksflow-source-content-tree/v1`, and tree digest; and
- the exact Plan credential-set projection: set UUID, issuer, audience, set
  handle hash, member-bindings digest/count, and distinct issuance/revocation
  artifact IDs.

The following equality checks are mandatory both during composition and again
inside the atomic database append:

```text
Plan.inputAuthorityId = WIA.authorityId
WIA.qualificationPolicyAuthorityId/hash = exact Policy ID/hash
Policy is the current active generation for the WIA project/profile

Policy.credentialProfile.authorityId = Plan.credentialSet.issuer
Policy.credentialProfile.audience = Plan.credentialSet.audience
Policy.credentialProfile.issuanceArtifactId = Plan.credentialSet.issuanceArtifactId
Policy.credentialProfile.revocationArtifactId = Plan.credentialSet.revocationArtifactId
```

The member request-set digest is deliberately not compared to the member
bindings digest. The trusted Credential Resolver proves the former authorized
the latter and returns an immutable receipt hash bound to the exact canonical
resolution request.

## 4. Two independent executable authorities

The composition service requires two sealed interfaces:

```go
type SourceVerifier interface { /* package-sealed */ }
type CredentialResolver interface { /* package-sealed */ }
```

`NewSourceVerifier` and `NewCredentialResolver` install server adapters. Each
adapter has a bounded stable authority identity and an immutable executable
digest. Each receives only a typed, canonical, non-secret request. It returns
an observation that binds the exact request hash and one immutable receipt
hash. The public observation is converted immediately into a package-private
verified grant; callers cannot manufacture or pass a grant into `Service`.

The grant is not written straight into the precommit row. The Store first
appends a package-owned immutable receipt admission containing the closed kind,
adapter identity, executable digest, request hash, and externally verified
receipt hash. Exact canonical admission bytes and
`ReceiptAdmissionHashDomainV1` are retained. Only that package-private grant
can call the kind-specific admission method. The precommit proof binds both
the external receipt hash and the local admission hash, and atomic issue locks
and re-resolves the admission. Thus a syntactically valid candidate
`receiptHash` is never accepted as proof by itself.

Source and credential roles must have different authority identities and
different executable digests. The six source/credential request, external
receipt, and local admission hashes are pairwise distinct across both roles.
Identity, digest, or proof-domain aliasing fails closed even if both adapters
otherwise return success. The service computes both request hashes before the
first admission and validates the credential grant before persisting it, so a
cross-domain observation cannot poison the credential request's durable
first-commit winner. The source verifier cannot satisfy the credential role
and the credential resolver cannot satisfy the source role.

The source request binds exact WIA/Policy/Plan references,
`sourcePolicyDigest`, the Plan source projection, and the reviewed Source
Verifier identity/executable digest. The credential request binds the same
exact authority references, the reviewed Credential Resolver
identity/executable digest, the complete Policy
CredentialProfile (including `memberRequestSetDigest`), and the complete Plan
credential set. Receipt storage and signature verification belong to the two
upstream authorities; this authority retains their immutable hashes rather
than copying opaque receipt payloads.

`NewService` compares the two reviewed bindings before any external callback,
so identity or executable aliasing has no verifier side effect. After resolving
the upstream tuple, Service also requires each installed adapter to equal the
reviewed binding. Existing admissions and concurrent first-commit winners are
accepted only when their authority identity and executable digest equal the
binding inside the exact request. The atomic Issue Store repeats the same
comparison; an older executable's otherwise valid receipt cannot win a new
composition.

## 5. Canonical wire and hashes

The five closed schemas are:

```text
worksflow-qualification-input-precommit-request/v1
worksflow-qualification-source-verification-request/v1
worksflow-qualification-credential-resolution-request/v1
worksflow-qualification-input-verification-receipt-admission/v1
worksflow-qualification-input-precommit-authority/v1
```

Canonical JSON is bounded, BOM-free UTF-8 with duplicate and unknown names
rejected, UTF-8-byte ordered object keys, one exact document, integer-only
JavaScript-safe numbers, lowercase UUID/digest forms, and no insignificant
whitespace. Issuance time is trusted UTC millisecond time encoded as
`YYYY-MM-DDTHH:MM:SS.mmmZ`.

Go and PostgreSQL share a frozen ASCII-only case-folded sensitive-string
predicate for provider tokens, bearer/header values, credential URLs and
assignments, environment assignments, private keys, and absolute host paths.
It deliberately does not depend on a database locale's word, whitespace, or
case-fold classes. Audience limits are measured after UTF-8 conversion, and
the complete Unicode White_Space set is rejected at either boundary, matching
Go's canonical string rule even on a non-UTF-8 PostgreSQL server encoding.

Every document is retained as exact canonical raw bytes plus its domain hash:

```text
SHA256(
  UTF8("worksflow-qualification-input-precommit-hash/v1") || 0x00 ||
  UTF8(domain) || 0x00 ||
  exactCanonicalBytes
)
```

Domains are:

```text
worksflow.qualification-input-precommit.request/v1
worksflow.qualification-input-precommit.source-request/v1
worksflow.qualification-input-precommit.credential-request/v1
worksflow.qualification-input-precommit.receipt-admission/v1
worksflow.qualification-input-precommit.authority/v1
```

The root Authority document binds the two request hashes and both proof
bindings (authority identity, executable digest, request hash, external
receipt hash, local admission hash). It does not contain its own AuthorityHash.

## 6. Service, replay, and commit uncertainty

The server command contains only the five UUIDs from section 3. `Service.Issue`
uses this sequence:

```text
validate server command
  -> inspect operation (exact replay wins before any external call)
  -> resolve exact server-owned WIA/Policy/Plan projections
  -> canonicalize both source and credential requests; reject request aliasing
  -> reuse source admission or call SourceVerifier; reject cross-domain proof before admit
  -> reuse credential admission or call CredentialResolver; reject all six hash aliases before admit
  -> revalidate role identity/executable/proof independence
  -> obtain trusted millisecond database time
  -> compile exact canonical authority bytes/hash
  -> atomically re-resolve, lock, compare current inputs, and append
```

The Store must recheck every upstream scalar, raw canonical component, and
hash while holding the production locks. A successful external verification
cannot turn a stale Policy generation or changed upstream tuple into an
authority.

The operation ID is the sole precommit idempotency key. Exact replay returns the original
bytes even if an adapter or upstream record is later retired. The same
operation ID with different command identities is a conflict. WIA and Plan are
single-use in v1; another operation cannot bind either one.

Receipt admission is a durable two-step saga because an external verifier must
not run inside the final database transaction. `(kind, requestHash)` is its
unique idempotency key. On retry, Service resolves this key before calling the
adapter and reuses the first admitted receipt. If two concurrent verifier calls
return different observations, the first committed admission wins and every
competitor resolves that same record; the service never ranks, replaces, or
selects the later receipt. A crash after source admission but before credential
admission or final Issue therefore resumes from the source admission without
calling the source verifier again. Admission commit-unknown is recovered from
its deterministic AdmissionHash; request-key recovery handles a concurrent
winner with a different observation. Because each request includes its
reviewed executable binding, a deployment of a different executable produces a
different request hash; it cannot occupy or reuse the old request key.

Resolver, verifier, clock, Store, and inspection errors cross the service
boundary only as stable error classes (plus context cancellation/deadline).
Raw adapter errors are never interpolated into returned text because they may
contain endpoints, credentials, environment values, or host paths.

When Store commit status is unknown, the service inspects only the same
operation ID and requires byte-for-byte equality with the candidate. A missing
result remains outcome-unknown. It never allocates another ID, calls either
adapter again under a new operation, or treats a retryable transport error as
proof that nothing committed.

The production `PostgresStore` is deliberately not an `AuthorityResolver`.
Preflight WIA/Policy/Plan resolution is a separate trusted server dependency
installed into `Service`; the three migration-80 runtime roles have no generic
upstream table or resolver capability from which they could assemble that
projection. Production wiring must supply an independently reviewed resolver
that returns `ResolvedAuthorities`. It must not replace that dependency with
autocommit reads through any of the Store DSNs. `Store.Issue` remains the
authoritative transaction-time re-resolution and comparison boundary, so a
preflight result is never authority by itself.

## 7. In-memory semantic reference

`qualificationinputauthority.MemoryStore` is a concurrency-safe semantic
reference and implements both the trusted upstream resolver and immutable
Store. Tests install server-owned `ResolvedAuthorities`; the Store keeps its
own copy and compares it again under the same mutex used for append. Tests can
retire/current-switch a Policy between resolution and append and inject one
post-commit unknown outcome.

The memory implementation is not a production authority. In particular, its
mutex is not evidence of PostgreSQL session affinity, role separation,
transaction isolation, durable append-only enforcement, or restore safety.

## 8. Migration 000080 PostgreSQL shape

Migration `000080` must provide append-only tables for:

```text
qualification_input_precommit_authorities
qualification_input_precommit_identity_reservations
qualification_input_precommit_wia_reservations
qualification_input_precommit_plan_reservations
qualification_input_source_receipt_admissions
qualification_input_credential_receipt_admissions
qualification_input_precommit_executable_binding_generations
qualification_input_precommit_executable_binding_heads
```

Each receipt-admission table has a unique request hash, unique admission hash,
closed fixed kind, exact canonical admission bytes/JSON/hash, exact canonical
source-or-credential request bytes/JSON/hash, adapter identity, executable
digest, and external receipt hash. Deferred checks decode the request by kind
and prove that its verifier/resolver binding equals the admission binding. It
rejects mutation and cannot be written by the composition role. Its
kind-specific definer function accepts only a private verifier result and
implements first-commit-wins CAS on the request hash.
Resolve-by-admission-hash and resolve-by-request-hash are both required for
crash and concurrent-observation recovery.

The executable-binding generations are append-only reviewed deployment
history, scoped by the closed `source-verification` and
`credential-resolution` roles. A separate two-row binding-head table stores
the exact current generation, stable identity, and executable digest. Only the
owner-only review function may insert or advance a head, and it must advance
one contiguous generation using the exact previous digest. Admissions and
final Issue join and `FOR SHARE` the concrete head plus its history row. This
is intentionally not `MAX(generation)`: if a SERIALIZABLE transaction took an
old snapshot before a concurrent rotation, the changed head row produces a
serialization failure (or a missing-current fail-closed result) instead of
certifying the retired executable. A runtime argument or environment variable
is not this authority.

The authority row must retain every canonical byte sequence, JSONB projection,
domain hash, upstream identity/hash scalar, proof scalar, and the database
timestamp. Deferred closure triggers must independently decode/recompute all
documents and prove row/document/raw-byte/hash equality. The seven history and
authority tables reject `UPDATE`, `DELETE`, and `TRUNCATE`; the binding-head
table rejects removal and is mutable only by the owner review function.
Identity and single-use reservations are append-only and globally collision
checked where shared reservation ledgers exist. The frozen catalog footprint
is eight tables, 28 indexes, 24 routines, and eleven non-internal triggers
(seven immutable, one head no-removal, and three deferred closure triggers).

The atomic issue function must lock and validate, in this order:

```text
worksflow:workflow-input-authority-migration:v1 rollout fence
  -> inspect the exact immutable operation replay only
  -> resolve only the WIA-owned project locator without locking another plane
  -> projects(id) FOR UPDATE
  -> project-scoped input-precommit advisory mutex
  -> migration-78 transaction-bound current-WIA assertion
     (project idempotently, then run/node/upstream rows in platform order)
  -> current source/credential binding heads plus exact generation rows
  -> explicitly lock and revalidate the current Qualification Policy generation
  -> explicitly lock and revalidate the exact Plan
  -> immutable source-verifier receipt admission
  -> immutable credential-resolver receipt admission
  -> precommit reservations and authority append
```

The locator read is not authority and may supply only the project ID needed to
take the platform lock. The function must use migration `000078`'s WIA
assertion rather than independently taking Policy before WIA; a Policy→WIA
order could deadlock against the established project-first assertion path.
After that assertion it explicitly locks and revalidates the exact Policy and
Plan used by the candidate. It must compare their actual profile/input
documents, not accept the Go candidate as evidence of their contents. Policy
currentness is checked inside this transaction. Receipt hashes must resolve to
immutable terminal local admission records whose request hash equals the stored
source/credential request hash; those admission records in turn bind the
externally verified receipt hashes. SQL may not accept a candidate receipt
hash or an unpersisted verifier return value directly.

## 9. Role, session, ACL, and no-bypass posture

Migration owner owns tables and SECURITY DEFINER functions but is not a
runtime login. Each runtime identity is one restricted `LOGIN` with
`INHERIT`, belonging directly to exactly one matching `NOLOGIN` operator group
with `inherit_option=true`, `set_option=false`, and `admin_option=false`.
The input-precommit LOGIN/group may execute only issue, inspect-operation, and
resolve-exact-authority entrypoints. It receives no direct table/sequence
access and no generic WIA, Policy, Plan, or receipt resolver. The ordinary
application, workflow, Plan, Evidence, and Receipt roles receive no precommit
write capability; Promotion receives only the exact precommit resolver.
PUBLIC gets nothing. Every definer function requires the original LOGIN with
`current_setting('role') = 'none'`, uses a fixed trusted `search_path`, and
performs explicit row-count checks. A second membership, an additional member
of the operator group, `SET ROLE`, ownership, or elevated role attribute fails
closed.

The Source Verifier operator may execute only source admit,
inspect-by-request, and resolve-by-admission-hash; the Credential Resolver
operator has the same three capabilities only for credential admissions.
Neither may issue precommits or read/write the other kind.
The composition operator may resolve the admissions only through the atomic
issue function; it cannot insert or update them. This keeps a leaked
composition credential from manufacturing a verifier receipt.

`NewPostgresStore` requires three distinct `*sql.DB` pools authenticated as the
input-precommit, Source Verifier, and Credential Resolver LOGINs. Each pool
must be explicitly declared direct or session-pooled; unverified and
transaction-pooled configurations are rejected. Receipt recovery is
kind-aware and routes directly to one verifier pool. There is no generic
hash-only recovery that probes both DSNs or allows one role to observe the
other admission namespace.

Production issue and both admission appends use a dedicated physical
PostgreSQL session. Each acquires a domain-separated operation/request session
advisory fence before `BEGIN ISOLATION LEVEL SERIALIZABLE`, executes the
primary check plus capability routine on that same connection, commits, and
then unlocks. An unknown advisory-lock acquisition result, unknown `BEGIN`
result, unknown commit result, failed rollback acknowledgement, failed unlock,
or `pg_advisory_unlock = false` poisons and discards that physical connection.
Commit-unknown recovery always uses the deterministic operation/admission key
on a fresh connection; the uncertain session is never explicitly unlocked and
returned to the pool. Only a linear, concrete PostgreSQL `40001` or `40P01`
proves that the whole attempt aborted and permits a bounded retry; joined
transport/abort errors and commit-unknown are never retried. Exhausted
known-abort retries return `ErrRetryable`, distinct from outcome-unknown.

Inspect-operation, resolve-authority, both request/admission recovery paths,
and the database clock verify read-write-primary posture in the same SQL
statement as their lookup/time projection. A replica can therefore never turn
lag into a false not-found or recovery decision. Returned rows are not trusted
because a SECURITY DEFINER function produced them: Go scans retained canonical
bytes, JSONB projections, every duplicated scalar, and the millisecond
timestamp, strictly reconstructs the typed documents, compares JSONB back to
the retained bytes, and runs `ValidateRecord` or the receipt-admission closure.
Any missing, widened, unknown, drifted, or corrupt field fails closed.

The fake-driver suite deterministically covers direct/session-pool admission,
three-role routing, the explicit non-resolver preflight boundary, exact replay,
replica posture, corrupt retained raw bytes/JSONB/scalar/timestamp rows,
definite posture/query/commit retries, bounded exhaustion, ambiguous
begin/commit/rollback, advisory lock-acquire result loss, unlock error/false,
and bad-connection discard. The
real PostgreSQL 16 canary creates three restricted LOGINs, gives each exactly
one non-settable/non-admin membership in its matching stable NOLOGIN group,
uses the actual migration-80 routines for both admission/recovery paths,
proves the input role reaches Issue without upstream table grants, and proves
direct table reads plus cross-verifier routine execution return `42501`.

Production enablement requires all of these canaries:

- exact canonical Go/PostgreSQL golden-vector parity;
- duplicate/unknown/null/widened/secret JSON rejection;
- current, superseded, suspended, wrong-WIA, wrong-Policy, wrong-Plan, source
  drift, credential drift, reviewed-executable drift, and role-alias rejection
  before callbacks;
- old-snapshot and lock-held generation-rotation races, proving a retired
  source/credential executable can never append an admission or authority;
- rollout-fence/project-first lock-order concurrency against migration-78 WIA
  assertion, Policy issue/supersession, Plan freeze, and migration-81 Promotion,
  with no Policy→WIA or Plan→project inversion;
- concurrent exact issue, changed replay, WIA/Plan single use, shared UUID
  collision, and post-commit recovery;
- direct `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, table read, routine execute,
  search-path shadow, SET ROLE, and transaction-pool bypass denial;
- lock-acquire result loss, commit result loss, unlock false/error, and
  connection discard verification;
- partial-admission crash recovery, concurrent different-observation
  first-commit-wins, and rejection of an existing/concurrent winner from a
  different authority identity or executable digest;
- empty and non-empty rollback canaries; and
- one production-role no-trigger-bypass fixture from current Policy + WIA +
  Plan + two immutable verification receipts through exact resolve.

Only after these pass may migration `000081` Promotion require and atomically
consume the exact input-precommit ID/hash/full binding alongside WIA, Policy,
Plan, Evidence, and Receipt.

## 10. Direct Promotion canonical binding

This authority is not rigorously represented as an ordinary current Policy v1
`independentRequirements` member. That list currently names different fixed
posture kinds and binds an exact opaque ID/hash when Policy is issued. An input
precommit is generated later for one WIA+Plan pair, so Policy cannot know its
future ID/hash. Treating it as another optional policy member would either
invent that identity early or weaken the exact-binding rule.

Promotion must instead impose it as one fixed, mandatory lineage authority.
The authoritative lookup key is the exact `(workflowInputAuthorityId,
qualificationPlanAuthorityId)` pair; SQL uniqueness additionally makes each
WIA and each Plan single-use. The locked record must bind the same current
Policy ID/hash that Promotion already resolved. Zero matches, multiple
matches, a stale Policy, or any scalar/raw-byte/hash disagreement fails closed.

`qualificationinputauthority.PromotionBindingFromRecord` produces the closed
projection migration `000081` Promotion must place inside its canonical
closure bytes and closure hash:

```text
kind = qualification-input-precommit
authorityId, authorityHash
workflowInputAuthorityId, workflowInputAuthorityHash
qualificationPolicyAuthorityId, qualificationPolicyAuthorityHash
qualificationPlanAuthorityId, qualificationPlanAuthorityHash
sourceRequestHash, sourceReceiptHash, sourceAdmissionHash
credentialRequestHash, credentialReceiptHash, credentialAdmissionHash
```

The migration-81 Promotion wire has one required typed `inputPrecommit` member
with exactly that projection, separate from policy-configured ModelProfile and
PostgreSQL posture admissions. If an implementation reuses generic
independent-admission machinery internally, it is rigorous only when all of
the following remain true:

- `qualification-input-precommit` is a fixed system-required kind, not an
  optional or caller/policy-selected requirement;
- a kind-specific admission is uniquely derived from and locks the exact
  WIA+Plan pair;
- its canonical `sourceLinkage` contains the complete `PromotionBinding` above;
- the Promotion closure binds both the full typed projection and, if present,
  its admission-record hash; and
- consume re-resolves and compares the admission, precommit, two local receipt
  admissions, WIA, current Policy, and Plan in one transaction.

The current generic projection alone is insufficient: its opaque
authority/admission/source-receipt fields do not visibly close both source and
credential edges or establish WIA+Plan uniqueness. A junction row, foreign
key, preflight query, or trigger that is not represented in the canonical
closure is also insufficient because replay and Receipt inspection would not
authenticate the dependency. Migration `000081` freezes the closure only with
this member present. No pre-member Promotion-v2 bytes may be relabeled as that
contract; any already persisted incompatible experiment requires an explicit
versioned migration, not silent reinterpretation.

## 11. Fixed rollout and acceptance order

The production order is fixed and may not be collapsed into one unreviewed
activation:

```text
000080 Qualification Input Precommit Authority
  -> SQL/role/admission/session-affinity/no-bypass canaries pass
  -> 000081 Promotion v2 consumes one full typed inputPrecommit binding
  -> Promotion remains runtime-disabled until its updated no-bypass canary passes
  -> 000082 private Handoff consumes one pending Promotion result
```

Migration `000080` is complete as a production prerequisite only when its
append-only SQL authority, distinct source/credential admission roles,
session-affine adapter, exact ACL posture, canonical Go/PostgreSQL parity,
concurrency/crash recovery, rollback guards, and real production-role
no-bypass canary all pass. The in-memory reference alone does not satisfy this
criterion.

Acceptance must also prove forward composition: migration `000081` rejects a
missing, duplicate, stale, aliased, reduced, or byte/hash-mismatched precommit
and retains the complete `PromotionBinding` in canonical closure bytes.
Migration `000082` may consume only a pending result from that updated
Promotion contract. Passing migration `000080` never authorizes Promotion or
Handoff by itself.
