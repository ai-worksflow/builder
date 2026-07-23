# Qualification Policy Authority v1

Status: implementation contract for migration `000078`. The pure Go semantic
authority is implemented under `backend/internal/qualificationpolicyauthority`;
the PostgreSQL authority, WIA binding, and production adapters must all pass
the tests in section 9 before workflow-engine/v3 can be advertised.

## 1. Purpose

Workflow Input Authority, Qualification Plan, Receipt v3, and Promotion v2
must apply one reviewed policy generation. A caller-supplied policy ID/hash or
a mutable configuration lookup is not an authority. This contract defines an
append-only, project/profile-scoped authority that decides:

- source revision currency and Canonical Review requirements;
- the exact Qualification Plan input profile; and
- Promotion v2 schemas, single-use protocol, and independent authorities.

The authority does not execute qualification, approve artifact revisions,
invent ModelProfile/PostgreSQL posture facts, or contain runtime credentials.

## 2. Scope and generations

The immutable head scope is:

```text
(projectId, executionProfile.version, executionProfile.hash)
```

There is no mutable head row. The largest committed `generation` in a scope is
the current generation. Generation one has a null `previousAuthorityHash`;
each later generation binds the immediately preceding authority hash. Issuance
uses an exact expected-previous-hash compare-and-swap under the project lock.

Each generation is either `active` or `suspended`. Only a current `active`
generation authorizes WIA Freeze or Promotion. The v1 supersession rule is
`invalidate-unconsumed/v1`: a newer generation makes every unconsumed WIA,
Plan, or Receipt bound to the prior generation stale. Historical records stay
immutable and inspectable.

## 3. Canonical root

The root schema is `worksflow-qualification-policy-authority/v1`. It contains
only these closed members:

```text
authorityId
componentDigests { revisionPolicy, planInputProfile, promotionPolicy }
executionProfile { hash, version }
externalGatePolicy
generation
issuedAt
operationId
planInputProfile
policySourceId
previousAuthorityHash
projectId
promotionPolicy
revisionPolicy
schemaVersion
status
supersessionPolicy
```

The database timestamp is UTC millisecond precision. UUID identities are
lowercase RFC-4122 UUIDv4. JSON is strict, duplicate/unknown-name rejecting,
UTF-8 canonical JSON with JavaScript-safe integers.
`policySourceId` is the bounded, non-secret opaque identifier resolved by the
trusted service; keeping it in the root makes source provenance part of the
authority hash and exact replay contract.

Hashes use this framing:

```text
SHA256(
  UTF8("worksflow-qualification-policy-authority-hash/v1") || 0x00 ||
  UTF8(domain) || 0x00 || exactCanonicalBytes
)
```

The four domains are:

```text
worksflow.qualification-policy.revision/v1
worksflow.qualification-policy.plan-input-profile/v1
worksflow.qualification-policy.promotion/v1
worksflow.qualification-policy.authority/v1
```

PostgreSQL retains exact canonical bytes, JSONB, and the domain hash for every
component and the root. A raw SHA-256 digest is not interchangeable with one
of these domain hashes.

## 4. Revision policy

The schema is `worksflow-qualification-revision-policy/v1`.

The default governed-source currency is
`latest-approved-required`. The Workspace target is fixed to the same
currency and `canonicalReviewRequired=false`. A governed source may use
`exact-approved` only when this complete tuple is listed:

```text
(sourceKind, purpose, artifactId, revisionId, contentHash)
```

The list is bounded, strictly sorted, unique, and cannot name a Workspace
source/target. An exact exception changes currency only; it never lowers the
review rule.

Review policy contains exactly one rule for each closed artifact revision
change source, sorted by name:

```text
ai_proposal
human
import
merge
rollback
system
```

`human` must require Canonical Review. Other choices remain explicit reviewed
product policy; missing rules are invalid rather than defaulting open.

During WIA Freeze, PostgreSQL locks the current policy and derives for every
locked revision:

```text
currencyPolicy
canonicalReviewRequired
changeSourceAtFreeze
sourceRequiredAtFreeze
```

`sourceRequiredAtFreeze` comes from the exact BuildContract source row, not
from the policy. Candidate values are equality assertions only. The receipt
set must equal exactly the derived `canonicalReviewRequired=true` source set;
it is not automatically `all sources` and never includes the Workspace under
v1.

## 5. Plan input profile

The schema is `worksflow-qualification-plan-input-profile/v1`. It freezes:

- exact Qualification Manifest ID/revision/content/plan digest;
- exact approved TemplateRelease binding;
- exact Golden runtime binding;
- bounded artifact expectations and output identities;
- restricted-encryption, trace, video, immutable-snapshot, plaintext-disposal,
  and credential-revocation rules;
- KMS recipient and trust bindings;
- source/trust policy digests; and
- a non-secret credential resolver profile.

It must not contain URLs, DSNs, filesystem paths, environment values, tokens,
cookies, headers, private keys, credential members, or other secret material.
The credential profile identifies an authority/audience and immutable request
set only; actual credentials remain in the CredentialSet authority.

## 6. Promotion policy

The schema is `worksflow-qualification-promotion-policy/v1`. It binds:

```text
planAuthoritySchema = worksflow-qualification-plan-authority/v1
receiptSchema       = worksflow-qualification-receipt/v3
singleUseProtocol   = worksflow-qualification-promotion-consume/v2
```

Independent requirements are a closed, sorted list containing only:

```text
model-profile-activation
production-postgresql-posture
```

Each member binds one exact opaque authority ID/hash; one ID or hash cannot
satisfy both roles. An empty list must be an explicit reviewed product choice,
never an implicit fallback caused by an unavailable resolver.

## 7. Command and storage boundary

The service command contains only:

```text
operationId
authorityId
opaque policySourceId
expectedPreviousAuthorityHash
```

A trusted server-installed resolver returns typed policy content. Public API
clients cannot submit policy JSON or individual switches. Exact operation
replay is inspected before resolving the source, so retirement of the source
does not break recovery.

PostgreSQL tables are append-only and include:

```text
qualification_policy_authorities
qualification_policy_review_defaults
qualification_policy_exact_approved_sources
qualification_policy_identity_reservations
```

Every table rejects `UPDATE`, `DELETE`, and `TRUNCATE`. Deferred closure checks
prove generation/predecessor continuity, component bytes/hashes/documents,
six review defaults, exact-source ordinals, cross-authority UUID-role
reservations, and root/component equality.

Required database operations are issue, inspect operation, resolve authority,
resolve current scope, assert current active authority, and test whether one
record is exact. Store commit-unknown recovery inspects the same operation and
never allocates a second generation.

## 8. Lock order and ACL

The common ordering is:

```text
rolling migration fence
  -> project row/advisory mutex
  -> workflow run/node when applicable
  -> current qualification-policy generation
  -> sorted artifacts/revisions
  -> manifests/build contract/review receipts
  -> WIA
  -> Plan / Evidence / Receipt / Promotion
```

The ordinary application role has no policy table access and cannot issue or
resolve raw policy records. A dedicated policy operator may issue, inspect an
operation, and resolve an exact authority or current diagnostic head so the
trusted Store can implement the complete service contract.
The Plan operator receives only the WIA-scoped policy projection it needs.
Promotion may call a transaction-bound current assertion only. All
SECURITY DEFINER routines use a fixed trusted `search_path`; PUBLIC and
unrelated roles receive no execute privilege.

Migration `000079` must relation-lock the policy tables with the rest of the
v3 catalog transition. Migration `000078` down refuses while any policy or WIA
history exists.

## 9. Required verification

The stage is incomplete until all of these pass:

- strict/golden Go vectors and cross-language PostgreSQL hash parity;
- unknown/duplicate/null/widened JSON rejection and secret scanning;
- first generation, successor generation, stale CAS, identity collision,
  exact replay, changed replay, commit-unknown recovery, and concurrent issue;
- current-active versus current-suspended assertions;
- exact six-rule review closure and human-review fail-closed checks;
- exact-approved tuple allow/deny tests, including non-latest source and proof
  that exact currency cannot lower review;
- WIA Freeze rejects an absent, stale, suspended, hash-mismatched, or
  caller-invented policy and derives the exact receipt subset;
- a newer policy generation makes unconsumed WIA current assertion fail;
- ACL/owner/search-path posture and empty/non-empty rollback canaries; and
- one no-trigger-bypass production fixture from BuildContract and Canonical
  Review through current policy, WIA Freeze, recovery, and current assertion.
