---
name: worksflow-platform-automation
description: Advance exact Worksflow output proposals through operation decisions, Apply, immutable revision creation, canonical review, and workflow resumption by using the platform's guarded automation command. Use when an AI host agent needs to continue or recover a PageSpec, Prototype, Blueprint, document, or other proposal-backed platform flow; when repeated manual platform steps would create stale versions or 409 conflicts; or when diagnosing where an automated advance stopped. Do not use inside an isolated Candidate coding attempt.
---

# Worksflow Platform Automation

Use the platform automation boundary instead of composing individual mutation APIs. Keep content judgment with the user and keep state transitions deterministic.

## Advance a reviewed proposal

1. Read the current proposal, artifact, project governance mode, workflow run, and actor-specific allowed actions.
2. Show the proposal content diff to the user. Obtain one exact selection of accepted operation IDs. Never infer acceptance from a generation success.
3. For Solo review, obtain an explicit self-review confirmation and a non-empty reason. For Team review, select an eligible independent reviewer.
4. Invoke `POST /v1/output-proposals/{proposalId}/advance` once with the exact accepted IDs, reviewer IDs, review reason, and confirmation flags.
5. Trust the returned `stage`, `revision`, and `review`. Do not reconstruct IDs, ETags, versions, or content hashes.
6. If `stage` is `review_requested`, stop at that human decision point.
7. If `stage` is `approved` and the proposal belongs to an active workflow edit node, submit the returned exact revision. Resolve its matching workflow review node only when the actor has the server-projected action and the same explicit review confirmation authorizes it.

Read [references/automation-contract.md](references/automation-contract.md) before implementing a new caller or handling an error response. Use [references/host-tool.json](references/host-tool.json) as the single host tool or MCP input contract; do not widen it with mutable platform state.

## Recover safely

- Retry the same automation command with the same semantic input after a network interruption. The server resumes from committed state.
- Refresh and retry only when the server reports a genuine concurrency precondition.
- On `automation_preflight_failed`, do not patch the artifact, proposal, database, or lineage by hand. Request a new generation through the platform recovery path or report the exact blocker.
- On `review_requested`, wait for the assigned reviewer. Never substitute a self-approval in Team mode.
- On an unknown or ambiguous proposal, stop. Never choose the newest proposal merely by timestamp when an exact workflow proposal pin exists.

## Keep boundaries intact

- Never expose this Skill or platform credentials to the isolated Candidate runner.
- Never let an AI approve content without an exact user-confirmed operation selection.
- Never call Decide, Apply, CreateRevision, or Review endpoints individually when the guarded advance command supports the flow.
- Never treat HTTP 409 as a generic validation response. Preserve 409 for changed authority or concurrency; report deterministic content preflight failures separately.
- Never manually repair generated output during black-box platform validation.
