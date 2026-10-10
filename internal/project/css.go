package project

import (
	"encoding/base64"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	parse "github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/css"
)

// scopeCSS reads styles in the supplied order and returns their scoped,
// concatenated CSS. scopeID is the complete attribute name (for example,
// "data-a-0123456789"). Relative url() resources are embedded as data URIs,
// so the returned stylesheet is independent of the output stylesheet's
// location. Absolute, fragment-only, and non-file URLs are left unchanged.
func scopeCSS(styles []string, scopeID string) (string, []Diagnostic) {
	css, _, _, diagnostics := scopeCSSWithMappings(styles, scopeID)
	return css, diagnostics
}

type cssSourceMapping struct {
	Generated int
	File      string
	Source    int
}

func scopeCSSWithMappings(styles []string, scopeID string) (string, []cssSourceMapping, map[string]string, []Diagnostic) {
	var diagnostics []Diagnostic
	if scopeID == "" || strings.ContainsAny(scopeID, "[]\"' \t\r\n") {
		diagnostics = append(diagnostics, Diagnostic{
			Code: "P0308", Message: "scope ID must be a non-empty CSS attribute name", Severity: "error",
		})
		return "", nil, nil, diagnostics
	}

	sheets := make([]cssSheet, 0, len(styles))
	sourceContents := make(map[string]string, len(styles))
	keyframes := make(map[string]string)
	for _, path := range styles {
		source, err := os.ReadFile(path)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{File: path, Code: "P0301", Message: "cannot read stylesheet: " + err.Error(), Severity: "error"})
			continue
		}
		sourceContents[path] = string(source)
		sheet := parseCSS(path, string(source), &diagnostics)
		for _, event := range sheet.events {
			if !isKeyframesRule(event) || event.grammar != css.BeginAtRuleGrammar {
				continue
			}
			if name, _, ok := keyframeName(event.values); ok {
				if _, exists := keyframes[name]; !exists {
					keyframes[name] = name + "-" + scopeID
				}
			}
		}
		sheets = append(sheets, sheet)
	}

	var output strings.Builder
	var mappings []cssSourceMapping
	for _, sheet := range sheets {
		output.WriteString(renderCSSMapped(sheet, scopeID, keyframes, &diagnostics, output.Len(), &mappings))
	}
	scoped := output.String()
	generatedOffsets := utf16ByteOffsets(scoped)
	sourceOffsets := make(map[string][]int, len(sourceContents))
	for path, source := range sourceContents {
		sourceOffsets[path] = utf16ByteOffsets(source)
	}
	for i := range mappings {
		mapping := &mappings[i]
		mapping.Generated = generatedOffsets[mapping.Generated]
		mapping.Source = sourceOffsets[mapping.File][mapping.Source]
	}
	return scoped, mappings, sourceContents, diagnostics
}

type cssEvent struct {
	grammar css.GrammarType
	token   css.TokenType
	data    string
	values  []css.Token
	start   int
	end     int
	err     error
}

type cssSheet struct {
	path   string
	source string
	events []cssEvent
}

func parseCSS(path, source string, diagnostics *[]Diagnostic) cssSheet {
	sheet := cssSheet{path: path, source: source}
	input := parse.NewInputString(source)
	parser := css.NewParser(input, false)
	lastEnd := 0
	for {
		grammar, token, data := parser.Next()
		end := parser.Offset()
		if end < lastEnd {
			end = lastEnd
		}
		event := cssEvent{grammar: grammar, token: token, data: string(data), start: lastEnd, end: end}
		for _, value := range parser.Values() {
			event.values = append(event.values, css.Token{TokenType: value.TokenType, Data: append([]byte(nil), value.Data...)})
		}
		if grammar == css.ErrorGrammar {
			err := parser.Err()
			if err == io.EOF {
				break
			}
			event.err = err
			offset := cssErrorOffset(source, err)
			addDiagnostic(diagnostics, path, source, "P0301", "CSS syntax error: "+err.Error(), offset)
			sheet.events = append(sheet.events, event)
			if end == lastEnd {
				break
			}
		} else {
			sheet.events = append(sheet.events, event)
		}
		lastEnd = end
	}
	return sheet
}

func cssErrorOffset(source string, err error) int {
	if parseErr, ok := err.(*parse.Error); ok {
		offset := 0
		line := 1
		for offset < len(source) && line < parseErr.Line {
			if source[offset] == '\n' {
				line++
			}
			offset++
		}
		// Parser positions are one-based byte columns.
		offset += parseErr.Column - 1
		if offset < 0 {
			return 0
		}
		if offset > len(source) {
			return len(source)
		}
		return offset
	}
	return 0
}

type cssFrame struct {
	atRule    bool
	keyframes bool
	rule      bool
}

func renderCSS(sheet cssSheet, scopeID string, keyframes map[string]string, diagnostics *[]Diagnostic) string {
	return renderCSSMapped(sheet, scopeID, keyframes, diagnostics, 0, nil)
}

func renderCSSMapped(sheet cssSheet, scopeID string, keyframes map[string]string, diagnostics *[]Diagnostic, generatedOffset int, mappings *[]cssSourceMapping) string {
	var output strings.Builder
	frames := make([]cssFrame, 0, 8)
	scopeSelector := "[" + scopeID + "]"
	for _, event := range sheet.events {
		if event.err != nil {
			continue
		}
		if mappings != nil {
			*mappings = append(*mappings, cssSourceMapping{
				Generated: generatedOffset + output.Len(),
				File:      sheet.path,
				Source:    event.start,
			})
		}
		switch event.grammar {
		case css.CommentGrammar, css.TokenGrammar:
			output.WriteString(event.data)
		case css.AtRuleGrammar:
			if strings.EqualFold(event.data, "@import") {
				cssEventDiagnosticAt(diagnostics, sheet, event, "P0302", "scoped styles do not support @import",
					locateInEvent(sheet.source, event, "@import"))
			}
			output.WriteString(event.data)
			output.WriteString(serializeTokens(event.values))
			output.WriteByte(';')
			writeEventComments(&output, sheet, event)
		case css.BeginAtRuleGrammar:
			inRule := hasRuleFrame(frames)
			if inRule {
				cssEventDiagnostic(diagnostics, sheet, event, "P0303", "CSS nesting is not supported in scoped styles")
			}
			if strings.EqualFold(event.data, "@import") {
				cssEventDiagnosticAt(diagnostics, sheet, event, "P0302", "scoped styles do not support @import",
					locateInEvent(sheet.source, event, "@import"))
			}
			values := event.values
			if isKeyframesRule(event) {
				values = renameKeyframePrelude(values, keyframes)
			}
			output.WriteString(event.data)
			output.WriteString(serializeTokens(values))
			output.WriteByte('{')
			writeEventComments(&output, sheet, event)
			frames = append(frames, cssFrame{atRule: true, keyframes: inKeyframes(frames) || isKeyframesRule(event)})
		case css.EndAtRuleGrammar:
			output.WriteByte('}')
			frames = popFrame(frames, true)
		case css.BeginRulesetGrammar:
			inKeyframes := inKeyframes(frames)
			if hasRuleFrame(frames) {
				cssEventDiagnostic(diagnostics, sheet, event, "P0303", "CSS nesting is not supported in scoped styles")
			}
			if !inKeyframes {
				diagnoseUnsupportedSelectors(diagnostics, sheet, event)
				if selectorHasNesting(event.values) {
					cssEventDiagnostic(diagnostics, sheet, event, "P0303", "CSS nesting selectors are not supported in scoped styles")
				}
				output.WriteString(scopeSelectors(event.values, scopeSelector))
			} else {
				output.WriteString(serializeTokens(event.values))
			}
			output.WriteByte('{')
			writeEventComments(&output, sheet, event)
			frames = append(frames, cssFrame{rule: true, keyframes: inKeyframes})
		case css.EndRulesetGrammar:
			output.WriteByte('}')
			frames = popFrame(frames, false)
		case css.DeclarationGrammar, css.CustomPropertyGrammar:
			property := strings.ToLower(strings.TrimSpace(event.data))
			values := rewriteURLs(event.values, sheet, diagnostics, event)
			if property == "animation" || strings.HasSuffix(property, "-animation") ||
				property == "animation-name" || strings.HasSuffix(property, "-animation-name") {
				values = rewriteAnimation(values, keyframes, property, sheet, event, diagnostics)
			}
			output.WriteString(event.data)
			output.WriteByte(':')
			output.WriteString(serializeTokens(values))
			output.WriteByte(';')
			writeEventComments(&output, sheet, event)
		case css.QualifiedRuleGrammar:
			output.WriteString(event.data)
			output.WriteString(serializeTokens(event.values))
		}
	}
	return output.String()
}

func popFrame(frames []cssFrame, atRule bool) []cssFrame {
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].atRule == atRule {
			return frames[:i]
		}
	}
	return frames
}

func hasRuleFrame(frames []cssFrame) bool {
	for _, frame := range frames {
		if frame.rule {
			return true
		}
	}
	return false
}

func inKeyframes(frames []cssFrame) bool {
	for _, frame := range frames {
		if frame.keyframes {
			return true
		}
	}
	return false
}

func isKeyframesRule(event cssEvent) bool {
	name := strings.ToLower(strings.TrimSpace(event.data))
	return strings.HasPrefix(name, "@") && strings.HasSuffix(name, "keyframes")
}

func keyframeName(values []css.Token) (string, int, bool) {
	for index, value := range values {
		switch value.TokenType {
		case css.IdentToken:
			return string(value.Data), index, true
		case css.StringToken:
			text := string(value.Data)
			if len(text) >= 2 {
				return cssUnescape(text[1 : len(text)-1]), index, true
			}
		}
	}
	return "", 0, false
}

func renameKeyframePrelude(values []css.Token, keyframes map[string]string) []css.Token {
	name, index, ok := keyframeName(values)
	if !ok {
		return values
	}
	renamed, ok := keyframes[name]
	if !ok {
		return values
	}
	result := cloneCSSTokens(values)
	if result[index].TokenType == css.StringToken {
		quote := byte('"')
		if len(result[index].Data) > 0 && result[index].Data[0] == '\'' {
			quote = '\''
		}
		result[index].Data = []byte(string(quote) + cssEscapeString(renamed, quote) + string(quote))
	} else {
		result[index].Data = []byte(renamed)
	}
	return result
}

func scopeSelectors(values []css.Token, attribute string) string {
	var output strings.Builder
	for start := 0; start < len(values); {
		end := findTopLevel(values, start, css.CommaToken)
		if end < 0 {
			end = len(values)
		}
		output.WriteString(scopeSelectorGroup(values[start:end], attribute))
		if end == len(values) {
			break
		}
		output.WriteByte(',')
		start = end + 1
	}
	return output.String()
}

func findTopLevel(tokens []css.Token, start int, delimiter css.TokenType) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		token := tokens[i]
		if depth == 0 && token.TokenType == delimiter {
			return i
		}
		depth = tokenDepth(depth, token)
	}
	return -1
}

func scopeSelectorGroup(tokens []css.Token, attribute string) string {
	var output strings.Builder
	start := 0
	depth := 0
	for i, token := range tokens {
		if depth == 0 && isSelectorSeparator(token) {
			writeScopedCompound(&output, tokens[start:i], attribute)
			output.Write(token.Data)
			start = i + 1
		}
		depth = tokenDepth(depth, token)
	}
	writeScopedCompound(&output, tokens[start:], attribute)
	return output.String()
}

func isSelectorSeparator(token css.Token) bool {
	if token.TokenType == css.WhitespaceToken || token.TokenType == css.ColumnToken {
		return true
	}
	return token.TokenType == css.DelimToken && (string(token.Data) == ">" || string(token.Data) == "+" || string(token.Data) == "~")
}

func tokenDepth(depth int, token css.Token) int {
	switch token.TokenType {
	case css.FunctionToken, css.LeftParenthesisToken, css.LeftBracketToken:
		return depth + 1
	case css.RightParenthesisToken, css.RightBracketToken:
		if depth > 0 {
			return depth - 1
		}
	}
	return depth
}

func writeScopedCompound(output *strings.Builder, tokens []css.Token, attribute string) {
	if len(tokens) == 0 {
		return
	}
	onlyWhitespace := true
	for _, token := range tokens {
		if token.TokenType != css.WhitespaceToken {
			onlyWhitespace = false
			break
		}
	}
	if onlyWhitespace {
		output.WriteString(serializeTokens(tokens))
		return
	}
	insertAt := pseudoElementIndex(tokens)
	for i, token := range tokens {
		if i == insertAt {
			output.WriteString(attribute)
		}
		output.Write(token.Data)
	}
	if insertAt < 0 {
		output.WriteString(attribute)
	}
}

func pseudoElementIndex(tokens []css.Token) int {
	depth := 0
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if depth == 0 && token.TokenType == css.ColonToken {
			if i+1 < len(tokens) && tokens[i+1].TokenType == css.ColonToken {
				return i
			}
			if i+1 < len(tokens) && tokens[i+1].TokenType == css.IdentToken {
				switch strings.ToLower(string(tokens[i+1].Data)) {
				case "before", "after", "first-line", "first-letter":
					return i
				}
			}
		}
		depth = tokenDepth(depth, token)
	}
	return -1
}

func selectorHasNesting(tokens []css.Token) bool {
	brackets := 0
	for _, token := range tokens {
		if brackets == 0 && token.TokenType == css.DelimToken && string(token.Data) == "&" {
			return true
		}
		if token.TokenType == css.LeftBracketToken {
			brackets++
		} else if token.TokenType == css.RightBracketToken && brackets > 0 {
			brackets--
		}
	}
	return false
}

func diagnoseUnsupportedSelectors(diagnostics *[]Diagnostic, sheet cssSheet, event cssEvent) {
	for i, token := range event.values {
		if token.TokenType != css.ColonToken {
			continue
		}
		nameIndex := i + 1
		if nameIndex < len(event.values) && event.values[nameIndex].TokenType == css.ColonToken {
			nameIndex++
		}
		if nameIndex >= len(event.values) {
			continue
		}
		name := ""
		switch event.values[nameIndex].TokenType {
		case css.IdentToken:
			name = strings.ToLower(string(event.values[nameIndex].Data))
		case css.FunctionToken:
			name = strings.ToLower(strings.TrimSuffix(string(event.values[nameIndex].Data), "("))
		}
		if name != "host" && name != "global" && name != "root" {
			continue
		}
		offset := locateInEvent(sheet.source, event, ":"+name)
		cssEventDiagnosticAt(diagnostics, sheet, event, "P0304", ":"+name+" is not supported in scoped selectors", offset)
	}
}

func serializeTokens(values []css.Token) string {
	var output strings.Builder
	for _, value := range values {
		output.Write(value.Data)
	}
	return output.String()
}

func cloneCSSTokens(values []css.Token) []css.Token {
	result := make([]css.Token, len(values))
	for i, value := range values {
		result[i] = css.Token{TokenType: value.TokenType, Data: append([]byte(nil), value.Data...)}
	}
	return result
}

func rewriteURLs(values []css.Token, sheet cssSheet, diagnostics *[]Diagnostic, event cssEvent) []css.Token {
	result := cloneCSSTokens(values)
	for i := range result {
		if result[i].TokenType != css.URLToken {
			continue
		}
		rewritten, err := embedCSSURL(string(result[i].Data), sheet.path)
		if err != nil {
			offset := locateInEvent(sheet.source, event, string(result[i].Data))
			cssEventDiagnosticAt(diagnostics, sheet, event, "P0306", "cannot embed CSS resource: "+err.Error(), offset)
			continue
		}
		result[i].Data = []byte(rewritten)
	}
	return result
}

func embedCSSURL(raw, stylesheet string) (string, error) {
	open := strings.IndexByte(raw, '(')
	close := strings.LastIndexByte(raw, ')')
	if open < 0 || close <= open {
		return raw, nil
	}
	inner := strings.TrimSpace(raw[open+1 : close])
	pathText := inner
	if len(inner) >= 2 && (inner[0] == '"' || inner[0] == '\'') && inner[len(inner)-1] == inner[0] {
		pathText = cssUnescape(inner[1 : len(inner)-1])
	} else {
		pathText = cssUnescape(strings.Trim(inner, " \t\r\n\f"))
	}
	if pathText == "" || strings.HasPrefix(pathText, "#") {
		return raw, nil
	}
	parsed, err := url.Parse(pathText)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() || strings.HasPrefix(pathText, "//") || strings.HasPrefix(pathText, "/") {
		return raw, nil
	}
	// Embedded bytes need no cache-buster query; appending it would corrupt the data URL payload.
	resourcePath := filepath.Join(filepath.Dir(stylesheet), filepath.FromSlash(parsed.Path))
	data, err := os.ReadFile(resourcePath)
	if err != nil {
		return "", err
	}
	contentType := mime.TypeByExtension(filepath.Ext(resourcePath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	result := "url(\"data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
	if parsed.Fragment != "" {
		result += "#" + parsed.EscapedFragment()
	}
	return result + "\")", nil
}

func rewriteAnimation(values []css.Token, keyframes map[string]string, property string, sheet cssSheet, event cssEvent, diagnostics *[]Diagnostic) []css.Token {
	if len(keyframes) == 0 {
		return values
	}
	result := cloneCSSTokens(values)
	if property == "animation-name" || strings.HasSuffix(property, "-animation-name") {
		for i := range result {
			if result[i].TokenType != css.IdentToken && result[i].TokenType != css.StringToken {
				continue
			}
			name, quoted := animationTokenName(result[i])
			if renamed, ok := keyframes[name]; ok {
				result[i].Data = renamedAnimationToken(result[i], renamed, quoted)
			}
		}
		return result
	}

	for _, segment := range splitTokenList(result, css.CommaToken) {
		candidates := make([]int, 0, 2)
		for _, index := range segment {
			if result[index].TokenType != css.IdentToken && result[index].TokenType != css.StringToken {
				continue
			}
			name, _ := animationTokenName(result[index])
			if _, ok := keyframes[name]; ok {
				candidates = append(candidates, index)
			}
		}
		ambiguous := len(candidates) > 1
		for _, index := range candidates {
			name, quoted := animationTokenName(result[index])
			if !quoted && animationReservedName(name) {
				ambiguous = true
			}
		}
		if ambiguous {
			cssEventDiagnostic(diagnostics, sheet, event, "P0305", "animation shorthand contains an ambiguous keyframe name")
			continue
		}
		if len(candidates) == 1 {
			index := candidates[0]
			name, quoted := animationTokenName(result[index])
			result[index].Data = renamedAnimationToken(result[index], keyframes[name], quoted)
		}
	}
	return result
}

func splitTokenList(tokens []css.Token, delimiter css.TokenType) [][]int {
	var result [][]int
	start, depth := 0, 0
	for i, token := range tokens {
		if depth == 0 && token.TokenType == delimiter {
			part := make([]int, 0, i-start)
			for j := start; j < i; j++ {
				part = append(part, j)
			}
			result = append(result, part)
			start = i + 1
		}
		depth = tokenDepth(depth, token)
	}
	part := make([]int, 0, len(tokens)-start)
	for j := start; j < len(tokens); j++ {
		part = append(part, j)
	}
	result = append(result, part)
	return result
}

func writeEventComments(output *strings.Builder, sheet cssSheet, event cssEvent) {
	start, end := event.start, event.end
	if start < 0 {
		start = 0
	}
	if end > len(sheet.source) {
		end = len(sheet.source)
	}
	if start >= end {
		return
	}
	source := sheet.source[start:end]
	quote := byte(0)
	escaped := false
	for i := 0; i+1 < len(source); i++ {
		if quote != 0 {
			if escaped {
				escaped = false
			} else if source[i] == '\\' {
				escaped = true
			} else if source[i] == quote {
				quote = 0
			}
			continue
		}
		if source[i] == '"' || source[i] == '\'' {
			quote = source[i]
			continue
		}
		if source[i:i+2] == "/*" {
			if close := strings.Index(source[i+2:], "*/"); close >= 0 {
				close += i + 4
				output.WriteString(source[i:close])
				i = close - 1
			}
		}
	}
}

func animationTokenName(token css.Token) (string, bool) {
	if token.TokenType == css.StringToken {
		text := string(token.Data)
		if len(text) >= 2 {
			return cssUnescape(text[1 : len(text)-1]), true
		}
	}
	return string(token.Data), false
}

func renamedAnimationToken(token css.Token, name string, quoted bool) []byte {
	if !quoted {
		return []byte(name)
	}
	old := string(token.Data)
	quote := byte('"')
	if len(old) > 0 && old[0] == '\'' {
		quote = '\''
	}
	return []byte(string(quote) + cssEscapeString(name, quote) + string(quote))
}

func animationReservedName(name string) bool {
	switch strings.ToLower(name) {
	case "none", "initial", "inherit", "unset", "revert", "revert-layer",
		"normal", "reverse", "alternate", "alternate-reverse", "forwards", "backwards",
		"both", "running", "paused", "infinite", "ease", "linear", "ease-in",
		"ease-out", "ease-in-out", "step-start", "step-end":
		return true
	default:
		return false
	}
}

func cssEventDiagnostic(diagnostics *[]Diagnostic, sheet cssSheet, event cssEvent, code, message string) {
	cssEventDiagnosticAt(diagnostics, sheet, event, code, message, event.start)
}

func cssEventDiagnosticAt(diagnostics *[]Diagnostic, sheet cssSheet, event cssEvent, code, message string, offset int) {
	addDiagnostic(diagnostics, sheet.path, sheet.source, code, message, offset)
}

func locateInEvent(source string, event cssEvent, needle string) int {
	start, end := event.start, event.end
	if start < 0 {
		start = 0
	}
	if end > len(source) {
		end = len(source)
	}
	if start > end {
		start = end
	}
	if needle == "" {
		return start
	}
	if index := strings.Index(source[start:end], needle); index >= 0 {
		return start + index
	}
	if index := strings.Index(source, needle); index >= 0 {
		return index
	}
	return start
}

func cssUnescape(value string) string {
	var output strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '\\' || i+1 >= len(value) {
			output.WriteByte(value[i])
			i++
			continue
		}
		i++
		if value[i] == '\n' || value[i] == '\r' || value[i] == '\f' {
			if value[i] == '\r' && i+1 < len(value) && value[i+1] == '\n' {
				i++
			}
			i++
			continue
		}
		if isHexByte(value[i]) {
			start := i
			for i < len(value) && i-start < 6 && isHexByte(value[i]) {
				i++
			}
			codepoint, err := strconv.ParseInt(value[start:i], 16, 32)
			if err == nil && codepoint > 0 && codepoint <= 0x10ffff {
				output.WriteRune(rune(codepoint))
			} else {
				output.WriteRune('\uFFFD')
			}
			if i < len(value) && (value[i] == ' ' || value[i] == '\t' || value[i] == '\r' || value[i] == '\n' || value[i] == '\f') {
				i++
			}
			continue
		}
		output.WriteByte(value[i])
		i++
	}
	return output.String()
}

func isHexByte(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func cssEscapeString(value string, quote byte) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, string(quote), `\`+string(quote))
	value = strings.ReplaceAll(value, "\n", `\a `)
	value = strings.ReplaceAll(value, "\r", `\d `)
	return value
}
