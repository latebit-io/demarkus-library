package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/latebit/demarkus-library/internal/core/domain"
)

// fakeGateway and fakeRenderer stand in for the outbound adapters.
type fakeGateway struct {
	raw domain.RawDocument
	err error
}

func (f fakeGateway) Fetch(string) (domain.RawDocument, error) { return f.raw, f.err }

type fakeRenderer struct {
	html string
	err  error
}

func (f fakeRenderer) Render(string) (string, error) { return f.html, f.err }

func TestReadRendersAndPopulatesDocument(t *testing.T) {
	svc := NewReadingService(
		fakeGateway{raw: domain.RawDocument{
			Source:   "world:6309",
			Path:     "/greeting.md",
			Body:     "# Hi",
			Metadata: map[string]string{"title": "Greeting"},
		}},
		fakeRenderer{html: "<h1>Hi</h1>"},
	)

	doc, err := svc.Read("/greeting.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.Title != "Greeting" {
		t.Errorf("title = %q, want Greeting", doc.Title)
	}
	if doc.Source != "world:6309" {
		t.Errorf("source = %q, want world:6309", doc.Source)
	}
	if doc.HTML != "<h1>Hi</h1>" {
		t.Errorf("html = %q", doc.HTML)
	}
}

func TestReadPropagatesGatewayError(t *testing.T) {
	for _, want := range []error{domain.ErrNotFound, domain.ErrUnauthorized} {
		svc := NewReadingService(fakeGateway{err: want}, fakeRenderer{})
		if _, err := svc.Read("/x.md"); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	}
}

func TestReadPropagatesRenderError(t *testing.T) {
	boom := errors.New("render boom")
	svc := NewReadingService(
		fakeGateway{raw: domain.RawDocument{Body: "x"}},
		fakeRenderer{err: boom},
	)
	if _, err := svc.Read("/x.md"); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestTitleFallsBackToBasename(t *testing.T) {
	svc := NewReadingService(
		fakeGateway{raw: domain.RawDocument{Path: "/notes/deploy.md"}},
		fakeRenderer{html: ""},
	)
	doc, err := svc.Read("/notes/deploy.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.Title != "deploy" {
		t.Errorf("title = %q, want deploy", doc.Title)
	}
}

func TestTitleFromMetadataIsTrimmed(t *testing.T) {
	if got := titleFor("/x.md", map[string]string{"title": "  Spaced  "}); got != "Spaced" {
		t.Errorf("got %q, want Spaced", got)
	}
	if !strings.HasPrefix(titleFor("/a/b/c.md", nil), "c") {
		t.Errorf("basename fallback broken")
	}
}
