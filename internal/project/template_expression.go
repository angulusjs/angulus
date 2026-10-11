package project

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type expressionToken struct {
	text       string
	start, end int
}

type expressionLexer struct {
	source string
	pos    int
}

func (l *expressionLexer) next() (expressionToken, error) {
	for l.pos < len(l.source) {
		r, size := utf8.DecodeRuneInString(l.source[l.pos:])
		if unicode.IsSpace(r) {
			l.pos += size
			continue
		}
		if strings.HasPrefix(l.source[l.pos:], "//") {
			if end := strings.IndexByte(l.source[l.pos:], '\n'); end >= 0 {
				l.pos += end + 1
			} else {
				l.pos = len(l.source)
			}
			continue
		}
		if strings.HasPrefix(l.source[l.pos:], "/*") {
			start := l.pos
			end := strings.Index(l.source[l.pos+2:], "*/")
			if end < 0 {
				return expressionToken{}, fmt.Errorf("unterminated comment at byte %d", start)
			}
			l.pos += end + 4
			continue
		}
		break
	}
	if l.pos == len(l.source) {
		return expressionToken{start: l.pos, end: l.pos}, nil
	}

	start := l.pos
	r, size := utf8.DecodeRuneInString(l.source[l.pos:])
	if r == '\'' || r == '"' || r == '`' {
		l.pos += size
		interpolation := false
		closed := false
		for l.pos < len(l.source) {
			current := l.source[l.pos]
			if current == '\\' {
				l.pos++
				if l.pos < len(l.source) {
					_, escapedSize := utf8.DecodeRuneInString(l.source[l.pos:])
					l.pos += escapedSize
				}
				continue
			}
			if r == '`' && strings.HasPrefix(l.source[l.pos:], "${") {
				interpolation = true
			}
			_, currentSize := utf8.DecodeRuneInString(l.source[l.pos:])
			l.pos += currentSize
			if rune(current) == r {
				closed = true
				break
			}
		}
		if !closed {
			return expressionToken{}, fmt.Errorf("unterminated string literal at byte %d", start)
		}
		if interpolation {
			return expressionToken{}, fmt.Errorf("template-literal interpolation is not supported at byte %d", start)
		}
		return expressionToken{text: l.source[start:l.pos], start: start, end: l.pos}, nil
	}
	if expressionIdentifierStart(r) {
		l.pos += size
		for l.pos < len(l.source) {
			next, nextSize := utf8.DecodeRuneInString(l.source[l.pos:])
			if !expressionIdentifierPart(next) {
				break
			}
			l.pos += nextSize
		}
		return expressionToken{text: l.source[start:l.pos], start: start, end: l.pos}, nil
	}
	if unicode.IsDigit(r) || r == '.' && l.pos+1 < len(l.source) && l.source[l.pos+1] >= '0' && l.source[l.pos+1] <= '9' {
		l.pos += size
		for l.pos < len(l.source) {
			c := l.source[l.pos]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' ||
				c == 'x' || c == 'X' || c == 'b' || c == 'B' || c == 'o' || c == 'O' ||
				c == 'n' || c == '_' || c == '.' || c == '+' || c == '-') {
				break
			}
			if (c == '+' || c == '-') && l.pos > start && l.source[l.pos-1] != 'e' && l.source[l.pos-1] != 'E' {
				break
			}
			l.pos++
		}
		return expressionToken{text: l.source[start:l.pos], start: start, end: l.pos}, nil
	}
	for _, operator := range []string{"===", "!==", "?.", "=>", "==", "!=", "<=", ">=", "&&", "||", "??", "**"} {
		if strings.HasPrefix(l.source[l.pos:], operator) {
			l.pos += len(operator)
			return expressionToken{text: operator, start: start, end: l.pos}, nil
		}
	}
	l.pos += size
	if strings.ContainsRune("()[]{}.,?:+-*/%<>!~", r) {
		return expressionToken{text: l.source[start:l.pos], start: start, end: l.pos}, nil
	}
	return expressionToken{}, fmt.Errorf("unsupported token %q at byte %d", r, start)
}

func expressionIdentifierStart(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r)
}

func expressionIdentifierPart(r rune) bool {
	return expressionIdentifierStart(r) || unicode.IsDigit(r)
}

type expressionRewrite struct {
	start, end int
	text       string
}

type expressionOffsetMapping struct {
	Generated    int
	Source       int
	generatedEnd int
	sourceEnd    int
}

type expressionParser struct {
	source   string
	lexer    expressionLexer
	current  expressionToken
	locals   map[string]bool
	runtime  bool
	event    bool
	rewrites []expressionRewrite
	parseErr error
}

func rewriteExpression(source string, locals map[string]bool, runtime, event bool) (string, int, error) {
	result, _, offset, err := rewriteExpressionWithOffsets(source, locals, runtime, event)
	return result, offset, err
}

func rewriteExpressionWithOffsets(source string, locals map[string]bool, runtime, event bool) (string, []expressionOffsetMapping, int, error) {
	p := &expressionParser{
		source: source, lexer: expressionLexer{source: source}, locals: copyLocals(locals),
		runtime: runtime, event: event,
	}
	if err := p.advance(); err != nil {
		return "", nil, expressionErrorOffset(err), err
	}
	if p.current.text == "" {
		err := fmt.Errorf("expected expression at byte 0")
		return "", nil, 0, err
	}
	if err := p.parseExpression(0); err != nil {
		return "", nil, expressionErrorOffset(err), err
	}
	if p.current.text != "" {
		err := fmt.Errorf("unexpected token %q at byte %d", p.current.text, p.current.start)
		return "", nil, p.current.start, err
	}
	var out strings.Builder
	mappings := make([]expressionOffsetMapping, 0, len(p.rewrites))
	last := 0
	for _, rewrite := range p.rewrites {
		if rewrite.start < last {
			continue
		}
		out.WriteString(source[last:rewrite.start])
		generated := out.Len()
		out.WriteString(rewrite.text)
		mappings = append(mappings, expressionOffsetMapping{
			Generated: generated, Source: rewrite.start,
			generatedEnd: out.Len(), sourceEnd: rewrite.end,
		})
		last = rewrite.end
	}
	out.WriteString(source[last:])
	return out.String(), mappings, 0, nil
}

// rewriteExpressionWithUTF16Offsets returns an original UTF-16 offset for each
// generated UTF-16 code unit, plus the exclusive end position. Copied tokens
// retain exact positions; inserted qualification/signal syntax anchors to the
// identifier it belongs to. The diagnostic offset remains a source byte offset.
func rewriteExpressionWithUTF16Offsets(source string, locals map[string]bool, runtime, event bool) (string, []int, int, error) {
	rewritten, mappings, offset, err := rewriteExpressionWithOffsets(source, locals, runtime, event)
	if err != nil {
		return "", nil, offset, err
	}
	sourceUnits := utf16ByteOffsets(source)
	generatedUnits := utf16ByteOffsets(rewritten)
	offsets := make([]int, utf16Length(rewritten)+1)
	copySpan := func(generatedStart, sourceStart, length int) {
		for i := 0; i < length; i++ {
			offsets[generatedUnits[generatedStart+i]] = sourceUnits[sourceStart+i]
		}
	}
	lastGenerated, lastSource := 0, 0
	for _, mapping := range mappings {
		copySpan(lastGenerated, lastSource, mapping.Source-lastSource)
		for i := generatedUnits[mapping.Generated]; i < generatedUnits[mapping.generatedEnd]; i++ {
			offsets[i] = sourceUnits[mapping.Source]
		}
		originalToken := source[mapping.Source:mapping.sourceEnd]
		replacement := rewritten[mapping.Generated:mapping.generatedEnd]
		// Shorthand expansion may contain both the property key and its value.
		for start := 0; start < len(replacement); {
			match := strings.Index(replacement[start:], originalToken)
			if match < 0 {
				break
			}
			match += start
			copySpan(mapping.Generated+match, mapping.Source, len(originalToken))
			start = match + len(originalToken)
		}
		lastGenerated, lastSource = mapping.generatedEnd, mapping.sourceEnd
	}
	copySpan(lastGenerated, lastSource, len(source)-lastSource)
	offsets[len(offsets)-1] = utf16Length(source)
	// A supplementary rune occupies two code units at the same source token.
	for byteOffset, r := range rewritten {
		if r > 0xffff {
			unit := generatedUnits[byteOffset]
			offsets[unit+1] = offsets[unit] + 1
		}
	}
	return rewritten, offsets, 0, nil
}

func expressionErrorOffset(err error) int {
	message := err.Error()
	at := strings.LastIndex(message, " at byte ")
	if at < 0 {
		return 0
	}
	offset, parseErr := strconv.Atoi(message[at+9:])
	if parseErr != nil {
		return 0
	}
	return offset
}

func (p *expressionParser) advance() error {
	token, err := p.lexer.next()
	if err != nil {
		p.parseErr = err
		return err
	}
	p.current = token
	return nil
}

func (p *expressionParser) take(text string) (bool, error) {
	if p.current.text != text {
		return false, nil
	}
	return true, p.advance()
}

func (p *expressionParser) require(text string) error {
	if p.current.text != text {
		return fmt.Errorf("expected %q at byte %d", text, p.current.start)
	}
	return p.advance()
}

func (p *expressionParser) parseExpression(minimum int) error {
	if err := p.parsePrefix(); err != nil {
		return err
	}
	for {
		if p.current.text == "." || p.current.text == "?." || p.current.text == "[" || p.current.text == "(" {
			if 16 < minimum {
				return nil
			}
			switch p.current.text {
			case ".", "?.":
				optional := p.current.text == "?."
				if err := p.advance(); err != nil {
					return err
				}
				if optional && (p.current.text == "[" || p.current.text == "(") {
					if p.current.text == "[" {
						if err := p.advance(); err != nil {
							return err
						}
						if err := p.parseExpression(0); err != nil {
							return err
						}
						if err := p.require("]"); err != nil {
							return err
						}
					} else if err := p.parseArguments(); err != nil {
						return err
					}
					continue
				}
				if p.current.text == "" || !expressionIdentifierStart([]rune(p.current.text)[0]) {
					return fmt.Errorf("expected property name at byte %d", p.current.start)
				}
				if err := p.advance(); err != nil {
					return err
				}
			case "[":
				if err := p.advance(); err != nil {
					return err
				}
				if err := p.parseExpression(0); err != nil {
					return err
				}
				if err := p.require("]"); err != nil {
					return err
				}
			case "(":
				if err := p.parseArguments(); err != nil {
					return err
				}
			}
			continue
		}
		if p.current.text == "?" && minimum <= 1 {
			if err := p.advance(); err != nil {
				return err
			}
			if err := p.parseExpression(0); err != nil {
				return err
			}
			if err := p.require(":"); err != nil {
				return err
			}
			if err := p.parseExpression(1); err != nil {
				return err
			}
			continue
		}
		precedence := expressionPrecedence(p.current.text)
		if precedence < minimum {
			return nil
		}
		operator := p.current.text
		if err := p.advance(); err != nil {
			return err
		}
		nextMinimum := precedence + 1
		if operator == "**" {
			nextMinimum = precedence
		}
		if err := p.parseExpression(nextMinimum); err != nil {
			return err
		}
	}
}

func expressionPrecedence(operator string) int {
	switch operator {
	case "??":
		return 2
	case "||":
		return 3
	case "&&":
		return 4
	case "==", "!=", "===", "!==":
		return 8
	case "<", "<=", ">", ">=", "in":
		return 9
	case "+", "-":
		return 11
	case "*", "/", "%":
		return 12
	case "**":
		return 13
	default:
		return -1
	}
}

func (p *expressionParser) parsePrefix() error {
	token := p.current
	if token.text == "!" || token.text == "~" || token.text == "+" || token.text == "-" || token.text == "typeof" {
		if err := p.advance(); err != nil {
			return err
		}
		return p.parseExpression(14)
	}
	if token.text == "this" || token.text == "function" || token.text == "new" || token.text == "await" || token.text == "yield" ||
		token.text == "delete" || token.text == "void" || token.text == "class" {
		return fmt.Errorf("unsupported expression %q at byte %d", token.text, token.start)
	}
	if token.text == "(" {
		return p.parseParenthesized()
	}
	if token.text == "[" {
		if err := p.advance(); err != nil {
			return err
		}
		for p.current.text != "]" {
			if p.current.text == "" {
				return fmt.Errorf("expected ']' at byte %d", p.current.start)
			}
			if p.current.text == "," {
				return fmt.Errorf("array elisions are not supported at byte %d", p.current.start)
			}
			if err := p.parseExpression(0); err != nil {
				return err
			}
			if p.current.text != "," {
				break
			}
			if err := p.advance(); err != nil {
				return err
			}
		}
		return p.require("]")
	}
	if token.text == "{" {
		return p.parseObject()
	}
	if isExpressionLiteral(token.text) {
		return p.advance()
	}
	if !expressionIdentifierStart(firstRune(token.text)) {
		return fmt.Errorf("expected expression at byte %d", token.start)
	}
	if token.text == "true" || token.text == "false" || token.text == "null" {
		return p.advance()
	}
	if expressionReservedWord(token.text) {
		return fmt.Errorf("unsupported expression keyword %q at byte %d", token.text, token.start)
	}
	if err := p.advance(); err != nil {
		return err
	}
	p.rewrites = append(p.rewrites, expressionRewrite{token.start, token.end, p.reference(token.text)})
	return nil
}

func expressionReservedWord(value string) bool {
	switch value {
	case "break", "case", "catch", "const", "continue", "debugger", "default", "do", "else",
		"enum", "export", "extends", "finally", "for", "if", "implements", "import",
		"in", "instanceof", "interface", "let", "package", "private", "protected",
		"public", "return", "static", "super", "switch", "throw", "try", "var", "while", "with":
		return true
	default:
		return false
	}
}

func firstRune(text string) rune {
	r, _ := utf8.DecodeRuneInString(text)
	return r
}

func isExpressionLiteral(value string) bool {
	if value == "" {
		return false
	}
	first := value[0]
	if first == '\'' || first == '"' || first == '`' {
		return true
	}
	if first >= '0' && first <= '9' || first == '.' && len(value) > 1 && value[1] >= '0' && value[1] <= '9' {
		return true
	}
	return false
}

func (p *expressionParser) parseParenthesized() error {
	if err := p.advance(); err != nil {
		return err
	}
	if err := p.parseExpression(0); err != nil {
		return err
	}
	return p.require(")")
}

func (p *expressionParser) parseArguments() error {
	if err := p.advance(); err != nil {
		return err
	}
	for p.current.text != ")" {
		if p.current.text == "" {
			return fmt.Errorf("expected ')' at byte %d", p.current.start)
		}
		if err := p.parseExpression(0); err != nil {
			return err
		}
		if p.current.text != "," {
			break
		}
		if err := p.advance(); err != nil {
			return err
		}
	}
	return p.require(")")
}

func (p *expressionParser) parseObject() error {
	if err := p.advance(); err != nil {
		return err
	}
	for p.current.text != "}" {
		if p.current.text == "" {
			return fmt.Errorf("expected '}' at byte %d", p.current.start)
		}
		key := p.current
		if !expressionIdentifierStart(firstRune(key.text)) && !isExpressionLiteral(key.text) {
			return fmt.Errorf("object expressions support explicit properties and shorthand only at byte %d", key.start)
		}
		if err := p.advance(); err != nil {
			return err
		}
		if p.current.text == ":" {
			if err := p.advance(); err != nil {
				return err
			}
			if err := p.parseExpression(0); err != nil {
				return err
			}
		} else {
			if !expressionIdentifierStart(firstRune(key.text)) {
				return fmt.Errorf("object shorthand must use an identifier at byte %d", key.start)
			}
			value := p.reference(key.text)
			p.rewrites = append(p.rewrites, expressionRewrite{key.start, key.end, key.text + ": " + value})
		}
		if p.current.text != "," {
			break
		}
		if err := p.advance(); err != nil {
			return err
		}
	}
	return p.require("}")
}

func (p *expressionParser) reference(name string) string {
	if name == "undefined" || name == "NaN" || name == "Infinity" || name == "true" || name == "false" || name == "null" ||
		name == "$event" && p.event {
		return name
	}
	if p.locals[name] {
		if p.runtime {
			return name + "()"
		}
		return name
	}
	if _, ok := p.locals[name]; ok {
		return name
	}
	return "ctx." + name
}
