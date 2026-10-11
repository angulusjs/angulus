package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	parseInput "github.com/tdewolff/parse/v2"
	jsLexer "github.com/tdewolff/parse/v2/js"
)

type TSModuleTokenKind uint8

const (
	TSModuleIdentifier TSModuleTokenKind = iota
	TSModuleString
	TSModulePunctuation
	TSModuleOpaque
)

// TSModuleToken is one lexical token. Start and End are byte offsets into the
// original UTF-8 source, with End exclusive.
type TSModuleToken struct {
	Kind       TSModuleTokenKind
	Text       string
	Value      string
	Start, End int
}

type libraryModuleTokenKind uint8

const (
	libraryTokenIdentifier libraryModuleTokenKind = iota
	libraryTokenString
	libraryTokenPunctuation
	libraryTokenOpaque
)

type libraryModuleToken struct {
	kind       libraryModuleTokenKind
	text       string
	value      string
	start, end int
}

type libraryExportReference struct {
	module string
	name   string
}

type libraryModuleDeclaration struct {
	exports map[string]libraryExportReference
	stars   []string
	imports map[string]libraryExportReference
}

type libraryComponentSymbol struct {
	path     string
	export   string
	metadata LibraryComponentMetadata
}

type libraryExportResolver struct {
	modules    map[string]*libraryModuleDeclaration
	components []libraryComponentSymbol
	external   map[string][]libraryComponentSymbol
}

// LexTSModule tokenizes TypeScript source without parsing or rewriting it.
// Comments are skipped; template and regular-expression literals are opaque.
func LexTSModule(source string) ([]TSModuleToken, error) {
	tokens, err := tokenizeLibraryModule(source)
	if err != nil {
		return nil, err
	}
	publicTokens := make([]TSModuleToken, len(tokens))
	for i, token := range tokens {
		publicTokens[i] = TSModuleToken{
			Kind: TSModuleTokenKind(token.kind), Text: token.text, Value: token.value,
			Start: token.start, End: token.end,
		}
	}
	return publicTokens, nil
}

func tokenizeLibraryModule(source string) ([]libraryModuleToken, error) {
	input := parseInput.NewInputString(source)
	lexer := jsLexer.NewLexer(input)
	var tokens []libraryModuleToken
	var contextTokens []libraryModuleToken
	var templateContexts [][]libraryModuleToken
	offset := 0
	templateDepth, templateStart := 0, 0
	appendToken := func(token libraryModuleToken) {
		contextTokens = append(contextTokens, token)
		if templateDepth == 0 {
			tokens = append(tokens, token)
		}
	}
	for {
		start := offset
		tokenType, lexeme := lexer.Next()
		if tokenType == jsLexer.ErrorToken {
			if errors.Is(lexer.Err(), io.EOF) {
				break
			}
			if kind, ok := tsCompatibilityError(lexeme, contextTokens); ok {
				text := string(lexeme)
				offset = start + len(lexeme)
				appendToken(libraryModuleToken{kind: kind, text: text, start: start, end: offset})
				continue
			}
			return nil, fmt.Errorf("TypeScript lexical error at byte %d: %w", offset, lexer.Err())
		}
		if tokenType == jsLexer.DivToken || tokenType == jsLexer.DivEqToken {
			if libraryRegexMayStart(contextTokens) {
				var regexpLexeme []byte
				tokenType, regexpLexeme = lexer.RegExp()
				if tokenType == jsLexer.ErrorToken {
					return nil, fmt.Errorf("invalid regular expression at byte %d: %w", start, lexer.Err())
				}
				lexeme = regexpLexeme
			}
		}
		offset = start + len(lexeme)
		if len(lexeme) == 0 {
			return nil, fmt.Errorf("lexer made no progress at byte %d", offset)
		}
		if tokenType == jsLexer.WhitespaceToken || tokenType == jsLexer.LineTerminatorToken ||
			tokenType == jsLexer.CommentToken || tokenType == jsLexer.CommentLineTerminatorToken {
			continue
		}
		text := string(lexeme)
		if tokenType == jsLexer.TemplateToken {
			appendToken(libraryModuleToken{kind: libraryTokenOpaque, text: text, start: start, end: offset})
			continue
		}
		if tokenType == jsLexer.TemplateStartToken {
			if templateDepth == 0 {
				templateStart = start
			}
			templateDepth++
			templateContexts = append(templateContexts, contextTokens)
			contextTokens = []libraryModuleToken{{kind: libraryTokenPunctuation, text: "("}}
			continue
		}
		if tokenType == jsLexer.TemplateEndToken {
			templateDepth--
			if templateDepth < 0 {
				return nil, fmt.Errorf("unexpected template end at byte %d", start)
			}
			contextTokens = templateContexts[len(templateContexts)-1]
			templateContexts = templateContexts[:len(templateContexts)-1]
			contextTokens = append(contextTokens, libraryModuleToken{kind: libraryTokenOpaque, text: "<template>", start: start, end: offset})
			if templateDepth == 0 {
				tokens = append(tokens, libraryModuleToken{
					kind: libraryTokenOpaque, text: source[templateStart:offset], start: templateStart, end: offset,
				})
			}
			continue
		}
		if tokenType == jsLexer.TemplateMiddleToken {
			contextTokens = []libraryModuleToken{{kind: libraryTokenPunctuation, text: "("}}
			continue
		}
		if templateDepth > 0 {
			kind := libraryTokenPunctuation
			if isJSIdentifierToken(tokenType) {
				kind = libraryTokenIdentifier
			}
			contextTokens = append(contextTokens, libraryModuleToken{kind: kind, text: text, start: start, end: offset})
			continue
		}
		switch {
		case tokenType == jsLexer.StringToken:
			value, err := decodeLibraryString(text[0], text[1:len(text)-1])
			if err != nil {
				return nil, fmt.Errorf("invalid string at byte %d: %w", start, err)
			}
			appendToken(libraryModuleToken{kind: libraryTokenString, text: text, value: value, start: start, end: offset})
		case tokenType == jsLexer.RegExpToken:
			appendToken(libraryModuleToken{kind: libraryTokenOpaque, text: text, start: start, end: offset})
		case isJSIdentifierToken(tokenType):
			appendToken(libraryModuleToken{kind: libraryTokenIdentifier, text: text, start: start, end: offset})
		default:
			appendToken(libraryModuleToken{kind: libraryTokenPunctuation, text: text, start: start, end: offset})
		}
	}
	if templateDepth != 0 {
		return nil, fmt.Errorf("unterminated template literal at byte %d", templateStart)
	}
	return tokens, nil
}

func isJSIdentifierToken(tokenType jsLexer.TokenType) bool {
	return tokenType >= jsLexer.ReservedToken
}

func tsCompatibilityError(lexeme []byte, previous []libraryModuleToken) (libraryModuleTokenKind, bool) {
	text := string(lexeme)
	if text == "@" {
		return libraryTokenPunctuation, true
	}
	if len(previous) == 0 {
		return 0, false
	}
	last := previous[len(previous)-1].text
	numeric := last != "" && last[len(last)-1] >= '0' && last[len(last)-1] <= '9'
	if numeric && (text == "n" || (strings.HasPrefix(text, "_") && len(text) > 1)) {
		return libraryTokenOpaque, true
	}
	return 0, false
}

func libraryRegexMayStart(tokens []libraryModuleToken) bool {
	if len(tokens) == 0 {
		return true
	}
	previous := tokens[len(tokens)-1]
	if previous.kind == libraryTokenIdentifier {
		switch previous.text {
		case "return", "throw", "case", "delete", "void", "typeof", "instanceof", "in", "of", "yield", "await":
			return true
		default:
			return false
		}
	}
	if previous.text == ")" {
		depth := 0
		for i := len(tokens) - 1; i >= 0; i-- {
			switch tokens[i].text {
			case ")":
				depth++
			case "(":
				depth--
				if depth == 0 && i > 0 {
					switch tokens[i-1].text {
					case "if", "while", "for", "with", "switch", "catch":
						return true
					}
					return false
				}
			}
		}
	}
	switch previous.text {
	case "=", "(", "[", "{", ",", ":", ";", "!", "?", "&&", "||", "??", "=>", "==", "===", "!=", "!==", "+", "-", "*", "%":
		return true
	default:
		return false
	}
}

func libraryDeclarationModuleSpecifiers(tokens []libraryModuleToken) []libraryModuleToken {
	var specifiers []libraryModuleToken
	for i, token := range tokens {
		if token.kind != libraryTokenString {
			continue
		}
		if i > 0 && (tokens[i-1].text == "from" || tokens[i-1].text == "import") {
			specifiers = append(specifiers, token)
			continue
		}
		if i >= 2 && tokens[i-1].text == "(" && tokens[i-2].text == "import" &&
			i+1 < len(tokens) && tokens[i+1].text == ")" {
			specifiers = append(specifiers, token)
		}
	}
	return specifiers
}

func decodeLibraryString(quote byte, raw string) (string, error) {
	if quote == '"' {
		return strconv.Unquote(`"` + raw + `"`)
	}
	var converted strings.Builder
	converted.WriteByte('"')
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			if i+1 >= len(raw) {
				return "", fmt.Errorf("trailing escape")
			}
			i++
			if raw[i] == '\'' {
				converted.WriteByte('\'')
			} else {
				converted.WriteByte('\\')
				converted.WriteByte(raw[i])
			}
		case '"':
			converted.WriteString(`\"`)
		default:
			converted.WriteByte(raw[i])
		}
	}
	converted.WriteByte('"')
	quoted := converted.String()
	return strconv.Unquote(quoted)
}

func parseLibraryModule(source string) (*libraryModuleDeclaration, error) {
	tokens, err := tokenizeLibraryModule(source)
	if err != nil {
		return nil, err
	}
	module := &libraryModuleDeclaration{
		exports: make(map[string]libraryExportReference),
		imports: make(map[string]libraryExportReference),
	}
	braceDepth, parenDepth, bracketDepth := 0, 0, 0
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if braceDepth == 0 && parenDepth == 0 && bracketDepth == 0 && token.kind == libraryTokenIdentifier {
			switch token.text {
			case "import":
				if i+1 < len(tokens) && tokens[i+1].text != "(" && tokens[i+1].text != "." {
					parseLibraryImport(tokens, i, module)
				}
			case "export":
				parseLibraryExport(tokens, i, module)
			}
		}
		switch token.text {
		case "{":
			braceDepth++
		case "}":
			if braceDepth > 0 {
				braceDepth--
			}
		case "(":
			parenDepth++
		case ")":
			if parenDepth > 0 {
				parenDepth--
			}
		case "[":
			bracketDepth++
		case "]":
			if bracketDepth > 0 {
				bracketDepth--
			}
		}
	}
	return module, nil
}

func parseLibraryImport(tokens []libraryModuleToken, start int, module *libraryModuleDeclaration) {
	end := start + 1
	for end < len(tokens) && tokens[end].text != ";" && tokens[end].text != "from" {
		end++
	}
	if end >= len(tokens) || tokens[end].text != "from" || end+1 >= len(tokens) || tokens[end+1].kind != libraryTokenString {
		return
	}
	specifier := tokens[end+1].value
	clauseStart := start + 1
	importTypeOnly := clauseStart < end && tokens[clauseStart].text == "type"
	if importTypeOnly {
		clauseStart++
	}
	if !importTypeOnly && clauseStart < end && tokens[clauseStart].kind == libraryTokenIdentifier && tokens[clauseStart].text != "type" {
		module.imports[tokens[clauseStart].text] = libraryExportReference{module: specifier, name: "default"}
		clauseStart++
		if clauseStart < end && tokens[clauseStart].text == "," {
			clauseStart++
		}
	}
	for i := clauseStart; i < end; i++ {
		if tokens[i].text != "{" {
			continue
		}
		for i++; i < end && tokens[i].text != "}"; {
			if tokens[i].text == "," {
				i++
				continue
			}
			specifierTypeOnly := importTypeOnly || tokens[i].text == "type"
			if tokens[i].text == "type" {
				i++
			}
			if tokens[i].kind != libraryTokenIdentifier {
				i++
				continue
			}
			imported := tokens[i].text
			local := imported
			i++
			if i+1 < end && tokens[i].text == "as" && tokens[i+1].kind == libraryTokenIdentifier {
				local = tokens[i+1].text
				i += 2
			}
			if !specifierTypeOnly {
				module.imports[local] = libraryExportReference{module: specifier, name: imported}
			}
		}
		break
	}
}

func parseLibraryExport(tokens []libraryModuleToken, start int, module *libraryModuleDeclaration) {
	if start+1 >= len(tokens) {
		return
	}
	next := start + 1
	typeOnly := tokens[next].text == "type"
	if typeOnly {
		next++
	}
	if next >= len(tokens) {
		return
	}
	if tokens[next].text == "*" {
		i := next + 1
		if typeOnly || (i < len(tokens) && tokens[i].text == "as") {
			return
		}
		if i < len(tokens) && tokens[i].text == "from" && i+1 < len(tokens) && tokens[i+1].kind == libraryTokenString {
			module.stars = append(module.stars, tokens[i+1].value)
		}
		return
	}
	if tokens[next].text == "{" {
		close := next + 1
		for close < len(tokens) && tokens[close].text != "}" {
			close++
		}
		if close >= len(tokens) {
			return
		}
		specifier := ""
		if close+2 < len(tokens) && tokens[close+1].text == "from" && tokens[close+2].kind == libraryTokenString {
			specifier = tokens[close+2].value
		}
		for i := next + 1; i < close; {
			if tokens[i].text == "," {
				i++
				continue
			}
			specifierTypeOnly := typeOnly || tokens[i].text == "type"
			if tokens[i].text == "type" {
				i++
			}
			if tokens[i].kind != libraryTokenIdentifier {
				i++
				continue
			}
			original := tokens[i].text
			exported := original
			i++
			if i+1 < close && tokens[i].text == "as" && tokens[i+1].kind == libraryTokenIdentifier {
				exported = tokens[i+1].text
				i += 2
			}
			if exported != "default" && !specifierTypeOnly {
				module.exports[exported] = libraryExportReference{module: specifier, name: original}
			}
		}
		return
	}
	if tokens[next].text == "default" || tokens[next].text == "=" {
		return
	}
	for next < len(tokens) && isLibraryExportModifier(tokens[next].text) {
		next++
	}
	if next < len(tokens) && tokens[next].kind == libraryTokenIdentifier {
		switch tokens[next].text {
		case "class", "function", "interface", "type", "enum", "const", "let", "var", "namespace":
			if next+1 < len(tokens) && tokens[next+1].kind == libraryTokenIdentifier {
				module.exports[tokens[next+1].text] = libraryExportReference{name: tokens[next+1].text}
			}
		}
	}
}

func isLibraryExportModifier(text string) bool {
	switch text {
	case "declare", "abstract", "async":
		return true
	default:
		return false
	}
}

func newLibraryExportResolver(components []*Component) *libraryExportResolver {
	resolver := &libraryExportResolver{
		modules:  make(map[string]*libraryModuleDeclaration),
		external: make(map[string][]libraryComponentSymbol),
	}
	for _, component := range components {
		resolver.components = append(resolver.components, libraryComponentSymbol{
			path: filepath.Clean(component.Facade), export: component.Descriptor.Export,
			metadata: LibraryComponentMetadata{
				Selector: component.Descriptor.Selector,
				Module:   "./components/" + component.Descriptor.Selector + ".js",
				Export:   component.Descriptor.Export,
			},
		})
		resolver.components = append(resolver.components, libraryComponentSymbol{
			path: filepath.Clean(component.Module), export: component.Descriptor.Export,
			metadata: LibraryComponentMetadata{Selector: component.Descriptor.Selector},
		})
	}
	return resolver
}

func (resolver *libraryExportResolver) externalComponents(from, specifier string) ([]libraryComponentSymbol, error) {
	key := filepath.Dir(from) + "\x00" + specifier
	if components, exists := resolver.external[key]; exists {
		return components, nil
	}
	metadata, packageDir, subpath, err := installedLibraryMetadata(filepath.Dir(from), specifier)
	if err != nil {
		return nil, err
	}
	var components []libraryComponentSymbol
	if metadata != nil {
		for _, component := range libraryMetadataExports(*metadata, subpath) {
			components = append(components, libraryComponentSymbol{
				path:   filepath.Join(packageDir, filepath.FromSlash(component.Module)),
				export: component.Export, metadata: component,
			})
		}
	}
	resolver.external[key] = components
	return components, nil
}

func (resolver *libraryExportResolver) externalComponent(from, specifier, name string) (*libraryComponentSymbol, error) {
	if name == "default" {
		return nil, nil
	}
	components, err := resolver.externalComponents(from, specifier)
	if err != nil {
		return nil, err
	}
	var found *libraryComponentSymbol
	for i := range components {
		if components[i].export == name {
			if found != nil {
				return nil, fmt.Errorf("external component export %q from %q is ambiguous", name, specifier)
			}
			found = &components[i]
		}
	}
	return found, nil
}

func (resolver *libraryExportResolver) load(path string) (*libraryModuleDeclaration, error) {
	path = filepath.Clean(path)
	if module, ok := resolver.modules[path]; ok {
		return module, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	module, err := parseLibraryModule(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse TypeScript module %s: %w", path, err)
	}
	resolver.modules[path] = module
	return module, nil
}

func (resolver *libraryExportResolver) resolveModule(from, specifier string) (string, bool, error) {
	if !strings.HasPrefix(specifier, ".") {
		return "", false, nil
	}
	base := filepath.Clean(filepath.Join(filepath.Dir(from), filepath.FromSlash(specifier)))
	ext := filepath.Ext(base)
	candidates := []string{base}
	switch ext {
	case ".js":
		candidates = append(candidates, strings.TrimSuffix(base, ext)+".ts", strings.TrimSuffix(base, ext)+".tsx")
	case ".mjs":
		candidates = append(candidates, strings.TrimSuffix(base, ext)+".mts")
	case ".cjs":
		candidates = append(candidates, strings.TrimSuffix(base, ext)+".cts")
	default:
		candidates = append(candidates, base+".ts", base+".tsx", base+".mts", base+".cts",
			filepath.Join(base, "index.ts"), filepath.Join(base, "index.tsx"))
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return filepath.Clean(candidate), true, nil
		}
	}
	return "", false, fmt.Errorf("cannot resolve relative module %q from %s", specifier, from)
}

func (resolver *libraryExportResolver) publicExportNames(path string, visiting map[string]bool) (map[string]bool, error) {
	path = filepath.Clean(path)
	if visiting[path] {
		return map[string]bool{}, nil
	}
	visiting[path] = true
	defer delete(visiting, path)
	module, err := resolver.load(path)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(module.exports))
	for name := range module.exports {
		if name != "default" {
			names[name] = true
		}
	}
	for _, specifier := range module.stars {
		target, local, err := resolver.resolveModule(path, specifier)
		if err != nil {
			return nil, err
		}
		if !local {
			components, err := resolver.externalComponents(path, specifier)
			if err != nil {
				return nil, err
			}
			for _, component := range components {
				if component.export != "default" {
					names[component.export] = true
				}
			}
			continue
		}
		targetNames, err := resolver.publicExportNames(target, visiting)
		if err != nil {
			return nil, err
		}
		for name := range targetNames {
			if name != "default" {
				names[name] = true
			}
		}
	}
	return names, nil
}

func (resolver *libraryExportResolver) resolveComponent(path, name string, visiting map[string]bool) (*libraryComponentSymbol, error) {
	path = filepath.Clean(path)
	key := path + "\x00" + name
	if visiting[key] {
		return nil, nil
	}
	visiting[key] = true
	defer delete(visiting, key)
	for i := range resolver.components {
		component := &resolver.components[i]
		if component.path == path && component.export == name && component.metadata.Module != "" {
			return component, nil
		}
	}
	module, err := resolver.load(path)
	if err != nil {
		return nil, err
	}
	if reference, exists := module.exports[name]; exists {
		if reference.module == "" {
			if imported, ok := module.imports[reference.name]; ok {
				reference = imported
			} else if name == reference.name {
				return nil, nil
			}
		}
		target := path
		if reference.module != "" {
			var local bool
			target, local, err = resolver.resolveModule(path, reference.module)
			if err != nil {
				return nil, err
			}
			if !local {
				return resolver.externalComponent(path, reference.module, reference.name)
			}
		}
		return resolver.resolveComponent(target, reference.name, visiting)
	}
	if imported, exists := module.imports[name]; exists {
		target, local, err := resolver.resolveModule(path, imported.module)
		if err != nil {
			return nil, err
		}
		if local {
			return resolver.resolveComponent(target, imported.name, visiting)
		}
		return resolver.externalComponent(path, imported.module, imported.name)
	}
	var found *libraryComponentSymbol
	for _, specifier := range module.stars {
		target, local, err := resolver.resolveModule(path, specifier)
		if err != nil {
			return nil, err
		}
		var component *libraryComponentSymbol
		if local {
			component, err = resolver.resolveComponent(target, name, visiting)
		} else {
			component, err = resolver.externalComponent(path, specifier, name)
		}
		if err != nil {
			return nil, err
		}
		if component == nil {
			continue
		}
		if found != nil && (found.path != component.path || found.export != component.export) {
			return nil, fmt.Errorf("component export %q is ambiguous through export * in %s", name, path)
		}
		found = component
	}
	return found, nil
}

func libraryRootComponentExports(entry string, components []*Component) ([]LibraryComponentMetadata, error) {
	resolver := newLibraryExportResolver(components)
	names, err := resolver.publicExportNames(entry, make(map[string]bool))
	if err != nil {
		return nil, err
	}
	exportNames := make([]string, 0, len(names))
	for name := range names {
		exportNames = append(exportNames, name)
	}
	sort.Strings(exportNames)
	result := make([]LibraryComponentMetadata, 0)
	seen := make(map[string]bool)
	for _, name := range exportNames {
		component, err := resolver.resolveComponent(entry, name, make(map[string]bool))
		if err != nil {
			return nil, err
		}
		if component == nil {
			continue
		}
		metadata := component.metadata
		metadata.Module = "./index.js"
		metadata.Export = name
		key := metadata.Selector + "\x00" + metadata.Module + "\x00" + metadata.Export
		if !seen[key] {
			seen[key] = true
			result = append(result, metadata)
		}
	}
	return result, nil
}

// External export-star names are intentionally absent from esbuild's metafile.
// Confirm those names against the external stars retained in the emitted ESM.
func libraryEmittedStarComponent(output, entry string, component LibraryComponentMetadata) (bool, error) {
	data, err := os.ReadFile(output)
	if err != nil {
		return false, err
	}
	module, err := parseLibraryModule(string(data))
	if err != nil {
		return false, err
	}
	resolver := newLibraryExportResolver(nil)
	for _, specifier := range module.stars {
		if strings.HasPrefix(specifier, ".") {
			continue
		}
		symbol, err := resolver.externalComponent(entry, specifier, component.Export)
		if err != nil {
			return false, err
		}
		if symbol != nil && symbol.metadata.Selector == component.Selector {
			return true, nil
		}
	}
	return false, nil
}

func libraryMetafileExports(metafile, outputName string) (map[string]bool, error) {
	var data struct {
		Outputs map[string]struct {
			Exports []string `json:"exports"`
		} `json:"outputs"`
	}
	if err := jsonUnmarshalMetafile(metafile, &data); err != nil {
		return nil, err
	}
	outputName = filepath.ToSlash(filepath.Clean(filepath.FromSlash(outputName)))
	var found map[string]bool
	for path, output := range data.Outputs {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		matches := clean == outputName || strings.HasSuffix(clean, "/"+outputName)
		if outputName == "index.js" {
			matches = filepath.Base(clean) == outputName
		}
		if !matches {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("esbuild metafile contains multiple outputs for %s", outputName)
		}
		found = make(map[string]bool, len(output.Exports))
		for _, name := range output.Exports {
			found[name] = true
		}
	}
	if found == nil {
		return nil, fmt.Errorf("esbuild metafile is missing output %s", outputName)
	}
	return found, nil
}

func jsonUnmarshalMetafile(data string, value any) error {
	if err := json.Unmarshal([]byte(data), value); err != nil {
		return fmt.Errorf("invalid esbuild metafile: %w", err)
	}
	return nil
}
