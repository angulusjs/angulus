package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"angulus/internal/compiler"
)

type generatedFile struct {
	path string
	body string
}

func relativeImport(from, target string) string {
	rel, err := filepath.Rel(filepath.Dir(from), target)
	if err != nil {
		rel = target
	}
	rel = filepath.ToSlash(rel)
	ext := filepath.Ext(rel)
	if ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" {
		rel = strings.TrimSuffix(rel, ext)
	}
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	return rel
}

// qualifyExpression preserves source text while qualifying free references.
func qualifyExpression(expr string, locals map[string]bool) (string, string) {
	result, _, err := rewriteExpression(expr, locals, true, false)
	if err != nil {
		return "", err.Error()
	}
	return result, ""
}

type qualifiedExpressionMapping struct {
	Start      int
	Expression string
	Generated  int
	Source     int
}

func qualifyNodes(nodes []compiler.Node, file string, source string, diagnostics *[]Diagnostic, locals map[string]bool,
	mappings *[]qualifiedExpressionMapping) {
	qualify := func(value string, start, end int, exprLocals map[string]bool, event bool) string {
		_ = end
		result, offsets, offset, err := rewriteExpressionWithOffsets(value, exprLocals, true, event)
		if err != nil {
			sourceOffset := byteOffsetForUTF16(source, start) + offset
			addDiagnostic(diagnostics, file, source, "P0400", err.Error(), sourceOffset)
			return value
		}
		for _, mapping := range offsets {
			*mappings = append(*mappings, qualifiedExpressionMapping{
				Start:      start,
				Expression: result,
				Generated:  utf16Length(result[:mapping.Generated]),
				Source:     start + utf16Length(value[:mapping.Source]),
			})
		}
		return result
	}
	for i := range nodes {
		n := &nodes[i]
		switch n.Kind {
		case "interpolation", "if", "switch":
			n.Expression = qualify(n.Expression, n.ExprStart, n.End, locals, false)
		case "for":
			n.Expression = qualify(n.Expression, n.ExprStart, n.End, locals, false)
			trackLocals := copyLocals(locals)
			trackLocals[n.Item] = false
			trackLocals["$index"] = false
			n.Track = qualify(n.Track, n.TrackStart, n.End, trackLocals, false)
			bodyLocals := copyLocals(locals)
			bodyLocals[n.Item] = true
			bodyLocals["$index"] = true
			qualifyNodes(n.Children, file, source, diagnostics, bodyLocals, mappings)
			continue
		case "element":
			for ai := range n.Attributes {
				attr := &n.Attributes[ai]
				kind, _ := compilerAttributeKind(attr.Name)
				attrLocals := copyLocals(locals)
				if kind == "event" {
					attrLocals["$event"] = false
				}
				if kind == "property" || kind == "event" || kind == "twoway" {
					attr.Value = qualify(attr.Value, attr.ValueStart, n.End, attrLocals, kind == "event")
				}
			}
		}
		qualifyNodes(n.Children, file, source, diagnostics, locals, mappings)
		qualifyNodes(n.Otherwise, file, source, diagnostics, locals, mappings)
		for ci := range n.Cases {
			if n.Cases[ci].Expression != "" {
				n.Cases[ci].Expression = qualify(n.Cases[ci].Expression, n.Cases[ci].ExprStart, n.End, locals, false)
			}
			qualifyNodes(n.Cases[ci].Children, file, source, diagnostics, locals, mappings)
		}
	}
}

func copyLocals(locals map[string]bool) map[string]bool {
	copy := make(map[string]bool, len(locals)+2)
	for name, signal := range locals {
		copy[name] = signal
	}
	return copy
}

func compilerAttributeKind(name string) (string, string) {
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

func byteOffsetForUTF16(s string, units int) int {
	if units <= 0 {
		return 0
	}
	count := 0
	for i, r := range s {
		count += len(utf16.Encode([]rune{r}))
		if count >= units {
			return i + len(string(r))
		}
	}
	return len(s)
}

func generateComponent(c *Component, diagnostics *[]Diagnostic) []generatedFile {
	templateBytes, err := os.ReadFile(c.Template)
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{File: c.Template, Code: "P0401", Message: err.Error(), Severity: "error"})
		return nil
	}
	template := string(templateBytes)
	parsed := compiler.Parse(template, c.Template)
	for _, d := range parsed.Diagnostics {
		*diagnostics = append(*diagnostics, fromCompilerDiagnostic(d, template))
	}
	validateComponentTags(parsed.Nodes, c, template, diagnostics)
	checkNodes := compiler.Parse(template, c.Template).Nodes
	var expressionMappings []qualifiedExpressionMapping
	qualifyNodes(parsed.Nodes, c.Template, template, diagnostics, map[string]bool{}, &expressionMappings)
	checkFile := templateCheckPath(c.Render)
	checkBody := templateCheckSource(c, checkNodes)

	componentImports := make(map[string]string)
	var renderImports []string
	var files []generatedFile
	if len(c.Styles) > 0 {
		scoped, cssMappings, cssSources, cssDiagnostics := scopeCSSWithMappings(c.Styles, c.ScopeID)
		*diagnostics = append(*diagnostics, cssDiagnostics...)
		if hasErrors(cssDiagnostics) {
			return nil
		}
		c.ScopedCSS = strings.TrimSuffix(c.Path, ".component.json") + ".component.styles.generated.css"
		cssBody := generatedCSSMarker + "\n" + scoped
		prefixUnits := utf16Length(generatedCSSMarker + "\n")
		cssPoints := make([]sourceMapPoint, 0, len(cssMappings))
		for _, mapping := range cssMappings {
			cssPoints = append(cssPoints, sourceMapPoint{
				Generated: prefixUnits + mapping.Generated,
				Source:    mapping.File,
				Original:  mapping.Source,
			})
		}
		files = append(files, generatedFile{c.ScopedCSS, inlineSourceMap(c.ScopedCSS, cssBody, cssPoints, cssSources)})
	}
	for i, dependency := range c.Imports {
		alias := fmt.Sprintf("__angulusComponent%d", i)
		componentImports[dependency.Descriptor.Selector] = alias
		renderImports = append(renderImports, fmt.Sprintf("import {%s as %s} from %s;", dependency.Descriptor.Export, alias,
			strconv.Quote(componentImportSpecifier(c.Render, dependency, true))))
	}
	sort.Strings(renderImports)
	result := compiler.Generate(parsed.Nodes, c.ScopeID, componentImports)
	for _, d := range result.Diagnostics {
		diagnostic := fromCompilerDiagnostic(d, template)
		if diagnostic.File == "" {
			diagnostic.File = c.Template
		}
		*diagnostics = append(*diagnostics, diagnostic)
	}

	renderPrefix := generatedMarker + "\n// @ts-nocheck\nimport * as __f from \"@angulus/core\";\n"
	renderPrefix += fmt.Sprintf("import type { __angulusTemplateCheck } from %s;\n", strconv.Quote(relativeImport(c.Render, checkFile)))
	renderPrefix += "type __AngulusTemplateCheck = typeof __angulusTemplateCheck;\n"
	renderPrefix += strings.Join(renderImports, "\n") + "\n"
	renderPrefix += "export const render: __f.Render<any> = "
	renderBody := renderPrefix + result.Code + "\n"
	renderPoints := make([]sourceMapPoint, 0, len(result.Mappings))
	for _, mapping := range result.Mappings {
		renderPoints = append(renderPoints, sourceMapPoint{
			Generated: utf16Length(renderPrefix) + mapping.Generated,
			Source:    c.Template,
			Original:  mapping.Source,
		})
		for _, expression := range expressionMappings {
			if mapping.Source != expression.Start {
				continue
			}
			generatedByte := byteOffsetForUTF16(result.Code, mapping.Generated)
			if generatedByte+len(expression.Expression) > len(result.Code) ||
				!strings.HasPrefix(result.Code[generatedByte:], expression.Expression) {
				continue
			}
			renderPoints = append(renderPoints, sourceMapPoint{
				Generated: utf16Length(renderPrefix) + mapping.Generated + expression.Generated,
				Source:    c.Template,
				Original:  expression.Source,
			})
		}
	}
	renderBody = inlineSourceMap(c.Render, renderBody, renderPoints, map[string]string{c.Template: template})

	module, err := os.ReadFile(c.Module)
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{File: c.Module, Code: "P0401", Message: err.Error(), Severity: "error"})
		return nil
	}
	facadeBody := generatedMarker + "\n"
	facadeBody += "import { defineComponent as __defineComponent } from \"@angulus/core\";\n"
	facadeBody += fmt.Sprintf("import {%s as __angulusCtor} from %s;\n", c.Descriptor.Export, strconv.Quote(relativeImport(c.Facade, c.Module)))
	facadeBody += fmt.Sprintf("import { render as __angulusRender } from %s;\n", strconv.Quote(relativeImport(c.Facade, c.Render)))
	if c.ScopedCSS != "" {
		facadeBody += "// @ts-ignore esbuild resolves this generated stylesheet import\n"
		facadeBody += fmt.Sprintf("import %s;\n", strconv.Quote(relativeImport(c.Facade, c.ScopedCSS)))
	}
	facadeBody += fmt.Sprintf("__defineComponent(__angulusCtor, __angulusRender, %s);\n", strconv.Quote(c.ScopeID))
	facadeBody += fmt.Sprintf("export { __angulusCtor as %s };\n", c.Descriptor.Export)
	facadeMappings := []sourceMapPoint{
		{Generated: utf16Length(generatedLineOffset(facadeBody, "import {"+c.Descriptor.Export+" as __angulusCtor}")), Source: c.Module, Original: moduleExportOffset(string(module), c.Descriptor.Export)},
		{Generated: utf16Length(generatedLineOffset(facadeBody, "__defineComponent(")), Source: c.Module, Original: moduleExportOffset(string(module), c.Descriptor.Export)},
		{Generated: utf16Length(generatedLineOffset(facadeBody, "export { __angulusCtor as")), Source: c.Module, Original: moduleExportOffset(string(module), c.Descriptor.Export)},
	}
	facadeBody = inlineSourceMap(c.Facade, facadeBody, facadeMappings, map[string]string{c.Module: string(module)})
	files = append(files, generatedFile{c.Render, renderBody}, generatedFile{c.Facade, facadeBody},
		generatedFile{checkFile, checkBody})
	return files
}

func generatedLineOffset(source, linePrefix string) string {
	offset := strings.Index(source, linePrefix)
	if offset < 0 {
		return ""
	}
	return source[:offset]
}

func moduleExportOffset(source, export string) int {
	patterns := []string{
		"export class " + export,
		"export abstract class " + export,
		"export function " + export,
		"export const " + export,
		"export let " + export,
		"export var " + export,
		"export interface " + export,
		"export type " + export,
		"export enum " + export,
	}
	for _, pattern := range patterns {
		if offset := strings.Index(source, pattern); offset >= 0 {
			return utf16Length(source[:offset])
		}
	}
	return 0
}

func fromCompilerDiagnostic(d compiler.Diagnostic, source string) Diagnostic {
	diagnostic := Diagnostic{File: d.File, Start: d.Start, End: d.End, Code: d.Code, Message: d.Message, Severity: d.Severity}
	setSourceLocation(&diagnostic, source)
	return diagnostic
}

var standardElements = map[string]bool{
	"a": true, "abbr": true, "address": true, "area": true, "article": true, "aside": true,
	"audio": true, "b": true, "base": true, "bdi": true, "bdo": true, "blockquote": true, "body": true, "br": true,
	"button": true, "canvas": true, "caption": true, "cite": true, "code": true, "col": true, "colgroup": true,
	"data": true, "datalist": true, "dd": true, "del": true, "details": true, "dfn": true,
	"dialog": true, "div": true, "dl": true, "dt": true, "em": true, "embed": true, "fieldset": true,
	"figcaption": true, "figure": true, "footer": true, "form": true, "h1": true, "h2": true, "hgroup": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "head": true, "header": true, "hr": true,
	"html": true, "i": true, "iframe": true, "img": true, "input": true, "ins": true, "kbd": true,
	"label": true, "legend": true, "li": true, "link": true, "main": true, "map": true,
	"mark": true, "menu": true, "meta": true, "meter": true, "nav": true, "noscript": true,
	"object": true, "ol": true, "optgroup": true, "option": true, "output": true, "p": true,
	"picture": true, "pre": true, "progress": true, "q": true, "rp": true, "rt": true,
	"ruby": true, "s": true, "samp": true, "script": true, "search": true, "section": true,
	"select": true, "slot": true, "small": true, "source": true, "span": true, "strong": true, "style": true,
	"sub": true, "summary": true, "sup": true, "table": true, "tbody": true, "td": true,
	"template": true, "textarea": true, "tfoot": true, "th": true, "thead": true, "time": true,
	"title": true, "tr": true, "track": true, "u": true, "ul": true, "var": true, "video": true,
	"wbr": true, "svg": true, "path": true, "g": true, "circle": true, "rect": true, "line": true,
	"polyline": true, "polygon": true, "ellipse": true, "text": true, "defs": true, "use": true,
}

func validateComponentTags(nodes []compiler.Node, component *Component, source string, diagnostics *[]Diagnostic) {
	imported := make(map[string]bool, len(component.Imports))
	for _, dependency := range component.Imports {
		imported[dependency.Descriptor.Selector] = true
	}
	allowed := make(map[string]bool, len(component.Descriptor.CustomElements))
	for _, tag := range component.Descriptor.CustomElements {
		allowed[tag] = true
	}
	var visit func([]compiler.Node)
	visit = func(current []compiler.Node) {
		for _, node := range current {
			if node.Kind == "element" {
				for _, attr := range node.Attributes {
					kind, name := compilerAttributeKind(attr.Name)
					if kind == "property" && !imported[node.Tag] && (strings.EqualFold(name, "srcset") ||
						strings.EqualFold(name, "imagesrcset") || strings.EqualFold(name, "attr.srcset") ||
						strings.EqualFold(name, "attr.imagesrcset")) {
						diagnostic := Diagnostic{File: component.Template, Start: attr.Start, End: attr.Start + utf16Length(attr.Name),
							Code: "P0412", Message: "Dynamic srcset bindings are not supported; bind an individual src URL instead", Severity: "error"}
						setSourceLocation(&diagnostic, source)
						*diagnostics = append(*diagnostics, diagnostic)
					}
					if strings.Contains(attr.Value, "{{") {
						diagnostic := Diagnostic{File: component.Template, Start: attr.Start, End: attr.Start + utf16Length(attr.Name),
							Code: "P0413", Message: "Attribute interpolation is not supported; use a property binding", Severity: "error"}
						setSourceLocation(&diagnostic, source)
						*diagnostics = append(*diagnostics, diagnostic)
					}
				}
			}
			if node.Kind == "element" &&
				!standardElements[strings.ToLower(node.Tag)] && !imported[node.Tag] && !allowed[node.Tag] {
				diagnostic := Diagnostic{File: component.Template, Start: node.Start, End: node.End,
					Code: "P0410", Message: fmt.Sprintf("Unknown component or custom element %q; add a descriptor import or declare it in customElements", node.Tag), Severity: "error"}
				setSourceLocation(&diagnostic, source)
				*diagnostics = append(*diagnostics, diagnostic)
			}
			visit(node.Children)
			visit(node.Otherwise)
			for _, branch := range node.Cases {
				visit(branch.Children)
			}
		}
	}
	visit(nodes)
}

type generatedRegistry struct {
	Files []string `json:"files"`
}

func writeGenerated(result *Result) {
	if result.hasErrors() {
		return
	}
	var files []generatedFile
	for _, c := range result.Components {
		files = append(files, generateComponent(c, &result.Diagnostics)...)
	}
	if result.hasErrors() {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	for _, file := range files {
		existing, err := os.ReadFile(file.path)
		if err == nil && !isGeneratedFile(file.path, string(existing)) {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: file.path, Code: "P0402",
				Message: "Refusing to overwrite a user-owned file; move it or remove it to allow generation", Severity: "error"})
			return
		}
		if err != nil && !os.IsNotExist(err) {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: file.path, Code: "P0403", Message: err.Error(), Severity: "error"})
			return
		}
		if err == nil && string(existing) == file.body {
			continue
		}
	}
	registryPath := filepath.Join(result.Root, ".angulus", "project-generated.json")
	var old generatedRegistry
	if data, err := os.ReadFile(registryPath); err == nil {
		if err := json.Unmarshal(data, &old); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: registryPath, Code: "P0404", Message: "Invalid generated-file registry: " + err.Error(), Severity: "error"})
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(registryPath), 0755); err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: registryPath, Code: "P0403", Message: err.Error(), Severity: "error"})
		return
	}
	next := generatedRegistry{}
	current := map[string]bool{}
	for _, file := range files {
		rel, _ := filepath.Rel(result.Root, file.path)
		rel = filepath.ToSlash(rel)
		next.Files = append(next.Files, rel)
		current[rel] = true
		if existing, err := os.ReadFile(file.path); err == nil && string(existing) == file.body {
			continue
		}
		if err := atomicWrite(file.path, []byte(file.body)); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: file.path, Code: "P0403", Message: err.Error(), Severity: "error"})
			return
		}
	}
	for _, oldName := range old.Files {
		if current[oldName] || filepath.IsAbs(oldName) || strings.HasPrefix(filepath.Clean(oldName), "..") {
			continue
		}
		oldPath := filepath.Join(result.Root, filepath.FromSlash(oldName))
		if data, err := os.ReadFile(oldPath); err == nil && isGeneratedFile(oldPath, string(data)) {
			_ = os.Remove(oldPath)
		}
	}
	registryData, _ := json.MarshalIndent(next, "", "  ")
	if err := atomicWrite(registryPath, append(registryData, '\n')); err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: registryPath, Code: "P0403", Message: err.Error(), Severity: "error"})
	}
}

func isGeneratedFile(path, source string) bool {
	marker := generatedMarker
	if strings.HasSuffix(path, ".css") {
		marker = generatedCSSMarker
	}
	return strings.HasPrefix(source, marker)
}

func atomicWrite(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".angulus-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
