package web

// Live streams end before their bearer does: an open tab refreshes no login,
// so a stream held past its bearer reads with a lapsed one, and the broker's
// watch pool, fed by the readers' reads, is left with nothing to lend.

import (
	"context"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web/session"
)

// renewBeforeExpiry bounds a live stream's ctx by renewAt: its watch closes
// with ctx, the stream ends bare, and EventSource reconnects through the
// turnstile.
func renewBeforeExpiry(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		at, ok := renewAt(c.Request().Context())
		if !ok {
			return next(c)
		}
		ctx, cancel := context.WithDeadline(c.Request().Context(), at)
		defer cancel()
		c.SetRequest(c.Request().WithContext(ctx))
		return next(c)
	}
}

// renewAt is when a stream renews: inside the session's refresh window, so the
// reconnect gets a fresh bearer, which its first live read lends to the pool.
// A bearer already inside the window (a refresh came back short) is held no
// longer than it lasts. False without an expiry (quic mode).
func renewAt(ctx context.Context) (time.Time, bool) {
	expires := bearer.ExpiryFromContext(ctx)
	if expires.IsZero() {
		return time.Time{}, false
	}
	at := expires.Add(-session.DefaultRefreshSkew / 2)
	if !time.Now().Before(at) {
		at = expires
	}
	return at, true
}
