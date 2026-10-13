# Improvement priorities

These items extend the read-only contract in [SECURITY.md](SECURITY.md).

## P1: Verify executable cancellation

- Component: `cmd/homelab-evidence-mcp`, `internal/mcpserver/collectors.go`.
- Benefit: prove that cancelled stdio requests release source and tool slots.
- Completion: a child-process scenario cancels a blocked HTTP fixture.
  A subsequent tool call completes, and the check retains its transcript.

## P1: Report audit storage failures

- Component: `internal/audit`, `internal/mcpserver/tools.go`.
- Benefit: make failed or full audit storage visible to the operator.
- Completion: a failing writer produces a bounded diagnostic without secrets.
  Healthy tool responses follow a documented failure policy.

## P2: Bound evidence-cache memory by bytes

- Component: `internal/evidence/cache.go`.
- Benefit: control memory when many evidence items contain large attributes.
- Completion: large fixtures cause deterministic eviction within both limits,
  without changing retrieval freshness.

## P2: Expand adapter compatibility fixtures

- Component: `internal/adapters`, [API compatibility](docs/api-compatibility.md).
- Benefit: detect differences between supported upstream response versions.
- Completion: versioned fixtures verify missing fields, timestamps, partial
  failures, and truncation for all six adapters.

## P2: Define source-cache cancellation semantics

- Component: `internal/httpx/client.go`.
- Benefit: make cached and network responses consistent under request cancellation.
- Completion: the documented contract has stdio checks for cancelled requests
  with warm and cold caches.
