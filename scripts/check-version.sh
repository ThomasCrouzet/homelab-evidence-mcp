#!/usr/bin/env bash

set -euo pipefail

repository_root="$(git rev-parse --show-toplevel)"
cd "$repository_root"

tag=${1:-}
if [ -z "$tag" ]; then
  printf 'usage: %s <tag>\n' "$0" >&2
  exit 2
fi

number='(0|[1-9][0-9]*)'
non_numeric='[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*'
prerelease="(${number}|${non_numeric})"
build='[0-9A-Za-z-]+'
semver="^v${number}\.${number}\.${number}"
semver+="(-${prerelease}(\.${prerelease})*)?"
semver+="(\+${build}(\.${build})*)?$"
if [[ ! "$tag" =~ $semver ]]; then
  printf 'Invalid version tag: %s\n' "$tag" >&2
  exit 1
fi

release_version="${tag#v}"
source_version="$(tr -d '[:space:]' < internal/version/version.txt)"
makefile_version="$(make -s print-version)"
if [ "$release_version" != "$source_version" ] || \
  [ "$release_version" != "$makefile_version" ]; then
  printf 'Tag, source version, and Makefile version must be equal.\n' >&2
  printf 'tag=%s source=%s makefile=%s\n' \
    "$release_version" "$source_version" "$makefile_version" >&2
  exit 1
fi
if ! awk -v heading="## [$release_version] - " \
  'index($0, heading) == 1 { found = 1 } END { exit !found }' \
  CHANGELOG.md; then
  printf 'CHANGELOG.md has no section for %s.\n' "$release_version" >&2
  exit 1
fi

printf 'version ok: %s\n' "$release_version"
