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

// brandingWriter is the slice of the editor port the desk needs: the version a
// generated document currently sits at, and a whole-document republish. The
// desk never reads rendered documents, so it takes neither the reader nor the
// append side.
type brandingWriter interface {
	EditDraft(ctx context.Context, world, path string) (domain.EditDraft, error)
	Publish(ctx context.Context, world, path, body string, meta domain.PublishMeta, expectedVersion int) (domain.Document, *domain.MergeCandidate, error)
}

// BrandingHandler serves the desk. It is the whole surface family: two routes
// over the shared nav chrome and a two-method write port.
type BrandingHandler struct {
	writer brandingWriter
	brands *WorldBrands // read for the current values, invalidated after a save; nil ⇒ nothing current to show
	chrome chromeBuilder
}

// brandingVM is the view model of the "branding" template.
type brandingVM struct {
	navChrome // the shared nav (chrome.go)
	Title     string
	World     string
	WorldPath string
	Name      string // current in-world name
	LogoSVG   string // pasted SVG kept across a failed save (file inputs cannot be refilled)
	Tokens    []tokenField
	CSS       string // current in-world stylesheet source
	LogoURL   string // current in-world logo (empty ⇒ none)
	Error     string
	Notice    string
	CancelURL string
}

// tokenField is one design-token input on the desk: the token an operator
// may set without writing CSS, its stored value, and an example.
type tokenField struct {
	Name  string
	Value string
	Hint  string
}

// brandingForm is one submitted desk form, parsed and validated once.
type brandingForm struct {
	name     string
	logoSVG  string
	tokens   map[string]string
	css      string
	clearCSS bool
	logoBody string
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

// BrandingForm serves the desk pre-filled from the world's current documents.
func (h *BrandingHandler) BrandingForm(c *echo.Context) error {
	world := c.Param("world")
	vm := h.brandingVM(c, world)
	if c.QueryParam("saved") == "1" {
		vm.Notice = "Saved. Readers see the new identity on their next page."
	}
	return c.Render(http.StatusOK, "branding", vm)
}

func (h *BrandingHandler) brandingVM(c *echo.Context, world string) brandingVM {
	vm := brandingVM{
		navChrome: h.chrome.build(c),
		Title:     "Branding: " + world,
		World:     world,
		WorldPath: url.PathEscape(world),
		CancelURL: "/w/" + url.PathEscape(world) + "/u",
	}
	var stored brandDesk
	if h.brands != nil {
		stored = h.brands.Desk(c.Request().Context(), world)
		vm.Name, vm.CSS, vm.LogoURL = stored.Name, stored.CSS, stored.LogoURL
	}
	vm.Tokens = tokenFields(stored.Tokens)
	return vm
}

// tokenFields lists every settable token, filled in from what is stored.
func tokenFields(stored map[string]string) []tokenField {
	fields := make([]tokenField, 0, len(brandTokens))
	for _, name := range brandTokens {
		fields = append(fields, tokenField{Name: name, Value: stored[name], Hint: tokenHints[name]})
	}
	return fields
}

// SaveBranding publishes the changed documents. Empty fields leave a document
// alone; the clear checkboxes publish it without a fence, which the resolver
// reads as "none". branding.md is always written: it is the anchor.
func (h *BrandingHandler) SaveBranding(c *echo.Context) error {
	world := c.Param("world")
	ctx := c.Request().Context()

	form, err := readBrandingForm(c)
	if err != nil {
		vm := h.brandingVM(c, world)
		form.restore(&vm)
		vm.Error = err.Error()
		return c.Render(http.StatusBadRequest, "branding", vm)
	}
	docs := brandingDocs(form)

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

// readBrandingForm parses and validates a submitted desk form. Tokens are
// checked here so an unsafe value is refused before anything is published.
func readBrandingForm(c *echo.Context) (brandingForm, error) {
	form := brandingForm{
		name:     strings.TrimSpace(c.FormValue("name")),
		logoSVG:  strings.TrimSpace(c.FormValue("logo_svg")),
		css:      strings.TrimSpace(c.FormValue("css")),
		clearCSS: c.FormValue("clear_css") != "",
		tokens:   map[string]string{},
	}
	for _, name := range brandTokens {
		if value := strings.TrimSpace(c.FormValue("token_" + name)); value != "" {
			form.tokens[name] = value
		}
	}
	if _, err := tokensCSS(form.tokens); err != nil {
		return form, err
	}
	logoBody, err := logoDocument(c)
	if err != nil {
		return form, err
	}
	form.logoBody = logoBody
	return form, nil
}

// restore puts a submitted form back into the view model after a failure, so
// a rejected save never costs the operator their typing.
func (f brandingForm) restore(vm *brandingVM) {
	vm.Name, vm.CSS, vm.LogoSVG = f.name, f.css, f.logoSVG
	vm.Tokens = tokenFields(f.tokens)
}

// brandingDocs assembles what this save writes: the anchor always, the
// stylesheet when submitted or cleared, the logo only when one was supplied.
func brandingDocs(form brandingForm) []brandDoc {
	docs := []brandDoc{{
		path:    WorldBrandDoc,
		title:   brandNameTitle,
		body:    fencedDoc{Title: brandNameTitle, Summary: "How this world presents itself in the library.", Fence: fence{Lang: "yaml", Content: brandingYAML(form)}}.markdown(),
		restore: func(vm *brandingVM) { vm.Name, vm.Tokens = form.name, tokenFields(form.tokens) },
		// the logo rides its own document; nothing to restore here
	}}
	switch {
	case form.css != "":
		docs = append(docs, brandDoc{
			path:    WorldBrandCSS,
			title:   brandCSSTitle,
			body:    fencedDoc{Title: brandCSSTitle, Summary: "Rules the library loads for this world, after its design tokens.", Fence: fence{Lang: "css", Content: form.css}}.markdown(),
			restore: func(vm *brandingVM) { vm.CSS = form.css },
		})
	case form.clearCSS:
		docs = append(docs, brandDoc{
			path:  WorldBrandCSS,
			title: brandCSSTitle,
			body:  "# " + brandCSSTitle + "\n\nNo stylesheet: the room's theme applies.\n",
		})
	}
	if form.logoBody != "" {
		docs = append(docs, brandDoc{path: WorldBrandLogo, title: brandLogoTitle, body: form.logoBody})
	}
	return docs
}

// brandingYAML renders the anchor document's fence: the name and any tokens.
func brandingYAML(form brandingForm) string {
	out, err := yaml.Marshal(brandingFile{Name: form.name, Theme: form.tokens})
	if err != nil {
		return `name: ""`
	}
	return strings.TrimRight(string(out), "\n")
}

// renderPartialSave re-renders the desk from persisted state after a failed
// write, naming what saved and what did not so a partial save is never silent.
func (h *BrandingHandler) renderPartialSave(c *echo.Context, failed brandDoc, saved []string, err error) error {
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
func (h *BrandingHandler) publishBrandDoc(ctx context.Context, world string, doc brandDoc) error {
	meta := domain.PublishMeta{Title: doc.title, Tags: []string{"library", "branding"}, Importance: "0.2"}
	version := 0
	switch draft, err := h.writer.EditDraft(ctx, world, doc.path); {
	case err == nil:
		version = draft.Version
	case errors.Is(err, domain.ErrNotFound):
	default:
		return err
	}
	for range 2 {
		_, merge, err := h.writer.Publish(ctx, world, doc.path, doc.body, meta, version)
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
