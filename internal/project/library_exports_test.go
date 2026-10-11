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

func TestBuildLibraryReexportsInstalledExternalComponents(t *testing.T) {
	tsgo := nativeTSGo(".")
	if tsgo == "" {
		t.Skip("native TSGo platform executable is unavailable")
	}
	t.Setenv("ANGULUS_TSGO", tsgo)
	root := t.TempDir()
	writeFixture(t, root, "package.json", `{"name":"@test/reexports","version":"1.0.0","peerDependencies":{"@angulus/core":"*","@other/widgets":"*"}}`)
	writeFixture(t, root, "tsconfig.json", `{"compilerOptions":{"target":"ES2022","module":"ESNext","moduleResolution":"Bundler","strict":true},"include":["src/**/*.ts"]}`)
	writeFixture(t, root, "node_modules/@other/widgets/package.json", `{"name":"@other/widgets","type":"module","types":"index.d.ts","exports":{".":{"types":"./index.d.ts","import":"./index.js"},"./components/badge.js":{"types":"./index.d.ts","import":"./index.js"}},"angulusComponents":"./angulus.components.json"}`)
	writeFixture(t, root, "node_modules/@other/widgets/index.d.ts", `export declare class Badge { label: string; } export declare class Ordinary {}`)
	writeFixture(t, root, "node_modules/@other/widgets/index.js", `throw new Error("must not execute library"); export class Badge {} export class Ordinary {}`)
	writeFixture(t, root, "node_modules/@other/widgets/angulus.components.json", `{"version":1,"components":[{"selector":"ui-badge","module":"./index.js","export":"Badge"},{"selector":"ui-badge","module":"./components/badge.js","export":"Badge"}]}`)
	entry := writeFixture(t, root, "src/index.ts", `
		export { Badge as ExternalBadge } from "@other/widgets";
		import { Badge as LocalBadge } from "@other/widgets";
		export { LocalBadge as ImportedBadge };
		export { Badge as SubpathBadge } from "@other/widgets/components/badge.js";
		export * from "@other/widgets";
	`)
	result := BuildLibrary(context.Background(), root, "dist")
	if result.hasErrors() {
		t.Fatalf("external re-export build failed: %+v", result.Diagnostics)
	}
	metadata, err := ReadLibraryMetadata(filepath.Join(root, "dist", "angulus.components.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []LibraryComponentMetadata{
		{Selector: "ui-badge", Module: "./index.js", Export: "Badge"},
		{Selector: "ui-badge", Module: "./index.js", Export: "ExternalBadge"},
		{Selector: "ui-badge", Module: "./index.js", Export: "ImportedBadge"},
		{Selector: "ui-badge", Module: "./index.js", Export: "SubpathBadge"},
	}
	if !reflect.DeepEqual(metadata.Components, want) {
		t.Fatalf("external component metadata = %+v, want %+v", metadata.Components, want)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "components")); !os.IsNotExist(err) {
		t.Fatalf("external components must not generate local facades: %v", err)
	}
	emitted, err := os.ReadFile(filepath.Join(root, "dist", "index.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(emitted), `"@other/widgets"`) || strings.Contains(string(emitted), "must not execute") {
		t.Fatalf("external dependency was bundled: %s", emitted)
	}
	declarations, err := os.ReadFile(filepath.Join(root, "dist", "types", "src", "index.d.ts"))
	if err != nil || !strings.Contains(string(declarations), "ExternalBadge") {
		t.Fatalf("external alias declaration missing: %s, err=%v", declarations, err)
	}
	manifestData, err := os.ReadFile(filepath.Join(root, "dist", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest libraryManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.PeerDependencies["@other/widgets"] == "" {
		t.Fatal("external library peer dependency was not preserved")
	}
	got, err := ResolveLibraryComponent(root, "@other/widgets", "Badge")
	if err != nil || got.Module != "./index.js" {
		t.Fatalf("duplicate root/subpath identities must resolve root export: %+v %v", got, err)
	}
	if ok, err := libraryEmittedStarComponent(filepath.Join(root, "dist", "index.js"), entry, want[0]); err != nil || !ok {
		t.Fatalf("emitted external star was not verified: %v %v", ok, err)
	}
	consumer := t.TempDir()
	if err := copyTree(filepath.Join(root, "dist"), filepath.Join(consumer, "node_modules", "@test", "reexports")); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveLibraryComponent(consumer, "@test/reexports", "ExternalBadge")
	if err != nil || resolved != want[1] {
		t.Fatalf("installed external alias metadata did not resolve without source: %+v %v", resolved, err)
	}
}

func TestLibraryExternalExportsIgnoreOrdinaryPackagesAndRejectInvalidMetadata(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "node_modules/ordinary/package.json", `{"name":"ordinary","type":"module"}`)
	entry := writeFixture(t, root, "src/index.ts", `export { Helper } from "ordinary"; export * from "ordinary";`)
	exports, err := libraryRootComponentExports(entry, nil)
	if err != nil || len(exports) != 0 {
		t.Fatalf("ordinary package exports were classified as components: %+v %v", exports, err)
	}
	writeFixture(t, root, "node_modules/ordinary/package.json", `{"name":"ordinary","angulusComponents":"./missing.json"}`)
	if _, err := libraryRootComponentExports(entry, nil); err == nil {
		t.Fatal("explicit missing component metadata was silently ignored")
	}
	writeFixture(t, root, "node_modules/ordinary/missing.json", `{"version":99,"components":[]}`)
	if _, err := libraryRootComponentExports(entry, nil); err == nil {
		t.Fatal("invalid external component metadata was silently ignored")
	}
}

func TestLibraryExportLexerResolvesAliasesStarsAndImportBindings(t *testing.T) {
	root := t.TempDir()
	facade := writeFixture(t, root, "src/widget.component.generated.ts", `export { __angulusCtor as Badge };`)
	writeFixture(t, root, "src/level-one.ts", `
		export { Badge as PublicBadge } from "./widget.component.generated";
		export * from "./starred";
	`)
	writeFixture(t, root, "src/starred.ts", `
		export { Badge as StarBadge } from "./widget.component.generated";
		export default class NotAComponent {}
	`)
	writeFixture(t, root, "src/imported-alias.ts", `
		import { Badge as LocalBadge } from "./widget.component.generated";
		export { LocalBadge as ImportAlias };
	`)
	entry := writeFixture(t, root, "src/index.ts", `
		export { PublicBadge as Renamed } from "./level-one";
		export * from "./level-one";
		export { ImportAlias } from "./imported-alias";
		export { default as DefaultOnly } from "./starred";
	`)
	component := &Component{
		Facade:     facade,
		Module:     writeFixture(t, root, "src/widget.ts", `export class Badge {}`),
		Descriptor: Descriptor{Selector: "ui-badge", Export: "Badge"},
	}

	got, err := libraryRootComponentExports(entry, []*Component{component})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, item := range got {
		if item.Selector != "ui-badge" || item.Module != "./index.js" {
			t.Fatalf("unexpected public component metadata: %+v", item)
		}
		names = append(names, item.Export)
	}
	want := []string{"ImportAlias", "PublicBadge", "Renamed", "StarBadge"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("component exports = %v, want %v", names, want)
	}
}

func TestLibraryExportLexerIgnoresCommentsStringsAndNestedExports(t *testing.T) {
	module, err := parseLibraryModule(`
		// export { Fake } from "./fake";
		const text = "export * from './also-fake'";
		const matcher = /export \{ RegexFake \} from "\.\/regex-fake"/;
		function nested() { export { Nope }; }
		export { Real as Public } from "./real";
		export type { Shape } from "./types";
		export type * from "./type-only";
		export * as Namespace from "./namespace";
		export default class DefaultValue {}
	`)
	if err != nil {
		t.Fatal(err)
	}
	if ref, ok := module.exports["Public"]; !ok || ref.module != "./real" || ref.name != "Real" {
		t.Fatalf("named re-export was not parsed: %+v, exists=%v", ref, ok)
	}
	if _, ok := module.exports["Shape"]; ok {
		t.Fatal("type-only export was treated as a runtime value")
	}
	if _, ok := module.exports["Namespace"]; ok || len(module.stars) != 0 {
		t.Fatalf("namespace export was treated as export *: exports=%v stars=%v", module.exports, module.stars)
	}
	for _, excluded := range []string{"Fake", "RegexFake", "Nope", "DefaultValue"} {
		if _, ok := module.exports[excluded]; ok {
			t.Fatalf("%q was incorrectly treated as a named component export", excluded)
		}
	}
}

func TestLexTSModuleExposesByteSpansAndSkipsLiteralContents(t *testing.T) {
	source := "/* α */ const text = 'export class StringFake';\r\n" +
		"const template = `export class TemplateFake ${ /export/.test(value) ? `nested ${\"export\"}` : \"\" }`;\n" +
		"if (ready) /export class RegexFake/.test(text);\n" +
		"const quotient = left / right / divisor;\n" +
		"export class Real {}"
	tokens, err := LexTSModule(source)
	if err != nil {
		t.Fatal(err)
	}
	var identifiers []string
	for _, token := range tokens {
		if token.Start < 0 || token.End > len(source) || token.Start >= token.End ||
			source[token.Start:token.End] != token.Text {
			t.Fatalf("token span does not match source: %+v", token)
		}
		if token.Kind == TSModuleIdentifier {
			identifiers = append(identifiers, token.Text)
		}
		if token.Kind == TSModuleString && token.Value == "" {
			t.Fatalf("nonempty string literal decoded empty: %+v", token)
		}
	}
	for _, hidden := range []string{"StringFake", "TemplateFake", "RegexFake"} {
		for _, identifier := range identifiers {
			if identifier == hidden {
				t.Fatalf("literal contents leaked as identifiers: %q", hidden)
			}
		}
	}
	if len(identifiers) < 3 || identifiers[len(identifiers)-1] != "Real" || identifiers[len(identifiers)-2] != "class" {
		t.Fatalf("final declaration tokens were not retained: %v", identifiers)
	}
	if len(identifiers) == 0 {
		t.Fatal("lexer returned no identifiers")
	}
	foundDivision := false
	for _, token := range tokens {
		if token.Text == "/" && token.Kind == TSModulePunctuation {
			foundDivision = true
		}
	}
	if !foundDivision {
		t.Fatal("division operators were treated as regular-expression literals")
	}
}

func TestLexTSModuleAcceptsCommonTypeScriptLexicalForms(t *testing.T) {
	source := `@Component
export class Widget {
	#privateValue = 1_000n;
	protected input!: string;
	field = this.#privateValue satisfies number;
}`
	tokens, err := LexTSModule(source)
	if err != nil {
		t.Fatalf("valid TypeScript lexical form failed: %v", err)
	}
	var foundPrivate, foundProtected bool
	for _, token := range tokens {
		foundPrivate = foundPrivate || token.Text == "#privateValue"
		foundProtected = foundProtected || token.Text == "protected"
	}
	if !foundPrivate || !foundProtected {
		t.Fatalf("TypeScript identifiers/modifiers were not preserved: %+v", tokens)
	}
}

func TestSharedModuleLexerKeepsRegexAndNestedTemplateBracesOpaque(t *testing.T) {
	for _, literal := range []string{
		`/[{}]/`,
		"`outer ${`inner`}`",
		"`outer ${ /[}]/.test(value) ? `inner ${/[{]/.test(value)}` : '' }`",
		"`first ${value} second ${ /[}]/.test(value) }`",
		"`outer ${ { text: '}', matcher: /[}]/, nested: `inner ${value}` } }`",
	} {
		t.Run(literal, func(t *testing.T) {
			source := "const literal = " + literal + `; export { Badge as Public } from "./badge";`
			tokens, err := tokenizeLibraryModule(source)
			if err != nil {
				t.Fatal(err)
			}
			foundOpaque := false
			for _, token := range tokens {
				if token.text == literal && token.kind == libraryTokenOpaque {
					foundOpaque = true
				}
			}
			if !foundOpaque {
				t.Fatalf("literal was not returned as a single opaque token: %+v", tokens)
			}
			module, err := parseLibraryModule(source)
			if err != nil {
				t.Fatal(err)
			}
			if module.exports["Public"] != (libraryExportReference{module: "./badge", name: "Badge"}) {
				t.Fatalf("literal braces hid the following export: %+v", module.exports)
			}
		})
	}
}

func TestSharedModuleLexerPreservesGroupedArrowAndMemberPositions(t *testing.T) {
	source := `export class Widget {
		safe = () => /[{}]/.test("private field = input()");
		text = ` + "`outer ${`private hidden = output()`}`" + `;
		private hidden = input();
		protected event = output();
	}`
	tokens, err := tokenizeLibraryModule(source)
	if err != nil {
		t.Fatal(err)
	}
	var privateCount, protectedCount, arrows int
	for _, token := range tokens {
		if source[token.start:token.end] != token.text {
			t.Fatalf("token has incorrect byte span: %+v", token)
		}
		if token.kind == libraryTokenIdentifier {
			switch token.text {
			case "private":
				privateCount++
			case "protected":
				protectedCount++
			}
		}
		if token.kind == libraryTokenPunctuation && token.text == "=>" {
			arrows++
		}
	}
	if privateCount != 1 || protectedCount != 1 || arrows != 1 {
		t.Fatalf("member/arrow tokens were not preserved: private=%d protected=%d arrows=%d", privateCount, protectedCount, arrows)
	}
}

func TestLibraryMetafileExportsChecksExactOutput(t *testing.T) {
	metafile := `{"outputs":{"dist/index.js":{"exports":["Badge","Renamed"]},"dist/components/ui-badge.js":{"exports":["Badge"]}}}`
	exports, err := libraryMetafileExports(metafile, "index.js")
	if err != nil {
		t.Fatal(err)
	}
	if !exports["Badge"] || !exports["Renamed"] || exports["Missing"] {
		t.Fatalf("unexpected entry exports: %v", exports)
	}
	if _, err := libraryMetafileExports(metafile, "components/missing.js"); err == nil {
		t.Fatal("missing compiled subpath was accepted")
	}
}

func TestReadLibraryMetadataAllowsPublicAliasesForSameSelector(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	write := `{"version":1,"components":[
		{"selector":"ui-badge","module":"./components/ui-badge.js","export":"Badge"},
		{"selector":"ui-badge","module":"./index.js","export":"Badge"},
		{"selector":"ui-badge","module":"./index.js","export":"RenamedBadge"}
	]}`
	if err := os.WriteFile(path, []byte(write), 0644); err != nil {
		t.Fatal(err)
	}
	metadata, err := ReadLibraryMetadata(path)
	if err != nil || len(metadata.Components) != 3 {
		t.Fatalf("valid aliases rejected: metadata=%+v err=%v", metadata, err)
	}
}
