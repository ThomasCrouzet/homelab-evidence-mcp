# API compatibility

Project support is only for data structures in `httptest` tests. Other
versions can be compatible without project support.

The adapter gives an error when a JSON collection is `null`. It also gives an
error when an envelope does not contain the expected array. For the two cases,
the adapter does not give an empty array without an error.

## Gatus

The Gatus adapter has this behavior:

- The adapter sends a `GET /api/v1/endpoints/statuses` request.
- The response body is an array of objects with `name`, `group`, `key`, and
  `results`.
- Each result contains `success`, `status`, `timestamp`, `errors`, and
  `duration`.
- The adapter accepts `duration` as a nanosecond number (stock Gatus) or a
  string.
- The adapter selects the most recent state by the greatest correct timestamp.
- Tests include empty arrays, empty results, unknown fields, and unordered
  history.

## Docker Engine

The Docker adapter has this behavior:

- The adapter sends a `GET /containers/json?all=true` request.
- The adapter reads `Names`, `Image`, `State`, `Status`, some `Labels`, and
  `Created`.
- The adapter ignores letter case when it compares names. It also ignores a
  leading `/`.
- A network error or invalid JSON does not give an empty array without an error.
- The adapter sends no request to `inspect`, `create`, `start`, `stop`, or
  `exec` endpoints.
- `observed_at` contains the collection time.
- `attributes.snapshot` is `true`.
- A historical window does not include the current snapshot.

## Loki

The Loki adapter has this behavior:

- The adapter sends a `GET /loki/api/v1/query_range` request.
- The request contains `query`, `start`, `end`, `limit`, and
  `direction=forward`.
- The response contains streams with `[nanosecond_timestamp, line]` pairs.
- If the response includes a result type, its value must be `streams`.
- The adapter filters and redacts labels. It uses size limits for the labels.
- The adapter ignores timestamps that are not in the window.
- The adapter uses the limit again if the server exceeds it.
- Status 429 gives an explicit error.

## Healthchecks

The Healthchecks adapter has this behavior:

- The adapter sends a `GET /api/v3/checks/` request.
- The adapter uses the authentication header in the configuration. The default
  header is
  `X-Api-Key`.
- The adapter reads `name`, `slug`, `tags`, `status`, `grace`, `last_ping`, and
  `next_ping`.
- The adapter does not include ping URLs or UUIDs in its output.
- The adapter accepts wrapped responses and raw arrays.
- The Healthchecks list API can show the current state. It cannot show a past
  incident.
- A historical window does not include the current state.

## Beszel

The Beszel adapter has this behavior:

- The adapter first sends a request to
  `/api/collections/systems/records` (PocketBase).
- It then tries `/api/systems`, `/api/beszel/systems`, and the variants with a
  trailing slash.
- The adapter accepts arrays and `systems`, `items`, or `data` envelopes. It
  also accepts empty arrays and envelopes.
- The adapter gets the identity from `name`, `system`, `host`, or `info.h`.
- The `cpu` and `mem` values are optional. Their value can be zero.
- The response contains only a snapshot.
- A historical window does not include the current snapshot.

## ntfy

The ntfy adapter has this behavior:

- The adapter sends a `GET /{topic}/json?poll=1&since=<unix>` request.
- The adapter stores the message body with a size limit in
  `attributes.message`. The summary contains a shorter view.
- The adapter reads the topic only from the configuration.
- The adapter accepts NDJSON and JSON arrays.
- The adapter reads `id`, `time`, `event`, `message`, `title`, `priority`, and
  `tags`.
- The adapter ignores events other than `message`.
- An invalid or truncated NDJSON line gives an explicit source error.
- The adapter sorts messages before it uses the limit.
