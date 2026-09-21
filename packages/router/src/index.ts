import { Scope, mount, signal, type ComponentRef, type ComponentType, type Signal } from "@angulus/core";

export interface Route {
  path: string;
  load: () => Promise<ComponentType>;
  inputs?: (params: Readonly<Record<string, string>>, url: URL) => Record<string, unknown>;
}
export interface RouteState {
  path: string;
  params: Readonly<Record<string, string>>;
  url: URL;
  route: Route | null;
}
export interface RouterOptions {
  outlet: HTMLElement;
  routes: readonly Route[];
  notFound?: () => Promise<ComponentType>;
  base?: string;
}
export interface Router {
  readonly current: Signal<RouteState | null>;
  readonly error: Signal<unknown | null>;
  readonly loading: Signal<boolean>;
  start(): Promise<void>;
  navigate(path: string, options?: { replace?: boolean }): Promise<void>;
  retry(): Promise<void>;
  destroy(): void;
}

function normalizeBase(base: string): string {
  if (!base.startsWith("/") || base.includes("?") || base.includes("#")) throw new Error("Router base must be an absolute URL pathname");
  return base.replace(/\/+$/, "");
}
function match(pattern: string, pathname: string): Record<string, string> | null {
  if (pattern === "*") return {};
  const expected = pattern.split("/").filter(Boolean);
  const actual = pathname.split("/").filter(Boolean);
  if (expected.length !== actual.length) return null;
  const params: Record<string, string> = Object.create(null);
  for (let index = 0; index < expected.length; index++) {
    const segment = expected[index]!;
    const value = decodeURIComponent(actual[index]!);
    if (segment.startsWith(":")) params[segment.slice(1)] = value;
    else if (decodeURIComponent(segment) !== value) return null;
  }
  return params;
}

/** Route paths are relative to base; navigate() accepts same-origin browser URLs including base. */
export function createRouter(options: RouterOptions): Router {
  const { outlet } = options;
  const window = outlet.ownerDocument.defaultView;
  if (!window) throw new Error("Router outlet must belong to a browser document");
  const base = normalizeBase(options.base ?? "/");
  for (const route of options.routes) {
    if (route.path !== "*" && !route.path.startsWith("/")) throw new Error(`Route path must start with "/": ${route.path}`);
  }
  const scope = new Scope();
  const current = signal<RouteState | null>(null);
  const error = signal<unknown | null>(null);
  const loading = signal(false);
  let version = 0;
  let started = false;
  let mounted: ComponentRef<unknown> | undefined;
  let statusCleanup: (() => void) | undefined;
  function clear(): void {
    statusCleanup?.();
    statusCleanup = undefined;
    const previous = mounted;
    mounted = undefined;
    try { previous?.destroy(); } finally { outlet.replaceChildren(); }
  }
  function showError(reason: unknown): void {
    error.set(reason);
    const panel = outlet.ownerDocument.createElement("div");
    panel.setAttribute("role", "alert");
    const message = outlet.ownerDocument.createElement("p");
    message.textContent = `Unable to load route: ${reason instanceof Error ? reason.message : String(reason)}`;
    const retry = outlet.ownerDocument.createElement("button");
    retry.type = "button";
    retry.textContent = "Retry";
    const onRetry = () => { void resolve(); };
    retry.addEventListener("click", onRetry);
    statusCleanup = () => retry.removeEventListener("click", onRetry);
    panel.append(message, retry);
    outlet.replaceChildren(panel);
  }
  async function resolve(): Promise<void> {
    if (scope.disposed) throw new Error("Router has been destroyed");
    const request = ++version;
    error.set(null);
    loading.set(true);
    try {
      clear();
      outlet.textContent = "Loading…";
      const url = new URL(window!.location.href);
      const pathname = url.pathname;
      const withinBase = !base || pathname === base || pathname.startsWith(`${base}/`);
      const local = withinBase ? pathname.slice(base.length) || "/" : null;
      let selected: Route | null = null;
      let params: Record<string, string> = {};
      if (local !== null) {
        for (const route of options.routes) {
          const result = match(route.path, local);
          if (result) { selected = route; params = result; break; }
        }
      }
      const state = { path: local ?? pathname, params, url, route: selected };
      const load = selected?.load ?? options.notFound;
      if (!load) {
        current.set(state);
        outlet.textContent = "Page not found";
        return;
      }
      const ctor = await load();
      if (request !== version || scope.disposed) return;
      outlet.replaceChildren();
      const next = mount(ctor, outlet, { scope, inputs: selected?.inputs?.(params, url) });
      if (request !== version || scope.disposed) { next.destroy(); return; }
      mounted = next;
      current.set(state);
    } catch (reason) {
      if (request !== version || scope.disposed) return;
      current.set(null);
      showError(reason);
    } finally {
      if (request === version && !scope.disposed) loading.set(false);
    }
  }
  const onPopState = () => { void resolve(); };
  scope.add(() => {
    version++;
    window.removeEventListener("popstate", onPopState);
    loading.set(false);
    clear();
  });
  return {
    current, error, loading,
    start() {
      if (scope.disposed) return Promise.reject(new Error("Router has been destroyed"));
      if (!started) { started = true; window.addEventListener("popstate", onPopState); }
      return resolve();
    },
    navigate(path, navigation = {}) {
      if (scope.disposed) return Promise.reject(new Error("Router has been destroyed"));
      const url = new URL(path, window.location.href);
      if (url.origin !== window.location.origin) return Promise.reject(new Error("Router navigation must stay on the same origin"));
      if (base && url.pathname !== base && !url.pathname.startsWith(`${base}/`)) {
        return Promise.reject(new Error(`Router navigation must stay under base "${base}"`));
      }
      window.history[navigation.replace ? "replaceState" : "pushState"](null, "", url);
      return resolve();
    },
    retry: resolve,
    destroy: () => scope.dispose(),
  };
}
