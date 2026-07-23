#!/bin/sh
set -eu
d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd); . "$d/common.sh"; require_executable "$KIND"
"$KIND" delete cluster --name "$LOCAL_CLUSTER_NAME"
