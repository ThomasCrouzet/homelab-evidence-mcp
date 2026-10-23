#!/usr/bin/env bash

set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
repo_root=$(cd "$repo_root" && pwd -P)
artifact_dir=${HEM_E2E_ARTIFACT_DIR:-${TMPDIR:-/tmp}/homelab-evidence-mcp-e2e}
mkdir -p "$artifact_dir"
artifact_dir=$(cd "$artifact_dir" && pwd -P)

case "$artifact_dir" in
  "$repo_root"|"$repo_root"/*)
    printf 'HEM_E2E_ARTIFACT_DIR must be outside the repository.\n' >&2
    exit 2
    ;;
esac

sha256_stream() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  else
    shasum -a 256 | awk '{print $1}'
  fi
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

cd "$repo_root"
run_id=$(date -u +%Y%m%dT%H%M%SZ)-$$
log_file="$artifact_dir/stdio-e2e-$run_id.log"
metadata_file="$artifact_dir/stdio-e2e-$run_id.txt"
source_revision=$(git rev-parse HEAD)
tracked_diff_sha256=$(git diff --binary HEAD | sha256_stream)
working_tree_identity_sha256=$(
  {
    git diff --binary HEAD
    git ls-files --others --exclude-standard | LC_ALL=C sort |
      while IFS= read -r file_path; do
        printf '%s  %s\n' "$(git hash-object "$file_path")" "$file_path"
      done
  } | sha256_stream
)
fixture_sha256=$(sha256_file internal/mcpserver/stdio_e2e_test.go)
stdio_fixtures_identity_sha256=$(
  for file_path in internal/mcpserver/*stdio*_test.go; do
    printf '%s  %s\n' "$(sha256_file "$file_path")" "$file_path"
  done | sha256_stream
)
if [ -n "${HEM_E2E_BINARY:-}" ]; then
  binary=$HEM_E2E_BINARY
  case "$binary" in
    /*) ;;
    *) binary="$repo_root/$binary" ;;
  esac
  binary_dir=$(cd "$(dirname "$binary")" && pwd -P)
  binary="$binary_dir/$(basename "$binary")"
  if [ ! -f "$binary" ] || [ ! -x "$binary" ]; then
    printf 'HEM_E2E_BINARY must be an executable regular file: %s\n' "$binary" >&2
    exit 2
  fi
  export HEM_E2E_BINARY=$binary
  binary_sha256=$(sha256_file "$binary")
  binary_version=$("$binary" --version)
  command_text="HEM_E2E_BINARY=$binary GOMAXPROCS=2 GOFLAGS=-p=1 go test -race ./internal/mcpserver -run ^TestStdio -count=1 -v"
else
  binary=built-from-source-with-race
  binary_sha256=per-test-temporary-binary
  binary_version=source-default-version
  command_text="GOMAXPROCS=2 GOFLAGS=-p=1 go test -race ./internal/mcpserver -run ^TestStdio -count=1 -v"
fi

set +e
GOMAXPROCS=2 GOFLAGS=-p=1 \
  go test -race ./internal/mcpserver -run '^TestStdio' -count=1 -v 2>&1 |
  tee "$log_file"
pipeline_status=("${PIPESTATUS[@]}")
test_status=${pipeline_status[0]}
tee_status=${pipeline_status[1]}
set -e

if [ "$test_status" -eq 0 ] && [ "$tee_status" -eq 0 ]; then
  result=pass
  result_status=0
else
  result=fail
  if [ "$test_status" -ne 0 ]; then
    result_status=$test_status
  else
    result_status=$tee_status
  fi
fi

{
  printf 'command: %s\n' "$command_text"
  printf 'source_revision: %s\n' "$source_revision"
  printf 'tracked_diff_sha256: %s\n' "$tracked_diff_sha256"
  printf 'working_tree_identity_sha256: %s\n' "$working_tree_identity_sha256"
  printf 'fixture_sha256: %s\n' "$fixture_sha256"
  printf 'stdio_fixtures_identity_sha256: %s\n' "$stdio_fixtures_identity_sha256"
  printf 'environment: %s; %s\n' "$(uname -a)" "$(go version)"
  printf 'binary: %s\n' "$binary"
  printf 'binary_sha256: %s\n' "$binary_sha256"
  printf 'binary_version: %s\n' "$binary_version"
  printf 'fixtures: internal/mcpserver/*stdio*_test.go; local httptest HTTP services\n'
  printf 'result: %s\n' "$result"
  printf 'test_exit_code: %s\n' "$test_status"
  printf 'tee_exit_code: %s\n' "$tee_status"
  printf 'result_exit_code: %s\n' "$result_status"
  printf 'log: %s\n' "$log_file"
} > "$metadata_file"

printf 'E2E metadata: %s\n' "$metadata_file"
exit "$result_status"
