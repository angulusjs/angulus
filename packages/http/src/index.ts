import { Scope, computed, effect, signal, untracked, type Signal } from "@angulus/core";

export type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
export type HttpParam = string | number | boolean;
export type HttpParams = URLSearchParams | Readonly<Record<string, HttpParam | readonly HttpParam[] | null | undefined>>;
export interface HttpRequestOptions<T = unknown> extends Omit<RequestInit, "method" | "body"> {
  readonly params?: HttpParams;
  readonly parse?: (value: unknown) => T | PromiseLike<T>;
}
export interface HttpClientOptions {
  readonly fetch?: typeof globalThis.fetch;
  readonly headers?: HeadersInit;
  readonly credentials?: RequestCredentials;
}
export interface HttpClient {
  request<T = unknown>(method: HttpMethod, url: string, options?: HttpRequestOptions<T> & { readonly body?: unknown }): Promise<T>;
  get<T = unknown>(url: string, options?: HttpRequestOptions<T>): Promise<T>;
  post<T = unknown>(url: string, body?: unknown, options?: HttpRequestOptions<T>): Promise<T>;
  put<T = unknown>(url: string, body?: unknown, options?: HttpRequestOptions<T>): Promise<T>;
  patch<T = unknown>(url: string, body?: unknown, options?: HttpRequestOptions<T>): Promise<T>;
  delete<T = unknown>(url: string, options?: HttpRequestOptions<T>): Promise<T>;
}

export class HttpError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly statusText: string,
    readonly url: string,
    readonly headers: Headers,
    readonly body: unknown,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "HttpError";
  }
}

function requestUrl(url: string, params?: HttpParams): string {
  if (typeof url !== "string" || !url.trim()) throw new TypeError("HTTP requests require a non-empty URL");
  const query = new URLSearchParams();
  if (params instanceof URLSearchParams) {
    params.forEach((value, key) => query.append(key, value));
  } else {
    for (const [key, value] of Object.entries(params ?? {})) {
      if (value == null) continue;
      for (const entry of Array.isArray(value) ? value : [value]) query.append(key, String(entry));
    }
  }
  const encoded = query.toString();
  if (!encoded) return url;
  const hash = url.indexOf("#");
  const path = hash < 0 ? url : url.slice(0, hash);
  const fragment = hash < 0 ? "" : url.slice(hash);
  const separator = path.endsWith("?") || path.endsWith("&") ? "" : path.includes("?") ? "&" : "?";
  return `${path}${separator}${encoded}${fragment}`;
}

function responseError(response: Response, url: string, body: unknown, cause?: SyntaxError): HttpError {
  return new HttpError(
    cause ? `Invalid JSON response from ${url}` : `HTTP ${response.status} ${response.statusText}: ${url}`,
    response.status, response.statusText, response.url || url, new Headers(response.headers), body,
    cause ? { cause } : undefined,
  );
}

export function createHttpClient(options: HttpClientOptions = {}): HttpClient {
  const defaults = new Headers(options.headers);
  const credentials = options.credentials;
  const fetcher = options.fetch ?? ((input, init) => globalThis.fetch(input, init));
  async function request<T = unknown>(
    method: HttpMethod,
    url: string,
    options: HttpRequestOptions<T> & { readonly body?: unknown } = {},
  ): Promise<T> {
    const { params, parse, headers: overrides, body, ...init } = options;
    const target = requestUrl(url, params);
    const headers = new Headers(defaults);
    new Headers(overrides).forEach((value, key) => headers.set(key, value));
    if (!headers.has("Accept")) headers.set("Accept", "application/json");
    let serialized: string | undefined;
    if (body !== undefined) {
      if (method === "GET") throw new TypeError("GET requests cannot contain a body");
      serialized = JSON.stringify(body);
      if (serialized === undefined) throw new TypeError("HTTP request body must be JSON-serializable");
      if (!headers.has("Content-Type")) headers.set("Content-Type", "application/json");
    }
    const response = await fetcher(target, {
      ...init, credentials: init.credentials ?? credentials, method, headers, body: serialized,
    });
    const text = await response.text();
    if (!response.ok) {
      let errorBody: unknown = text;
      if (text) {
        try { errorBody = JSON.parse(text); }
        catch (error) { if (!(error instanceof SyntaxError)) throw error; }
      }
      throw responseError(response, target, errorBody);
    }
    let value: unknown;
    if (response.status !== 204 && response.status !== 205) {
      try { value = JSON.parse(text); }
      catch (error) {
        if (!(error instanceof SyntaxError)) throw error;
        throw responseError(response, target, text, error);
      }
    }
    // A generic type is a caller assertion; parse can validate untrusted JSON at runtime.
    return parse ? parse(value) : value as T;
  }
  return {
    request,
    get: (url, options) => request("GET", url, options),
    post: (url, body, options) => request("POST", url, { ...options, body }),
    put: (url, body, options) => request("PUT", url, { ...options, body }),
    patch: (url, body, options) => request("PATCH", url, { ...options, body }),
    delete: (url, options) => request("DELETE", url, options),
  };
}

export interface HttpResourceRequest extends Omit<HttpRequestOptions, "parse" | "signal"> {
  readonly url: string;
}
export type HttpResourceStatus = "idle" | "loading" | "reloading" | "resolved" | "error";
export interface HttpResourceOptions<T> {
  readonly client?: HttpClient;
  readonly parse?: (value: unknown) => T | PromiseLike<T>;
  readonly scope?: Scope;
}
export interface HttpResource<T> {
  readonly value: Signal<T | undefined>;
  readonly error: Signal<unknown>;
  readonly status: Signal<HttpResourceStatus>;
  readonly isLoading: Signal<boolean>;
  readonly hasValue: Signal<boolean>;
  reload(): boolean;
  destroy(): void;
}
interface ResourceState<T> {
  status: HttpResourceStatus;
  value: T | undefined;
  error: unknown;
}

export function httpResource<T = unknown>(
  request: () => string | HttpResourceRequest | undefined,
  options: HttpResourceOptions<T> = {},
): HttpResource<T> {
  const scope = new Scope(options.scope);
  const client = options.client ?? createHttpClient();
  const parse = options.parse;
  const state = signal<ResourceState<T>>({ status: "idle", value: undefined, error: undefined });
  const revision = signal(0);
  const source = scope.run(() => computed(request));
  let previous: string | HttpResourceRequest | undefined;
  let lastRevision = 0;
  let reloadQueued = false;
  scope.add(() => {
    reloadQueued = false;
    state.set({ status: "idle", value: undefined, error: undefined });
  });

  // Defer the first read until component inputs have been installed and mount has finished.
  queueMicrotask(() => {
    if (scope.disposed) return;
    scope.run(() => effect(() => {
      const currentRevision = revision();
      let target: string | HttpResourceRequest | undefined;
      try { target = source(); }
      catch (error) {
        reloadQueued = false;
        lastRevision = currentRevision;
        state.set({ status: "error", value: undefined, error });
        return;
      }
      return untracked(() => {
        const current = state();
        const reloading = currentRevision !== lastRevision && target === previous && current.status === "resolved";
        lastRevision = currentRevision;
        reloadQueued = false;
        previous = target;
        if (target === undefined) {
          state.set({ status: "idle", value: undefined, error: undefined });
          return;
        }
        const controller = new AbortController();
        state.set({ status: reloading ? "reloading" : "loading", value: reloading ? current.value : undefined, error: undefined });
        const load = async () => {
          const { url, ...init } = typeof target === "string" ? { url: target } : target;
          return client.get<T>(url, { ...init, parse, signal: controller.signal });
        };
        void load().then(
          value => {
            if (!scope.disposed && !controller.signal.aborted) state.set({ status: "resolved", value, error: undefined });
          },
          error => {
            if (!scope.disposed && !controller.signal.aborted) state.set({ status: "error", value: undefined, error });
          },
        );
        return () => controller.abort();
      });
    }));
  });
  return {
    value: () => state().value,
    error: () => state().error,
    status: () => state().status,
    isLoading: () => state().status === "loading" || state().status === "reloading",
    hasValue: () => state().value !== undefined,
    reload(): boolean {
      return untracked(() => {
        const status = state().status;
        if (scope.disposed || reloadQueued || status === "idle" || status === "loading" || status === "reloading") return false;
        reloadQueued = true;
        revision.update(value => value + 1);
        return true;
      });
    },
    destroy: () => scope.dispose(),
  };
}
