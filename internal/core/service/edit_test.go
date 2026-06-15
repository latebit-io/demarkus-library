package service

import (
	"reflect"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestEditDraftSplitsMetadataAndVersion(t *testing.T) {
	gw := fakeGateway{raw: domain.RawDocument{
		Path: "/adr/0007.md",
		Body: "# ADR 7\n\nbody",
		Metadata: map[string]string{
			"title":      "ADR 7",
			"tags":       "adr, decision, status:accepted",
			"importance": "0.9",
			"version":    "7",
		},
	}}
	draft, err := NewReadingService(gw, fakeRenderer{}, nil).EditDraft(t.Context(), "root", "/adr/0007.md")
	if err != nil {
		t.Fatalf("EditDraft: %v", err)
	}
	if draft.Body != "# ADR 7\n\nbody" || draft.Title != "ADR 7" || draft.Version != 7 {
		t.Errorf("draft = %+v", draft)
	}
	if draft.Status != "accepted" {
		t.Errorf("status = %q, want accepted (split from tags)", draft.Status)
	}
	if !reflect.DeepEqual(draft.Tags, []string{"adr", "decision"}) {
		t.Errorf("tags = %v, want [adr decision] (status axis removed)", draft.Tags)
	}
	if draft.Importance != "0.9" {
		t.Errorf("importance = %q", draft.Importance)
	}
}

func TestEditDraftRejectsUnreadableVersion(t *testing.T) {
	// A fetched doc with missing/malformed version must error, not silently
	// become version 0 (the create sentinel) and bypass the conflict guard.
	for _, bad := range []string{"", "nope"} {
		gw := fakeGateway{raw: domain.RawDocument{
			Path: "/x.md", Body: "# X", Metadata: map[string]string{"version": bad},
		}}
		if _, err := NewReadingService(gw, fakeRenderer{}, nil).EditDraft(t.Context(), "root", "/x.md"); err == nil {
			t.Errorf("version %q: EditDraft accepted, want error", bad)
		}
	}
}

func TestPublishWritesThenRereadsLive(t *testing.T) {
	called := ""
	gw := fakeGateway{
		called: &called,
		raw:    domain.RawDocument{Path: "/x.md", Body: "# X", Metadata: map[string]string{"version": "3"}},
	}
	doc, err := NewReadingService(gw, fakeRenderer{html: "<h1>X</h1>"}, nil).
		Publish(t.Context(), "root", "/x.md", "# X", domain.PublishMeta{Tags: []string{"a"}}, 2)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// After Publish the gateway's last call must be the live re-read (Fetch),
	// so the room shows fresh content (focused-live).
	if called != "Fetch" {
		t.Errorf("last gateway call = %q, want Fetch (re-read after write)", called)
	}
	if doc.Path != "/x.md" || doc.HTML != "<h1>X</h1>" {
		t.Errorf("re-read doc = %+v", doc)
	}
}

func TestPublishPropagatesConflict(t *testing.T) {
	gw := fakeGateway{publishErr: domain.ErrConflict}
	_, err := NewReadingService(gw, fakeRenderer{}, nil).
		Publish(t.Context(), "root", "/x.md", "body", domain.PublishMeta{}, 1)
	if err != domain.ErrConflict {
		t.Errorf("err = %v, want ErrConflict (no re-read, surfaced to the desk)", err)
	}
}

func TestAppendWritesThenRereadsLive(t *testing.T) {
	called := ""
	gw := fakeGateway{
		called: &called,
		raw:    domain.RawDocument{Path: "/log.md", Body: "# Log", Metadata: map[string]string{"version": "4"}},
	}
	doc, err := NewReadingService(gw, fakeRenderer{html: "<h1>Log</h1>"}, nil).
		Append(t.Context(), "root", "/log.md", "\n- entry")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if called != "Fetch" {
		t.Errorf("last gateway call = %q, want Fetch (re-read after append)", called)
	}
	if doc.Path != "/log.md" {
		t.Errorf("re-read doc = %+v", doc)
	}
}

func TestAppendPropagatesError(t *testing.T) {
	gw := fakeGateway{publishErr: domain.ErrWriteUnsupported}
	_, err := NewReadingService(gw, fakeRenderer{}, nil).Append(t.Context(), "h.io", "/x.md", "more")
	if err != domain.ErrWriteUnsupported {
		t.Errorf("err = %v, want ErrWriteUnsupported", err)
	}
}

func TestPreviewRenders(t *testing.T) {
	r, err := NewReadingService(fakeGateway{}, fakeRenderer{html: "<p>hi</p>"}, nil).Preview("hi")
	if err != nil || r.HTML != "<p>hi</p>" {
		t.Errorf("Preview = %+v, %v", r, err)
	}
}
