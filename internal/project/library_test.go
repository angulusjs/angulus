package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildLibraryEmitsESMDeclarationsStylesAndVersionedMetadata(t *testing.T) {
	tsgo := nativeTSGo(".")
	if tsgo == "" {
		t.Skip("native TSGo platform executable is unavailable")
	}
	t.Setenv("ANGULUS_TSGO", tsgo)

	root := t.TempDir()
	writeFixture(t, root, "package.json", `{
		"name":"@test/widgets",
		"version":"1.2.3",
		"peerDependencies":{"@angulus/core":"^0.3.0"}
	}`)
	writeFixture(t, root, "tsconfig.json", `{
		"compilerOptions":{
			"target":"ES2022",
			"module":"ESNext",
			"moduleResolution":"Bundler",
			"strict":true,
			"noEmit":true,
			"skipLibCheck":true
		},
		"include":["src/**/*.ts"]
	}`)
	writeFixture(t, root, "node_modules/@angulus/core/package.json", `{"types":"index.d.ts","module":"index.js"}`)
	writeFixture(t, root, "node_modules/@angulus/core/index.d.ts", `
		export interface Scope {}
		export type Render<T> = (instance:T,parent:Node,scope:Scope)=>void;
		export declare function defineComponent<T>(ctor:new (...args:any[])=>T,render:Render<T>,scopeId?:string):new (...args:any[])=>T;
		export declare function element(...args:any[]):any;
		export declare function text(...args:any[]):any;
		export declare function bind(...args:any[]):any;
	`)
	writeFixture(t, root, "node_modules/@angulus/core/index.js", `export function defineComponent(ctor) { return ctor; }`)
	writeFixture(t, root, "src/widget.ts", `export class Widget { label = "widget"; }`)
	writeFixture(t, root, "src/widget.html", `<button>{{ label }}</button>`)
	writeFixture(t, root, "src/widget.css", `button { color: red; }`)
	writeFixture(t, root, "src/widget.component.json",
		`{"selector":"ui-widget","module":"./widget.ts","export":"Widget","template":"./widget.html","styles":["./widget.css"]}`)
	writeFixture(t, root, "src/index.ts", `
		export { Widget, Widget as RenamedWidget } from "./widget.component.generated";
	`)

	result := BuildLibrary(context.Background(), root, "dist")
	if result.hasErrors() {
		t.Fatalf("BuildLibrary diagnostics: %+v", result.Diagnostics)
	}
	for _, path := range []string{
		"dist/index.js",
		"dist/components/ui-widget.js",
		"dist/types/src/index.d.ts",
		"dist/types/src/widget.component.generated.d.ts",
		"dist/style.css",
		"dist/style.d.ts",
		"dist/angulus.components.json",
		"dist/package.json",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Fatalf("missing library output %s: %v", path, err)
		}
	}
	metadata, err := ReadLibraryMetadata(filepath.Join(root, "dist", "angulus.components.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantMetadata := []LibraryComponentMetadata{
		{Selector: "ui-widget", Module: "./components/ui-widget.js", Export: "Widget"},
		{Selector: "ui-widget", Module: "./index.js", Export: "RenamedWidget"},
		{Selector: "ui-widget", Module: "./index.js", Export: "Widget"},
	}
	if !reflect.DeepEqual(metadata.Components, wantMetadata) {
		t.Fatalf("unexpected component metadata: %+v", metadata)
	}
	manifestData, err := os.ReadFile(filepath.Join(root, "dist", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Exports map[string]map[string]string `json:"exports"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Exports["./components/ui-widget.js"]["types"] != "./types/src/widget.component.generated.d.ts" {
		t.Fatalf("component package export has no matching declaration: %s", manifestData)
	}
	entryTypes, err := os.ReadFile(filepath.Join(root, "dist", "types", "src", "index.d.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entryTypes), `from "./widget.component.generated.js"`) {
		t.Fatalf("relative declaration module was not rewritten to a portable ESM specifier: %s", entryTypes)
	}
	if !strings.Contains(string(entryTypes), "RenamedWidget") {
		t.Fatalf("TSGo entry declaration is missing the public alias: %s", entryTypes)
	}
	var moduleSources strings.Builder
	err = filepath.WalkDir(filepath.Join(root, "dist"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		moduleSources.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(moduleSources.String(), `from "@angulus/core"`) {
		t.Fatalf("runtime peer dependency was bundled instead of externalized: %s", moduleSources.String())
	}
	css, err := os.ReadFile(filepath.Join(root, "dist", "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "data-a-") || !strings.Contains(string(css), "button") {
		t.Fatalf("library stylesheet is not scoped: %s", css)
	}
}

func TestBuiltLibraryCanBeInstalledAndConsumed(t *testing.T) {
	tsgo := nativeTSGo(".")
	if tsgo == "" {
		t.Skip("native TSGo platform executable is unavailable")
	}
	t.Setenv("ANGULUS_TSGO", tsgo)

	libraryRoot := t.TempDir()
	writeFixture(t, libraryRoot, "package.json", `{
		"name":"@test/widgets",
		"version":"1.0.0",
		"peerDependencies":{"@angulus/core":"^0.3.0"}
	}`)
	writeFixture(t, libraryRoot, "tsconfig.json", `{
		"compilerOptions":{"target":"ES2022","module":"ESNext","moduleResolution":"Bundler","strict":true,"noEmit":true,"skipLibCheck":true},
		"include":["src/**/*.ts"]
	}`)
	writeFixture(t, libraryRoot, "node_modules/@angulus/core/package.json", `{"types":"index.d.ts","module":"index.js"}`)
	writeFixture(t, libraryRoot, "node_modules/@angulus/core/index.d.ts", `
		export interface Scope {}
		export type Render<T> = (instance:T,parent:Node,scope:Scope)=>void;
		export declare function defineComponent<T>(ctor:new (...args:any[])=>T,render:Render<T>,scopeId?:string):new (...args:any[])=>T;
		export declare function element(...args:any[]):HTMLElement;
		export declare function text(...args:any[]):Text;
		export declare function bind(...args:any[]):void;
		export declare function mountChild(...args:any[]):any;
	`)
	writeFixture(t, libraryRoot, "node_modules/@angulus/core/index.js", `
		export const defineComponent = (ctor) => ctor;
		export const element = () => document.createElement("div");
		export const text = () => document.createTextNode("");
		export const bind = () => {};
		export const mountChild = () => {};
	`)
	writeFixture(t, libraryRoot, "src/widget.ts", `export class Widget { label = "from library"; }`)
	writeFixture(t, libraryRoot, "src/widget.html", `<span>{{ label }}</span>`)
	writeFixture(t, libraryRoot, "src/widget.component.json",
		`{"selector":"ui-widget","module":"./widget.ts","export":"Widget","template":"./widget.html"}`)
	writeFixture(t, libraryRoot, "src/index.ts", `export { Widget, Widget as RenamedWidget } from "./widget.component.generated";`)

	libraryResult := BuildLibrary(context.Background(), libraryRoot, "dist")
	if libraryResult.hasErrors() {
		t.Fatalf("BuildLibrary diagnostics: %+v", libraryResult.Diagnostics)
	}
	libraryMetadata, err := ReadLibraryMetadata(filepath.Join(libraryRoot, "dist", "angulus.components.json"))
	if err != nil {
		t.Fatal(err)
	}
	foundRootAlias := false
	for _, item := range libraryMetadata.Components {
		if item.Selector == "ui-widget" && item.Module == "./index.js" && item.Export == "RenamedWidget" {
			foundRootAlias = true
		}
	}
	if !foundRootAlias {
		t.Fatalf("built metadata omitted the public root alias: %+v", libraryMetadata.Components)
	}
	entryTypes, err := os.ReadFile(filepath.Join(libraryRoot, "dist", "types", "src", "index.d.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entryTypes), "RenamedWidget") {
		t.Fatalf("TSGo library entry declaration omitted its public alias: %s", entryTypes)
	}

	appRoot := t.TempDir()
	writeFixture(t, appRoot, "package.json", `{"name":"consumer","version":"1.0.0","type":"module"}`)
	writeFixture(t, appRoot, "tsconfig.json", `{
		"compilerOptions":{"target":"ES2022","module":"ESNext","moduleResolution":"Bundler","strict":true,"noEmit":true,"skipLibCheck":true},
		"include":["src/**/*.ts"]
	}`)
	if err := copyTree(filepath.Join(libraryRoot, "dist"), filepath.Join(appRoot, "node_modules", "@test", "widgets")); err != nil {
		t.Fatalf("install built library fixture: %v", err)
	}
	writeFixture(t, appRoot, "node_modules/@angulus/core/package.json", `{"name":"@angulus/core","types":"index.d.ts","module":"index.js"}`)
	writeFixture(t, appRoot, "node_modules/@angulus/core/index.d.ts", `
		export interface Scope {}
		export type Render<T> = (instance:T,parent:Node,scope:Scope)=>void;
		export declare function defineComponent<T>(ctor:new (...args:any[])=>T,render:Render<T>,scopeId?:string):new (...args:any[])=>T;
		export declare function element(...args:any[]):HTMLElement;
		export declare function text(...args:any[]):Text;
		export declare function bind(...args:any[]):void;
		export declare function mountChild(...args:any[]):any;
	`)
	writeFixture(t, appRoot, "node_modules/@angulus/core/index.js", `
		export const defineComponent = (ctor) => ctor;
		export const element = () => document.createElement("div");
		export const text = () => document.createTextNode("");
		export const bind = () => {};
		export const mountChild = () => {};
	`)
	writeFixture(t, appRoot, "src/app.ts", `export class App {}`)
	writeFixture(t, appRoot, "src/app.html", `<ui-widget></ui-widget>`)
	writeFixture(t, appRoot, "src/app.component.json",
		`{"selector":"app-root","module":"./app.ts","export":"App","template":"./app.html","imports":[{"module":"@test/widgets","export":"RenamedWidget"}]}`)
	writeFixture(t, appRoot, "src/main.ts", `import { App } from "./app.component.generated"; void App;`)
	writeFixture(t, appRoot, "index.html", `<script type="module" src="./src/main.ts"></script>`)

	prepared := Prepare(appRoot)
	if prepared.hasErrors() {
		t.Fatalf("consumer Prepare diagnostics: %+v", prepared.Diagnostics)
	}
	render, err := os.ReadFile(filepath.Join(appRoot, "src", "app.component.render.generated.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(render), `import {RenamedWidget as __angulusComponent0} from "@test/widgets";`) {
		t.Fatalf("consumer runtime import did not preserve the installed package module specifier:\n%s", render)
	}
	checked := Check(context.Background(), appRoot)
	if checked.hasErrors() {
		t.Fatalf("consumer Check diagnostics: %+v", checked.Diagnostics)
	}
	built := Build(context.Background(), appRoot, "build")
	if built.hasErrors() {
		t.Fatalf("consumer Build diagnostics: %+v", built.Diagnostics)
	}
	if _, err := os.Stat(filepath.Join(appRoot, "build", "src", "main.js")); err != nil {
		t.Fatalf("consumer build output is missing: %v", err)
	}
}

func TestReadLibraryMetadataRejectsUnsupportedVersionsAndUnsafeModules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	for _, data := range []string{
		`{"version":2,"components":[]}`,
		`{"version":1,"components":[{"selector":"ui-widget","module":"../escape.js","export":"Widget"}]}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadLibraryMetadata(path); err == nil {
			t.Fatalf("accepted invalid metadata: %s", data)
		}
	}
}

func TestResolveLibraryComponentUsesInstalledVersionedMetadata(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "node_modules/@test/widgets/package.json",
		`{"name":"@test/widgets","angulusComponents":"./angulus.components.json"}`)
	writeFixture(t, root, "node_modules/@test/widgets/angulus.components.json",
		`{"version":1,"components":[{"selector":"ui-widget","module":"./components/ui-widget.js","export":"Widget"},{"selector":"ui-widget","module":"./index.js","export":"RenamedWidget"}]}`)

	if _, err := ResolveLibraryComponent(root, "@test/widgets", "Widget"); err == nil {
		t.Fatal("component subpath symbol was accepted as an unexported root name")
	}
	component, err := ResolveLibraryComponent(root, "@test/widgets/components/ui-widget.js", "Widget")
	if err != nil {
		t.Fatal(err)
	}
	if component != (LibraryComponentMetadata{
		Selector: "ui-widget", Module: "./components/ui-widget.js", Export: "Widget",
	}) {
		t.Fatalf("unexpected resolved component metadata: %+v", component)
	}
	component, err = ResolveLibraryComponent(root, "@test/widgets", "RenamedWidget")
	if err != nil || component.Module != "./index.js" || component.Export != "RenamedWidget" {
		t.Fatalf("root public alias did not resolve to the package entry: component=%+v err=%v", component, err)
	}
	if _, err := ResolveLibraryComponent(root, "@test/widgets/index.js", "RenamedWidget"); err == nil {
		t.Fatal("non-exported index.js subpath was accepted")
	}
	writeFixture(t, root, "node_modules/@test/widgets/angulus.components.json",
		`{"version":1,"components":[{"selector":"ui-widget","module":"./components/ui-widget.js","export":"Widget"},{"selector":"ui-other","module":"./components/ui-other.js","export":"Widget"}]}`)
	if _, err := ResolveLibraryComponent(root, "@test/widgets", "Widget"); err == nil {
		t.Fatal("ambiguous component export was accepted")
	}
	component, err = ResolveLibraryComponent(root, "@test/widgets/components/ui-other.js", "Widget")
	if err != nil || component.Selector != "ui-other" {
		t.Fatalf("component subpath was not resolved: component=%+v err=%v", component, err)
	}
	if _, err := ResolveLibraryComponent(root, "@test/widgets", "Missing"); err == nil {
		t.Fatal("missing component export was accepted")
	}
}

func TestLibraryScopeIDIncludesPackageIdentity(t *testing.T) {
	root := t.TempDir()
	descriptor := filepath.Join(root, "src", "widget.component.json")
	first := libraryScopeID(root, descriptor, "@test/first", "1.0.0")
	if first != libraryScopeID(root, descriptor, "@test/first", "1.0.0") {
		t.Fatal("library scope ID is not deterministic")
	}
	if first == libraryScopeID(root, descriptor, "@test/second", "1.0.0") {
		t.Fatal("distinct libraries share a component scope ID")
	}
}

func TestMakeDeclarationsPortableRewritesRelativeESMSpecifiers(t *testing.T) {
	root := t.TempDir()
	types := filepath.Join(root, "types")
	path := writeFixture(t, types, "index.d.ts", `export { A } from "./a"; export type { B } from "../b.ts"; /** from "./comment" */ export type C = import("./c").C; export declare const documentation: "from './fake'";`)
	if err := makeDeclarationsPortable(types, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `export { A } from "./a.js"; export type { B } from "../b.js"; /** from "./comment" */ export type C = import("./c.js").C; export declare const documentation: "from './fake'";`
	if string(data) != want {
		t.Fatalf("declaration module specifiers = %q", data)
	}
	if err := os.WriteFile(path, []byte(`export type A = import("C:/build/source").A;`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := makeDeclarationsPortable(types, root); err == nil {
		t.Fatal("absolute build-machine import was accepted")
	}
}
func TestBuildLibraryRequiresCorePeerDependencyWithoutReplacingOutput(t *testing.T) {
	tsgo := nativeTSGo(".")
	if tsgo == "" {
		t.Skip("native TSGo platform executable is unavailable")
	}
	t.Setenv("ANGULUS_TSGO", tsgo)
	root := t.TempDir()
	writeFixture(t, root, "package.json", `{"name":"@test/widgets","version":"1.0.0"}`)
	writeFixture(t, root, "tsconfig.json", `{"compilerOptions":{"target":"ES2022","module":"ESNext","moduleResolution":"Bundler"},"include":["src/**/*.ts"]}`)
	writeFixture(t, root, "node_modules/@angulus/core/package.json", `{"types":"index.d.ts"}`)
	writeFixture(t, root, "node_modules/@angulus/core/index.d.ts", `
		export interface Scope {}
		export type Render<T> = (instance:T,parent:Node,scope:Scope)=>void;
		export declare function defineComponent<T>(ctor:new (...args:any[])=>T,render:Render<T>,scopeId?:string):new (...args:any[])=>T;
		export declare function element(...args:any[]):any;
		export declare function text(...args:any[]):any;
		export declare function bind(...args:any[]):any;
	`)
	writeFixture(t, root, "src/widget.ts", `export class Widget {}`)
	writeFixture(t, root, "src/widget.html", `<p>Widget</p>`)
	writeFixture(t, root, "src/widget.component.json", `{"selector":"ui-widget","module":"./widget.ts","export":"Widget","template":"./widget.html"}`)
	writeFixture(t, root, "src/index.ts", `export {Widget} from "./widget.component.generated";`)
	output := writeFixture(t, root, "dist/keep.txt", "existing output")

	result := BuildLibrary(context.Background(), root, "dist")
	if !result.hasErrors() || !strings.Contains(result.Diagnostics[len(result.Diagnostics)-1].Message, "peerDependency") {
		t.Fatalf("missing runtime peer dependency was not rejected: %+v", result.Diagnostics)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing output" {
		t.Fatalf("failed build replaced existing output with %q", content)
	}
}
