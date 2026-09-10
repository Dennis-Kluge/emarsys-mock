# emarsys-mock

An API-compatible mock of the SAP Emarsys Suite API, plus a dashboard for
inspecting and editing the mocked data.

Emarsys ships production-only: there is no sandbox, and a second licensed
instance is expensive. This service closes that gap so integrations can be
tested in CI and locally without touching production data.

## What it is

A deterministic contract simulator: authentication, request validation,
persistence, response shapes, reply codes, rate limits and asynchronous export
jobs. It reproduces the behaviour that real integrations trip over — numeric
field ids, partial batch failures returned under HTTP 200, opt-in semantics,
duplicate key identifiers.

## What it is not

It does not reimplement Emarsys' business logic, and it should not try to.
Segmentation, Automation Center programs, e-mail rendering and delivery, and
Predict recommendations are all out of scope. Segments exist as CRUD objects
with a manually maintained member list; an event trigger is recorded and
acknowledged, and nothing happens afterwards.

## Quickstart

```sh
make build && ./bin/emarsys-mock         # listens on :8080, in-memory database
curl localhost:8080/_ctl/health
```

Point an integration at `http://localhost:8080/api` instead of
`https://api.emarsys.net/api` and sign requests with the seeded credentials
(`mock-api-user` / `mock-secret`, or `mock-client` / `mock-client-secret` for
OAuth2). Nothing else about the client changes.

```sh
# WSSE, the way a v2 client authenticates
NONCE=$(openssl rand -hex 16)
CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
DIGEST=$(printf '%s%s%s' "$NONCE" "$CREATED" mock-secret | sha1sum | cut -d' ' -f1 | tr -d '\n' | base64)
curl -H "X-WSSE: UsernameToken Username=\"mock-api-user\", PasswordDigest=\"$DIGEST\", Nonce=\"$NONCE\", Created=\"$CREATED\"" \
     localhost:8080/api/v2/field

# OAuth2 client credentials, the way a v3 client authenticates
TOKEN=$(curl -s -u mock-client:mock-client-secret \
  -d grant_type=client_credentials localhost:8080/oauth2/token | jq -r .access_token)
curl -H "Authorization: Bearer $TOKEN" localhost:8080/api/v3/contacts
```

## Configuration

Everything is an environment variable with a working default, so the service
runs unconfigured.

| Variable | Default | Purpose |
|---|---|---|
| `EMARSYS_MOCK_ADDR` | `:8080` | Listen address |
| `EMARSYS_MOCK_DB` | `:memory:` | SQLite path, or `:memory:` for an ephemeral database |
| `WSSE_SKEW_SECONDS` | `300` | Accepted age of the WSSE `Created` timestamp |
| `WSSE_REJECT_NONCE_REUSE` | `false` | Reject replayed nonces. Off by default because retrying with the same nonce is realistic client behaviour |
| `OAUTH_TOKEN_TTL_SECONDS` | `3600` | Lifetime of issued access tokens |
| `OAUTH_SIGNING_KEY` | random per process | Fix this to keep tokens valid across restarts |
| `CTL_TOKEN` | unset | Token guarding `/_ctl` and `/admin`. Unset means localhost only |
| `READONLY` | `false` | Serve the dashboard without any write action |
| `RATE_LIMIT_PER_MINUTE` | `1000` | Lower it in CI to exercise retry and backoff logic |
| `MAX_BATCH_CONTACTS` | `1000` | Contacts per batch before replyCode 1000 |
| `MAX_BODY_BYTES` | `10485760` | General payload limit |
| `MAX_CONTACT_BODY_BYTES` | `8388608` | Contact batch payload limit |
| `REQUEST_LOG_MAX` | `10000` | Request log ring size |
| `EXPORT_POLLS_BEFORE_DONE` | `2` | Polls an export returns `in progress` before flipping to `done` |
| `EXPORT_TIMEZONE` | `Europe/Vienna` | Timezone for export timestamps |
| `WEBHOOK_URL` | unset | Outbound webhook fired on event triggers |

## Control plane

`/_ctl` is ours and answers plain JSON rather than a replyCode envelope, so a
response is never mistaken for an Emarsys one. Without a `CTL_TOKEN` it is
reachable only from localhost; with one it works from anywhere, via the
`X-Ctl-Token` header, a bearer token, a `?token=` parameter or a `ctl_token`
cookie.

| Endpoint | Purpose |
|---|---|
| `GET /_ctl/health` | Liveness and the effective configuration. Needs no credentials |
| `POST /_ctl/reset` | Back to the seeded state. This is what a test calls between cases |
| `POST /_ctl/seed` | Load fixtures. The `fields` block takes a production `GET /v2/field` response unchanged |
| `GET /_ctl/requests` | The request log, filterable by `since`, `path`, `method` and `status` |
| `GET /_ctl/events/triggers` | Recorded event triggers with their payloads |
| `GET`/`POST /_ctl/faults`, `DELETE /_ctl/faults/{id}` | Fault rules |
| `POST /_ctl/exports/{id}/status` | Force an export job into a state |

### Fault injection

A fault rule matches on method, a path glob and a body substring, and forces a
response: HTTP status, replyCode and replyText. `probability` below 1 makes it
flaky; `remaining_hits` spends it after a fixed number of requests.

```sh
# the next API request fails with 429, then the rule retires itself
curl -X POST localhost:8080/_ctl/faults -d '{
  "match_path_pattern": "/api/*",
  "http_status": 429,
  "reply_code": 2011,
  "reply_text": "Rate limit exceeded",
  "remaining_hits": 1
}'
```

This is a core feature, not an extra. Without it a suite only ever exercises the
happy path, and the retry, backoff and error-handling code of an integration --
the part most likely to be wrong -- is never run at all.

Rules apply only to `/api`. One that could break `/_ctl` would make the mock
unrecoverable, because the endpoint needed to delete the rule would be the one
failing.

## Dashboard

`/admin` is a server-rendered debugging tool, guarded by the same token as the
control plane. There is no build step and no CDN: the stylesheet and a vendored
htmx are embedded in the binary, so the single-file deployment survives contact
with the UI.

| View | What it is for |
|---|---|
| Requests | The log, filterable by path, method, HTTP status and replyCode, with both bodies expandable. "Go live" tails it in place |
| Contacts | Search, plus a detail page with every field value inline-editable and the full change history |
| Fields | The catalogue: create and delete custom fields, manage choices, toggle the index flag that decides whether `contact/query` accepts a field |
| Lists & segments | Membership, by contact id or e-mail |
| Events | Defined events plus every received trigger with its payload expanded |
| Exports | Job state, forcing a job to done, and the CSV |
| Faults | Active rules, and one button for "next request → 429" |
| Danger zone | Reset and fixture loading |

Field ids are shown next to every field name, because the id is what an
integration sends and what an error message names.

`READONLY=1` hides every write control and refuses the writes server-side, so a
shared instance can be handed out without anyone resetting it under a
colleague's running test. It guards our own surface only — the Emarsys endpoints
keep working, or the mock would be useless for the tests it exists to serve.

## Two separate surfaces

`/api/v2/...` and `/api/v3/...` must match production byte for byte. `/_ctl`
(JSON) and `/admin` (HTML) are ours and have no Emarsys counterpart; the
deliberately unlikely `_ctl` prefix guarantees it can never collide with a real
path.

## Deployment

One static binary, no CGO, no runtime dependencies. `make build` produces it;
`make build-linux-arm64` proves the cross-compile. The Dockerfile's final stage
is `scratch`.

The one deployment trap is the timezone: Emarsys renders export timestamps in
Vienna local time, and a scratch image has no `/usr/share/zoneinfo`. The binary
embeds the IANA database via `time/tzdata` (about 400 KB) so this works in the
container exactly as it does locally.

## Development

```sh
make check   # vet, gofmt check, tests
make race    # tests under the race detector
```

Handlers are written against `docs/api-reference.md`, which is distilled from
the official Postman collections. Check the shape there before adding or
changing an endpoint rather than guessing paths and bodies — the documentation
is less precise than the real behaviour in several places, and a mock that only
knows the docs gives false confidence.

## Status

All six phases are in place.

- Skeleton, migrations, seeded system fields, both authentication schemes, the
  response envelope and reply-code table, request logging, health.
- Contact endpoints: create, update, upsert, getdata, query, checkids, delete
  and last_change, with the batch partial-failure semantics and every behaviour
  from section 8 of the briefing covered by a named test.
- Field endpoints: list, create, delete, choices.
- External events: CRUD, trigger with per-contact `event_time` and `trigger_id`,
  idempotency on `trigger_id`, and an optional outbound webhook.
- Contact lists, segments as a static-membership facade, and asynchronous
  exports with the polling state machine.
- Control plane with fault injection, per-caller rate limiting, and the
  dashboard.

Not built, and deliberately so: the `/api/v3` surface. It is not simply v2 with
a different authentication scheme — several endpoints have different payload
shapes — so it needs to be verified against the v3 Postman collection rather
than assumed.

Response shapes are pinned by golden files in `internal/server/testdata`.
Regenerate them with `go test ./internal/server -update` and read the diff before
committing: a change there is a change to the contract.
