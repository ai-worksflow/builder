#!/bin/sh

# Fail-closed validation shared by both runner image builds. Keep this script
# dependency-light because it runs in the pinned Go builder image before any
# project binary or npm package is built.

semver_prerelease_identifier='(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
semver_pattern="^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(-${semver_prerelease_identifier}(\\.${semver_prerelease_identifier})*)?(\\+[0-9A-Za-z-]+(\\.[0-9A-Za-z-]+)*)?$"

validation_error() {
  printf 'runner build contract: %s\n' "$1" >&2
  return 1
}

validate_digest_image() {
  argument_name=$1
  image_reference=$2

  case "$image_reference" in
    *@sha256:*) ;;
    *)
      validation_error "${argument_name} must be a complete image reference ending in @sha256:<64 lowercase hex>"
      return 1
      ;;
  esac

  repository=${image_reference%@sha256:*}
  digest=${image_reference##*@sha256:}

  if [ -z "$repository" ]; then
    validation_error "${argument_name} must include a repository before @sha256"
    return 1
  fi
  case "$repository" in
    *'@'*)
      validation_error "${argument_name} contains more than one digest separator"
      return 1
      ;;
  esac
  if [ "${#digest}" -ne 64 ]; then
    validation_error "${argument_name} sha256 digest must contain exactly 64 lowercase hex characters"
    return 1
  fi
  case "$digest" in
    *[!0-9a-f]*)
      validation_error "${argument_name} sha256 digest must contain only lowercase hex characters"
      return 1
      ;;
  esac
}

validate_codex_version() {
  codex_version=$1

  if [ -z "$codex_version" ]; then
    validation_error 'CODEX_VERSION must be an exact SemVer version'
    return 1
  fi
  case "$codex_version" in
    *[!0-9A-Za-z.+-]*)
      validation_error 'CODEX_VERSION contains characters that are not valid in SemVer'
      return 1
      ;;
  esac
  if ! printf '%s\n' "$codex_version" | LC_ALL=C grep -Eq "$semver_pattern"; then
    validation_error 'CODEX_VERSION must be exact SemVer without a v prefix, range, or package tag'
    return 1
  fi
}

validate_codex_integrity() {
  codex_integrity=$1

  if ! printf '%s\n' "$codex_integrity" | LC_ALL=C grep -Eq '^sha512-[A-Za-z0-9+/]+={0,2}$'; then
    validation_error 'CODEX_INTEGRITY must be one canonical sha512 Subresource Integrity value'
    return 1
  fi
  encoded=${codex_integrity#sha512-}
  if ! printf '%s' "$encoded" | base64 -d >/dev/null 2>&1; then
    validation_error 'CODEX_INTEGRITY contains invalid base64'
    return 1
  fi
  decoded_bytes=$(printf '%s' "$encoded" | base64 -d | wc -c | tr -d '[:space:]')
  if [ "$decoded_bytes" != 64 ]; then
    validation_error 'CODEX_INTEGRITY must encode exactly one SHA-512 digest'
    return 1
  fi
}

validate_contract() {
  if [ "$#" -ne 4 ]; then
    validation_error 'expected GO_IMAGE, NODE_IMAGE, CODEX_VERSION, and CODEX_INTEGRITY'
    return 1
  fi

  validate_digest_image 'GO_IMAGE' "$1" || return 1
  validate_digest_image 'NODE_IMAGE' "$2" || return 1
  validate_codex_version "$3" || return 1
  validate_codex_integrity "$4" || return 1
}

first_matching_line() {
  pattern=$1
  file=$2
  awk -v pattern="$pattern" '$0 ~ pattern { print NR; exit }' "$file"
}

check_dockerfile() {
  dockerfile=$1

  if [ ! -f "$dockerfile" ]; then
    validation_error "missing Dockerfile: ${dockerfile}"
    return 1
  fi

  validation_line=$(first_matching_line 'RUN sh /usr/local/libexec/validate-runner-build-args.sh' "$dockerfile")
  build_line=$(first_matching_line '^RUN .*go build' "$dockerfile")
  if [ -z "$validation_line" ]; then
    validation_error "${dockerfile} does not invoke the runner build contract"
    return 1
  fi
  if [ -z "$build_line" ] || [ "$validation_line" -ge "$build_line" ]; then
    validation_error "${dockerfile} must validate build arguments before compiling"
    return 1
  fi
  if ! grep -Eq 'npm install .*--ignore-scripts' "$dockerfile"; then
    validation_error "${dockerfile} must disable npm lifecycle scripts"
    return 1
  fi
  if ! grep -q '^ARG CODEX_INTEGRITY$' "$dockerfile" ||
    ! grep -Eq 'npm pack .*--ignore-scripts' "$dockerfile" ||
    ! grep -q 'value\[0\]\.integrity' "$dockerfile"; then
    validation_error "${dockerfile} must bind the packed Codex tarball to CODEX_INTEGRITY"
    return 1
  fi
}

check_dockerfiles() {
  repository_root=${1:-.}
  check_dockerfile "${repository_root}/agent-runner/Dockerfile" || return 1
  check_dockerfile "${repository_root}/sandbox-runner/Dockerfile" || return 1
  printf 'runner build contract: Dockerfile wiring checks passed\n'
}

self_test() {
  valid_go="golang:1.22-alpine@sha256:$(printf '%064d' 0)"
  valid_node="registry.example:5000/platform/node:22@sha256:$(printf '%064d' 1)"
  valid_integrity='sha512-wk+2CWiBNXiJLBoN2D08N9RceWkSBnlgk5g2K1a4CXrP/C0gdlHyRUG7RFzm9y41DCK/7tvCct233JVxyFmznw=='
  passed=0
  failed=0

  expect_valid() {
    label=$1
    shift
    if validate_contract "$@" >/dev/null 2>&1; then
      passed=$((passed + 1))
    else
      printf 'not ok - expected valid: %s\n' "$label" >&2
      failed=$((failed + 1))
    fi
  }

  expect_invalid() {
    label=$1
    shift
    if validate_contract "$@" >/dev/null 2>&1; then
      printf 'not ok - expected invalid: %s\n' "$label" >&2
      failed=$((failed + 1))
    else
      passed=$((passed + 1))
    fi
  }

  expect_valid 'stable exact version' "$valid_go" "$valid_node" '1.2.3' "$valid_integrity"
  expect_valid 'zero version' "$valid_go" "$valid_node" '0.0.0' "$valid_integrity"
  expect_valid 'prerelease and build metadata' "$valid_go" "$valid_node" '12.34.56-rc.1+linux.amd64' "$valid_integrity"
  expect_valid 'hyphenated prerelease' "$valid_go" "$valid_node" '2.1.0-preview-2' "$valid_integrity"

  expect_invalid 'empty Go image' '' "$valid_node" '1.2.3' "$valid_integrity"
  expect_invalid 'mutable Go image tag' 'golang:1.22-alpine' "$valid_node" '1.2.3' "$valid_integrity"
  expect_invalid 'wrong digest algorithm' "golang@sha512:$(printf '%064d' 0)" "$valid_node" '1.2.3' "$valid_integrity"
  expect_invalid 'short digest' 'golang@sha256:abc123' "$valid_node" '1.2.3' "$valid_integrity"
  expect_invalid 'long digest' "golang@sha256:$(printf '%065d' 0)" "$valid_node" '1.2.3' "$valid_integrity"
  expect_invalid 'uppercase digest' "golang@sha256:$(printf '%063d' 0)A" "$valid_node" '1.2.3' "$valid_integrity"
  expect_invalid 'non-hex digest' "golang@sha256:$(printf '%063d' 0)g" "$valid_node" '1.2.3' "$valid_integrity"
  expect_invalid 'missing repository' "@sha256:$(printf '%064d' 0)" "$valid_node" '1.2.3' "$valid_integrity"
  expect_invalid 'multiple digest separators' "golang@other@sha256:$(printf '%064d' 0)" "$valid_node" '1.2.3' "$valid_integrity"
  expect_invalid 'mutable Node image tag' "$valid_go" 'node:22' '1.2.3' "$valid_integrity"

  expect_invalid 'empty version' "$valid_go" "$valid_node" '' "$valid_integrity"
  expect_invalid 'v-prefixed version' "$valid_go" "$valid_node" 'v1.2.3' "$valid_integrity"
  expect_invalid 'dist tag' "$valid_go" "$valid_node" 'latest' "$valid_integrity"
  expect_invalid 'caret range' "$valid_go" "$valid_node" '^1.2.3' "$valid_integrity"
  expect_invalid 'comparison range' "$valid_go" "$valid_node" '>=1.2.3' "$valid_integrity"
  expect_invalid 'partial version' "$valid_go" "$valid_node" '1.2' "$valid_integrity"
  expect_invalid 'extra core component' "$valid_go" "$valid_node" '1.2.3.4' "$valid_integrity"
  expect_invalid 'major leading zero' "$valid_go" "$valid_node" '01.2.3' "$valid_integrity"
  expect_invalid 'minor leading zero' "$valid_go" "$valid_node" '1.02.3' "$valid_integrity"
  expect_invalid 'patch leading zero' "$valid_go" "$valid_node" '1.2.03' "$valid_integrity"
  expect_invalid 'numeric prerelease leading zero' "$valid_go" "$valid_node" '1.2.3-01' "$valid_integrity"
  expect_invalid 'empty prerelease' "$valid_go" "$valid_node" '1.2.3-' "$valid_integrity"
  expect_invalid 'empty build metadata' "$valid_go" "$valid_node" '1.2.3+' "$valid_integrity"
  expect_invalid 'whitespace suffix' "$valid_go" "$valid_node" '1.2.3 latest' "$valid_integrity"
  expect_invalid 'embedded newline' "$valid_go" "$valid_node" "1.2.3
latest" "$valid_integrity"

  expect_invalid 'empty integrity' "$valid_go" "$valid_node" '1.2.3' ''
  expect_invalid 'wrong integrity algorithm' "$valid_go" "$valid_node" '1.2.3' 'sha256-YQ=='
  expect_invalid 'invalid integrity base64' "$valid_go" "$valid_node" '1.2.3' 'sha512-abcde'
  expect_invalid 'short integrity digest' "$valid_go" "$valid_node" '1.2.3' 'sha512-YQ=='

  if [ "$failed" -ne 0 ]; then
    printf 'runner build contract: %s passed, %s failed\n' "$passed" "$failed" >&2
    return 1
  fi
  printf 'runner build contract: %s validation cases passed\n' "$passed"
}

case "${1-}" in
  --self-test)
    if [ "$#" -ne 1 ]; then
      validation_error '--self-test does not accept additional arguments'
      exit 1
    fi
    self_test
    ;;
  --check-dockerfiles)
    if [ "$#" -gt 2 ]; then
      validation_error '--check-dockerfiles accepts at most one repository root'
      exit 1
    fi
    check_dockerfiles "${2:-.}"
    ;;
  *)
    validate_contract "$@"
    ;;
esac
