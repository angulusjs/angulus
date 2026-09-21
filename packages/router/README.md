# @angulus/router

```ts
const router = createRouter({
  outlet: document.querySelector<HTMLElement>("#outlet")!,
  routes: [
    {path: "/", load: () => import("./home").then(m => m.Home)},
    {
      path: "/products/:id",
      load: () => import("./product").then(m => m.Product),
      inputs: params => ({id: params.id}),
    },
  ],
  notFound: () => import("./not-found").then(m => m.NotFound),
});
await router.start();
await router.navigate("/products/42");
// router.destroy() when the owning application ends
```

Routes are matched in declaration order, support named path parameters and a
whole-path `*` fallback, and load a **component constructor**, not a module object.
Inputs are mapped explicitly; params are decoded and queries are available as the
second `inputs` argument (`URL`). Route changes dispose the previous component
immediately, show a loading message, and ignore stale lazy-load results.
Errors appear in an alert with a Retry button; `router.error()`, `loading()`, and
`current()` are signals. `retry()` retries the current browser URL.

`navigate(path, {replace: true})` uses browser history without reloading.
`start()` subscribes to popstate for back/forward. Ordinary anchors are **not**
automatically intercepted; call `navigate` from a click handler after
`preventDefault`. Navigation must stay same-origin and within optional `base`.
Route config paths are relative to base; navigation URLs include base
(base `/app`, route `/products/:id`, navigate `/app/products/42`).
The outlet is exclusively owned by the router. Creating it inside an active Angulus
scope also ties disposal to that scope. Destroy removes the popstate listener,
status listeners, the active component, and invalidates pending loads.

No nested layouts, guards, redirects, SSR, or route providers are implemented.
The HTTP server must separately support deep-link SPA fallback.
