import { expression } from "./expressions.mjs";
import { CompilationError, diagnostic } from "./diagnostics.mjs";

const htmlTags = new Set(("a abbr address area article aside audio b base bdi bdo blockquote body br button canvas caption cite code col colgroup data datalist dd del details dfn dialog div dl dt em embed fieldset figcaption figure footer form h1 h2 h3 h4 h5 h6 head header hgroup hr html i iframe img input ins kbd label legend li link main map mark menu meta meter nav noscript object ol optgroup option output p picture pre progress q rp rt ruby s samp script search section select slot small source span strong style sub summary sup table tbody td template textarea tfoot th thead time title tr track u ul var video wbr").split(" "));
const forbiddenTags = new Set(["script", "style", "iframe", "object", "embed", "template", "link", "base", "meta"]);
const unsafeProperties = new Set(["innerhtml", "outerhtml", "srcdoc"]);

export function prepareTemplate(nodes, component, dependencies) {
  const errors = [];
  const { templateFile: file, template: source } = component;
  const components = Object.fromEntries(dependencies.map(dep => [dep.selector, dep.local]));
  const fail = (node, message) => errors.push(diagnostic(file, source, node.start ?? 0, "F_TEMPLATE", message, node.end));
  function transform(raw, start, locals, runtime, event = false) {
    try { return expression(raw, { file, source, start, locals, runtime, event }).code; }
    catch (error) {
      if (!(error instanceof CompilationError)) throw error;
      errors.push(...error.diagnostics);
      return "undefined";
    }
  }
  function walk(items, locals = new Map()) {
    return items.map(node => {
      const output = { ...node };
      if (node.expression) output.expression = transform(node.expression, node.exprStart, locals, true);
      if (node.kind === "element") {
        if (forbiddenTags.has(node.tag)) fail(node, `Element <${node.tag}> is not allowed in templates.`);
        if (!htmlTags.has(node.tag) && !components[node.tag] && !component.customElements.includes(node.tag)) {
          fail(node, `Unknown component <${node.tag}>. Add it to @Component imports, or explicitly allow a custom element.`);
        }
        const seen = new Set();
        output.attributes = (node.attributes ?? []).map(attribute => {
          const attr = { ...attribute };
          const { name, value, valueStart } = attribute;
          if (seen.has(name)) fail(attribute, `Duplicate attribute '${name}'.`);
          seen.add(name);
          const property = name.replace(/^[[(]+|[\])]+$/g, "").toLowerCase();
          if (unsafeProperties.has(property) || /^on/i.test(property)) fail(attribute, `Unsafe property '${property}' is not supported.`);
          if (!components[node.tag] && name.startsWith("[") && ["srcset", "imagesrcset", "attr.srcset", "attr.imagesrcset"].includes(property)) fail(attribute, "Dynamic srcset bindings are not supported; use individually validated src URLs.");
          if (name.startsWith("[(")) {
            if (node.tag !== "input" || name !== "[(value)]" || components[node.tag]) fail(attribute, "Two-way binding is supported only for [(value)] on text inputs.");
            const type = (node.attributes ?? []).find(item => item.name === "type");
            if (type && type.value !== "text") fail(attribute, "Two-way binding is supported only for input type=\"text\".");
            if ((node.attributes ?? []).some(item => item.name === "[type]")) fail(attribute, "A two-way text input cannot have a dynamic type.");
            if ((node.attributes ?? []).some(item => item.name === "[value]" || item.name === "(input)")) fail(attribute, "[(value)] cannot be combined with [value] or (input).");
            attr.value = transform(value, valueStart, locals, true);
          } else if (name.startsWith("[")) attr.value = transform(value, valueStart, locals, true);
          else if (name.startsWith("(")) attr.value = transform(value, valueStart, locals, true, true);
          else if (value?.includes("{{")) fail(attribute, "Attribute interpolation is not supported; use a property binding.");
          return attr;
        });
      }
      if (node.kind === "for") {
        if (!/^[A-Za-z_$][\w$]*$/.test(node.item) || node.item.startsWith("__") || ["ctx", "$event", "$index", "host", "scope"].includes(node.item)) fail(node, "Invalid or reserved loop variable.");
        const loop = new Map(locals);
        loop.set(node.item, "plain");
        loop.set("$index", "plain");
        output.track = transform(node.track, node.trackStart, loop, true);
        loop.set(node.item, "signal");
        loop.set("$index", "signal");
        output.children = walk(node.children ?? [], loop);
      } else if (node.children) output.children = walk(node.children, locals);
      if (node.otherwise) output.otherwise = walk(node.otherwise, locals);
      if (node.cases) output.cases = node.cases.map(branch => ({
        ...branch,
        expression: branch.expression ? transform(branch.expression, branch.exprStart, locals, true) : "",
        children: walk(branch.children, locals),
      }));
      return output;
    });
  }
  const runtimeNodes = walk(nodes);
  if (errors.length) throw new CompilationError(errors);
  return { nodes: runtimeNodes, components };
}

export function checkerSource(nodes, component, dependencies) {
  const { templateFile: file, template: source } = component;
  const byTag = new Map(dependencies.map(dep => [dep.selector, dep]));
  let code = `import { ${component.name} } from ${JSON.stringify(component.file)};\n`;
  code += 'import type { Input, Output, WritableSignal, RequiredInput } from "@angulus/core";\n';
  code += 'export type __InputValue<T> = T extends Input<infer V> ? V : never;\n';
  code += 'export type __OutputValue<T> = T extends Output<infer V> ? V : never;\n';
  code += 'export type __IsOutput<T> = T extends Output<infer _V> ? true : never;\n';
  code += 'export type __Required<T> = { [K in keyof T]-?: T[K] extends RequiredInput<unknown> ? K : never }[keyof T];\n';
  code += 'export declare function __twoWay(value: WritableSignal<string>): void;\n';
  for (const [index, dep] of dependencies.entries()) {
    dep.checkerName = `__Child${index}`;
    code += `import { ${dep.name} as ${dep.checkerName} } from ${JSON.stringify(dep.file)};\n`;
  }
  code += `export function __check(ctx: ${component.name}) {\nvoid ctx;\n`;
  const mappings = [];
  const add = (text, start) => {
    if (start !== undefined) mappings.push({ from: code.length, to: code.length + text.length, start });
    code += text;
  };
  const expr = (raw, start, locals, event = false) => {
    const result = expression(raw, { file, source, start, locals, event });
    mappings.push({ from: code.length, to: code.length + result.code.length, start, offsets: result.offsets });
    code += result.code;
  };
  let counter = 0;
  function walk(items, locals = new Map()) {
    for (const node of items) {
      if (node.kind === "interpolation") {
        add("void ("); expr(node.expression, node.exprStart, locals); add(");\n");
      } else if (node.kind === "if") {
        add("if ("); expr(node.expression, node.exprStart, locals); add(") {\n");
        walk(node.children ?? [], locals);
        add("} else {\n"); walk(node.otherwise ?? [], locals); add("}\n");
      } else if (node.kind === "for") {
        add(`for (const ${node.item} of (`); expr(node.expression, node.exprStart, locals); add(")) {\n");
        const loop = new Map(locals).set(node.item, "plain").set("$index", "plain");
        add(`const $index: number = 0;\nvoid $index;\nvoid ${node.item};\nvoid (`); expr(node.track, node.trackStart, loop); add(");\n");
        walk(node.children ?? [], loop); add("}\n");
      } else if (node.kind === "switch") {
        add("switch ("); expr(node.expression, node.exprStart, locals); add(") {\n");
        for (const branch of node.cases ?? []) {
          if (branch.expression) { add("case ("); expr(branch.expression, branch.exprStart, locals); add("): {\n"); }
          else add("default: {\n");
          walk(branch.children, locals); add("break;\n}\n");
        }
        add("}\n");
      } else if (node.kind === "element") {
        const id = `__element${counter++}`;
        const child = byTag.get(node.tag);
        const custom = component.customElements.includes(node.tag);
        add("{\n");
        if (child) {
          add(`const ${id} = null! as ${child.checkerName};\n`);
          const bound = (node.attributes ?? []).filter(a => !a.name.startsWith("(") && !a.name.startsWith("[(")).map(a => a.name.startsWith("[") ? a.name.slice(1, -1) : a.name);
          // Assignability of this mapped type checks every required input, including inherited ones.
          add(`const __required: Record<Exclude<__Required<${child.checkerName}>, ${bound.length ? bound.map(JSON.stringify).join(" | ") : "never"}>, never> = {};\nvoid __required;\n`, node.start);
        } else add(`const ${id} = document.createElement(${JSON.stringify(node.tag)});\n`);
        add(`void ${id};\n`);
        for (const attr of node.attributes ?? []) {
          if (attr.name.startsWith("[(")) {
            add("__twoWay("); expr(attr.value, attr.valueStart, locals); add(");\n");
          } else if (attr.name.startsWith("[")) {
            const property = attr.name.slice(1, -1);
            const inputName = `__input${counter++}`;
            if (child) add(`const ${inputName}: __InputValue<typeof ${id}[${JSON.stringify(property)}]> = `, attr.start);
            else if (custom) add("void (");
            else add(`${id}[${JSON.stringify(property)}] = `, attr.start);
            expr(attr.value, attr.valueStart, locals); add(custom && !child ? ");\n" : ";\n");
            if (child) add(`void ${inputName};\n`);
          } else if (attr.name.startsWith("(")) {
            const eventName = attr.name.slice(1, -1);
            if (child) {
              const outputName = `__output${counter++}`;
              add(`const ${outputName}: __IsOutput<typeof ${id}[${JSON.stringify(eventName)}]> = true;\nvoid ${outputName};\n`, attr.start);
              add(`(($event: __OutputValue<typeof ${id}[${JSON.stringify(eventName)}]>) => { `, attr.start);
            }
            else if (custom) add("(($event: Event) => { ");
            else add(`${id}.addEventListener(${JSON.stringify(eventName)}, ($event) => { `);
            add("void $event; ");
            expr(attr.value, attr.valueStart, locals, true); add("; });\n");
          } else if (child) {
            const staticName = `__static${counter++}`;
            add(`const ${staticName}: __InputValue<typeof ${id}[${JSON.stringify(attr.name)}]> = ${JSON.stringify(attr.value)};\nvoid ${staticName};\n`, attr.start);
          }
        }
        if (!child) walk(node.children ?? [], locals);
        add("}\n");
      }
    }
  }
  walk(nodes);
  add("}\n");
  return { code, mappings, file, source };
}
