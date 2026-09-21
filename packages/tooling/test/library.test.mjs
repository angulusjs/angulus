import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdir, mkdtemp, readFile, readdir, realpath, rm, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { Window } from "happy-dom";
import { build, createServer } from "vite";
import * as runtime from "@angulus/core";
import { buildLibrary } from "../src/library.mjs";
import { Project } from "../src/project.mjs";
import { angulus } from "../src/vite.mjs";
import { run, workspace } from "../src/client.mjs";

const config = {
  compilerOptions: {
    target: "ES2022", module: "ESNext", moduleResolution: "Bundler",
    strict: true, experimentalDecorators: true, noEmit: true,
    lib: ["ES2022", "DOM", "DOM.Iterable"], types: [], skipLibCheck: true,
  },
  include: ["src/**/*.ts"],
};
const coreManifest = JSON.parse(await readFile(resolve(workspace, "packages/core/package.json"), "utf8"));
const libraryManifest = {
  name: "@angulus-test/widgets", version: "1.0.0", type: "module", license: "MIT",
  peerDependencies: { "@angulus/core": coreManifest.version },
};

async function write(root, files) {
  for (const [name, contents] of Object.entries(files)) {
    await mkdir(dirname(resolve(root, name)), { recursive: true });
    await writeFile(resolve(root, name), typeof contents === "string" ? contents : JSON.stringify(contents));
  }
}

async function fixture(t) {
  await mkdir(resolve(workspace, ".angulus"), { recursive: true });
  const root = await realpath(await mkdtemp(resolve(workspace, ".angulus/library-test-")));
  t.after(() => rm(root, { recursive: true, force: true }));
  const library = resolve(root, "library");
  await write(library, {
    "package.json": libraryManifest,
    "tsconfig.json": config,
    "src/index.ts": 'export { Card as Widget } from "./card"; export * from "./public";',
    "src/public.ts": 'export { Child as SmallCounter } from "./child";',
    "src/child.ts": `import {Component,input,output} from "@angulus/core";
@Component({selector:"ui-child",templateUrl:"./child.html",styleUrl:"./child.css"})
export class Child {
  readonly value = input.required<number>();
  readonly changed = output<number>();
  increment(): void { this.changed.emit(this.value() + 1); }
}`,
    "src/child.html": '<button (click)="increment()">{{ value() }}</button>',
    "src/child.css": "button { color: rgb(1, 2, 3); animation: pulse 1s; } @keyframes pulse { to { opacity: 1; } }",
    "src/card.ts": `import {Component,input,output} from "@angulus/core";
import {SmallCounter as Nested} from "./public";
@Component({selector:"ui-card",templateUrl:"./card.html",styleUrl:"./card.css",imports:[Nested]})
export class Card {
  readonly value = input.required<number>();
  readonly changed = output<number>();
}`,
    "src/card.html": '<section><ui-child [value]="value()" (changed)="changed.emit($event)" /></section>',
    "src/card.css": "section { border: 1px solid red; } section button { color: rgb(255, 0, 0); }",
    "README.md": "# Widget library",
    "LICENSE": "MIT",
  });
  return { root, library };
}

async function npm(cwd, args) {
  const result = await run(process.env.npm_execpath ? process.execPath : "npm",
    process.env.npm_execpath ? [process.env.npm_execpath, ...args] : args, { cwd });
  assert.equal(result.code, 0, result.stderr + result.stdout);
  return result.stdout;
}

test("packed library imports, typed bindings, scoped styles, runtime events and Vite consumption", { timeout: 30_000 }, async t => {
  const { root, library } = await fixture(t);
  const events = [];
  const output = await buildLibrary({ root: library, onEvent: event => events.push(event) });
  assert.equal(output.entry, resolve(library, "dist/index.js"));
  assert.equal(output.types, resolve(library, "dist/types/src/index.d.ts"));
  assert.equal(output.style, resolve(library, "dist/style.css"));
  assert.equal(events.at(-1).valid, true);
  const js = await readFile(output.entry, "utf8");
  assert.match(js, /from "@angulus\/core"/);
  assert.doesNotMatch(js, /Symbol\("Angulus component definition"\)/);
  assert.ok(!js.includes(library), "Published JS must not contain build-machine paths");
  const css = await readFile(output.style, "utf8");
  const scopes = [...new Set(css.match(/data-f-[a-f0-9]+/g))];
  assert.equal(scopes.length, 2);
  assert.match(css, /pulse-f-[a-f0-9]+/);
  assert.match(await readFile(output.types, "utf8"), /"\.\/card\.js"/);
  const [packed] = JSON.parse(await npm(output.directory, ["pack", "--json", "--ignore-scripts", "--pack-destination", root]));
  assert.ok(packed.files.some(file => file.path.endsWith(".d.ts.angulus.json")));
  assert.ok(packed.files.some(file => file.path === "style.css"));
  assert.ok(packed.files.every(file => !file.path.endsWith(".html") && (!file.path.endsWith(".ts") || file.path.endsWith(".d.ts"))));
  const app = resolve(root, "app");
  await write(app, {
    "package.json": { name: "library-consumer", private: true, type: "module" },
    "tsconfig.json": config,
    "index.html": '<div id="app"></div><script type="module" src="/src/main.ts"></script>',
    "src/main.ts": 'import "@angulus-test/widgets/style.css"; import {mount} from "@angulus/core"; import {App} from "./app"; mount(App, document.getElementById("app")!);',
    "src/app.ts": `import {Component,signal} from "@angulus/core";
import {Widget as Card,SmallCounter} from "@angulus-test/widgets";
@Component({selector:"app-main",templateUrl:"./app.html",imports:[Card,SmallCounter]})
export class App { readonly count = signal(3); accept(n:number):void { this.count.set(n); } }`,
    "src/app.html": '<ui-card [value]="count()" (changed)="accept($event)" /><ui-child [value]="1" />',
  });
  await npm(app, ["install", resolve(root, packed.filename), "--ignore-scripts", "--legacy-peer-deps", "--no-audit", "--no-fund", "--offline"]);
  const installed = resolve(app, "node_modules/@angulus-test/widgets");
  assert.equal(await realpath(installed), installed, "Library must be installed from a tarball, not linked");
  await rm(resolve(library, "src"), { recursive: true });
  const project = new Project(app);
  try {
    assert.deepEqual(await project.check(), []);
    await write(app, { "src/app.html": '<ui-card /> <ui-card [value]="\'wrong\'" (changed)="accept($event.noSuchProperty)" />' });
    project.invalidate(resolve(app, "src/app.html"));
    const diagnostics = await project.check();
    assert.equal(diagnostics.length, 3, JSON.stringify(diagnostics));
    assert.ok(diagnostics.every(item => item.file === resolve(app, "src/app.html")));
    await write(app, { "src/app.html": '<ui-card [value]="count()" (changed)="accept($event)" />' });
  } finally { await project.close(); }

  const { Widget } = await import(pathToFileURL(resolve(installed, "index.js")).href);
  const window = new Window();
  const stylesheet = window.document.createElement("style");
  stylesheet.textContent = css;
  window.document.head.append(stylesheet);
  const outsideButton = window.document.createElement("button");
  window.document.body.append(outsideButton);
  const host = window.document.createElement("main");
  window.document.body.append(host);
  const scope = new runtime.Scope();
  const value = runtime.signal(3);
  const changed = [];
  try {
    runtime.mountChild(scope, host, Widget, { value }, { changed: n => { changed.push(n); value.set(n); } });
    assert.equal(host.textContent, "3");
    host.querySelector("button").click();
    runtime.flushSync();
    assert.deepEqual(changed, [4]);
    assert.equal(host.textContent, "4");
    assert.match(window.getComputedStyle(host.querySelector("button")).color, /^(?:#010203|rgb\(1, 2, 3\))$/);
    assert.doesNotMatch(window.getComputedStyle(outsideButton).color, /^(?:#010203|rgb\(1, 2, 3\))$/);
    for (const id of scopes) assert.ok(host.querySelector(`[${id}]`), `DOM must carry CSS scope ${id}`);
    assert.notEqual(host.querySelector("section").getAttributeNames().find(name => name.startsWith("data-f-")),
      host.querySelector("button").getAttributeNames().find(name => name.startsWith("data-f-")));
  } finally {
    scope.dispose();
    await window.happyDOM.close();
  }

  await build({ root: app, configFile: false, logLevel: "silent", plugins: [angulus()] });
  const assets = await readdir(resolve(app, "dist/assets"));
  const appCss = await readFile(resolve(app, "dist/assets", assets.find(name => name.endsWith(".css"))), "utf8");
  for (const id of scopes) assert.ok(appCss.includes(id));
  const server = await createServer({
    root: app, configFile: false, logLevel: "silent", plugins: [angulus({ onEvent() {} })],
    server: { host: "127.0.0.1", port: 0 },
  });
  try {
    await server.listen();
    const url = server.resolvedUrls.local[0];
    const response = await fetch(new URL("src/app.ts", url), { signal: AbortSignal.timeout(10_000) });
    const transformed = await response.text();
    assert.equal(response.status, 200, transformed);
    assert.match(transformed, /defineComponent/);
    const styleResponse = await fetch(new URL("node_modules/@angulus-test/widgets/style.css", url), { signal: AbortSignal.timeout(10_000) });
    assert.equal(styleResponse.status, 200);
    assert.ok((await styleResponse.text()).includes(scopes[0]));
    await server.waitForRequestsIdle();
    const dependency = transformed.match(/from "(\/node_modules\/\.vite\/deps\/@angulus-test_widgets\.js[^"]*)"/);
    assert.ok(dependency, transformed);
    const optimized = await fetch(new URL(dependency[1], url), { signal: AbortSignal.timeout(10_000) });
    const libraryCode = await optimized.text();
    assert.equal(optimized.status, 200, libraryCode);
    const coreImport = transformed.match(/from "(\/node_modules\/\.vite\/deps\/@angulus_core\.js[^"]*)"/);
    assert.ok(coreImport, transformed);
    const coreResponse = await fetch(new URL(coreImport[1], url), { signal: AbortSignal.timeout(10_000) });
    const coreCode = await coreResponse.text();
    assert.equal(coreResponse.status, 200, coreCode);
    const chunks = libraryCode.match(/chunk-[\w-]+\.js/g) ?? [];
    assert.ok(libraryCode.includes(JSON.stringify(coreImport[1])) || chunks.some(chunk => coreCode.includes(chunk)),
      "Optimized application and library must import the same core module or shared runtime chunk");
  } finally { await server.close(); }
});

test("library CLI emits JSON, custom entry works, and CSS scope includes package identity", async t => {
  const { library } = await fixture(t);
  const first = await buildLibrary({ root: library });
  const css = await readFile(first.style, "utf8");
  await write(library, {
    "package.json": { ...libraryManifest, name: "@angulus-test/other" },
    "angulus.config.json": { library: { entry: "src/public.ts" } },
  });
  const result = await run(process.execPath, [resolve(workspace, "packages/tooling/src/cli.mjs"), "build", "--lib", "--json", "--root", library]);
  assert.equal(result.code, 0, result.stderr + result.stdout);
  const events = result.stdout.trim().split("\n").map(line => JSON.parse(line));
  assert.deepEqual(events.map(event => event.type), ["checked", "built"]);
  assert.equal(events[1].types, resolve(library, "dist/types/src/public.d.ts"));
  const otherCss = await readFile(events[1].style, "utf8");
  for (const id of css.match(/data-f-[a-f0-9]+/g)) assert.ok(!otherCss.includes(id));
});

test("library build rejects invalid templates and missing runtime peer before publication", async t => {
  const { library } = await fixture(t);
  await write(library, { "src/child.html": "<p>{{ doesNotExist }}</p>" });
  await assert.rejects(buildLibrary({ root: library }), /doesNotExist/);
  await assert.rejects(readFile(resolve(library, "dist/package.json")), { code: "ENOENT" });
  await write(library, { "package.json": { ...libraryManifest, peerDependencies: {} } });
  await assert.rejects(buildLibrary({ root: library }), /peerDependency/);
});

test("declaration aliases are portable and a CSS-free package has no stylesheet export", async t => {
  const { library } = await fixture(t);
  await write(library, {
    "tsconfig.json": { ...config, compilerOptions: { ...config.compilerOptions, paths: { "#local/*": ["./src/*"] } } },
    "src/index.ts": 'export { Plain } from "#local/plain";',
    "src/plain.ts": `import {Component} from "@angulus/core";
@Component({selector:"ui-plain",templateUrl:"./plain.html"})
export class Plain { value = "plain"; }`,
    "src/plain.html": "<p>{{ value }}</p>",
  });
  const output = await buildLibrary({ root: library });
  assert.equal(output.style, undefined);
  const manifest = JSON.parse(await readFile(resolve(output.directory, "package.json"), "utf8"));
  assert.equal(manifest.exports["./style.css"], undefined);
  assert.match(await readFile(output.types, "utf8"), /"\.\/plain\.js"/);
  assert.doesNotMatch(await readFile(output.entry, "utf8"), /#local/);
});

test("a library can re-export and compose components from another compiled package", async t => {
  const { root, library } = await fixture(t);
  const first = await buildLibrary({ root: library });
  const [packed] = JSON.parse(await npm(first.directory, ["pack", "--json", "--ignore-scripts", "--pack-destination", root]));
  const wrapper = resolve(root, "wrapper");
  await write(wrapper, {
    "package.json": { ...libraryManifest, name: "@angulus-test/wrapper" },
    "tsconfig.json": config,
    "src/index.ts": 'export { Wrapper } from "./wrapper"; export { Widget as ReexportedWidget } from "@angulus-test/widgets";',
    "src/wrapper.ts": `import {Component} from "@angulus/core";
import {Widget} from "@angulus-test/widgets";
@Component({selector:"ui-wrapper",templateUrl:"./wrapper.html",imports:[Widget]})
export class Wrapper {}`,
    "src/wrapper.html": '<ui-card [value]="42" />',
  });
  await npm(wrapper, ["install", resolve(root, packed.filename), "--ignore-scripts", "--legacy-peer-deps", "--no-audit", "--no-fund", "--offline"]);
  // Published dependency ranges must not refer to local development tarballs.
  await write(wrapper, { "package.json": { ...libraryManifest, name: "@angulus-test/wrapper",
    dependencies: { "@angulus-test/widgets": "1.0.0" } } });
  const output = await buildLibrary({ root: wrapper });
  assert.match(await readFile(output.entry, "utf8"), /from "@angulus-test\/widgets"/);
  assert.match(await readFile(output.types, "utf8"), /from "@angulus-test\/widgets"/);
  const { Wrapper } = await import(pathToFileURL(output.entry).href);
  const window = new Window();
  const host = window.document.createElement("main");
  try {
    const mounted = runtime.mount(Wrapper, host);
    assert.equal(host.textContent, "42");
    mounted.destroy();
  } finally { await window.happyDOM.close(); }
});
