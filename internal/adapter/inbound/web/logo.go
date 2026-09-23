package web

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

// Logo validation. A world is a shared write surface, so a logo is untrusted
// wherever it comes from: the same check runs on an upload at the desk and on
// the bytes read back out of a world's document (ADR 0008).

const (
	logoSummary    = "The mark shown beside this world's name."
	faviconSummary = "The browser-tab icon for the whole room."
)

// rasterTypes are the inert image types a logo may be; anything that is not
// one of these or an inert SVG is refused.
var rasterTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// svgActive matches SVG constructs that could run or fetch: scripts, event
// handlers, javascript: URLs, embedded documents, and external references.
var svgActive = regexp.MustCompile(`(?i)<script|<foreignobject|<iframe|<embed|<object|javascript:|\son[a-z]+\s*=|href\s*=\s*["']?\s*(https?:|//)|@import|url\(\s*["']?\s*(https?:|//)|data:text/html`)

// checkLogo validates bytes against their declared type and returns the type
// to serve: raster bytes must sniff as what they claim, an SVG must be inert.
func checkLogo(blob []byte, declared string) (string, error) {
	if len(blob) == 0 {
		return "", errors.New("logo is empty")
	}
	if len(blob) > worldBrandMaxBytes {
		return "", errors.New("logo is larger than 256 KB")
	}
	declared = strings.ToLower(strings.TrimSpace(strings.SplitN(declared, ";", 2)[0]))
	if declared == "application/octet-stream" {
		declared = "" // the browser's "I don't know"; sniff instead
	}
	trimmed := bytes.TrimSpace(blob)
	isSVG := bytes.HasPrefix(trimmed, []byte("<svg")) ||
		(bytes.HasPrefix(trimmed, []byte("<?xml")) && bytes.Contains(trimmed, []byte("<svg")))
	if declared == "image/svg+xml" || (declared == "" && isSVG) {
		if !isSVG {
			return "", errors.New("logo is not SVG markup")
		}
		if svgActive.Match(trimmed) {
			return "", errors.New("SVG logo must not contain scripts, event handlers, or external references")
		}
		return "image/svg+xml", nil
	}
	sniffed := strings.SplitN(http.DetectContentType(blob), ";", 2)[0]
	if declared == "" {
		declared = sniffed
	}
	if !rasterTypes[declared] {
		return "", errors.New("logo must be PNG, JPEG, GIF, WebP, or SVG")
	}
	if sniffed != declared {
		return "", errors.New("logo bytes do not match the declared image type")
	}
	return declared, nil
}

// imageMarkdown validates image bytes and wraps them as a branding document
// (logo.md or favicon.md): SVG verbatim, a raster image base64-encoded with
// its type in the info string.
func imageMarkdown(blob []byte, declared, title, summary string) (string, error) {
	ctype, err := checkLogo(blob, declared)
	if err != nil {
		return "", err
	}
	doc := fencedDoc{Title: title, Summary: summary}
	if ctype == "image/svg+xml" {
		doc.Fence = fence{Lang: "svg", Content: string(blob)}
	} else {
		doc.Fence = fence{Lang: "base64", Info: ctype, Content: base64.StdEncoding.EncodeToString(blob)}
	}
	return doc.markdown(), nil
}

// decodeLogo unwraps logo.md and validates what it finds; an unreadable or
// unsafe logo is simply absent, never served.
func decodeLogo(body string) *worldAsset {
	f, ok := firstFence(body)
	if !ok {
		return nil
	}
	var blob []byte
	declared := ""
	switch f.Lang {
	case "svg", "xml":
		blob, declared = []byte(f.Content), "image/svg+xml"
	case "base64":
		var err error
		if blob, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(f.Content), "")); err != nil {
			return nil
		}
		declared = f.Info
	default:
		return nil
	}
	ctype, err := checkLogo(blob, declared)
	if err != nil {
		return nil
	}
	return &worldAsset{ctype: ctype, blob: blob}
}
