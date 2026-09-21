package compiler

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type generator struct {
	code         strings.Builder
	scopeID      string
	components   map[string]string
	constructors map[string]string
	diagnostics  []Diagnostic
	used         map[string]bool
	next         int
	generated    int
	mappings     []Mapping
}

// Generate expects JavaScript expressions resolved by the TypeScript frontend.
// It never evaluates source and emits text through DOM text nodes only.
func Generate(nodes []Node, scopeID string, components map[string]string) GenerateResult {
	g := &generator{scopeID: scopeID, components: components, constructors: map[string]string{}, diagnostics: []Diagnostic{}, used: map[string]bool{}}
	g.reserve(nodes)
	g.line("(ctx,host,scope)=>{")
	var selectors []string
	for selector := range components {
		selectors = append(selectors, selector)
		g.used[components[selector]] = true
	}
	sort.Strings(selectors)
	for _, selector := range selectors {
		ctor := components[selector]
		if identifier(ctor) {
			alias := g.variable()
			g.constructors[selector] = alias
			g.line("const %s=%s;", alias, ctor)
		}
	}
	g.nodes(nodes, "host", "scope", 0)
	g.line("}")
	code := g.code.String()
	if len(g.diagnostics) > 0 {
		code = ""
		g.mappings = nil
	}
	return GenerateResult{Code: code, Diagnostics: g.diagnostics, Mappings: g.mappings}
}

func js(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func (g *generator) reserve(nodes []Node) {
	for _, n := range nodes {
		g.used[n.Item] = true
		g.reserve(n.Children)
		g.reserve(n.Otherwise)
		for _, c := range n.Cases {
			g.reserve(c.Children)
		}
	}
}

func (g *generator) variable() string {
	for {
		g.next++
		name := fmt.Sprintf("__angulus%d", g.next)
		if !g.used[name] {
			g.used[name] = true
			return name
		}
	}
}

func (g *generator) line(format string, values ...any) {
	g.write(format, values...)
	g.append("\n")
}

type mappedText struct {
	text   string
	source int
}

func mapped(text string, source int) mappedText {
	return mappedText{text, source}
}

func (g *generator) append(text string) {
	g.code.WriteString(text)
	for _, r := range text {
		g.generated++
		if r > 0xffff {
			g.generated++
		}
	}
}

func (g *generator) mark(source int) {
	mapping := Mapping{Generated: g.generated, Source: source}
	if len(g.mappings) > 0 && g.mappings[len(g.mappings)-1].Generated == g.generated {
		g.mappings[len(g.mappings)-1] = mapping
	} else {
		g.mappings = append(g.mappings, mapping)
	}
}

// Emission templates use only %s placeholders. Writing fragments individually
// retains exact anchors even when the same expression occurs several times.
func (g *generator) write(format string, values ...any) {
	parts := strings.Split(format, "%s")
	if len(parts) != len(values)+1 {
		panic("compiler emission placeholder mismatch")
	}
	g.append(parts[0])
	for i, value := range values {
		if fragment, ok := value.(mappedText); ok {
			g.mark(fragment.source)
			g.append(fragment.text)
		} else {
			g.append(fmt.Sprint(value))
		}
		g.append(parts[i+1])
	}
}

func (g *generator) error(n Node, code, message string) {
	g.diagnostics = append(g.diagnostics, Diagnostic{"", n.Start, n.End, code, message, "error"})
}

func (g *generator) expression(n Node, value string) string {
	if strings.TrimSpace(value) == "" {
		g.error(n, "F2000", "Missing expression in "+n.Kind+" node")
		return "undefined"
	}
	return value
}

func (g *generator) nodes(nodes []Node, parent, scope string, depth int) {
	if depth > 256 {
		g.error(Node{}, "F2001", "Template nesting exceeds 256 levels")
		return
	}
	for _, n := range nodes {
		g.mark(n.Start)
		switch n.Kind {
		case "text":
			g.line("__f.text(%s,%s);", parent, js(n.Text))
		case "interpolation":
			text := g.variable()
			g.line("const %s=__f.text(%s,\"\");", text, parent)
			g.line("__f.bind(%s,()=>{%s.data=String((%s)??\"\");});", scope, text, mapped(g.expression(n, n.Expression), n.ExprStart))
		case "element":
			g.element(n, parent, scope, depth)
		case "if":
			childParent, childScope := g.variable(), g.variable()
			g.line("__f.ifBlock(%s,%s,()=> (%s),(%s,%s)=>{", scope, parent, mapped(g.expression(n, n.Expression), n.ExprStart), childParent, childScope)
			g.nodes(n.Children, childParent, childScope, depth+1)
			if len(n.Otherwise) > 0 {
				g.line("},(%s,%s)=>{", childParent, childScope)
				g.nodes(n.Otherwise, childParent, childScope, depth+1)
			}
			g.line("});")
		case "for":
			if !identifier(n.Item) || n.Item == "ctx" || n.Item == "__f" || n.Item == "$index" || n.Item == "arguments" || n.Item == "eval" {
				g.error(n, "F2002", "Invalid or reserved @for item identifier: "+n.Item)
				continue
			}
			childParent, childScope := g.variable(), g.variable()
			g.line("__f.forBlock(%s,%s,()=> (%s),(%s,$index)=> (%s),(%s,%s,%s,$index)=>{",
				scope, parent, mapped(g.expression(n, n.Expression), n.ExprStart), n.Item, mapped(g.expression(n, n.Track), n.TrackStart), childParent, childScope, n.Item)
			g.nodes(n.Children, childParent, childScope, depth+1)
			g.line("});")
		case "switch":
			g.line("__f.switchBlock(%s,%s,()=> (%s),[", scope, parent, mapped(g.expression(n, n.Expression), n.ExprStart))
			defaultSeen := false
			for i, c := range n.Cases {
				if c.Expression == "" {
					if defaultSeen || i != len(n.Cases)-1 {
						g.error(n, "F2003", "A single @default must be the last switch case")
					}
					defaultSeen = true
					g.line("{")
				} else {
					g.line("{test:()=> (%s),", mapped(c.Expression, c.ExprStart))
				}
				childParent, childScope := g.variable(), g.variable()
				g.line("render:(%s,%s)=>{", childParent, childScope)
				g.nodes(c.Children, childParent, childScope, depth+1)
				g.line("}},")
			}
			g.line("]);")
		default:
			g.error(n, "F2004", "Unknown AST node kind: "+n.Kind)
		}
	}
}

func attributeKind(name string) (kind, target string) {
	if strings.HasPrefix(name, "[(") && strings.HasSuffix(name, ")]") {
		return "twoway", name[2 : len(name)-2]
	}
	if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
		return "property", name[1 : len(name)-1]
	}
	if strings.HasPrefix(name, "(") && strings.HasSuffix(name, ")") {
		return "event", name[1 : len(name)-1]
	}
	return "static", name
}

func dangerousProperty(name string) bool {
	name = strings.ToLower(name)
	return name == "innerhtml" || name == "outerhtml" || name == "srcdoc" || strings.HasPrefix(name, "on")
}

func dangerousURL(name, value string) bool {
	switch strings.ToLower(name) {
	case "href", "src", "action", "formaction", "xlink:href":
		value = strings.Map(func(r rune) rune {
			if r <= 32 {
				return -1
			}
			return r
		}, strings.ToLower(value))
		return strings.HasPrefix(value, "javascript:") || strings.HasPrefix(value, "vbscript:") || strings.HasPrefix(value, "data:text/html")
	}
	return false
}

func (g *generator) element(n Node, parent, scope string, depth int) {
	if n.Tag == "" {
		g.error(n, "F2005", "Element requires a tag name")
		return
	}
	if ctor, ok := g.components[n.Tag]; ok {
		g.component(n, ctor, parent, scope)
		return
	}
	switch strings.ToLower(n.Tag) {
	case "script", "iframe", "object", "embed", "style", "base", "link", "meta":
		g.error(n, "F2006", "Unsupported active-content element: "+n.Tag)
		return
	}
	element := g.variable()
	g.line("const %s=__f.element(%s,%s,%s,%s);", element, scope, parent, js(n.Tag), js(g.scopeID))
	for _, attr := range n.Attributes {
		g.mark(attr.Start)
		kind, name := attributeKind(attr.Name)
		if name == "" || kind == "static" && strings.ContainsAny(name, "[]()") {
			g.error(n, "F2007", "Malformed binding: "+attr.Name)
			continue
		}
		if kind != "event" && dangerousProperty(name) {
			g.error(n, "F2008", "Unsafe property or inline event attribute: "+name)
			continue
		}
		switch kind {
		case "static":
			if dangerousURL(name, attr.Value) {
				g.error(n, "F2009", "Unsafe URL in "+name)
				continue
			}
			g.line("%s.setAttribute(%s,%s);", element, js(name), mapped(js(attr.Value), attr.ValueStart))
		case "property":
			g.line("__f.bind(%s,()=>__f.setProperty(%s,%s,(%s)));", scope, element, js(name), mapped(g.expression(n, attr.Value), attr.ValueStart))
		case "event":
			g.line("__f.listen(%s,%s,%s,($event)=>{%s;});", scope, element, js(name), mapped(g.expression(n, attr.Value), attr.ValueStart))
		case "twoway":
			valid := strings.EqualFold(n.Tag, "input") && name == "value"
			for _, other := range n.Attributes {
				if strings.EqualFold(other.Name, "type") && !strings.EqualFold(other.Value, "text") ||
					other.Name == "[type]" || other.Name == "[value]" || other.Name == "value" {
					valid = false
				}
			}
			if !valid {
				g.error(n, "F2010", "Two-way binding supports only [(value)] on text input without another value binding or dynamic type")
				continue
			}
			expr := mapped(g.expression(n, attr.Value), attr.ValueStart)
			g.line("__f.bind(%s,()=>__f.setProperty(%s,\"value\",(%s)()));", scope, element, expr)
			g.line("__f.listen(%s,%s,\"input\",($event)=>{(%s).set($event.target.value);});", scope, element, expr)
		}
	}
	g.nodes(n.Children, element, scope, depth+1)
}

func (g *generator) component(n Node, ctor, parent, scope string) {
	if !identifier(ctor) {
		g.error(n, "F2011", "Component constructor must be a JavaScript identifier")
		return
	}
	for _, child := range n.Children {
		if child.Kind != "text" || strings.TrimSpace(child.Text) != "" {
			g.error(n, "F2012", "Content projection is not supported on child components")
			break
		}
	}
	var inputs, outputs []Attribute
	for _, attr := range n.Attributes {
		kind, name := attributeKind(attr.Name)
		if name == "" || kind == "static" && strings.ContainsAny(name, "[]()") {
			g.error(n, "F2007", "Malformed component binding: "+attr.Name)
			continue
		}
		switch kind {
		case "property", "static":
			inputs = append(inputs, attr)
		case "event":
			outputs = append(outputs, attr)
		case "twoway":
			g.error(n, "F2010", "Two-way binding is supported only on text input, not child components")
		}
	}
	g.write("__f.mountChild(%s,%s,%s,{", scope, parent, g.constructors[n.Tag])
	for i, attr := range inputs {
		if i > 0 {
			g.write(",")
		}
		g.mark(attr.Start)
		kind, name := attributeKind(attr.Name)
		if kind == "property" {
			g.write("%s:()=> (%s)", js(name), mapped(g.expression(n, attr.Value), attr.ValueStart))
		} else {
			g.write("%s:()=> %s", js(name), mapped(js(attr.Value), attr.ValueStart))
		}
	}
	g.write("},{")
	for i, attr := range outputs {
		if i > 0 {
			g.write(",")
		}
		g.mark(attr.Start)
		_, name := attributeKind(attr.Name)
		g.write("%s:($event)=>{%s;}", js(name), mapped(g.expression(n, attr.Value), attr.ValueStart))
	}
	g.line("});")
}
