# Emarsys Suite API — observed contract

Distilled from the official Postman collections. Whenever a handler is written or
changed, check the shape here first — do not guess paths or bodies.

Sources:

- v2 / WSSE: `emartech/developer-hub-public-assets` →
  `resources/EmarsysV2PostmanCollection.json` (base URL `https://api.emarsys.net/api`)
- v3 / OIDC: `emartech/Emarsys-postman-collection`

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
