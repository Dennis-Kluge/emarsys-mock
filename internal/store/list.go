package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ContactList is a static list of contacts.
type ContactList struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created,omitempty"`
}

// ErrNoList is returned when a contact list id is unknown.
var ErrNoList = errors.New("no such contact list")

func (db *DB) ContactLists(ctx context.Context) ([]ContactList, error) {
	rows, err := db.Read.QueryContext(ctx, `SELECT id, name, created_at FROM contact_lists ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list contact lists: %w", err)
	}
	defer rows.Close()

	out := []ContactList{}
	for rows.Next() {
		var l ContactList
		if err := rows.Scan(&l.ID, &l.Name, &l.Created); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (db *DB) ContactList(ctx context.Context, id int64) (ContactList, error) {
	var l ContactList
	err := db.Read.QueryRowContext(ctx,
		`SELECT id, name, created_at FROM contact_lists WHERE id = ?`, id).Scan(&l.ID, &l.Name, &l.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return ContactList{}, ErrNoList
	}
	return l, err
}

func (db *DB) CreateContactList(ctx context.Context, name string) (int64, error) {
	res, err := db.Write.ExecContext(ctx,
		`INSERT INTO contact_lists (name, created_at) VALUES (?, ?)`, name, nowString())
	if err != nil {
		return 0, fmt.Errorf("create contact list: %w", err)
	}
	return res.LastInsertId()
}

func (db *DB) RenameContactList(ctx context.Context, id int64, name string) error {
	res, err := db.Write.ExecContext(ctx, `UPDATE contact_lists SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoList
	}
	return nil
}

func (db *DB) DeleteContactList(ctx context.Context, id int64) error {
	res, err := db.Write.ExecContext(ctx, `DELETE FROM contact_lists WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoList
	}
	return nil
}

// ListMembers returns the contact ids on a list, oldest first.
func (db *DB) ListMembers(ctx context.Context, listID int64, limit, offset int) ([]int64, error) {
	rows, err := db.Read.QueryContext(ctx,
		`SELECT contact_id FROM contact_list_members WHERE list_id = ?
		 ORDER BY contact_id LIMIT ? OFFSET ?`, listID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (db *DB) ListMemberCount(ctx context.Context, listID int64) (int, error) {
	var n int
	err := db.Read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM contact_list_members WHERE list_id = ?`, listID).Scan(&n)
	return n, err
}

// RemoveFromList drops contacts from a list and reports how many rows went.
func RemoveFromList(ctx context.Context, tx dbtx, listID int64, contactIDs []int64) (int, error) {
	removed := 0
	for _, id := range contactIDs {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM contact_list_members WHERE list_id = ? AND contact_id = ?`, listID, id)
		if err != nil {
			return removed, fmt.Errorf("remove contact from list: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			removed++
		}
	}
	return removed, nil
}

// ClearList empties a list without deleting it.
func ClearList(ctx context.Context, tx dbtx, listID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM contact_list_members WHERE list_id = ?`, listID)
	return err
}

// AddToListCounting adds contacts and reports how many were not already on the
// list, which is the number production returns as inserted_contacts.
func AddToListCounting(ctx context.Context, tx dbtx, listID int64, contactIDs []int64) (int, error) {
	inserted := 0
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range contactIDs {
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO contact_list_members (list_id, contact_id, added_at) VALUES (?, ?, ?)`,
			listID, id, now)
		if err != nil {
			return inserted, fmt.Errorf("add contact to list: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}
