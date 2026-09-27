package web

import "github.com/labstack/echo/v5"

// librarianRoutes registers the AI librarian (Phase 4): the entrance, the
// pane's ask form, the SSE stream (ContextTimeout-exempt — see the
// composition root; ?slow= keeps the transport-soak diagnostic), and the
// stop and start-over controls. All degrade when no librarian is wired.
func librarianRoutes(e *echo.Echo, handler LibrarianHandler, mw ...echo.MiddlewareFunc) {
	e.GET("/a", handler.LibrarianEntrance, mw...)
	e.POST(LibrarianAskPath, handler.AskLibrarian, mw...)
	e.GET(LibrarianStreamPath, handler.LibrarianStream, mw...)
	e.POST("/a/stop", handler.StopLibrarian, mw...)
	e.POST("/a/new", handler.ResetLibrarian, mw...)
}
