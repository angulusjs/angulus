package project

import (
	"strings"
	"testing"
)

func TestDiscoverRejectsHiddenInputOutputDeclarationsWithoutTemplateUse(t *testing.T) {
	cases := []string{
		`import {input} from "@angulus/core"; export class Widget { private value = input.required<string>(); }`,
		`import {output as event} from "@angulus/core"; export class Widget { protected changed = event<number>(); }`,
		`import {input as bind} from "@angulus/core"; export class Widget { readonly #value = bind(1); }`,
		`import * as core from "@angulus/core"; export class Widget { private readonly value = core.input.required<{id: number}>(); }`,
		`import {input} from "@angulus/core"; class Actual { private value = (input(1)); } export { Actual as Widget };`,
		`import {input} from "@angulus/core"; export class Widget { private value: { (): number } = input(1); }`,
		`import {output} from "@angulus/core"; const key = Symbol(); export class Widget { private [key] = output<number>(); }`,
		`const regex = /class Widget \{/; import {input} from "@angulus/core"; export class Widget { private value = input(1); }`,
	}
	for _, source := range cases {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			module := writeFixture(t, root, "widget.ts", source)
			writeFixture(t, root, "widget.html", "<p>no bindings</p>")
			writeFixture(t, root, "widget.component.json",
				`{"selector":"app-widget","module":"./widget.ts","export":"Widget","template":"./widget.html"}`)
			result := Discover(root)
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Code == "P0111" && diagnostic.File == module && diagnostic.Line > 0 && diagnostic.Column > 0 {
					return
				}
			}
			t.Fatalf("hidden input/output was accepted: %+v", result.Diagnostics)
		})
	}
}

func TestDiscoverVisibilityInspectionDoesNotRejectOrdinaryTypeScript(t *testing.T) {
	source := `import {input, output} from "@angulus/core";
const create = () => input(0);
export class Widget {
  private secret = 1;
  private label = "private bad = input.required()";
  private text = ` + "`private bad = output()`" + `;
  private nested = ` + "`outer ${`inner ${\"protected bad = input()\"}`}`" + `;
  private regex = /[{}]/;
  private unused!: number
  readonly value = input(1);
  readonly changed = output<number>();
  private factory(): () => number { return create(); }
  private callback: () => number = create;
  readonly read = () => this.#hidden;
  #hidden = 1;
  // private bad = input(1);
}`
	root := t.TempDir()
	writeFixture(t, root, "widget.ts", source)
	writeFixture(t, root, "widget.html", "<p>no bindings</p>")
	writeFixture(t, root, "widget.component.json",
		`{"selector":"app-widget","module":"./widget.ts","export":"Widget","template":"./widget.html"}`)
	result := Discover(root)
	if hasErrors(result.Diagnostics) {
		t.Fatalf("ordinary TypeScript source was rejected: %+v", result.Diagnostics)
	}
	for _, diagnostic := range result.Diagnostics {
		if strings.Contains(diagnostic.Message, "must be public") {
			t.Fatalf("non-contract field was rejected: %+v", diagnostic)
		}
	}
}
