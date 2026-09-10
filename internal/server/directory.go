package server

import (
	"context"
	"errors"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/auth"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// directory adapts the store to the auth package's Directory interface, which
// keeps the authentication logic free of any database types.
type directory struct{ db *store.DB }

func (d directory) ByUsername(ctx context.Context, username string) (*auth.Principal, error) {
	u, err := d.db.APIUserByUsername(ctx, username)
	return toPrincipal(u, err)
}

func (d directory) ByClientID(ctx context.Context, clientID string) (*auth.Principal, error) {
	u, err := d.db.APIUserByClientID(ctx, clientID)
	return toPrincipal(u, err)
}

func (d directory) RememberNonce(ctx context.Context, nonce string, seenAt time.Time) (bool, error) {
	return d.db.RememberNonce(ctx, nonce, seenAt)
}

func toPrincipal(u *store.APIUser, err error) (*auth.Principal, error) {
	if err != nil {
		if errors.Is(err, store.ErrNoAPIUser) {
			return nil, auth.ErrUnknownPrincipal
		}
		return nil, err
	}
	return &auth.Principal{
		Username:     u.Username,
		Secret:       u.Secret,
		ClientID:     u.ClientID,
		ClientSecret: u.ClientSecret,
		Permissions:  u.Permissions,
	}, nil
}
