# Upstream fixture provenance

These fixtures are test source. They contain synthetic values. No fixture is a
captured production payload, and no test contacts a live upstream service.
The review date is 2026-11-05.

Verification levels:

- `documented-replay`: the structure follows the identified upstream release or
  official API document. The values are synthetic. The bytes were not captured
  from a live instance.
- `synthetic-contract`: an adapter edge case that is not an upstream example.

## Files

- `gatus/statuses-v5.38.0.json`: Gatus v5.38.0 statuses API.
  Level: documented-replay. Covers success, failure, numeric duration, and
  empty results.
- `gatus/statuses-empty.json`: Gatus v5.38.0.
  Level: synthetic-contract. Covers an empty endpoint array.
- `docker/containers-v1.56.json`: Docker Engine API v1.56.
  Level: documented-replay. Covers a running healthy container, labels, and
  unused fields.
- `docker/containers-empty.json`: Docker Engine API v1.56.
  Level: synthetic-contract. Covers an empty container array.
- `loki/query-range-v3.7.9.json`: Grafana Loki v3.7.9 query range API.
  Level: documented-replay. Covers one stream, two timestamps, and an error
  line.
- `loki/query-range-invalid-timestamp.json`: Grafana Loki v3.7.9.
  Level: synthetic-contract. Covers an invalid timestamp that is discarded.
- `healthchecks/checks-v3-readonly.json`: Healthchecks v5.0.3 Management API v3.
  Level: documented-replay. Covers a read-only key with `unique_key` and no
  `uuid`.
- `healthchecks/checks-v3-array.json`: Healthchecks v5.0.3 Management API v3.
  Level: documented-replay. Covers a bare array with a full-access `uuid`.
- `healthchecks/checks-v3-empty.json`: Healthchecks v5.0.3 Management API v3.
  Level: synthetic-contract. Covers an empty checks array.
- `beszel/systems-page-1-v0.21.1.json`: Beszel v0.21.1 PocketBase collection.
  Level: documented-replay. Covers page 1 of 2 with current metrics.
- `beszel/systems-page-2-v0.21.1.json`: Beszel v0.21.1.
  Level: documented-replay. Covers page 2 of 2 with an offline system and zero
  metrics.
- `beszel/systems-empty.json`: Beszel v0.21.1.
  Level: synthetic-contract. Covers a complete empty collection.
- `ntfy/replay-v2.37.2.ndjson`: ntfy v2.37.2 replay poll API.
  Level: documented-replay. Covers message and keepalive lines with the default
  priority.
- `ntfy/replay-array-v2.37.2.json`: ntfy v2.37.2.
  Level: documented-replay. Covers the JSON array form with high priority.

## Upstream references

- Gatus: <https://github.com/TwiN/gatus/releases/tag/v5.38.0>
- Docker Engine API: <https://github.com/moby/moby/blob/docker-v29.9.3/api/swagger.yaml>
- Grafana Loki: <https://github.com/grafana/loki/releases/tag/v3.7.9>
- Healthchecks: <https://github.com/healthchecks/healthchecks/releases/tag/v5.0.3>
  and <https://healthchecks.io/docs/api/>
- Beszel: <https://github.com/henrygd/beszel/releases/tag/v0.21.1>
- ntfy: <https://github.com/binwiederhier/ntfy/releases/tag/v2.37.2> and
  <https://docs.ntfy.sh/subscribe/api/>

## Review

Each fixture uses example hostnames and synthetic identifiers. The corpus has no
tokens, ping URLs, private addresses, personal data, or instructions to an
agent. Do not add a captured production response without a new review.

The replay tests in `replay_test.go` load these files and run them through the
adapters. Normal CI stays offline and deterministic.
