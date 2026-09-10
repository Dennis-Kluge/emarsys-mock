package store

import (
	"context"
	"fmt"
	"time"
)

// RequestLogEntry is one recorded HTTP exchange. It backs both the dashboard's
// request view and GET /_ctl/requests, which tests use to assert that an
// integration called what it was supposed to call.
type RequestLogEntry struct {
	ID           int64     `json:"id"`
	TS           time.Time `json:"ts"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	Query        string    `json:"query,omitempty"`
	AuthUser     string    `json:"auth_user,omitempty"`
	HTTPStatus   int       `json:"http_status"`
	ReplyCode    *int      `json:"reply_code,omitempty"`
	RequestBody  string    `json:"request_body,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	DurationMS   int64     `json:"duration_ms"`
}

// AppendRequestLog stores an entry and keeps the table bounded to max rows.
//
// Without the trim a long-running dev instance grows its database file without
// limit and the dashboard's request view gets slower every day.
func (db *DB) AppendRequestLog(ctx context.Context, e RequestLogEntry, max int) error {
	res, err := db.Write.ExecContext(ctx,
		`INSERT INTO request_log
		 (ts, method, path, query, auth_user, http_status, reply_code, request_body, response_body, duration_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS.UTC().Format(time.RFC3339Nano), e.Method, e.Path, e.Query, e.AuthUser,
		e.HTTPStatus, e.ReplyCode, e.RequestBody, e.ResponseBody, e.DurationMS)
	if err != nil {
		return fmt.Errorf("append request log: %w", err)
	}

	// Trimming on every insert would double the write cost of every API call
	// for no benefit; every 128th is often enough to keep the table near max.
	id, err := res.LastInsertId()
	if err != nil || max <= 0 || id%128 != 0 {
		return nil
	}
	_, err = db.Write.ExecContext(ctx,
		`DELETE FROM request_log WHERE id <= ?`, id-int64(max))
	if err != nil {
		return fmt.Errorf("trim request log: %w", err)
	}
	return nil
}
