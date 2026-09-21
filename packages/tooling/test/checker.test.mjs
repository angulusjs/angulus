import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, writeFile, mkdir, rm } from "node:fs/promises";
import { resolve, dirname } from "node:path";
import { Project } from "../src/project.mjs";
import { workspace } from "../src/client.mjs";

async function fixture(t, files) {
  await mkdir(resolve(workspace, ".angulus"), { recursive: true });
  const root = await mkdtemp(resolve(workspace, ".angulus/check-fixture-"));
  for (const [name, source] of Object.entries({
    "tsconfig.json": JSON.stringify({ compilerOptions: { target: "ES2022", module: "ESNext", moduleResolution: "Bundler", strict: true, experimentalDecorators: true, skipLibCheck: true, lib: ["ES2022", "DOM", "DOM.Iterable"] }, include: ["**/*.ts"], exclude: [".angulus"] }),
    ...files,
  })) {
    await mkdir(dirname(resolve(root, name)), { recursive: true });
    await writeFile(resolve(root, name), source);
  }
  const project = new Project(root);
  await project.start();
  t.after(async () => { await project.close(); await rm(root, { recursive: true }); });
  return { project, root };
}

const component = `
import { Component, signal } from "@angulus/core";
@Component({selector:"app-counter",templateUrl:"./counter.html"})
export class Counter {
  readonly count = signal(0);
  readonly items = signal([{id:1,name:"one"}]);
  user: {name:string}|null = {name:"valid"};
  private secret = 1;
  increment(event: MouseEvent): void { this.count.update(n=>n+1); }
}
`;

test("native template checker maps diagnostics to original HTML including Unicode offsets", async t => {
  const html = "<p>Привет 😀</p>\n<span>{{ missingMember }}</span>";
  const { project, root } = await fixture(t, { "counter.ts": component, "counter.html": html });
  const errors = await project.check();
  assert.equal(errors.length, 1, JSON.stringify(errors, null, 2));
  assert.equal(errors[0].file, resolve(root, "counter.html"));
  assert.equal(errors[0].line, 2);
  assert.equal(errors[0].column, 10);
  assert.equal(errors[0].start, html.indexOf("missingMember"));
  assert.match(errors[0].message, /missingMember/);
});

test("native checks loops, narrowing, member visibility and typed DOM events", async t => {
  const { project, root } = await fixture(t, {
    "counter.ts": component,
    "counter.html": '@if (user) { <p>{{ user.name }}</p> } @for (item of items(); track item.id) { <span>{{ item.name }} {{ $index }}</span> } <button (click)="increment($event)">+</button>',
  });
  assert.deepEqual(await project.check(), []);
  await writeFile(resolve(root, "counter.html"), '<input (input)="increment($event)" /><span>{{ secret }}</span><p>{{ item }}</p>');
  project.invalidate(resolve(root, "counter.html"));
  const errors = await project.check();
  assert.equal(errors.length, 3, JSON.stringify(errors, null, 2));
  assert.ok(errors.some(error => error.message.includes("private")));
  assert.ok(errors.some(error => error.message.includes("MouseEvent")));
  assert.ok(errors.some(error => error.message.includes("item")));
});

test("required inputs and output event types are verified across explicit imports", async t => {
  const { project, root } = await fixture(t, {
    "child.ts": `import {Component,input,output} from "@angulus/core"; @Component({selector:"app-child",templateUrl:"./child.html"}) export class Child { readonly value = input.required<number>(); readonly changed = output<number>(); }`,
    "child.html": "<p>{{ value() }}</p>",
    "counter.ts": `import {Component} from "@angulus/core"; import {Child} from "./child"; @Component({selector:"app-counter",templateUrl:"./counter.html",imports:[Child]}) export class Counter { accept(n:number):void {} }`,
    "counter.html": '<app-child [value]="1" (changed)="accept($event)" />',
  });
  assert.deepEqual(await project.check(), []);
  await writeFile(resolve(root, "counter.html"), '<app-child /> <app-child [value]="\'wrong\'" (changed)="accept($event.noSuchProperty)" />');
  project.invalidate(resolve(root, "counter.html"));
  const errors = await project.check();
  assert.equal(errors.length, 3, JSON.stringify(errors, null, 2));
  assert.ok(errors.every(error => error.file === resolve(root, "counter.html")));
});

test("unopened components and CSS fail full checks, compiler is reused and disposed", async t => {
  const { project, root } = await fixture(t, {
    "counter.ts": component,
    "counter.html": "<p>{{ count() }}</p>",
    "lazy/lazy.ts": `import {Component} from "@angulus/core"; @Component({selector:"app-lazy",templateUrl:"./lazy.html"}) export class Lazy {}`,
    "lazy/lazy.html": "<p>{{ unopenedError }}</p>",
  });
  const pid = project.pid;
  assert.ok((await project.check()).some(error => error.file === resolve(root, "lazy/lazy.html")));
  const compiled = await project.compile(resolve(root, "counter.ts"));
  assert.equal(project.pid, pid);
  assert.match(compiled.code, /defineComponent/);
  assert.doesNotMatch(compiled.code, /@Component/);
  assert.ok(compiled.map.sources.some(file => file.endsWith("counter.html")));
  assert.ok(compiled.map.sources.some(file => file.endsWith("counter.ts")));
  await project.close();
  assert.throws(() => process.kill(pid, 0), /ESRCH/);
});

test("unsupported dynamic URL lists fail checking rather than only failing at runtime", async t => {
  const { project, root } = await fixture(t, {
    "counter.ts": component,
    "counter.html": '<img [srcset]="\'small.png 1x, large.png 2x\'" />',
  });
  const errors = await project.check();
  assert.equal(errors.length, 1, JSON.stringify(errors));
  assert.equal(errors[0].file, resolve(root, "counter.html"));
  assert.match(errors[0].message, /Dynamic srcset/);
});
