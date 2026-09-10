package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// dbtx is the subset of *sql.DB, *sql.Tx and *sql.Conn the contact helpers use,
// so the same code serves a read outside a transaction and a write inside one.
type dbtx interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// WithTx runs fn inside a write transaction, rolling back on any error.
//
// Contact batches must be transactional: a batch that fails halfway through
// would otherwise leave the mock in a state no production account could reach,
// and the test that hit it would be debugging the mock rather than the client.
func (db *DB) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// FindContactIDs returns every contact matching a key field value.
//
// It returns a slice rather than a single id on purpose: Emarsys does not
// enforce uniqueness on key fields, and a value matching more than one contact
// is the condition behind replyCode 2010.
func FindContactIDs(ctx context.Context, q dbtx, keyFieldID int, value string) ([]int64, error) {
	var (
		rows *sql.Rows
		err  error
	)
	switch keyFieldID {
	case KeyFieldInternalID:
		id, convErr := strconv.ParseInt(value, 10, 64)
		if convErr != nil {
			return nil, nil
		}
		rows, err = q.QueryContext(ctx, `SELECT id FROM contacts WHERE id = ?`, id)
	case KeyFieldUID:
		rows, err = q.QueryContext(ctx, `SELECT id FROM contacts WHERE uid = ?`, value)
	default:
		rows, err = q.QueryContext(ctx,
			`SELECT contact_id FROM contact_values WHERE field_id = ? AND value = ? ORDER BY contact_id`,
			keyFieldID, value)
	}
	if err != nil {
		return nil, fmt.Errorf("find contacts: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateContact inserts a contact with the given field values.
func CreateContact(ctx context.Context, tx dbtx, values map[int]string, source string) (int64, error) {
	uid, err := newUID()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := tx.ExecContext(ctx,
		`INSERT INTO contacts (uid, created_at, updated_at) VALUES (?, ?, ?)`, uid, now, now)
	if err != nil {
		return 0, fmt.Errorf("create contact: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := ApplyContactValues(ctx, tx, id, values, source); err != nil {
		return 0, err
	}
	return id, nil
}

// ApplyContactValues writes the given field values and records every change in
// the history table.
//
// Only the fields present in values are touched: an update replaces what it was
// sent and leaves everything else alone. An empty string is a value like any
// other and overwrites what was there, which is exactly how an integration that
// sends a field it did not mean to send wipes consent data in production.
func ApplyContactValues(ctx context.Context, tx dbtx, contactID int64, values map[int]string, source string) error {
	if len(values) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)

	for _, fieldID := range sortedFieldIDs(values) {
		newValue := values[fieldID]

		var oldValue sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT value FROM contact_values WHERE contact_id = ? AND field_id = ?`,
			contactID, fieldID).Scan(&oldValue)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read current value: %w", err)
		}
		if oldValue.Valid && oldValue.String == newValue {
			continue
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO contact_values (contact_id, field_id, value, updated_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT(contact_id, field_id) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			contactID, fieldID, newValue, now); err != nil {
			return fmt.Errorf("write contact value: %w", err)
		}

		// Emarsys exposes no endpoint that returns a field's change history, so
		// a consent audit trail has to be built on our side. Recording it here
		// is what makes contact/last_change and getchanges implementable.
		var old any
		if oldValue.Valid {
			old = oldValue.String
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO contact_field_history (contact_id, field_id, old_value, new_value, changed_at, source)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			contactID, fieldID, old, newValue, now, source); err != nil {
			return fmt.Errorf("record field history: %w", err)
		}
	}

	_, err := tx.ExecContext(ctx, `UPDATE contacts SET updated_at = ? WHERE id = ?`, now, contactID)
	return err
}

// DeleteContact removes a contact and its values.
func DeleteContact(ctx context.Context, tx dbtx, contactID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM contacts WHERE id = ?`, contactID)
	return err
}

// ContactRow is a contact together with the field values that were asked for.
type ContactRow struct {
	ID     int64
	UID    string
	Values map[int]string
}

// LoadContacts returns the requested fields for the given contacts in one pass.
// Passing no field ids returns every stored value.
func LoadContacts(ctx context.Context, q dbtx, ids []int64, fieldIDs []int) (map[int64]*ContactRow, error) {
	out := make(map[int64]*ContactRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	idList := placeholders(len(ids))
	args := make([]any, 0, len(ids)+len(fieldIDs))
	for _, id := range ids {
		args = append(args, id)
	}

	rows, err := q.QueryContext(ctx,
		`SELECT id, uid FROM contacts WHERE id IN (`+idList+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("load contacts: %w", err)
	}
	for rows.Next() {
		var r ContactRow
		if err := rows.Scan(&r.ID, &r.UID); err != nil {
			rows.Close()
			return nil, err
		}
		r.Values = map[int]string{}
		out[r.ID] = &r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	query := `SELECT contact_id, field_id, value FROM contact_values WHERE contact_id IN (` + idList + `)`
	if len(fieldIDs) > 0 {
		query += ` AND field_id IN (` + placeholders(len(fieldIDs)) + `)`
		for _, f := range fieldIDs {
			args = append(args, f)
		}
	}
	valueRows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load contact values: %w", err)
	}
	defer valueRows.Close()
	for valueRows.Next() {
		var contactID int64
		var fieldID int
		var value string
		if err := valueRows.Scan(&contactID, &fieldID, &value); err != nil {
			return nil, err
		}
		if row, ok := out[contactID]; ok {
			row.Values[fieldID] = value
		}
	}
	return out, valueRows.Err()
}

// QueryContactsByField returns contacts whose field holds the given value.
func QueryContactsByField(ctx context.Context, q dbtx, fieldID int, value string, excludeEmpty bool, limit, offset int) ([]int64, error) {
	query := `SELECT contact_id FROM contact_values WHERE field_id = ? AND value = ?`
	if excludeEmpty {
		query += ` AND value <> ''`
	}
	query += ` ORDER BY contact_id LIMIT ? OFFSET ?`

	rows, err := q.QueryContext(ctx, query, fieldID, value, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query contacts: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// AddContactsToList is used by the contact endpoints, which accept an optional
// contact_list_id and drop every touched contact into that list.
func AddContactsToList(ctx context.Context, tx dbtx, listID int64, contactIDs []int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range contactIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO contact_list_members (list_id, contact_id, added_at) VALUES (?, ?, ?)`,
			listID, id, now); err != nil {
			return fmt.Errorf("add contact to list: %w", err)
		}
	}
	return nil
}

// ContactListExists reports whether a list id is known.
func ContactListExists(ctx context.Context, q dbtx, listID int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM contact_lists WHERE id = ?`, listID).Scan(&n)
	return n > 0, err
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func newUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate contact uid: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
