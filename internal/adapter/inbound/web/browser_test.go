//go:build browser && unix

// Real headless Chrome checks that clicks swap in place rather than reload the
// page, which markup tests cannot see. Run: go test -tags browser
// ./internal/adapter/inbound/web (CHROME_BIN picks the browser; skips without).
package web

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestBrowserClicksSwapInPlace(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{
			"/a.md":       {Title: "A", Path: "/a.md", HTML: `<p>Read <a href="b.md">the next one</a>.</p>`},
			"/b.md":       {Title: "B", Path: "/b.md", HTML: "<p>b</p>"},
			"/plans/":     {Title: "Index of /plans/", Path: "/plans/", HTML: `<ul><li><a href="c.md">c.md</a></li></ul>`},
			"/plans/c.md": {Title: "C", Path: "/plans/c.md", HTML: "<p>c</p>"},
		},
		neighbor: map[string]domain.Neighborhood{
			"/a.md": {Center: domain.Ref{World: "w.io", Path: "/a.md"},
				Out: []domain.Ref{{World: "w.io", Path: "/b.md"}}},
		},
	}
	srv := browserServer(t, svc)
	b := launchBrowser(t)

	cases := []struct {
		name, start, open, target, want string
	}{
		{name: "prose link", start: "/t/w.io/d/a.md",
			target: `.pane.focused .doc-body a[href^="/t/"]`, want: "/b.md"},
		{name: "listing row", start: "/t/w.io/d/plans/",
			target: `.pane.focused .listing a[href^="/t/"]`, want: "/plans/c.md"},
		{name: "graph node", start: "/t/w.io/d/a.md", open: "g",
			target: `#graph-overlay svg.graph a[data-node] circle`, want: "/b.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b.navigate(t, srv.URL+c.start)
			b.eval(t, "window.__stayed = true")
			if c.open != "" {
				b.key(t, c.open)
			}
			x, y := b.centre(t, c.target)
			b.click(t, x, y)
			b.waitFor(t, fmt.Sprintf("location.pathname.endsWith(%q)", c.want))
			if stayed := b.eval(t, "window.__stayed === true"); stayed != true {
				t.Errorf("the click reloaded the page instead of swapping in place")
			}
		})
	}
}

// browserServer serves the room (pages and static assets) over real HTTP.
// Requests are serialized: the fake service is not safe for concurrent use.
func browserServer(t *testing.T, svc *fakeReading) *httptest.Server {
	t.Helper()
	app := readingApp(t, svc)
	StaticRoutes(app, "")
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		app.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cdp is a minimal Chrome DevTools Protocol client over one page target.
type cdp struct {
	ws *websocket.Conn
	id int
}

type cdpReply struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func chromePath() string {
	if p := os.Getenv("CHROME_BIN"); p != "" {
		return p
	}
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
	} {
		if full, err := exec.LookPath(p); err == nil {
			return full
		}
	}
	return ""
}

func launchBrowser(t *testing.T) *cdp {
	t.Helper()
	chrome := chromePath()
	if chrome == "" {
		t.Skip("no Chrome found; set CHROME_BIN to run browser tests")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick a debugging port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // freed for Chrome; a race here only fails the launch, loudly

	profile := filepath.Join(t.TempDir(), "chrome")
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-crash-reporter",
		"--remote-allow-origins=*", fmt.Sprintf("--remote-debugging-port=%d", port), "--user-data-dir="+profile,
		"--window-size=1440,900", "about:blank")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own group: teardown reaches the helpers
	if err := cmd.Start(); err != nil {
		t.Fatalf("start chrome %s: %v", chrome, err)
	}
	t.Cleanup(func() {
		// Runs before t.TempDir's own removal. Killed helpers can finish a last
		// profile write, so clear the profile here, retrying briefly.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		for i := 0; i < 20 && os.RemoveAll(profile) != nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	})

	var wsURL string
	deadline := time.Now().Add(15 * time.Second)
	for wsURL == "" && time.Now().Before(deadline) {
		wsURL = pageTarget(port)
		if wsURL == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if wsURL == "" {
		t.Fatalf("chrome on port %d exposed no page target", port)
	}
	ws, err := websocket.Dial(wsURL, "", "http://127.0.0.1/")
	if err != nil {
		t.Fatalf("dial devtools %s: %v", wsURL, err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return &cdp{ws: ws}
}

// pageTarget is the page's devtools socket, or "" while Chrome is starting.
func pageTarget(port int) string {
	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json", port)) //nolint:noctx // local test browser
	if err != nil {
		return ""
	}
	defer func() { _ = res.Body.Close() }()
	var targets []struct {
		Type string `json:"type"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	if json.NewDecoder(res.Body).Decode(&targets) != nil {
		return ""
	}
	for _, tg := range targets {
		if tg.Type == "page" {
			return tg.WS
		}
	}
	return ""
}

// call sends one command and waits for its reply, skipping events.
func (c *cdp) call(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	c.id++
	if err := websocket.JSON.Send(c.ws, map[string]any{"id": c.id, "method": method, "params": params}); err != nil {
		t.Fatalf("send %s: %v", method, err)
	}
	for {
		var reply cdpReply
		if err := websocket.JSON.Receive(c.ws, &reply); err != nil {
			t.Fatalf("receive %s: %v", method, err)
		}
		if reply.ID != c.id {
			continue // an event, or a late reply
		}
		if reply.Error != nil {
			t.Fatalf("%s: %s", method, reply.Error.Message)
		}
		return reply.Result
	}
}

func (c *cdp) eval(t *testing.T, expr string) any {
	t.Helper()
	raw := c.call(t, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true})
	var res struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode eval of %q: %v", expr, err)
	}
	if res.Exception != nil {
		t.Fatalf("eval %q: %s", expr, res.Exception.Text)
	}
	return res.Result.Value
}

func (c *cdp) waitFor(t *testing.T, cond string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.eval(t, "!!("+cond+")") == true {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (at %v)", cond, c.eval(t, "location.pathname"))
}

func (c *cdp) navigate(t *testing.T, url string) {
	t.Helper()
	c.call(t, "Page.navigate", map[string]any{"url": url})
	c.waitFor(t, `document.readyState === "complete" && window.htmx`)
	time.Sleep(200 * time.Millisecond) // islands.js binds on DOMContentLoaded; let htmx finish processing
}

func (c *cdp) key(t *testing.T, key string) {
	t.Helper()
	params := map[string]any{"type": "keyDown", "key": key}
	if len([]rune(key)) == 1 {
		params["text"] = key
	}
	c.call(t, "Input.dispatchKeyEvent", params)
	c.call(t, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key})
	time.Sleep(300 * time.Millisecond) // the overlay's entrance
}

// centre is the viewport centre of the first element matching selector.
func (c *cdp) centre(t *testing.T, selector string) (x, y float64) {
	t.Helper()
	sel, _ := json.Marshal(selector)
	v := c.eval(t, fmt.Sprintf(`(() => { const el = document.querySelector(%s); if (!el) return null;
		el.scrollIntoView({block: "center"}); const r = el.getBoundingClientRect();
		return [r.x + r.width / 2, r.y + r.height / 2]; })()`, sel))
	pt, ok := v.([]any)
	if !ok || len(pt) != 2 {
		t.Fatalf("no element for %s", selector)
	}
	return pt[0].(float64), pt[1].(float64)
}

func (c *cdp) click(t *testing.T, x, y float64) {
	t.Helper()
	for _, typ := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		c.call(t, "Input.dispatchMouseEvent", map[string]any{
			"type": typ, "x": x, "y": y, "button": "left", "clickCount": 1,
		})
	}
}
