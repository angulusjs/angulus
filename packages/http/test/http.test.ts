import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { Scope, effect, flushSync, signal } from "@angulus/core";
import {
  createHttpClient, httpResource, HttpError,
  type HttpClient, type HttpResourceRequest,
} from "../src/index.js";

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function mockClient(handle: (url: string, init: RequestInit) => Response | Promise<Response>): HttpClient {
  return createHttpClient({ fetch: async (url, init) => handle(String(url), init ?? {}) });
}

function pendingClient() {
  const calls: Array<{
    url: string; init: RequestInit;
    resolve(response: Response): void; reject(error: unknown): void;
  }> = [];
  const client = mockClient((url, init) => new Promise<Response>((resolve, reject) => {
    calls.push({ url, init, resolve, reject });
  }));
  return { client, calls };
}

async function settle(): Promise<void> {
  await new Promise<void>(resolve => setImmediate(resolve));
  flushSync();
}

test("client appends encoded query values without mutating params or losing fragments", async () => {
  const urls: string[] = [];
  const client = mockClient((url, init) => {
    urls.push(url);
    assert.equal(init.method, "GET");
    assert.equal(init.body, undefined);
    assert.equal(new Headers(init.headers).get("Accept"), "application/json");
    assert.equal(new Headers(init.headers).has("Content-Type"), false);
    return json({ ok: true });
  });
  assert.deepEqual(await client.get("/api?old=1#section", {
    params: { q: "a & b", page: 2, active: false, tag: ["x", "y"], skipped: undefined, absent: null },
  }), { ok: true });
  assert.equal(urls[0], "/api?old=1&q=a+%26+b&page=2&active=false&tag=x&tag=y#section");
  const params = new URLSearchParams("tag=a&tag=b");
  await client.get("/api?", { params });
  assert.equal(urls[1], "/api?tag=a&tag=b");
  assert.equal(params.toString(), "tag=a&tag=b");
  await client.get("/api#anchor", { params: {} });
  assert.equal(urls[2], "/api#anchor");
  await assert.rejects(client.get(" "), /non-empty URL/);
});

test("JSON mutations serialize bodies and merge headers and fetch options", async () => {
  const calls: RequestInit[] = [];
  const defaults = new Headers({ Authorization: "Bearer test", "X-Default": "original" });
  const client = createHttpClient({
    headers: defaults,
    credentials: "include",
    fetch: async (_url, init) => { calls.push(init!); return json({ saved: true }); },
  });
  defaults.set("X-Default", "mutated");
  const controller = new AbortController();
  await client.post("/api", { name: "Ada" }, { headers: { "x-default": "override" }, signal: controller.signal, cache: "no-store" });
  await client.put("/api", null);
  await client.patch("/api", { active: false }, { credentials: "omit" });
  await client.delete("/api");
  assert.deepEqual(calls.map(call => call.method), ["POST", "PUT", "PATCH", "DELETE"]);
  assert.equal(calls[0].body, '{"name":"Ada"}');
  assert.equal(calls[1].body, "null");
  assert.equal(calls[3].body, undefined);
  assert.equal(calls[0].signal, controller.signal);
  assert.equal(calls[0].cache, "no-store");
  assert.equal(calls[0].credentials, "include");
  assert.equal(calls[2].credentials, "omit");
  assert.equal(new Headers(calls[0].headers).get("Authorization"), "Bearer test");
  assert.equal(new Headers(calls[0].headers).get("X-Default"), "override");
  assert.equal(new Headers(calls[1].headers).get("X-Default"), "original");
  assert.equal(new Headers(calls[0].headers).get("Content-Type"), "application/json");
  await client.post("/api", {}, { headers: { "Content-Type": "application/merge-patch+json", Accept: "application/problem+json" } });
  assert.equal(new Headers(calls[4].headers).get("Content-Type"), "application/merge-patch+json");
  assert.equal(new Headers(calls[4].headers).get("Accept"), "application/problem+json");
});

test("invalid request bodies reject before fetch", async () => {
  let calls = 0;
  const client = mockClient(() => { calls++; return json(null); });
  await assert.rejects(client.request("GET", "/api", { body: {} }), /cannot contain a body/);
  await assert.rejects(client.post("/api", () => 1), /JSON-serializable/);
  await assert.rejects(client.post("/api", 1n), TypeError);
  const circular: { self?: unknown } = {};
  circular.self = circular;
  await assert.rejects(client.post("/api", circular), TypeError);
  assert.equal(calls, 0);
});

test("response parsing supports JSON, no-content responses and runtime validation", async () => {
  const client = mockClient(url => url === "/empty" ? new Response(null, { status: 204 }) : json({ count: 3 }));
  assert.deepEqual(await client.get("/json"), { count: 3 });
  assert.equal(await client.delete<void>("/empty"), undefined);
  const value = await client.get("/json", { parse: value => {
    if (typeof value !== "object" || value === null || !("count" in value) || typeof value.count !== "number") throw new Error("Invalid count");
    return value.count;
  } });
  assert.equal(value, 3);
  const failed = new Error("schema rejected response");
  await assert.rejects(client.get("/json", { parse: () => { throw failed; } }), error => error === failed);
  const reset = mockClient(() => new Response(null, { status: 205 }));
  assert.equal(await reset.get("/reset"), undefined);
});

test("HTTP errors preserve status, headers, URL and JSON or text bodies", async () => {
  for (const body of ['{"message":"denied"}', "not JSON", ""]) {
    const client = mockClient(() => new Response(body, {
      status: 403, statusText: "Forbidden", headers: { "X-Request-Id": "123" },
    }));
    await assert.rejects(client.get("/private"), error => {
      assert.ok(error instanceof HttpError);
      assert.equal(error.status, 403);
      assert.equal(error.statusText, "Forbidden");
      assert.equal(error.url, "/private");
      assert.equal(error.headers.get("X-Request-Id"), "123");
      assert.deepEqual(error.body, body.startsWith("{") ? { message: "denied" } : body);
      return true;
    });
  }
});

test("malformed successful JSON is an error, not an empty success", async () => {
  for (const body of ["", "<html>fallback</html>"]) {
    const client = mockClient(() => new Response(body));
    await assert.rejects(client.get("/api"), error => {
      assert.ok(error instanceof HttpError);
      assert.equal(error.status, 200);
      assert.equal(error.body, body);
      assert.ok(error.cause instanceof SyntaxError);
      return true;
    });
  }
  const failure = new TypeError("network offline");
  const client = mockClient(() => { throw failure; });
  await assert.rejects(client.get("/api"), error => error === failure);
});

test("native fetch talks to a local backend and propagates cancellation", async t => {
  let slowStarted!: () => void;
  const started = new Promise<void>(resolve => { slowStarted = resolve; });
  const server = createServer(async (request, response) => {
    if (request.url === "/slow") { slowStarted(); return; }
    let body = "";
    for await (const chunk of request) body += chunk;
    response.setHeader("Content-Type", "application/json");
    response.end(JSON.stringify({ method: request.method, url: request.url, body: body ? JSON.parse(body) : null }));
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  t.after(async () => {
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  });
  const address = server.address();
  assert.ok(address && typeof address !== "string");
  const url = `http://127.0.0.1:${address.port}`;
  const client = createHttpClient();
  assert.deepEqual(await client.post(`${url}/users`, { name: "Ada" }, { params: { active: true } }), {
    method: "POST", url: "/users?active=true", body: { name: "Ada" },
  });
  const controller = new AbortController();
  const pending = client.get(`${url}/slow`, { signal: controller.signal });
  const aborted = assert.rejects(pending, error => error instanceof Error && error.name === "AbortError");
  await started;
  controller.abort();
  await aborted;
});

test("resource starts after construction and resolves read-only signal state", async t => {
  const { client, calls } = pendingClient();
  let initialized = false;
  const resource = httpResource<{ name: string }>(() => {
    assert.equal(initialized, true);
    return "/users/1";
  }, { client });
  t.after(() => resource.destroy());
  assert.equal(resource.status(), "idle");
  assert.equal(resource.reload(), false);
  initialized = true;
  await settle();
  assert.equal(calls.length, 1);
  assert.equal(resource.status(), "loading");
  assert.equal(resource.isLoading(), true);
  assert.equal(resource.value(), undefined);
  assert.equal(resource.error(), undefined);
  calls[0].resolve(json({ name: "Ada" }));
  await settle();
  assert.equal(resource.status(), "resolved");
  assert.equal(resource.isLoading(), false);
  assert.equal(resource.hasValue(), true);
  assert.deepEqual(resource.value(), { name: "Ada" });
});

test("resource batches signal changes and ignores superseded responses even if fetch ignores abort", async t => {
  const { client, calls } = pendingClient();
  const id = signal(1);
  const resource = httpResource<number>(() => `/users/${id()}`, { client });
  t.after(() => resource.destroy());
  await settle();
  id.set(2); id.set(3);
  flushSync();
  assert.equal(calls.length, 2);
  assert.equal(calls[1].url, "/users/3");
  assert.equal(calls[0].init.signal?.aborted, true);
  calls[1].resolve(json(3));
  await settle();
  calls[0].resolve(json(1));
  await settle();
  assert.equal(resource.value(), 3);
  assert.equal(resource.status(), "resolved");
  id.set(4); flushSync();
  assert.equal(resource.value(), undefined);
  assert.equal(resource.status(), "loading");
});

test("superseded errors cannot overwrite a newer response", async t => {
  const { client, calls } = pendingClient();
  const id = signal(1);
  const resource = httpResource(() => `/users/${id()}`, { client });
  t.after(() => resource.destroy());
  await settle();
  id.set(2); flushSync();
  calls[1].resolve(json("new"));
  await settle();
  calls[0].reject(new Error("stale failure"));
  await settle();
  assert.equal(resource.value(), "new");
  assert.equal(resource.error(), undefined);
});

test("undefined disables a resource, cancels work and clears value/error", async t => {
  const { client, calls } = pendingClient();
  const enabled = signal(false);
  const resource = httpResource(() => enabled() ? "/api" : undefined, { client });
  t.after(() => resource.destroy());
  await settle();
  assert.equal(calls.length, 0);
  assert.equal(resource.status(), "idle");
  enabled.set(true); flushSync();
  assert.equal(calls.length, 1);
  enabled.set(false); flushSync();
  assert.equal(calls[0].init.signal?.aborted, true);
  assert.equal(resource.status(), "idle");
  calls[0].resolve(json("late"));
  await settle();
  assert.equal(resource.hasValue(), false);
  assert.equal(resource.reload(), false);
});

test("reload preserves resolved values while pending and coalesces repeated calls", async t => {
  const { client, calls } = pendingClient();
  const resource = httpResource<string>(() => ({ url: "/api", params: { page: 1 } }), { client });
  t.after(() => resource.destroy());
  await settle();
  assert.equal(resource.reload(), false);
  calls[0].resolve(json("initial"));
  await settle();
  assert.equal(resource.reload(), true);
  assert.equal(resource.reload(), false);
  flushSync();
  assert.equal(resource.status(), "reloading");
  assert.equal(resource.value(), "initial");
  assert.equal(resource.isLoading(), true);
  assert.equal(calls[1].url, "/api?page=1");
  assert.equal(resource.reload(), false);
  calls[1].resolve(json("refreshed"));
  await settle();
  assert.equal(resource.value(), "refreshed");
  assert.equal(resource.status(), "resolved");
});

test("source changes clear the old value even when reload is queued in the same batch", async t => {
  const { client, calls } = pendingClient();
  const id = signal(1);
  const resource = httpResource(() => ({ url: `/api/${id()}` }), { client });
  t.after(() => resource.destroy());
  await settle();
  calls[0].resolve(json("initial"));
  await settle();
  resource.reload();
  id.set(2);
  flushSync();
  assert.equal(resource.status(), "loading");
  assert.equal(resource.value(), undefined);
  assert.equal(calls[1].url, "/api/2");
});

test("request and parse failures become error state and reload retries", async t => {
  const { client, calls } = pendingClient();
  const resource = httpResource(() => "/api", { client, parse: value => {
    if (typeof value !== "string") throw new TypeError("Expected a string");
    return value;
  } });
  t.after(() => resource.destroy());
  await settle();
  calls[0].resolve(json("initial"));
  await settle();
  resource.reload(); flushSync();
  calls[1].resolve(json({ invalid: true }));
  await settle();
  assert.equal(resource.status(), "error");
  assert.equal(resource.isLoading(), false);
  assert.equal(resource.value(), undefined);
  assert.ok(resource.error() instanceof TypeError);
  assert.equal(resource.reload(), true);
  flushSync();
  assert.equal(resource.error(), undefined);
  calls[2].resolve(json({ message: "unavailable" }, 503));
  await settle();
  assert.ok(resource.error() instanceof HttpError);
  resource.reload(); flushSync();
  calls[3].resolve(json("recovered"));
  await settle();
  assert.equal(resource.status(), "resolved");
  assert.equal(resource.value(), "recovered");
});

test("factory exceptions are visible and reactive dependencies can recover", async t => {
  const { client, calls } = pendingClient();
  const ready = signal(false);
  const failure = new Error("not configured");
  const resource = httpResource(() => {
    if (!ready()) throw failure;
    return "/api";
  }, { client });
  t.after(() => resource.destroy());
  await settle();
  assert.equal(resource.status(), "error");
  assert.equal(resource.error(), failure);
  assert.equal(calls.length, 0);
  resource.reload(); flushSync();
  assert.equal(resource.error(), failure);
  ready.set(true); flushSync();
  calls[0].resolve(json(true));
  await settle();
  assert.equal(resource.value(), true);
});

test("async parsing keeps resources loading and superseded parsing cannot publish", async t => {
  const parsed: Array<(value: string) => void> = [];
  const id = signal(1);
  const client = mockClient(() => json({ name: "Ada" }));
  const resource = httpResource(() => `/api/${id()}`, {
    client,
    parse: () => new Promise<string>(resolve => { parsed.push(resolve); }),
  });
  t.after(() => resource.destroy());
  await settle();
  assert.equal(resource.status(), "loading");
  id.set(2); flushSync();
  await settle();
  parsed[0]("stale");
  await settle();
  assert.equal(resource.status(), "loading");
  assert.equal(resource.value(), undefined);
  parsed[1]("current");
  await settle();
  assert.equal(resource.value(), "current");
  assert.equal(await client.get("/api", { parse: async () => "validated" }), "validated");
  const failure = new Error("async parser failed");
  await assert.rejects(client.get("/api", { parse: async () => { throw failure; } }), error => error === failure);
});

test("resource only tracks the request factory, not signals read inside the client", async t => {
  const incidental = signal(0);
  let calls = 0;
  const client = mockClient(() => { incidental(); calls++; return json(true); });
  const resource = httpResource(() => "/api", { client });
  t.after(() => resource.destroy());
  await settle();
  incidental.set(1); flushSync();
  await settle();
  assert.equal(calls, 1);
});

test("scope disposal aborts resources and destruction before startup prevents fetch", async () => {
  const { client, calls } = pendingClient();
  const parent = new Scope();
  const id = signal(1);
  const resource = parent.run(() => httpResource(() => `/api/${id()}`, { client }));
  await settle();
  parent.dispose();
  assert.equal(calls[0].init.signal?.aborted, true);
  assert.equal(resource.status(), "idle");
  assert.equal(resource.reload(), false);
  id.set(2); flushSync();
  calls[0].resolve(json("late"));
  await settle();
  assert.equal(resource.value(), undefined);
  assert.equal(calls.length, 1);
  resource.destroy();
  const cancelled = httpResource(() => "/never", { client });
  cancelled.destroy();
  await settle();
  assert.equal(calls.length, 1);
  const disposed = new Scope();
  disposed.dispose();
  assert.throws(() => httpResource(() => "/never", { scope: disposed }), /disposed/);
});

test("explicit resource destruction updates observers without leaking dependencies", async t => {
  const { client, calls } = pendingClient();
  const scope = new Scope();
  t.after(() => scope.dispose());
  const resource = httpResource(() => "/api", { client, scope });
  const states: string[] = [];
  scope.run(() => effect(() => { states.push(resource.status()); }));
  await settle();
  calls[0].resolve(json("value"));
  await settle();
  resource.destroy();
  flushSync();
  assert.deepEqual(states, ["idle", "loading", "resolved", "idle"]);
});

// Compiled by the workspace type check, never called at runtime.
function typeContracts(): void {
  const client = createHttpClient();
  const value: Promise<{ name: string }> = client.get("/api", {
    parse: value => {
      if (typeof value !== "string") throw new TypeError("Expected string");
      return { name: value };
    },
  });
  const resource = httpResource(() => "/api", { parse: value => String(value) });
  const name: string | undefined = resource.value();
  const asyncResource = httpResource(() => "/api", { parse: async () => "parsed" });
  const asyncName: string | undefined = asyncResource.value();
  const asyncResult: Promise<string> = client.get("/api", { parse: async () => "parsed" });
  // @ts-expect-error Resource signals cannot be mutated.
  resource.value.set("bad");
  // @ts-expect-error Resources accept GET descriptors, not mutations.
  const mutation: HttpResourceRequest = { url: "/api", method: "POST" };
  // @ts-expect-error Lifecycle cancellation is owned by the resource.
  const externalAbort: HttpResourceRequest = { url: "/api", signal: new AbortController().signal };
  // @ts-expect-error GET accepts no JSON request body.
  client.get("/api", { body: {} });
  // @ts-expect-error Request methods are explicit supported verbs.
  client.request("GETT", "/api");
  void [value, name, asyncName, asyncResult, mutation, externalAbort];
}
