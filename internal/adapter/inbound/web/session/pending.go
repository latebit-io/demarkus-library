package session

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"time"
)

// PendingTTL bounds how long a login attempt may sit between the authorize
// redirect and the callback. Matches the broker's own pending-auth window.
const PendingTTL = 10 * time.Minute

// PendingLogin is one in-flight authorize redirect: the CSRF state that will
// come back on the callback, the PKCE verifier that redeems the code, and
// where to send the reader afterwards.
type PendingLogin struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	// CreatedAt is stamped by Seal and bounds the attempt to PendingTTL.
	CreatedAt time.Time `json:"c"`
}

// PendingLogins seals in-flight logins into the login cookie so the callback
// may land on any replica. The verifier stays encrypted: the browser carries
// it but cannot read it.
type PendingLogins struct {
	sealer *Sealer
	now    func() time.Time
}

// NewPendingLogins wires the login cookie to a Sealer.
func NewPendingLogins(sealer *Sealer) *PendingLogins {
	return &PendingLogins{sealer: sealer, now: time.Now}
}

// Seal stamps pl and returns the login cookie value.
func (p *PendingLogins) Seal(pl PendingLogin) (string, error) {
	pl.CreatedAt = p.now()
	value, err := p.sealer.seal(purposeLogin, pl)
	if err != nil {
		return "", fmt.Errorf("session: seal pending login: %w", err)
	}
	return value, nil
}

// Open returns the login sealed in value if it was minted for state and is
// inside PendingTTL. Single use comes from the caller clearing the cookie and
// the broker's one-time code.
func (p *PendingLogins) Open(value, state string) (PendingLogin, error) {
	var pl PendingLogin
	if err := p.sealer.open(purposeLogin, value, &pl); err != nil {
		return PendingLogin{}, err
	}
	if state == "" || subtle.ConstantTimeCompare([]byte(pl.State), []byte(state)) != 1 {
		return PendingLogin{}, errors.New("session: login state mismatch")
	}
	if !p.now().Before(pl.CreatedAt.Add(PendingTTL)) {
		return PendingLogin{}, errors.New("session: login attempt expired")
	}
	return pl, nil
}
