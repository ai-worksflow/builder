#!/bin/sh

set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$repository_root"

command -v docker >/dev/null 2>&1 || {
  printf '%s\n' 'Compose contract check requires docker' >&2
  exit 1
}
command -v jq >/dev/null 2>&1 || {
  printf '%s\n' 'Compose contract check requires jq' >&2
  exit 1
}

default_config=$(mktemp)
enabled_config=$(mktemp)
trap 'rm -f "$default_config" "$enabled_config"' EXIT HUP INT TERM

docker compose config --quiet
APP_ENV=production docker compose --profile maintenance config --quiet
docker compose --profile maintenance config --format json >"$default_config"

grep -F -- '--tmpfs /tmp:rw,nosuid,nodev,noexec,size=16777216,uid=10001,gid=10001,mode=0700' \
  deploy/prepare-sandbox-runtime.sh >/dev/null

jq -e '
  (.services.frontend.build.args.NODE_IMAGE | test("^[^[:space:]@]+@sha256:[0-9a-f]{64}$")) and
  (.services.api.build.args.GO_IMAGE | test("^[^[:space:]@]+@sha256:[0-9a-f]{64}$")) and
  (.services.api.build.args.RUNTIME_IMAGE | test("^[^[:space:]@]+@sha256:[0-9a-f]{64}$")) and
  .services.migrate.build.args.GO_IMAGE == .services.api.build.args.GO_IMAGE and
  .services.migrate.build.args.RUNTIME_IMAGE == .services.api.build.args.RUNTIME_IMAGE and
  .services["repository-index-gc"].build.args.GO_IMAGE == .services.api.build.args.GO_IMAGE and
  .services["repository-index-gc"].build.args.RUNTIME_IMAGE == .services.api.build.args.RUNTIME_IMAGE and
  .services["agent-model-host-relay"].build.args.GO_IMAGE == .services.api.build.args.GO_IMAGE and
  .services["agent-model-host-relay"].build.args.RUNTIME_IMAGE == .services.api.build.args.RUNTIME_IMAGE and
  .services.api.environment.RELEASE_DELIVERY_WORKER_ENABLED == "false" and
  .services.api.environment.RELEASE_DELIVERY_CONTROLLER_PROTOCOL == "worksflow.release-delivery/v3" and
  .services.api.environment.AGENT_ENABLED == "false" and
  .services.api.environment.AGENT_WORKER_ENABLED == "false" and
  .services.api.environment.AGENT_EXECUTOR_ADAPTER == "codex-cli" and
  .services.api.environment.AGENT_EXECUTOR_PROVIDER == "openai" and
  .services.api.environment.AGENT_MODEL_GATEWAY_BASE_URL == "http://worksflow-agent-model-relay:18080/internal/agent-model/v1" and
  .services.api.environment.AGENT_DAEMON_HOST == "tcp://sandbox:2375" and
  .services.api.environment.AGENT_WORKTREE_ROOT == "/var/lib/worksflow/agent-worktrees" and
  .services["sandbox-images"].environment.AGENT_ENABLED == "false" and
  .services["sandbox-images"].environment.AGENT_RUNNER_NETWORK == "worksflow-agent-model" and
  .services["agent-model-host-relay"].network_mode == "service:sandbox" and
  .services["agent-model-host-relay"].read_only == true and
  .services["agent-model-host-relay"].cap_drop == ["ALL"] and
  any(.services["agent-model-host-relay"].tmpfs[]; contains("uid=10001,gid=10001,mode=0700")) and
  .services["agent-model-host-relay"].environment.WORKSFLOW_MODEL_RELAY_TARGET_ORIGIN == "http://api:8080" and
  any(.services.api.volumes[]; .source == "agent-worktrees" and .target == "/var/lib/worksflow/agent-worktrees") and
  any(.services.sandbox.volumes[]; .source == "agent-worktrees" and .target == "/var/lib/worksflow/agent-worktrees") and
  any(.services["sandbox-images"].volumes[]; .target == "/usr/local/share/worksflow/prepare-sandbox-runtime.sh" and .read_only == true)
' "$default_config" >/dev/null

qualified_hash=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
qualified_image=registry.example/worksflow/agent-runner@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
RELEASE_DELIVERY_WORKER_ENABLED=true \
RELEASE_DELIVERY_WORKER_ID=release-worker-1 \
RELEASE_DELIVERY_CONTROLLER_URL=https://release-controller.example.com \
RELEASE_DELIVERY_CONTROLLER_TOKEN=0123456789abcdef0123456789abcdef \
RELEASE_DELIVERY_CONTROLLER_ID=production-release-controller \
RELEASE_DELIVERY_CONTROLLER_VERSION=1.0.0 \
RELEASE_DELIVERY_CONTROLLER_PROTOCOL=worksflow.release-delivery/v3 \
RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST="$qualified_hash" \
RELEASE_DELIVERY_LEASE_DURATION=7m \
RELEASE_DELIVERY_POLL_INTERVAL=2s \
RELEASE_DELIVERY_RECONCILE_DELAY=8s \
RELEASE_DELIVERY_REQUEST_TIMEOUT=3m \
RELEASE_DELIVERY_RESPONSE_MAX_BYTES=2097152 \
AGENT_ENABLED=true \
AGENT_WORKER_ENABLED=true \
AGENT_WORKER_ID=agent-worker-1 \
AGENT_RUNNER_IMAGE="$qualified_image" \
AGENT_RUNNER_NETWORK=qualified-agent-model \
AGENT_MODEL_POLICY_HASH="$qualified_hash" \
AGENT_PARAMETERS_HASH="$qualified_hash" \
AGENT_PROMPT_HASH="$qualified_hash" \
AGENT_OUTPUT_SCHEMA_HASH="$qualified_hash" \
AGENT_TOOLCHAIN_HASH="$qualified_hash" \
AGENT_WALL_TIME=6m \
HTTP_WRITE_TIMEOUT=7m \
docker compose config --format json >"$enabled_config"

jq -e --arg hash "$qualified_hash" --arg image "$qualified_image" '
  .services.api.environment.RELEASE_DELIVERY_WORKER_ENABLED == "true" and
  .services.api.environment.RELEASE_DELIVERY_WORKER_ID == "release-worker-1" and
  .services.api.environment.RELEASE_DELIVERY_CONTROLLER_URL == "https://release-controller.example.com" and
  .services.api.environment.RELEASE_DELIVERY_CONTROLLER_TOKEN == "0123456789abcdef0123456789abcdef" and
  .services.api.environment.RELEASE_DELIVERY_CONTROLLER_ID == "production-release-controller" and
  .services.api.environment.RELEASE_DELIVERY_CONTROLLER_VERSION == "1.0.0" and
  .services.api.environment.RELEASE_DELIVERY_CONTROLLER_PROTOCOL == "worksflow.release-delivery/v3" and
  .services.api.environment.RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST == $hash and
  .services.api.environment.RELEASE_DELIVERY_LEASE_DURATION == "7m" and
  .services.api.environment.RELEASE_DELIVERY_POLL_INTERVAL == "2s" and
  .services.api.environment.RELEASE_DELIVERY_RECONCILE_DELAY == "8s" and
  .services.api.environment.RELEASE_DELIVERY_REQUEST_TIMEOUT == "3m" and
  .services.api.environment.RELEASE_DELIVERY_RESPONSE_MAX_BYTES == "2097152" and
  .services.api.environment.AGENT_ENABLED == "true" and
  .services.api.environment.AGENT_WORKER_ENABLED == "true" and
  .services.api.environment.AGENT_WORKER_ID == "agent-worker-1" and
  .services.api.environment.AGENT_RUNNER_IMAGE == $image and
  .services.api.environment.AGENT_RUNNER_NETWORK == "qualified-agent-model" and
  .services.api.environment.AGENT_MODEL_POLICY_HASH == $hash and
  .services.api.environment.AGENT_PARAMETERS_HASH == $hash and
  .services.api.environment.AGENT_PROMPT_HASH == $hash and
  .services.api.environment.AGENT_OUTPUT_SCHEMA_HASH == $hash and
  .services.api.environment.AGENT_TOOLCHAIN_HASH == $hash and
  .services.api.environment.AGENT_WALL_TIME == "6m" and
  .services.api.environment.HTTP_WRITE_TIMEOUT == "7m" and
  .services["sandbox-images"].environment.AGENT_ENABLED == "true" and
  .services["sandbox-images"].environment.AGENT_RUNNER_IMAGE == $image and
  .services["sandbox-images"].environment.AGENT_RUNNER_NETWORK == "qualified-agent-model"
' "$enabled_config" >/dev/null

printf '%s\n' 'Compose release/Agent topology contracts passed'
