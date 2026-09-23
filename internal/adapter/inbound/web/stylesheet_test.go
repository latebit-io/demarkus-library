package web

import (
	"strings"
	"testing"
)

func TestCheckCSS(t *testing.T) {
	tests := []struct {
		name   string
		sheet  string
		reject bool
	}{
		{name: "tokens", sheet: ":root { --paper: #fff; }"},
		{name: "relative url", sheet: "body { background: url(/static/paper.png); }"},
		{name: "data url", sheet: "@font-face { src: url(data:font/woff2;base64,AAAA); }"},
		{name: "same-origin font", sheet: "@font-face { src: url(\"/theme/worlds/x/font.woff2\"); }"},
		{name: "external url", sheet: `input[name=_csrf][value^="a"] { background: url(https://evil.example/a); }`, reject: true},
		{name: "protocol-relative url", sheet: "body { background: url('//evil.example/a'); }", reject: true},
		{name: "image-set string", sheet: `body { background: image-set("//evil.example/a" 1x); }`, reject: true},
		{name: "import", sheet: "@import url(/theme/site.css);", reject: true},
		{name: "external font", sheet: "@font-face { src: url(http://fonts.example/a.woff); }", reject: true},
		{name: "expression", sheet: "div { width: expression(alert(1)); }", reject: true},
		{name: "behavior", sheet: "div { behavior: url(a.htc); }", reject: true},
		{name: "markup", sheet: "</style><script>1</script>", reject: true},
		{name: "escaped import", sheet: `\@import url(/x.css);`, reject: true},
		{name: "escaped url scheme", sheet: `body { background: url(https\:\/\/evil.example/a); }`, reject: true},
		{name: "escaped content", sheet: `a::after { content: "\2014"; }`, reject: true},
		{name: "oversized", sheet: strings.Repeat("a", worldBrandMaxBytes+1), reject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkCSS(tt.sheet)
			if tt.reject && err == nil {
				t.Errorf("accepted %q", tt.sheet)
			}
			if !tt.reject && err != nil {
				t.Errorf("rejected %q: %v", tt.sheet, err)
			}
		})
	}
}
