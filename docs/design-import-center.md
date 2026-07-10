# Design Import Center

Design imports deliberately treat Figma, Penpot, Excalidraw, tldraw, Storybook, Ladle, and generic files as untrusted asset sources. They never become project facts directly.

## Lifecycle

1. `POST /v1/projects/:projectId/design-imports` validates an exported file and freezes an immutable, content-addressed snapshot.
2. The service pins the current approved PageSpec and snapshot identity into an `InputManifest`.
3. A canonical Prototype conversion is stored as a reviewable `OutputProposal`.
4. `POST /v1/design-imports/:id/decision` requires both `If-Match` and `Idempotency-Key`.
5. Approval applies the reviewed proposal and creates an immutable Prototype revision with exact PageSpec, proposal, and manifest lineage. Rejection leaves the target unchanged.

Reads require project `view`; creation requires `edit`; decisions require `review` and the existing proposal apply permission. Every lifecycle mutation writes an audit row and transactional outbox event.

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
- Existing Prototype targets are accepted only when their exact required PageSpec source matches the requested PageSpec revision. Changing PageSpec lineage requires a separate trusted workflow.
