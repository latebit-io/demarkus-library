package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestHTMLErrorHandlerAnswersInHTML(t *testing.T) {
	cases := []struct {
		name, target string
		status       int
		want         []string
	}{
		{"malformed trail", "/t/w.io/d/~/w.io/d/x.md", http.StatusBadRequest,
			[]string{"<h1>Bad Request</h1>", "trail link is malformed"}},
		{"unknown route", "/nowhere", http.StatusNotFound, []string{"<h1>Not Found</h1>"}},
	}
	for _, tc := range cases {
		rec := get(readingApp(t, &fakeReading{}), tc.target)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.status)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q, want HTML", tc.name, ct)
		}
		body := rec.Body.String()
		for _, w := range append(tc.want, `class="error-page"`) {
			if !strings.Contains(body, w) {
				t.Errorf("%s: body missing %q", tc.name, w)
			}
		}
	}
}
