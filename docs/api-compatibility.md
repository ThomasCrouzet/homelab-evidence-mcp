# API compatibility

Only shapes covered by `httptest` tests are guaranteed. Other versions may work
without being officially supported. A JSON collection that is `null` or an
envelope missing its expected array produces an explicit error; it does not
become a successful empty list.

## Gatus

- Route: `GET /api/v1/endpoints/statuses`.
- Body: array of objects with `name`, `group`, `key`, and `results`.
- Result: `success`, `status`, `timestamp`, `errors`, and `duration`.
- `duration` is accepted as a nanosecond number (stock Gatus) or as a string.
- The latest state is chosen by the greatest valid timestamp.
- Empty lists, empty results, unknown fields, and unordered history are tested.

## Docker Engine

- Route: `GET /containers/json?all=true`.
- Fields read: `Names`, `Image`, `State`, `Status`, some `Labels`, and `Created`.
- Name comparison is case-insensitive and ignores a leading `/`.
- A network error or invalid JSON never becomes a successful empty list.
- No inspect, create, start, stop, or exec endpoint is called.
- `observed_at` is the collection time; `attributes.snapshot` is `true`.
- A historical window excludes this current snapshot.

## Loki

- Route: `GET /loki/api/v1/query_range`.
- Parameters: `query`, `start`, `end`, `limit`, and `direction=forward`.
- Response: streams containing `[nanosecond_timestamp, line]` pairs.
- When present, the result type must be `streams`.
- Labels are filtered, redacted, and bounded.
- Timestamps outside the window are ignored.
- The limit is reapplied locally if the server exceeds it.
- Status 429 produces an explicit error.

## Healthchecks

- Route: `GET /api/v3/checks/`.
- Authentication via the configured header, `X-Api-Key` by default.
- Fields used: `name`, `slug`, `tags`, `status`, `grace`, `last_ping`, and `next_ping`.
- Ping URLs and UUIDs are never emitted.
- Wrapped responses and raw arrays are accepted.
- The list can expose the current state, not prove a past incident.
- A historical window excludes this current state.

## Beszel

- Routes tried: `/api/collections/systems/records` (PocketBase), then `/api/systems`, `/api/beszel/systems`, and trailing-slash variants.
- Arrays and `systems`, `items`, or `data` envelopes accepted, including empty ones.
- Identity via `name`, `system`, `host`, or `info.h`.
- Optional `cpu` and `mem` values, including zero.
- Snapshot only.
- A historical window excludes this current snapshot.

## ntfy

- Route: `GET /{topic}/json?poll=1&since=<unix>`.
- The bounded message body is kept in `attributes.message`; the summary is a shorter view.
- Topic defined only in configuration.
- NDJSON and JSON arrays accepted.
- Fields: `id`, `time`, `event`, `message`, `title`, `priority`, and `tags`.
- Events other than `message` are ignored.
- An invalid or truncated NDJSON line produces an explicit source error.
- Messages are sorted before the limit is applied.
