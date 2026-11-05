# Upstream compatibility matrix

This page records what the project verifies for each adapter. It separates test
replays from live execution. The review date is 2026-11-05.

## Verification levels

- **Live verified**: the check ran against a disposable real service.
- **Documented replay**: a versioned fixture follows the identified upstream
  release or official API document. Tests replay it offline with synthetic
  values.
- **Synthetic coverage**: an adapter contract case that is not an upstream
  example.
- **Unverified**: no project check.

The current corpus has no live-verified combination. Every adapter has at least
one documented replay. The fixture files and provenance are under
`internal/adapters/upstream/testdata/`.

## Matrix

- **Gatus**: fixture anchor Gatus v5.38.0 statuses API. Verification:
  documented replay. Access path: `GET /api/v1/endpoints/statuses` with an
  optional token. Gap: no live token-scope check.
- **Docker Engine**: fixture anchor API v1.56 (`docker-v29.9.3`). Verification:
  documented replay. Access path: `GET /containers/json?all=true` through a
  socket proxy. Gap: the corpus does not prove the proxy denies other methods.
- **Loki**: fixture anchor v3.7.9 query range API. Verification: documented
  replay. Access path: `GET /loki/api/v1/query_range` with an optional token.
  Gap: no live tenant or token-scope check.
- **Healthchecks**: fixture anchor v5.0.3 Management API v3. Verification:
  documented replay. Access path: `GET /api/v3/checks/` with a read-only API
  key. Gap: the fixture decodes `unique_key`, but it does not prove the key is
  read-only at the server.
- **Beszel**: fixture anchor v0.21.1 PocketBase systems collection.
  Verification: documented replay. Access path:
  `GET /api/collections/systems/records` or a compatible systems route. Gap: no
  live PocketBase access-rule check.
- **ntfy**: fixture anchor v2.37.2 replay poll API. Verification: documented
  replay. Access path:
  `GET /{topic}/json?poll=1&since=<unix>`. Gap: no live topic ACL or
  token-scope check.

## Read-only scopes

An HTTP `GET` alone does not make a credential read-only. The operator must set
the upstream scope:

- Healthchecks: create a read-only API key. Bind the check with `check_unique_key`
  when the response omits `uuid`.
- Docker: use a socket proxy that permits only the required `GET` path. The
  server refuses redirects and cannot change the method. The proxy is outside
  this repository and its policy is not tested here.
- Loki: use a token with query permission only.
- Beszel: use a token with read access to the systems collection.
- ntfy: use a token that can read the configured topic.
- Gatus: the status route is read-only. If the deployment uses a token, give it
  read access only.

The server does not refresh, rotate, or create credentials. Provision and rotate
them outside the server. A restart reads the new value from `token_env` or
`token_file`.

## Test execution

Normal CI runs the offline replay tests in `internal/adapters/upstream`. The
tests are deterministic and use local `httptest` servers. No test contacts a
live upstream service.

A real-service integration lane is not implemented. That gap is explicit: the
matrix must not claim live verification until such a lane runs and records the
revision, versions, and result outside the repository.

## Adding a fixture

1. Identify the upstream release or official document.
2. Remove tokens, ping URLs, private addresses, personal data, and instructions.
3. Use example hostnames and synthetic identifiers.
4. Record the file, anchor, level, and notes in the provenance table.
5. Add or extend an offline replay test.
6. Keep execution reports outside the repository and do not upload them as
   Actions artifacts.
