# emarsys-mock

An API-compatible mock of the SAP Emarsys Suite API, plus a dashboard for
inspecting the mocked data and a proxy for recording real traffic.

Emarsys ships production-only: there is no sandbox, and a second licensed
instance is expensive. This service closes that gap so integrations can be
tested in CI and locally without touching production data.

## The one rule everything else follows

**Never derive the contract from prose.** The Emarsys documentation is less
precise than the API in several places, and a contract simulator that only knows
the docs gives false confidence — which is worse than no mock at all, because it
fails silently and in production.

Legitimate sources, in order:

1. A recording of real traffic (`emarsys-record`, see below) — the best source.
2. The official v2 Postman collection, distilled into `docs/api-reference.md`.
3. Nothing else. If neither settles a question, add it to the "Decisions the
   sources do not settle" table in `docs/api-reference.md` with the reasoning,
   and make the mock's behaviour obvious rather than plausible.

Every correction in `docs/api-reference.md` came from an artefact. Keep it that
way.

## What this is not

The mock does not reimplement Emarsys' business logic and should not try to.
Out of scope: the segmentation engine, Automation Center programs, e-mail
rendering and delivery, Predict. Segments exist as CRUD objects with a manually
maintained member list; an event trigger is recorded and acknowledged, and
nothing happens afterwards. Extending into any of these means guessing at
behaviour nobody can verify.

## Behaviours that are the point of the project

These are where real integrations fail against production. If any of them takes
the happy path, the mock is worthless. Each has a named test in
`internal/server/behaviour_test.go`; do not "simplify" one away.

1. `key_id` names a field by numeric id. A field name is replyCode 2004.
2. Opt-in (field 31) is single-choice: `1` and `2` only. `"true"`, `"false"`
   and `0` are rejected.
3. An empty value **overwrites**, it does not skip. This is how an integration
   that sends a field it did not mean to send wipes consent data.
4. An update replaces only the fields it was sent.
5. An upsert reports an updated contact's id as a **string** and a created one's
   as an **integer**.
6. Date fields take `YYYY-MM-DD` and nothing else.
7. An ambiguous key value is 2010, whether duplicated inside the batch or
   already in the database.
8. `contact/query` works only on indexed fields, else 2015.
9. Timestamps render in Vienna local time as `YYYY-MM-DD HH:MM:SS`, no offset,
   no zone marker. Stored data is UTC; conversion happens at the edge.
10. Rate limiting answers 429 with `X-RateLimit-*` and `Retry-After`.

And the rule that outranks all of them: **a batch with failing rows is HTTP 200
with replyCode 0**, failures inside `data.errors` keyed by the row's key value.
A client checking only the top-level reply code reads that as success.

## Layout

| Package | Holds |
|---|---|
| `cmd/emarsys-mock` | the server binary |
| `cmd/emarsys-record` | the recording proxy and its summariser |
| `internal/api` | the response envelope and the reply-code vocabulary |
| `internal/auth` | WSSE and OAuth2, free of database types |
| `internal/config` | env-var configuration, every value with a default |
| `internal/httpx` | logging, body limits, rate limiting, panic recovery |
| `internal/store` | SQLite: schema, migrations, queries |
| `internal/server` | handlers, routing, control plane, dashboard |
| `internal/record` | redaction, shape derivation, the proxy |
| `internal/webhook` | outbound notifications on event triggers |

Two HTTP surfaces, never mixed:

- `/api/v2/...` must match production byte for byte and answers the replyCode
  envelope.
- `/_ctl` (JSON) and `/admin` (HTML) are ours, answer plain JSON or HTML, and
  have no Emarsys counterpart. The `_ctl` prefix is deliberately unlikely so it
  can never collide with a real path.

## Conventions

- **Reply codes live in one table** (`internal/api/replycode.go`). Adding a code
  production returns is a one-line change there, never a handler change.
- **Migrations are numbered and never edited once committed.** A change goes
  into a new file.
- **Golden files pin the wire format** (`internal/server/testdata`). Regenerate
  with `go test ./internal/server -update` and *read the diff before committing*:
  a change there is a change to the contract, including the JSON type of an id.
- **Handlers validate through the store's field catalogue**, so an edit the API
  would refuse is refused everywhere, including from the dashboard.
- **English throughout** — code, comments, commit messages, UI.
- **Dependencies**: one direct (`modernc.org/sqlite`). Adding another needs a
  reason. CGO must stay off or the deployment story breaks.

## Commands

```sh
make check     # vet, gofmt check, tests
make race      # tests under the race detector
make build     # both binaries, static, CGO off
make run       # go run the server
./scripts/smoke.sh [base-url]   # the definition of done against a running instance
```

The server needs no configuration: `./bin/emarsys-mock` listens on `:8080` with
an in-memory database seeded with the system fields. Credentials are
`mock-api-user` / `mock-secret` (WSSE) and `mock-client` /
`mock-client-secret` (OAuth2).

`/_ctl` and `/admin` are localhost-only unless `CTL_TOKEN` is set. From outside a
container a request does not arrive over loopback, so a container needs the
token.

## Traps that have already cost time

- **`pkill -f "go test"` matches the agent's own shell** and kills the session.
  Use a captured PID.
- **A plain `:memory:` DSN gives every pooled connection its own database.**
  `store.Open` handles this; do not "simplify" the shared-cache naming away.
- **The write pool holds exactly one connection.** Asking the pool for a second
  while holding one deadlocks — that is why `Reset` releases its connection
  before calling `Migrate`.
- **`url.Values` is a map.** Copying the variable aliases it; a `Del` on the
  copy strips the key from the original.
- **`time/tzdata` must stay imported in `main`.** A scratch image has no
  `/usr/share/zoneinfo`, and without the embed `Europe/Vienna` fails at startup.
- **The race detector makes timing assertions meaningless.** The reset budget
  has a separate value under the `race` build tag.
- **Inside an iframe, `prefers-color-scheme` follows the OS**, not a theme choice
  made by the host page.

## Where to look first

- `docs/api-reference.md` — the observed contract, the details that differ from
  intuition, and every point the sources do not settle.
- `docs/HANDOVER.md` — project status, decisions taken and why, what is open.
