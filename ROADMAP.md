# Roadmap

This document gives possible directions. It does not give commitments or
dates.

## Version 0.1

The scope for version 0.1 is:

- Six read-only source families.
- A canonical service registry.
- Seven MCP tools that use stdio transport.
- A deterministic evidence model and timeline.
- Validation that does not accept incorrect data.
- Redaction, caches, and budgets.
- Binary releases for multiple platforms.

## Possible evolutions

Possible future changes include:

- Beszel time series when a stable historical API is available.
- Other notification systems that use the same evidence model.
- Homebrew or Nix packages when users request them.
- Optional OpenTelemetry traces that contain durations and no business
  content.

## Permanent non-goals

The project will not:

- Change Docker, hosts, DNS, or notifications.
- Become a request proxy for general observability.
- Identify root causes.
- Supply access secrets or a private topology.
- Accept the Docker Unix socket.
- Supply a CLI option that deactivates TLS verification.
