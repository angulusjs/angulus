import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expression } from "../src/expressions.mjs";
import { metadata, exportedComponent } from "../src/frontend.mjs";
import { scopeStyles } from "../src/styles.mjs";

const parse = (raw, options = {}) => expression(raw, { file: "view.html", source: raw, start: 0, ...options }).code;

test("expressions preserve this, local names, strings, shorthand, optional access", () => {
  assert.equal(parse("increment($event)", { event: true }), "ctx.increment($event)");
  assert.equal(parse("user?.name ?? 'unknown'"), "ctx.user?.name ?? 'unknown'");
  assert.equal(parse("{name, id: product.id}"), "{name: ctx.name, id: ctx.product.id}");
  assert.equal(parse("item.id + $index", { locals: new Map([["item", "signal"], ["$index", "signal"]]), runtime: true }), "item().id + $index()");
  assert.equal(parse("item.id", { locals: new Map([["item", "plain"]]), runtime: true }), "item.id");
});

test("expressions reject instructions, assignment, assertions and ambient globals", () => {
  for (const source of ["a = 1", "a++; b()", "new Object()", "a as any", "() => 1", "delete user.name", "await promise", "x; alert('x')"]) {
    assert.throws(() => parse(source), /Unsupported|expression|expected/i, source);
  }
  assert.equal(parse("window.location"), "ctx.window.location");
  assert.throws(() => parse("this.name"), /Unsupported/);
});

test("expression character maps retain original member positions after qualification", () => {
  const raw = "foo.bar + item.name";
  const result = expression(raw, { file: "view.html", source: `12345${raw}`, start: 5, locals: new Map([["item", "signal"]]), runtime: true });
  assert.equal(result.code, "ctx.foo.bar + item().name");
  assert.equal(result.offsets[result.code.indexOf("bar")], 5 + raw.indexOf("bar"));
  assert.equal(result.offsets[result.code.indexOf("name")], 5 + raw.indexOf("name"));
});

test("static metadata permits literal paths and imported component references only", async () => {
  const directory = await mkdtemp(join(tmpdir(), "angulus-meta-"));
  const file = join(directory, "counter.ts");
  try {
    await writeFile(file, `import { Component as View } from "@angulus/core";
import { Child } from "./child";
@View({ selector: "app-counter", templateUrl: "./counter.html", imports: [Child] })
export class Counter {}
`);
    const result = await metadata(file);
    assert.equal(result.name, "Counter");
    assert.equal(result.dependencies[0].exported, "Child");
    await writeFile(file, `import { Component } from "@angulus/core"; @Component(getMetadata()) export class Counter {}`);
    await assert.rejects(metadata(file), /object literal/);
    await writeFile(file, `import {Component,input} from "@angulus/core"; @Component({selector:"app-private",templateUrl:"./a.html"}) export class Private { private value = input.required<string>(); }`);
    await assert.rejects(metadata(file), /must be public/);
    await writeFile(file, `import * as core from "@angulus/core"; @core.Component({selector:"app-private",templateUrl:"./a.html"}) export class Private {}`);
    await assert.rejects(metadata(file), /named import/);
  } finally { await rm(directory, { recursive: true }); }
});

test("CSS scoping handles selector lists, pseudo elements, at-rules and keyframes", () => {
  const result = scopeStyles(`
@media (width > 500px) { .a:hover > span::before, :is(.b, .c) { color: red } }
@keyframes pulse { from { opacity: 0 } to { opacity: 1 } }
div { animation: pulse 1s; background: url("./image.png") }
`, "/app/features/card.css", "f-test");
  assert.match(result.code, /\.a:hover\[data-f-test\] > span\[data-f-test\]::before/);
  assert.match(result.code, /:is\(\.b, \.c\)\[data-f-test\]/);
  assert.match(result.code, /@keyframes pulse-f-test/);
  assert.doesNotMatch(result.code, /from\[data-/);
  assert.match(result.code, /animation: pulse-f-test 1s/);
  assert.match(result.code, /\/@fs\/\/app\/features\/image.png/);
  assert.deepEqual(result.map.sourcesContent.length, 1);
  assert.throws(() => scopeStyles("@import 'external.css';", "/app/a.css", "f-a"), /do not support @import/);
  assert.throws(() => scopeStyles("a { & b {color:red} }", "/app/a.css", "f-a"), /nesting is not supported/);
  assert.throws(() => scopeStyles("@keyframes ease { to { opacity: 1 } } a { animation: ease 1s; }", "/app/a.css", "f-a"), /Ambiguous keyframe/);
  const names = scopeStyles('@keyframes "slide" { to { opacity: 1 } } a { animation-name: "slide"; animation-duration: 1s; }', "/app/a.css", "f-a");
  assert.match(names.code, /animation-name: "slide-f-a"/);
});

test("compiled metadata resolves barrel aliases and cycles without executing package code", async t => {
  const directory = await mkdtemp(join(tmpdir(), "angulus-exports-"));
  t.after(() => rm(directory, { recursive: true }));
  const child = join(directory, "child.d.ts");
  await writeFile(child, "export declare class Child { readonly value: string; }");
  await writeFile(`${child}.angulus.json`, JSON.stringify({ version: 1, name: "Child", selector: "lib-child" }));
  await writeFile(join(directory, "index.d.ts"), 'export * from "./cycle.js"; export { Child as PublicChild } from "./alias.js";');
  await writeFile(join(directory, "cycle.d.ts"), 'export * from "./index.js";');
  await writeFile(join(directory, "alias.d.ts"), 'import { Child } from "./child.js"; export { Child };');
  const tracked = new Set();
  assert.deepEqual(await exportedComponent(join(directory, "index.d.ts"), "PublicChild", {}, new Set(), tracked),
    { file: child, name: "Child", selector: "lib-child" });
  assert.ok(tracked.has(join(directory, "alias.d.ts")));
  assert.ok(tracked.has(`${child}.angulus.json`));
  assert.equal(await exportedComponent(join(directory, "index.d.ts"), "Missing"), null);
  await writeFile(`${child}.angulus.json`, '{"version":2,"name":"Child","selector":"lib-child"}');
  await assert.rejects(exportedComponent(child, "Child"), /Unsupported or invalid/);
  await writeFile(`${child}.angulus.json`, "{");
  await assert.rejects(exportedComponent(child, "Child"), /Invalid Angulus metadata/);
});
