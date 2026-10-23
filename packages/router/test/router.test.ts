import { test } from "node:test";
import assert from "node:assert/strict";
import { Window } from "happy-dom";
import { defineComponent, input, text, Scope, type ComponentType } from "@angulus/core";
import { createRouter } from "../src/index.js";

function setup(path = "/") {
  const window = new Window({ url: `http://localhost${path}` });
  const outlet = window.document.createElement("main");
  window.document.body.append(outlet);
  return { window, outlet: outlet as unknown as HTMLElement };
}
function component(label: string, destroy = () => {}) {
  class Page { onDestroy() { destroy(); } }
  defineComponent(Page, (_, parent) => { text(parent, label); });
  return Page;
}
const tick = () => new Promise(resolve => setTimeout(resolve, 0));

test("direct route mounts once with required inputs and connected rendered DOM", async (t) => {
  const { outlet } = setup("/track/101");
  const mounts: Array<{ id: string; rendered: string | null; connected: boolean }> = [];
  class TrackingPage {
    readonly routeOrderId = input.required<string>();
    onMount() {
      mounts.push({
        id: this.routeOrderId(),
        rendered: outlet.textContent,
        connected: outlet.firstChild?.isConnected ?? false,
      });
    }
  }
  defineComponent(TrackingPage, (ctx, parent) => { text(parent, ctx.routeOrderId()); });
  const router = createRouter({ outlet, routes: [{
    path: "/track/:orderId",
    load: async () => TrackingPage,
    inputs: params => ({ routeOrderId: params.orderId }),
  }] });
  t.after(() => router.destroy());

  await router.start();
  await tick();
  assert.equal(router.error(), null);
  assert.equal(outlet.textContent, "101");
  assert.deepEqual(mounts, [{ id: "101", rendered: "101", connected: true }]);
});

test("navigation, retry and not found mount each page once and clean up lifecycle resources", async (t) => {
  const { outlet } = setup("/track/101");
  const mounts: string[] = [], cleanups: string[] = [], destroyed: string[] = [];
  class TrackingPage {
    readonly routeOrderId = input.required<string>();
    onMount() {
      const id = this.routeOrderId();
      mounts.push(id);
      return () => { cleanups.push(id); };
    }
    onDestroy() { destroyed.push(this.routeOrderId()); }
  }
  class Missing {
    onMount() { mounts.push("missing"); return () => { cleanups.push("missing"); }; }
    onDestroy() { destroyed.push("missing"); }
  }
  defineComponent(TrackingPage, (ctx, parent) => { text(parent, ctx.routeOrderId()); });
  defineComponent(Missing, (_, parent) => { text(parent, "missing"); });
  const router = createRouter({ outlet, routes: [{
    path: "/track/:orderId",
    load: async () => TrackingPage,
    inputs: params => ({ routeOrderId: params.orderId }),
  }], notFound: async () => Missing });
  t.after(() => router.destroy());

  await router.start();
  await router.navigate("/track/102");
  await router.retry();
  await router.navigate("/missing");
  assert.equal(router.error(), null);
  assert.equal(outlet.textContent, "missing");
  assert.deepEqual(mounts, ["101", "102", "102", "missing"]);
  assert.deepEqual(cleanups, ["101", "102", "102"]);
  assert.deepEqual(destroyed, ["101", "102", "102"]);

  router.destroy();
  router.destroy();
  assert.equal(outlet.textContent, "");
  assert.deepEqual(mounts, ["101", "102", "102", "missing"]);
  assert.deepEqual(cleanups, mounts);
  assert.deepEqual(destroyed, mounts);
});

test("lazy route params, navigation, not found, popstate, and destruction", async () => {
  const { window, outlet } = setup("/products/hello%20world?view=full");
  let destroyed = 0, loads = 0;
  class Product {
    id = input.required<string>();
    view = input("");
    onDestroy() { destroyed++; }
  }
  defineComponent(Product, (ctx, parent) => { text(parent, `${ctx.id()}:${ctx.view()}`); });
  const Home = component("home", () => { destroyed++; });
  const router = createRouter({
    outlet,
    routes: [
      { path: "/", load: async () => Home },
      { path: "/products/:id", load: async () => { loads++; return Product; }, inputs: (params, url) => ({ id: params.id, view: url.searchParams.get("view") }) },
    ],
  });
  await router.start();
  assert.equal(outlet.textContent, "hello world:full");
  assert.equal(router.current()?.params.id, "hello world");
  assert.equal(loads, 1);
  await router.navigate("/");
  assert.equal(outlet.textContent, "home");
  assert.equal(destroyed, 1);
  window.history.replaceState(null, "", "/products/back?view=history");
  window.dispatchEvent(new window.PopStateEvent("popstate"));
  await tick();
  assert.equal(outlet.textContent, "back:history");
  assert.equal(destroyed, 2);
  await router.navigate("/missing");
  assert.equal(outlet.textContent, "Page not found");
  assert.equal(destroyed, 3);
  router.destroy();
  window.dispatchEvent(new window.PopStateEvent("popstate"));
  await tick();
  assert.equal(outlet.textContent, "");
  await assert.rejects(router.navigate("/"), /destroyed/);
});

test("load failure is visible and Retry button recovers", async () => {
  const { outlet } = setup();
  let attempts = 0;
  const Page = component("recovered");
  const router = createRouter({ outlet, routes: [{ path: "/", load: async () => {
    if (++attempts === 1) throw new Error("network unavailable");
    return Page;
  } }] });
  await router.start();
  assert.match(outlet.querySelector('[role="alert"]')!.textContent!, /network unavailable/);
  assert.ok(router.error() instanceof Error);
  outlet.querySelector("button")!.click();
  await tick();
  assert.equal(outlet.textContent, "recovered");
  assert.equal(router.error(), null);
  assert.equal(router.loading(), false);
  router.destroy();
});

test("stale successful/failed lazy loads cannot replace a newer route", async () => {
  const { outlet } = setup("/slow");
  let complete!: (ctor: ComponentType) => void;
  let reject!: (error: Error) => void;
  const Slow = component("slow"), Fast = component("fast");
  const router = createRouter({ outlet, routes: [
    { path: "/slow", load: () => new Promise(resolve => { complete = resolve; }) },
    { path: "/failed", load: () => new Promise((_, fail) => { reject = fail; }) },
    { path: "/fast", load: async () => Fast },
  ] });
  const first = router.start();
  await router.navigate("/fast");
  complete(Slow); await first;
  assert.equal(outlet.textContent, "fast");
  const failure = router.navigate("/failed");
  await router.navigate("/fast");
  reject(new Error("stale failure")); await failure;
  assert.equal(outlet.textContent, "fast");
  assert.equal(router.error(), null);
  const final = router.navigate("/slow");
  router.destroy();
  complete(Slow); await final;
  assert.equal(outlet.textContent, "");
});

test("base paths, explicit not found, rejected external URLs and owning scope", async () => {
  const { outlet } = setup("/app/nested/42");
  const scope = new Scope();
  let destroyed = 0;
  const Page = component("page", () => { destroyed++; }), Missing = component("custom missing");
  const router = scope.run(() => createRouter({
    outlet, base: "/app/",
    routes: [{ path: "/nested/:id", load: async () => Page }],
    notFound: async () => Missing,
  }));
  await router.start();
  assert.equal(outlet.textContent, "page");
  assert.equal(router.current()?.params.id, "42");
  await assert.rejects(router.navigate("https://example.com/"), /same origin/);
  await assert.rejects(router.navigate("/outside"), /base/);
  await router.navigate("/app/missing");
  assert.equal(outlet.textContent, "custom missing");
  assert.equal(destroyed, 1);
  scope.dispose();
  assert.equal(outlet.textContent, "");
});
