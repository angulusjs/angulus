package project

import (
	"os"
	"strings"
)

// Only visibility and the initializer's imported factory are inspected here;
// TypeScript syntax and semantic types remain the native checker's job.
func validateMemberContracts(result *Result) {
	for _, component := range result.Components {
		data, err := os.ReadFile(component.Module)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{
				File: component.Module, Code: "P0110", Message: err.Error(), Severity: "error",
			})
			continue
		}
		source := string(data)
		tokens, err := tokenizeLibraryModule(source)
		if err != nil {
			addDiagnostic(&result.Diagnostics, component.Module, source, "P0110",
				"Cannot inspect component contracts: "+err.Error(), expressionErrorOffset(err))
			continue
		}
		module, err := parseLibraryModule(source)
		if err != nil {
			addDiagnostic(&result.Diagnostics, component.Module, source, "P0110",
				"Cannot inspect component imports: "+err.Error(), expressionErrorOffset(err))
			continue
		}
		className := component.Descriptor.Export
		if reference, exists := module.exports[className]; exists && reference.module == "" {
			className = reference.name
		}
		body := componentClassBody(tokens, className)
		if body < 0 {
			continue
		}
		namespaces := coreNamespaces(tokens)
		braceDepth, parenDepth, bracketDepth := 1, 0, 0
		for index := body + 1; index < len(tokens) && braceDepth > 0; index++ {
			token := tokens[index]
			hidden := token.text == "private" || token.text == "protected" || strings.HasPrefix(token.text, "#")
			if hidden && braceDepth == 1 && parenDepth == 0 && bracketDepth == 0 &&
				(index == 0 || tokens[index-1].text != "." && tokens[index-1].text != "=") {
				initializer := memberInitializer(tokens, index, source)
				if initializer >= 0 && inputOutputInitializer(tokens, initializer, module.imports, namespaces) {
					addDiagnostic(&result.Diagnostics, component.Module, source, "P0111",
						"Component inputs and outputs must be public.", token.start)
				}
			}
			switch token.text {
			case "{":
				braceDepth++
			case "}":
				braceDepth--
			case "(":
				parenDepth++
			case ")":
				parenDepth--
			case "[":
				bracketDepth++
			case "]":
				bracketDepth--
			}
		}
	}
}

func componentClassBody(tokens []libraryModuleToken, name string) int {
	for index := 0; index+1 < len(tokens); index++ {
		if tokens[index].text != "class" || tokens[index+1].text != name {
			continue
		}
		parens, angles := 0, 0
		for index += 2; index < len(tokens); index++ {
			switch tokens[index].text {
			case "(":
				parens++
			case ")":
				parens--
			case "<":
				angles++
			case ">":
				if angles > 0 {
					angles--
				}
			case "{":
				if parens == 0 && angles == 0 {
					return index
				}
			}
		}
	}
	return -1
}

func memberInitializer(tokens []libraryModuleToken, start int, source string) int {
	index := start
	for index < len(tokens) {
		switch tokens[index].text {
		case "private", "protected", "public", "readonly", "static", "declare", "override", "abstract", "accessor":
			index++
		default:
			goto name
		}
	}
name:
	if index < len(tokens) && tokens[index].text == "#" {
		index++
	}
	if index >= len(tokens) {
		return -1
	}
	if tokens[index].text == "[" {
		depth := 0
		for index < len(tokens) {
			if tokens[index].text == "[" {
				depth++
			} else if tokens[index].text == "]" {
				depth--
			}
			index++
			if depth == 0 {
				break
			}
		}
	} else if tokens[index].kind == libraryTokenIdentifier || tokens[index].kind == libraryTokenString ||
		strings.HasPrefix(tokens[index].text, "#") {
		index++
	} else {
		return -1
	}
	if index < len(tokens) && (tokens[index].text == "?" || tokens[index].text == "!") {
		index++
	}
	if index < len(tokens) && tokens[index].text == "(" {
		return -1
	}
	parens, brackets, braces := 0, 0, 0
	for ; index < len(tokens); index++ {
		token := tokens[index]
		if parens == 0 && brackets == 0 && braces == 0 {
			if token.text == "=" && (index+1 == len(tokens) || tokens[index+1].text != ">") {
				return index + 1
			}
			if token.text == ";" || token.text == "}" {
				return -1
			}
			if index > start && strings.ContainsAny(source[tokens[index-1].end:token.start], "\r\n") &&
				(token.text == "private" || token.text == "protected" || token.text == "public" || token.text == "readonly" ||
					token.kind == libraryTokenIdentifier && index+1 < len(tokens) &&
						(tokens[index+1].text == "=" || tokens[index+1].text == ":")) {
				return -1
			}
		}
		switch token.text {
		case "(":
			parens++
		case ")":
			parens--
		case "[":
			brackets++
		case "]":
			brackets--
		case "{":
			braces++
		case "}":
			braces--
		}
	}
	return -1
}

func coreNamespaces(tokens []libraryModuleToken) map[string]bool {
	namespaces := make(map[string]bool)
	for index := 0; index+5 < len(tokens); index++ {
		if tokens[index].text == "import" && tokens[index+1].text == "*" &&
			tokens[index+2].text == "as" && tokens[index+4].text == "from" &&
			tokens[index+5].value == "@angulus/core" {
			namespaces[tokens[index+3].text] = true
		}
	}
	return namespaces
}

func inputOutputInitializer(tokens []libraryModuleToken, index int, imports map[string]libraryExportReference, namespaces map[string]bool) bool {
	for index < len(tokens) && tokens[index].text == "(" {
		index++
	}
	if index >= len(tokens) {
		return false
	}
	target := tokens[index].text
	reference, imported := imports[target]
	if namespaces[target] && index+2 < len(tokens) && tokens[index+1].text == "." {
		index += 2
		reference = libraryExportReference{module: "@angulus/core", name: tokens[index].text}
		imported = true
	}
	if !imported || reference.module != "@angulus/core" || reference.name != "input" && reference.name != "output" {
		return false
	}
	index++
	if reference.name == "input" && index+1 < len(tokens) && tokens[index].text == "." && tokens[index+1].text == "required" {
		index += 2
	}
	if index < len(tokens) && tokens[index].text == "<" {
		depth := 0
		for index < len(tokens) {
			if tokens[index].text == "<" {
				depth++
			} else if tokens[index].text == ">" || tokens[index].text == ">>" || tokens[index].text == ">>>" {
				depth -= len(tokens[index].text)
			}
			index++
			if depth <= 0 {
				break
			}
		}
	}
	return index < len(tokens) && tokens[index].text == "("
}
