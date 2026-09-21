import { readFile, writeFile, mkdir, readdir, unlink } from "node:fs/promises";
import { createHash } from "node:crypto";
import { resolve, dirname, relative, basename } from "node:path";
import { createRequire } from "node:module";
import ts from "typescript";
import MagicString from "magic-string";
import { encode } from "@jridgewell/sourcemap-codec";
import { CompilerClient, run } from "./client.mjs";
import { metadata, resolveImport, exportedComponent } from "./frontend.mjs";
import { prepareTemplate, checkerSource } from "./templates.mjs";
import { scopeStyles } from "./styles.mjs";
import { CompilationError, diagnostic } from "./diagnostics.mjs";

const require = createRequire(import.meta.url);
const checkerBin = resolve(dirname(require.resolve("@typescript/native-preview/package.json")), "bin/tsgo");
const hash = text => createHash("sha256").update(text).digest("hex").slice(0, 12);
const lineOffset = (source, line, column) => {
  let offset = 0;
  for (let index = 1; index < line; index++) {
    const next = source.indexOf("\n", offset);
    if (next < 0) return source.length;
    offset = next + 1;
  }
  return Math.min(offset + column - 1, source.length);
};
const location = (text, offset) => {
  const before = text.slice(0, offset);
  return [before.split("\n").length - 1, offset - before.lastIndexOf("\n") - 1];
};

export class Project {
  #client = new CompilerClient();
  #metadata = new Map();
  #components = new Map();
  #started;
  #options = {};
  #scopeNamespace;
  dependencies = new Map();

  constructor(root, { scopeNamespace = "" } = {}) {
    this.root = resolve(root);
    this.#scopeNamespace = scopeNamespace;
  }
  get pid() { return this.#client.pid; }
  async start() {
    this.#started ??= this.#client.start();
    await this.#started;
  }
  async close() { await this.#client.close(); }
  invalidate(file) {
    file = resolve(file);
    if (file.endsWith(".ts") || basename(file) === "tsconfig.json") {
      this.#metadata.clear();
      this.#components.clear();
    } else {
      for (const [owner, files] of this.dependencies) if (files.has(file)) this.#components.delete(owner);
    }
  }
  async #meta(file) {
    if (!this.#metadata.has(file)) this.#metadata.set(file, metadata(file));
    return this.#metadata.get(file);
  }
  async #component(file) {
    await this.start();
    if (!this.#components.has(file)) this.#components.set(file, this.#loadComponent(file));
    return this.#components.get(file);
  }
  async #loadComponent(file) {
    const meta = await this.#meta(file);
    if (!meta) return null;
    const templateFile = resolve(dirname(file), meta.templateUrl);
    const styleFile = meta.styleUrl ? resolve(dirname(file), meta.styleUrl) : null;
    const files = new Set([file, templateFile, ...(styleFile ? [styleFile] : [])]);
    this.dependencies.set(file, files);
    const template = await readFile(templateFile, "utf8");
    const parsed = await this.#client.request("parse", { source: template, file: templateFile });
    if (parsed.diagnostics?.length) throw new CompilationError(parsed.diagnostics.map(d => diagnostic(templateFile, template, d.start, d.code, d.message, d.end)));
    const dependencies = [];
    const selectors = new Set();
    for (const dependency of meta.dependencies) {
      const depFile = resolveImport(file, dependency.from, this.#options);
      files.add(depFile);
      const child = await exportedComponent(depFile, dependency.exported, this.#options, new Set(), files);
      if (!child) {
        throw new CompilationError([diagnostic(file, meta.source, meta.decoratorStart, "F_IMPORT",
          `${dependency.local} is not a named @Component class with compiler metadata. For npm libraries, run angulus build --lib and publish the generated dist package.`)]);
      }
      if (selectors.has(child.selector)) throw new CompilationError([diagnostic(file, meta.source, meta.decoratorStart, "F_IMPORT", `Duplicate imported selector '${child.selector}'.`)]);
      selectors.add(child.selector);
      files.add(child.file);
      dependencies.push({ ...child, local: dependency.local });
    }
    const component = { ...meta, template, templateFile, styleFile, scopeId: `f-${hash(`${this.#scopeNamespace}${relative(this.root, file).replaceAll("\\", "/")}`)}` };
    const prepared = prepareTemplate(parsed.nodes ?? [], component, dependencies);
    return { component, dependencies, prepared, nodes: parsed.nodes ?? [] };
  }
  async compile(file) {
    file = resolve(file);
    const data = await this.#component(file);
    if (!data) return null;
    const { component, prepared, dependencies } = data;
    const usedNames = new Set();
    const collectNames = node => { if (ts.isIdentifier(node)) usedNames.add(node.text); ts.forEachChild(node, collectNames); };
    collectNames(component.ast);
    const uniqueName = prefix => {
      let name = prefix;
      while (usedNames.has(name)) name += "_";
      usedNames.add(name);
      return name;
    };
    const runtimeName = uniqueName("__angulusRuntime");
    const childAliases = dependencies.map(dependency => ({ ...dependency, generatedName: uniqueName("__angulusChild") }));
    const generated = await this.#client.request("generate", {
      ...prepared,
      components: Object.fromEntries(childAliases.map(dependency => [dependency.selector, dependency.generatedName])),
      file: component.templateFile, scopeId: component.scopeId,
    });
    if (generated.diagnostics?.length) throw new CompilationError(generated.diagnostics.map(d => diagnostic(component.templateFile, component.template, d.start, d.code, d.message, d.end)));
    const magic = new MagicString(component.source);
    magic.remove(component.decoratorStart, component.decoratorEnd);
    const generatedPrefix = `\nimport * as ${runtimeName} from "@angulus/core";\n${component.styleFile ? `import ${JSON.stringify(`${file}?angulus-style.css`)};\n` : ""}${childAliases.map(dependency => `const ${dependency.generatedName} = ${dependency.local};\n`).join("")}((__f) => { __f.defineComponent(${component.name}, `;
    const generatedStart = magic.toString().length + generatedPrefix.length;
    magic.append(`${generatedPrefix}${generated.code}, ${JSON.stringify(component.scopeId)}); })(${runtimeName});\n`);
    const code = magic.toString();
    const map = magic.generateDecodedMap({ source: file, file, includeContent: true, hires: true });
    map.sources.push(component.templateFile);
    map.sourcesContent.push(component.template);
    for (const mapping of generated.mappings ?? []) {
      const [line, column] = location(code, generatedStart + mapping.generated);
      const [sourceLine, sourceColumn] = location(component.template, mapping.source);
      while (map.mappings.length <= line) map.mappings.push([]);
      map.mappings[line].push([column, 1, sourceLine, sourceColumn]);
      map.mappings[line].sort((a, b) => a[0] - b[0]);
    }
    return { code, map: { ...map, mappings: encode(map.mappings) }, dependencies: [...this.dependencies.get(file)] };
  }
  async style(file) {
    const data = await this.#component(resolve(file));
    if (!data?.component.styleFile) throw new Error(`No component stylesheet for ${file}`);
    const { styleFile, scopeId } = data.component;
    return scopeStyles(await readFile(styleFile, "utf8"), styleFile, scopeId);
  }
  async check(additionalFiles = []) {
    await this.start();
    const configPath = resolve(this.root, "tsconfig.json");
    const loaded = ts.readConfigFile(configPath, ts.sys.readFile);
    const errors = [];
    const hasConfig = ts.sys.fileExists(configPath);
    if (loaded.error && hasConfig) return [diagnostic(configPath, await readFile(configPath, "utf8"), loaded.error.start ?? 0, `TS${loaded.error.code}`, ts.flattenDiagnosticMessageText(loaded.error.messageText, "\n"))];
    const config = hasConfig ? loaded.config : { compilerOptions: { target: "ES2022", module: "ESNext", moduleResolution: "Bundler", strict: true, experimentalDecorators: true, lib: ["ES2022", "DOM", "DOM.Iterable"], skipLibCheck: true }, include: ["**/*.ts"], exclude: ["node_modules", "dist", ".angulus"] };
    const parsedConfig = ts.parseJsonConfigFileContent(config, ts.sys, this.root);
    this.#options = parsedConfig.options;
    for (const error of parsedConfig.errors) errors.push(diagnostic(configPath, "", 0, `TS${error.code}`, ts.flattenDiagnosticMessageText(error.messageText, "\n")));
    const sources = [...new Set([...parsedConfig.fileNames, ...additionalFiles])].filter(file =>
      !relative(this.root, file).split(/[\\/]/).some(part => ["node_modules", ".angulus", "dist"].includes(part)));
    const checkDir = resolve(this.root, ".angulus/check");
    await mkdir(checkDir, { recursive: true });
    const generated = new Map();
    for (const file of sources) {
      try {
        const data = await this.#component(file);
        if (!data) continue;
        const check = checkerSource(data.nodes, data.component, data.dependencies);
        const checkFile = resolve(checkDir, `${hash(file)}.ts`);
        await writeIfChanged(checkFile, check.code);
        generated.set(checkFile, check);
        // Generation and CSS validation also run for components never opened by Vite.
        await this.compile(file);
        if (data.component.styleFile) await this.style(file);
      } catch (error) {
        if (error instanceof CompilationError) errors.push(...error.diagnostics);
        else errors.push(diagnostic(file, "", 0, "F_IO", error.message));
      }
    }
    for (const entry of await readdir(checkDir)) {
      const file = resolve(checkDir, entry);
      if (entry.endsWith(".ts") && !generated.has(file)) await unlink(file);
    }
    const checkConfig = {
      ...(hasConfig ? { extends: configPath } : {}),
      compilerOptions: {
        ...(hasConfig ? {} : config.compilerOptions),
        strict: true, noEmit: true, incremental: true,
        tsBuildInfoFile: resolve(this.root, ".angulus/check.tsbuildinfo"),
        rootDir: resolve(this.root, "../.."),
        allowImportingTsExtensions: true,
      },
      files: [...sources, ...generated.keys()],
      include: [], exclude: [],
    };
    const generatedConfig = resolve(this.root, ".angulus/tsconfig.check.json");
    await writeIfChanged(generatedConfig, JSON.stringify(checkConfig, null, 2));
    const result = await run(process.execPath, [checkerBin, "--project", generatedConfig, "--pretty", "false", "--locale", "en"], { cwd: this.root });
    const output = `${result.stdout}\n${result.stderr}`.trim();
    let current;
    for (const line of output.split(/\r?\n/)) {
      if (!line) continue;
      const match = /^(.*?)\((\d+),(\d+)\): error TS(\d+): (.*)$/.exec(line);
      if (match) {
        const file = resolve(this.root, match[1]);
        const generatedFile = generated.get(file);
        let originalFile = file;
        let source = generatedFile?.code ?? await readFile(file, "utf8");
        let start = lineOffset(source, Number(match[2]), Number(match[3]));
        if (generatedFile) {
          const mapping = generatedFile.mappings.find(item => item.from <= start && start < item.to);
          originalFile = generatedFile.file;
          source = generatedFile.source;
          start = mapping?.offsets?.[start - mapping.from] ?? mapping?.start ?? 0;
        }
        current = diagnostic(originalFile, source, start, `TS${match[4]}`, match[5]);
        errors.push(current);
      } else if (/^\s/.test(line) && current) current.message += `\n${line}`;
      else {
        current = diagnostic(generatedConfig, "", 0, "F_NATIVE", line);
        errors.push(current);
      }
    }
    if (result.code !== 0 && !output) errors.push(diagnostic(generatedConfig, "", 0, "F_NATIVE", `Native TypeScript exited with code ${result.code}, signal ${result.signal ?? "none"}.`));
    return errors;
  }
}

async function writeIfChanged(file, source) {
  try { if (await readFile(file, "utf8") === source) return; }
  catch (error) { if (error.code !== "ENOENT") throw error; }
  await writeFile(file, source);
}
