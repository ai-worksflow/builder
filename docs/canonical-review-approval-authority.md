# Canonical Review Approval Authority

Status: implemented and closed through migrations `000076` and `000077`.

This document defines the only Canonical Review approval authority that later
workflow, qualification, Promotion, handoff, and Publish code may trust. A
mutable `review_requests`/`review_decisions` projection, an approved badge, or
a browser-computed count is not authority. The authority is one immutable,
database-authored receipt for one exact approved revision.

## 1. Migration boundary

The implementation is intentionally split across two forward migrations:

| Migration | Contract |
| --- | --- |
| `000076_canonical_review_approval_receipt_authority` | version markers, immutable receipt store, canonical wire/hash functions, issuer, resolver, application probe, atomic-close trigger, source/receipt immutability, ACL boundary |
| `000077_canonical_review_authority_hardening` | exact non-zero canonical UUID checks, Go-compatible Unicode trimming, append-only decisions, exact optimistic-concurrency chain, causal time closure, Solo Owner and current-authority hardening |

`000076` is a published immutable migration. Its checksum must never be
changed to incorporate a later correction; all corrections are forward-only.
`000077` preserves the `000076` wire and table identity and strengthens its
verifiers and mutation guards.

Historical rows are not upgraded into trusted evidence. Rows present before
`000076` are permanently marked `review_authority_version = 0`; new requests
and decisions default to version `1`. No migration guesses missing reviewer,
governance, precondition, or Solo Owner facts.

## 2. State-machine invariant

For a version-1 approval, the closing transaction must perform all of the
following atomically:

```text
lock project governance
  -> lock exact request and source revision closure
  -> append one exact reviewer decision
  -> update revision and artifact approved pointers
  -> close request with that exact decision
  -> issue the immutable Canonical Review receipt
  -> commit the deferred receipt requirement
```

The deferred constraint trigger
`canonical_review_approved_requires_receipt` rejects a version-1 request that
commits as approved without the exact receipt. The receipt binds request,
project, artifact, revision, revision content hash, and closing decision.

After close:

- the request is immutable and cannot be deleted;
- decisions are append-only and cannot be deleted;
- the receipt rejects `UPDATE`, `DELETE`, and `TRUNCATE`; and
- foreign keys use `ON DELETE RESTRICT`, never cascade away authority.

Open decisions are append-only as well. One reviewer can contribute at most
one decision to a request. A duplicate reviewer retry is a conflict, not an
overwrite of historical evidence.

## 3. Exact optimistic-concurrency chain

Each version-1 decision freezes the caller's exact predecessor ETag:

```text
"review:<request-id>:open:<prior-decision-count>:<prior-latest-created-at-unix-nano>"
```

The first decision therefore uses:

```text
"review:<request-id>:open:0:0"
```

The issuer and receipt verifier reconstruct the chain in deterministic
`(created_at, id)` order. A syntactically plausible but forged ETag, duplicate
reviewer, mutable decision, already-reached threshold left open, role drift,
or malformed predecessor makes the open request stale and restartable; it can
never be silently repaired into authority.

## 4. Receipt wire and hash

The root contract is:

```text
schemaVersion: worksflow-canonical-review-approval-receipt/v1
mediaType: application/vnd.worksflow.canonical-review-approval-receipt+json;version=1
hashDomain: worksflow.canonical-review.receipt/v1
```

It embeds six closed components:

| Component | Schema version | Hash domain |
| --- | --- | --- |
| review request | `worksflow-canonical-review-request-snapshot/v1` | `worksflow.canonical-review.review-request/v1` |
| revision | `worksflow-canonical-review-revision-snapshot/v1` | `worksflow.canonical-review.revision/v1` |
| policy | `worksflow-canonical-review-policy-snapshot/v1` | `worksflow.canonical-review.policy/v1` |
| decisions | `worksflow-canonical-review-decisions-snapshot/v1` | `worksflow.canonical-review.decisions/v1` |
| governance | `worksflow-canonical-review-governance-snapshot/v1` | `worksflow.canonical-review.governance/v1` |
| approval | `worksflow-canonical-review-approval-snapshot/v1` | `worksflow.canonical-review.approval/v1` |

Every root/component stores its canonical bytes, domain-separated hash, and
closed JSON document. The digest is not a plain hash of the bytes:

```text
sha256(
  UTF8("worksflow-canonical-review-authority-hash/v1")
  || 0x00
  || UTF8(hashDomain)
  || 0x00
  || canonicalBytes
)
```

The serialized form is `sha256:<64 lowercase hex>`. Object names are ordered
by UTF-8 bytes; only JSON syntax/control characters are escaped; integers are
bounded to the JavaScript-safe range; duplicate/unknown fields, noncanonical
numbers, NUL, invalid UTF-8, noncanonical UUID text, the zero UUID, and
timestamps outside the shared `[1678-01-01, 2262-01-01)` Unix-nanosecond
authority domain are rejected. PostgreSQL and Go share fixed golden
vectors, including Chinese text, control bytes, U+2028/U+2029, NBSP, and
ideographic-space cases. The SQL verifier also parses and UTC-round-trips every
microsecond timestamp, so PostgreSQL-normalized forms such as hour `24`, leap
second `60`, or an invalid calendar date cannot become SQL-exact/Go-invalid.

The receipt table identity is:

```text
canonical_review_approval_receipts
primary key: review_request_id
unique: receipt_hash
unique: revision_id
```

There is deliberately no second receipt ID. Downstream authority must retain
the exact `receipt_hash`, `review_request_id`, `revision_id`, and copied receipt
bytes rather than inventing an alias.

## 5. Approval semantics

The receipt proves the following facts at issuance:

- the exact request targets the exact revision and content hash;
- the artifact is active and the revision is both latest and latest-approved;
- revision creation precedes the request, and close/approval/closing-decision
  times form one causal, millisecond-normalized closure;
- the strict policy contains only its canonical fields, always prohibits
  ordinary self-review, and has a threshold from 1 through 20;
- the approval count equals the threshold exactly;
- all reviewers are currently project owners, admins, or editors and match
  their frozen decision roles;
- governance mode, owner count, sole owner, reviewer assignment, decision
  identities, and the complete ETag chain are exact; and
- every copied root/component byte, hash, document, and duplicated scalar is
  internally consistent.

Ordinary self-review is prohibited. The only exception is an explicit Solo
Owner proof:

- governance is `solo`;
- exactly one owner exists;
- that owner authored the revision and is explicitly assigned as a reviewer;
- policy freezes `soloSelfReviewOwnerId`;
- the decision freezes owner role, sole-owner identity, and explicit
  confirmation; and
- the decision includes a non-empty trimmed explanation.

An optional Solo Owner self-review policy does not force self-review. An
assigned independent editor/admin/owner may approve the owner-authored
revision as an ordinary independent approval.

The receipt is historical authority. It does not claim that its revision will
remain the artifact's current pointer forever. A later Workflow Input freeze
or Promotion policy must separately check any required current-pointer rule
while still binding this immutable receipt.

## 6. Database API and ACL

The canonical SQL entry points are:

```sql
issue_canonical_review_approval_receipt(review_request_id uuid)

resolve_canonical_review_approval_receipt(
  project_id uuid,
  revision_id uuid,
  receipt_hash text
)

canonical_review_approval_receipt_is_exact(
  project_id uuid,
  revision_id uuid,
  review_request_id uuid
) returns boolean
```

The issuer returns the immutable row plus `created`; exact replay returns the
same row with `created = false`. The owner-side resolver locks the project,
resolves the exact tuple, and recomputes the entire closed record. Missing
authority uses `WCR02`; durable corruption uses `WCR03`. The Boolean probe is
for application ReviewGate/readiness only and is not an authority-construction
API.

`PUBLIC` has no access. `worksflow_application` receives only non-grantable
execute permission on the issuer and exact Boolean probe. It cannot read or
mutate the receipt table and cannot execute the owner-side resolver or
internal canonicalization/guard helpers. All other platform/operator roles
remain outside this authority unless a later reviewed owner-side composition
routine invokes the resolver internally.

The production posture at head `000077` pins the Canonical Review inventory to
one table, five indexes, four triggers, twelve functions, and four
`SECURITY DEFINER` functions. The three `000077` primitive helpers are
owner-only `IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER` functions: the
UUID/Unicode-boundary predicates use SQL and the timestamp round-trip
predicate uses PL/pgSQL with a fixed `pg_catalog` search path.

## 7. Service and UI recovery behavior

The Go service never upgrades malformed authority in place:

- legacy version-0 open request;
- malformed version-1 policy, including a missing `governanceMode`;
- role/member/governance drift;
- an approval threshold that exceeds the currently eligible non-author
  reviewers (including an empty reviewer set after membership loss);
- forged or duplicate predecessor decision;
- revision content/state/current-pointer drift;
- an artifact archived while its review is open; or
- an already-satisfied threshold left open

causes one locked transaction to close the request as `stale`, move an
`in_review` revision to `changes_requested`, emit the stale event, and return a
conflict. The user can then submit a clean version-1 review.

List and exact lookup return a non-null fail-closed policy projection for
legacy/malformed rows, plus:

```text
reviewAuthorityVersion: 0 | 1
authorityState: current | legacy | invalid
```

This prevents one historical bad row from crashing the project review list.
The HTTP decision route uses the project-bound exact lookup rather than
requiring a full list decode before it can reach the recovery transaction.
Clients must not enable downstream Workflow approval from `status` alone;
ReviewGate passes only when the version-1 exact receipt probe succeeds.

Commit-unknown approval reconciliation is deliberately narrow. It accepts
only the exact closing decision, actor, payload, original ETag, close time, and
immutable receipt; it never retries the mutation or infers success from
mutable rows.

## 8. Rollback policy

Both down migrations take writer fences. `000077.down` refuses once any
version-1 request, decision, or receipt exists; after a truly empty hardening
rollback, `000076.down` applies its own version-1/receipt fence. Cancelled
rollback cannot partially remove the authority.

Upgrade is also fail-closed. `000077.up` holds access-exclusive locks across
requests, decisions, and receipts, installs the new verifier transactionally,
and validates every receipt published by `000076` before changing mutation or
issuer behavior. An incompatible historical receipt aborts the whole migration
and restores the exact `000076` catalog/authority. Compatible `000076`
receipts remain exact after upgrade.

One catalog-only limitation is pinned by test rather than hidden: the already
published immutable `000076.down` recreates the historical
`review_request_policy_immutable()` trigger helper without its prior explicit
`worksflow_application EXECUTE` ACL entry. Trigger execution does not consult
that otherwise inert direct-call ACL, and all owner, body, trigger, security,
path, language, volatility, and PUBLIC-revocation attributes are restored
exactly. The test permits only this named delta. Do not edit `000076`; any
future policy must be a new forward migration.

## 9. Verification evidence

The closed test set includes:

- Go canonical golden vectors, strict decode, semantic drift, Unicode trim,
  arbitrary-version/non-zero canonical UUID, duplicate names, and race tests;
- real PostgreSQL upgrade from history, compatible/incompatible published
  `000076` receipt preflight, permanent v0 marking, exact issuance,
  replay, resolver/probe, missing-receipt deferred rollback, concurrent final
  approval, append-only sources, application/PUBLIC ACL, writer/down fences;
- root plus all six component byte/hash/document tamper canaries and duplicated
  scalar tamper canaries;
- Solo Owner, independent approval, two-person quorum, duplicate reviewer,
  forged ETag, ACK-loss reconciliation, role drift, legacy/malformed policy,
  already-reached threshold, stale/restart, and legacy-approved ReviewGate
  fail-closed service tests; and
- the exhaustive PostgreSQL 16 production posture through migrations 1–77.

Focused commands from `backend/` are:

```sh
go test -count=1 ./internal/canonicalreviewreceipt ./internal/core ./internal/httpapi/transport ./migrations
go test -race -count=1 ./internal/canonicalreviewreceipt ./internal/core ./internal/httpapi/transport
go vet ./internal/canonicalreviewreceipt ./internal/core ./internal/httpapi/transport ./migrations

WORKSFLOW_TEST_POSTGRES_DSN="$TEST_DSN" \
  go test -timeout=15m -count=1 -p=1 ./migrations \
  -run '^TestCanonicalReview'

WORKSFLOW_TEST_POSTGRES_DSN="$TEST_DSN" \
  go test -timeout=15m -count=1 -p=1 ./internal/core \
  -run '^TestSoloGovernance'

WORKSFLOW_TEST_POSTGRES_DSN="$POSTGRES16_TEST_DSN" \
  go test -count=1 ./internal/platform \
  -run '^TestPostgresAPIRolePostureRealPostgres$'
```

## 10. Downstream consumption rule

Workflow Input Authority begins at migration `000078` and has both `000076`
and `000077` as hard prerequisites. Its owner-side freeze must derive the
required reviewed revisions from server state, call the exact owner resolver,
copy the immutable receipt bytes/hash, and include them in its own canonical
closure. It must never accept a browser-supplied receipt, trust the Boolean
probe as a freeze primitive, or reconstruct approval from live review rows.
