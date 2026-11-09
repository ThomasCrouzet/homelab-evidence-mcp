#!/usr/bin/env bash

set -euo pipefail

# Detect concrete private topology in tracked files. The check prints only a
# file and line, never the matching value. Documentation CIDR ranges and
# example hostnames are allowed.

repository_root="$(git rev-parse --show-toplevel)"
cd "$repository_root"

ipv4_private='(^|[^0-9])(10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3})([^0-9]|$)'
tailscale_ip='100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.'
ipv6_ula='(^|[^0-9a-fA-F])(f[cd][0-9a-fA-F]{2}):'
tailscale_dns='[[:alnum:].-]+\.ts\.net'
private_dns='[[:alnum:]][[:alnum:].-]*\.(internal|local|lan|home\.arpa)([^[:alnum:].-]|$)'

combined="${ipv4_private}|${tailscale_ip}|${ipv6_ula}|${tailscale_dns}|${private_dns}"

is_private_line() {
  local line="$1"
  # A documented CIDR range is not a concrete endpoint.
  if printf '%s\n' "$line" | grep -qE '[0-9a-fA-F:.]+/[0-9]{1,3}'; then
    return 1
  fi
  # Published example names are safe.
  if printf '%s\n' "$line" | grep -qE 'example\.'; then
    return 1
  fi
  # Loopback addresses are valid in local httptest fixtures.
  if printf '%s\n' "$line" | grep -qE '(^|[^0-9])127\.0\.0\.1([^0-9]|$)'; then
    return 1
  fi
  printf '%s\n' "$line" | grep -qE "$combined"
}

self_test() {
  local failures=0
  local positive=(
    'host: 10.1.2.3'
    'host: 172.16.0.5'
    'host: 192.168.1.10'
    'host: fd12:3456::1'
    'host: nas.home.arpa'
    'host: 100.64.1.2'
    'host: node.ts.net'
    'host: gatus.internal'
  )
  local negative=(
    'CIDR `10.0.0.0/8`'
    'CIDR `172.16.0.0/12`'
    'CIDR `192.168.0.0/16`'
    'TEST-NET `192.0.2.10`'
    'TEST-NET `198.51.100.5`'
    'TEST-NET `203.0.113.5`'
    'DOC `2001:db8::1`'
    'local `127.0.0.1`'
    'example `gatus.example.internal`'
    'example `example.local`'
  )
  for value in "${positive[@]}"; do
    if ! is_private_line "$value"; then
      echo "self-test: missed a private value: $value" >&2
      failures=$((failures + 1))
    fi
  done
  for value in "${negative[@]}"; do
    if is_private_line "$value"; then
      echo "self-test: rejected an allowed value: $value" >&2
      failures=$((failures + 1))
    fi
  done
  if [ "$failures" -ne 0 ]; then
    return 1
  fi
  echo "private-address self-test ok"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit $?
fi

self_test >/dev/null

status=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  if is_private_line "$line"; then
    file_and_line="${line%%:*}"
    rest="${line#*:}"
    line_number="${rest%%:*}"
    echo "Possible private network address: ${file_and_line}:${line_number}" >&2
    status=1
  fi
done < <(git grep -nIE "$combined" -- . ':(exclude)scripts/check-private-addresses.sh' || true)

if [ "$status" -ne 0 ]; then
  exit 1
fi
