package web

import (
	"context"
	"encoding/base64"
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

// The branding desk (ADR 0008): a world's owners set its name, stylesheet, and
// logo from inside the room. Each save is an ordinary publish of the
// /.well-known/library/ documents WorldBrands reads, so history and revert
// come with the world. Gated like every write: the turnstile, then the
// world's own write authz at the broker.

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

// WithWorldBrands gives the desk the resolver to invalidate after a save.
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
		vm.Name, vm.CSS, vm.LogoURL = h.brands.Desk(c.Request().Context(), world)
	}
	return vm
}

// brandDoc is one branding document a save writes. restore, when set, puts
// the submitted value back into the form if that document's write fails.
type brandDoc struct {
	path, body, title string
	restore           func(*brandingVM)
}

// SaveBranding publishes the changed documents. Empty fields leave a document
// alone; the clear checkboxes publish it without a fence, which the resolver
// reads as "none". branding.md is always written: it is the anchor.
func (h *ReadingHandler) SaveBranding(c *echo.Context) error {
	world := c.Param("world")
	ctx := c.Request().Context()
	name := strings.TrimSpace(c.FormValue("name"))
	css := strings.TrimSpace(c.FormValue("css"))
	fail := func(status int, msg string) error {
		vm := h.brandingVM(c, world)
		vm.Name, vm.CSS, vm.Error = name, css, msg
		return c.Render(status, "branding", vm)
	}

	logoBody, err := logoDocument(c)
	if err != nil {
		return fail(http.StatusBadRequest, err.Error())
	}
	docs := []brandDoc{{
		path:    WorldBrandDoc,
		body:    wrapFence("Branding", "How this world presents itself in the library.", "yaml", "", yamlMapping("name", name)),
		title:   "Branding",
		restore: func(vm *brandingVM) { vm.Name = name },
	}}
	switch {
	case css != "":
		docs = append(docs, brandDoc{
			path:    WorldBrandCSS,
			body:    wrapFence("Stylesheet", "Design tokens and rules the library loads for this world.", "css", "", css),
			title:   "Stylesheet",
			restore: func(vm *brandingVM) { vm.CSS = css },
		})
	case c.FormValue("clear_css") != "":
		docs = append(docs, brandDoc{
			path:  WorldBrandCSS,
			body:  "# Stylesheet\n\nNo stylesheet: the room's theme applies.\n",
			title: "Stylesheet",
		})
	}
	if logoBody != "" {
		docs = append(docs, brandDoc{path: WorldBrandLogo, body: logoBody, title: "Logo"})
	}
	// No batch write exists on the port, so each document lands on its own.
	// The cache is dropped after every success, so a failure re-renders from
	// what is actually persisted — except the field that failed, which keeps
	// the submitted text so a retry costs no retyping. The banner names what
	// saved and what did not: a partial save is never silent.
	var saved []string
	for _, d := range docs {
		meta := domain.PublishMeta{Title: d.title, Tags: []string{"library", "branding"}, Importance: "0.2"}
		err := h.publishBrandDoc(ctx, world, d.path, d.body, meta)
		if h.brands != nil {
			h.brands.Invalidate(world)
		}
		if err != nil {
			msg := d.title + " was not saved: " + editErrorMessage(err)
			if len(saved) > 0 {
				msg = "Saved " + strings.Join(saved, ", ") + ". " + msg + " Other fields show what is stored now."
			}
			vm := h.brandingVM(c, world)
			if d.restore != nil {
				d.restore(&vm)
			}
			vm.Error = msg
			return c.Render(editErrorStatus(err), "branding", vm)
		}
		saved = append(saved, strings.ToLower(d.title))
	}
	return c.Redirect(http.StatusSeeOther, "/w/"+url.PathEscape(world)+"/branding?saved=1")
}

// publishBrandDoc writes a generated document at its current version. A merge
// candidate (someone else wrote meanwhile) is resolved by taking ours: these
// documents are wholly regenerated from the form, never hand-merged.
func (h *ReadingHandler) publishBrandDoc(ctx context.Context, world, path, body string, meta domain.PublishMeta) error {
	version := 0
	switch d, err := h.reading.EditDraft(ctx, world, path); {
	case err == nil:
		version = d.Version
	case errors.Is(err, domain.ErrNotFound):
	default:
		return err
	}
	for range 2 {
		_, merge, err := h.reading.Publish(ctx, world, path, body, meta, version)
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

// logoDocument builds logo.md from the form: an uploaded file (SVG verbatim,
// anything else base64 with its content type), else pasted SVG, else the
// clear box. "" means leave the logo alone.
func logoDocument(c *echo.Context) (string, error) {
	const summary = "The mark shown beside this world's name."
	fh, err := c.FormFile("logo_file")
	switch {
	case err == nil && fh.Size > 0:
		if fh.Size > worldBrandMaxBytes {
			return "", errors.New("logo file is larger than 256 KB")
		}
		blob, declared, err := readUpload(fh)
		if err != nil {
			return "", err
		}
		ctype, err := checkLogo(blob, declared)
		if err != nil {
			return "", err
		}
		if ctype == "image/svg+xml" {
			return wrapFence("Logo", summary, "svg", "", string(blob)), nil
		}
		return wrapFence("Logo", summary, "base64", ctype, base64.StdEncoding.EncodeToString(blob)), nil
	case err != nil && !errors.Is(err, http.ErrMissingFile) && !errors.Is(err, http.ErrNotMultipart):
		return "", err
	}
	if svg := strings.TrimSpace(c.FormValue("logo_svg")); svg != "" {
		if _, err := checkLogo([]byte(svg), "image/svg+xml"); err != nil {
			return "", errors.New("pasted logo must be SVG markup: " + err.Error())
		}
		return wrapFence("Logo", summary, "svg", "", svg), nil
	}
	if c.FormValue("clear_logo") != "" {
		return "# Logo\n\nNo logo: the room's mark applies.\n", nil
	}
	return "", nil
}

// readUpload reads a bounded upload and returns the browser's declared type
// (checkLogo verifies it against the bytes).
func readUpload(fh *multipart.FileHeader) (blob []byte, ctype string, err error) {
	f, err := fh.Open()
	if err != nil {
		return nil, "", err
	}
	defer f.Close() //nolint:errcheck // read-only handle
	blob, err = io.ReadAll(io.LimitReader(f, worldBrandMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(blob) > worldBrandMaxBytes {
		return nil, "", errors.New("logo file is larger than 256 KB")
	}
	return blob, fh.Header.Get("Content-Type"), nil
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
