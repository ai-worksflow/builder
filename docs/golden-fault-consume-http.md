# Golden fault consume HTTP boundary

This document freezes the qualification-only server boundary for consuming an
already approved, one-shot Golden fault authority. It does **not** make an
ordinary product mutation available and does not qualify any external Golden
case by itself.

## Route and enablement

The only route is:

```text
POST /v1/qualification/golden-fault-authorities/:authorityId/consume
```

`httpapi.RouterOptions.GoldenFault` is optional. When the complete dedicated
credential authenticator, immutable authority repository, one-shot ledger, and
typed adapter registry are not supplied, the route is not registered and the
server returns the normal `404 route_not_found`. The main API composition does
not currently configure these external dependencies, so a normal Worksflow
deployment exposes no Golden fault mutation.

This route is intentionally outside the browser-session route group. It accepts
exactly one platform-issued `Authorization: Bearer <opaque credential>` and
rejects Cookie, Origin, Sec-Fetch metadata, query parameters, content encoding,
and method override headers. The credential verifier must return exact
`actorId`, `tenantId`, `projectId`, `runId`, `fixtureId`, `audience`, and
`role=fault-operator` claims. Owner, admin, user, browser session, and Reference
application credentials are never equivalent.

## Exact public request

The content type is exactly `application/json` without parameters. The bounded
body has exactly these three non-null string fields:

```json
{
  "fixtureId": "<uuid-v4>",
  "runId": "<uuid-v4>",
  "schemaVersion": "worksflow-golden-fault-consume-request/v1"
}
```

Unknown, duplicate, null, trailing, BOM, invalid UTF-8, and non-canonical UUID
values fail before authority lookup. In particular, the request has no
operation, resource ID, selector, fence, DSSE, digest, URL, shell, SQL, signal,
or arbitrary adapter input.

## Server-owned authority closure

The path `authorityId` is the sole lookup key for a trusted immutable
repository. One exact repository record provides:

- the canonical DSSE envelope and `ExpectedBinding` from the approved fixture;
- the historical fault-operator trust policy;
- the expected fault-operator actor, tenant, project, run, fixture, and
  audience.

Every repository value is cross-checked with the path, body, and authenticated
claims. The DSSE envelope digest is recomputed from copied server-owned bytes.
Trust keys are copied into a new verifier. A request can therefore never repair
or override repository drift.

The consumer then uses the existing append-before-side-effect ledger and closed
`OperationKind -> Adapter` registry. The registry has no string passthrough.
An authority replay enters the ledger `Inspect` path; it never resolves or
executes the adapter again. A reserved/unknown result returns a stable conflict
that instructs the runner to retry the same authority rather than mint another
one.

## Response and evidence

A terminal success is `200 application/json`, `Cache-Control: no-store`. The
body is the exact stored canonical bytes of
`worksflow-golden-fault-consume-receipt/v1`, with no wrapper or re-marshalling.
Before writing it, the server revalidates the full reservation/result closure.
An exact replay also sets `X-Idempotent-Replay: true`.

Problem responses use bounded stable text and never include authenticator,
repository, ledger, or adapter error details. Unknown authority is `404`, claim
drift is `403`, a one-shot conflict or unknown outcome is `409`, and unavailable
trusted dependencies are `503`.

Unit and race tests use in-memory ledgers and typed recording adapters, including
a refused security canary. Those adapters prove the boundary and one-shot state
machine only. They are not real external fault adapters, do not produce an
approved target run, and cannot change any Qualification suite from
`not-qualified` to `qualified`.
