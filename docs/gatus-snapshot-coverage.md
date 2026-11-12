# Gatus snapshot coverage

`service_status` returns the latest Gatus result for a bound endpoint. The
selected result is evidence. It does not prove that the complete endpoint
snapshot was usable.

## Selection

The adapter selects the result with the greatest parseable `timestamp`. Array
order does not decide the selection while the greatest timestamps differ. A
result with an unusable timestamp cannot win the selection.

If no result has a usable timestamp, the adapter keeps the first result. The
item then reports `source_time_known: false`,
`collection_time_derived: true`, and `freshness: unknown`. The collection time
is not an event time.

An absent `success` field does not create a success value. The item keeps
`success_known: false` and `severity: unknown`. An explicit `success: false`
stays a failure.

## Coverage rule

The coverage rule is conservative. The adapter counts every result on the
selected endpoint that it cannot interpret:

- The `timestamp` is absent or cannot be parsed.
- The `success` field is absent.

The source outcome reports:

- `discarded`: the count of these records.
- `reason: partial_invalid_records`.
- `complete: false`.

The count includes the selected result when that result itself has no usable
`success` field. It also includes every other unusable record on the endpoint.
The selected evidence stays available in the same response.

A reversed result order changes neither the selected result nor `discarded`,
`reason`, or `complete` while the greatest timestamps differ. When two records
share the greatest timestamp, the first row wins. That tie behavior is
unchanged.

## Empty and undated endpoints

An endpoint with an empty `results` list reports `reason: empty_result`. The
item has no `success_known` attribute because no result exists. It keeps
`severity: unknown` and `freshness: unknown`. Coverage is complete because the
adapter read the full empty list. The endpoint state is unknown, not healthy.

An endpoint whose results are all undated increments `discarded` for every
result. It reports `reason: partial_invalid_records` and `complete: false`.
The evidence shows an unknown source time. When the selected record has no
`success` field, the same item reports `success_known: false`.

## Complete snapshots

A snapshot with a usable timestamp and a `success` field on every result keeps
`complete: true`. It does not report `discarded` or
`partial_invalid_records`.

## Doctor consequence

A Gatus binding with an invalid sibling reports `partial` with
`partial_invalid_records` and `complete: false`. The doctor then exits 1 for
that binding. An all-valid endpoint stays `ok`. This result follows the
existing diagnostic completeness rule: an incomplete binding is not a healthy
binding. It is intended, not a regression in the doctor logic.

## Limit

This rule describes the records that Gatus returned at collection time. It
does not prove that Gatus retained every record for the endpoint. A windowed
Gatus query keeps the separate `retention_limited` limit.
