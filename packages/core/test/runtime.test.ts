import { test } from "node:test";
import assert from "node:assert/strict";
import { Window } from "happy-dom";
import {
  Scope, signal, computed, effect, flushSync, input, output, Component,
  defineComponent, mount, onMount, onDestroy, element, text, bind, listen,
  setProperty, ifBlock, forBlock, switchBlock, mountChild,
} from "../src/index.js";

function host() {
  const window = new Window({ url: "http://localhost/" });
  const node = window.document.createElement("main");
  window.document.body.append(node);
  return node as unknown as HTMLElement;
}

test("signals, lazy computed, dynamic dependencies, cleanup and synchronous batching", () => {
  const scope = new Scope();
  const a = signal(1), b = signal(10), toggle = signal(true);
  let calculations = 0, runs = 0, cleaned = 0;
  const selected = scope.run(() => computed(() => { calculations++; return toggle() ? a() : b(); }));
  assert.equal(calculations, 0);
  let value = 0;
  scope.run(() => effect(() => { runs++; value = selected(); return () => { cleaned++; }; }));
  assert.equal(value, 1);
  a.set(2); a.set(3);
  assert.equal(runs, 1);
  flushSync();
  assert.equal(runs, 2);
  assert.equal(value, 3);
  toggle.set(false);
  flushSync();
  assert.equal(value, 10);
  a.set(4); flushSync();
  assert.equal(runs, 3);
  b.update(n => n + 1); flushSync();
  assert.equal(value, 11);
  scope.dispose(); scope.dispose();
  assert.equal(cleaned, 4);
  b.set(12); flushSync();
  assert.equal(runs, 4);
});

test("microtask scheduler batches writes and errors do not prevent unrelated effects", async () => {
  const count = signal(0);
  let runs = 0;
  const stop = effect(() => { count(); runs++; });
  count.set(1); count.set(2);
  await Promise.resolve();
  assert.equal(runs, 2);
  stop();
  const stopBad = effect(() => { if (count() === 3) throw new Error("visible effect error"); });
  let last = 0;
  const stopGood = effect(() => { last = count(); });
  count.set(3);
  assert.throws(() => flushSync(), /visible effect error/);
  assert.equal(last, 3);
  stopBad(); stopGood();
});

test("independent component instances, event this, lifecycles, and disposal", () => {
  const order: string[] = [];
  class Counter {
    count = signal(0);
    constructor() { onMount(() => { order.push("registered mount"); return () => { order.push("mount cleanup"); }; }); }
    increment() { this.count.update(n => n + 1); }
    onMount() { order.push("class mount"); }
    onDestroy() { order.push("class destroy"); }
  }
  Component({ selector: "a-counter", templateUrl: "./counter.html" })(Counter);
  defineComponent(Counter, (ctx, parent, scope) => {
    const button = element(scope, parent, "button", "f-test");
    const label = text(button, "");
    bind(scope, () => setProperty(label, "data", ctx.count()));
    listen(scope, button, "click", () => ctx.increment());
  });
  const a = host(), b = host();
  const first = mount(Counter, a), second = mount(Counter, b);
  const button = a.querySelector("button")!;
  button.click(); flushSync();
  assert.equal(a.textContent, "1");
  assert.equal(b.textContent, "0");
  assert.equal(button.getAttribute("data-f-test"), "");
  assert.deepEqual(order.slice(0, 2), ["registered mount", "class mount"]);
  first.destroy();
  button.click(); flushSync();
  assert.equal(first.instance.count(), 1);
  assert.equal(a.childNodes.length, 0);
  second.destroy();
  assert.equal(order.filter(x => x === "class destroy").length, 2);
  assert.equal(order.filter(x => x === "mount cleanup").length, 2);
});

test("pagehide preserves BFCache state and disposes root mounts on actual unload", () => {
  let destroyed = 0;
  class Page {
    readonly value = signal(1);
    onDestroy() { destroyed++; }
  }
  defineComponent(Page, (ctx, parent, scope) => {
    const label = text(parent, "");
    bind(scope, () => setProperty(label, "data", ctx.value()));
  });
  const container = host();
  const ref = mount(Page, container);
  const pagehide = (persisted: boolean) => {
    const event = container.ownerDocument.createEvent("Event");
    event.initEvent("pagehide", false, false);
    Object.defineProperty(event, "persisted", { value: persisted });
    container.ownerDocument.defaultView!.dispatchEvent(event);
  };
  pagehide(true);
  assert.equal(ref.scope.disposed, false);
  ref.instance.value.set(2);
  flushSync();
  assert.equal(container.textContent, "2");
  pagehide(false);
  assert.equal(ref.scope.disposed, true);
  assert.equal(container.textContent, "");
  pagehide(false);
  ref.destroy();
  assert.equal(destroyed, 1);
});

test("required/default inputs and outputs connect before render and mount", () => {
  const events: string[] = [];
  class Child {
    name = input.required<string>();
    suffix = input("!");
    changed = output<string>();
    onMount() { this.changed.emit(this.name()); }
  }
  defineComponent(Child, (ctx, parent, scope) => {
    const label = text(parent, "");
    bind(scope, () => { label.data = ctx.name() + ctx.suffix(); });
  });
  assert.throws(() => input.required<string>()(), /before it was set/);
  assert.throws(() => mount(Child, host()), /Missing required input "name"/);
  const name = signal("one");
  class Parent {}
  defineComponent(Parent, (_, parent, scope) => {
    mountChild(scope, parent, Child, { name }, { changed: value => events.push(value) });
  });
  const node = host();
  const ref = mount(Parent, node);
  assert.equal(node.textContent, "one!");
  assert.deepEqual(events, ["one"]);
  name.set("two"); flushSync();
  assert.equal(node.textContent, "two!");
  ref.destroy();
  name.set("three"); flushSync();
  assert.equal(node.textContent, "");
  const standalone = mount(Child, node, { inputs: { name: "root" } });
  assert.equal(node.textContent, "root!");
  standalone.destroy();
});

test("if/switch branches release effects, listeners and child components", () => {
  const node = host(), scope = new Scope();
  const show = signal(true), kind = signal("a"), count = signal(1);
  let destroyed = 0, reads = 0;
  class Child { onDestroy() { destroyed++; } }
  defineComponent(Child, (_, parent, child) => {
    const label = text(parent, "");
    bind(child, () => { reads++; label.data = String(count()); });
  });
  ifBlock(scope, node, show, (parent, child) => {
    mountChild(child, parent, Child);
  }, parent => { text(parent, "off"); });
  switchBlock(scope, node, kind, [
    { test: () => "a", render: parent => { text(parent, "A"); } },
    { test: () => "b", render: parent => { text(parent, "B"); } },
    { render: parent => { text(parent, "default"); } },
  ]);
  assert.equal(node.textContent, "1A");
  show.set(false); kind.set("b"); flushSync();
  assert.equal(node.textContent, "offB");
  assert.equal(destroyed, 1);
  count.set(2); kind.set("unknown"); flushSync();
  assert.equal(reads, 1);
  assert.equal(node.textContent, "offdefault");
  show.set(true); flushSync();
  assert.equal(node.textContent, "2default");
  scope.dispose();
  assert.equal(destroyed, 2);
  assert.equal(node.childNodes.length, 0);
});

test("keyed reconciliation preserves components, updates values/index, removes and rejects duplicates", () => {
  const node = host(), scope = new Scope();
  const items = signal([{ id: 1, value: "a" }, { id: 2, value: "b" }]);
  let constructed = 0, destroyed = 0;
  class Child {
    value = input.required<string>();
    constructor() { constructed++; }
    onDestroy() { destroyed++; }
  }
  defineComponent(Child, (ctx, parent, scope) => {
    const label = text(parent, "");
    bind(scope, () => { label.data = ctx.value(); });
  });
  forBlock(scope, node, items, item => item.id, (parent, child, item, index) => {
    const row = element(child, parent, "div");
    bind(child, () => { row.dataset.index = String(index()); });
    mountChild(child, row, Child, { value: () => item().value });
  });
  const first = node.querySelectorAll("div")[0], second = node.querySelectorAll("div")[1];
  items.set([{ id: 2, value: "B" }, { id: 1, value: "A" }]); flushSync();
  assert.equal(node.querySelectorAll("div")[0], second);
  assert.equal(node.querySelectorAll("div")[1], first);
  assert.equal(node.textContent, "BA");
  assert.equal(first!.dataset.index, "1");
  assert.equal(constructed, 2);
  items.set([{ id: 1, value: "AA" }]); flushSync();
  assert.equal(node.querySelector("div"), first);
  assert.equal(destroyed, 1);
  items.set([{ id: 1, value: "bad" }, { id: 1, value: "duplicate" }]);
  assert.throws(() => flushSync(), /Duplicate @for key: 1/);
  assert.equal(node.textContent, "AA");
  items.set([{ id: 3, value: "C" }]); flushSync();
  assert.equal(node.textContent, "C");
  assert.equal(destroyed, 2);
  scope.dispose();
  assert.equal(destroyed, 3);
  assert.equal(node.childNodes.length, 0);
});

test("all cleanup runs on errors, failed mounts clean up, uncompiled classes reject", () => {
  const scope = new Scope();
  let cleaned = 0;
  scope.add(() => { cleaned++; });
  scope.add(() => { throw new Error("destroy failed"); });
  scope.add(() => { cleaned++; });
  assert.throws(() => scope.dispose(), /destroy failed/);
  assert.equal(cleaned, 2);
  class Broken { constructor() { onDestroy(() => { cleaned++; }); } }
  defineComponent(Broken, () => { throw new Error("render failed"); });
  assert.throws(() => mount(Broken, host()), /render failed/);
  assert.equal(cleaned, 3);
  class Uncompiled {}
  assert.equal(Component({ selector: "uncompiled-test", templateUrl: "./test.html" })(Uncompiled), Uncompiled);
  assert.ok(new Uncompiled() instanceof Uncompiled);
  assert.throws(() => mount(Uncompiled, host()), /has not been compiled/);
});

test("nested mount hooks see connected DOM and run before their parent", () => {
  const node = host(), order: string[] = [];
  class Child {
    onMount() {
      assert.equal(node.querySelector("span")?.isConnected, true);
      order.push("child");
    }
  }
  defineComponent(Child, (_, parent, scope) => { element(scope, parent, "span"); });
  class Parent { onMount() { order.push("parent"); } }
  defineComponent(Parent, (_, parent, scope) => {
    ifBlock(scope, parent, () => true, (parent, scope) => { mountChild(scope, parent, Child); });
  });
  const ref = mount(Parent, node);
  assert.deepEqual(order, ["child", "parent"]);
  ref.destroy();
});

test("computed failures remain visible and recover when their sources change", () => {
  const source = signal(1);
  const value = computed(() => {
    const n = source();
    if (n === 0) throw new Error("zero");
    return 10 / n;
  });
  let seen = 0;
  const stop = effect(() => { seen = value(); });
  source.set(0);
  assert.throws(() => flushSync(), /zero/);
  source.set(2); flushSync();
  assert.equal(seen, 5);
  stop();
});

test("dynamic URL bindings reject executable schemes and raw HTML properties", () => {
  const node = host();
  const label = text(node, "");
  setProperty(label, "data", "javascript: is safe as plain text");
  assert.equal(node.textContent, "javascript: is safe as plain text");
  const link = node.ownerDocument.createElement("a");
  setProperty(link, "href", "/products/42");
  assert.equal(link.getAttribute("href"), "/products/42");
  setProperty(link, "attr.href", "https://example.com/");
  assert.equal(link.href, "https://example.com/");
  setProperty(link, "href", "mailto:hello@example.com");
  for (const value of ["javascript:alert(1)", " \nJaVa\tScRiPt:alert(1)", "vbscript:msgbox(1)", "data:text/html,<script>bad()</script>"]) {
    assert.throws(() => setProperty(link, "href", value), /Unsafe URL scheme/);
    assert.throws(() => setProperty(link, "attr.href", value), /Unsafe URL scheme/);
  }
  assert.throws(() => setProperty(node, "innerHTML", "<b>raw</b>"), /Unsafe DOM property/);
  assert.throws(() => setProperty(node, "attr.onclick", "bad()"), /Unsafe DOM property/);
  assert.throws(() => setProperty(node, "srcdoc", "<b>raw</b>"), /Unsafe DOM property/);
  assert.throws(() => setProperty(node, "srcset", "first.png 1x, second.png 2x"), /not supported/);
  setProperty(link, "attr.href", null);
  assert.equal(link.hasAttribute("href"), false);
});
