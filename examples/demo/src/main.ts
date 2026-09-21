/// <reference types="vite/client" />
import { createRouter } from "@angulus/router";
import "./global.css";

const outlet = document.querySelector<HTMLElement>("#app");
if (!outlet) throw new Error("Missing application outlet");

const router = createRouter({
  outlet,
  routes: [
    { path: "/", load: () => import("./counter/counter").then(module => module.CounterComponent) },
    {
      path: "/products/:id",
      load: () => import("./products/product").then(module => module.ProductComponent),
      inputs: params => ({ id: params.id }),
    },
  ],
  notFound: () => import("./not-found/not-found").then(module => module.NotFoundComponent),
});

function navigate(event: MouseEvent): void {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  const anchor = event.target instanceof Element ? event.target.closest<HTMLAnchorElement>("a[data-link]") : null;
  if (!anchor || anchor.download || (anchor.target && anchor.target !== "_self")) return;
  const url = new URL(anchor.href);
  if (url.origin !== location.origin) return;
  event.preventDefault();
  void router.navigate(url.pathname + url.search + url.hash);
}

function dispose(): void {
  document.removeEventListener("click", navigate);
  window.removeEventListener("pagehide", pagehide);
  router.destroy();
}

function pagehide(event: PageTransitionEvent): void {
  if (!event.persisted) dispose();
}

document.addEventListener("click", navigate);
window.addEventListener("pagehide", pagehide);
import.meta.hot?.dispose(dispose);
void router.start();
