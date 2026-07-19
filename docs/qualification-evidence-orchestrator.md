# Qualification evidence lifecycle orchestrator

Status: strict internal control contract; no external qualification run or
production authority integration is claimed.

`backend/internal/qualificationevidence` coordinates the evidence-producing
steps that must occur before an external qualification Receipt can be consumed.
It does not implement a credential broker, target capture service, KMS,
Receipt signer, immutable filesystem builder, or production evidence verifier.
Those authorities are narrow interfaces injected once when the service starts.

## Current v1 internal order

The currently implemented Evidence v1 state machine has one successful order:

```text
reserve exact non-secret plan
  -> atomically issue the complete credential set
  -> accept the exact run-result/capture closure
  -> encrypt every restricted artifact and dispose its plaintext
  -> obtain the KMS attestation
  -> atomically revoke the exact issued credential set
  -> build the exact artifact index
  -> sign the qualification Receipt with two independent signers
  -> seal the immutable evidence snapshot
  -> reopen and verify that snapshot through a read-only verifier
```

That `Receipt -> snapshot` tail is retained only for strict v1 replay and
history. It must not be reused for new external qualification because a Receipt
that is also expected to bind its final containing snapshot creates a hash
cycle. The new wire-v3 contract in `qualification-receipt-v3.md` instead uses:

```text
artifact index
  -> seal a pre-Receipt snapshot that cannot contain a Receipt
  -> independently reopen and verify that snapshot
  -> compile the exact Receipt v3 payload whose sole subject is that snapshot
  -> durably start both signer requests
  -> runner + independent approver sign identical DSSE PAE
  -> independently verify and persist the terminal canonical envelope
```

Until that durable v3 control plane is connected, Evidence v1 `complete` is an
internal regression state only and cannot be promoted as a new external
Receipt.

Every mutating call is preceded by a durable `*-started` event. Once that event
exists, recovery calls only the authority's `Inspect*` method using the same
operation UUID and exact canonical request digest. A mutation is never repeated
after an ambiguous result. An ambiguous Store response is reconciled by a
strongly consistent `Load` and the exact event UUID; a different payload using
the same event UUID is an idempotency conflict.

The snapshot verifier is a deliberate exception: its interface contract is
strictly read-only. It may reopen and hash an immutable snapshot, but may not
sign, seal, publish, or mutate state. It is therefore safe to retry without a
verification-start event. An adapter that cannot honor this read-only contract
must instead be redesigned as a mutating started/Inspect boundary.

## Recovery and liveness limits

Append-before-call prevents an unrecorded side effect, but creates an honest
crash boundary. If the process stops after writing `started` and before invoking
the authority, the next process must call `Inspect`; it must not guess that the
call never happened. If that authority cannot durably report a `not-invoked`
outcome and safely resume the same operation, the orchestration remains
permanently fail-closed. This package does not claim liveness across that gap.

There is also no durable abort-and-revoke branch in this internal vertical yet.
If capture, encryption, KMS, indexing, signing, or sealing is terminally rejected
after credentials were issued, the state cannot become `complete` and no
Receipt or snapshot is produced, but the credential set relies on its short TTL
or an external emergency revoker. Production use requires a reviewed durable
abort-revocation started/Inspect path that revokes the exact issued set and
terminates without signing a qualification Receipt.

The eight plan operation UUIDs are pairwise distinct and cannot collide with
the orchestration, run, fixture, credential-set, or per-artifact encryption
UUIDs. The Memory Store remains a test implementation and enforces identity
only within one orchestration. Migration `000073` and `PostgresStore` reserve
every fixed and per-artifact operation UUID globally in one transaction, retain
canonical request/event bytes plus their independently recomputed hashes, use
database-authoritative millisecond event time, and replay the immutable ledger
instead of trusting the guarded head projection.

The PostgreSQL entry point is deliberately migration-owner-only: no API,
application, auditor, or operator role can execute it or access its four tables.
The supported writer is `PostgresStore`, which produces canonical bytes and
performs a byte-exact replay before committing. A migration owner is already a
fully trusted schema principal that can alter or drop these objects; direct
owner calls are not treated as an adversarial production API. Granting a future
operator `EXECUTE` requires a separately reviewed database-side canonical-byte
validator and a dedicated credential/DSN posture first.

## Hash and evidence closure

All persisted values are UUIDs, stable IDs, identities, counts, timestamps,
one-way hashes, or closed-enum evidence commitments. Events have no generic
metadata/error field and no token, cookie, authorization header, storage state,
wrapped key bytes, private key, filesystem path, or authority response body.
External errors are mapped to stable package errors rather than persisted.

The public `Execute` boundary accepts only an opaque canonical Plan Authority
UUID. It resolves that ID through a server-installed authority, re-canonicalizes
the returned Evidence Plan, and compares its exact bytes, hash, deterministic
artifact ID, authority identity, and direct TrustBindings digest before the
first reservation. Callers can no longer submit source, TemplateRelease,
credential, artifact, output, KMS, trust, or Plan material at this boundary.
Migration `000074` and `qualificationplanauthority.PostgresStore` durably retain
the complete server-resolved input/projection/trust/target/envelope closure.
This is still only an internal authority path: no production InputAuthority,
least-privilege Plan operator/login/DSN, API wiring, or target environment run
is claimed.

Restricted-artifact encryption binds and rechecks the exact capture digest,
plaintext digest, ciphertext digest, recipient, descriptor digest, wrapped-key
digest, and AAD digest. Plaintext disposition is `never-persisted` at the exact
encryption time or `deleted` strictly after encryption. KMS signs two different
commitments:

- the full encryption manifest, including operation/authority/time/disposition;
- a non-circular pre-revocation artifact-set projection binding credential
  issuance, capture, and exact restricted plaintext-to-ciphertext mappings.

The projections must not be equal or swapped. KMS precedes exact credential
revocation and revocation precedes the artifact index. In the historical v1
tail, the index digest is the Receipt subject and Receipt precedes sealing. New
qualification must use v3 instead: the pre-Receipt snapshot binds the index and
evidence closure, independent verification repeats those commitments, and the
Receipt's sole subject is the already sealed snapshot. Each terminal
observation timestamp is also bounded above by trusted Store time, so future-
dated completion evidence cannot enter the ledger.

## What `complete` means

`complete` means only that the injected internal authorities returned one exact,
hash-closed lifecycle and the final injected verifier repeated the immutable
snapshot commitments. It is not a production qualification decision, workflow
approval, immutable revision, or node submission. Real broker delivery/claim,
capture, KMS/HSM keys, dual Receipt signers, snapshot filesystem, dedicated
Plan/Evidence operators, a production immutable input resolver, abort revoker,
and a target external run remain outside this package and must be independently
provisioned and evidenced.
