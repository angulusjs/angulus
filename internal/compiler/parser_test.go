package compiler

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestParseNestedTemplate(t *testing.T) {
	source := `<section title="a &amp; b" [hidden]="!ready()" (click)="run($event)">
@if (ready()) {<b>{{ user({x: "}}"}) }}</b>} @else if (pending()) {wait} @else {no}
@for (item of items(); track item.id) {<input [(value)]="item.name">}
@switch (state()) {@case ('a') {A} @case (2) {<br>} @default {Z}}
</section>`
	result := Parse(source, "app.html")
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", result.Diagnostics)
	}
	root := result.Nodes[0]
	if root.Kind != "element" || root.Tag != "section" || root.End != len(source) {
		t.Fatalf("unexpected root: %+v", root)
	}
	if root.Attributes[0].Value != "a & b" || root.Attributes[1].Name != "[hidden]" {
		t.Fatalf("attributes: %+v", root.Attributes)
	}
	conditional := root.Children[1]
	if conditional.Kind != "if" || conditional.Expression != "ready()" ||
		conditional.Otherwise[0].Kind != "if" || conditional.Otherwise[0].Otherwise[0].Text != "no" {
		t.Fatalf("conditional: %+v", conditional)
	}
	if conditional.Children[0].Children[0].Expression != `user({x: "}}"})` {
		t.Fatalf("nested expression lost: %+v", conditional.Children)
	}
	loop := root.Children[3]
	if loop.Kind != "for" || loop.Item != "item" || loop.Expression != "items()" || loop.Track != "item.id" {
		t.Fatalf("loop: %+v", loop)
	}
	switchNode := root.Children[5]
	if len(switchNode.Cases) != 3 || switchNode.Cases[2].Expression != "" || switchNode.Cases[2].Children[0].Text != "Z" {
		t.Fatalf("switch: %+v", switchNode)
	}
}

func TestUTF16Positions(t *testing.T) {
	source := "😀 привет {{  count() }}<input [value]=\"name()\">"
	result := Parse(source, "unicode.html")
	if len(result.Diagnostics) != 0 {
		t.Fatal(result.Diagnostics)
	}
	offset := func(prefix string) int { return len(utf16.Encode([]rune(prefix))) }
	interpolation := result.Nodes[1]
	if interpolation.Start != offset("😀 привет ") || interpolation.ExprStart != offset("😀 привет {{  ") {
		t.Fatalf("UTF16 expression positions: %+v", interpolation)
	}
	attr := result.Nodes[2].Attributes[0]
	if attr.ValueStart != offset("😀 привет {{  count() }}<input [value]=\"") {
		t.Fatalf("UTF16 attribute positions: %+v", attr)
	}
	bad := Parse("😀<div>", "unicode.html")
	if len(bad.Diagnostics) != 1 || bad.Diagnostics[0].Start != 7 || bad.Diagnostics[0].File != "unicode.html" {
		t.Fatalf("UTF16 diagnostics: %+v", bad.Diagnostics)
	}
}

func TestExpressionDelimiters(t *testing.T) {
	for _, source := range []string{
		`{{ fn("}}", {'x': [1, 2]}, ` + "`hello }}`" + `) }}`,
		`@if (fn(')', {x: true})) {{{ ({a: 1}).a }}}`,
		"{{ count(/* }} ignored */) }}",
		"{{ count(// }} ignored\n) }}",
		"{{ `outer ${`inner ${value}` + '}}'}` }}",
		`@for (x of items(";", {a: [1,2]}); track key(x, ";")) { {{x}} }`,
		`@for (x of items(/* ; ignored */); track x.id) {}`,
		`<img src=a/><br><!-- comment -->literal @email.example &lt;`,
	} {
		t.Run(source, func(t *testing.T) {
			result := Parse(source, "x.html")
			if len(result.Diagnostics) != 0 {
				t.Fatal(result.Diagnostics)
			}
		})
	}
}

func TestMalformedTemplates(t *testing.T) {
	tests := []struct{ source, code string }{
		{`<div>`, "F1005"},
		{`<div></span>`, "F1002"},
		{`</div>`, "F1002"},
		{`<!--`, "F1003"},
		{`{{ count( }}`, "F1015"},
		{`{{ }}`, "F1013"},
		{`<input value="abc`, "F1008"},
		{`<p a="x" a="y"></p>`, "F1010"},
		{`<input [value]>`, "F1011"},
		{`@if (ok) {x`, "F1006"},
		{`@if ok {x}`, "F1016"},
		{`@if (ok) x`, "F1017"},
		{`@else {x}`, "F1004"},
		{`@for (x of xs) {x}`, "F1018"},
		{`@for (x in xs; track x) {x}`, "F1019"},
		{`@for (x of xs; other x) {x}`, "F1020"},
		{`@switch (x) {@default {} @default {}}`, "F1022"},
		{`@switch (x) {@default {} @case (1) {}}`, "F1021"},
		{`@switch (x) {oops}`, "F1023"},
	}
	for _, test := range tests {
		t.Run(test.source, func(t *testing.T) {
			result := Parse(test.source, "invalid.html")
			found := false
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Code == test.code {
					found = true
				}
				if diagnostic.Start < 0 || diagnostic.End < diagnostic.Start || diagnostic.End > len(test.source) || diagnostic.Severity != "error" {
					t.Fatalf("invalid diagnostic: %+v", diagnostic)
				}
			}
			if !found {
				t.Fatalf("expected %s, got %+v", test.code, result.Diagnostics)
			}
		})
	}
}

func TestNestingLimit(t *testing.T) {
	result := Parse(strings.Repeat("<div>", 300), "")
	if len(result.Diagnostics) == 0 || result.Diagnostics[0].Code != "F1000" {
		t.Fatalf("expected depth diagnostic, got %+v", result.Diagnostics)
	}
}

func FuzzParse(f *testing.F) {
	for _, source := range []string{"", "{{x}}", "<div>", "@if(x){<p>x</p>} @else {y}", "@for(x of xs; track x){x}", "😀"} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		result := Parse(source, "fuzz.html")
		for _, d := range result.Diagnostics {
			if d.Start < 0 || d.End < d.Start {
				t.Fatalf("bad range: %+v", d)
			}
		}
	})
}
