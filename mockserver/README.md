# Mock backend

A single, dependency-free Go service that emulates every component the Dynatrace OpenFeature
providers talk to — the **CDN** config endpoint, the **metrics** ingest endpoint, and an **SSE**
stream. It also exposes an HTTP **control plane** to script responses and inspect
what the provider sent.

## Run

```bash
go run .                 # listens on :8080 (override with PORT)
go test ./...            # unit tests (in-memory, no sockets)
```

Docker (local build):

```bash
docker build -t fm-provider-mock-backend:dev .
docker run --rm -p 8080:8080 fm-provider-mock-backend:dev
```

## Published image

Releases are cut by [release-please](../.release-please-config.json) and each release publishes a
multi-arch (`linux/amd64,linux/arm64`) image to GHCR:

```
ghcr.io/dynatrace/fm-provider-mock-backend:vX.Y.Z
ghcr.io/dynatrace/fm-provider-mock-backend:latest
```

Provider suites embed this repo as a git submodule and **read the matching version from
[`version.txt`](../version.txt)** (bumped atomically with the release tag), so the image can never
drift from the spec commit the submodule is pinned to. Wire that value into the harness's image
override (e.g. `MOCKSERVER_IMAGE` / `-Dmockserver.image=...`), for example:

```bash
docker pull "ghcr.io/dynatrace/fm-provider-mock-backend:v$(cat version.txt)"
```

## Provider-facing endpoints

| Method | Path                 | Behavior                                                                         |
|--------|----------------------|----------------------------------------------------------------------------------|
| `GET`  | `/server/{key}.json` | Serves the programmed CDN response sequence and records the request (path + headers). |
| `POST` | `/v1/metrics`        | Metrics sink — always `202`; records the request (path + headers + body) for inspection. |
| `GET`  | `/sse`               | SSE stream (`text/event-stream`); delivers messages pushed via the control plane. |

## Control plane (`/__control__`)

| Method | Path              | Body / Result                                                        |
|--------|-------------------|----------------------------------------------------------------------|
| `GET`  | `/health`         | `200 {"status":"ok"}` — Testcontainers wait target.                  |
| `POST` | `/reset`          | Clears the response program, cursor and recorded CDN + metrics requests. `204`. |
| `PUT`  | `/cdn/responses`  | Sets the ordered CDN response program. `204`.                        |
| `GET`  | `/cdn/requests`   | Returns recorded CDN requests since the last reset. `200`.           |
| `GET`  | `/metrics/requests` | Returns recorded metrics-ingest requests since the last reset. `200`. |
| `POST` | `/sse/emit`       | Pushes an SSE message to connected subscribers. `204`.               |

### `PUT /__control__/cdn/responses`

```json
{
  "responses": [
    { "status": 200, "body": "{\"flags\":{}}", "headers": { "Last-Modified": "Tue, 02 Jan 2024 00:00:00 GMT" } },
    { "status": 304, "headers": {} }
  ],
  "repeatLast": true
}
```

Each incoming `GET /server/*` consumes the next entry in order. When the list is exhausted the last
entry is served repeatedly if `repeatLast` is `true` (the default); otherwise the backend returns
`404`. This queue model lets a single fetch that retries internally (e.g. `500` then `200`) consume
several entries in one go.

- `body` — optional. Omit for *no* body; use `""` for an explicitly empty body.
- `headers` — returned verbatim (e.g. `ETag`, `Last-Modified`, `Retry-After`).
- `status` — defaults to `200` if omitted.

Re-programming mid-scenario rewinds the cursor but **preserves** recorded requests, so a step can
swap the response and still count fetches across the change. Use `/reset` to clear everything.

### `GET /__control__/cdn/requests`

```json
{
  "requests": [
    { "method": "GET", "path": "/server/dt01.server_us_....json",
      "headers": { "if-none-match": "\"v1\"", "if-modified-since": "Tue, 02 Jan 2024 00:00:00 GMT" } }
  ]
}
```

Requests are returned in arrival order; **header names are lower-cased**. Used to assert the initial
fetch is unconditional and that it targets `/server/{key}.json`.

### `GET /__control__/metrics/requests`

```json
{
  "requests": [
    { "method": "POST", "path": "/v1/metrics",
      "headers": { "content-type": "application/json", "authorization": "Api-Token dt0c01...." },
      "body": "{\"metrics\":[{\"key\":\"flag.a\"}]}" }
  ]
}
```

Every `POST /v1/metrics` is recorded in arrival order. Like the CDN log, **header names are
lower-cased**; the raw request `body` is captured verbatim as a string so a step can assert on the
exact payload the provider ingested. The endpoint still always responds `202` to the provider.

### `POST /__control__/sse/emit`

```json
{ "type": "refetchConfig", "etag": "\"v2\"", "lastModified": 1735776000 }
```

Broadcasts the JSON object as the `data:` payload of an SSE event to every connected `/sse`
subscriber. Not required by the startup/fetching spec (which drives refreshes via the provider's
poll) but part of the shared contract for the SSE spec.

## Design notes

- Standard library only — no third-party modules — so the image is tiny and builds anywhere.
- All state is per-process and guarded by a mutex; `POST /__control__/reset` gives each scenario a
  clean slate. Run one container per suite (or reset between scenarios).
- The CDN handler matches any path under `/server/`, so the provider's geo/key-derived path is
  recorded verbatim for assertions without the backend needing to know the key.
