package web

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"
)

// HTMLErrorHandler answers every error a handler returns with the room's
// error page instead of Echo's JSON (ADR 0003), so a failed navigation that
// islands.js reloads in full reads as a page. Unexpected errors stay in the log.
func HTMLErrorHandler(c *echo.Context, err error) {
	if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Committed {
		return
	}
	view := errorView{Status: echo.StatusCode(err)}
	if view.Status == 0 {
		view.Status = http.StatusInternalServerError
		c.Logger().Error("request failed", "path", c.Request().URL.Path, "err", err)
	}
	// Only an HTTPError's message is written for readers; other errors may
	// carry internals.
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		view.Detail = httpErr.Message
	}
	view.Title = http.StatusText(view.Status)
	if view.Detail == "" {
		view.Detail = "The reading room could not answer this request."
	}
	if c.Request().Method == http.MethodHead {
		err = c.NoContent(view.Status)
	} else {
		err = renderError(c, view)
	}
	if err != nil {
		c.Logger().Error("error page failed", "status", view.Status, "err", err)
	}
}
