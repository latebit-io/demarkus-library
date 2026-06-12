package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web/session"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/oauth"
)

// brokerAuth adapts the outbound oauth client to the two small interfaces the
// inbound side consumes: session.Authority (refresh/revoke) and web.LoginFlow
// (begin/exchange). This shim is the only place the two adapters meet — the
// web and session packages stay free of broker specifics, and the oauth
// package stays free of session vocabulary. Composition-root glue, on purpose.
type brokerAuth struct {
	client *oauth.Client
}

var (
	_ session.Authority = brokerAuth{}
	_ web.LoginFlow     = brokerAuth{}
)

// Begin mints state + PKCE and builds the broker authorize URL (web.LoginFlow).
func (a brokerAuth) Begin(ctx context.Context) (authURL, state, verifier string, err error) {
	state = oauth.GenerateState()
	verifier = oauth.GenerateVerifier()
	authURL, err = a.client.AuthCodeURL(ctx, state, oauth.Challenge(verifier))
	if err != nil {
		return "", "", "", err
	}
	return authURL, state, verifier, nil
}

// Exchange redeems the callback code (web.LoginFlow).
func (a brokerAuth) Exchange(ctx context.Context, code, verifier string) (session.Tokens, error) {
	ts, err := a.client.Exchange(ctx, code, verifier)
	if err != nil {
		return session.Tokens{}, err
	}
	return toSessionTokens(ts), nil
}

// Refresh maps the oauth sentinel onto the session package's dead-grant
// signal so the Manager tears the session down (session.Authority).
func (a brokerAuth) Refresh(ctx context.Context, refreshToken string) (session.Tokens, error) {
	ts, err := a.client.Refresh(ctx, refreshToken)
	if err != nil {
		if errors.Is(err, oauth.ErrInvalidGrant) {
			return session.Tokens{}, fmt.Errorf("%w: %w", session.ErrGrantDead, err)
		}
		return session.Tokens{}, err
	}
	return toSessionTokens(ts), nil
}

// Revoke drops the refresh token at the broker (session.Authority).
func (a brokerAuth) Revoke(ctx context.Context, refreshToken string) error {
	return a.client.Revoke(ctx, refreshToken)
}

func toSessionTokens(ts oauth.TokenSet) session.Tokens {
	return session.Tokens{
		IDToken:      ts.IDToken,
		RefreshToken: ts.RefreshToken,
		Expiry:       ts.Expiry,
	}
}
