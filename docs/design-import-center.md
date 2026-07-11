# Design Import Center

Design imports deliberately treat Figma, Penpot, Excalidraw, tldraw, Storybook, Ladle, and generic files as untrusted asset sources. They never become project facts directly.

## Lifecycle

1. `POST /v1/projects/:projectId/design-imports` validates an exported file and freezes an immutable, content-addressed snapshot.
2. The command durably pre-allocates the Prototype, clean base revision, `InputManifest`, and `OutputProposal` identities from the Design Import ID. For an existing Prototype, the pre-allocated target/base pair is its exact current immutable revision.
3. A worker acquires a bounded creation lease and advances `snapshot_frozen → target_frozen → manifest_frozen → proposal_ready` with version, stage, claim-token, and lease CAS checks. Every checkpoint extends the lease; the final checkpoint releases it.
4. The service pins the current approved PageSpec and snapshot identity into an `InputManifest`, then stores the canonical Prototype conversion as a reviewable `OutputProposal`.
5. `POST /v1/design-imports/:id/decision` requires both `If-Match` and `Idempotency-Key`.
6. Approval applies the reviewed proposal and creates an immutable Prototype revision with exact PageSpec, proposal, and manifest lineage. Rejection leaves the target unchanged.

Reads require project `view`; creation requires `edit`; decisions require `review` and the existing proposal apply permission. The snapshot creator is prohibited from both approving and rejecting their own import; a separate owner, admin, or editor must review it. Every lease claim/recovery, checkpoint, failure, and decision writes an audit row and transactional outbox event.

If a process crashes after creating a downstream object but before recording its checkpoint, recovery loads the object only by its reserved UUID and verifies its project, target, base, manifest, sources, constraints, operations, and payload. It never lists for a likely match and never creates a random replacement. An expired worker cannot checkpoint after a new worker takes the lease. A concurrent request receives `design_import_processing` with `Retry-After`; clients must retry the identical request with the same idempotency key.

## Capabilities and configuration

`GET /v1/projects/:projectId/design-import-capabilities` is authoritative. Remote URL connectors currently report `remoteEnabled: false`; the server does not simulate OAuth, accept connector credentials, or fetch remote URLs. Users can upload exported JSON, SVG, PNG, JPEG, WebP, or PDF according to each source capability.

The advertised upload limit is derived from `CONTENT_MAX_BYTES`, not hard-coded. The service reserves space for the JSON/base64 snapshot envelope and bounded catalog metadata, then caps decoded input at 8 MiB. If content storage is configured below the safe envelope reserve, uploads report disabled with an explicit reason.

## Security invariants

- JSON bodies reject unknown fields, so credentials cannot be smuggled into the command schema.
- URLs are HTTPS-only and reject credentials, literal/private/loopback/link-local hosts, non-default ports, and credential-like query parameters. No remote fetch occurs in the current capability mode.
- Filename, extension, media type, decoded size, file signature, and source-specific JSON shape must agree.
- SVG rejects scripts, event handlers, entities/doctype, `foreignObject`, CSS imports/`url()`, and non-fragment `href`/`xlink:href` references.
- PDF rejects JavaScript, open actions, and launch actions.
- Figma traversal is iterative with depth/node limits; all extracted catalogs have bounded item/text counts and report truncation explicitly.
- PostgreSQL triggers make snapshot fields immutable and enforce same-project PageSpec, Prototype, manifest, proposal, base revision, and applied revision lineage.
- PostgreSQL checks bind every realized resource to its pre-allocated identity, enforce monotonic pipeline stages and complete claim tuples, and prevent replacement of a live lease. Proposal-decision and proposal-apply triggers also reject creator self-review when callers bypass the Design Import service and use generic proposal tables directly.
- Existing Prototype targets are accepted only when their exact required PageSpec source matches the requested PageSpec revision. Changing PageSpec lineage requires a separate trusted workflow.
