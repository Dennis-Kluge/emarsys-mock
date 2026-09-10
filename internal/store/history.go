package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// FieldChange is one recorded change to a contact field.
type FieldChange struct {
	ID        int64   `json:"id"`
	ContactID int64   `json:"contact_id"`
	FieldID   int     `json:"field_id"`
	OldValue  *string `json:"old_value"`
	NewValue  *string `json:"new_value"`
	ChangedAt string  `json:"changed_at"`
	Source    string  `json:"source"`
}

// ErrNoChange is returned when a field has never been written.
var ErrNoChange = errors.New("no recorded change")

// FieldChanges returns the changes in a time window, oldest first.
//
// This is what makes contact/getchanges implementable at all: Emarsys exposes no
// endpoint that reports a field's history, so the record has to be kept here.
func (db *DB) FieldChanges(ctx context.Context, from, to time.Time, fieldIDs []int) ([]FieldChange, error) {
	query := `SELECT id, contact_id, field_id, old_value, new_value, changed_at, source
	          FROM contact_field_history WHERE changed_at >= ? AND changed_at <= ?`
	args := []any{from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)}

	if len(fieldIDs) > 0 {
		query += ` AND field_id IN (` + placeholders(len(fieldIDs)) + `)`
		for _, id := range fieldIDs {
			args = append(args, id)
		}
	}
	query += ` ORDER BY changed_at, id`

	rows, err := db.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read field changes: %w", err)
	}
	defer rows.Close()

	out := []FieldChange{}
	for rows.Next() {
		var c FieldChange
		if err := rows.Scan(&c.ID, &c.ContactID, &c.FieldID, &c.OldValue, &c.NewValue,
			&c.ChangedAt, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LastChange returns the most recent change to one field of one contact.
func (db *DB) LastChange(ctx context.Context, contactID int64, fieldID int) (FieldChange, error) {
	var c FieldChange
	err := db.Read.QueryRowContext(ctx,
		`SELECT id, contact_id, field_id, old_value, new_value, changed_at, source
		 FROM contact_field_history WHERE contact_id = ? AND field_id = ?
		 ORDER BY id DESC LIMIT 1`, contactID, fieldID).
		Scan(&c.ID, &c.ContactID, &c.FieldID, &c.OldValue, &c.NewValue, &c.ChangedAt, &c.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return FieldChange{}, ErrNoChange
	}
	return c, err
}

// ContactHistory returns a contact's change log, newest first. It backs the
// dashboard view that reconstructs how a contact reached its current state.
func (db *DB) ContactHistory(ctx context.Context, contactID int64, limit int) ([]FieldChange, error) {
	rows, err := db.Read.QueryContext(ctx,
		`SELECT id, contact_id, field_id, old_value, new_value, changed_at, source
		 FROM contact_field_history WHERE contact_id = ? ORDER BY id DESC LIMIT ?`, contactID, limit)
	if err != nil {
		return nil, fmt.Errorf("read contact history: %w", err)
	}
	defer rows.Close()

	out := []FieldChange{}
	for rows.Next() {
		var c FieldChange
		if err := rows.Scan(&c.ID, &c.ContactID, &c.FieldID, &c.OldValue, &c.NewValue,
			&c.ChangedAt, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
