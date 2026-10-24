// Package session holds the library-card state for logged-in readers. The
// session travels in the reader's HttpOnly cookie, sealed (encrypted and
// authenticated) under a key every replica shares, so no replica keeps
// session state and any replica serves any reader. Tokens never reach page
// script (ADR 0004).
//
// The package depends on no broker specifics: the Manager refreshes tokens
// through the small Authority interface, satisfied by a shim over the oauth
// adapter at the composition root. Cookie reading/writing is the web
// middleware's job; this package is sealing + lifecycle.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"time"
)

// Session is one reader's library card: the broker tokens plus bookkeeping.
// Short JSON names keep the sealed cookie small.
type Session struct {
	// ID is a stable 32-byte CSPRNG id, base64url: the reader's key for
	// per-reader state and refresh single-flight. Survives every re-seal.
	ID string `json:"i"`
	// IDToken is the bearer presented to the broker's /mcp gateway.
	IDToken string `json:"t"`
	// RefreshToken mints replacement id_tokens; the broker does not
	// rotate it for a confidential web client, so it lives as long as
	// the session.
	RefreshToken string `json:"r"`
	// IDTokenExpiry is when IDToken stops being accepted.
	IDTokenExpiry time.Time `json:"e"`
	// CreatedAt anchors the session's absolute lifetime.
	CreatedAt time.Time `json:"c"`
}

// NewID returns a fresh opaque session id: 32 CSPRNG bytes, base64url, no
// padding. crypto/rand.Read is documented (Go ≥1.24) to never return an
// error; the check still panics explicitly so a hypothetical entropy failure
// can never silently yield a predictable session id.
func NewID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("session: entropy source unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
