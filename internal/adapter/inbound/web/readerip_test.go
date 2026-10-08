package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

func TestReaderIP(t *testing.T) {
	cases := []struct{ name, realIP, remote, want string }{
		{"X-Real-IP wins", "203.0.113.9", "10.0.0.5:4444", "203.0.113.9"},
		{"bracketed IPv6", "[2001:db8::1]", "10.0.0.5:4444", "2001:db8::1"},
		{"garbage header falls back to peer", "not-an-ip", "10.0.0.5:4444", "10.0.0.5"},
		{"no header uses peer", "", "10.0.0.5:4444", "10.0.0.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := echo.New()
			var got string
			h := ReaderIP()(func(c *echo.Context) error {
				got = port.ReaderIP(c.Request().Context())
				return nil
			})
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			req.RemoteAddr = tc.remote
			if tc.realIP != "" {
				req.Header.Set(echo.HeaderXRealIP, tc.realIP)
			}
			if err := h(app.NewContext(req, httptest.NewRecorder())); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("reader IP = %q, want %q", got, tc.want)
			}
		})
	}
}
