#!/bin/sh
set -eu

k8s_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$k8s_dir/../.." && pwd)
. "$k8s_dir/versions.env"

tools_dir="$repo_root/.tools/kubernetes"
generated_dir="$k8s_dir/.generated"
kube_dir="$repo_root/.kube"
KUBECTL="$tools_dir/kubectl"
KIND="$tools_dir/kind"
KUBECONFIG="$kube_dir/$LOCAL_CLUSTER_NAME"
export KUBECONFIG

require_executable() {
  if [ ! -x "$1" ]; then
    echo "required executable is missing: $1" >&2
    exit 1
  fi
}

require_cluster() {
  require_executable "$KUBECTL"
  if ! "$KUBECTL" cluster-info >/dev/null 2>&1; then
    echo "cluster $LOCAL_CLUSTER_NAME is unavailable; run make k8s-deploy" >&2
    exit 1
  fi
}

expected_route_host() { printf '%s.apps.%s' "$1" "$LOCAL_BASE_DOMAIN"; }
expected_preview_host() { printf '%s.preview.%s' "$1" "$LOCAL_BASE_DOMAIN"; }
