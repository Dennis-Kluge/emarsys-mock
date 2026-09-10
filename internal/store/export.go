package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Export status literals.
//
// "in progress" carries a space. It is not a typo and not a slug: that is the
// exact string production returns, and a client comparing against "in_progress"
// never sees the job finish.
const (
	ExportScheduled  = "scheduled"
	ExportInProgress = "in progress"
	ExportDone       = "done"
	ExportError      = "error"
)

// Export is an asynchronous export job.
type Export struct {
	ID              int64
	Type            string
	Status          string
	Params          string
	CreatedAt       string
	ReadyAt         *string
	PollCount       int
	PollsBeforeDone int
	FileName        string
}

// ErrNoExport is returned when an export id is unknown.
var ErrNoExport = errors.New("no such export")

// ErrExportNotReady is returned when the data of an unfinished export is asked for.
var ErrExportNotReady = errors.New("export is not finished")

// CreateExport records a job and snapshots its payload immediately.
//
// Production builds the file asynchronously; the mock builds it up front and
// only pretends to take time, because what integrations need to exercise is the
// polling loop, not the generation.
func (db *DB) CreateExport(ctx context.Context, exportType, params string, pollsBeforeDone int, payload []byte) (int64, error) {
	created := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Write.ExecContext(ctx,
		`INSERT INTO exports (type, status, params_json, created_at, poll_count, polls_before_done, file_name, payload)
		 VALUES (?, ?, ?, ?, 0, ?, '', ?)`,
		exportType, ExportScheduled, params, created, pollsBeforeDone, payload)
	if err != nil {
		return 0, fmt.Errorf("create export: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = db.Write.ExecContext(ctx,
		`UPDATE exports SET file_name = ? WHERE id = ?`,
		fmt.Sprintf("%s_%d.csv", exportType, id), id)
	return id, err
}

// PollExport returns the job and advances its state machine.
//
// The first PollsBeforeDone polls report "in progress"; the next one reports
// "done". Without that delay nobody's polling loop ever gets exercised, which
// is the whole reason to mock an asynchronous endpoint.
func (db *DB) PollExport(ctx context.Context, id int64) (Export, error) {
	e, err := db.ExportByID(ctx, id)
	if err != nil {
		return Export{}, err
	}
	if e.Status == ExportDone || e.Status == ExportError {
		return e, nil
	}

	e.PollCount++
	e.Status = ExportInProgress
	var readyAt any
	if e.PollCount > e.PollsBeforeDone {
		e.Status = ExportDone
		ready := time.Now().UTC().Format(time.RFC3339)
		e.ReadyAt = &ready
		readyAt = ready
	}

	if _, err := db.Write.ExecContext(ctx,
		`UPDATE exports SET poll_count = ?, status = ?, ready_at = COALESCE(?, ready_at) WHERE id = ?`,
		e.PollCount, e.Status, readyAt, id); err != nil {
		return Export{}, fmt.Errorf("advance export: %w", err)
	}
	return e, nil
}

// ExportByID reads a job without advancing it, which is what the dashboard does.
func (db *DB) ExportByID(ctx context.Context, id int64) (Export, error) {
	var e Export
	err := db.Read.QueryRowContext(ctx,
		`SELECT id, type, status, params_json, created_at, ready_at, poll_count, polls_before_done, file_name
		 FROM exports WHERE id = ?`, id).
		Scan(&e.ID, &e.Type, &e.Status, &e.Params, &e.CreatedAt, &e.ReadyAt,
			&e.PollCount, &e.PollsBeforeDone, &e.FileName)
	if errors.Is(err, sql.ErrNoRows) {
		return Export{}, ErrNoExport
	}
	return e, err
}

// Exports lists jobs, newest first.
func (db *DB) Exports(ctx context.Context, limit int) ([]Export, error) {
	rows, err := db.Read.QueryContext(ctx,
		`SELECT id, type, status, params_json, created_at, ready_at, poll_count, polls_before_done, file_name
		 FROM exports ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list exports: %w", err)
	}
	defer rows.Close()

	out := []Export{}
	for rows.Next() {
		var e Export
		if err := rows.Scan(&e.ID, &e.Type, &e.Status, &e.Params, &e.CreatedAt, &e.ReadyAt,
			&e.PollCount, &e.PollsBeforeDone, &e.FileName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetExportStatus forces a job into a state. The dashboard and the control
// plane use it so a test does not have to poll a job to completion.
func (db *DB) SetExportStatus(ctx context.Context, id int64, status string) error {
	var readyAt any
	if status == ExportDone {
		readyAt = time.Now().UTC().Format(time.RFC3339)
	}
	res, err := db.Write.ExecContext(ctx,
		`UPDATE exports SET status = ?, ready_at = COALESCE(?, ready_at) WHERE id = ?`,
		status, readyAt, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoExport
	}
	return nil
}

// ExportPayload returns the generated file, refusing until the job is done.
func (db *DB) ExportPayload(ctx context.Context, id int64) ([]byte, error) {
	e, err := db.ExportByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if e.Status != ExportDone {
		return nil, ErrExportNotReady
	}
	var payload []byte
	err = db.Read.QueryRowContext(ctx, `SELECT payload FROM exports WHERE id = ?`, id).Scan(&payload)
	return payload, err
}
