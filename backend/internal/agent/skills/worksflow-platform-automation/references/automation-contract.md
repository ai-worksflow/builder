# Automation command contract

## Command

`POST /v1/output-proposals/{proposalId}/advance`

The authenticated actor and idempotency key come from the platform session. The caller must not send proposal versions, ETags, draft IDs, revision IDs, or content hashes.

```json
{
  "acceptedOperationIds": ["operation-id"],
  "reviewerIds": ["reviewer-user-id"],
  "reviewSummary": "Reason for approving the exact revision",
  "approveReview": true,
  "soloReviewConfirmed": true
}
```

- `acceptedOperationIds` is the complete stable selection. A retry must use the same selection.
- `reviewerIds` contains eligible project members. Use an independent reviewer in Team mode.
- `approveReview` may be true only when the current actor is intentionally recording the decision.
- `soloReviewConfirmed` may be true only with `approveReview` and an explicit Solo Owner confirmation.

## Durable stages

The executor reloads authoritative state before progressing and resumes across these boundaries:

1. proposal operations decided;
2. reviewed proposal applied to the draft;
3. immutable `ai_proposal` revision created;
4. canonical review requested;
5. canonical review approved.

Successful output returns:

```json
{
  "stage": "review_requested",
  "proposal": {},
  "revision": {},
  "review": {}
}
```

`stage` is `review_requested` or `approved`. The returned revision is the only exact revision a workflow caller may submit.

## Error handling

- `422 automation_preflight_failed`: generated content or lineage cannot enter review. Regenerate through the platform; do not hand-edit the payload.
- `422 invalid_input`: operation selection, reviewer assignment, reason, or confirmation is invalid.
- `403`: the actor lacks authority. Do not retry as another user without an explicit user action.
- `409`: authority or committed state changed incompatibly. Refresh exact state; never blindly repeat with different inputs.
- `5xx` or network interruption: retry with the same semantic input and idempotency key. The executor accepts already committed exact stages.

## Workflow continuation

After `approved`, submit the returned revision only to the exact active human-edit node bound to the proposal. Match the next review node by definition (`*-edit` to `*-review`) and identical `sliceId`. Use only its server-projected `approve_review` action. Workflow review records orchestration consumption of canonical approval; it must not create or alter artifact approval evidence.
