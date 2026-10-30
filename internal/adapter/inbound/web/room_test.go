package web

import (
	"testing"

	"github.com/labstack/echo/v5"
)

// Every live stream outlives the request timeout: one registered here but
// missing from LongLived would be cut mid-watch.
func TestLiveStreamsAreLongLived(t *testing.T) {
	app := echo.New()
	liveRoutes(app, LiveHandler{})
	routes := app.Router().Routes()
	if len(routes) == 0 {
		t.Fatal("no live routes registered")
	}
	for _, route := range routes {
		if !LongLived(route.Path) {
			t.Errorf("%s is not LongLived: the request timeout would cut its stream", route.Path)
		}
	}
}
