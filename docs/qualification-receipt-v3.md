# Snapshot-first Qualification Receipt v3

Status: strict internal canonical-payload, keyful-verification, and
request/observation/completion control contract with a concurrency-safe memory
reference Store. No PostgreSQL signing operator, production snapshot builder,
real signer, external run, promotion approval, immutable revision, or workflow
submission is claimed.

`backend/internal/qualificationreceiptv3` defines the next qualification
Receipt without reinterpreting the repository's existing wire v2. Existing
`worksflow-qualification-receipt/v2` documents seal a Receipt inside the
snapshot and remain historical, read-only evidence. The incompatible
snapshot-first protocol therefore uses:

```text
worksflow-qualification-receipt/v3
https://worksflow.dev/attestations/qualification-receipt/v3
application/vnd.in-toto+json
```

Using a new wire version is a security boundary, not a cosmetic rename. A v2
verifier must reject v3 and a v3 verifier must reject v2; neither format may be
silently upgraded, projected, or consumed through the other's promotion
ledger.

## Acyclic evidence order

The new Receipt is compiled only after an immutable pre-Receipt snapshot has
been sealed and independently reopened:

```text
exact Artifact Index
  -> persist a seal request with no Receipt payload, then seal pre-Receipt snapshot
  -> persist a verify request with no Receipt payload, then independently reopen
  -> compile exact canonical in-toto payload
  -> durably record both signer requests before either signer call
  -> obtain runner and independent approver signatures over identical DSSE PAE
  -> assemble and independently verify the canonical DSSE envelope
  -> persist one terminal Receipt v3
  -> separately consume it through Promotion v2
```

The in-toto subject is exactly the pre-Receipt snapshot ID and SHA-256 digest.
The snapshot type has no Receipt ID, Receipt digest, payload, envelope, or
signature field. This prevents the old `Receipt -> snapshot -> Receipt` hash
cycle. A later outer distribution bundle may contain both snapshot and
Receipt, but the signed payload cannot point back to that bundle.

The immutable seal request likewise has no snapshot digest, Receipt ID,
payload, or PAE. The verification request learns only the committed seal
result and snapshot digest; it still has no Receipt payload or PAE. The exact
Receipt is compiled only after both committed result documents are durable.

## Exact signed closure

The canonical predicate binds:

- the frozen Plan Authority ID/hash/artifact, input/projection/Evidence Plan,
  target, trust, and direct TrustBindings digests;
- the complete typed Evidence Plan and its independently recomputed hash;
- exact project/workflow/node/revision target and clean source tree;
- QualificationManifest artifact/revision/content hash, BuildManifest,
  BuildContract, reviewed TemplateRelease, and Golden runtime bindings;
- the precommitted CredentialSet plus exact issuance/revocation commitments;
- the closed evidence artifact set, restricted subset, per-artifact content
  digests, encryption/KMS commitments, and exact Artifact Index;
- the sealed snapshot and independent verification result; and
- exactly one qualification runner and one release approver using distinct
  identities, key IDs, and Ed25519 public keys.

`PayloadDigest` is SHA-256 of the decoded exact canonical in-toto payload
bytes. It is not the manifest projection hash, predicate hash, PlanDigest, or a
caller-provided label. The canonical DSSE envelope uses strict standard base64,
DSSE PAE, and deterministic key-ID ordering; its digest is a separate domain.

## Server-owned expectation and key policy

The verifier accepts only opaque Plan Authority and Receipt IDs. A startup-
installed `ExpectedResolver` must load the exact canonical expected payload
from trusted immutable storage; the wire payload cannot be supplied back as
its own expected value. Resolver identity drift, non-canonical bytes, payload
hash drift, unknown fields, duplicate JSON names, or any signed-but-unexpected
binding fails closed.

The keyful Receipt trust policy has its own canonical document and digest,
covering both public keys, key IDs, identities, algorithms, and roles. That
digest must exactly equal the trust-policy digest frozen by the resolved Plan
Authority. A valid signature under a different key policy is not accepted.

Some upstream facts cannot be derived from a one-way `InputHash`. In
particular, BuildManifest, BuildContract, QualificationManifest, source,
TemplateRelease, and Golden values are authorized by the immutable resolver,
not by repeating self-consistent labels inside a signed document. A future
Promotion v2 transaction must lock the Plan Authority's exact input bytes and
the Evidence ledger and compare these fields again before consumption.

## Time and started-before-call semantics

`qualificationStartedAt` is the target qualification-run start. `completedAt`
follows independent snapshot verification, and `issuedAt` is the frozen
Receipt issue time. None of these fields proves that an external signer call
was recorded before invocation.

The package now defines a separate append-only request/observation/completion
control contract and a concurrency-safe memory reference Store. Production
signing still needs its owner-only PostgreSQL implementation. That Store must
atomically persist both role-separated signer requests before either network
call, assign trusted database time, retain exact request/payload/PAE bytes and
hashes, and reconcile commit-unknown outcomes by exact inspection. After a
started record exists, recovery may use only authenticated `Inspect`. An
ordinary not-found response is not proof of `not-invoked`; safe resumption
requires the exact signed claim/acknowledgement closure implemented by the
control contract.

An authority-signed `observedAt` is evidence supplied by that authority, not
the ledger clock. Every durable observation also needs a Store-assigned UTC
millisecond `recordedAt`; completion must be later than all four recorded
source observations. Exact replay keeps the original recorded time and an
ambiguous append cannot manufacture a new one.

The trusted observation resolver authenticates the operational authority and
returns both the exact typed payload and its Ed25519 proof envelope. The
control service independently checks their canonical byte/hash/base64/role
closure and retains both documents; it does not treat a browser-supplied proof
or an unauthenticated resolver as an authority.

The frozen Evidence Plan v1 deliberately owns only `snapshotSeal` and
`receiptSign` operation UUIDs. Receipt v3 does not invent a third UUID outside
that reservation: seal and independent verify share the reserved snapshot
operation, while runner and approver share the reserved signing operation.
Their immutable identities are the closed composite `(operation ID, request
kind, authority role, request hash)`. A committed seal/verify observation must
retain the exact result document, raw bytes, and digest; a committed signer
observation retains the exact signature bytes and digest. Only an
authenticated `not-invoked` observation whose one-use claim and acknowledgement
bind the exact request and generation may advance to another generation. The
next generation must itself append an authenticated one-use pending claim
before call ownership is returned. A commit-unknown, conflicting append, or
replayed pending claim never grants call ownership.

## Non-authorization and remaining production work

A verified Receipt v3 proves only that two configured signers signed the exact
server-resolved payload and that the payload is structurally/hash closed. It
does not itself prove that a real signer HSM, snapshot filesystem, capture
runtime, Broker, KMS, or target deployment has been provisioned.

Before workflow mutation, a separate Promotion v2 ledger must atomically lock
and revalidate the exact Plan Authority input, Evidence head and artifact
closure, snapshot/signing observations, Receipt bytes, and current canonical
review/upstream-revision state. It may then create only a pending immutable-
revision handoff. Creation of that revision and exact node submission remain a
separate workflow transaction.

Until the durable Store, real adapters, target run, independent verification,
single-use consumption, and workflow handoff all exist and produce external
evidence, qualification remains blocked and the manifest must continue to
report zero externally qualified suites.
