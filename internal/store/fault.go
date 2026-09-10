package store

import (
	"context"
	"errors"
	"fmt"
)

// FaultRule forces a canned failure onto matching requests.
//
// Fault injection is not a nicety. Without it a test suite only ever exercises
// the happy path, and the retry, backoff and error-handling code of an
// integration -- the part most likely to be wrong -- is never run at all.
type FaultRule struct {
	ID                int64   `json:"id"`
	Enabled           bool    `json:"enabled"`
	MatchMethod       string  `json:"match_method"`
	MatchPathPattern  string  `json:"match_path_pattern"`
	MatchBodyContains string  `json:"match_body_contains"`
	HTTPStatus        int     `json:"http_status"`
	ReplyCode         int     `json:"reply_code"`
	ReplyText         string  `json:"reply_text"`
	Probability       float64 `json:"probability"`
	// RemainingHits nil means "until disabled"; a number counts down and the
	// rule disables itself when it reaches zero.
	RemainingHits *int `json:"remaining_hits"`
	// RetryAfter is the Retry-After header, in seconds. Left unset it is filled
	// in for the statuses that carry one in production, so an injected 429 is
	// indistinguishable from a real one.
	RetryAfter *int   `json:"retry_after"`
	CreatedAt  string `json:"created_at"`
}

// ErrNoFaultRule is returned when a rule id is unknown.
var ErrNoFaultRule = errors.New("no such fault rule")

func (db *DB) FaultRules(ctx context.Context, onlyEnabled bool) ([]FaultRule, error) {
	query := `SELECT id, enabled, match_method, match_path_pattern, match_body_contains,
	                 http_status, reply_code, reply_text, probability, remaining_hits,
	                 retry_after, created_at
	          FROM fault_rules`
	if onlyEnabled {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY id`

	rows, err := db.Read.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list fault rules: %w", err)
	}
	defer rows.Close()

	out := []FaultRule{}
	for rows.Next() {
		var r FaultRule
		if err := rows.Scan(&r.ID, &r.Enabled, &r.MatchMethod, &r.MatchPathPattern,
			&r.MatchBodyContains, &r.HTTPStatus, &r.ReplyCode, &r.ReplyText,
			&r.Probability, &r.RemainingHits, &r.RetryAfter, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) CreateFaultRule(ctx context.Context, r FaultRule) (FaultRule, error) {
	if r.HTTPStatus == 0 {
		r.HTTPStatus = 500
	}
	if r.ReplyCode == 0 {
		r.ReplyCode = 2011
	}
	if r.ReplyText == "" {
		r.ReplyText = "Internal error"
	}
	if r.Probability <= 0 {
		r.Probability = 1
	}
	// Production pairs these statuses with a Retry-After, so the mock does too
	// unless the rule says otherwise.
	if r.RetryAfter == nil && (r.HTTPStatus == 429 || r.HTTPStatus == 503) {
		seconds := 60
		r.RetryAfter = &seconds
	}

	res, err := db.Write.ExecContext(ctx,
		`INSERT INTO fault_rules
		 (enabled, match_method, match_path_pattern, match_body_contains,
		  http_status, reply_code, reply_text, probability, remaining_hits,
		  retry_after, created_at)
		 VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.MatchMethod, r.MatchPathPattern, r.MatchBodyContains,
		r.HTTPStatus, r.ReplyCode, r.ReplyText, r.Probability, r.RemainingHits,
		r.RetryAfter, nowString())
	if err != nil {
		return FaultRule{}, fmt.Errorf("create fault rule: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return FaultRule{}, err
	}
	r.ID = id
	r.Enabled = true
	return r, nil
}

func (db *DB) DeleteFaultRule(ctx context.Context, id int64) error {
	res, err := db.Write.ExecContext(ctx, `DELETE FROM fault_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoFaultRule
	}
	return nil
}

func (db *DB) SetFaultRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := db.Write.ExecContext(ctx, `UPDATE fault_rules SET enabled = ? WHERE id = ?`, enabled, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoFaultRule
	}
	return nil
}

// ConsumeFaultHit records that a rule fired, disabling it once its budget of
// hits is spent.
func (db *DB) ConsumeFaultHit(ctx context.Context, id int64) error {
	_, err := db.Write.ExecContext(ctx,
		`UPDATE fault_rules
		 SET remaining_hits = CASE WHEN remaining_hits IS NULL THEN NULL ELSE remaining_hits - 1 END,
		     enabled = CASE WHEN remaining_hits IS NOT NULL AND remaining_hits - 1 <= 0 THEN 0 ELSE enabled END
		 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("consume fault hit: %w", err)
	}
	return nil
}
