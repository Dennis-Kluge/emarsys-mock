package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Event is an external event an integration can trigger.
type Event struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created,omitempty"`
}

// ErrNoEvent is returned when an event id is unknown.
var ErrNoEvent = errors.New("no such event")

// ErrEventExists is returned when an event name is already taken.
var ErrEventExists = errors.New("event already exists")

func (db *DB) Events(ctx context.Context) ([]Event, error) {
	rows, err := db.Read.QueryContext(ctx, `SELECT id, name, created_at FROM events ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Name, &e.Created); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (db *DB) Event(ctx context.Context, id int64) (Event, error) {
	var e Event
	err := db.Read.QueryRowContext(ctx,
		`SELECT id, name, created_at FROM events WHERE id = ?`, id).Scan(&e.ID, &e.Name, &e.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNoEvent
	}
	return e, err
}

func (db *DB) CreateEvent(ctx context.Context, name string) (Event, error) {
	res, err := db.Write.ExecContext(ctx,
		`INSERT INTO events (name, created_at) VALUES (?, ?)`, name, nowString())
	if err != nil {
		// The name column is unique, which is how production behaves.
		return Event{}, fmt.Errorf("%w: %s", ErrEventExists, name)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Event{}, err
	}
	return Event{ID: id, Name: name}, nil
}

func (db *DB) RenameEvent(ctx context.Context, id int64, name string) (Event, error) {
	res, err := db.Write.ExecContext(ctx, `UPDATE events SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return Event{}, fmt.Errorf("%w: %s", ErrEventExists, name)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Event{}, ErrNoEvent
	}
	return Event{ID: id, Name: name}, nil
}

func (db *DB) DeleteEvent(ctx context.Context, id int64) error {
	res, err := db.Write.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoEvent
	}
	return nil
}

// EventTrigger is one recorded trigger of an external event.
type EventTrigger struct {
	ID         int64  `json:"id"`
	EventID    int64  `json:"event_id"`
	EventName  string `json:"event_name"`
	ContactID  *int64 `json:"contact_id,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	Payload    string `json:"payload"`
	EventTime  string `json:"event_time,omitempty"`
	TriggerID  string `json:"trigger_id,omitempty"`
	ReceivedAt string `json:"received_at"`
}

// RecordTrigger stores a trigger and reports whether it was new.
//
// A repeated trigger_id is not an error: Emarsys uses it as an idempotency key,
// so a client that retries after a timeout must not produce a second send.
func (db *DB) RecordTrigger(ctx context.Context, t EventTrigger) (int64, bool, error) {
	var triggerID any
	if t.TriggerID != "" {
		triggerID = t.TriggerID

		var existing int64
		err := db.Read.QueryRowContext(ctx,
			`SELECT id FROM event_triggers WHERE event_id = ? AND trigger_id = ?`,
			t.EventID, t.TriggerID).Scan(&existing)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, err
		}
	}

	var contactID any
	if t.ContactID != nil {
		contactID = *t.ContactID
	}
	res, err := db.Write.ExecContext(ctx,
		`INSERT INTO event_triggers
		 (event_id, contact_id, external_id, payload_json, event_time, trigger_id, received_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.EventID, contactID, t.ExternalID, t.Payload,
		nullIfEmpty(t.EventTime), triggerID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, false, fmt.Errorf("record trigger: %w", err)
	}
	id, err := res.LastInsertId()
	return id, true, err
}

// Triggers returns recorded triggers, newest first. A zero eventID returns all.
func (db *DB) Triggers(ctx context.Context, eventID int64, limit int) ([]EventTrigger, error) {
	query := `SELECT t.id, t.event_id, e.name, t.contact_id, COALESCE(t.external_id, ''),
	                 t.payload_json, COALESCE(t.event_time, ''), COALESCE(t.trigger_id, ''), t.received_at
	          FROM event_triggers t JOIN events e ON e.id = t.event_id`
	args := []any{}
	if eventID != 0 {
		query += ` WHERE t.event_id = ?`
		args = append(args, eventID)
	}
	query += ` ORDER BY t.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list triggers: %w", err)
	}
	defer rows.Close()

	out := []EventTrigger{}
	for rows.Next() {
		var t EventTrigger
		if err := rows.Scan(&t.ID, &t.EventID, &t.EventName, &t.ContactID, &t.ExternalID,
			&t.Payload, &t.EventTime, &t.TriggerID, &t.ReceivedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
