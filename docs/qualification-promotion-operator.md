# External Qualification Receipt and Promotion Evidence

Status: implemented-internal verifier and atomic consumption contract; no
external qualification has been executed, no pending handoff has been consumed
by the workflow service, and no stage-exit decision has been issued.

Compatibility note: `backend/internal/qualificationreceipt` and migration
`000071` implement the historical wire-v2 Receipt/Promotion path. Its
Receipt-before-snapshot order is read-only history. Every new qualification
write must use the snapshot-first wire v3 contract in
`qualification-receipt-v3.md`; a later Promotion consumption v2 must bind that
exact Plan/Evidence/Receipt v3 state and must not reinterpret the historical
Promotion Authority v2 as the new capability.

This document defines the trusted handoff between an external qualification
run and the service that may complete an external-qualification gate. It does
not define model-profile governance approval, production PostgreSQL identity
qualification, or a general deployment approval. Those remain independent
authorities and must be composed by the workflow gate that owns the final
node transition.

## 1. Security boundary

The qualification runner is not a promotion authority. It may execute tests
and assemble evidence, but it must not choose any expected identity that the
verifier treats as trusted. The historical wire-v2 verifier accepts expected
facts from a root-owned `worksflow-qualification-promotion-authority/v2`
document only for read-only verification. The wire-v3 verifier instead accepts
opaque Plan Authority/Receipt IDs and resolves exact expected payload bytes
through a server-installed immutable resolver.

The verifier produces `external-qualification-only` evidence. Its output is
not itself a workflow capability and must not be accepted from an arbitrary
client. `promotionTarget` contains the exact project, workflow run, node key,
target revision ID/hash, manifest subject, and stage gate. The new downstream
gate must execute the keyful verifier in its own trusted environment and
atomically consume the exact `(Plan Authority, Evidence head, Receipt v3,
promotionTarget)` closure in an append-only ledger/CAS transaction. A
stateless CLI cannot truthfully guarantee one-time consumption.

The final workflow transition therefore has three different inputs:

1. the signed external qualification receipt described here;
2. any independent governance or target-environment receipts required by the
   node, including ModelProfile and production PostgreSQL posture; and
3. the workflow's current exact upstream revision and canonical review facts.

Missing or stale input is a blocking gate, not a warning and not a reason to
reuse an older receipt.

## 2. Immutable inputs

Two independently identified snapshots are required.

### 2.1 Repository snapshot

The repository root must be an intrinsically immutable SquashFS or EROFS
filesystem. A read-only bind mount over a writable filesystem is insufficient.
The authority pins:

- canonical repository root and qualification manifest path;
- repository snapshot ID and immutable snapshot mode;
- exact Git commit, tracked path/mode/object closure, and an independent
  SHA-256 content-tree commitment over canonical path, mode, size, and the
  SHA-256 digest of each actual tracked file;
- a clean, fully closed worktree;
- digest of the trusted `/usr/bin/git` executable;
- qualification plan, pre-promotion manifest, source-policy tool, and
  source-policy attestation digests; and
- digest of the qualification verifier executable itself.

Repository verification rejects `.git` indirection, alternates, replace refs,
external object stores, symlinked metadata, untracked source files, changed
tracked bytes or modes, and a commit/tree mismatch. The exact source tree is
checked before and after evidence verification.

`qualification/manifest.json` is a pre-promotion plan. External suites must
remain `not-qualified` while evidence is being checked. Status fields and
receipt pointers do not alter the plan digest; executable commands, test paths,
required artifacts, policies, source documents, support files, and acceptance
mapping do. Every documented AIC/FQP/LSP acceptance ID must be mapped exactly
once.

The reviewed inventory is `worksflow-qualification-test-inventory/v2`. The
manifest's suite-level `criterionSource` is the independent hash-bound
authority for the required support-listed acceptance-criteria document;
inventory `criterionSources` must match it exactly. Every case carries both
`requirementIds` and `contractCriterionIds`. Before an external-complete run, the inventory checker
must prove complete closure of the suite's paths, top-level requirements, and
all `AC-*` entries in that document. The normalized Playwright artifact repeats
those exact arrays and the receipt verifier rejects any mismatch.

### 2.2 Evidence snapshot

Qualification writes first to a private mutable staging area. Before
verification, the pipeline must:

1. obtain a signed, run-scoped atomic credential-set issuance whose exact
   member digest and count match the root-owned Golden fixture;
2. execute the qualification and normalize the zero-mock/zero-skip
   Playwright result;
3. encrypt every credential-bearing or unstructured artifact through an
   approved KMS recipient;
4. prove plaintext was either never persisted or was deleted;
5. obtain the signed KMS encryption attestation;
6. atomically revoke the same credential set and obtain the signed revocation
   receipt for the identical member digest and count;
7. create the exact artifact index, including both credential-set receipts;
8. persist a seal request that contains no Receipt payload, then seal the index
   and indexed artifacts into one new SquashFS or EROFS pre-Receipt snapshot;
9. persist a separate verification request, independently reopen that exact
   snapshot, and retain the canonical verification result; and
10. only then compile and sign a wire-v3 Receipt whose sole in-toto subject is
    that verified snapshot, using independent runner and release-approver
    identities.

The server-owned Plan Authority pins the target, evidence plan, planned
snapshot/index identities, trust, and immutable input closure. The snapshot
verifier rejects symlinks, devices, sockets, unknown files, missing files,
duplicate paths, hard-linked artifacts, mutable filesystems, or any file not
closed by the exact index/artifact allowlist. The later Receipt is retained in
the durable control ledger and may be packaged beside—but never inside the
hash input of—the pre-Receipt snapshot. This prevents both hidden plaintext and
a Receipt/snapshot hash cycle.

### 2.3 Golden runtime and credential-set binding

A Golden run uses several independent browser and API credentials. A singular
credential handle cannot prove that all of them were revoked. The promotion
authority and qualification receipt must therefore bind one atomic,
run-scoped credential set by:

- the Golden authority document digest, Golden fixture document digest, and
  fixture ID;
- issuer, audience, opaque credential-set handle hash, exact member-bindings
  digest, and member count;
- a signed issuance predicate and signed revocation predicate containing the
  same sorted member list; and
- the immutable artifact IDs and payload digests of both predicates.

Each member binds a non-secret slot, principal actor, credential kind, and
one-way credential handle hash. Raw tokens, cookies, storage state, and secret
broker material must never enter the fixture, predicates, receipt, artifact
index, logs, traces, screenshots, or video. Issuance and revocation are atomic
operations over the entire set: partial issuance, partial revocation, a
changed member list, duplicate member, reused handle, or count/digest mismatch
fails closed. The short-lived credential set is root-issued before the run,
may therefore predate the later two-phase Promotion Authority, must cover the
complete run through revocation, must not live longer than 30 minutes, and is
not reusable by another run. The later Promotion Authority binds the exact
already-issued set and its immutable issuance/revocation evidence; it does not
retroactively redefine the set's lifetime.

## 3. Evidence classification

Only these strictly parsed normalized documents may be distributable:

- the Playwright qualification result;
- the credential-set issuance and credential-set revocation DSSE receipts;
- the non-secret Golden Authority and Golden Fixture documents whose actual
  bytes are pinned by the Promotion Authority;
- each Fixture-pinned canonical direct-DSSE Golden fault authority, each
  canonical plain consume receipt identified by its `resultId`, and the one
  canonical run-level fault ledger DSSE attestation;
- the v6-to-v7 writer-drain proof; and
- the KMS encryption attestation.

Credential-set predicates and Golden documents may contain only the canonical
non-secret identities defined in section 2.3. If any contains a raw token,
cookie, storage state, authorization header, secret path, or provider secret,
the run is invalid; it must not be made acceptable by encrypting that control
document and hiding it from strict verification.

Browser trace, video, logs, screenshots, provider transcripts, generic
evidence, and any other unstructured artifact are
`restricted-encrypted`. Their artifact descriptors must use
`AES-256-GCM+KMS/v1`, an approved key resource/version, a unique wrapped data
key, canonical nonce/tag values, and AAD binding the run, plan, artifact ID,
path, and exact TemplateRelease. The signed KMS attestation binds the
ciphertext digest, wrapped-key digest, descriptor digest, encryption time, and
plaintext disposition.

Encryption metadata does not make an artifact safe by assertion. The verifier
requires the KMS signer threshold and key validity window from the root-owned
trust policy. Runner, approver, credential issuer, KMS authority, fault
operator, and fault-ledger-attestor key IDs, identities, and public-key
fingerprints must all be globally independent; reuse across roles is rejected.

## 4. Required chronology

All timestamps use exact UTC ISO-8601 values. The required order is:

```text
v6-to-v7 writer drain completed
  < atomic credential set issued
  < qualification started
  < qualification completed
  < complete terminal fault ledger attestation issued
  < every restricted artifact encrypted
  <= plaintext disposition completed
  < KMS encryption attestation issued
  < the identical atomic credential set revoked
  < exact artifact index created
  <= pre-Receipt snapshot sealed
  <= pre-Receipt snapshot independently verified
  < qualification receipt issued
  <= Promotion v2 consumed
```

The root authority has a positive validity interval no longer than 15 minutes.
The signed receipt binds its target, nonce, and authority expiry. Verification
uses trusted service time, not a runner-provided clock. Credential lifetime is
bounded, revocation is signed by an approved issuer, and all receipt,
credential, KMS, fault operator, fault-ledger-attestor, runner, and approver
signing keys must be valid and unrevoked at their independently bound times.
Short-lived fault authorities are verified at the ledger-attested historical
`reservedAt`, not at the later promotion-verification time. A reservation at or
after authority expiry still fails closed.

The complete Golden run uses the canonical 13-operation set in
`qualification/golden-fault-operation-set.json`. Its canonical JSON digest is
root-bound as `goldenRuntime.faultOperationSetDigest` in both the Promotion
Authority and Receipt; a Fixture cannot choose a smaller proof obligation.
Authority, receipt, and ledger cardinality must match that exact set, and every
`adapterInvocationId` must be unique across the run.

The fault-ledger attestation's own `issuedAt` is a signed assertion, not an
external timestamp. The verifier therefore requires every attestor key to be
valid and unrevoked both at that asserted time and through the independently
root-fixed, runner/approver-signed trusted Receipt `issuedAt`. This prevents a
key revoked or expired before Receipt issuance from minting a newly backdated
ledger. Historical `reservedAt` remains a trust statement by the independent
attestor; deployments that require cryptographic non-backdating independent of
that role must add a transparency or trusted-timestamp service.

An expired authority, an old receipt presented under a new target, or a nonce
already consumed for the same target must fail closed. Retrying after an
uncertain downstream commit uses the same target, nonce, and authority digest
to query the consumption ledger; it does not issue a second promotion.

The new path uses an immutable Plan Authority before execution and an acyclic
durable control ledger after indexing. It persists seal and verify requests and
their authenticated observations first; only then does it atomically persist
both signer requests over identical payload/PAE bytes. A crash is reconciled by
the same request hash and generation. A new generation is permitted only after
an authenticated one-use claim/ACK proves `not-invoked` and the next pending
claim is durably appended before call ownership is returned.

After the canonical DSSE envelope is independently verified and one terminal
Receipt v3 is stored, Promotion consumption v2 must lock and revalidate the
exact Plan input bytes, Evidence ledger/head, four control observations,
Receipt bytes, current target revision, canonical review, and workflow
upstream state in the same consumption transaction. It creates only a pending
immutable-revision handoff; it neither reseals the evidence nor embeds a
post-Receipt authority back into the signed snapshot.

The new consumer is specified separately in
`docs/qualification-promotion-v2.md`. In particular, that contract preserves
the historical `000071` ledger, defines the project-first atomic composition,
and explains why the current unsigned, multi-snapshot PostgreSQL posture
diagnostic cannot be relabeled as an independent promotion authority.

## 5. Trust policy

The root-owned trust policy contains:

- threshold DSSE keys for the qualification runner and release approver;
- per-key validity and revocation windows;
- credential issuer keys, identities, and signature threshold;
- approved KMS recipients;
- evidence-encryption authority keys, identities, and threshold;
- fault-operator authority keys, identities, and threshold;
- independent fault-ledger-attestor keys, identities, and threshold; and
- maximum receipt age and future-clock skew.

The required document schema is
`worksflow-qualification-trust-policy/v2`. Every allowed identity set must
exactly match its configured keys. Key IDs, identities, and public keys are
non-reusable across every trust section.

The authority pins the exact trust-policy path and digest. Files and every
ancestor directory must be owned by uid 0, must not be symlinks, must have one
hard link, and must not be writable by group or other. Rotating a trust policy
requires a new authority and receipt; it must never reinterpret an existing
receipt under a different key set.

## 6. Historical wire-v2 verifier invocation

This section documents the existing read-only-compatible binary and v1
consumer. It is not the invocation contract for new snapshot-first runs.

The internal verifier is built from:

```text
backend/cmd/qualification-receipt
backend/internal/qualificationreceipt
```

The promotion service invokes the exact digest-pinned binary with paths that
must equal the root authority:

```sh
qualification-receipt \
  -repository-root /immutable/source \
  -qualification-manifest qualification/manifest.json \
  -receipt /immutable/evidence/meta/qualification-receipt.dsse.json \
  -artifact-index /immutable/evidence/meta/artifact-index.json \
  -artifact-root /immutable/evidence/artifacts \
  -promotion-authority /run/worksflow/qualification/promotion-authority.json
```

The CLI deliberately does not accept expected run, source, TemplateRelease,
BuildContract, trust, target, nonce, or signing identities as ordinary flags or
environment variables. A successful invocation emits a verified external-only
projection containing the exact target, nonce, authority digest, plan, artifact
and receipt digests, signer identities, issuance time, and the required
`downstream-append-only-ledger-cas-required` consumption policy.

The historical internal promotion service is implemented in
`backend/internal/qualificationpromotion`; migration `000071` installs its
append-only PostgreSQL ledger and dedicated operator boundary. Its public
consume command accepts only four opaque UUIDs: operation, authority, handoff,
and preallocated output-revision IDs. A trusted server-side resolver supplies
the authority document, immutable evidence paths, exact target, and expected
promotion; a trusted verifier must produce the full `VerifiedPromotion` before
the private store command can be constructed. Clients cannot assert verified
bytes, evidence paths, target fields, or expected identities.

The PostgreSQL routine atomically inserts the canonical request bytes, complete
verified projection, globally single-use nonce, and a hash-bound `pending`
handoff. Duplicate exact operation replay is an idempotent immutable read, even
after authority expiry or evidence retirement; changing any of the four IDs is
a conflict, and an unused expired authority cannot be consumed. A commit-unknown
result is recovered by inspecting the exact operation rather than issuing a
new nonce.

Rollback is equally fail closed. `000071.down` first takes `ACCESS EXCLUSIVE`
on both consumption and handoff ledgers, then rechecks that both are empty.
This prevents a concurrent consume from committing between a clean read and
`DROP`; any immutable consumption or pending handoff permanently blocks that
rollback.

The `pending` handoff is deliberately honest: it preallocates and binds the
intended output revision, but is neither an immutable revision nor proof of
workflow submission. A separate workflow-side consumer must still, in one
transaction, lock and re-resolve the exact project/run/node, current upstream
revision and content hash, canonical review, and every independent gate; create
the exact promotion-only immutable revision; submit the node under CAS; and
record terminal handoff consumption. That consumer is not implemented by
`000071`. New Receipt v3 state must not be projected into this v1 consumer; it
requires the separate Promotion consumption v2 transaction described above.

## 7. Failure and recovery

| Failure | Required action |
|---|---|
| Repository, manifest, source policy, Git, or verifier digest drift | Reject; build a new immutable source snapshot and authority. |
| Evidence file missing, extra, mutable, symlinked, or hash-mismatched | Reject; never patch a sealed snapshot. Produce a new run. |
| Any failed, skipped, flaky, retried, or mocked qualification case | Reject; no qualified receipt may be issued. |
| Credential set not wholly revoked or plaintext disposition incomplete | Reject and quarantine evidence; complete incident handling before a new run. |
| Signature/key/role threshold invalid | Reject; do not substitute a runner signature for approver authority. |
| Authority expired before consumption | Reject; verify a new run under a new nonce/authority. |
| Downstream response lost after commit | Query the append-only ledger with the same target, nonce, and authority digest. Do not resubmit with a new nonce. |
| ModelProfile or production identity receipt absent | Keep the overall workflow node blocked even if external Golden evidence is valid. |

## 8. Current implementation status

The repository contains the strict plan parser, cross-language digest vectors,
immutable source/evidence checks, artifact closure, DSSE threshold verifier,
KMS and credential evidence verification, exact Golden fault
authority/consume-receipt/run-ledger closure, and external-only CLI projection.
It also contains an independent fail-closed production PostgreSQL posture
checker for five distinct LOGINs: application, migrator, catalog-only auditor,
qualification-promotion operator, and qualification-policy operator. The
checker enforces a six-group `NOLOGIN` boundary. After the Promotion-v2 schema
migration, the promotion identity has no table or column access and exactly the
Promotion-v2 consume, operation-inspect, and historical-v1-inspect executions;
handoff resolve/assert remains outside that identity. Its five catalog queries
use separate PostgreSQL snapshots within one bounded check window, so its safe
JSON is not an atomic cross-identity Receipt and explicitly excludes
qualification, promotion and GC-scheduler claims. It additionally requires a
direct or session-pool Promotion affinity declaration and the fail-closed
`disabled-pending-input-precommit-authority-canary` runtime gate. These are
repository-internal implementation facts and do not enable a worker.

The following external facts are still absent:

- an approved Golden TemplateRelease and executable full-stack endpoint;
- complete Sandbox, Agent, Reference AI, Release, and LSP Golden cases;
- a live Provider and registry-approved Agent Runner;
- deployed Registry/KMS/Secret Broker/Release Controller/target cluster;
- a target-environment production PostgreSQL identity result plus the separate
  GC scheduler recovery qualification and its external composition evidence;
- ModelProfile conformance and activation evidence;
- a real evidence capture, encryption, signing, sealing, and an externally
  evidenced execution of the internal single-consumption service;
- a workflow-side consumer that turns the exact `pending` handoff into one
  immutable promotion-only revision and atomically submits the target node
  after rechecking canonical review and every independent gate;
- real fault adapters and an external fault-ledger-attestor/orchestrator run.

Until those facts exist and the independent gates are composed, the correct
status is `implemented-internal`, not `production-qualified` and not a closed
workflow node.
