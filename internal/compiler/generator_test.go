package compiler

import (
	"os/exec"
	"strings"
	"testing"
	"unicode/utf16"
)

func generated(t *testing.T, source string, components map[string]string) string {
	t.Helper()
	parsed := Parse(source, "test.html")
	if len(parsed.Diagnostics) > 0 {
		t.Fatal(parsed.Diagnostics)
	}
	result := Generate(parsed.Nodes, "data-f-test", components)
	if len(result.Diagnostics) > 0 {
		t.Fatal(result.Diagnostics)
	}
	return result.Code
}

func TestGenerateBindings(t *testing.T) {
	code := generated(t, `<div title="&quot;x&quot;" [hidden]="ctx.hidden()" (click)="ctx.run($event)">{{ctx.count()}}<input [(value)]="ctx.name"></div>`, nil)
	for _, expected := range []string{
		`(ctx,host,scope)=>{`, `__f.element(scope,host,"div","data-f-test")`,
		`.setAttribute("title","\"x\"")`, `"hidden",(ctx.hidden())`,
		`"click",($event)=>{ctx.run($event);}`, `.data=String((ctx.count())??"")`,
		`"value",(ctx.name)()`, `(ctx.name).set($event.target.value)`,
	} {
		if !strings.Contains(code, expected) {
			t.Errorf("missing %q in:\n%s", expected, code)
		}
	}
}

func TestGenerateControlFlowAndComponents(t *testing.T) {
	code := generated(t, `@if (ctx.ok()) {<child-box [value]="ctx.count()" (change)="ctx.changed($event)"/>} @else {no}
@for (__angulus1 of ctx.items(); track __angulus1.id) {
@for (inner of __angulus1().children; track inner.id) {<b>{{__angulus1().id}}:{{inner().id}}:{{$index()}}</b>}}
@switch (ctx.state()) {@case (1) {one} @default {default}}`, map[string]string{"child-box": "Child"})
	for _, expected := range []string{"__f.ifBlock", "__f.forBlock", "__f.switchBlock", "__f.mountChild",
		`"value":()=> (ctx.count())`, `"change":($event)=>{ctx.changed($event);}`,
		`(__angulus1,$index)=> (__angulus1.id)`, `()=> (__angulus1().children)`, `test:()=> (1)`} {
		if !strings.Contains(code, expected) {
			t.Errorf("missing %q in:\n%s", expected, code)
		}
	}
	if strings.Contains(code, "const __angulus1=") || strings.Contains(code, "(__angulus1,__angulus") {
		t.Fatalf("generated identifier shadows user item:\n%s", code)
	}
}

func TestGenerateRejectsUnsupported(t *testing.T) {
	for _, source := range []string{
		`<div onclick="bad()"></div>`, `<div [innerHTML]="ctx.html"></div>`,
		`<iframe srcdoc="bad"></iframe>`, `<script>alert(1)</script>`,
		`<a href="java&#x73;cript:bad()">bad</a>`,
		`<input type="checkbox" [(value)]="ctx.value">`, `<select [(value)]="ctx.value"></select>`,
		`<input [type]="ctx.type" [(value)]="ctx.value">`,
		`<input value="x" [(value)]="ctx.value">`, `<input [(checked)]="ctx.value">`,
		`<child-box><span>projected</span></child-box>`, `<child-box [(value)]="ctx.value"/>`,
		`@for (ctx of ctx.items(); track ctx.id) {}`,
		`<input [broken="ctx.value">`,
	} {
		t.Run(source, func(t *testing.T) {
			result := Generate(Parse(source, "").Nodes, "", map[string]string{"child-box": "Child"})
			if len(result.Diagnostics) == 0 || result.Code != "" {
				t.Fatalf("unsafe code accepted: %+v", result)
			}
		})
	}
}

func TestGeneratedJavaScriptExecutes(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is unavailable")
	}
	code := generated(t, `<button (click)="ctx.increment()">{{ctx.count()}}</button>
<input [(value)]="ctx.name">
@if (ctx.ok) {yes} @else {no}
@for (outer of ctx.items; track outer.id) {
@for (inner of outer().children; track inner.id) { {{outer().id + inner().id + $index()}} }}
@switch (ctx.mode) {@case (1) {matched} @default {default}}
@for (Child of ctx.items; track Child.id) {<child-box [value]="ctx.count()" (change)="ctx.changed($event)"/>}`, map[string]string{"child-box": "Child"})
	script := `
const assert=require('node:assert/strict');
const texts=[],elements=[],effects=[],listeners=[],keys=[],children=[];
let count=1, name='before', changed;
const signal=()=>name; signal.set=v=>name=v;
class Child {}
const ctx={count:()=>count,increment(){assert.equal(this,ctx);count++},name:signal,ok:true,
items:[{id:10,children:[{id:1},{id:2}]}],mode:1,changed(v){changed=v}};
const __f={
text(parent,value){const n={data:value};texts.push(n);return n},
element(scope,parent,tag,id){const n={tag,setAttribute(){}};elements.push(n);return n},
bind(scope,fn){effects.push(fn);fn()},
listen(scope,node,event,handler){listeners.push({node,event,handler})},
setProperty(node,name,value){node[name]=value},
ifBlock(scope,parent,test,yes,no){(test()?yes:no)?.(parent,scope)},
forBlock(scope,parent,items,key,render){items().forEach((item,index)=>{keys.push(key(item,index));render(parent,scope,()=>item,()=>index)})},
switchBlock(scope,parent,expression,cases){const value=expression();const selected=cases.find(c=>!c.test||c.test()===value);selected?.render(parent,scope)},
mountChild(scope,parent,Ctor,inputs,outputs){children.push({Ctor,inputs,outputs})}
};
const factory=(` + code + `);
factory(ctx,{}, {});
assert.deepEqual(keys,[10,1,2,10]);
assert(texts.some(n=>n.data==='11'));
assert(texts.some(n=>n.data==='13'));
assert(texts.some(n=>n.data==='yes'));
assert(texts.some(n=>n.data==='matched'));
assert(!texts.some(n=>n.data==='default'));
listeners.find(l=>l.event==='click').handler({});
effects.forEach(f=>f());
assert.equal(texts[0].data,'2');
assert.equal(elements.find(n=>n.tag==='input').value,'before');
listeners.find(l=>l.event==='input').handler({target:{value:'after'}});
assert.equal(name,'after');
assert.equal(children[0].inputs.value(),2);
assert.equal(children[0].Ctor,Child);
children[0].outputs.change(42);
assert.equal(changed,42);
`
	if output, err := exec.Command("node", "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("generated JavaScript failed: %v\n%s\n%s", err, output, code)
	}
}

func TestGeneratorMalformedAST(t *testing.T) {
	for _, node := range []Node{
		{Kind: "unknown"}, {Kind: "element"}, {Kind: "interpolation"},
		{Kind: "for", Item: "x);bad()"},
		{Kind: "switch", Expression: "ctx.x", Cases: []Case{{}, {Expression: "1"}}},
	} {
		result := Generate([]Node{node}, "", nil)
		if len(result.Diagnostics) == 0 || result.Code != "" || len(result.Mappings) != 0 {
			t.Fatalf("malformed node accepted: %+v", node)
		}
	}
}

func TestGeneratedMappings(t *testing.T) {
	source := `😀<div title="😀" [hidden]="ctx.hidden" (click)="ctx.clicked($event)">{{ name() }}</div>
<input [(value)]="ctx.name">
@if (ctx.ok) {yes} @else {no}
@for (item of ctx.items; track item.id) {<b>{{item().label('😀')}}</b>}
@switch (ctx.mode) {@case (ctx.first) {first} @default {last}}
<child-box title="child" [value]="ctx.childValue" (change)="ctx.changed($event)"/>`
	parsed := Parse(source, "mapped.html")
	if len(parsed.Diagnostics) > 0 {
		t.Fatal(parsed.Diagnostics)
	}
	// Frontend qualification changes emitted text, never the template offsets.
	parsed.Nodes[1].Children[0].Expression = "ctx.name()"
	result := Generate(parsed.Nodes, "data-f-map", map[string]string{"child-box": "Child"})
	if len(result.Diagnostics) > 0 {
		t.Fatal(result.Diagnostics)
	}
	generatedUnits := utf16.Encode([]rune(result.Code))
	sourceUnits := utf16.Encode([]rune(source))
	last := -1
	for _, mapping := range result.Mappings {
		if mapping.Generated <= last || mapping.Generated >= len(generatedUnits) || mapping.Source < 0 || mapping.Source >= len(sourceUnits) {
			t.Fatalf("invalid mapping %+v after %d", mapping, last)
		}
		last = mapping.Generated
	}
	expect := func(generatedText, sourceText string, occurrences int) {
		t.Helper()
		byteOffset := strings.Index(source, sourceText)
		if byteOffset < 0 {
			t.Fatalf("missing source fixture %q", sourceText)
		}
		sourceOffset := len(utf16.Encode([]rune(source[:byteOffset])))
		found := 0
		for _, mapping := range result.Mappings {
			if mapping.Source == sourceOffset && strings.HasPrefix(string(utf16.Decode(generatedUnits[mapping.Generated:])), generatedText) {
				found++
			}
		}
		if found != occurrences {
			t.Errorf("%q -> %q: expected %d exact UTF16 anchors, got %d\n%+v\n%s", generatedText, sourceText, occurrences, found, result.Mappings, result.Code)
		}
	}
	expect("ctx.name()", "name()", 1)
	expect("ctx.name)", "ctx.name", 2)
	for _, expression := range []string{
		"ctx.hidden", "ctx.clicked($event)", "ctx.ok", "ctx.items", "item.id",
		"item().label('😀')", "ctx.mode", "ctx.first", "ctx.childValue", "ctx.changed($event)",
	} {
		expect(expression, expression, 1)
	}
	expect(`"child"`, "child\"", 1)
	for _, n := range parsed.Nodes {
		found := false
		for _, mapping := range result.Mappings {
			if mapping.Source == n.Start {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing node-start anchor for %+v", n)
		}
	}
}
