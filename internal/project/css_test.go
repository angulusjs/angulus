package project

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScopeCSSAcceptsLocalResourceQueriesAndPreservesFragments(t *testing.T) {
	root := t.TempDir()
	font := []byte("font fixture bytes")
	svg := []byte("<svg/>")
	writeFixture(t, root, "font.woff2", string(font))
	writeFixture(t, root, "icon.svg", string(svg))
	path := writeFixture(t, root, "component.css", `@font-face { font-family: Fixture; src: url('font.woff2?v=1'); }
.icon { background: url("icon.svg?version=2#symbol"); }`)
	got, diagnostics := scopeCSS([]string{path}, "data-a-query")
	if hasErrors(diagnostics) {
		t.Fatalf("local asset cache-busters must not invalidate CSS: %+v", diagnostics)
	}
	if !strings.Contains(got, ";base64,"+base64.StdEncoding.EncodeToString(font)+`")`) {
		t.Fatalf("query-bearing font URL did not embed the original bytes: %s", got)
	}
	if !strings.Contains(got, ";base64,"+base64.StdEncoding.EncodeToString(svg)+`#symbol")`) {
		t.Fatalf("query-bearing SVG URL lost its bytes or fragment: %s", got)
	}
	if strings.Contains(got, "?v=") || strings.Contains(got, "?version=") {
		t.Fatalf("cache-buster query was appended to an embedded data payload: %s", got)
	}
}

func cssFixture(t *testing.T, name, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScopeCSSGroupedCombinatorsAndPseudoElements(t *testing.T) {
	path := cssFixture(t, "component.css", `.card, #menu > li.active:hover::before { color: red; }
a + b ~ c:first-child { color: blue; }
article :not(.hidden, [data-state="x,y"]) { color: black; }`)

	got, diagnostics := scopeCSS([]string{path}, "data-a-test")
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diagnostics)
	}
	for _, selector := range []string{
		`.card[data-a-test],#menu[data-a-test]>li.active:hover[data-a-test]::before`,
		`a[data-a-test]+b[data-a-test]~c:first-child[data-a-test]`,
		`article[data-a-test] :not(.hidden,[data-state="x,y"])[data-a-test]`,
	} {
		if !strings.Contains(got, selector) {
			t.Errorf("output does not contain scoped selector %q:\n%s", selector, got)
		}
	}
}

func TestScopeCSSLeavesStringsAndCommentsAloneAndEmbedsURLs(t *testing.T) {
	root := t.TempDir()
	cssPath := filepath.Join(root, "styles", "component.css")
	if err := os.MkdirAll(filepath.Dir(cssPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "styles", "icon.svg"), []byte("<svg/>"), 0644); err != nil {
		t.Fatal(err)
	}
	source := `.x { /* url(fake.png) */ content: "url(fake.png) @keyframes spin"; background: url("../styles/icon.svg"); }`
	if err := os.WriteFile(cssPath, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}

	got, diagnostics := scopeCSS([]string{cssPath}, "data-a-assets")
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diagnostics)
	}
	if !strings.Contains(got, `content:"url(fake.png) @keyframes spin"`) {
		t.Fatalf("string contents were altered:\n%s", got)
	}
	if !strings.Contains(got, `/* url(fake.png) */`) {
		t.Fatalf("comment was not preserved:\n%s", got)
	}
	if !strings.Contains(got, `url("data:image/svg+xml;base64,`) {
		t.Fatalf("relative URL was not embedded:\n%s", got)
	}
	if strings.Contains(got, `url(fake.png)`) && !strings.Contains(got, `content:"url(fake.png)`) {
		t.Fatalf("URL-like string was rewritten:\n%s", got)
	}
}

func TestScopeCSSRenamesKeyframesAcrossOrderedStyles(t *testing.T) {
	first := cssFixture(t, "first.css", `.spinner { animation: spin 1s ease, "fade" 2s; animation-name: spin, fade; }`)
	second := cssFixture(t, "second.css", `@-webkit-keyframes spin { from { opacity: 0; } to { opacity: 1; } }
@keyframes "fade" { 0% { opacity: 0; } 100% { opacity: 1; } }`)

	got, diagnostics := scopeCSS([]string{first, second}, "data-a-kf")
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diagnostics)
	}
	for _, want := range []string{
		`animation:spin-data-a-kf 1s ease,"fade-data-a-kf" 2s`,
		`animation-name:spin-data-a-kf,fade-data-a-kf`,
		`@-webkit-keyframes spin-data-a-kf`,
		`@keyframes "fade-data-a-kf"`,
		`from{opacity:0;}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `from[data-a-kf]`) {
		t.Fatalf("keyframe selectors must not be component scoped:\n%s", got)
	}
}

func TestScopeCSSReportsUnsupportedConstructsAndSyntaxErrors(t *testing.T) {
	path := cssFixture(t, "bad.css", `@import "other.css";
.parent { & .child { color: red; } }
:host, :global(.x), :root { color: red; }
@keyframes ease { from { opacity: 0; } }
.animated { animation: ease 1s; }
.broken { color red; }`)

	_, diagnostics := scopeCSS([]string{path}, "data-a-errors")
	found := map[string]bool{}
	for _, diagnostic := range diagnostics {
		if diagnostic.File != path || diagnostic.Line == 0 || diagnostic.Column == 0 || diagnostic.Start < 0 || diagnostic.End < diagnostic.Start {
			t.Errorf("diagnostic has imprecise source location: %+v", diagnostic)
		}
		found[diagnostic.Code] = true
	}
	for _, code := range []string{"P0301", "P0302", "P0303", "P0304", "P0305"} {
		if !found[code] {
			t.Errorf("expected diagnostic %s, got %+v", code, diagnostics)
		}
	}
}

func TestScopeCSSPreservesNestedAtRulesAndReportsImportLocation(t *testing.T) {
	path := cssFixture(t, "media.css", `@media (min-width: 20rem) { .box > span { color: red; } }
@import "x.css";`)
	_, diagnostics := scopeCSS([]string{path}, "data-a-media")
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "P0302" {
			if diagnostic.Line != 2 {
				t.Fatalf("@import diagnostic should point to line 2: %+v", diagnostic)
			}
			return
		}
	}
	t.Fatalf("expected @import diagnostic: %+v", diagnostics)
}

func TestScopeCSSMappingsRetainOriginalRuleLocations(t *testing.T) {
	source := `.first { color: red; }
.second { color: blue; }`
	path := cssFixture(t, "mapped.css", source)
	_, mappings, sources, diagnostics := scopeCSSWithMappings([]string{path}, "data-a-map")
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diagnostics)
	}
	if len(mappings) < 4 {
		t.Fatalf("expected per-event CSS mappings, got %d", len(mappings))
	}
	lines := map[int]bool{}
	for _, mapping := range mappings {
		if mapping.File != path {
			t.Errorf("mapping source = %q, want %q", mapping.File, path)
		}
		lines[sourceMapPositionAt(sourceMapPositions(sources[path]), mapping.Source).line] = true
	}
	if !lines[0] || !lines[1] {
		t.Fatalf("CSS mappings did not retain both source lines: %v", lines)
	}
}
