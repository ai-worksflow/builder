# Worksflow Codex Agent Runner

This image is a deliberately small adapter around `codex exec`. The platform
mounts an exact TaskCapsule, ContextPack, prompt, and output schema read-only at
`/input`, an isolated historical Candidate worktree at `/workspace`, and an
empty evidence directory at `/output`.

Run the build from the repository root. Builds must provide digest-pinned
`GO_IMAGE` and `NODE_IMAGE` values plus an
exact `CODEX_VERSION`; mutable package tags such as `latest` are rejected. The
deployed `AGENT_RUNNER_IMAGE` must refer to the resulting image by digest.

The container is expected to run with a read-only root filesystem, a tmpfs at
`/tmp`, all Linux capabilities dropped, `no-new-privileges`, bounded CPU,
memory/PID limits, and a dedicated model-gateway network. It receives only a
short-lived attempt-scoped gateway token, never the upstream provider secret.

The model's `changedPaths` and verification claims are advisory. The platform
independently scans the worktree, constructs the patch, enforces path policy,
and executes the exact template verification commands.

## Budget enforcement and evidence

Runner request v3 seals `maxInputTokens`, `maxOutputTokens`, and `maxCommands`
from the exact TaskCapsule. The launcher rejects a request whose values drift
from that mounted capsule. It counts unique `command_execution` item IDs in the
documented `codex exec --json` stream and cancels the Codex child process as
soon as it observes the first command beyond `maxCommands`. The immutable
execution record includes the sealed limits, observed command count, provider-
reported turn token usage, and the exact exceeded-budget kind. The platform
independently reparses `events.jsonl` and rejects a record whose observations or
budget decision do not match the raw event evidence. After validation, the
platform prepends the exact execution record as a
`worksflow.platform.runner_execution` JSONL envelope before content-addressing
stdout evidence, so the record remains available even when a failed run has no
`result.json` and later log storage retains only the evidence prefix.

`max_output_tokens` is capped for every Responses request and is reserved
across the attempt-scoped capability. Input admission is deliberately
conservative: the gateway reserves normalized UTF-8 request bytes plus fixed
protocol and JSON-structure allowances across requests. This is not an exact
model tokenizer and may reject an otherwise valid request. Provider-reported
`turn.completed` usage is recorded for post-execution audit; qualifying a new
model/provider must demonstrate that the conservative admission policy remains
safe for its Responses framing. If usage is missing, a successful execution is
not accepted, and the gateway does not refund its conservative output
reservation.
