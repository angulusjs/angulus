package compiler

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

type parser struct {
	source, file string
	pos          int
	offsets      []int
	diagnostics  []Diagnostic
	depth        int
}

// Parse uses byte indices internally and converts every public position to UTF-16.
func Parse(source, file string) ParseResult {
	p := &parser{source: source, file: file, diagnostics: []Diagnostic{}, offsets: make([]int, len(source)+1)}
	units := 0
	for i, r := range source {
		size := utf8.RuneLen(r)
		if size < 1 {
			size = 1
		}
		for j := 0; j < size && i+j < len(source); j++ {
			p.offsets[i+j] = units
		}
		units++
		if r > 0xffff {
			units++
		}
	}
	p.offsets[len(source)] = units
	nodes := p.nodes("", false)
	return ParseResult{Nodes: nodes, Diagnostics: p.diagnostics}
}

func (p *parser) offset(i int) int {
	if i < 0 {
		i = 0
	}
	if i > len(p.source) {
		i = len(p.source)
	}
	return p.offsets[i]
}

func (p *parser) error(start, end int, code, message string) {
	p.diagnostics = append(p.diagnostics, Diagnostic{p.file, p.offset(start), p.offset(end), code, message, "error"})
}

func (p *parser) has(s string) bool { return strings.HasPrefix(p.source[p.pos:], s) }
func (p *parser) space() {
	for p.pos < len(p.source) {
		r, size := utf8.DecodeRuneInString(p.source[p.pos:])
		if !unicode.IsSpace(r) {
			break
		}
		p.pos += size
	}
}

func nameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':' || c == '.'
}

func (p *parser) directive(s string) bool {
	if !p.has(s) {
		return false
	}
	end := p.pos + len(s)
	return end == len(p.source) || !nameChar(p.source[end])
}

func (p *parser) nodes(tag string, block bool) []Node {
	result := []Node{}
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 256 {
		p.error(p.pos, p.pos, "F1000", "Template nesting exceeds 256 levels")
		p.pos = len(p.source)
		return result
	}
	for p.pos < len(p.source) {
		start := p.pos
		if p.has("</") {
			p.pos += 2
			nameStart := p.pos
			for p.pos < len(p.source) && nameChar(p.source[p.pos]) {
				p.pos++
			}
			name := p.source[nameStart:p.pos]
			p.space()
			if p.has(">") {
				p.pos++
			} else {
				p.error(start, p.pos, "F1001", "Expected '>' after closing tag")
			}
			if name != tag || tag == "" {
				p.error(start, p.pos, "F1002", "Unexpected closing tag </"+name+">")
			}
			if tag != "" {
				return result
			}
			continue
		}
		if block && p.has("}") {
			p.pos++
			return result
		}
		switch {
		case p.has("<!--"):
			end := strings.Index(p.source[p.pos+4:], "-->")
			if end < 0 {
				p.error(start, len(p.source), "F1003", "Unterminated HTML comment")
				p.pos = len(p.source)
			} else {
				p.pos += 4 + end + 3
			}
		case p.has("{{"):
			p.pos += 2
			expr, exprStart := p.expression("}}")
			result = append(result, Node{Kind: "interpolation", Start: p.offset(start), End: p.offset(p.pos), Expression: expr, ExprStart: p.offset(exprStart)})
		case p.has("<") && p.pos+1 < len(p.source) && nameChar(p.source[p.pos+1]):
			result = append(result, p.element())
		case p.directive("@if"):
			result = append(result, p.ifNode())
		case p.directive("@for"):
			result = append(result, p.forNode())
		case p.directive("@switch"):
			result = append(result, p.switchNode())
		case p.directive("@else") || p.directive("@case") || p.directive("@default"):
			p.pos++
			p.error(start, p.pos, "F1004", "Unexpected control-flow directive")
		default:
			p.pos++
			for p.pos < len(p.source) && !p.has("{{") && !p.has("<!--") && !p.has("</") &&
				!(p.has("<") && p.pos+1 < len(p.source) && nameChar(p.source[p.pos+1])) &&
				!(block && p.has("}")) && !p.directive("@if") && !p.directive("@for") &&
				!p.directive("@switch") && !p.directive("@else") && !p.directive("@case") && !p.directive("@default") {
				p.pos++
			}
			result = append(result, Node{Kind: "text", Start: p.offset(start), End: p.offset(p.pos), Text: html.UnescapeString(p.source[start:p.pos])})
		}
	}
	if tag != "" {
		p.error(p.pos, p.pos, "F1005", "Unclosed element <"+tag+">")
	}
	if block {
		p.error(p.pos, p.pos, "F1006", "Expected '}' to close control-flow block")
	}
	return result
}

var voidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "param": true, "source": true,
	"track": true, "wbr": true,
}

func (p *parser) element() Node {
	start := p.pos
	p.pos++
	tagStart := p.pos
	for p.pos < len(p.source) && nameChar(p.source[p.pos]) {
		p.pos++
	}
	n := Node{Kind: "element", Start: p.offset(start), Tag: p.source[tagStart:p.pos]}
	seen := map[string]bool{}
	closed, selfClosing := false, false
	for p.pos < len(p.source) {
		p.space()
		if p.has("/>") {
			p.pos += 2
			closed, selfClosing = true, true
			break
		}
		if p.has(">") {
			p.pos++
			closed = true
			break
		}
		attrStart := p.pos
		for p.pos < len(p.source) && !strings.ContainsRune(" \t\r\n=/>", rune(p.source[p.pos])) {
			p.pos++
		}
		name := p.source[attrStart:p.pos]
		if name == "" {
			p.error(attrStart, p.pos, "F1007", "Expected attribute name")
			if p.pos < len(p.source) {
				p.pos++
			}
			continue
		}
		a := Attribute{Name: name, Start: p.offset(attrStart), ValueStart: p.offset(p.pos)}
		p.space()
		if p.has("=") {
			p.pos++
			p.space()
			if p.pos < len(p.source) && (p.source[p.pos] == '"' || p.source[p.pos] == '\'') {
				quote := p.source[p.pos]
				p.pos++
				valueStart := p.pos
				for p.pos < len(p.source) && p.source[p.pos] != quote {
					p.pos++
				}
				a.Value = html.UnescapeString(p.source[valueStart:p.pos])
				a.ValueStart = p.offset(valueStart)
				if p.pos == len(p.source) {
					p.error(valueStart-1, p.pos, "F1008", "Unterminated attribute value")
				} else {
					p.pos++
				}
			} else {
				valueStart := p.pos
				for p.pos < len(p.source) && !strings.ContainsRune(" \t\r\n>", rune(p.source[p.pos])) && !p.has("/>") {
					p.pos++
				}
				a.Value, a.ValueStart = html.UnescapeString(p.source[valueStart:p.pos]), p.offset(valueStart)
				if valueStart == p.pos {
					p.error(valueStart, p.pos, "F1009", "Expected attribute value")
				}
			}
		}
		if seen[name] {
			p.error(attrStart, p.pos, "F1010", "Duplicate attribute "+name)
		}
		seen[name] = true
		if (strings.HasPrefix(name, "[") || strings.HasPrefix(name, "(")) && strings.TrimSpace(a.Value) == "" {
			p.error(attrStart, p.pos, "F1011", "Binding requires an expression")
		}
		n.Attributes = append(n.Attributes, a)
	}
	if !closed {
		p.error(start, p.pos, "F1012", "Unterminated opening tag")
	} else if !selfClosing && !voidTags[strings.ToLower(n.Tag)] {
		n.Children = p.nodes(n.Tag, false)
	}
	n.End = p.offset(p.pos)
	return n
}

// expression scans nested delimiters, quoted strings, and comments, rather than
// interpreting JavaScript. The TypeScript frontend owns expression validation.
func (p *parser) expression(terminator string) (string, int) {
	start := p.pos
	var stack []byte
	for p.pos < len(p.source) {
		if len(stack) == 0 && p.has(terminator) {
			end := p.pos
			p.pos += len(terminator)
			value, valueStart := trimExpression(p.source[start:end], start)
			if value == "" {
				p.error(start, end, "F1013", "Expected expression")
			}
			return value, valueStart
		}
		c := p.source[p.pos]
		if c == '\'' || c == '"' || c == '`' {
			p.quote(c)
			continue
		}
		if p.skipJSComment() {
			continue
		}
		switch c {
		case '(', '[', '{':
			stack = append(stack, c)
		case ')', ']', '}':
			if len(stack) == 0 || !matching(stack[len(stack)-1], c) {
				p.error(p.pos, p.pos+1, "F1014", "Unbalanced expression delimiter")
			} else {
				stack = stack[:len(stack)-1]
			}
		}
		p.pos++
	}
	p.error(start, p.pos, "F1015", "Unterminated expression; expected "+terminator)
	value, valueStart := trimExpression(p.source[start:p.pos], start)
	return value, valueStart
}

func matching(open, close byte) bool {
	return open == '(' && close == ')' || open == '[' && close == ']' || open == '{' && close == '}'
}

func (p *parser) quote(quote byte) {
	p.pos++
	for p.pos < len(p.source) {
		c := p.source[p.pos]
		p.pos++
		if c == '\\' && p.pos < len(p.source) {
			p.pos++
		} else if c == quote {
			return
		} else if quote == '`' && c == '$' && p.has("{") {
			p.pos++
			depth := 1
			for p.pos < len(p.source) && depth > 0 {
				c = p.source[p.pos]
				if c == '\'' || c == '"' || c == '`' {
					p.quote(c)
					continue
				}
				if p.skipJSComment() {
					continue
				}
				if c == '{' {
					depth++
				} else if c == '}' {
					depth--
				}
				p.pos++
			}
		}
	}
}

func (p *parser) skipJSComment() bool {
	if p.has("//") {
		for p.pos < len(p.source) && p.source[p.pos] != '\n' {
			p.pos++
		}
		return true
	}
	if p.has("/*") {
		end := strings.Index(p.source[p.pos+2:], "*/")
		if end < 0 {
			p.pos = len(p.source)
		} else {
			p.pos += 2 + end + 2
		}
		return true
	}
	return false
}

func trimExpression(value string, start int) (string, int) {
	left := strings.TrimLeftFunc(value, unicode.IsSpace)
	return strings.TrimSpace(left), start + len(value) - len(left)
}

func (p *parser) parens() (string, int) {
	p.space()
	if !p.has("(") {
		p.error(p.pos, p.pos, "F1016", "Expected '('")
		return "", p.pos
	}
	p.pos++
	return p.expression(")")
}

func (p *parser) block() []Node {
	p.space()
	if !p.has("{") {
		p.error(p.pos, p.pos, "F1017", "Expected '{'")
		return []Node{}
	}
	p.pos++
	return p.nodes("", true)
}

func (p *parser) ifNode() Node {
	start := p.pos
	p.pos += len("@if")
	return p.ifBody(start)
}

func (p *parser) ifBody(start int) Node {
	expr, exprStart := p.parens()
	n := Node{Kind: "if", Start: p.offset(start), Expression: expr, ExprStart: p.offset(exprStart)}
	n.Children = p.block()
	saved := p.pos
	p.space()
	if p.directive("@else") {
		p.pos += len("@else")
		p.space()
		if p.directive("if") {
			ifStart := p.pos
			p.pos += len("if")
			p.depth++
			if p.depth > 256 {
				p.error(ifStart, p.pos, "F1000", "Template nesting exceeds 256 levels")
				p.pos = len(p.source)
			} else {
				n.Otherwise = []Node{p.ifBody(ifStart)}
			}
			p.depth--
		} else {
			n.Otherwise = p.block()
		}
	} else {
		p.pos = saved
	}
	n.End = p.offset(p.pos)
	return n
}

// topLevelParts splits only separators outside JavaScript nesting and quotes.
func topLevelParts(value string, separator byte) []int {
	var parts []int
	var stack []byte
	for i := 0; i < len(value); i++ {
		c := value[i]
		scanner := parser{source: value, pos: i}
		if c == '\'' || c == '"' || c == '`' {
			scanner.quote(c)
			i = scanner.pos - 1
			continue
		}
		if scanner.skipJSComment() {
			i = scanner.pos - 1
			continue
		}
		switch c {
		case '(', '[', '{':
			stack = append(stack, c)
		case ')', ']', '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			if c == separator && len(stack) == 0 {
				parts = append(parts, i)
			}
		}
	}
	return parts
}

func identifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if !(unicode.IsLetter(r) || r == '_' || r == '$' || i > 0 && unicode.IsDigit(r)) {
			return false
		}
	}
	switch value {
	case "if", "else", "for", "while", "do", "switch", "case", "default", "return", "throw",
		"new", "class", "function", "var", "let", "const", "this", "super", "null", "true",
		"false", "delete", "typeof", "void", "in", "instanceof", "await", "yield", "import",
		"export", "try", "catch", "finally", "break", "continue", "with", "debugger":
		return false
	}
	return true
}

func (p *parser) forNode() Node {
	start := p.pos
	p.pos += len("@for")
	header, headerStart := p.parens()
	n := Node{Kind: "for", Start: p.offset(start)}
	parts := topLevelParts(header, ';')
	if len(parts) != 1 {
		p.error(headerStart, headerStart+len(header), "F1018", "@for requires 'item of expression; track expression'")
	} else {
		binding := header[:parts[0]]
		i := 0
		for i < len(binding) && !unicode.IsSpace(rune(binding[i])) {
			i++
		}
		n.Item = binding[:i]
		rest, restStart := trimExpression(binding[i:], headerStart+i)
		if !identifier(n.Item) || !strings.HasPrefix(rest, "of") || len(rest) < 3 || !unicode.IsSpace(rune(rest[2])) {
			p.error(headerStart, headerStart+len(binding), "F1019", "@for requires a single identifier followed by 'of'")
		} else {
			expr, exprStart := trimExpression(rest[2:], restStart+2)
			n.Expression, n.ExprStart = expr, p.offset(exprStart)
			if expr == "" {
				p.error(exprStart, exprStart, "F1013", "Expected collection expression")
			}
		}
		track, trackStart := trimExpression(header[parts[0]+1:], headerStart+parts[0]+1)
		if !strings.HasPrefix(track, "track") || len(track) < 6 || !unicode.IsSpace(rune(track[5])) {
			p.error(trackStart, trackStart+len(track), "F1020", "@for requires a track expression")
		} else {
			expr, exprStart := trimExpression(track[5:], trackStart+5)
			n.Track, n.TrackStart = expr, p.offset(exprStart)
			if expr == "" {
				p.error(exprStart, exprStart, "F1020", "@for requires a track expression")
			}
		}
	}
	n.Children = p.block()
	n.End = p.offset(p.pos)
	return n
}

func (p *parser) switchNode() Node {
	start := p.pos
	p.pos += len("@switch")
	expr, exprStart := p.parens()
	n := Node{Kind: "switch", Start: p.offset(start), Expression: expr, ExprStart: p.offset(exprStart), Cases: []Case{}}
	p.space()
	if !p.has("{") {
		p.error(p.pos, p.pos, "F1017", "Expected '{'")
		n.End = p.offset(p.pos)
		return n
	}
	p.pos++
	hasDefault := false
	for p.pos < len(p.source) {
		p.space()
		if p.has("}") {
			p.pos++
			n.End = p.offset(p.pos)
			return n
		}
		c := Case{Children: []Node{}}
		switch {
		case p.directive("@case"):
			if hasDefault {
				p.error(p.pos, p.pos+5, "F1021", "@default must be the last switch case")
			}
			p.pos += len("@case")
			value, valueStart := p.parens()
			c.Expression, c.ExprStart = value, p.offset(valueStart)
		case p.directive("@default"):
			if hasDefault {
				p.error(p.pos, p.pos+8, "F1022", "Duplicate @default")
			}
			hasDefault = true
			p.pos += len("@default")
		default:
			if p.pos < len(p.source) {
				p.error(p.pos, p.pos+1, "F1023", "Expected @case or @default")
				p.pos++
			}
			continue
		}
		c.Children = p.block()
		n.Cases = append(n.Cases, c)
	}
	p.error(p.pos, p.pos, "F1006", "Expected '}' to close @switch")
	n.End = p.offset(p.pos)
	return n
}
