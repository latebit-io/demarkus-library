package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// postLibrarianForm submits a librarian form, as htmx or as a plain post.
func postLibrarianForm(t *testing.T, e *echo.Echo, path string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestAskLibrarian_StarterSubmitsItsQuestion(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{events: []domain.LibrarianEvent{{Kind: domain.LibrarianAnswer, Text: "a"}, {Kind: domain.LibrarianDone}}}
	e := librarianApp(t, lib)
	rec := postLibrarianForm(t, e, "/a/ask", url.Values{"question": {""}, "preset": {"Summarize this document"}, "trail": {"a"}, "idx": {"0"}}, false)
	if rec.Code != http.StatusSeeOther || len(lib.asked) != 1 || lib.asked[0] != "Summarize this document" {
		t.Errorf("status %d, asked %v; want the starter asked", rec.Code, lib.asked)
	}
	// A typed question wins over a starter.
	postLibrarianForm(t, e, "/a/ask", url.Values{"question": {"typed"}, "preset": {"starter"}, "trail": {"a"}, "idx": {"0"}}, false)
	if lib.asked[len(lib.asked)-1] != "typed" {
		t.Errorf("asked %v; the typed question must win", lib.asked)
	}
}

func TestAskLibrarian_LimitNoticeSaysWhen(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{askErr: &domain.LibrarianLimitError{RetryAfter: 12*time.Minute + time.Second}}
	e := librarianApp(t, lib)
	rec := postLibrarianForm(t, e, "/a/ask", url.Values{"question": {"one more"}, "trail": {"a"}, "idx": {"0"}}, false)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	if got := loc.Query().Get("notice"); got != "you have reached this hour's question limit — try again in 13 minutes" {
		t.Errorf("notice = %q", got)
	}
}

func TestAskLibrarian_ForwardsPersona(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{events: []domain.LibrarianEvent{{Kind: domain.LibrarianDone}}}
	e := librarianApp(t, lib)
	postLibrarianForm(t, e, "/a/ask", url.Values{"question": {"hi"}, "trail": {"a"}, "idx": {"0"}}, false)
	// The stock room: the role word is not a name, the universe term rides.
	if len(lib.personas) != 1 || lib.personas[0] != (domain.LibrarianPersona{Universe: "Universe"}) {
		t.Errorf("persona = %+v; want the stock persona", lib.personas)
	}
}

func TestStopLibrarian(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)
	if rec := postLibrarianForm(t, e, "/a/stop", url.Values{}, true); rec.Code != http.StatusNoContent {
		t.Errorf("htmx stop = %d; want 204", rec.Code)
	}
	rec := postLibrarianForm(t, e, "/a/stop", url.Values{"trail": {"w.io/d/x.md/~/a"}, "idx": {"1"}}, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/t/w.io/d/x.md/~/a" {
		t.Errorf("plain stop = %d %q; want 303 back to the trail", rec.Code, rec.Header().Get("Location"))
	}
	if len(lib.stopped) != 2 {
		t.Errorf("stops = %v; want both forwarded", lib.stopped)
	}
	if rec := postLibrarianForm(t, spikeApp(t), "/a/stop", url.Values{}, true); rec.Code != http.StatusNotFound {
		t.Errorf("feature-dark stop = %d; want 404", rec.Code)
	}
}

func TestResetLibrarian(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)
	rec := postLibrarianForm(t, e, "/a/new", url.Values{"trail": {"w.io/d/x.md/~/a"}, "idx": {"1"}}, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/t/w.io/d/x.md/~/a" || len(lib.resets) != 1 {
		t.Errorf("reset = %d %q (resets %v); want 303 back to the trail", rec.Code, rec.Header().Get("Location"), lib.resets)
	}

	lib.resetErr = domain.ErrLibrarianBusy
	rec = postLibrarianForm(t, e, "/a/new", url.Values{"trail": {"a"}, "idx": {"0"}}, false)
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/t/a?notice=") {
		t.Errorf("busy reset Location = %q; want the trail with a notice", loc)
	}
}

func TestTrailAskAbout(t *testing.T) {
	t.Parallel()

	doc := func(p string) paneAddr { return paneAddr{Kind: paneDoc, World: "w.io", Value: p} }
	lib := paneAddr{Kind: paneLibrarian}
	tests := []struct {
		name  string
		panes []paneAddr
		idx   int
		want  []paneAddr
	}{
		{"appends beside the document", []paneAddr{doc("/a.md"), doc("/b.md")}, 0, []paneAddr{doc("/a.md"), lib}},
		{"moves an existing librarian", []paneAddr{lib, doc("/a.md"), doc("/b.md")}, 2, []paneAddr{doc("/a.md"), doc("/b.md"), lib}},
		{"drops a librarian to the right", []paneAddr{doc("/a.md"), lib}, 0, []paneAddr{doc("/a.md"), lib}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := trailAskAbout(trail{Panes: tt.panes, Focus: tt.idx, Reader: -1, Meta: -1}, tt.idx)
			if len(got.Panes) != len(tt.want) || got.Focus != len(tt.want)-1 {
				t.Fatalf("trail = %+v; want %+v focused last", got, tt.want)
			}
			for i := range tt.want {
				if got.Panes[i] != tt.want[i] {
					t.Errorf("pane %d = %+v; want %+v", i, got.Panes[i], tt.want[i])
				}
			}
		})
	}
}

func TestContextDoc(t *testing.T) {
	t.Parallel()

	doc := paneAddr{Kind: paneDoc, World: "w.io", Value: "/x.md"}
	listing := paneAddr{Kind: paneDoc, World: "w.io", Value: "/ops/"}
	floor := paneAddr{Kind: paneFloor}
	lib := paneAddr{Kind: paneLibrarian}
	tests := []struct {
		name  string
		panes []paneAddr
		focus int
		want  int
	}{
		{"focused document", []paneAddr{floor, doc, lib}, 1, 1},
		{"librarian beside a document", []paneAddr{doc, listing, lib}, 2, 0},
		{"librarian with no document", []paneAddr{floor, lib}, 1, -1},
		{"focused listing is not a document", []paneAddr{listing, lib}, 0, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := contextDoc(trail{Panes: tt.panes}, tt.focus); got != tt.want {
				t.Errorf("contextDoc = %d; want %d", got, tt.want)
			}
		})
	}
}
