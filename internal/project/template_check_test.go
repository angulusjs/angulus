package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpressionParserBindsReferencesAndPreservesPropertyNames(t *testing.T) {
	got, err := qualifyExpression(`user?.name + ({ label: title, count }).count + values[0].name + ctx`, nil)
	if err != "" {
		t.Fatal(err)
	}
	want := `ctx.user?.name + ({ label: ctx.title, count: ctx.count }).count + ctx.values[0].name + ctx.ctx`
	if got != want {
		t.Fatalf("rewritten expression = %q, want %q", got, want)
	}

	rewritten, _, rewriteErr := rewriteExpression(`{ item, position: $index }.item + item.id`, map[string]bool{"item": true, "$index": true}, true, false)
	if rewriteErr != nil {
		t.Fatal(rewriteErr)
	}
	if want := `{ item: item(), position: $index() }.item + item().id`; rewritten != want {
		t.Fatalf("signal locals = %q, want %q", rewritten, want)
	}
}

func TestExpressionParserRejectsUnsupportedSyntax(t *testing.T) {
	for _, source := range []string{
		`value = 1`, `new Widget()`, `items.map(item => item.name = "x")`,
		`() => 1`, `items.map(item => item.name)`, `this.name`,
		`items.map((item, index) => item.name + index)`,
		"`value ${name}`", `{[key]: value}`, `call<string>()`,
	} {
		t.Run(source, func(t *testing.T) {
			if _, _, err := rewriteExpression(source, nil, true, false); err == nil {
				t.Fatalf("unsupported expression accepted: %s", source)
			}
		})
	}
}

func TestExpressionParserReportsOriginalNestedOffset(t *testing.T) {
	source := `ready && (user.name + (new Widget()))`
	_, offset, err := rewriteExpression(source, nil, false, false)
	if err == nil || offset != strings.Index(source, "new") {
		t.Fatalf("nested diagnostic offset = %d, error = %v", offset, err)
	}
}

func TestNativeDiagnosticPositionsUseOriginalUTF16Source(t *testing.T) {
	root := t.TempDir()
	source := "// Привет 😀\r\nconst value: number = 'wrong';\r\n"
	path := writeFixture(t, root, "src/component.ts", source)
	diagnostics := parseTSGoDiagnostics("src/component.ts(2,7): error TS2322: Type 'string' is not assignable to type 'number'.")
	setDiagnosticPositions(root, diagnostics)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	diagnostic := diagnostics[0]
	want := utf16Length(source[:strings.Index(source, "value")])
	if diagnostic.File != path || diagnostic.Start != want || diagnostic.End != want+1 {
		t.Fatalf("source position = %+v, want %s:%d", diagnostic, path, want)
	}
}

func TestGeneratedConstructorDiagnosticsIdentifyDescriptor(t *testing.T) {
	root := t.TempDir()
	source := `{"selector":"app-counter","module":"./counter.ts","export":"Missing","template":"./counter.html"}`
	path := writeFixture(t, root, "counter.component.json", source)
	writeFixture(t, root, "counter.ts", "export class Counter {}\n")
	writeFixture(t, root, "counter.html", "counter")
	prepared := Prepare(root)
	if hasErrors(prepared.Diagnostics) {
		t.Fatalf("prepare: %+v", prepared.Diagnostics)
	}
	diagnostics := []Diagnostic{
		{File: prepared.Components[0].Facade, Line: 3, Column: 9, Code: "TS2305", Message: "no exported member Missing", Severity: "error"},
		{File: templateCheckPath(prepared.Components[0].Render), Line: 2, Column: 14, Code: "TS2305", Message: "no exported member Missing", Severity: "error"},
	}
	mapDescriptorDiagnostics(root, prepared.Components, diagnostics)
	for _, diagnostic := range diagnostics {
		if diagnostic.File != path || diagnostic.Start != strings.Index(source, `"export"`) {
			t.Fatalf("constructor error did not identify source descriptor: %+v", diagnostic)
		}
	}
}

func TestPrepareRejectsUnsupportedDynamicURLAndAttributeInterpolation(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "component.ts", "export class Component {}\n")
	writeFixture(t, root, "component.html", `<img [srcset]="'small.png 1x'"><div title="hello {{ name }}"></div>`)
	writeFixture(t, root, "component.component.json", `{"selector":"app-component","module":"./component.ts","export":"Component","template":"./component.html"}`)
	result := Prepare(root)
	found := map[string]bool{}
	for _, diagnostic := range result.Diagnostics {
		found[diagnostic.Code] = true
	}
	if !found["P0412"] || !found["P0413"] {
		t.Fatalf("expected dynamic URL and attribute interpolation diagnostics, got %+v", result.Diagnostics)
	}
}

func TestPrepareValidatesUnknownElementsAndExplicitCustomElements(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "component.ts", "export class Component {}\n")
	templatePath := writeFixture(t, root, "component.html", `<div></div><custom-control></custom-control>`)
	writeFixture(t, root, "component.component.json", `{"selector":"app-component","module":"./component.ts","export":"Component","template":"./component.html","customElements":["custom-control"]}`)
	result := Prepare(root)
	if hasErrors(result.Diagnostics) {
		t.Fatalf("explicit custom element was rejected: %+v", result.Diagnostics)
	}
	writeFixture(t, root, "component.html", `<div></div><unknown></unknown>`)
	result = Prepare(root)
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == "P0410" && diagnostic.File == templatePath {
			return
		}
	}
	t.Fatalf("unknown non-hyphen element was not rejected: %+v", result.Diagnostics)
}

func TestPrepareRefusesUserOwnedTemplateCheckerFile(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "component.ts", "export class Component {}\n")
	writeFixture(t, root, "component.html", `{{ value }}`)
	writeFixture(t, root, "component.component.json", `{"selector":"app-component","module":"./component.ts","export":"Component","template":"./component.html"}`)
	checkFile := writeFixture(t, root, "component.component.template-check.generated.ts", "user-owned\n")
	result := Prepare(root)
	if !hasErrors(result.Diagnostics) {
		t.Fatal("user-owned template checker collision was overwritten")
	}
	content, err := os.ReadFile(checkFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "user-owned\n" {
		t.Fatalf("user-owned template checker was modified: %q", content)
	}
}

func TestCheckEnforcesTemplateContractsAndMapsDiagnostics(t *testing.T) {
	tsgo := nativeTSGo(".")
	if tsgo == "" {
		t.Skip("native TSGo platform executable is unavailable")
	}
	t.Setenv("ANGULUS_TSGO", tsgo)
	root := t.TempDir()
	writeFixture(t, root, "tsconfig.json", `{
  "compilerOptions": {
    "target": "ES2022", "module": "ESNext", "moduleResolution": "Bundler",
    "strict": true, "skipLibCheck": true, "lib": ["ES2022", "DOM", "DOM.Iterable"]
  },
  "include": ["src/**/*.ts"]
}`)
	writeFixture(t, root, "node_modules/@angulus/core/index.d.ts", `export interface Scope {}
export interface Signal<T> { (): T }
export interface WritableSignal<T> extends Signal<T> { set(value: T): void; update(update: (value: T) => T): void }
export interface Input<T> extends Signal<T> { readonly __input: true }
export interface RequiredInput<T> extends Input<T> { readonly __requiredInput: true }
export interface Output<T> { readonly __output: true; emit(value: T): void; subscribe(handler: (value: T) => void): () => void }
export declare const input: { <T>(value: T): Input<T>; required<T>(): RequiredInput<T> };
export declare function output<T>(): Output<T>;
export type Render<T> = (instance: T, parent: Node, scope: Scope) => void;
export declare function defineComponent<T>(ctor: new () => T, render: Render<T>, scopeId?: string): new () => T;
export declare function text(parent: Node, value: unknown): Text;
export declare function bind(scope: Scope, fn: () => void): void;
export declare function element(scope: Scope, parent: Node, tag: string, scopeId: string): HTMLElement;
export declare function setProperty(node: unknown, name: string, value: unknown): void;
export declare function listen(scope: Scope, node: EventTarget, event: string, handler: (event: any) => void): void;
export declare function ifBlock(scope: Scope, parent: Node, test: () => unknown, yes: (parent: Node, scope: Scope) => void, no?: (parent: Node, scope: Scope) => void): void;
export declare function forBlock<T>(scope: Scope, parent: Node, items: () => Iterable<T>, key: (item: T, index: number) => unknown, render: (parent: Node, scope: Scope, item: Signal<T>, index: Signal<number>) => void): void;
export declare function switchBlock(scope: Scope, parent: Node, value: () => unknown, cases: Array<{ test?: () => unknown; render: (parent: Node, scope: Scope) => void }>): void;
export declare function mountChild<T>(scope: Scope, parent: Node, ctor: new () => T, values: Record<string, () => unknown>, outputs: Record<string, (value: any) => void>): void;
`)
	writeFixture(t, root, "src/child.ts", `import { input, output } from "@angulus/core";
class BaseChild {
  readonly value = input.required<number>();
  readonly label = input("label");
}
export class Child extends BaseChild {
  readonly changed = output<{ id: number }>();
}`)
	writeFixture(t, root, "src/child.html", `{{ value() }}`)
	writeFixture(t, root, "src/child.component.json", `{"selector":"app-child","module":"./child.ts","export":"Child","template":"./child.html"}`)
	writeFixture(t, root, "src/parent.ts", `import { Child } from "./child";
export class Parent {
  user: { name: string } | null = { name: "ready" };
  items = [{ id: 1, name: "one" }];
  state: { kind: "ready"; value: string } | { kind: "waiting"; reason: string } = { kind: "ready", value: "go" };
  private secret = 1;
  accept(id: number): void { void id; }
  clicked(event: MouseEvent): void { void event; }
}`)
	template := `@if (user) { <p>{{ user.name }}</p> }
@for (item of items; track item.id) { <span>{{ item.name }} {{ $index }}</span> }
@switch (state.kind) { @case ("ready") { {{ state.value }} } @default { {{ state.reason }} } }
<button (click)="clicked($event)">go</button>
<app-child label="child" [value]="1" (changed)="accept($event.id)" />`
	writeFixture(t, root, "src/parent.html", template)
	writeFixture(t, root, "src/parent.component.json", `{"selector":"app-parent","module":"./parent.ts","export":"Parent","template":"./parent.html","imports":["./child.component.json"]}`)

	result := Check(context.Background(), root)
	if hasErrors(result.Diagnostics) {
		t.Fatalf("valid template contracts produced diagnostics: %+v", result.Diagnostics)
	}
	foundDeclarations := false
	for _, dependency := range result.Dependencies {
		if strings.HasSuffix(dependency, filepath.Join("@angulus", "core", "index.d.ts")) {
			foundDeclarations = true
		}
	}
	if !foundDeclarations {
		t.Fatalf("native checker did not record declaration dependency: %v", result.Dependencies)
	}

	writeFixture(t, root, "tsconfig.json", `{
  "compilerOptions": {
    "target": "ES2022", "module": "ESNext", "moduleResolution": "Bundler",
    "strict": true, "skipLibCheck": true, "lib": ["ES2022", "DOM", "DOM.Iterable"]
  },
  "include": ["src/parent.ts"],
  "exclude": ["src/**/*.generated.ts"]
}`)
	template = `😀<app-child /> <app-child [value]="'wrong'" [label]="42" (changed)="accept($event.noSuchProperty)" />
<input [value]="1" (input)="clicked($event)" /> <p>{{ missingMember }} {{ secret }} {{ user.noSuchName }}</p>`
	writeFixture(t, root, "src/parent.html", template)
	result = Check(context.Background(), root)
	if !hasErrors(result.Diagnostics) {
		t.Fatal("invalid template contracts were accepted")
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.File != filepath.Join(root, "src/parent.html") {
			t.Errorf("template diagnostic was not mapped to HTML: %+v", diagnostic)
		}
		if diagnostic.Start < 0 || diagnostic.End < diagnostic.Start || diagnostic.End > len([]rune(template))*2 {
			t.Errorf("invalid mapped template span: %+v", diagnostic)
		}
	}
	for _, expected := range []string{"required", "string", "noSuchProperty", "missingMember", "private", "MouseEvent"} {
		found := false
		for _, diagnostic := range result.Diagnostics {
			if strings.Contains(diagnostic.Message, expected) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q contract diagnostic: %+v", expected, result.Diagnostics)
		}
	}
	for _, diagnostic := range result.Diagnostics {
		if strings.Contains(diagnostic.Message, "missingMember") {
			start := strings.Index(template, "missingMember")
			if diagnostic.Start != utf16Length(template[:start]) || diagnostic.Line != 2 {
				t.Errorf("Unicode template mapping = %+v, expected source offset %d on line 2", diagnostic, utf16Length(template[:start]))
			}
			if strings.Contains(diagnostic.Message, "noSuchName") {
				start := strings.Index(template, "noSuchName")
				if diagnostic.Start != utf16Length(template[:start]) || diagnostic.Line != 2 {
					t.Errorf("qualified property mapping = %+v, expected token offset %d", diagnostic, utf16Length(template[:start]))
				}
			}
		}
	}

	writeFixture(t, root, "src/parent.html", `{{ state.kind }}`)
	writeFixture(t, root, "src/lazy.ts", "export class Lazy {}\n")
	writeFixture(t, root, "src/lazy.html", `{{ unopenedError }}`)
	writeFixture(t, root, "src/lazy.component.json", `{"selector":"app-lazy","module":"./lazy.ts","export":"Lazy","template":"./lazy.html"}`)
	result = Check(context.Background(), root)
	foundLazyError := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.File == filepath.Join(root, "src/lazy.html") && strings.Contains(diagnostic.Message, "unopenedError") {
			foundLazyError = true
		}
	}
	if !foundLazyError {
		t.Fatalf("template outside tsconfig includes was not checked: %+v", result.Diagnostics)
	}
}
