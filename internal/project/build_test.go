package project

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

func TestBuilderRejectsStaleArtifactsBeforePublication(t *testing.T) {
	root := appFixture(t)
	builder := NewBuilder(root, "dist", BuildOptions{Mode: BuildDevelopment})
	defer builder.Close()
	requireBuilt(t, builder.Build(context.Background()))
	previous := readOutput(t, filepath.Join(root, "dist"), "src/main.js")
	prepared := Prepare(root)
	plan, ok := collectBuildEntries(&prepared)
	if !ok {
		t.Fatalf("collect entries: %+v", prepared.Diagnostics)
	}
	builder.disposeLocked()
	contextOut := filepath.Join(root, buildContextDir)
	options := applicationBuildOptions(root, contextOut, plan.entries, builder.options)
	options.Plugins = append(options.Plugins, api.Plugin{Name: "change-source-during-build", Setup: func(build api.PluginBuild) {
		build.OnEnd(func(*api.BuildResult) (api.OnEndResult, error) {
			err := os.WriteFile(filepath.Join(root, "src", "counter.css"), []byte(".counter { color: orange; }"), 0644)
			return api.OnEndResult{}, err
		})
	}})
	buildContext, err := api.Context(options)
	if err != nil {
		t.Fatal(err)
	}
	builder.context = buildContext
	builder.key = buildContextKey(root, contextOut, plan.entries, builder.options)
	result := builder.Build(context.Background())
	found := false
	for _, diagnostic := range result.Diagnostics {
		found = found || diagnostic.Code == "P0608"
	}
	if !found {
		t.Fatalf("changed inputs must reject stale publication: %+v", result.Diagnostics)
	}
	if current := readOutput(t, filepath.Join(root, "dist"), "src/main.js"); current != previous {
		t.Fatal("stale build replaced the previous output")
	}
	assertNoStaging(t, root)
}

func TestExpressionUTF16OffsetsPreservePropertyAndShorthandTokens(t *testing.T) {
	source := `user.noSuchProperty + { value }.value + "😀" + next`
	rewritten, offsets, _, err := rewriteExpressionWithUTF16Offsets(source, nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"noSuchProperty", "next"} {
		generated := utf16Length(rewritten[:strings.Index(rewritten, token)])
		original := utf16Length(source[:strings.Index(source, token)])
		for i := 0; i < len(token); i++ {
			if offsets[generated+i] != original+i {
				t.Fatalf("%s offset %d maps to %d, want %d", token, i, offsets[generated+i], original+i)
			}
		}
	}
	if len(offsets) != utf16Length(rewritten)+1 || offsets[len(offsets)-1] != utf16Length(source) {
		t.Fatalf("invalid UTF-16 offset map length or exclusive end: %v", offsets)
	}
}

func TestBuildOutputPreservesRepositoryAndPublicAssets(t *testing.T) {
	root := t.TempDir()
	for _, out := range []string{".git", ".idea", ".config/nested", "public", "public/images"} {
		result := Result{Root: root}
		if _, ok := resolveBuildOutput(&result, out); ok || !hasErrors(result.Diagnostics) {
			t.Fatalf("unsafe output %q was accepted", out)
		}
	}
}

func TestResolveBuildOutputRejectsEntryPublicAndSourceOverlaps(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "index.html", `<link rel="stylesheet" href="./assets/site.css"><script type="module" src="./src/main.ts"></script>`)
	writeFixture(t, root, "src/main.ts", "export const main = true;\n")
	writeFixture(t, root, "assets/site.css", "body { color: red; }\n")
	writeFixture(t, root, "public/image.svg", "<svg/>")

	for _, out := range []string{"index.html", "src", "src/main.ts", "assets", "assets/site.css", "public"} {
		if resolved, err := ResolveBuildOutput(root, out); err == nil {
			t.Errorf("unsafe output %q resolved to %q", out, resolved)
		}
	}
	resolved, err := ResolveBuildOutput(root, "dist")
	if err != nil || resolved != filepath.Join(root, "dist") {
		t.Fatalf("safe output path was rejected: %q, %v", resolved, err)
	}
}

func TestBuilderValidatesOutputBeforeReturningSourceErrors(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "index.html", `<script type="module" src="./src/main.ts"></script>`)
	writeFixture(t, root, "src/main.ts", `const broken: = ;`)
	builder := NewBuilder(root, "src", BuildOptions{})
	defer builder.Close()

	result := builder.Build(context.Background())
	if !hasErrors(result.Diagnostics) || result.Diagnostics[0].Code != "P0600" ||
		!strings.Contains(result.Diagnostics[0].Message, "entrypoint") {
		t.Fatalf("unsafe output must be diagnosed before source checking: %+v", result.Diagnostics)
	}
}

func TestBuildHTMLTokenizerAcceptsUnquotedAttributesAndPreservesOtherContent(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "src/main.ts", "console.log('entry');")
	writeFixture(t, root, "styles/site.css", "body { color: red; }")
	source := `<!-- <script type=module src=missing.ts> -->
<LINK data-note="a>b" REL="alternate stylesheet" HREF=./styles/site.css>
<script data-src="./src/main.ts" TYPE=module SRC=./src/main.ts>const text = "<link rel=stylesheet href=missing.css>";</script>`
	writeFixture(t, root, "index.html", source)
	result := Result{Root: root}
	plan, ok := collectBuildEntries(&result)
	if !ok || len(plan.entries) != 2 {
		t.Fatalf("valid HTML entries were not discovered: %+v, %+v", plan.entries, result.Diagnostics)
	}
	output := rootRelativeStyles(rewriteModuleScripts(source))
	if !strings.Contains(output, `href="/styles/site.css"`) || !strings.Contains(output, `src="/src/main.js"`) {
		t.Fatalf("unquoted entries were not rewritten: %s", output)
	}
	for _, unchanged := range []string{`data-note="a>b"`, `data-src="./src/main.ts"`,
		`<!-- <script type=module src=missing.ts> -->`, `const text = "<link rel=stylesheet href=missing.css>";`} {
		if !strings.Contains(output, unchanged) {
			t.Errorf("unrelated HTML content was modified: %s", unchanged)
		}
	}
	linked := attachCSSBundles(source, root, map[string]string{filepath.Join(root, "src", "main.ts"): "src/main.css"})
	if !strings.Contains(linked, `<link rel="stylesheet" href="/src/main.css">`) {
		t.Fatalf("unquoted module entry did not receive its CSS bundle: %s", linked)
	}
	if _, err := ResolveBuildOutput(root, "src"); err == nil {
		t.Fatal("unquoted module entrypoint was not protected from output overlap")
	}
}

func TestBuilderDependenciesSnapshotTracksResolvedInputsAndSurvivesFailures(t *testing.T) {
	root := appFixture(t)
	builder := NewBuilder(root, "dist", BuildOptions{Mode: BuildDevelopment})
	defer builder.Close()
	requireBuilt(t, builder.Build(context.Background()))
	dependencies := builder.Dependencies()
	for _, expected := range []string{
		filepath.Join(root, "src", "main.ts"),
		filepath.Join(root, "src", "lazy.ts"),
		filepath.Join(root, "node_modules", "@angulus", "core", "index.js"),
	} {
		found := false
		for _, dependency := range dependencies {
			if !filepath.IsAbs(dependency) {
				t.Errorf("dependency must be absolute: %s", dependency)
			}
			found = found || samePath(dependency, expected)
		}
		if !found {
			t.Errorf("resolved input missing from dependency snapshot: %s (%v)", expected, dependencies)
		}
	}
	original := strings.Join(dependencies, "\n")
	dependencies[0] = "caller mutation"
	if strings.Join(builder.Dependencies(), "\n") != original {
		t.Fatal("caller mutation changed builder dependency snapshot")
	}
	typeDependency := writeFixture(t, root, "node_modules/type-only/index.d.ts", "export interface RemoteType { value: number; }")
	writeFixture(t, root, "node_modules/type-only/package.json", `{"types":"index.d.ts"}`)
	writeFixture(t, root, "src/main.ts", "import type { RemoteType } from 'type-only'; const invalid: RemoteType = { value: 'wrong' };")
	if result := builder.Build(context.Background()); !hasErrors(result.Diagnostics) {
		t.Fatal("invalid source must fail checking")
	}
	afterFailure := builder.Dependencies()
	for _, expected := range append(strings.Split(original, "\n"), typeDependency) {
		found := false
		for _, dependency := range afterFailure {
			found = found || samePath(dependency, expected)
		}
		if !found {
			t.Errorf("failed check dependency snapshot omitted %s; recorded dependencies: %v", expected, afterFailure)
		}
	}
}

func appFixture(t *testing.T) string {
	t.Helper()
	tsgo := nativeTSGo(".")
	if tsgo == "" {
		t.Skip("native TSGo platform executable is unavailable")
	}
	t.Setenv("ANGULUS_TSGO", tsgo)
	root := t.TempDir()
	writeFixture(t, root, "src/counter.ts", "export class Counter { count = 1; }\n")
	writeFixture(t, root, "src/counter.html", "<div>\n  {{ count }}\n</div>\n")
	writeFixture(t, root, "src/counter.css", ".counter {\n  color: red;\n}\n")
	writeFixture(t, root, "src/counter.component.json",
		`{"selector":"app-counter","module":"./counter.ts","export":"Counter","template":"./counter.html","styles":["./counter.css"]}`)
	writeFixture(t, root, "src/main.ts", "import { Counter } from \"./counter.component.generated\";\n"+
		"export function describeCounter(): string { const counterDescription = String(new Counter()); return counterDescription + \"-v1\"; }\n"+
		"console.log(describeCounter());\n"+
		"document.addEventListener(\"click\", () => import(\"./lazy\").then(module => module.lazyRoute()));\n")
	writeFixture(t, root, "src/lazy-route.ts", "export class LazyRoute { value = \"lazy-route-marker\"; }\n")
	writeFixture(t, root, "src/lazy-route.html", `{{ value }}`)
	writeFixture(t, root, "src/lazy-route.css", `.lazy-route { color: purple; }`)
	writeFixture(t, root, "src/lazy-route.component.json",
		`{"selector":"app-lazy-route","module":"./lazy-route.ts","export":"LazyRoute","template":"./lazy-route.html","styles":["./lazy-route.css"]}`)
	writeFixture(t, root, "src/lazy.ts", "import { LazyRoute } from \"./lazy-route.component.generated\";\n"+
		"import { Counter } from \"./counter.component.generated\";\n"+
		"export function lazyRoute(): string { console.log(new LazyRoute().value, new Counter().count); return \"lazy-route-marker\"; }\n")
	writeFixture(t, root, "src/assets.d.ts", `declare module "*.css";`)
	writeFixture(t, root, "src/global.css", `body { color: #123; }`)
	writeFixture(t, root, "index.html", `<html><head><link rel="stylesheet" href="./src/global.css"></head><body><script type="module" src="./src/main.ts"></script></body></html>`)
	writeFixture(t, root, "node_modules/@angulus/core/package.json", `{"main":"index.js","types":"index.d.ts"}`)
	writeFixture(t, root, "node_modules/@angulus/core/index.js", `export function defineComponent(ctor, render, scopeId) { return ctor; } export function text() {} export function bind() {}`)
	writeFixture(t, root, "node_modules/@angulus/core/index.d.ts",
		`export interface Scope {} export type Render<T> = (instance: T, parent: Node, scope: Scope) => void; export declare function defineComponent<T>(ctor: new (...args: any[]) => T, render: Render<T>, scopeId?: string): new (...args: any[]) => T; export declare function text(...args: any[]): any; export declare function bind(...args: any[]): any;`)
	return root
}

func readOutput(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func requireBuilt(t *testing.T, result Result) {
	t.Helper()
	if hasErrors(result.Diagnostics) {
		t.Fatalf("build diagnostics: %+v", result.Diagnostics)
	}
}

func lazyChunk(t *testing.T, out string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(out, "chunks", "lazy-*.js"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("lazy route chunk was not split: %v %v", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func lazyStyleChunkPath(t *testing.T, out string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(out, "src", "lazy-route.component.styles.generated.css"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("lazy route stylesheet was not split: %v %v", matches, err)
	}
	return matches[0]
}

func sourceMapSources(t *testing.T, path string) []string {
	t.Helper()
	var sourceMap struct {
		Sources  []string `json:"sources"`
		Mappings string   `json:"mappings"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &sourceMap); err != nil {
		t.Fatalf("invalid source map %s: %v", path, err)
	}
	if sourceMap.Mappings == "" {
		t.Fatalf("source map %s has no mappings", path)
	}
	return sourceMap.Sources
}

func inlineSourceMapSources(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "sourceMappingURL=data:application/json;base64,"
	comment := string(data)
	start := strings.LastIndex(comment, marker)
	if start < 0 {
		t.Fatalf("generated source %s has no inline source map", path)
	}
	encoded := strings.Fields(comment[start+len(marker):])[0]
	encoded = strings.TrimSuffix(encoded, "*/")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode inline source map in %s: %v", path, err)
	}
	var sourceMap struct {
		Sources  []string `json:"sources"`
		Mappings string   `json:"mappings"`
	}
	if err := json.Unmarshal(decoded, &sourceMap); err != nil {
		t.Fatalf("invalid inline source map in %s: %v", path, err)
	}
	if sourceMap.Mappings == "" {
		t.Fatalf("inline source map in %s has no mappings", path)
	}
	return sourceMap.Sources
}

func sourceMapContainsSourcePosition(t *testing.T, path, sourceSuffix string, line, column int) bool {
	t.Helper()
	var sourceMap struct {
		Sources  []string `json:"sources"`
		Mappings string   `json:"mappings"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &sourceMap); err != nil {
		t.Fatalf("invalid source map %s: %v", path, err)
	}
	sourceIndex := -1
	for index, source := range sourceMap.Sources {
		if strings.HasSuffix(filepath.ToSlash(source), "/"+sourceSuffix) {
			sourceIndex = index
			break
		}
	}
	if sourceIndex < 0 {
		return false
	}
	previousSource, previousLine, previousColumn := 0, 0, 0
	for _, mappingLine := range strings.Split(sourceMap.Mappings, ";") {
		for _, segment := range strings.Split(mappingLine, ",") {
			if segment == "" {
				continue
			}
			values, err := decodeSourceMapSegment(segment)
			if err != nil {
				t.Fatalf("invalid mappings in %s: %v", path, err)
			}
			if len(values) < 4 {
				continue
			}
			previousSource += values[1]
			previousLine += values[2]
			previousColumn += values[3]
			if previousSource == sourceIndex && previousLine == line && previousColumn == column {
				return true
			}
		}
	}
	return false
}

func decodeSourceMapSegment(segment string) ([]int, error) {
	var values []int
	for index := 0; index < len(segment); {
		value, shift := 0, uint(0)
		for {
			if index >= len(segment) {
				return nil, fmt.Errorf("unterminated VLQ segment %q", segment)
			}
			digit := strings.IndexByte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/", segment[index])
			index++
			if digit < 0 {
				return nil, fmt.Errorf("invalid VLQ digit in %q", segment)
			}
			value |= (digit & 31) << shift
			shift += 5
			if digit&32 == 0 {
				break
			}
		}
		if value&1 != 0 {
			values = append(values, -(value >> 1))
		} else {
			values = append(values, value>>1)
		}
	}
	return values, nil
}

func runLazyRouteImport(t *testing.T, mainJS string) (before, after []string) {
	t.Helper()
	script := `
import { pathToFileURL } from "node:url";
const links = [];
const listeners = [];
const originalLog = console.log;
console.log = (...values) => {
  if (values[0] && values[0].startsWith("__ANGULUS_STYLE_STATE__")) originalLog(...values);
};
globalThis.document = {
  baseURI: "http://localhost/products/deep",
  head: { append(link) { links.push(link); } },
  querySelectorAll() { return links; },
  createElement() { return { rel: "", href: "" }; },
  addEventListener(_name, listener) { listeners.push(listener); }
};
await import(pathToFileURL(process.argv[1]).href);
const before = links.map(link => link.href);
await listeners[0]();
console.log("__ANGULUS_STYLE_STATE__" + JSON.stringify([before, links.map(link => link.href)]));
`
	output, err := exec.Command("node", "--input-type=module", "-e", script, mainJS).CombinedOutput()
	if err != nil {
		t.Fatalf("run built ESM entry: %v\n%s", err, output)
	}
	line := ""
	for _, candidate := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(candidate, "__ANGULUS_STYLE_STATE__") {
			line = strings.TrimPrefix(candidate, "__ANGULUS_STYLE_STATE__")
		}
	}
	if line == "" {
		t.Fatalf("built entry did not report style loading state: %s", output)
	}
	var state [][]string
	if err := json.Unmarshal([]byte(line), &state); err != nil || len(state) != 2 {
		t.Fatalf("invalid style loading state %q: %v", line, err)
	}
	return state[0], state[1]
}

func assertNoStaging(t *testing.T, root string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(root, ".angulus", "build-staging-*"))
	if len(matches) != 0 {
		t.Fatalf("staging directories leaked: %v", matches)
	}
	if _, err := os.Stat(filepath.Join(root, buildContextDir)); !os.IsNotExist(err) {
		t.Fatalf("esbuild context outdir must never be written: err=%v", err)
	}
}

func TestBuilderRebuildsIncrementallyAndRecreatesContextOnEntryChanges(t *testing.T) {
	root := appFixture(t)
	builder := NewBuilder(root, filepath.Join(".angulus", "dev"), BuildOptions{Mode: BuildDevelopment})
	defer builder.Close()
	out := filepath.Join(root, ".angulus", "dev")

	requireBuilt(t, builder.Build(context.Background()))
	if stats := builder.Stats(); stats != (BuilderStats{Contexts: 1}) {
		t.Fatalf("first build stats = %+v", stats)
	}
	mainJS := readOutput(t, out, "src/main.js")
	if !strings.Contains(mainJS, "counterDescription") || !strings.Contains(mainJS, "-v1") ||
		!strings.Contains(mainJS, "//# sourceMappingURL=main.js.map") {
		t.Fatalf("development output must be readable with linked source maps:\n%s", mainJS)
	}
	if _, err := os.Stat(filepath.Join(out, "src", "main.js.map")); err != nil {
		t.Fatal(err)
	}
	jsMaps := []string{filepath.Join(out, "src", "main.js.map")}
	chunkMaps, err := filepath.Glob(filepath.Join(out, "chunks", "*.js.map"))
	if err != nil {
		t.Fatal(err)
	}
	jsMaps = append(jsMaps, chunkMaps...)
	var jsSources []string
	for _, path := range jsMaps {
		jsSources = append(jsSources, sourceMapSources(t, path)...)
	}
	for _, source := range []string{"counter.ts", "counter.html"} {
		found := false
		for _, mapped := range jsSources {
			found = found || strings.HasSuffix(filepath.ToSlash(mapped), "/"+source)
		}
		if !found {
			t.Errorf("final JavaScript map does not point to original %s: %v", source, jsSources)
		}
	}
	for generated, source := range map[string]string{
		filepath.Join(root, "src", "counter.component.generated.ts"):        "counter.ts",
		filepath.Join(root, "src", "counter.component.render.generated.ts"): "counter.html",
	} {
		found := false
		for _, mapped := range inlineSourceMapSources(t, generated) {
			found = found || filepath.Base(filepath.FromSlash(mapped)) == source
		}
		if !found {
			t.Errorf("generated source map for %s does not target %s", generated, source)
		}
	}
	expressionMapped := false
	for _, path := range jsMaps {
		expressionMapped = expressionMapped || sourceMapContainsSourcePosition(t, path, "counter.html", 1, 5)
	}
	if !expressionMapped {
		t.Error("final JavaScript map does not retain the rewritten template identifier location")
	}
	if cssSources := sourceMapSources(t, filepath.Join(out, "src", "counter.component.styles.generated.css.map")); !strings.Contains(strings.Join(cssSources, "\n"), "counter.css") {
		t.Errorf("final CSS map does not point to the original stylesheet: %v", cssSources)
	}
	if !sourceMapContainsSourcePosition(t, filepath.Join(out, "src", "counter.component.styles.generated.css.map"), "counter.css", 0, 0) {
		t.Error("final CSS map does not retain original stylesheet rule locations")
	}
	if !strings.Contains(lazyChunk(t, out), "lazy-route-marker") {
		t.Fatal("lazy route chunk lost its module")
	}
	lazyCSSPath := lazyStyleChunkPath(t, out)
	lazyCSS, err := os.ReadFile(lazyCSSPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lazyCSS), "purple") {
		t.Fatal("lazy route CSS output lost the component styles")
	}
	if eagerCSS := readOutput(t, out, "src/counter.component.styles.generated.css"); strings.Contains(eagerCSS, "lazy-route") || strings.Contains(eagerCSS, "purple") {
		t.Fatalf("eager component stylesheet contains lazy route rules: %s", eagerCSS)
	}
	if _, err := os.Stat(filepath.Join(out, "src", "main.css")); !os.IsNotExist(err) {
		t.Fatalf("component CSS must not aggregate into eager main.css: %v", err)
	}
	if cssSources := sourceMapSources(t, lazyCSSPath+".map"); !strings.Contains(strings.Join(cssSources, "\n"), "lazy-route.css") {
		t.Errorf("lazy CSS map does not point to its original stylesheet: %v", cssSources)
	}
	beforeStyles, afterStyles := runLazyRouteImport(t, filepath.Join(out, "src", "main.js"))
	if len(beforeStyles) != 1 || !strings.HasSuffix(beforeStyles[0], "/src/counter.component.styles.generated.css") {
		t.Fatalf("initial ESM import should load only its reachable component CSS: %v", beforeStyles)
	}
	if len(afterStyles) != 2 || !strings.HasSuffix(afterStyles[1], "/src/lazy-route.component.styles.generated.css") {
		t.Fatalf("lazy route CSS should load only after its dynamic import: before=%v after=%v", beforeStyles, afterStyles)
	}
	beforeCSSUpdate := readOutput(t, out, "src/main.js")
	beforeLazyJS := lazyChunk(t, out)
	writeFixture(t, root, "src/lazy-route.css", ".lazy-route { color: orange; }")
	requireBuilt(t, builder.Build(context.Background()))
	if readOutput(t, out, "src/main.js") != beforeCSSUpdate || lazyChunk(t, out) != beforeLazyJS {
		t.Fatal("CSS-only edit changed the JavaScript graph and would lose runtime state")
	}
	if !strings.Contains(readOutput(t, out, "src/lazy-route.component.styles.generated.css"), "orange") {
		t.Fatal("lazy CSS update was not published under its stable URL")
	}

	// TS edit with an unchanged entry graph reuses the context.
	writeFixture(t, root, "src/main.ts", strings.Replace(readOutput(t, root, "src/main.ts"), "-v1", "-v2", 1))
	requireBuilt(t, builder.Build(context.Background()))
	if stats := builder.Stats(); stats != (BuilderStats{Contexts: 1, Rebuilds: 2}) {
		t.Fatalf("unchanged entry graph must rebuild: %+v", stats)
	}
	if mainJS := readOutput(t, out, "src/main.js"); !strings.Contains(mainJS, "-v2") || strings.Contains(mainJS, "-v1") {
		t.Fatalf("rebuild did not pick up TypeScript edit:\n%s", mainJS)
	}

	// Template and component CSS edits flow through regenerated descriptor outputs.
	writeFixture(t, root, "src/counter.html", `<b>{{ count }}</b>`)
	writeFixture(t, root, "src/counter.css", `.counter { color: blue; }`)
	requireBuilt(t, builder.Build(context.Background()))
	if stats := builder.Stats(); stats != (BuilderStats{Contexts: 1, Rebuilds: 3}) {
		t.Fatalf("template/CSS edit must rebuild: %+v", stats)
	}
	if css := readOutput(t, out, "src/counter.component.styles.generated.css"); !strings.Contains(css, "blue") {
		t.Fatalf("scoped CSS update was not published:\n%s", css)
	}

	// An unreferenced descriptor must not put its component styles in the app bundle.
	writeFixture(t, root, "src/badge.ts", "export class Badge { label = \"b\"; }\n")
	writeFixture(t, root, "src/badge.html", `{{ label }}`)
	writeFixture(t, root, "src/badge.css", `.badge { color: green; }`)
	writeFixture(t, root, "src/badge.component.json",
		`{"selector":"app-badge","module":"./badge.ts","export":"Badge","template":"./badge.html","styles":["./badge.css"]}`)
	requireBuilt(t, builder.Build(context.Background()))
	if stats := builder.Stats(); stats != (BuilderStats{Contexts: 1, Rebuilds: 4}) {
		t.Fatalf("unreachable component CSS must not change the entry graph: %+v", stats)
	}
	html := readOutput(t, out, "index.html")
	if strings.Contains(html, "badge") || strings.Contains(html, "component.styles") || !strings.Contains(html, `src="/src/main.js"`) ||
		!strings.Contains(html, `href="/src/global.css"`) {
		t.Fatalf("index.html must link reachable bundles only:\n%s", html)
	}
	if _, err := os.Stat(filepath.Join(out, "src", "badge.component.styles.generated.css")); !os.IsNotExist(err) {
		t.Fatalf("unreachable component CSS was emitted: %v", err)
	}

	// A new module-script entrypoint still recreates the esbuild context.
	writeFixture(t, root, "src/secondary.ts", "export const secondary = 1;\n")
	index := strings.Replace(readOutput(t, root, "index.html"), "</body>",
		`<script type="module" src="./src/secondary.ts"></script></body>`, 1)
	writeFixture(t, root, "index.html", index)
	requireBuilt(t, builder.Build(context.Background()))
	if stats := builder.Stats(); stats != (BuilderStats{Contexts: 2, Rebuilds: 4}) {
		t.Fatalf("entry graph change must recreate the context: %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(out, "src", "secondary.js")); err != nil {
		t.Fatalf("new module-script entry was not emitted: %v", err)
	}
	assertNoStaging(t, root)
}

func TestBuilderFailedRebuildKeepsPreviousOutputAndRecovers(t *testing.T) {
	root := appFixture(t)
	builder := NewBuilder(root, "dist", BuildOptions{Mode: BuildDevelopment})
	defer builder.Close()
	out := filepath.Join(root, "dist")
	requireBuilt(t, builder.Build(context.Background()))
	before := readOutput(t, out, "src/main.js")

	// esbuild-only failure: TSGo accepts declared modules but the bundler cannot resolve them.
	writeFixture(t, root, "src/missing.d.ts", `declare module "virtual-missing" { export const value: string; }`)
	original := readOutput(t, root, "src/main.ts")
	writeFixture(t, root, "src/main.ts", "import { value } from \"virtual-missing\";\nconsole.log(value);\n"+original)
	result := builder.Build(context.Background())
	if !hasErrors(result.Diagnostics) {
		t.Fatal("unresolved bundle import must fail")
	}
	found := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == "P0605" && strings.Contains(diagnostic.Message, "virtual-missing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("esbuild error was not reported: %+v", result.Diagnostics)
	}
	if after := readOutput(t, out, "src/main.js"); after != before {
		t.Fatal("failed rebuild replaced the published output")
	}
	if stats := builder.Stats(); stats.Contexts != 1 || stats.Rebuilds != 1 {
		t.Fatalf("failed build must reuse the context: %+v", stats)
	}

	writeFixture(t, root, "src/main.ts", strings.Replace(original, "-v1", "-fixed", 1))
	requireBuilt(t, builder.Build(context.Background()))
	if !strings.Contains(readOutput(t, out, "src/main.js"), "-fixed") {
		t.Fatal("rebuild after error did not publish fixed output")
	}
	assertNoStaging(t, root)
}

func TestBuildProductionMinifiesAndPreservesLazyChunks(t *testing.T) {
	root := appFixture(t)
	writeFixture(t, root, "index.html", `<html><head><link rel=stylesheet href=./src/global.css></head><body><script type=module src=./src/main.ts></script></body></html>`)
	requireBuilt(t, Build(context.Background(), root, "dist"))
	out := filepath.Join(root, "dist")
	mainJS := readOutput(t, out, "src/main.js")
	if strings.Contains(mainJS, "counterDescription") || strings.Contains(mainJS, "sourceMappingURL") || !strings.Contains(mainJS, "-v1") {
		t.Fatalf("production output must be minified without linked source maps:\n%s", mainJS)
	}
	if strings.Count(strings.TrimSpace(mainJS), "\n") > 2 {
		t.Fatalf("production output was not whitespace-minified:\n%s", mainJS)
	}
	if strings.Contains(mainJS, "lazy-route-marker") || !regexp.MustCompile(`import\("\.\./chunks/lazy-[^"]+\.js"\)`).MatchString(mainJS) {
		t.Fatalf("lazy route must stay a dynamic import of a split chunk:\n%s", mainJS)
	}
	if !strings.Contains(lazyChunk(t, out), "lazy-route-marker") {
		t.Fatal("lazy route chunk lost its module")
	}
	html := readOutput(t, out, "index.html")
	if !strings.Contains(html, `src="/src/main.js"`) || strings.Contains(html, "component.styles") || strings.Contains(html, "lazy-") {
		t.Fatalf("index.html must keep root-absolute URLs for SPA deep routes:\n%s", html)
	}
	assertNoStaging(t, root)
}

func TestBuilderCancellationAndCloseDoNotPublish(t *testing.T) {
	root := appFixture(t)
	builder := NewBuilder(root, "dist", BuildOptions{})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result := builder.Build(canceled)
	if !hasErrors(result.Diagnostics) {
		t.Fatal("canceled build must fail")
	}
	if _, err := os.Stat(filepath.Join(root, "dist")); !os.IsNotExist(err) {
		t.Fatalf("canceled build published output: %v", err)
	}
	requireBuilt(t, builder.Build(context.Background()))
	if err := builder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := builder.Close(); err != nil {
		t.Fatal(err)
	}
	result = builder.Build(context.Background())
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "P0606" {
		t.Fatalf("closed builder must refuse to build: %+v", result.Diagnostics)
	}
	assertNoStaging(t, root)
}

func TestBuildRejectsContextOutputDirectory(t *testing.T) {
	root := appFixture(t)
	result := Build(context.Background(), root, filepath.Join(".angulus", "build-context", "x"))
	if !hasErrors(result.Diagnostics) || result.Diagnostics[len(result.Diagnostics)-1].Code != "P0600" {
		t.Fatalf("build-context output must be rejected: %+v", result.Diagnostics)
	}
}

func TestBuilderBundleHonorsContextCancellation(t *testing.T) {
	root := appFixture(t)
	builder := NewBuilder(root, "dist", BuildOptions{Mode: BuildDevelopment})
	defer builder.Close()
	result := Check(context.Background(), root)
	requireBuilt(t, result)
	plan, ok := collectBuildEntries(&result)
	if !ok {
		t.Fatalf("entries: %+v", result.Diagnostics)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	builder.mu.Lock()
	_, ok = builder.bundleLocked(canceled, &result, plan.entries, filepath.Join(result.Root, buildContextDir))
	builder.mu.Unlock()
	if ok || result.Diagnostics[len(result.Diagnostics)-1].Code != "P0607" {
		t.Fatalf("canceled bundle must report P0607: %+v", result.Diagnostics)
	}
	requireBuilt(t, builder.Build(context.Background()))
	if stats := builder.Stats(); stats != (BuilderStats{Contexts: 1, Rebuilds: 1}) {
		t.Fatalf("context must stay reusable after cancellation: %+v", stats)
	}
}
