package project

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"angulus/internal/compiler"
)

type templateCheckMap struct {
	firstLine, lastLine    int
	sourceStart, sourceEnd int
}

type templateCheckWriter struct {
	code        strings.Builder
	line        int
	mappings    []templateCheckMap
	expressions []templateExpressionMap
}

type templateExpressionMap struct {
	GeneratedStart int   `json:"generatedStart"`
	SourceStart    int   `json:"sourceStart"`
	Offsets        []int `json:"offsets"`
}

func (w *templateCheckWriter) expressionMapped(text string, sourceStart, sourceEnd int, raw string, expressionStart int, locals map[string]bool, event bool) {
	qualified, offsets, _, err := rewriteExpressionWithUTF16Offsets(raw, locals, false, event)
	if err == nil {
		index := strings.Index(text, qualified)
		if index >= 0 {
			w.expressions = append(w.expressions, templateExpressionMap{
				GeneratedStart: utf16Length(w.code.String()) + utf16Length(text[:index]),
				SourceStart:    expressionStart, Offsets: offsets,
			})
		}
	}
	w.mapped(text, sourceStart, sourceEnd)
}

func (w *templateCheckWriter) write(text string) {
	w.code.WriteString(text)
	w.line += strings.Count(text, "\n")
}

func (w *templateCheckWriter) mapped(text string, sourceStart, sourceEnd int) {
	firstLine := w.line
	w.write(text)
	lastLine := w.line
	if strings.HasSuffix(text, "\n") {
		lastLine--
	}
	w.mappings = append(w.mappings, templateCheckMap{
		firstLine: firstLine, lastLine: lastLine, sourceStart: sourceStart, sourceEnd: sourceEnd,
	})
}

func templateCheckPath(render string) string {
	return strings.TrimSuffix(render, "render.generated.ts") + "template-check.generated.ts"
}

func templateCheckSource(component *Component, nodes []compiler.Node) string {
	path := templateCheckPath(component.Render)
	w := &templateCheckWriter{line: 1}
	w.write(generatedMarker + "\n")
	w.write(fmt.Sprintf("import type {%s as __AngulusContext} from %s;\n",
		component.Descriptor.Export, strconv.Quote(relativeImport(path, component.Module))))
	w.write("type __AngulusInputValue<T> = T extends { (): infer V; readonly __input: true } ? V : never;\n")
	w.write("type __AngulusOutputValue<T> = T extends { readonly __output: true; emit(value: infer V): void } ? V : never;\n")
	w.write("type __AngulusIsOutput<T> = T extends { readonly __output: true; emit(value: infer _V): void } ? true : never;\n")
	w.write("type __AngulusRequired<T> = { [K in keyof T]-?: T[K] extends { (): unknown; readonly __requiredInput: true } ? K : never }[keyof T];\n")
	w.write("declare function __angulusTwoWay(value: (() => string) & { set(value: string): void; update(update: (value: string) => string): void }): void;\n")
	dependencyNames := make(map[string]string, len(component.Imports))
	for index, dependency := range component.Imports {
		name := fmt.Sprintf("__AngulusChild%d", index)
		dependencyNames[dependency.Descriptor.Selector] = name
		w.write(fmt.Sprintf("import type {%s as %s} from %s;\n",
			dependency.Descriptor.Export, name, strconv.Quote(componentImportSpecifier(path, dependency, false))))
	}
	w.write("export function __angulusTemplateCheck(ctx: __AngulusContext): void {\nvoid ctx;\n")
	counter := 0
	var expression func(string, int, map[string]bool, bool, bool)
	expression = func(raw string, start int, locals map[string]bool, event, statement bool) {
		checked, offset, err := rewriteExpression(raw, locals, false, event)
		if err != nil {
			_ = offset
			checked = "undefined"
		}
		if statement {
			checked = "void (\n" + checked + "\n);\n"
		} else {
			checked += "\n"
		}
		w.expressionMapped(checked, start, start+utf16Length(raw), raw, start, locals, event)
	}
	var walk func([]compiler.Node, map[string]bool)
	walk = func(current []compiler.Node, locals map[string]bool) {
		for _, node := range current {
			switch node.Kind {
			case "interpolation":
				expression(node.Expression, node.ExprStart, locals, false, true)
			case "if":
				w.write("if (")
				expression(node.Expression, node.ExprStart, locals, false, false)
				w.write(") {\n")
				walk(node.Children, locals)
				w.write("} else {\n")
				walk(node.Otherwise, locals)
				w.write("}\n")
			case "for":
				w.write("for (const " + node.Item + " of (")
				expression(node.Expression, node.ExprStart, locals, false, false)
				w.write(")) {\nconst $index: number = 0;\nvoid $index;\n")
				trackLocals := copyLocals(locals)
				trackLocals[node.Item], trackLocals["$index"] = false, false
				expression(node.Track, node.TrackStart, trackLocals, false, true)
				bodyLocals := copyLocals(trackLocals)
				bodyLocals[node.Item], bodyLocals["$index"] = true, true
				walk(node.Children, bodyLocals)
				w.write("}\n")
			case "switch":
				w.write("switch (")
				expression(node.Expression, node.ExprStart, locals, false, false)
				w.write(") {\n")
				for _, branch := range node.Cases {
					if branch.Expression == "" {
						w.write("default: {\n")
					} else {
						w.write("case (")
						expression(branch.Expression, branch.ExprStart, locals, false, false)
						w.write("): {\n")
					}
					walk(branch.Children, locals)
					w.write("break;\n}\n")
				}
				w.write("}\n")
			case "element":
				counter++
				element := fmt.Sprintf("__angulusElement%d", counter)
				if child, ok := dependencyNames[node.Tag]; ok {
					w.write("{\nconst " + element + " = null! as " + child + ";\n")
					var bound []string
					for _, attr := range node.Attributes {
						kind, name := compilerAttributeKind(attr.Name)
						if kind == "static" || kind == "property" {
							bound = append(bound, strconv.Quote(name))
						}
					}
					excluded := strings.Join(bound, " | ")
					if excluded == "" {
						excluded = "never"
					}
					w.mapped(fmt.Sprintf("const __angulusRequired%d: Record<Exclude<__AngulusRequired<%s>, %s>, never> = {};\nvoid __angulusRequired%d;\n",
						counter, child, excluded, counter), node.Start, node.End)
					for _, attr := range node.Attributes {
						kind, name := compilerAttributeKind(attr.Name)
						switch kind {
						case "property":
							value := checkedExpression(attr.Value, locals, false)
							w.expressionMapped(fmt.Sprintf("const __angulusInput%d: __AngulusInputValue<typeof %s[%s]> = (\n%s\n);\nvoid __angulusInput%d;\n",
								counter, element, strconv.Quote(name), value, counter), attr.Start, attr.ValueStart+utf16Length(attr.Value), attr.Value, attr.ValueStart, locals, false)
						case "static":
							w.mapped(fmt.Sprintf("const __angulusStatic%d: __AngulusInputValue<typeof %s[%s]> = %s;\nvoid __angulusStatic%d;\n",
								counter, element, strconv.Quote(name), strconv.Quote(attr.Value), counter), attr.Start, attr.ValueStart+utf16Length(attr.Value))
						case "event":
							value := checkedExpression(attr.Value, locals, true)
							w.expressionMapped(fmt.Sprintf("const __angulusOutput%d: __AngulusIsOutput<typeof %s[%s]> = true;\nvoid __angulusOutput%d;\n(($event: __AngulusOutputValue<typeof %s[%s]>) => { void $event; void (\n%s\n); });\n",
								counter, element, strconv.Quote(name), counter, element, strconv.Quote(name), value),
								attr.Start, attr.ValueStart+utf16Length(attr.Value), attr.Value, attr.ValueStart, locals, true)
						case "twoway":
							w.mapped("/* child-component two-way bindings are rejected by the template generator */\n", attr.Start, node.End)
						}
					}
				} else {
					w.write("{\nconst " + element + " = document.createElement(" + strconv.Quote(node.Tag) + ");\n")
					custom := strings.Contains(node.Tag, "-") && !standardElements[strings.ToLower(node.Tag)]
					for _, attr := range node.Attributes {
						kind, name := compilerAttributeKind(attr.Name)
						switch kind {
						case "property":
							value := checkedExpression(attr.Value, locals, false)
							if custom || strings.HasPrefix(name, "attr.") {
								w.expressionMapped("void ("+value+");\n", attr.Start, attr.ValueStart+utf16Length(attr.Value), attr.Value, attr.ValueStart, locals, false)
								continue
							}
							property := name
							if property == "class" {
								property = "className"
							}
							w.expressionMapped(fmt.Sprintf("%s[%s] = (\n%s\n);\n", element, strconv.Quote(property), value),
								attr.Start, attr.ValueStart+utf16Length(attr.Value), attr.Value, attr.ValueStart, locals, false)
						case "event":
							value := checkedExpression(attr.Value, locals, true)
							if custom {
								w.expressionMapped(fmt.Sprintf("%s.addEventListener(%s, ($event: Event) => { void $event; void (\n%s\n); });\n",
									element, strconv.Quote(name), value), attr.Start, attr.ValueStart+utf16Length(attr.Value), attr.Value, attr.ValueStart, locals, true)
							} else {
								w.expressionMapped(fmt.Sprintf("%s.addEventListener(%s, ($event) => { void $event; void (\n%s\n); });\n",
									element, strconv.Quote(name), value), attr.Start, attr.ValueStart+utf16Length(attr.Value), attr.Value, attr.ValueStart, locals, true)
							}
						case "twoway":
							value := checkedExpression(attr.Value, locals, false)
							w.expressionMapped("__angulusTwoWay(\n"+value+"\n);\n", attr.Start, attr.ValueStart+utf16Length(attr.Value), attr.Value, attr.ValueStart, locals, false)
						}
					}
					walk(node.Children, locals)
				}
				w.write("}\n")
			}
		}
	}
	walk(nodes, map[string]bool{})
	w.write("}\n")
	for _, mapping := range w.mappings {
		w.write(fmt.Sprintf("// @angulus-template-map %d %d %d %d\n",
			mapping.firstLine, mapping.lastLine, mapping.sourceStart, mapping.sourceEnd))
	}
	for _, mapping := range w.expressions {
		data, _ := json.Marshal(mapping)
		w.write("// @angulus-expression-map " + string(data) + "\n")
	}
	return w.code.String()
}

func checkedExpression(raw string, locals map[string]bool, event bool) string {
	value, _, err := rewriteExpression(raw, locals, false, event)
	if err != nil {
		return "undefined"
	}
	return value
}

func utf16Length(value string) int {
	units := 0
	for _, r := range value {
		if r > 0xffff {
			units += 2
		} else {
			units++
		}
	}
	return units
}
