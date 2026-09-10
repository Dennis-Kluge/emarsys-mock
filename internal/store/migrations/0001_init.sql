-- Core schema.
--
-- Contact values are stored EAV-style because Emarsys addresses contact fields
-- by numeric field id and every customer defines their own custom fields. A
-- fixed column layout would have to be regenerated for each account.

CREATE TABLE api_users (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    username         TEXT NOT NULL UNIQUE,
    secret           TEXT NOT NULL,
    client_id        TEXT UNIQUE,
    client_secret    TEXT,
    permissions_json TEXT NOT NULL DEFAULT '["*"]',
    created_at       TEXT NOT NULL
);

-- id is the Emarsys field id and is assigned explicitly, never autoincremented:
-- integrations hardcode 3 for e-mail and 31 for opt-in.
CREATE TABLE fields (
    id               INTEGER PRIMARY KEY,
    string_id        TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL,
    application_type TEXT NOT NULL,
    is_system        INTEGER NOT NULL DEFAULT 0,
    -- contact/query only works on indexed columns; an unindexed field must
    -- yield replyCode 2015 the way production does.
    is_indexed       INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL
);

CREATE TABLE field_choices (
    field_id INTEGER NOT NULL REFERENCES fields(id) ON DELETE CASCADE,
    choice_id INTEGER NOT NULL,
    label    TEXT NOT NULL,
    sort_id  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (field_id, choice_id)
);

CREATE TABLE contacts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    uid        TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE contact_values (
    contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    field_id   INTEGER NOT NULL REFERENCES fields(id) ON DELETE CASCADE,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (contact_id, field_id)
);

CREATE INDEX idx_contact_values_field_value ON contact_values(field_id, value);

-- Emarsys has no endpoint that returns a field's change history, so consent
-- audit trails have to be kept on our side. This table is both the backing
-- store for contact/last_change and getchanges, and the pattern for that trail.
CREATE TABLE contact_field_history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    field_id   INTEGER NOT NULL,
    old_value  TEXT,
    new_value  TEXT,
    changed_at TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT 'api'
);

CREATE INDEX idx_contact_field_history_contact ON contact_field_history(contact_id, changed_at);
CREATE INDEX idx_contact_field_history_changed ON contact_field_history(changed_at);

CREATE TABLE contact_lists (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE contact_list_members (
    list_id    INTEGER NOT NULL REFERENCES contact_lists(id) ON DELETE CASCADE,
    contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    added_at   TEXT NOT NULL,
    PRIMARY KEY (list_id, contact_id)
);

CREATE TABLE events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);

CREATE TABLE event_triggers (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id    INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    contact_id  INTEGER,
    external_id TEXT,
    payload_json TEXT NOT NULL DEFAULT '{}',
    event_time  TEXT,
    trigger_id  TEXT,
    received_at TEXT NOT NULL
);

CREATE INDEX idx_event_triggers_event ON event_triggers(event_id, received_at);
-- trigger_id is Emarsys' de-duplication key, unique per event.
CREATE UNIQUE INDEX idx_event_triggers_dedup ON event_triggers(event_id, trigger_id)
    WHERE trigger_id IS NOT NULL;

-- Segments exist as CRUD objects with a manually maintained member list. The
-- mock deliberately does not evaluate criteria_json.
CREATE TABLE segments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL,
    criteria_json TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL
);

CREATE TABLE segment_members (
    segment_id INTEGER NOT NULL REFERENCES segments(id) ON DELETE CASCADE,
    contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    PRIMARY KEY (segment_id, contact_id)
);

CREATE TABLE exports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL,
    -- 'scheduled' | 'in progress' | 'done' | 'error'. Note the space in
    -- 'in progress' -- that is the literal Emarsys uses.
    status      TEXT NOT NULL,
    params_json TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL,
    ready_at    TEXT,
    poll_count  INTEGER NOT NULL DEFAULT 0,
    -- number of polls that must return 'in progress' before this job flips to
    -- 'done'; per-job so a test can force an immediate or a slow export
    polls_before_done INTEGER NOT NULL DEFAULT 2,
    file_name   TEXT NOT NULL DEFAULT '',
    payload     BLOB
);

CREATE TABLE request_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ts            TEXT NOT NULL,
    method        TEXT NOT NULL,
    path          TEXT NOT NULL,
    query         TEXT NOT NULL DEFAULT '',
    auth_user     TEXT NOT NULL DEFAULT '',
    http_status   INTEGER NOT NULL,
    reply_code    INTEGER,
    request_body  TEXT,
    response_body TEXT,
    duration_ms   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_request_log_ts ON request_log(ts);

CREATE TABLE fault_rules (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    enabled             INTEGER NOT NULL DEFAULT 1,
    match_method        TEXT NOT NULL DEFAULT '',
    match_path_pattern  TEXT NOT NULL DEFAULT '',
    match_body_contains TEXT NOT NULL DEFAULT '',
    http_status         INTEGER NOT NULL DEFAULT 500,
    reply_code          INTEGER NOT NULL DEFAULT 2011,
    reply_text          TEXT NOT NULL DEFAULT 'Internal error',
    probability         REAL NOT NULL DEFAULT 1.0,
    -- NULL means "until disabled"; a number counts down and disables the rule
    remaining_hits      INTEGER,
    created_at          TEXT NOT NULL
);

-- Only populated when WSSE_REJECT_NONCE_REUSE is on.
CREATE TABLE wsse_nonces (
    nonce   TEXT PRIMARY KEY,
    seen_at TEXT NOT NULL
);
