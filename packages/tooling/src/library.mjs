import { copyFile, mkdir, readFile, readdir, realpath, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, isAbsolute, relative, resolve, sep } from "node:path";
import ts from "typescript";
import { Project } from "./project.mjs";
import { angulus } from "./vite.mjs";
import { metadata } from "./frontend.mjs";
import { run } from "./client.mjs";
import { CompilationError, stderrLogger } from "./diagnostics.mjs";

const require = createRequire(import.meta.url);
const checkerBin = resolve(dirname(require.resolve("@typescript/native-preview/package.json")), "bin/tsgo");
const contained = (root, file) => {
  const part = relative(root, file);
  return part !== ".." && !part.startsWith(`..${sep}`) && !isAbsolute(part);
};
const declarationPath = file => /\.d\.[cm]?ts$/.test(file)
  ? file : file.replace(/\.(?:([cm])[jt]s|[jt]sx?)$/, (_, prefix) => `.d.${prefix ?? ""}ts`);
const format = diagnostic => ts.flattenDiagnosticMessageText(diagnostic.messageText, "\n");

async function copyDirectory(source, target) {
  await mkdir(target, { recursive: true });
  for (const item of await readdir(source, { withFileTypes: true })) {
    if (item.isDirectory()) await copyDirectory(resolve(source, item.name), resolve(target, item.name));
    else if (item.isFile()) await copyFile(resolve(source, item.name), resolve(target, item.name));
  }
}

export async function buildLibrary({ root = process.cwd(), entry = "src/index.ts", onEvent } = {}) {
  root = await realpath(root);
  if (typeof entry !== "string" || !entry.endsWith(".ts") || entry.endsWith(".d.ts")) {
    throw new Error("Library entry must be a TypeScript .ts module.");
  }
  entry = await realpath(resolve(root, entry));
  if (!contained(root, entry) || /(^|[/\\])(node_modules|dist|\.angulus)([/\\]|$)/.test(relative(root, entry))) {
    throw new Error("Library entry must be a source file inside the project.");
  }
  const manifest = JSON.parse(await readFile(resolve(root, "package.json"), "utf8"));
  if (typeof manifest.name !== "string" || !manifest.name || typeof manifest.version !== "string" || !manifest.version) {
    throw new Error("Library package.json must declare name and version.");
  }
  if (!manifest.peerDependencies?.["@angulus/core"] || manifest.dependencies?.["@angulus/core"] ||
      manifest.optionalDependencies?.["@angulus/core"]) {
    throw new Error("Declare @angulus/core as a peerDependency, not a dependency: library and application must share one runtime.");
  }
  const configPath = resolve(root, "tsconfig.json");
  const loaded = ts.readConfigFile(configPath, ts.sys.readFile);
  if (loaded.error) throw new Error(format(loaded.error));
  const parsed = ts.parseJsonConfigFileContent(loaded.config, ts.sys, root);
  if (parsed.errors.length) throw new Error(parsed.errors.map(format).join("\n"));
  const program = ts.createProgram({ rootNames: [entry], options: parsed.options });
  const sources = program.getSourceFiles().filter(source =>
    contained(root, source.fileName) && !relative(root, source.fileName).split(/[/\\]/).includes("node_modules"));
  const files = sources.filter(source => !source.isDeclarationFile).map(source => source.fileName);
  const directory = resolve(root, "dist");
  const staging = resolve(root, ".angulus/library-types");
  // Both directories are owned build outputs, never follow a redirected output.
  for (const target of [resolve(root, ".angulus"), directory, staging]) {
    let canonical;
    try { canonical = await realpath(target); }
    catch (error) { if (error.code !== "ENOENT") throw error; }
    if (canonical && canonical !== target) throw new Error(`Refusing to build through an output symlink: ${target}`);
  }
  const project = new Project(root, { scopeNamespace: `${manifest.name}@${manifest.version}:` });
  try {
    const diagnostics = await project.check(files);
    onEvent?.({ version: 1, type: "checked", revision: 1, diagnostics,
      valid: !diagnostics.some(item => item.severity === "error"), stale: false });
    if (diagnostics.some(item => item.severity === "error")) throw new CompilationError(diagnostics);
    await rm(staging, { recursive: true, force: true });
    await mkdir(staging, { recursive: true });
    const emitConfig = resolve(root, ".angulus/tsconfig.library.json");
    await writeFile(emitConfig, JSON.stringify({
      extends: configPath,
      compilerOptions: {
        noEmit: false, declaration: true, emitDeclarationOnly: true, noEmitOnError: true,
        declarationMap: false, sourceMap: false, inlineSourceMap: false,
        incremental: false, composite: false, tsBuildInfoFile: undefined,
        rootDir: root, outDir: staging, declarationDir: staging,
        allowImportingTsExtensions: true,
      },
      files: [entry], include: [], exclude: [],
    }, null, 2));
    const emitted = await run(process.execPath, [checkerBin, "-p", emitConfig, "--pretty", "false"], { cwd: root });
    if (emitted.code !== 0) throw new Error(`Library declaration build failed:\n${emitted.stdout}\n${emitted.stderr}`);
    for (const source of sources) {
      const target = resolve(staging, declarationPath(relative(root, source.fileName)));
      if (source.isDeclarationFile || source.fileName.endsWith(".json")) {
        await mkdir(dirname(target), { recursive: true });
        await copyFile(source.fileName, target);
      }
      if (source.fileName.endsWith(".json")) continue;
      const component = source.isDeclarationFile ? null : await metadata(source.fileName);
      if (component) await writeFile(`${target}.angulus.json`, JSON.stringify({
        version: 1, name: component.name, selector: component.selector,
      }, null, 2) + "\n");
      await portableDeclarations(target, source.fileName, root, parsed.options);
    }
    const externalPackages = new Set([
      ...Object.keys(manifest.dependencies ?? {}), ...Object.keys(manifest.peerDependencies ?? {}),
      ...Object.keys(manifest.optionalDependencies ?? {}),
    ]);
    const { build } = await import("vite");
    await build({
      root, configFile: false, clearScreen: false, logLevel: "warn", customLogger: stderrLogger(),
      resolve: { tsconfigPaths: true },
      plugins: [angulus({ project, checkBuild: false })],
      build: {
        outDir: directory, emptyOutDir: true, sourcemap: false,
        lib: { entry, formats: ["es"], fileName: () => "index.js", cssFileName: "style" },
        rolldownOptions: {
          external: id => [...externalPackages].some(name => id === name || id.startsWith(`${name}/`)),
        },
      },
    });
    await copyDirectory(staging, resolve(directory, "types"));
    const types = `./types/${declarationPath(relative(root, entry)).replaceAll("\\", "/")}`;
    const style = ts.sys.fileExists(resolve(directory, "style.css")) ? resolve(directory, "style.css") : undefined;
    if (style) await writeFile(resolve(directory, "style.d.ts"), "export {};\n");
    const published = {};
    for (const key of ["name", "version", "description", "license", "private", "repository", "author", "contributors",
      "homepage", "bugs", "keywords", "funding", "dependencies", "optionalDependencies",
      "peerDependencies", "peerDependenciesMeta", "engines", "publishConfig"]) {
      if (manifest[key] !== undefined) published[key] = manifest[key];
    }
    Object.assign(published, {
      type: "module", main: "./index.js", types,
      exports: { ".": { types, import: "./index.js", default: "./index.js" },
        ...(style ? { "./style.css": { types: "./style.d.ts", default: "./style.css" } } : {}) },
      sideEffects: ["**/*.css"],
      files: ["**/*.js", "**/*.css", "style.d.ts", "types", "LICENSE", "README.md"],
    });
    await writeFile(resolve(directory, "package.json"), JSON.stringify(published, null, 2) + "\n");
    for (const name of ["README.md", "LICENSE"]) {
      try { await copyFile(resolve(root, name), resolve(directory, name)); }
      catch (error) { if (error.code !== "ENOENT") throw error; }
    }
    return { directory, entry: resolve(directory, "index.js"), types: resolve(directory, types), style };
  } finally {
    await project.close();
    await rm(staging, { recursive: true, force: true });
  }
}

async function portableDeclarations(file, original, root, options) {
  let text = await readFile(file, "utf8");
  const ast = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true);
  const replacements = [];
  function visit(node) {
    if (ts.isStringLiteral(node) && (
      (ts.isImportDeclaration(node.parent) || ts.isExportDeclaration(node.parent)) && node.parent.moduleSpecifier === node
      || ts.isLiteralTypeNode(node.parent) && ts.isImportTypeNode(node.parent.parent)
    )) {
      const target = ts.resolveModuleName(node.text, original, options, ts.sys).resolvedModule?.resolvedFileName;
      if (!target) {
        if (node.text.startsWith(".") || isAbsolute(node.text)) throw new Error(`Cannot resolve declaration import '${node.text}' in ${original}`);
        return;
      }
      if (contained(root, target) && !relative(root, target).split(/[/\\]/).includes("node_modules")) {
        let specifier = relative(dirname(original), target).replaceAll("\\", "/")
          .replace(/(?:\.d)?\.(?:([cm])ts|tsx?)$/, (_, prefix) => `.${prefix ?? ""}js`);
        if (!specifier.startsWith(".")) specifier = `./${specifier}`;
        replacements.push({ start: node.getStart(ast), end: node.end, text: JSON.stringify(specifier) });
      } else if (node.text.startsWith(".") || isAbsolute(node.text)) {
        throw new Error(`Library declaration refers outside the package: ${original}: ${node.text}`);
      }
    }
    ts.forEachChild(node, visit);
  }
  visit(ast);
  for (const item of replacements.sort((a, b) => b.start - a.start)) text = text.slice(0, item.start) + item.text + text.slice(item.end);
  await writeFile(file, text);
}
