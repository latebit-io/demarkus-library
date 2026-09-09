package web

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"gopkg.in/yaml.v3"
)

// The branding desk (ADR 0008): a world's owners set its name, stylesheet,
// and logo from inside the room. Each save is an ordinary publish of the
// documents WorldBrands reads, gated by the turnstile and the world's authz.

// brandingVM is the view model of the "branding" template.
type brandingVM struct {
	Title         string
	World         string
	WorldPath     string
	Authenticated bool
	User          string
	LibrarianURL  string
	Name          string // current in-world name
	CSS           string // current in-world stylesheet source
	LogoURL       string // current in-world logo (empty ⇒ none)
	Error         string
	Notice        string
	CancelURL     string
}

// The documents a save writes, titled once here so the body and its catalog
// metadata cannot drift apart.
const (
	brandNameTitle = "Branding"
	brandCSSTitle  = "Stylesheet"
	brandLogoTitle = "Logo"
)

// brandDoc is one branding document a save writes. restore, when set, puts
// the submitted value back into the form if that document's write fails.
type brandDoc struct {
	path, body, title string
	restore           func(*brandingVM)
}

// WithWorldBrands gives the desk the resolver to read current branding from
// and to invalidate after a save.
func (h ReadingHandler) WithWorldBrands(w *WorldBrands) ReadingHandler {
	h.brands = w
	return h
}

// BrandingForm serves the desk pre-filled from the world's current documents.
func (h *ReadingHandler) BrandingForm(c *echo.Context) error {
	world := c.Param("world")
	vm := h.brandingVM(c, world)
	if c.QueryParam("saved") == "1" {
		vm.Notice = "Saved. Readers see the new identity on their next page."
	}
	return c.Render(http.StatusOK, "branding", vm)
}

func (h *ReadingHandler) brandingVM(c *echo.Context, world string) brandingVM {
	vm := brandingVM{
		Title:         "Branding: " + world,
		World:         world,
		WorldPath:     url.PathEscape(world),
		Authenticated: c.Get(authedKey) != nil,
		User:          userEmail(c),
		CancelURL:     "/w/" + url.PathEscape(world) + "/u",
	}
	if h.lib != nil {
		vm.LibrarianURL = "/a"
	}
	if h.brands != nil {
		stored := h.brands.Desk(c.Request().Context(), world)
		vm.Name, vm.CSS, vm.LogoURL = stored.Name, stored.CSS, stored.LogoURL
	}
	return vm
}

// SaveBranding publishes the changed documents. Empty fields leave a document
// alone; the clear checkboxes publish it without a fence, which the resolver
// reads as "none". branding.md is always written: it is the anchor.
func (h *ReadingHandler) SaveBranding(c *echo.Context) error {
	world := c.Param("world")
	ctx := c.Request().Context()
	name := strings.TrimSpace(c.FormValue("name"))
	css := strings.TrimSpace(c.FormValue("css"))

	logoBody, err := logoDocument(c)
	if err != nil {
		vm := h.brandingVM(c, world)
		vm.Name, vm.CSS, vm.Error = name, css, err.Error()
		return c.Render(http.StatusBadRequest, "branding", vm)
	}
	docs := brandingDocs(c, name, css, logoBody)

	// No batch write exists on the port, so documents land one at a time and
	// the cache is dropped after each success; renderPartialSave reports what
	// did and did not persist.
	var saved []string
	for _, doc := range docs {
		err := h.publishBrandDoc(ctx, world, doc)
		if h.brands != nil {
			h.brands.Invalidate(world)
		}
		if err != nil {
			return h.renderPartialSave(c, doc, saved, err)
		}
		saved = append(saved, strings.ToLower(doc.title))
	}
	return c.Redirect(http.StatusSeeOther, "/w/"+url.PathEscape(world)+"/branding?saved=1")
}

// brandingDocs assembles what this save writes: the anchor always, the
// stylesheet when submitted or cleared, the logo only when one was supplied.
func brandingDocs(c *echo.Context, name, css, logoBody string) []brandDoc {
	docs := []brandDoc{{
		path:    WorldBrandDoc,
		title:   brandNameTitle,
		body:    fencedDoc{Title: brandNameTitle, Summary: "How this world presents itself in the library.", Fence: fence{Lang: "yaml", Content: yamlMapping("name", name)}}.markdown(),
		restore: func(vm *brandingVM) { vm.Name = name },
	}}
	switch {
	case css != "":
		docs = append(docs, brandDoc{
			path:    WorldBrandCSS,
			title:   brandCSSTitle,
			body:    fencedDoc{Title: brandCSSTitle, Summary: "Design tokens and rules the library loads for this world.", Fence: fence{Lang: "css", Content: css}}.markdown(),
			restore: func(vm *brandingVM) { vm.CSS = css },
		})
	case c.FormValue("clear_css") != "":
		docs = append(docs, brandDoc{
			path:  WorldBrandCSS,
			title: brandCSSTitle,
			body:  "# " + brandCSSTitle + "\n\nNo stylesheet: the room's theme applies.\n",
		})
	}
	if logoBody != "" {
		docs = append(docs, brandDoc{path: WorldBrandLogo, title: brandLogoTitle, body: logoBody})
	}
	return docs
}

// renderPartialSave re-renders the desk from persisted state after a failed
// write, naming what saved and what did not so a partial save is never silent.
func (h *ReadingHandler) renderPartialSave(c *echo.Context, failed brandDoc, saved []string, err error) error {
	msg := failed.title + " was not saved: " + editErrorMessage(err)
	if len(saved) > 0 {
		msg = "Saved " + strings.Join(saved, ", ") + ". " + msg + " Other fields show what is stored now."
	}
	vm := h.brandingVM(c, c.Param("world"))
	if failed.restore != nil {
		failed.restore(&vm)
	}
	vm.Error = msg
	return c.Render(editErrorStatus(err), "branding", vm)
}

// publishBrandDoc writes a generated document at its current version. A merge
// candidate is resolved by taking ours: these documents are regenerated whole
// from the form, never hand-merged.
func (h *ReadingHandler) publishBrandDoc(ctx context.Context, world string, doc brandDoc) error {
	meta := domain.PublishMeta{Title: doc.title, Tags: []string{"library", "branding"}, Importance: "0.2"}
	version := 0
	switch draft, err := h.reading.EditDraft(ctx, world, doc.path); {
	case err == nil:
		version = draft.Version
	case errors.Is(err, domain.ErrNotFound):
	default:
		return err
	}
	for range 2 {
		_, merge, err := h.reading.Publish(ctx, world, doc.path, doc.body, meta, version)
		if err != nil {
			return err
		}
		if merge == nil {
			return nil
		}
		version = merge.PublishAtVersion
	}
	return domain.ErrConflict
}

// logoDocument builds logo.md from the form: an uploaded file, else pasted
// SVG, else the clear box. "" means leave the current logo alone.
func logoDocument(c *echo.Context) (string, error) {
	fh, err := c.FormFile("logo_file")
	switch {
	case err == nil && fh.Size > 0:
		upload, err := readUpload(fh)
		if err != nil {
			return "", err
		}
		return logoMarkdown(upload.blob, upload.declared)
	case err != nil && !errors.Is(err, http.ErrMissingFile) && !errors.Is(err, http.ErrNotMultipart):
		return "", err
	}
	if svg := strings.TrimSpace(c.FormValue("logo_svg")); svg != "" {
		body, err := logoMarkdown([]byte(svg), "image/svg+xml")
		if err != nil {
			return "", errors.New("pasted logo must be SVG markup: " + err.Error())
		}
		return body, nil
	}
	if c.FormValue("clear_logo") != "" {
		return "# " + brandLogoTitle + "\n\nNo logo: the room's mark applies.\n", nil
	}
	return "", nil
}

// logoUpload is a submitted logo file: its bytes and the type the browser
// declared for them (checkLogo decides whether that claim holds).
type logoUpload struct {
	blob     []byte
	declared string
}

// readUpload reads a bounded upload.
func readUpload(fh *multipart.FileHeader) (logoUpload, error) {
	if fh.Size > worldBrandMaxBytes {
		return logoUpload{}, errors.New("logo file is larger than 256 KB")
	}
	f, err := fh.Open()
	if err != nil {
		return logoUpload{}, err
	}
	defer f.Close() //nolint:errcheck // read-only handle
	blob, err := io.ReadAll(io.LimitReader(f, worldBrandMaxBytes+1))
	if err != nil {
		return logoUpload{}, err
	}
	if len(blob) > worldBrandMaxBytes {
		return logoUpload{}, errors.New("logo file is larger than 256 KB")
	}
	return logoUpload{blob: blob, declared: fh.Header.Get("Content-Type")}, nil
}

// yamlMapping renders one key/value as a YAML mapping so a long or odd value
// is quoted and folded with the indentation the decoder expects.
func yamlMapping(key, value string) string {
	out, err := yaml.Marshal(map[string]string{key: value})
	if err != nil {
		return key + `: ""`
	}
	return strings.TrimRight(string(out), "\n")
}
