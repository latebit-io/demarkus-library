package broker

import (
	"errors"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// TestMapWriteError locks the broker write-error → domain mapping, including the
// substring branches the gateway relies on (a broker wording change that breaks
// these silently downgrades a conflict to a generic error and breaks the desk's
// reload / re-login flow).
func TestMapWriteError(t *testing.T) {
	cases := []struct {
		text string
		want error
	}{
		{"expected version 5 but current is 6", domain.ErrConflict},
		{"version mismatch", domain.ErrConflict},
		{"write CONFLICT detected", domain.ErrConflict},
		{"identity not authorized for world", domain.ErrUnauthorized},
		{"not permitted to write /hr/secret.md", domain.ErrUnauthorized},
		{"world server unreachable", nil}, // generic — no sentinel
	}
	for _, tc := range cases {
		got := mapWriteError(tc.text)
		if tc.want == nil {
			if errors.Is(got, domain.ErrConflict) || errors.Is(got, domain.ErrUnauthorized) {
				t.Errorf("mapWriteError(%q) = %v, want a generic error", tc.text, got)
			}
			if got == nil {
				t.Errorf("mapWriteError(%q) = nil, want a non-nil generic error", tc.text)
			}
			continue
		}
		if !errors.Is(got, tc.want) {
			t.Errorf("mapWriteError(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestMapToolError(t *testing.T) {
	if got := mapToolError("identity not authorized"); !errors.Is(got, domain.ErrUnauthorized) {
		t.Errorf("not-authorized → %v, want ErrUnauthorized", got)
	}
	if got := mapToolError("world unreachable"); errors.Is(got, domain.ErrUnauthorized) || got == nil {
		t.Errorf("generic tool error → %v, want a non-nil generic error", got)
	}
}

// TestAppendToolErrorMaps drives the Append isToolError branch end-to-end so the
// tool-error → domain mapping on the write path is exercised, not just the
// pure mapWriteError function.
func TestAppendToolErrorMaps(t *testing.T) {
	cases := []struct {
		text string
		want error
	}{
		{"expected version 2 but current is 3", domain.ErrConflict},
		{"not authorized", domain.ErrUnauthorized},
	}
	for _, tc := range cases {
		g := &Gateway{caller: &fakeCaller{text: tc.text, isToolErr: true}}
		if _, err := g.Append(authedCtx(t), "w", "/p.md", "more"); !errors.Is(err, tc.want) {
			t.Errorf("Append tool-error %q → %v, want %v", tc.text, err, tc.want)
		}
	}
}
