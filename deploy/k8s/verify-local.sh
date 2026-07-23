#!/bin/sh
set -eu
k8s_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$k8s_dir/common.sh"; require_cluster
a=$(expected_route_host project-a); b=$(expected_route_host project-b); preview=$(expected_preview_host 4f0c2e8a)
assert_project() {
  scheme=$1; port=$2; host=$3; project=$4; shift 4
  response=$(curl --noproxy '*' --silent --show-error --fail --resolve "$host:$port:127.0.0.1" "$@" "$scheme://$host:$port/")
  printf '%s' "$response" | grep -Fq "\"project\":\"$project\""
}
assert_project http "$LOCAL_HTTP_PORT" "$a" project-a
assert_project http "$LOCAL_HTTP_PORT" "$b" project-b
assert_project http "$LOCAL_HTTP_PORT" "$preview" project-a
assert_project https "$LOCAL_HTTPS_PORT" "$a" project-a --cacert "$generated_dir/local-apps-wildcard.crt"
assert_project https "$LOCAL_HTTPS_PORT" "$b" project-b --cacert "$generated_dir/local-apps-wildcard.crt"
assert_project https "$LOCAL_HTTPS_PORT" "$preview" project-a --cacert "$generated_dir/local-preview-wildcard.crt"
unknown="unknown.apps.$LOCAL_BASE_DOMAIN"
status=$(curl --noproxy '*' --silent -o /dev/null -w '%{http_code}' --resolve "$unknown:$LOCAL_HTTP_PORT:127.0.0.1" "http://$unknown:$LOCAL_HTTP_PORT/")
[ "$status" = 404 ] || { echo "unknown hostname returned $status" >&2; exit 1; }
headers=$(mktemp /tmp/worksflow-websocket.XXXXXX); trap 'rm -f "$headers"' EXIT HUP INT TERM
curl --noproxy '*' --silent --max-time 3 --resolve "$a:$LOCAL_HTTP_PORT:127.0.0.1" --http1.1 -D "$headers" -o /dev/null -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' "http://$a:$LOCAL_HTTP_PORT/ws" || true
grep -Eq '^HTTP/1\.[01] 101 ' "$headers" || { echo "WebSocket upgrade failed" >&2; cat "$headers" >&2; exit 1; }
"$KUBECTL" delete --ignore-not-found -n wf-p-a7f3c2 job/cross-namespace-denied >/dev/null
"$KUBECTL" apply -f "$k8s_dir/manifests/90-isolation-probe.yaml" >/dev/null
"$KUBECTL" wait --timeout=60s -n wf-p-a7f3c2 job/cross-namespace-denied --for=condition=Complete >/dev/null
"$KUBECTL" logs -n wf-p-a7f3c2 job/cross-namespace-denied | grep -Fq 'cross-namespace connection denied after DNS resolution'
gateway_programmed=$("$KUBECTL" get -n worksflow-gateway gateway public -o jsonpath='{.status.conditions[?(@.type=="Programmed")].status}')
[ "$gateway_programmed" = True ] || { echo "Gateway is not Programmed: $gateway_programmed" >&2; exit 1; }
listener_programmed=$("$KUBECTL" get -n worksflow-gateway gateway public -o jsonpath='{.status.listeners[*].conditions[?(@.type=="Programmed")].status}')
case "$listener_programmed" in *False*|*Unknown*|'') echo "a Gateway listener is not Programmed: $listener_programmed" >&2; exit 1;; esac
for ref in wf-p-a7f3c2/project-a-app wf-p-a7f3c2/project-a-preview wf-p-b9d4e1/project-b-app; do
  namespace=${ref%/*}; route=${ref#*/}
  accepted=$("$KUBECTL" get -n "$namespace" httproute "$route" -o jsonpath='{.status.parents[*].conditions[?(@.type=="Accepted")].status}')
  case "$accepted" in *False*|*Unknown*|'') echo "HTTPRoute $route is not Accepted: $accepted" >&2; exit 1;; esac
done
echo "Kubernetes vertical slice verified:"
echo "  http://$a:$LOCAL_HTTP_PORT"
echo "  https://$a:$LOCAL_HTTPS_PORT"
echo "  http://$b:$LOCAL_HTTP_PORT"
echo "  http://$preview:$LOCAL_HTTP_PORT"
echo "  HTTP/HTTPS, WebSocket, unknown-host rejection, and cross-namespace denial passed"
