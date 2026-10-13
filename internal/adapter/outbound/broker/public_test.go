package broker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestPublicReaderReadsHubBrandingWithoutABearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/worlds/root/.well-known/library/site.css.md":
			_, _ = w.Write([]byte("# Sheet\n"))
		case "/worlds/root/.well-known/library/logo.md":
			http.NotFound(w, r)
		case "/worlds/root/.well-known/library/branding.md":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	reader := NewPublicReader(srv.URL+"/", nil)
	raw, err := reader.Raw(context.Background(), "root", "/.well-known/library/site.css.md")
	if err != nil || raw.Body != "# Sheet\n" || gotAuth != "" {
		t.Fatalf("sheet: %+v %v auth=%q", raw, err, gotAuth)
	}
	if _, err := reader.Raw(context.Background(), "root", "/.well-known/library/logo.md"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("404 should be ErrNotFound, got %v", err)
	}
	if _, err := reader.Raw(context.Background(), "root", "/.well-known/library/branding.md"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("403 should be ErrUnauthorized, got %v", err)
	}
	if _, err := reader.Raw(context.Background(), "root", "/.well-known/library/favicon.md"); err == nil || errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("502 should be a plain error, got %v", err)
	}
}
