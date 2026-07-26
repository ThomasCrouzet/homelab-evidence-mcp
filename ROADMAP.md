# Roadmap

This document describes directions, not commitments or dates.

## Version 0.1

- six read-only source families;
- canonical service registry;
- seven MCP tools over stdio transport;
- deterministic evidence model and timeline;
- strict validation, redaction, caches, and budgets;
- multi-platform binary publishing.

## Possible evolutions

- Beszel time series when a stable historical API becomes available;
- other notification systems compatible with the same evidence model;
- Homebrew or Nix packages if real demand appears;
- optional OpenTelemetry traces limited to durations, without business content.

## Permanent non-goals

- mutate Docker, hosts, DNS, or notifications;
- become a general-purpose observability request proxy;
- produce root-cause analysis;
- distribute access secrets or a private topology;
- accept the direct Docker Unix socket;
- expose an option that disables TLS verification.
