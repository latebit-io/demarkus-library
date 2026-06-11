package web

import (
	"errors"
	"html/template"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/latebit/demarkus-library/internal/core/domain"
	"github.com/latebit/demarkus-library/internal/core/port"
)

// ReadingHandler serves rendered demarkus documents. It depends on the inbound
// port, not the concrete service.
type ReadingHandler struct {
	reading    port.ReadingService
	defaultDoc string
}

// NewReadingHandler binds the reading service and the document shown at /.
func NewReadingHandler(reading port.ReadingService, defaultDoc string) ReadingHandler {
	return ReadingHandler{reading: reading, defaultDoc: defaultDoc}
}

// page is the view model for the document template.
type page struct {
	Title   string
	Host    string
	Path    string
	Content template.HTML // sanitized by the markdown adapter
}

// Root renders the configured default document.
func (h *ReadingHandler) Root(c *echo.Context) error {
	return h.render(c, h.defaultDoc)
}

// Doc renders the document at the wildcard path (/d/<path>).
func (h *ReadingHandler) Doc(c *echo.Context) error {
	return h.render(c, "/"+c.Param("*"))
}

func (h *ReadingHandler) render(c *echo.Context, path string) error {
	doc, err := h.reading.Read(path)
	switch {
	case err == nil:
		return c.Render(http.StatusOK, "page.html", page{
			Title:   doc.Title,
			Host:    doc.Source,
			Path:    doc.Path,
			Content: template.HTML(doc.HTML), //nolint:gosec // sanitized in the markdown adapter
		})
	case errors.Is(err, domain.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, path+": not found")
	case errors.Is(err, domain.ErrUnauthorized):
		return echo.NewHTTPError(http.StatusUnauthorized, path+": not authorized")
	default:
		c.Logger().Error("read failed", "path", path, "err", err)
		return echo.NewHTTPError(http.StatusBadGateway, "reading room is unreachable")
	}
}
