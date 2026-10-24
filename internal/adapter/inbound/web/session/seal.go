package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// KeySize is the sealing key length in bytes (AES-256).
const KeySize = 32

// maxSealedLen keeps a sealed value inside the 4096-byte name+value budget
// browsers guarantee per cookie; past it the browser drops the cookie
// silently and the reader loops through login.
const maxSealedLen = 4000

// Purposes bind a sealed value to what it holds, so a login cookie can never
// open as a session.
const (
	purposeSession = "demarkus-library/session/v1"
	purposeLogin   = "demarkus-library/login/v1"
)

// ErrInvalid means a sealed value did not open: tampered, sealed under another
// key or purpose, or not a sealed value at all.
var ErrInvalid = errors.New("session: invalid sealed value")

// ErrTooLarge means the sealed value would not fit in one cookie.
var ErrTooLarge = errors.New("session: sealed value too large for a cookie")

// Sealer encrypts and authenticates cookie payloads (AES-256-GCM, random
// nonce). Every replica holding the same key opens what any replica sealed.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer builds a Sealer over a KeySize-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("session: sealing key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("session: sealing cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("session: sealing mode: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// NewEphemeralSealer seals under a fresh random key: its sessions end with
// the process and never cross replicas.
func NewEphemeralSealer() *Sealer {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		panic("session: entropy source unavailable: " + err.Error())
	}
	s, err := NewSealer(key)
	if err != nil {
		panic("session: ephemeral sealer: " + err.Error())
	}
	return s
}

// seal JSON-encodes payload and returns nonce||ciphertext, base64url.
func (s *Sealer) seal(purpose string, payload any) (string, error) {
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("session: encode %s: %w", purpose, err)
	}
	value := base64.RawURLEncoding.EncodeToString(s.aead.Seal(nil, nil, plain, []byte(purpose)))
	if len(value) > maxSealedLen {
		return "", fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(value), maxSealedLen)
	}
	return value, nil
}

// open reverses seal into payload; any failure is ErrInvalid.
func (s *Sealer) open(purpose, value string, payload any) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return ErrInvalid
	}
	plain, err := s.aead.Open(nil, nil, raw, []byte(purpose))
	if err != nil {
		return ErrInvalid
	}
	if err := json.Unmarshal(plain, payload); err != nil {
		return ErrInvalid
	}
	return nil
}
