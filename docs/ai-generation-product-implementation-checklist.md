# Worksflow AI generation product implementation checklist

This checklist turns the prototype gaps into verifiable product requirements. A capability is complete only when its acceptance evidence is present in the current worktree and exercised by an automated or manual runtime check.

Status: complete and verified on 2026-07-10.

## 1. Generation runtime

- [x] A prompt creates a server-side generation run with a stable run id.
- [x] Planning and build modes produce prompt-specific output.
- [x] The client receives typed streaming lifecycle, plan, task, file, log, result and error events.
- [x] A running request can be cancelled and a failed request can be retried.
- [x] Provider configuration stays server-side and a useful local fallback remains available without credentials.

## 2. Context composer

- [x] Users can add text files, images, URLs, linked documents and selected workspace files.
- [x] Attachments show type, size, inclusion state and removal controls.
- [x] Users can choose model, generation mode and plan/build behavior.
- [x] Slash commands and file/document mentions are discoverable and keyboard accessible.

## 3. Project workspace

- [x] Generated files are stored in a path-safe virtual workspace.
- [x] Users can create, edit, rename and delete files.
- [x] Search, multi-file navigation and dirty state work.
- [x] Generated file patches are visible before/after application.
- [x] The terminal or command runner executes supported project checks and streams output.

## 4. Preview and inspection

- [x] Preview renders the current generated workspace rather than a hard-coded component.
- [x] Refresh, route, desktop, tablet, mobile, custom size and new-window controls work.
- [x] Runtime errors and console messages are visible and can be sent back to the composer.
- [x] Users can select a preview element or region as iteration context.

## 5. Persistence and recovery

- [x] Projects, files, runs, versions, documents and settings survive refresh.
- [x] Autosave exposes saving, saved and failure state.
- [x] Corrupt or incompatible stored data fails safely and can be reset.
- [x] Cross-tab changes are detected without silently losing newer work.

## 6. Versions and quality

- [x] Checkpoints contain immutable file snapshots.
- [x] Users can compare versions, branch, restore and undo a restore.
- [x] Build, type, lint, test, accessibility, dependency and secret checks report structured diagnostics.
- [x] Diagnostics can be attached to a repair prompt.

## 7. Git, export and publish

- [x] GitHub connection uses real authorization or an explicitly configured token flow.
- [x] Users can select a repository and branch, inspect changes, commit, push and open a pull request.
- [x] Source, document, preview and blueprint exports download real artifacts.
- [x] Publishing returns a reachable URL, streams logs, supports environment variables and records history/rollback.

## 8. Data and backend

- [x] A database connection can be tested and stored without exposing secrets.
- [x] Users can inspect schema, tables, records, auth, storage, functions and migrations.
- [x] Generated schema changes have a preview and explicit apply confirmation.
- [x] Environment variables and secrets have masked values and scoped access.

## 9. Identity and collaboration

- [x] Sign-in, sign-out and session restoration work.
- [x] Project roles are enforced for view, comment, edit, publish and administration actions.
- [x] Comments, replies, review decisions, notifications and audit events persist.
- [x] Concurrent edits expose presence and conflicts instead of silently overwriting data.

## 10. Product operations and safety

- [x] Prompt history, templates and reusable workflows are searchable.
- [x] Runs expose duration, provider/model, usage and configured cost/limit information.
- [x] Network, rate-limit, quota, context-limit and provider failures have actionable recovery UI.
- [x] Destructive, external and charge-incurring actions require appropriate confirmation.
- [x] Sensitive values are redacted from logs, prompts and exports by default.

## Verification gates

- [x] `pnpm lint`
- [x] `pnpm typecheck`
- [x] Unit tests for generation, workspace, persistence, versioning and integrations
- [x] End-to-end generation, edit, preview, restore, export and recovery flows
- [x] Production `pnpm build`
- [x] Manual responsive and accessibility review

## Verification evidence

- `pnpm test:unit`: 141 generation, workspace, persistence, preview, quality, delivery, data, GitHub, collaboration, authorization and product-flow tests passed.
- `pnpm test:e2e`: 13 Chromium product flows passed with the shared filesystem runtime serialized for isolation.
- `pnpm lint`, `pnpm typecheck`, `git diff --check` and `pnpm build`: passed without product build warnings.
- Manual browser review: 390×844 and 1280×720 layouts had no horizontal overflow, unnamed buttons, undersized focus targets or application console warnings/errors after fixes.
