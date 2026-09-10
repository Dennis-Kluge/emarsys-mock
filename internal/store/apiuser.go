package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// APIUser is a set of credentials the mock accepts. One row backs both the
// WSSE (username/secret) and the OAuth2 (client id/secret) path, so the same
// permissions apply whichever way a client authenticates.
type APIUser struct {
	ID           int64
	Username     string
	Secret       string
	ClientID     string
	ClientSecret string
	Permissions  []string
}

// ErrNoAPIUser is returned when no credentials match.
var ErrNoAPIUser = errors.New("no such api user")

func (db *DB) APIUserByUsername(ctx context.Context, username string) (*APIUser, error) {
	return db.apiUserWhere(ctx, `username = ?`, username)
}

func (db *DB) APIUserByClientID(ctx context.Context, clientID string) (*APIUser, error) {
	return db.apiUserWhere(ctx, `client_id = ?`, clientID)
}

func (db *DB) apiUserWhere(ctx context.Context, where string, arg any) (*APIUser, error) {
	row := db.Read.QueryRowContext(ctx,
		`SELECT id, username, secret, COALESCE(client_id,''), COALESCE(client_secret,''), permissions_json
		 FROM api_users WHERE `+where, arg)

	var u APIUser
	var perms string
	if err := row.Scan(&u.ID, &u.Username, &u.Secret, &u.ClientID, &u.ClientSecret, &perms); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoAPIUser
		}
		return nil, fmt.Errorf("load api user: %w", err)
	}
	if err := json.Unmarshal([]byte(perms), &u.Permissions); err != nil {
		return nil, fmt.Errorf("parse permissions for %s: %w", u.Username, err)
	}
	return &u, nil
}

// RememberNonce records a WSSE nonce and reports whether it is new. It is only
// consulted when nonce-replay rejection is enabled; leaving it off is the
// default because a client that retries a request with the same nonce is a
// realistic thing to want to test.
func (db *DB) RememberNonce(ctx context.Context, nonce string, seenAt time.Time) (bool, error) {
	res, err := db.Write.ExecContext(ctx,
		`INSERT OR IGNORE INTO wsse_nonces (nonce, seen_at) VALUES (?, ?)`,
		nonce, seenAt.UTC().Format(time.RFC3339))
	if err != nil {
		return false, fmt.Errorf("record nonce: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
