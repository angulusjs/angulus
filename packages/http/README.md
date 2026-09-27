# @angulus/http

A JSON HTTP client built on `fetch` and a signal-based `httpResource` for reactive
reads. Inspired by Angular Resource, without Angular DI, RxJS, or new template
syntax. This is not an Angular API compatibility layer.

```sh
npm install @angulus/core @angulus/http
```

Use matching Angulus versions from a release containing HTTP support.

## Reactive reads

```ts
import { Component, signal } from "@angulus/core";
import { httpResource } from "@angulus/http";

interface User { id: number; name: string }

@Component({ selector: "app-user", templateUrl: "./user.html" })
export class UserProfile {
  readonly id = signal<number | undefined>(1);
  readonly user = httpResource<User>(() => {
    const id = this.id();
    return id === undefined ? undefined : `/api/users/${id}`;
  });
}
```

```html
@if (user.isLoading()) {
  <p>Loading...</p>
}
@if (user.status() === "error") {
  <p role="alert">Unable to load this user.</p>
}
@if (user.hasValue()) {
  <p>{{ user.value()?.name }}</p>
}
<button [disabled]="user.isLoading()" (click)="user.reload()">Reload</button>
```

The factory reads signals and returns a URL, a GET request descriptor, or
`undefined` to disable loading:

```ts
const results = httpResource(() => ({
  url: "/api/search",
  params: { q: search(), page: page(), tag: ["typescript", "signals"] },
  headers: { Accept: "application/json" },
  credentials: "same-origin",
  cache: "no-store",
}));
```

`params` accepts `URLSearchParams` or a record of strings, numbers, booleans, and
arrays of those values. Arrays become repeated keys; `null` and `undefined` are
omitted. Existing queries and URL fragments are preserved. URLs must be non-empty.

## State and lifetime

| Member | Contract |
| --- | --- |
| `value()` | Current data, or `undefined` when unavailable |
| `error()` | Original failure, or `undefined` outside the error state |
| `status()` | `idle`, `loading`, `reloading`, `resolved`, or `error` |
| `isLoading()` | `true` during loading or reloading |
| `hasValue()` | Whether `value()` is different from `undefined` |
| `reload()` | Queue a refresh or retry; returns whether one was queued |
| `destroy()` | Abort, detach reactive dependencies, and reset to idle; idempotent |

The first request is scheduled in a microtask, after construction and synchronous
mounting. Required component inputs are therefore available to the factory.
Subsequent signal changes use core effect batching. Only the factory's signal
reads are dependencies; signals read in a custom client or parser are not.

A changed request clears old data and starts `loading`. An explicit reload of a
resolved request retains data while `reloading`. Failures clear data and enter
`error`; `value()` does not throw. Inspect `status()` rather than the truthiness
of `error()`, since JavaScript can reject with any value.

Reload returns `false` while idle, loading, destroyed, or already queued.
Repeated reloads in one batch are coalesced. Disabling a request clears data and
errors. Source-factory and runtime-parser exceptions are also exposed as errors.

Each new request aborts the preceding one. Late results and errors from
superseded requests are ignored even if a custom fetch implementation ignores
abort. Returning `undefined` and destroying the resource also cancel pending work.
Resources only perform GET requests: do not put mutations in reactive factories.

Resources created during component construction belong to its active disposal
scope. They abort automatically when the component or route is destroyed.
Outside a component, pass `{ scope }` or call `destroy()` explicitly. Destroying
a resource before its initial microtask prevents the request entirely.

## Explicit API calls and mutations

```ts
import { createHttpClient } from "@angulus/http";

const api = createHttpClient({
  credentials: "same-origin",
  headers: { Accept: "application/json" },
});

const users = await api.get<User[]>("/api/users", { params: { active: true } });
const created = await api.post<User>("/api/users", { name: "Ada" });
await api.put<User>(`/api/users/${created.id}`, { name: "Ada Lovelace" });
await api.patch<User>(`/api/users/${created.id}`, { active: false });
await api.delete<void>(`/api/users/${created.id}`);
```

`request(method, url, options)` is the generic entry point and accepts an optional
`body` alongside request options. Methods are `GET`, `POST`, `PUT`, `PATCH`, and
`DELETE`. GET bodies are rejected. Mutations are **never** automatically retried.
After a successful mutation, refresh the appropriate resource with `reload()`.
Mutation loading/error UI is application-owned; await calls and handle failures
in your action handler.

Bodies use `JSON.stringify`. `Accept: application/json` is added unless supplied;
`Content-Type: application/json` is added only when a body is supplied and no
content type was specified. Per-request headers override client defaults
case-insensitively, without mutating the caller's headers. Request options also
accept Fetch settings such as `credentials`, `cache`, `mode`, and `signal`.
Without a credentials setting, Fetch's normal same-origin default applies.

The client accepts `{ fetch }` for testing or a custom transport. It does not
configure a backend URL, cookies, authentication, CORS, or CSRF protection on your
behalf. Use relative URLs with a same-origin backend or the existing CLI proxy:

```json
{
  "proxy": { "/api": "http://localhost:3000" }
}
```

For explicit calls, cancellation is caller-owned:

```ts
const controller = new AbortController();
const pending = api.get("/api/users", { signal: controller.signal });
// Abort from the application's cancellation/lifecycle handler.
controller.abort();
try {
  await pending;
} catch (error) {
  if (!(error instanceof Error) || error.name !== "AbortError") throw error;
  console.info("Request cancelled");
}
```

## Response validation and errors

By default, responses are `unknown`. A generic such as `get<User>()` or
`httpResource<User>()` is a TypeScript assertion, **not runtime validation**.
Supply `parse` to validate the decoded JSON and infer the result type:

```ts
function parseUser(value: unknown): User {
  if (typeof value !== "object" || value === null ||
      !("id" in value) || typeof value.id !== "number" ||
      !("name" in value) || typeof value.name !== "string") {
    throw new TypeError("Invalid user response");
  }
  return { id: value.id, name: value.name };
}

const user = httpResource(() => "/api/users/1", { client: api, parse: parseUser });
const validated = await api.get("/api/users/1", { parse: parseUser });
```

Parsers may return a value or a promise. Resources remain loading until parsing
finishes; results from superseded parsing are ignored too.

Non-2xx responses reject with `HttpError`, carrying `status`, `statusText`, `url`,
`headers`, and `body`. Error bodies are decoded as JSON when possible, otherwise
preserved as text. Malformed JSON in a successful response is also an `HttpError`,
with the response status, raw body, and a `SyntaxError` cause.

HTTP 204/205 resolve to `undefined` (or pass `undefined` to `parse`). An empty 200
response is invalid JSON, not a successful empty result. Network, abort, body-read,
and custom parser errors propagate unchanged.

## Scope of this version

No shared cache, deduplication, automatic retry, interceptors, uploads, streaming,
binary/text response modes, or optimistic mutations. Each resource owns its
requests independently. Fetch cancellation does not undo work already performed
by a server.

The source demo at `/http` uses [local JSON fixtures](../../examples/demo/public/data/users)
and the same resource/client as backend requests, without requiring an external
service. Replace its URLs with your API endpoints when integrating a backend.
