# Improvement priorities

These items extend the read-only contract in [SECURITY.md](SECURITY.md).

## P2: Bound evidence-cache memory by bytes

Status: implemented. The cache enforces an entry limit and a serialized-payload
byte budget. See `internal/evidence/cache.go`.

## P2: Expand adapter compatibility fixtures

Status: implemented for documented replays. The versioned corpus, provenance,
offline replay tests, and matrix are under `internal/adapters/upstream` and
[docs/upstream-fixtures.md](docs/upstream-fixtures.md). An opt-in live lane
exists, but no live-verified combination is recorded yet.

## P2: Define source-cache cancellation semantics

Status: implemented. The HTTP client and source collector check the caller
context before a cache read or network request. See `internal/httpx/client.go`.
