# Compact incident context

`incident_context` has two selection modes. The default keeps the previous
newest-item behavior. `compact: true` reserves representation for each
contributing source and enforces a response byte budget.

## Compatibility

- The default is `compact: false`. Existing clients see the same global
  newest-item selection and `max_items` limit.
- `max_incident_response_bytes` is the response byte budget for compact and
  default incident mode. Both trim evidence items to the budget and report
  `byte_size`, `byte_budget`, `byte_budget_truncated`, and `omitted_items`.
  The other evidence tools use the same budget.
- A compact client must read the new omission fields before it claims complete
  coverage.

## Selection

Compact mode uses these steps:

1. Keep one newest item from every source that contributed evidence.
2. Fill the remaining `max_items` slots with the newest evidence across sources.
3. If `max_items` is smaller than the number of contributing sources, keep the
   newest per-source items and report the rest.
4. While the serialized result exceeds the byte budget, drop the oldest item
   from a source that still has another selected item. Keep the last selected
   item of each source until no other choice remains.

The selection is deterministic. Equal timestamps use the documented evidence
order, not opaque identifiers.

The server does not group repeated evidence. Grouping can hide a difference
between observations. Repeated records stay separate until the item or byte
budget omits them. `omitted_items` and `omitted_sources` report the loss.

## Byte accounting

`byte_size` counts both representations that the MCP result carries:

- The pretty-printed JSON text block.
- The compact structured content.
- A fixed envelope allowance for MCP metadata.

This is a serialized payload budget, not exact process memory. The minimum
accepted value is 8192 bytes. The default is 262144 bytes. The maximum is
4194304 bytes. A smaller value is rejected at configuration load because it
cannot hold the envelope for the six source outcomes.

## Fields

- `compact`: the selection mode used.
- `byte_budget`: the configured limit.
- `byte_size`: the accounted result size.
- `selection_truncated`: `max_items` or the per-source reservation omitted items.
- `byte_budget_truncated`: the byte budget omitted items.
- `omitted_items`: total selected losses.
- `omitted_sources`: contributing source kinds with no selected item.
- `truncated`: source or timeline truncation, including selection and byte loss.

`warnings` and source outcomes still describe source limits, discarded records,
and incomplete coverage.

## References

Selected items are stored in the process-local evidence cache and can be read
with `get_evidence`. The cache has a TTL and entry and byte limits. An expired
or evicted reference returns an explicit `not found or expired` error. Increase
`limits.evidence_cache_ttl`, `limits.evidence_cache_max`, or
`limits.evidence_cache_max_bytes` when a workflow reads many references after a
large compact response.
