#!/bin/sh
set -eu
d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd); . "$d/common.sh"; require_cluster
"$KUBECTL" get nodes -o wide
"$KUBECTL" get gateway,httproute -A
"$KUBECTL" get pods -n envoy-gateway-system
"$KUBECTL" get pods,service,networkpolicy,resourcequota -n wf-p-a7f3c2
"$KUBECTL" get pods,service,networkpolicy,resourcequota -n wf-p-b9d4e1
