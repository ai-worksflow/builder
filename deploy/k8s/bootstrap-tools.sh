#!/bin/sh
set -eu

k8s_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$k8s_dir/../.." && pwd)
. "$k8s_dir/versions.env"

case "$(uname -m)" in
  x86_64|amd64) binary_arch=amd64 ;;
  aarch64|arm64) binary_arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

tools_dir="$repo_root/.tools/kubernetes"
mkdir -p "$tools_dir"
download_dir=$(mktemp -d /tmp/worksflow-k8s-tools.XXXXXX)
cleanup() { rm -rf "$download_dir"; }
trap cleanup EXIT HUP INT TERM

install_kubectl() {
  kubectl_url="https://dl.k8s.io/release/$KUBECTL_VERSION/bin/linux/$binary_arch/kubectl"
  curl -fL --retry 3 -o "$download_dir/kubectl" "$kubectl_url"
  curl -fL --retry 3 -o "$download_dir/kubectl.sha256" "$kubectl_url.sha256"
  expected=$(tr -d '\r\n' < "$download_dir/kubectl.sha256")
  actual=$(sha256sum "$download_dir/kubectl" | awk '{print $1}')
  [ "$actual" = "$expected" ] || { echo "kubectl checksum mismatch" >&2; exit 1; }
  install -m 0755 "$download_dir/kubectl" "$tools_dir/kubectl"
}

install_kind() {
  kind_name="kind-linux-$binary_arch"
  kind_url="https://github.com/kubernetes-sigs/kind/releases/download/$KIND_VERSION/$kind_name"
  curl -fL --retry 3 -o "$download_dir/$kind_name" "$kind_url"
  curl -fL --retry 3 -o "$download_dir/$kind_name.sha256sum" "$kind_url.sha256sum"
  (cd "$download_dir" && sha256sum --check "$kind_name.sha256sum")
  install -m 0755 "$download_dir/$kind_name" "$tools_dir/kind"
}

install_cilium() {
  archive="cilium-linux-$binary_arch.tar.gz"
  url="https://github.com/cilium/cilium-cli/releases/download/$CILIUM_CLI_VERSION/$archive"
  curl -fL --retry 3 -o "$download_dir/$archive" "$url"
  curl -fL --retry 3 -o "$download_dir/$archive.sha256sum" "$url.sha256sum"
  (cd "$download_dir" && sha256sum --check "$archive.sha256sum")
  tar -xzf "$download_dir/$archive" -C "$download_dir" cilium
  install -m 0755 "$download_dir/cilium" "$tools_dir/cilium"
}

if [ ! -x "$tools_dir/kubectl" ] || ! "$tools_dir/kubectl" version --client=true 2>/dev/null | grep -Fq "$KUBECTL_VERSION"; then install_kubectl; fi
if [ ! -x "$tools_dir/kind" ] || ! "$tools_dir/kind" version 2>/dev/null | grep -Fq "$KIND_VERSION"; then install_kind; fi
if [ ! -x "$tools_dir/cilium" ] || ! "$tools_dir/cilium" version --client 2>/dev/null | grep -Fq "$CILIUM_CLI_VERSION"; then install_cilium; fi
"$tools_dir/kubectl" version --client=true
"$tools_dir/kind" version
"$tools_dir/cilium" version --client
