# Handover

Where the project stands, what was decided and why, and what is open. Read this
once before changing anything; `CLAUDE.md` carries the rules that apply while
working.

Written at the end of the cloud session that built phases 1–6, on branch
`claude/emarsys-staging-mock-2dq1zp`.

## Status

All six phases of the original briefing are in place and pushed.

| | |
|---|---|
| Tests | 319, green, also under `-race` |
| Emarsys endpoints wired | 31 under `/api/v2` |
| Control plane | 9 endpoints under `/_ctl` |
| Dashboard | 8 views under `/admin` |
| Direct dependencies | 1 (`modernc.org/sqlite`) |
| Binary | ~14 MB, static, CGO off, cross-compiles to darwin/linux/windows |

Working tree clean, everything committed. One commit per phase, so
`git log --oneline` reads as the build order.

## Decisions taken

These were put to the project owner and answered. Do not relitigate them without
asking.

| Question | Decision | Why |
|---|---|---|
| Outbound on event triggers | **Generic webhook**, not Pub/Sub | Keeps the mock free of a cloud dependency; a Pub/Sub adapter can subscribe to the webhook in twenty lines. In CI the target is an `httptest` server. |
| Multi-tenancy | **Single tenant** | One instance per test run. Tenant-scoped queries would complicate every read, every reset and every dashboard view, and process isolation is more reliable than row isolation. |
| Read-only mode | **Yes, via `READONLY`** | A shared dev instance can be handed out without anyone resetting it under a colleague's running test. It guards `/_ctl` and `/admin` only — blocking the Emarsys surface would make the mock useless for its purpose. |
| Language | **English throughout** | Code, comments, commits, UI. |

Technical calls worth knowing:

- **`modernc.org/sqlite`, not `mattn/go-sqlite3`.** The latter needs CGO, which
  breaks cross-compilation and the scratch image.
- **Hand-rolled HS256 JWT** rather than a JWT dependency. Forty lines of
  standard library against a dependency in a project whose selling point is a
  single static binary.
- **Exports are snapshotted at creation** and only pretend to take time. What
  integrations need to exercise is their polling loop, not file generation.
- **Segments store `criteria` and never evaluate it.** See `CLAUDE.md`.

## Deliberately not built: the `/api/v3` endpoints

v3 **authentication** is done and tested: the token endpoint, bearer
verification and per-user permissions all work. A v3 client authenticates today
and then gets an honest 404:

```
GET /api/v3/contacts   (no token)      401  replyCode 1
GET /api/v3/contacts   (valid bearer)  404  replyCode 2011
```

The handlers are missing because **no machine-readable v3 reference could be
found**. `emartech/Emarsys-postman-collection`, named in the original briefing,
does not resolve — not the collection under any obvious filename, not even a
README — while the v2 collection in `developer-hub-public-assets` returns 200
from the same host. The absence is real, not a blocked proxy. The briefing took
that reference from a research report; the report was wrong there.

v3 is also not v2 with different authentication — several endpoints have
different payload shapes — so the handlers cannot be derived from the v2
collection either.

**How to unblock it:** run `emarsys-record` in front of the real v3 traffic for
an afternoon. Its summary reports each endpoint's request and response shape,
which fields are optional, and every field that came back as more than one JSON
type. That summary is the specification. Storage, validation, the envelope and
the reply codes are all in place and shared, so the handlers themselves are a
small job afterwards.

Rehearsed against the mock itself, the recorder independently rediscovered the
mixed `ids` types on upsert and the way `data` degrades to a string on an error
— reasonable confidence it will find the same class of detail in v3.

## Verify against production before relying on these

`docs/api-reference.md` lists sixteen points the sources do not settle, each with
the decision taken and the reasoning. Three are worth a recorded prod response
before an integration depends on them:

1. **Mixed id types on upsert** — updated contact returns a string id, created
   one an integer. This came from the briefing; the collection's example shows
   only strings. `internal/server/testdata/contact_upsert.json` is the single
   place to change.
2. **`POST /v2/contact/getid`** — absent from the official collection entirely.
   Served as an alias of `checkids` because integrations written against older
   documentation call it, but the shape is unverified.
3. **The reply code for an invalid field *value*** (bad date, undefined choice).
   The mock uses 2006 with an interpolated message; 2006 is documented as
   "invalid field id". Reusing it with a clear text beat inventing a number.

Also unconfirmed and lower-stakes: the seeded field catalogue holds only the
system fields that are stable across accounts (1–5, 31). The real catalogue
belongs in via `POST /_ctl/seed`, whose `fields` block takes a production
`GET /v2/field` response unchanged. Guessing a customer's custom fields would be
worse than importing them.

## What the cloud session could not do

Three things were out of reach there and are trivial locally. They are not
known-broken — they are unverified.

- **Building the container image.** No Docker daemon in that sandbox. CI builds
  it and runs `scripts/smoke.sh` against the running container, which is the
  only place the embedded timezone database and the absence of a shell are
  really exercised. Worth running `make docker` once locally to confirm.
- **Reading the integration repositories.** Adding another repository to the
  session was blocked, so which Emarsys API your integrations actually speak is
  still unknown. A `grep -rn "api.emarsys.net\|X-WSSE\|oauth2/token"` over
  `global-email-delivery-microservice` answers it in one command, and decides
  how urgent v3 is.
- **Reaching the running server from outside.** Hence the published snapshot of
  the dashboard rather than a live link.

## Next steps, in order

1. `make check && make docker` — confirm the toolchain and the image locally.
2. Find out which API version the integrations use (the grep above). If they are
   all v2/WSSE, v3 is future-proofing rather than a gap.
3. Point one real integration at the mock, base URL only, and see what breaks.
   That is the first genuine test of the contract, and anything it turns up
   belongs in `docs/api-reference.md`.
4. Import the production field catalogue via `POST /_ctl/seed`.
5. If v3 is needed: record, summarise, then build the handlers against the
   summary.
6. Deployment, if the team should reach a shared instance. Cloud Run fits — one
   container, no volume — and needs `CTL_TOKEN` plus a decision on whether it
   sits behind IAP.

## Starting a session on this

A prompt that lands in the right place:

> Read CLAUDE.md and docs/HANDOVER.md. Then <task>. Follow the sourcing rule:
> nothing about the Emarsys contract gets derived from prose — if the collection
> or a recording does not settle it, add it to the unsettled table with the
> reasoning instead of guessing.
