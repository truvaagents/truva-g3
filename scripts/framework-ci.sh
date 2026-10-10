#!/usr/bin/env bash
# Shared framework gates for local use and GitHub Actions. Examples have their
# own build-only job; the root module's framework package is checked as '.'.
set -euo pipefail

readonly GOLANGCI_LINT_VERSION=v2.14.0
readonly GOVULNCHECK_VERSION=v1.7.0
readonly MODULES=(. ai core memory orchestration resilience telemetry)

usage() {
  printf '%s\n' \
    'Usage: bash scripts/framework-ci.sh <command>' \
    '  install-lint  Install the pinned linter (includes gofmt/goimports/gosec).' \
    '  install-vuln  Install the pinned vulnerability scanner.' \
    '  test          Build and run uncached race tests with coverage, including Bedrock.' \
    '  lint          Check formatting, vet and lint, including Bedrock.' \
    '  vuln          Scan framework dependencies, including Bedrock.' \
    '  all           Run test, lint and vuln; install the tools first.' \
    'Tools live in TRUVAG3_CI_TOOLS_DIR, or the Go build cache, not the repository.'
}

command_name="${1:-help}"
case "$command_name" in
  help|--help|-h) usage; exit 0 ;;
  install-lint|install-vuln|test|lint|vuln|all) ;;
  *) usage >&2; exit 2 ;;
esac
if [[ $# -ne 1 ]]; then
  usage >&2
  exit 2
fi

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
go_version="$(awk '$1 == "go" { print $2 }' go.work)"
if [[ ! "$go_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'Expected a patch-pinned Go version in go.work; got %s\n' "$go_version" >&2
  exit 1
fi
# The local entry point uses the same toolchain as actions/setup-go, rather than
# whichever Go happens to appear first on a developer's PATH.
export GOTOOLCHAIN="go${go_version}"
go version
go env GOOS GOARCH CGO_ENABLED
tools_dir="${TRUVAG3_CI_TOOLS_DIR:-$(go env GOCACHE)/truvag3-ci-tools}"
if [[ "$tools_dir" != /* ]]; then
  printf 'TRUVAG3_CI_TOOLS_DIR must be an absolute path\n' >&2
  exit 1
fi

verify_tool() {
  local name="$1" module="$2" expected="$3" actual
  if [[ ! -x "$tools_dir/$name" ]]; then
    printf 'Missing %s. Run the corresponding install command first.\n' "$tools_dir/$name" >&2
    exit 1
  fi
  actual="$(go version -m "$tools_dir/$name" | awk -v module="$module" '$1 == "mod" && $2 == module { print $3 }')"
  if [[ "$actual" != "$expected" ]]; then
    printf '%s version is %s; expected %s. Re-run the install command.\n' "$name" "$actual" "$expected" >&2
    exit 1
  fi
  # Include compiler and relevant embedded analyzer versions in the CI evidence.
  go version -m "$tools_dir/$name" | awk 'NR == 1 || $1 == "mod" || ($1 == "dep" && ($2 == "golang.org/x/tools" || $2 == "github.com/securego/gosec/v2"))'
}

check_linter() {
  verify_tool golangci-lint github.com/golangci/golangci-lint/v2 "$GOLANGCI_LINT_VERSION"
}

check_scanner() {
  verify_tool govulncheck golang.org/x/vuln "$GOVULNCHECK_VERSION"
}

run_tests() {
  local module pattern
  for module in "${MODULES[@]}"; do
    pattern=./...
    [[ "$module" != . ]] || pattern=.
    printf 'Build and race tests: %s\n' "$module"
    (cd "$module" && go build "$pattern" &&
      go test -count=1 -race -coverprofile=coverage.out -covermode=atomic "$pattern")
  done
  printf 'Build and race tests: ai (Bedrock enabled)\n'
  (cd ai && go build -tags bedrock ./... &&
    go test -count=1 -race -tags bedrock -coverprofile=coverage-bedrock.out -covermode=atomic ./...)
}

check_formatting() {
  local diff status=0
  # --diff is non-mutating. Explicitly check its output as well as its status:
  # formatting differences must fail even if a formatter returns status zero.
  diff="$("$tools_dir/golangci-lint" fmt --config "$repo_dir/.golangci.yml" --diff --color never "$@")" || status=$?
  if [[ -n "$diff" ]]; then
    printf '%s\n' "$diff" >&2
    return 1
  fi
  return "$status"
}

run_lint() {
  local module pattern
  check_linter
  for module in "${MODULES[@]}"; do
    pattern=./...
    [[ "$module" != . ]] || pattern=.
    printf 'Formatting, vet and lint: %s\n' "$module"
    (
      cd "$module"
      if [[ "$module" == . ]]; then
        check_formatting ./*.go
      else
        check_formatting ./...
      fi
      go vet "$pattern"
      "$tools_dir/golangci-lint" run --config "$repo_dir/.golangci.yml" "$pattern"
    )
  done
  printf 'Vet and lint: ai/providers/bedrock (Bedrock enabled)\n'
  (cd ai && go vet -tags bedrock ./providers/bedrock &&
    "$tools_dir/golangci-lint" run --config "$repo_dir/.golangci.yml" --build-tags bedrock ./providers/bedrock)
}

run_vuln() {
  local module pattern
  check_scanner
  "$tools_dir/govulncheck" -version
  for module in "${MODULES[@]}"; do
    pattern=./...
    [[ "$module" != . ]] || pattern=.
    printf 'Vulnerability scan: %s\n' "$module"
    (cd "$module" && "$tools_dir/govulncheck" "$pattern")
  done
  (cd ai && "$tools_dir/govulncheck" -tags bedrock ./providers/bedrock)
}

case "$command_name" in
  install-lint)
    mkdir -p "$tools_dir"
    GOBIN="$tools_dir" GOWORK=off go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$GOLANGCI_LINT_VERSION"
    check_linter
    ;;
  install-vuln)
    mkdir -p "$tools_dir"
    GOBIN="$tools_dir" GOWORK=off go install "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION"
    check_scanner
    ;;
  test) run_tests ;;
  lint) run_lint ;;
  vuln) run_vuln ;;
  all)
    check_linter
    check_scanner
    run_tests
    run_lint
    run_vuln
    ;;
esac
