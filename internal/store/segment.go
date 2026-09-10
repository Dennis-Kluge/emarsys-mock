package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Segment exists in the mock as a CRUD object with a manually maintained member
// list.
//
// The mock deliberately does not evaluate criteria_json. Reimplementing the
// segmentation engine would mean guessing at behaviour nobody can verify, and a
// segment that is wrong in a subtle way is worse than one that is obviously
// static.
type Segment struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Criteria string `json:"criteria"`
	Created  string `json:"created,omitempty"`
}

// ErrNoSegment is returned when a segment id is unknown.
var ErrNoSegment = errors.New("no such segment")

func (db *DB) Segments(ctx context.Context) ([]Segment, error) {
	rows, err := db.Read.QueryContext(ctx,
		`SELECT id, name, criteria_json, created_at FROM segments ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list segments: %w", err)
	}
	defer rows.Close()

	out := []Segment{}
	for rows.Next() {
		var s Segment
		if err := rows.Scan(&s.ID, &s.Name, &s.Criteria, &s.Created); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (db *DB) Segment(ctx context.Context, id int64) (Segment, error) {
	var s Segment
	err := db.Read.QueryRowContext(ctx,
		`SELECT id, name, criteria_json, created_at FROM segments WHERE id = ?`, id).
		Scan(&s.ID, &s.Name, &s.Criteria, &s.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return Segment{}, ErrNoSegment
	}
	return s, err
}

func (db *DB) CreateSegment(ctx context.Context, name, criteria string) (int64, error) {
	if criteria == "" {
		criteria = "{}"
	}
	res, err := db.Write.ExecContext(ctx,
		`INSERT INTO segments (name, criteria_json, created_at) VALUES (?, ?, ?)`,
		name, criteria, nowString())
	if err != nil {
		return 0, fmt.Errorf("create segment: %w", err)
	}
	return res.LastInsertId()
}

func (db *DB) UpdateSegment(ctx context.Context, id int64, name, criteria string) error {
	res, err := db.Write.ExecContext(ctx,
		`UPDATE segments SET name = COALESCE(NULLIF(?, ''), name),
		                     criteria_json = COALESCE(NULLIF(?, ''), criteria_json)
		 WHERE id = ?`, name, criteria, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSegment
	}
	return nil
}

func (db *DB) DeleteSegment(ctx context.Context, id int64) error {
	res, err := db.Write.ExecContext(ctx, `DELETE FROM segments WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSegment
	}
	return nil
}

func (db *DB) SegmentMembers(ctx context.Context, segmentID int64) ([]int64, error) {
	rows, err := db.Read.QueryContext(ctx,
		`SELECT contact_id FROM segment_members WHERE segment_id = ? ORDER BY contact_id`, segmentID)
	if err != nil {
		return nil, fmt.Errorf("read segment members: %w", err)
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

func (db *DB) SetSegmentMember(ctx context.Context, segmentID, contactID int64, member bool) error {
	if !member {
		_, err := db.Write.ExecContext(ctx,
			`DELETE FROM segment_members WHERE segment_id = ? AND contact_id = ?`, segmentID, contactID)
		return err
	}
	_, err := db.Write.ExecContext(ctx,
		`INSERT OR IGNORE INTO segment_members (segment_id, contact_id) VALUES (?, ?)`,
		segmentID, contactID)
	return err
}
