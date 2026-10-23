#!/usr/bin/env bash

set -euo pipefail

repository_root="$(git rev-parse --show-toplevel)"
cd "$repository_root"

tailscale_ip='100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.'
tailscale_dns='[[:alnum:].-]+\.ts\.net'

if git grep -nIE "$tailscale_ip" -- . ||
  git grep -nIE "$tailscale_dns" -- .; then
  echo "Possible private Tailscale address or domain" >&2
  exit 1
fi
