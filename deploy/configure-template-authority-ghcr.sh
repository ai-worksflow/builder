#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
target="$script_dir/.env.template-authority.local"

read -r -p 'GitHub username: ' github_username
read -r -s -p 'PAT classic (read:packages): ' github_pat
printf '\n'

if [[ ! "$github_username" =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,38}$ ]] ||
  [[ "$github_username" == *- || "$github_username" == *--* ]]; then
  printf 'Invalid GitHub username.\n' >&2
  exit 64
fi
if [[ ! "$github_pat" =~ ^ghp_[A-Za-z0-9]{20,}$ ]]; then
  printf 'A PAT classic value beginning with ghp_ is required.\n' >&2
  exit 64
fi

authorization=$(printf '%s:%s' "$github_username" "$github_pat" | base64 -w 0)
umask 077
temporary=$(mktemp "$target.tmp.XXXXXX")
printf "WORKSFLOW_TEMPLATE_REGISTRY_AUTHORIZATION='Basic %s'\n" "$authorization" > "$temporary"
chmod 600 "$temporary"
mv -- "$temporary" "$target"
unset github_pat authorization

printf 'Wrote owner-only GHCR authorization to %s\n' "$target"
