-- Seed data.
--
-- The system field ids below are the ones that are stable across every Emarsys
-- account and that integrations hardcode. The real catalogue of an account also
-- contains dozens of further system fields and all custom fields; import those
-- from production with `GET /v2/field` and load them via POST /_ctl/seed rather
-- than guessing them here.

INSERT INTO fields (id, string_id, name, application_type, is_system, is_indexed, created_at) VALUES
    (1,  'first_name',    'First Name',    'shorttext',    1, 0, '2020-01-01T00:00:00Z'),
    (2,  'last_name',     'Last Name',     'shorttext',    1, 0, '2020-01-01T00:00:00Z'),
    (3,  'email',         'E-Mail',        'shorttext',    1, 1, '2020-01-01T00:00:00Z'),
    (4,  'birth_date',    'Date of birth', 'date',         1, 0, '2020-01-01T00:00:00Z'),
    (5,  'gender',        'Gender',        'singlechoice', 1, 0, '2020-01-01T00:00:00Z'),
    (31, 'opt_in',        'Opt-in',        'singlechoice', 1, 1, '2020-01-01T00:00:00Z');

INSERT INTO field_choices (field_id, choice_id, label, sort_id) VALUES
    (5, 1, 'Male', 1),
    (5, 2, 'Female', 2),
    (31, 1, 'True', 1),
    (31, 2, 'False', 2);

-- Example custom fields matching the integrations this mock is built for.
INSERT INTO fields (id, string_id, name, application_type, is_system, is_indexed, created_at) VALUES
    (10001, 'customer_id',        'Customer ID',        'shorttext', 0, 1, '2020-01-01T00:00:00Z'),
    (10002, 'wishlist_updated_at','Wishlist updated at','date',      0, 0, '2020-01-01T00:00:00Z'),
    (10003, 'is_test_contact',    'Is test contact',    'singlechoice', 0, 0, '2020-01-01T00:00:00Z');

INSERT INTO field_choices (field_id, choice_id, label, sort_id) VALUES
    (10003, 1, 'True', 1),
    (10003, 2, 'False', 2);

INSERT INTO api_users (username, secret, client_id, client_secret, permissions_json, created_at) VALUES
    ('mock-api-user', 'mock-secret', 'mock-client', 'mock-client-secret', '["*"]', '2020-01-01T00:00:00Z');

INSERT INTO events (name, created_at) VALUES
    ('wishlist_reminder', '2020-01-01T00:00:00Z'),
    ('double_opt_in',     '2020-01-01T00:00:00Z');

INSERT INTO contact_lists (name, created_at) VALUES
    ('Newsletter', '2020-01-01T00:00:00Z');
