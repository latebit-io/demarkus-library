package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// GenerateVerifier returns a fresh PKCE code verifier (RFC 7636 §4.1): 32
// bytes of CSPRNG randomness base64url-encoded without padding, yielding 43
// characters — inside the 43..128 window and entirely within the unreserved
// charset.
func GenerateVerifier() string {
	return randomToken(32)
}

// Challenge derives the S256 code challenge for a verifier (RFC 7636 §4.2):
// base64url, no padding, of the verifier's ASCII sha256.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateState returns a fresh CSRF state value for one authorize redirect.
// Single-use: the callback consumes it.
func GenerateState() string {
	return randomToken(32)
}

// randomToken returns n CSPRNG bytes base64url-encoded without padding.
// crypto/rand.Read is documented (Go ≥1.24) to never return an error; the
// check still panics explicitly so a hypothetical entropy failure can never
// silently yield a predictable verifier or state.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("oauth: entropy source unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
