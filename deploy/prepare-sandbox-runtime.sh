#!/bin/sh

set -eu

relay_container=worksflow-agent-model-relay
relay_kind=worksflow-agent-model-relay
network_kind=worksflow-agent-model-network

fail() {
  printf 'sandbox runtime preparation: %s\n' "$1" >&2
  exit 64
}

require_boolean() {
  name=$1
  value=$2
  case "$value" in
    true|false) ;;
    *) fail "${name} must be true or false" ;;
  esac
}

require_digest_image() {
  name=$1
  value=$2
  if ! printf '%s\n' "$value" | grep -Eq '^[^[:space:]@]+@sha256:[0-9a-f]{64}$'; then
    fail "${name} must be an immutable image@sha256:<64 lowercase hex> reference"
  fi
}

ensure_image() {
  image=$1
  if docker image inspect "$image" >/dev/null 2>&1; then
    printf 'sandbox runtime preparation: using pre-provisioned image %s\n' "$image"
    return
  fi
  docker pull "$image"
}

pull_quality_images() {
  test -n "${NODE_IMAGE:-}" || fail 'NODE_IMAGE is required'
  test -n "${GO_IMAGE:-}" || fail 'GO_IMAGE is required'
  ensure_image "$NODE_IMAGE"
  ensure_image "$GO_IMAGE"
}

prepare_interactive_runner() {
  if [ "$SANDBOX_ENABLED" != true ]; then
    return
  fi
  require_digest_image SANDBOX_RUNNER_IMAGE "${SANDBOX_RUNNER_IMAGE:-}"
  ensure_image "$SANDBOX_RUNNER_IMAGE"
}

prepare_lsp_images() {
  if [ "$LSP_ENABLED" != true ]; then
    return
  fi
  test -n "${LSP_PRELOAD_IMAGES:-}" || fail 'LSP_PRELOAD_IMAGES is required when LSP_ENABLED=true'
  for image in $LSP_PRELOAD_IMAGES; do
    require_digest_image LSP_PRELOAD_IMAGES "$image"
    ensure_image "$image"
  done
}

managed_relay_exists() {
  docker container inspect "$relay_container" >/dev/null 2>&1
}

remove_managed_relay() {
  if ! managed_relay_exists; then
    return
  fi
  observed_kind=$(docker container inspect --format '{{ index .Config.Labels "worksflow.kind" }}' "$relay_container")
  if [ "$observed_kind" != "$relay_kind" ]; then
    fail "container ${relay_container} exists without the managed relay identity"
  fi
  docker container rm --force "$relay_container" >/dev/null
}

prepare_agent_network() {
  if printf '%s\n' "$AGENT_RUNNER_NETWORK" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$'; then
    case "$AGENT_RUNNER_NETWORK" in
      bridge|host|none|container*) fail 'AGENT_RUNNER_NETWORK must be a dedicated internal network' ;;
    esac
  else
    fail 'AGENT_RUNNER_NETWORK is invalid'
  fi

  if docker network inspect "$AGENT_RUNNER_NETWORK" >/dev/null 2>&1; then
    observed_internal=$(docker network inspect --format '{{.Internal}}' "$AGENT_RUNNER_NETWORK")
    observed_kind=$(docker network inspect --format '{{ index .Labels "worksflow.kind" }}' "$AGENT_RUNNER_NETWORK")
    if [ "$observed_internal" != true ] || [ "$observed_kind" != "$network_kind" ]; then
      fail "existing network ${AGENT_RUNNER_NETWORK} is not the managed internal Agent network"
    fi
    return
  fi

  docker network create \
    --internal \
    --label "worksflow.kind=${network_kind}" \
    --label 'worksflow.owner=worksflow-builder-compose' \
    "$AGENT_RUNNER_NETWORK" >/dev/null
}

prepare_agent_relay() {
  if [ "$AGENT_ENABLED" != true ]; then
    remove_managed_relay
    return
  fi
  require_digest_image AGENT_RUNNER_IMAGE "${AGENT_RUNNER_IMAGE:-}"
  ensure_image "$AGENT_RUNNER_IMAGE"
  prepare_agent_network
  remove_managed_relay

  docker container create \
    --name "$relay_container" \
    --pull never \
    --network "$AGENT_RUNNER_NETWORK" \
    --restart unless-stopped \
    --read-only \
    --cap-drop ALL \
    --security-opt no-new-privileges \
    --pids-limit 64 \
    --memory 64m \
    --cpus 0.10 \
    --user 10001:10001 \
    --tmpfs /tmp:rw,nosuid,nodev,noexec,size=16777216,uid=10001,gid=10001,mode=0700 \
    --add-host worksflow-dind-host:host-gateway \
    --label "worksflow.kind=${relay_kind}" \
    --label 'worksflow.owner=worksflow-builder-compose' \
    --label "worksflow.runner-image=${AGENT_RUNNER_IMAGE}" \
    --env 'WORKSFLOW_MODEL_RELAY_LISTEN_ADDRESS=0.0.0.0:18080' \
    --env 'WORKSFLOW_MODEL_RELAY_TARGET_ORIGIN=http://worksflow-dind-host:18080' \
    --env "WORKSFLOW_MODEL_RELAY_REQUEST_TIMEOUT=${AGENT_MODEL_RELAY_REQUEST_TIMEOUT:-8h1m}" \
    --health-cmd '/usr/local/bin/worksflow-agent-model-relay healthcheck' \
    --health-interval 1s \
    --health-timeout 3s \
    --health-retries 30 \
    --entrypoint /usr/local/bin/worksflow-agent-model-relay \
    "$AGENT_RUNNER_IMAGE" >/dev/null
  docker network connect bridge "$relay_container"
  docker container start "$relay_container" >/dev/null

  attempt=0
  while [ "$attempt" -lt 30 ]; do
    health=$(docker container inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$relay_container")
    if [ "$health" = healthy ]; then
      printf 'sandbox runtime preparation: Agent Model Relay is healthy\n'
      return
    fi
    if [ "$health" = unhealthy ] || [ "$health" = missing ]; then
      docker container logs "$relay_container" >&2 || true
      fail "Agent Model Relay health is ${health}"
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
  docker container logs "$relay_container" >&2 || true
  fail 'Agent Model Relay did not become healthy'
}

require_boolean SANDBOX_ENABLED "${SANDBOX_ENABLED:-false}"
require_boolean LSP_ENABLED "${LSP_ENABLED:-false}"
require_boolean AGENT_ENABLED "${AGENT_ENABLED:-false}"

pull_quality_images
prepare_interactive_runner
prepare_lsp_images
prepare_agent_relay
