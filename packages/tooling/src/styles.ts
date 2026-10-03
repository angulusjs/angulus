import postcss from "postcss";
import selectorParser from "postcss-selector-parser";
import valueParser from "postcss-value-parser";
import { dirname, resolve } from "node:path";
import { CompilationError, diagnostic } from "./diagnostics.js";
import type { SourceMapInput, AngulusSourceMap } from "./sourcemap.js";

export function scopeStyles(source: string, file: string, scopeId: string): { code: string; map: SourceMapInput } {
  try {
    const root = postcss.parse(source, { from: file });
    const keyframes = new Map();
    root.walkAtRules(rule => {
      if (rule.name === "import") throw rule.error("Scoped styles do not support @import; import global CSS explicitly from TypeScript.");
      if (/keyframes$/i.test(rule.name)) {
        const name = valueParser(rule.params);
        const parts = name.nodes.filter(node => node.type !== "space" && node.type !== "comment");
        if (parts.length !== 1 || !["word", "string"].includes(parts[0].type)) throw rule.error("Expected one keyframe name.");
        const original = parts[0].value;
        keyframes.set(original, `${original}-${scopeId}`);
        parts[0].value = `${original}-${scopeId}`;
        rule.params = name.toString();
      }
    });
    root.walkRules(rule => {
      for (let parent = rule.parent as postcss.Container | undefined; parent; parent = parent.parent as postcss.Container | undefined) {
        if (parent.type === "atrule" && "name" in parent && typeof parent.name === "string" && /keyframes$/i.test(parent.name)) return;
      }
      rule.selector = selectorParser(selectors => {
        selectors.walk(node => {
          if (node.type === "nesting") throw rule.error("CSS nesting is not supported in scoped styles; use explicit selectors.");
          if (node.type === "pseudo" && [":host", ":global", ":root"].includes(node.value)) throw rule.error(`${node.value} is not supported in scoped styles.`);
        });
        selectors.each(selector => {
          type SelectorChild = Exclude<selectorParser.Node, selectorParser.Selector>;
          let compound: SelectorChild[] = [];
          const finish = () => {
            if (!compound.length) return;
            const anchor = compound.find(node => node.type === "pseudo" && node.value.startsWith("::"));
            const attribute = selectorParser.attribute({ attribute: `data-${scopeId}`, value: "", raws: {} });
            const last = compound.at(-1);
            if (anchor) selector.insertBefore(anchor as SelectorChild, attribute);
            else if (last) selector.insertAfter(last, attribute);
            compound = [];
          };
          for (const node of [...selector.nodes]) {
            if (node.type === "combinator") finish();
            else compound.push(node as SelectorChild);
          }
          finish();
        });
      }).processSync(rule.selector);
    });
    root.walkDecls(declaration => {
      const value = valueParser(declaration.value);
      if (/^(?:-webkit-)?animation(?:-name)?$/.test(declaration.prop)) value.nodes.forEach(node => {
        if (["word", "string"].includes(node.type) && keyframes.has(node.value)) {
          if (!declaration.prop.endsWith("-name") && node.type === "word" && /^(ease|ease-in|ease-out|ease-in-out|linear|step-start|step-end|infinite|normal|reverse|alternate|alternate-reverse|none|forwards|backwards|both|running|paused|initial|inherit|unset|revert|auto)$/.test(node.value)) {
            throw declaration.error("Ambiguous keyframe name in animation shorthand; use animation-name or a non-keyword name.");
          }
          node.value = keyframes.get(node.value);
        }
      });
      value.walk(node => {
        if (node.type === "function" && node.value.toLowerCase() === "url" && node.nodes.length === 1) {
          const url = node.nodes[0];
          if ((url.type === "word" || url.type === "string") && !/^(?:[a-z]+:|\/|#)/i.test(url.value)) {
            const value = `/@fs/${resolve(dirname(file), url.value).replaceAll("\\", "/")}`;
            if (url.type === "word") {
              node.nodes[0] = { ...url, type: "string", quote: '"', value };
            } else {
              url.quote = '"';
              url.value = value;
            }
          }
        }
      });
      declaration.value = value.toString();
    });
    const result = root.toResult({ to: file, map: { inline: false, annotation: false, sourcesContent: true } });
    const map = result.map.toJSON();
    return { code: result.css, map: { ...map, version: Number(map.version) } as AngulusSourceMap };
  } catch (error) {
    if (error instanceof postcss.CssSyntaxError) {
      const lines = source.split("\n");
      const start = lines.slice(0, (error.line ?? 1) - 1).reduce((sum, line) => sum + line.length + 1, 0) + (error.column ?? 1) - 1;
      throw new CompilationError([diagnostic(file, source, start, "F_CSS", error.reason)]);
    }
    throw new CompilationError([diagnostic(file, source, 0, "F_CSS", error instanceof Error ? error.message : String(error))]);
  }
}
