package web

import (
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// ReaderIP records the reader's IP on the request context for outbound
// calls (the broker keys its per-IP limits on it). X-Real-IP is used
// verbatim, so enable this only behind a proxy that overwrites that header
// (ingress-nginx does); otherwise the peer address is used.
func ReaderIP() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if ip := readerIP(req); ip != "" {
				c.SetRequest(req.WithContext(port.WithReaderIP(req.Context(), ip)))
			}
			return next(c)
		}
	}
}

func readerIP(req *http.Request) string {
	raw := strings.Trim(strings.TrimSpace(req.Header.Get(echo.HeaderXRealIP)), "[]")
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String()
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return ""
	}
	return host
}
