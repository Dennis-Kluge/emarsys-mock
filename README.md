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

Phase 1 is in place: project skeleton, migrations, seeded system fields, both
authentication schemes, the response envelope and reply-code table, request
logging and the health probe. Contact, field, event, list and export endpoints
follow in the next phases; `/api` paths without a handler answer HTTP 404 inside
a well-formed envelope so a client can tell that apart from a transport failure.
