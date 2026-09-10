# Emarsys Suite API — observed contract

Distilled from the official Postman collections. Whenever a handler is written or
changed, check the shape here first — do not guess paths or bodies.

Sources:

- v2 / WSSE: `emartech/developer-hub-public-assets` →
  `resources/EmarsysV2PostmanCollection.json` (base URL `https://api.emarsys.net/api`).
  Verified reachable; everything below is distilled from it.
- v3 / OIDC: **no machine-readable source found.** `emartech/Emarsys-postman-collection`,
  named in the original briefing, does not resolve — not the collection file under any
  of the obvious names, not even a README — and `developer-hub-public-assets` carries
  only the v2 collection. See "The v3 surface" below.

## Base URL

`https://api.emarsys.net/api` + `/v2/...`, i.e. the full path is `/api/v2/contact`.
The mock mirrors this: it serves `/api/v2/...`, so switching an integration over is a
base-URL change only.

## Envelope

Success and per-row-error responses:

```json
{ "replyCode": 0, "replyText": "OK", "data": { } }
```

Hard errors keep the same envelope but `data` degrades to a string (usually empty).
`replyCode` is independent of the HTTP status.

## Details that differ from intuition

These come straight from the collection and are the reason the mock exists.

| Detail | Reality |
|---|---|
| Export status | `"in progress"` — with a space, not `in_progress` |
| `getdata` key casing | `keyId` / `keyValues` / `fields` (camelCase) |
| `contact` create/update key casing | `key_id` / `contacts` (snake_case) |
| Event trigger overrides | `event_time`, `trigger_id`, `attachment`, `external_id` live **inside** each `contacts[]` entry |
| `checkids` | `data.ids` is an object keyed by external id, not an array |
| PUT contact `data.ids` | example returns strings; POST returns integers |
| Segment create | `PUT /v2/filter`, not POST |
| Segment delete | `GET /v2/filter/{segmentId}/delete` — a GET that mutates |
| List delete | `/contactlist/{id}/delete` removes *contacts from* the list; `/deletelist` removes the list |
| `getdata` result rows | carry `id` and `uid` alongside the numeric field keys |

## The v3 surface

The mock speaks v3's **authentication** but serves none of its **endpoints**.

Done and tested:

- `POST /oauth2/token`, client-credentials grant, issuing a signed HS256 token
- bearer verification on every `/api/**` request, with the same per-user
  endpoint permissions as WSSE
- the 8 MB contact-batch body limit recognises `/api/v3/contacts`

So a v3 client authenticates successfully today and then gets an honest 404
inside a well-formed envelope:

```
GET /api/v3/contacts   (no token)      401  replyCode 1
GET /api/v3/contacts   (valid bearer)  404  replyCode 2011
                                            "Endpoint not implemented by emarsys-mock"
```

What is missing is every handler, and it is missing deliberately. v3 is not v2
with a different authentication scheme — several endpoints have different
payload shapes — so the handlers cannot be derived from the v2 collection. With
no reachable v3 collection, writing them would mean guessing from prose
documentation, which is exactly the failure mode this mock exists to avoid: a
contract simulator that only knows the docs gives false confidence, and false
confidence is worse than a 404 that says what it is.

To close the gap, one of these is needed:

1. The official v3 Postman collection, if it exists somewhere reachable.
2. A recording from the production account. `emarsys-record` in this repository
   does exactly that: it proxies the real calls, pseudonymises the payloads, and
   its summary reports each endpoint's request and response shape plus every
   field that came back as more than one JSON type. That is the more reliable
   source anyway — the documentation is less precise than the real behaviour in
   several places, and every correction in this file came from an artefact
   rather than from prose.

   Pointed at the mock itself as a rehearsal, it independently rediscovered the
   mixed `ids` types on upsert and the way `data` degrades to a string on an
   error, which is a reasonable amount of confidence that it will find the same
   class of detail in v3.

Then the handlers are a small job: the storage, validation, envelope and reply
codes are all in place and shared.

## Endpoint inventory (relevant subset)

```
GET    /v2/contact/query/?{fieldId}=<value>&return=<fieldId>&excludeempty=&limit=&offset=
POST   /v2/contact                      body: {contact_list_id?, key_id, contacts[]}
PUT    /v2/contact/?create_if_not_exists=0|1
POST   /v2/contact/getdata              body: {contact_list_id?, keyId, keyValues[], fields[]}
POST   /v2/contact/delete
POST   /v2/contact/checkids             body: {key_id, external_ids[], contact_list_id?, get_multiple_ids}
POST   /v2/contact/merge
POST   /v2/contact/last_change          body: {keyId, keyValues[], fieldId}
POST   /v2/contact/getchanges           body: {distribution_method, time_range[], contact_fields[], ...}
POST   /v2/contact/getregistrations
POST   /v2/contact/getcontacthistory

POST   /v2/field                        create a custom field
DELETE /v2/field/{fieldId}
GET    /v2/field/translate/{languageId} list fields: [{id, name, application_type}]
GET    /v2/field/choices?fields=&language=
GET    /v2/field/{fieldId}/choice/translate/{languageId}

POST   /v2/event                        body: {name} -> {id, name}
GET    /v2/event/                       -> [{id, name, created}]
GET    /v2/event/{eventId}
POST   /v2/event/{eventId}              rename
POST   /v2/event/{eventId}/delete
POST   /v2/event/{eventId}/trigger      body: {key_id, contacts[], data?}
GET    /v2/event/{eventId}/usages

POST   /v2/contactlist                  create
GET    /v2/contactlist                  list
GET    /v2/contactlist/{listId}/?offset=&limit=
GET    /v2/contactlist/{listId}/count
GET    /v2/contactlist/{listId}/contacts/
GET    /v2/contactlist/{listId}/contacts/data?fields=&limit=&offset=
POST   /v2/contactlist/{listId}/add
POST   /v2/contactlist/{listId}/delete
POST   /v2/contactlist/{listId}/replace
POST   /v2/contactlist/{listId}/rename
POST   /v2/contactlist/{listId}/deletelist

GET    /v2/export/{exportId}            -> {id, created, status, type, file_name, ftp_host, ftp_dir}
GET    /v2/export/{exportId}/data?offset=&limit=
POST   /v2/export/filter

PUT    /v2/filter                       create segment
GET    /v2/filter/{segmentId}
GET    /v2/filter/{segmentId}/delete
GET    /v2/filter/{segmentId}/contacts/count
POST   /v2/filter/{segmentId}/runs
```

## Decisions the sources do not settle

The Postman collections pin paths and shapes but not every behaviour. The
points below were decided deliberately; each is cheap to change once a recorded
production response settles it. Capturing one from prod is the way to close
them — do not resolve them from the documentation, which is less precise than
the real API.

| Point | What the mock does | Why it is open |
|---|---|---|
| `POST /v2/contact/getid` | Served as an alias of `checkids`, also accepting `key_value` and `key_values` | The endpoint does not appear anywhere in the official collection. Integrations written against older documentation call it, so answering is better than a 404 |
| Invalid field *value* (bad date, undefined choice) | Per-row error with replyCode 2006 and a message naming the field and reason | 2006 is documented as "invalid field id". Production may use a different code for a bad value; inventing a number would be worse than reusing this one with a clear text |
| Unknown field *id* in a batch | Request-level error, HTTP 400 replyCode 2006 | A client sending an unknown id is misconfigured for every row, not just the one it appeared in |
| `contact/query` limit | Enforced as 1–10000, replyCode 2016 outside that | The collection's own example passes `limit=1000000`, which contradicts the documented range |
| `PUT /v2/contact` id types | Updated contact returns a string id, created contact an integer | The collection example shows only strings. The mixed behaviour is what the briefing recorded from production; the golden file `contact_upsert.json` is the one place to change if prod says otherwise |
| Field list payload | Includes `string_id` alongside `id`, `name`, `application_type` | The collection example omits it, but real accounts return it and clients use it |
| Choice `bit_position` | Set to the choice id | Meaningful for multichoice fields in production; the collection gives no rule for deriving it |
| `null` as a field value | Treated the same as an empty string: clears the field | Consistent with the empty-string behaviour, but production may distinguish the two |
| `GET /v2/event/{eventId}` | Returns the single event as an object | The collection's example for this path shows an array of events, which looks like the list response attached to the wrong request |
| Trigger response | Always `{"errors": {…}}`, empty when everything succeeded | The collection carries no success example for this path. A single consistent shape beats one that changes with the outcome |
| Repeated `trigger_id` | Acknowledged, but neither stored nor announced a second time | `trigger_id` is Emarsys' idempotency key, so a client retrying after a timeout must not cause a second send |
| Unknown event, list or export id | HTTP 400, replyCode 2011, with a message naming the id | No documented code covers "unknown object id" on these paths |
| Export CSV layout | `Timestamp` first, then the requested `contact_fields` in the order given | The collection describes the request but never the file |
| `GET /v2/export/{id}/data` before the job is done | HTTP 400, replyCode 2011, "is not finished yet" | The collection's only example for this path is an empty 500 |
| Outbound webhook | Not an Emarsys feature at all: `WEBHOOK_URL` gets a POST on every accepted trigger | Ours, so the loop through the event bus can be closed in CI without a cloud dependency in the mock |
| Segments | CRUD with a manually maintained member list; `criteria` is stored and never evaluated | Reimplementing the segmentation engine would mean guessing at behaviour nobody can verify, and a segment that is subtly wrong is worse than one that is obviously static |

## Timestamps

Every timestamp the mock renders in a payload or an export file uses Vienna
local time and the layout `YYYY-MM-DD HH:MM:SS`, with no offset and no zone
marker. That is what production does, and it is why a client parsing an export
timestamp as UTC is one or two hours out all year round. `EXPORT_TIMEZONE`
changes the zone if an account is provisioned differently.

The same rule applies on the way in: a bare date in a `time_range` is
interpreted in that timezone, because that is the calendar a caller means when
they ask for "yesterday".

Stored data is always UTC; the conversion happens at the edge.

## Value validation

The mock validates every incoming value against its field's `application_type`,
which is what produces the per-row errors an integration needs to see:

- `date` — `YYYY-MM-DD` only. An ISO timestamp, a German or a US ordering is rejected.
- `numeric` — must parse as a number.
- `singlechoice` — must be one of the field's defined choice ids. This is why
  opt-in (field 31) accepts `1` and `2` and rejects `"true"`, `"false"` and `0`.
- `multichoice` / `interests` — a comma-separated list of defined choice ids.
- Everything else is stored verbatim.

An empty value is always accepted and **overwrites** what was there. That is not
a convenience: it is how an integration that includes a field it did not mean to
send wipes consent data in production, and the mock has to reproduce it.
