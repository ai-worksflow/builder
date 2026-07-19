# Immutable qualification plan authority

Status: strict internal server contract, in-memory semantic reference, and
owner-only durable PostgreSQL Store; no browser endpoint, production input
authority/operator, qualification run, Receipt, or promotion approval is
claimed.

`backend/internal/qualificationplanauthority` removes one unsafe degree of
freedom from the evidence lifecycle: a caller can no longer submit a complete
`qualificationevidence.Plan` and thereby certify its own source, template,
credential, artifact, output, KMS, or trust expectations. The only freeze
command contains three distinct UUIDv4 values:

- the idempotent freeze operation ID;
- the new immutable plan-authority ID; and
- an opaque input-authority ID resolved through a server-installed authority.

This package is not a promotion authority. A frozen plan does not mean that a
run happened, evidence is valid, a canonical review passed, an immutable
revision exists, or a workflow node may be submitted.

## Server-only freeze boundary

For a new operation, `Service.Freeze` performs the following closed sequence:

```text
inspect exact operation
  -> resolve opaque input authority from trusted server storage
  -> verify exact input and qualification-plan projection bytes/hashes
  -> validate target/source/template/Golden/credential/artifact/KMS/trust policy
  -> compile deterministic run and operation UUIDs into Evidence Plan v1
  -> construct request/input/projection/plan/trust/target/envelope materials
  -> atomically freeze all raw canonical bytes and independently recomputed hashes
```

An existing operation is returned before the input authority is called. This
allows an exact retry after the immutable input has expired or been retired and
prevents a resolver from silently changing an already frozen result. The same
operation with another authority or input-authority ID is a conflict.

The Store assigns `frozenAt` from its own trusted UTC millisecond clock. An
ambiguous commit is reconciled only by inspecting the same operation and
comparing every immutable byte. A missing or different result remains outcome
unknown or conflict; the service does not resolve the input again under a new
operation.

There is deliberately no generic metadata, path, URL, header, environment,
secret, token, cookie, storage state, model transcript, or diagnostic field in
the command or frozen documents.

## Immutable input closure

The server-resolved input binds all of the following as exact canonical data:

- the exact project/workflow/node/immutable-revision external-qualification
  target;
- qualification manifest artifact ID, immutable revision ID, manifest content
  hash, canonical `worksflow-qualification-plan/v1` bytes, and PlanDigest;
- BuildManifest and BuildContract IDs and content hashes;
- a clean 40-hex commit and independent
  `worksflow-source-content-tree/v1` digest (`dirty` must be false);
- exactly one reviewed TemplateRelease UUID, content hash, and approval receipt
  digest;
- Golden Authority/Fixture artifact IDs and hashes, the complete fixed Golden
  fault-operation-set digest, and the stable fixture UUID;
- a precommitted atomic credential-set UUID, issuer/audience, handle/member
  hashes, member count, and distinct issuance/revocation artifact IDs;
- a strictly sorted 1..512 artifact closure and fail-closed requirements for
  restricted encryption, trace, and video;
- an exact KMS key resource/version and immutable snapshot, plaintext
  disposition, and exact-revocation policies;
- distinct output IDs for KMS attestation, artifact index, Receipt, and sealed
  snapshot; and
- eight pairwise-distinct service authority identities plus the independently
  pinned trust-policy digest.

The complete typed input is stored as exact canonical bytes with `InputHash`.
The richer qualification-plan projection is separately retained as exact raw
canonical bytes; its SHA-256 must equal the manifest PlanDigest and its subject
must equal the frozen promotion target subject. This keeps the existing
manifest projection authoritative instead of reconstructing a lossy local
approximation.

## Three non-interchangeable digests

The freeze retains three intentionally different hash domains:

| Digest | Exact bytes | Purpose |
| --- | --- | --- |
| manifest `PlanDigest` / `ProjectionHash` | canonical `worksflow-qualification-plan/v1` projection | executable qualification manifest and support closure |
| `EvidencePlanHash` | canonical `worksflow-qualification-evidence-plan/v1` Plan | concrete orchestration/run/operation reservation |
| `AuthorityHash` | canonical `worksflow-qualification-plan-authority/v1` envelope | server freeze identity binding input, target, trust, projection, and Evidence Plan |

The envelope has no recursive `AuthorityHash` field. It binds the deterministic
artifact ID `qualification-plan-<authority UUID>`, authority and input IDs,
freeze operation, input hash, manifest PlanDigest, EvidencePlanHash, target
hash, complete trust-document hash, and direct TrustBindings digest. The Store
retains request, input, projection, Evidence Plan, trust, target, and envelope
bytes alongside each hash so JSONB equality cannot replace byte authority.

The Evidence Plan's dynamic orchestration, run, eight fixed operation, and each
restricted-artifact encryption UUID are domain-separated deterministic UUIDv4
values derived from the new authority ID and immutable input hash. This makes
concurrent exact freezes converge on identical bytes rather than allocating two
different plans for one operation.

## Identity ownership

The Store reserves locally owned UUIDs across every plan authority, even when
they appear in different identity fields: freeze operation, authority,
single-use input-authority, orchestration, run, precommitted credential set,
all eight fixed Evidence operations, and all restricted encryption operations.
The deterministic plan artifact ID is globally reserved as well. A collision
fails atomically without partially claiming any identity.

The Golden fixture UUID is different: it is an upstream stable reference, not
an ID allocated by this service. It must be distinct from identities inside one
Plan, but independent qualification runs may intentionally reuse the same
reviewed fixture. Project, target revision, TemplateRelease, and manifest/build
revision references likewise remain upstream identities and are not consumed
by freezing a plan.

`MemoryStore` is thread-safe, clones all mutable slices, provides exact replay,
and can inject one post-commit unknown outcome for tests. It remains a semantic
reference.

Migration `000074` and `PostgresStore` provide the corresponding durable,
owner-only boundary. One serializable transaction reserves every locally owned
identity and artifact ID, assigns database-authoritative millisecond time, and
retains every canonical raw byte sequence, hash, JSONB document, and scalar
projection for independent reads. Exact retries are inspect-only; ambiguous
commits require byte-exact reconciliation. The Store and migration use the same
Evidence-tables-before-Plan-tables relation-lock order so freeze cannot deadlock
with rollback, and migration `000073` Evidence event IDs cannot collide in
either direction with a Plan reservation. The migration deliberately creates
no login, role, grant, DSN, API, or browser capability.

## Evidence integration and non-authorization

`Service.Resolve(ctx, authorityUUID)` implements the server-installed
`qualificationevidence.PlanAuthority`. It accepts only a canonical UUIDv4,
loads the immutable authority, independently revalidates every retained raw
material and cross-binding, and returns:

- authority ID and AuthorityHash;
- deterministic plan artifact ID;
- exact EvidencePlanHash and bytes;
- direct TrustBindings digest; and
- a cloned typed Evidence Plan.

The evidence service must still canonicalize the returned Plan and compare its
bytes/hash/artifact/trust bindings. Its existing issue, capture, encrypt, KMS,
exact revoke, index, sign, seal, and read-only verification tail is historical
v1 replay only. New external qualification must hand the indexed evidence to a
wire-v3 operator that seals and independently verifies a pre-Receipt snapshot
before either Receipt signer is called. Neither service is allowed to
reinterpret AuthorityHash as a Promotion receipt or a workflow capability.

## Explicit production blockers

The following work remains mandatory before this is a real qualification
control plane:

1. **Production input authority.** A trusted operator must resolve the opaque
   input ID from immutable project/revision, qualification-manifest projection,
   BuildManifest/BuildContract, reviewed TemplateRelease, Golden fixture,
   source-tree, trust-policy, and target authorities. An HTTP request assembled
   from browser fields is not an implementation of `InputAuthority`.
2. **Credential precommit authority.** The credential expectation is only a
   one-way reservation. A real broker/issuer must prove the precommit is fresh,
   run-scoped, non-reused, atomically issuable, short lived, and exactly
   revocable. This freeze does not issue or deliver credentials.
3. **Artifact-level Receipt operation.** The pure wire-v3 domain now binds the
   exact frozen plan artifact, AuthorityHash, EvidencePlanHash, target,
   QualificationManifest, BuildManifest/BuildContract, TemplateRelease, Golden
   runtime, trust policy, per-artifact index, and verified pre-Receipt snapshot.
   It still requires a durable server resolver/request/observation/terminal
   Store and real sealer/verifier/signers; repeating a manifest PlanDigest or
   passing internal verifier tests is not execution evidence.
4. **Production Plan operator and credentials.** The durable owner-only Store
   exists, but a dedicated server operator, least-privilege database role/DSN,
   audit/metrics, bounded retries, secret injection, and restore/runbook
   evidence must still be deployed and externally qualified. Direct migration-
   owner access and package/PostgreSQL canaries do not establish that posture.
5. **Lifecycle failure recovery.** The Evidence operator still needs real
   broker/capture/KMS/index/pre-snapshot-seal/verify/sign adapters, authenticated `not-invoked`
   recovery for every started-before-call boundary, durable abort-to-exact-
   revocation, and one-time credential delivery claim/acknowledgement.
6. **Promotion composition.** A separately trusted verifier and atomic
   qualification-promotion ledger must consume a signed, target-bound Receipt,
   then compose ModelProfile governance, production PostgreSQL posture,
   canonical review, exact upstream revision, immutable-revision creation, and
   node submission. Plan freezing grants none of those approvals.
