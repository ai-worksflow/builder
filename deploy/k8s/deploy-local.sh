#!/bin/sh
set -eu
k8s_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
sh "$k8s_dir/bootstrap-tools.sh"
. "$k8s_dir/common.sh"
mkdir -p "$generated_dir" "$kube_dir" "$generated_dir/demo"
require_executable "$KUBECTL"; require_executable "$KIND"; require_executable "$tools_dir/cilium"
docker info >/dev/null 2>&1 || { echo "Docker daemon is unavailable" >&2; exit 1; }
if ! "$KIND" get clusters 2>/dev/null | grep -Fxq "$LOCAL_CLUSTER_NAME"; then
  "$KIND" create cluster --name "$LOCAL_CLUSTER_NAME" --image "$KIND_NODE_IMAGE" --config "$k8s_dir/kind-config.yaml" --kubeconfig "$KUBECONFIG"
else
  "$KIND" export kubeconfig --name "$LOCAL_CLUSTER_NAME" --kubeconfig "$KUBECONFIG"
fi
if ! "$KUBECTL" -n kube-system get daemonset cilium >/dev/null 2>&1; then
  "$tools_dir/cilium" install --version "$CILIUM_VERSION" --set image.pullPolicy=IfNotPresent --set ipam.mode=kubernetes --wait
fi
"$tools_dir/cilium" status --wait
"$KUBECTL" wait --timeout=5m nodes --all --for=condition=Ready
envoy_manifest="$generated_dir/envoy-gateway-$ENVOY_GATEWAY_VERSION-install.yaml"
manifest_sha=""; [ ! -f "$envoy_manifest" ] || manifest_sha=$(sha256sum "$envoy_manifest" | awk '{print $1}')
if [ "$manifest_sha" != "$ENVOY_GATEWAY_INSTALL_SHA256" ]; then
  curl -fL --retry 3 -o "$envoy_manifest" "https://github.com/envoyproxy/gateway/releases/download/$ENVOY_GATEWAY_VERSION/install.yaml"
fi
manifest_sha=$(sha256sum "$envoy_manifest" | awk '{print $1}')
[ "$manifest_sha" = "$ENVOY_GATEWAY_INSTALL_SHA256" ] || { echo "Envoy Gateway manifest checksum mismatch" >&2; exit 1; }
"$KUBECTL" apply --server-side --force-conflicts -f "$envoy_manifest"
"$KUBECTL" wait --timeout=5m -n envoy-gateway-system deployment/envoy-gateway --for=condition=Available
"$KUBECTL" label namespace envoy-gateway-system worksflow.dev/gateway-dataplane=true --overwrite
for manifest in 00-namespaces 10-project-baselines 20-network-policies; do "$KUBECTL" apply -f "$k8s_dir/manifests/$manifest.yaml"; done
docker build --tag "$ROUTE_PROBE_IMAGE" --file "$k8s_dir/demo/Dockerfile" "$k8s_dir/demo"
"$KIND" load docker-image --name "$LOCAL_CLUSTER_NAME" "$ROUTE_PROBE_IMAGE"
"$KUBECTL" apply -f "$k8s_dir/manifests/30-project-workloads.yaml"
"$KUBECTL" rollout status --timeout=3m -n wf-p-a7f3c2 deployment/route-probe
"$KUBECTL" rollout status --timeout=3m -n wf-p-b9d4e1 deployment/route-probe
for zone in apps preview; do
  cert="$generated_dir/local-$zone-wildcard.crt"
  key="$generated_dir/local-$zone-wildcard.key"
  if [ ! -s "$cert" ] || [ ! -s "$key" ]; then
    openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj "/CN=*.$zone.$LOCAL_BASE_DOMAIN" -addext "subjectAltName=DNS:*.$zone.$LOCAL_BASE_DOMAIN" -keyout "$key" -out "$cert"
  fi
  "$KUBECTL" -n worksflow-gateway create secret tls "local-$zone-wildcard" --cert "$cert" --key "$key" --dry-run=client -o yaml | "$KUBECTL" apply -f -
done
"$KUBECTL" apply -f "$k8s_dir/manifests/40-gateway.yaml"
"$KUBECTL" apply -f "$k8s_dir/manifests/50-routes.yaml"
"$KUBECTL" wait --timeout=5m -n worksflow-gateway gateway/public --for=condition=Accepted
"$KUBECTL" wait --timeout=5m -n envoy-gateway-system pod -l gateway.envoyproxy.io/owning-gateway-namespace=worksflow-gateway,gateway.envoyproxy.io/owning-gateway-name=public --for=condition=Ready
envoy_service=""; attempt=0
while [ -z "$envoy_service" ] && [ "$attempt" -lt 60 ]; do
  envoy_service=$("$KUBECTL" get service -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-namespace=worksflow-gateway,gateway.envoyproxy.io/owning-gateway-name=public -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  [ -n "$envoy_service" ] || sleep 2
  attempt=$((attempt + 1))
done
[ -n "$envoy_service" ] || { echo "Envoy data-plane service was not created" >&2; exit 1; }
sh "$k8s_dir/verify-local.sh"
