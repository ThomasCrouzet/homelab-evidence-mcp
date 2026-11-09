#!/usr/bin/env bash

# Opt-in live integration lane. It uses a real operator configuration and real
# read-only upstream credentials. Normal CI never runs this script.
#
# Required:
#   HEM_LIVE_INTEGRATION=1
#   HEM_LIVE_CONFIG=/path/to/private-config.yaml
# Optional:
#   HEM_LIVE_ARTIFACT_DIR=/var/tmp/hem-live

set -euo pipefail

if [ "${HEM_LIVE_INTEGRATION:-0}" != "1" ]; then
  echo "live integration skipped: set HEM_LIVE_INTEGRATION=1 and HEM_LIVE_CONFIG"
  exit 0
fi

config="${HEM_LIVE_CONFIG:-}"
if [ -z "$config" ] || [ ! -f "$config" ]; then
  echo "HEM_LIVE_CONFIG must name a readable configuration file" >&2
  exit 2
fi

repository_root="$(git rev-parse --show-toplevel)"
artifact_dir="${HEM_LIVE_ARTIFACT_DIR:-/var/tmp/hem-live}"
case "$artifact_dir" in
  "$repository_root"|"$repository_root"/*)
    echo "HEM_LIVE_ARTIFACT_DIR must be outside the repository" >&2
    exit 2
    ;;
esac
mkdir -p "$artifact_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
report="$artifact_dir/live-${stamp}.json"
metadata="$artifact_dir/live-${stamp}.txt"

cd "$repository_root"
make build >/dev/null
binary="$repository_root/bin/homelab-evidence-mcp"

set +e
"$binary" --config "$config" --validate >"$artifact_dir/validate-${stamp}.txt" 2>&1
validate_status=$?
"$binary" --config "$config" --doctor --json >"$report" 2>"$artifact_dir/doctor-${stamp}.err"
doctor_status=$?
set -e

{
  echo "command: live read-only validation and doctor"
  echo "source_revision: $(git rev-parse HEAD)"
  echo "tracked_diff_sha256: $(git diff --binary | sha256sum | cut -d' ' -f1)"
  echo "environment: $(uname -srm); $(go version)"
  echo "binary_version: $("$binary" --version)"
  echo "validate_exit_code: $validate_status"
  echo "doctor_exit_code: $doctor_status"
  echo "result: $([ "$validate_status" -eq 0 ] && [ "$doctor_status" -eq 0 ] && echo pass || echo fail)"
  echo "report: $report"
} >"$metadata"

cat "$metadata"
cat "$report"

if [ "$validate_status" -ne 0 ] || [ "$doctor_status" -ne 0 ]; then
  exit 1
fi
