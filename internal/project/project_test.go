package project

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPrepareGeneratesFacadeWithoutRewritingComponent(t *testing.T) {
	root := t.TempDir()
	module := "export class Counter { count = 1; increment() { this.count++; } }\n"
	modulePath := writeFixture(t, root, "src/counter.ts", module)
	writeFixture(t, root, "src/counter.html", `<button (click)="increment()">Count {{ count }}</button>`)
	writeFixture(t, root, "src/counter.css", `.counter > button:hover { color: red; }`)
	descriptor := `{"$schema":"./component.schema.json","selector":"app-counter","module":"./counter.ts","export":"Counter","template":"./counter.html","styles":["./counter.css"]}`
	descriptorPath := writeFixture(t, root, "src/counter.component.json", descriptor)

	result := Prepare(root)
	if hasErrors(result.Diagnostics) {
		t.Fatalf("Prepare diagnostics: %+v", result.Diagnostics)
	}
	gotModule, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotModule) != module {
		t.Fatal("component source was changed")
	}
	facade, err := os.ReadFile(strings.TrimSuffix(descriptorPath, ".json") + ".generated.ts")
	if err != nil {
		t.Fatal(err)
	}
	render, err := os.ReadFile(strings.TrimSuffix(descriptorPath, ".json") + ".render.generated.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(facade), "__defineComponent(__angulusCtor, __angulusRender,") ||
		!strings.Contains(string(facade), "export { __angulusCtor as Counter }") {
		t.Fatalf("facade does not register and re-export the original constructor:\n%s", facade)
	}
	if !strings.Contains(string(render), "ctx.increment()") || !strings.Contains(string(render), "ctx.count") {
		t.Fatalf("render expressions were not qualified:\n%s", render)
	}
	if !strings.Contains(string(render), "import * as __f from \"@angulus/core\"") {
		t.Fatalf("render does not import runtime helpers:\n%s", render)
	}
	scopedCSS, err := os.ReadFile(strings.TrimSuffix(descriptorPath, ".json") + ".styles.generated.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(scopedCSS), generatedCSSMarker) || !strings.Contains(string(scopedCSS), `.counter[data-a-`) {
		t.Fatalf("scoped stylesheet was not generated or marked as compiler-owned:\n%s", scopedCSS)
	}
}

func TestDiscoverReportsUnknownFieldsAndPathEscapes(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "bad.component.json",
		`{"selector":"app-bad","module":"./bad.ts","export":"Bad","template":"./bad.html","unknown":true}`)
	writeFixture(t, root, "escape.component.json",
		`{"selector":"app-escape","module":"../outside.ts","export":"Escape","template":"./missing.html"}`)
	result := Discover(root)
	if len(result.Diagnostics) < 2 {
		t.Fatalf("expected detailed descriptor diagnostics: %+v", result.Diagnostics)
	}
	if result.Diagnostics[0].File != path {
		t.Fatalf("diagnostic does not identify descriptor: %+v", result.Diagnostics)
	}
}

func TestDiscoverRejectsNullDescriptorArrays(t *testing.T) {
	for _, field := range []string{"styles", "imports", "customElements"} {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			source := `{"selector":"app-widget","module":"./widget.ts","export":"Widget","template":"./widget.html","` + field + `":null}`
			path := writeFixture(t, root, "widget.component.json", source)
			result := Discover(root)
			if len(result.Diagnostics) != 1 || result.Diagnostics[0].File != path ||
				result.Diagnostics[0].Start != strings.Index(source, `"`+field+`"`) ||
				!strings.Contains(result.Diagnostics[0].Message, "must be an array") {
				t.Fatalf("null field silently accepted or mislocated: %+v", result.Diagnostics)
			}
		})
	}
}

func TestDiscoverRejectsDuplicateDescriptorFieldsAndSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	duplicate := writeFixture(t, root, "duplicate.component.json",
		`{"selector":"app-first","selector":"app-second","module":"./x.ts","export":"X","template":"./x.html"}`)
	outside := t.TempDir()
	writeFixture(t, outside, "outside.ts", "export class Outside {}\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		if os.IsPermission(err) || strings.Contains(strings.ToLower(err.Error()), "privilege") {
			t.Skip("Directory symlinks are unavailable")
		}
		t.Fatal(err)
	}
	writeFixture(t, root, "linked.component.json",
		`{"selector":"app-linked","module":"./linked/outside.ts","export":"Outside","template":"./missing.html"}`)

	result := Discover(root)
	var duplicateFound, symlinkFound bool
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.File == duplicate && strings.Contains(diagnostic.Message, "duplicate JSON field") {
			duplicateFound = true
		}
		if diagnostic.File != "" && strings.Contains(diagnostic.Message, "symlink outside") {
			symlinkFound = true
		}
	}
	if !duplicateFound || !symlinkFound {
		t.Fatalf("duplicate keys and symlink escapes must be diagnosed: %+v", result.Diagnostics)
	}
}

func TestDescriptorImportsGenerateComponentDependencies(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "button/button.ts", "export class Button {}\n")
	writeFixture(t, root, "button/button.html", `<button>Button</button>`)
	writeFixture(t, root, "button/button.component.json",
		`{"selector":"app-button","module":"./button.ts","export":"Button","template":"./button.html"}`)
	writeFixture(t, root, "card/card.ts", "export class Card {}\n")
	writeFixture(t, root, "card/card.html", `<app-button></app-button>`)
	writeFixture(t, root, "card/card.component.json",
		`{"selector":"app-card","module":"./card.ts","export":"Card","template":"./card.html","imports":["../button/button.component.json"]}`)

	result := Prepare(root)
	if hasErrors(result.Diagnostics) {
		t.Fatalf("Prepare diagnostics: %+v", result.Diagnostics)
	}
	render, err := os.ReadFile(filepath.Join(root, "card", "card.component.render.generated.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(render), `import {Button as __angulusComponent0} from "../button/button.component.generated";`) ||
		!strings.Contains(string(render), "__f.mountChild") {
		t.Fatalf("component dependency was not generated:\n%s", render)
	}
}

func TestDescriptorSupportsMixedSourceAndLibraryImports(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "node_modules/@test/widgets/package.json",
		`{"name":"@test/widgets","angulusComponents":"./angulus.components.json"}`)
	writeFixture(t, root, "node_modules/@test/widgets/angulus.components.json",
		`{"version":1,"components":[{"selector":"ui-widget","module":"./components/ui-widget.js","export":"Widget"}]}`)
	writeFixture(t, root, "local/button.ts", "export class Button {}\n")
	writeFixture(t, root, "local/button.html", `<button>Button</button>`)
	writeFixture(t, root, "local/button.component.json",
		`{"selector":"app-button","module":"./button.ts","export":"Button","template":"./button.html"}`)
	writeFixture(t, root, "card/card.ts", "export class Card {}\n")
	writeFixture(t, root, "card/card.html", `<app-button></app-button><ui-widget></ui-widget>`)
	writeFixture(t, root, "card/card.component.json",
		`{"selector":"app-card","module":"./card.ts","export":"Card","template":"./card.html","imports":[{"module":"@test/widgets/components/ui-widget.js","export":"Widget"},"../local/button.component.json"]}`)

	result := Prepare(root)
	if hasErrors(result.Diagnostics) {
		t.Fatalf("Prepare diagnostics: %+v", result.Diagnostics)
	}
	if len(result.Components) != 2 {
		t.Fatalf("discovered %d source components, want 2", len(result.Components))
	}
	card := result.Components[0]
	if card.Descriptor.Selector != "app-card" || len(card.Imports) != 2 ||
		card.Imports[0].Descriptor.Selector != "ui-widget" ||
		card.Imports[0].Descriptor.Export != "Widget" ||
		card.Imports[0].Module != "@test/widgets/components/ui-widget.js" ||
		card.Imports[0].ImportModule != "@test/widgets/components/ui-widget.js" ||
		!card.Imports[0].IsLibrary ||
		card.Imports[1].Descriptor.Selector != "app-button" {
		t.Fatalf("mixed descriptor imports lost ordering or resolved metadata: %+v", card.Imports)
	}
	render, err := os.ReadFile(filepath.Join(root, "card", "card.component.render.generated.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(render), `from "@test/widgets/components/ui-widget.js"`) ||
		!strings.Contains(string(render), `from "../local/button.component.generated"`) {
		t.Fatalf("generated imports did not preserve package specifiers and relative source imports:\n%s", render)
	}
	check, err := os.ReadFile(filepath.Join(root, "card", "card.component.template-check.generated.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(check), `from "@test/widgets/components/ui-widget.js"`) {
		t.Fatalf("template checker rewrote a package module specifier:\n%s", check)
	}
}

func TestDiscoverRejectsTwoDescriptorsForOneSymlinkedConstructor(t *testing.T) {
	root := t.TempDir()
	module := writeFixture(t, root, "widget.ts", "export class Widget {}\n")
	writeFixture(t, root, "widget.html", "widget")
	if err := os.Symlink(module, filepath.Join(root, "alias.ts")); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	writeFixture(t, root, "first.component.json",
		`{"selector":"app-first","module":"./widget.ts","export":"Widget","template":"./widget.html"}`)
	writeFixture(t, root, "second.component.json",
		`{"selector":"app-second","module":"./alias.ts","export":"Widget","template":"./widget.html"}`)
	result := Discover(root)
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == "P0107" {
			return
		}
	}
	t.Fatalf("same constructor was described twice through a symlink: %+v", result.Diagnostics)
}

func TestDiscoverResolvesDescriptorPathsWithHostFilesystemCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows case-insensitive path resolution")
	}
	root := t.TempDir()
	writeFixture(t, root, "Child.ts", "export class Child {}\n")
	writeFixture(t, root, "Child.html", "child")
	writeFixture(t, root, "Child.component.json",
		`{"selector":"app-child","module":"./Child.ts","export":"Child","template":"./Child.html"}`)
	writeFixture(t, root, "parent.ts", "export class Parent {}\n")
	writeFixture(t, root, "parent.html", "<app-child />")
	writeFixture(t, root, "parent.component.json",
		`{"selector":"app-parent","module":"./parent.ts","export":"Parent","template":"./parent.html","imports":["./child.component.json"]}`)
	result := Discover(root)
	if hasErrors(result.Diagnostics) {
		t.Fatalf("valid host-filesystem component import failed: %+v", result.Diagnostics)
	}
}

func TestPrepareRefusesUserOwnedGeneratedFile(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "widget.ts", "export class Widget {}\n")
	writeFixture(t, root, "widget.html", `<div>Widget</div>`)
	writeFixture(t, root, "widget.component.json",
		`{"selector":"app-widget","module":"./widget.ts","export":"Widget","template":"./widget.html"}`)
	result := Prepare(root)
	if hasErrors(result.Diagnostics) {
		t.Fatalf("initial Prepare diagnostics: %+v", result.Diagnostics)
	}
	facadePath := filepath.Join(root, "widget.component.generated.ts")
	if err := os.WriteFile(facadePath, []byte("user-owned"), 0644); err != nil {
		t.Fatal(err)
	}
	result = Prepare(root)
	if !hasErrors(result.Diagnostics) || !strings.Contains(result.Diagnostics[0].Message, "Refusing to overwrite") {
		t.Fatalf("user-owned generated-name collision was not diagnosed: %+v", result.Diagnostics)
	}
	content, err := os.ReadFile(facadePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "user-owned" {
		t.Fatal("user-owned file was overwritten")
	}
}

func TestExpressionQualificationAvoidsPropertiesAndLocals(t *testing.T) {
	got, err := qualifyExpression(`user?.name + {label: title, count: $index}.count + (ready ? yes : no)`, map[string]bool{"$index": false})
	if err != "" {
		t.Fatal(err)
	}
	want := `ctx.user?.name + {label: ctx.title, count: $index}.count + (ctx.ready ? ctx.yes : ctx.no)`
	if got != want {
		t.Fatalf("qualified expression = %q, want %q", got, want)
	}
	if _, err := qualifyExpression("`value ${name}`", nil); err == "" {
		t.Fatal("template-literal interpolation should be diagnosed")
	}
	got, err = qualifyExpression(`title + values[0].name`, nil)
	if err != "" {
		t.Fatal(err)
	}
	want = `ctx.title + ctx.values[0].name`
	if got != want {
		t.Fatalf("qualified expression = %q, want %q", got, want)
	}
	got, err = qualifyExpression(`item.label + $index + { item, position: $index }`, map[string]bool{"item": true, "$index": true})
	if err != "" {
		t.Fatal(err)
	}
	want = `item().label + $index() + { item: item(), position: $index() }`
	if got != want {
		t.Fatalf("signal locals = %q, want %q", got, want)
	}
}

func TestBuildUsesESMAndPublishesModuleScriptEntry(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "src/counter.ts", "export class Counter { count = 1; }\n")
	writeFixture(t, root, "src/counter.html", `{{ count }}`)
	writeFixture(t, root, "src/counter.css", `.counter { color: red; }`)
	writeFixture(t, root, "src/counter.component.json",
		`{"selector":"app-counter","module":"./counter.ts","export":"Counter","template":"./counter.html","styles":["./counter.css"]}`)
	writeFixture(t, root, "src/main.ts", `import "./main.css"; import { Counter } from "./counter.component.generated"; console.log(new Counter());`)
	writeFixture(t, root, "src/main.css", `main { color: red; }`)
	writeFixture(t, root, "src/assets.d.ts", `declare module "*.css";`)
	writeFixture(t, root, "src/global.css", `body { color: #123; }`)
	writeFixture(t, root, "index.html", `<html><head><link rel="stylesheet" href="./src/global.css"></head><body><script type="module" src="./src/main.ts"></script></body></html>`)
	writeFixture(t, root, "node_modules/@angulus/core/package.json", `{"main":"index.js","types":"index.d.ts"}`)
	writeFixture(t, root, "node_modules/@angulus/core/index.js", `export function defineComponent(ctor, render, scopeId) { return ctor; } export function text() {} export function bind() {}`)
	writeFixture(t, root, "node_modules/@angulus/core/index.d.ts",
		`export interface Scope {} export type Render<T> = (instance: T, parent: Node, scope: Scope) => void; export declare function defineComponent<T>(ctor: new (...args: any[]) => T, render: Render<T>, scopeId?: string): new (...args: any[]) => T; export declare function text(...args: any[]): any; export declare function bind(...args: any[]): any;`)

	tsgo := nativeTSGo(".")
	if tsgo == "" {
		t.Skip("native TSGo platform executable is unavailable")
	}

	t.Setenv("ANGULUS_TSGO", tsgo)
	result := Build(context.Background(), root, "dist")
	if hasErrors(result.Diagnostics) {
		t.Fatalf("Build diagnostics: %+v", result.Diagnostics)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "index.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "src", "main.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "src", "global.css")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "src", "main.css")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "src", "counter.component.styles.generated.css")); err != nil {
		t.Fatalf("reachable component CSS should be emitted independently: %v", err)
	}
	mainCSS, err := os.ReadFile(filepath.Join(root, "dist", "src", "counter.component.styles.generated.css"))
	if err != nil || !strings.Contains(string(mainCSS), "color:red") {
		t.Fatalf("component CSS missing from reachable stylesheet: %v %s", err, mainCSS)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "src", "counter.component.generated.js")); !os.IsNotExist(err) {
		t.Fatalf("unreachable facade was emitted as an independent entrypoint: err=%v", err)
	}
	html, err := os.ReadFile(filepath.Join(root, "dist", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "/src/main.js") || !strings.Contains(string(html), `href="/src/main.css"`) ||
		strings.Contains(string(html), "counter.component.styles.generated.css") {
		t.Fatalf("module scripts or CSS bundles not linked in output HTML: %s", html)
	}
}

func TestServerUsesNavigationOnlySPAFallback(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "index.html", "<main>app</main>")
	writeFixture(t, root, "assets/app.js", "export {};")
	server := NewServer(root, false)
	request := func(path, accept string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", accept)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}

	if response := request("/products/1", "text/html"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "app") {
		t.Fatalf("SPA route response: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("/missing.js", "text/html"); response.Code != http.StatusNotFound {
		t.Fatalf("missing JavaScript asset fell through to SPA: %d", response.Code)
	}
	if response := request("/api/users", "text/html"); response.Code != http.StatusNotFound {
		t.Fatalf("API route fell through to SPA: %d", response.Code)
	}
}

func TestDevSPAFallbackIncludesLiveReload(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "index.html", "<body><main>app</main></body>")
	req := httptest.NewRequest(http.MethodGet, "/products/1", nil)
	req.Header.Set("Accept", "text/html")
	response := httptest.NewRecorder()
	NewServer(root, true).Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "/__angulus/events") {
		t.Fatalf("deep-route navigation lost development client: %d %s", response.Code, response.Body.String())
	}
}
