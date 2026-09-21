export interface Signal<T> { (): T }
export interface WritableSignal<T> extends Signal<T> {
  set(value: T): void;
  update(update: (value: T) => T): void;
}
export interface Input<T> extends Signal<T> { readonly __input: true }
export interface RequiredInput<T> extends Input<T> { readonly __requiredInput: true }
export interface Output<T> {
  readonly __output: true;
  emit(value: T): void;
  subscribe(handler: (value: T) => void): () => void;
}
export type InputValue<I> = I extends Input<infer T> ? T : never;
export type OutputValue<O> = O extends Output<infer T> ? T : never;
export type Cleanup = () => void;

interface Source { observers: Set<Observer> }
interface Observer { sources: Set<Source>; notify(): void }
let observer: Observer | undefined;
let activeScope: Scope | undefined;
const pending = new Set<ReactiveEffect>();
let scheduled = false;
let flushing = false;

function collectErrors(actions: Iterable<Cleanup>): void {
  const errors: unknown[] = [];
  for (const action of actions) {
    try { action(); } catch (error) { errors.push(error); }
  }
  if (errors.length === 1) throw errors[0];
  if (errors.length) throw new AggregateError(errors, "Multiple Angulus cleanup/update errors");
}
function track(source: Source): void {
  if (observer) { source.observers.add(observer); observer.sources.add(source); }
}
function disconnect(target: Observer): void {
  for (const source of target.sources) source.observers.delete(target);
  target.sources.clear();
}
function notify(source: Source): void {
  for (const target of [...source.observers]) target.notify();
}
export function untracked<T>(fn: () => T): T {
  const previous = observer;
  observer = undefined;
  try { return fn(); } finally { observer = previous; }
}
export function signal<T>(initial: T): WritableSignal<T> {
  let value = initial;
  const source: Source = { observers: new Set() };
  const read = (() => { track(source); return value; }) as WritableSignal<T>;
  read.set = next => {
    if (Object.is(value, next)) return;
    value = next;
    notify(source);
  };
  read.update = update => read.set(update(value));
  return read;
}
export function computed<T>(calculate: () => T): Signal<T> {
  let dirty = true;
  let failed = false;
  let evaluating = false;
  let value: T;
  const node: Source & Observer = {
    observers: new Set(), sources: new Set(),
    notify() { if (!dirty || failed) { dirty = true; failed = false; notify(node); } },
  };
  const dispose = () => { disconnect(node); node.observers.clear(); dirty = true; };
  activeScope?.add(dispose);
  return () => {
    track(node);
    if (dirty) {
      if (evaluating) throw new Error("Circular computed dependency");
      const previous = observer;
      disconnect(node);
      observer = node;
      evaluating = true;
      try { value = calculate(); dirty = false; failed = false; }
      catch (error) { failed = true; throw error; }
      finally { observer = previous; evaluating = false; }
    }
    return value;
  };
}
class ReactiveEffect implements Observer {
  sources = new Set<Source>();
  disposed = false;
  cleanup: Cleanup | undefined;
  constructor(readonly fn: () => void | Cleanup, readonly scope?: Scope) {}
  notify(): void {
    if (this.disposed) return;
    pending.add(this);
    if (!scheduled) {
      scheduled = true;
      queueMicrotask(() => { scheduled = false; flushSync(); });
    }
  }
  run(): void {
    if (this.disposed || this.scope?.disposed) return;
    disconnect(this);
    const previous = observer;
    observer = this;
    try {
      const cleanup = this.cleanup;
      this.cleanup = undefined;
      if (cleanup) untracked(cleanup);
      const result = this.scope ? this.scope.run(this.fn) : this.fn();
      if (typeof result === "function") this.cleanup = result;
    } finally { observer = previous; }
  }
  dispose = (): void => {
    this.disposed = true;
    pending.delete(this);
    disconnect(this);
    const cleanup = this.cleanup;
    this.cleanup = undefined;
    if (cleanup) untracked(cleanup);
  };
}
export function effect(fn: () => void | Cleanup, scope = activeScope): Cleanup {
  const job = new ReactiveEffect(fn, scope);
  const unregister = scope?.add(job.dispose);
  try { job.run(); } catch (error) { job.dispose(); unregister?.(); throw error; }
  return () => { unregister?.(); job.dispose(); };
}
export function flushSync(fn?: () => void): void {
  fn?.();
  if (flushing) return;
  flushing = true;
  const errors: unknown[] = [];
  try {
    let rounds = 0;
    while (pending.size) {
      if (++rounds > 1000) {
        pending.clear();
        throw new Error("Angulus reactive update loop exceeded 1000 iterations");
      }
      const jobs = [...pending];
      pending.clear();
      for (const job of jobs) {
        try { job.run(); } catch (error) { errors.push(error); }
      }
    }
  } finally { flushing = false; }
  if (errors.length === 1) throw errors[0];
  if (errors.length) throw new AggregateError(errors, "Angulus reactive update errors");
}
export function batch<T>(fn: () => T): T { return fn(); }

export class Scope {
  disposed = false;
  private cleanups = new Set<Cleanup>();
  private children = new Set<Scope>();
  private mounts: Array<() => void | Cleanup> = [];
  private mounted = false;
  private unlink?: Cleanup;
  constructor(private parent = activeScope) {
    if (parent) {
      if (parent.disposed) throw new Error("Cannot create a child of a disposed Angulus scope");
      parent.children.add(this);
      const removeCleanup = parent.add(() => this.dispose());
      this.unlink = () => { removeCleanup(); parent.children.delete(this); };
    }
  }
  run<T>(fn: () => T): T {
    if (this.disposed) throw new Error("Cannot enter a disposed Angulus scope");
    const previous = activeScope;
    activeScope = this;
    try { return fn(); } finally { activeScope = previous; }
  }
  add(cleanup: Cleanup): Cleanup {
    if (this.disposed) { cleanup(); return () => {}; }
    this.cleanups.add(cleanup);
    return () => { this.cleanups.delete(cleanup); };
  }
  onMount(fn: () => void | Cleanup): void {
    if (this.disposed) throw new Error("Cannot register onMount on a disposed scope");
    if (this.mounted) {
      const cleanup = this.run(fn);
      if (cleanup) this.add(cleanup);
    } else this.mounts.push(fn);
  }
  mount(): void {
    if (this.mounted || this.disposed) return;
    this.mounted = true;
    for (const child of this.children) child.mount();
    const mounts = this.mounts.splice(0);
    this.run(() => { for (const fn of mounts) { const cleanup = fn(); if (cleanup) this.add(cleanup); } });
  }
  /** Used by DOM builders after inserting their range. */
  mountWhenReady(): void {
    if (!this.parent || this.parent.mounted) this.mount();
  }
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.unlink?.();
    this.mounts = [];
    const cleanups = [...this.cleanups].reverse();
    this.cleanups.clear();
    untracked(() => collectErrors(cleanups));
  }
}
export function createScope(parent?: Scope): Scope { return new Scope(parent); }
export function onMount(fn: () => void | Cleanup): void {
  if (!activeScope) throw new Error("onMount must run in a Angulus scope");
  activeScope.onMount(fn);
}
export function onDestroy(fn: Cleanup): void {
  if (!activeScope) throw new Error("onDestroy must run in a Angulus scope");
  activeScope.add(fn);
}

const unset = Symbol("unset input");
const inputs = new WeakMap<object, { set(value: unknown): void; required: boolean; read(): unknown }>();
function makeInput<T>(initial: T | typeof unset, required: boolean): Input<T> {
  const value = signal(initial);
  const read = (() => {
    const result = value();
    if (result === unset) throw new Error("Required input was read before it was set");
    return result;
  }) as Input<T>;
  Object.defineProperty(read, "__input", { value: true });
  if (required) Object.defineProperty(read, "__requiredInput", { value: true });
  inputs.set(read, { set: next => value.set(next as T), required, read: value });
  return read;
}
export const input: {
  <T>(value: T): Input<T>;
  required<T>(): RequiredInput<T>;
} = Object.assign(<T>(value: T) => makeInput(value, false), {
  required: <T>() => makeInput<T>(unset, true) as RequiredInput<T>,
});
export function output<T>(): Output<T> {
  const handlers = new Set<(value: T) => void>();
  activeScope?.add(() => handlers.clear());
  return {
    __output: true,
    emit(value) { collectErrors([...handlers].map(handler => () => handler(value))); },
    subscribe(handler) {
      handlers.add(handler);
      return () => { handlers.delete(handler); };
    },
  };
}
export type ComponentType<T = any> = new () => T;
export interface ComponentMetadata {
  selector: string;
  templateUrl: string;
  styleUrl?: string;
  imports?: readonly ComponentType[];
  customElements?: readonly string[];
}
/** Compile-time marker. The compiler attaches the executable definition separately. */
export function Component(_metadata: ComponentMetadata) {
  return <T extends ComponentType>(target: T, _context?: ClassDecoratorContext<T>): T => target;
}
export type Render<T> = (instance: T, parent: Node, scope: Scope) => void;
const definition = Symbol("Angulus component definition");
interface Definition<T> { render: Render<T>; scopeId: string }
export function defineComponent<T>(ctor: ComponentType<T>, render: Render<T>, scopeId = ""): ComponentType<T> {
  Object.defineProperty(ctor, definition, { value: { render, scopeId }, configurable: true });
  return ctor;
}
export interface ComponentRef<T> { instance: T; scope: Scope; destroy(): void }
export type InputValues<T> = { [K in keyof T as T[K] extends Input<any> ? K : never]?: InputValue<T[K]> };
export interface MountOptions<T> { inputs?: InputValues<T>; scope?: Scope }
function setInput(instance: any, name: string, value: unknown): void {
  const state = inputs.get(instance[name]);
  if (!state) throw new Error(`Unknown component input "${name}"`);
  state.set(value);
}
function instantiate<T>(ctor: ComponentType<T>, host: Node, options: MountOptions<T>, prepare?: (instance: T, scope: Scope) => void): ComponentRef<T> {
  const def = Object.hasOwn(ctor, definition) ? (ctor as any)[definition] as Definition<T> : undefined;
  if (!def) throw new Error(`${ctor.name} has not been compiled by Angulus`);
  const scope = new Scope(options.scope);
  const created: Node[] = [];
  let instance: T;
  try {
    instance = scope.run(() => new ctor());
    const hooks = instance as { onMount?: () => void | Cleanup; onDestroy?: () => void };
    if (typeof hooks.onDestroy === "function") scope.add(() => hooks.onDestroy!());
    for (const [name, value] of Object.entries(options.inputs ?? {})) setInput(instance, name, value);
    prepare?.(instance, scope);
    for (const name of Object.keys(instance as object)) {
      const value = (instance as any)[name];
      const state = (typeof value === "function" || typeof value === "object") && value !== null ? inputs.get(value) : undefined;
      if (state && state.required && state.read() === unset) throw new Error(`Missing required input "${name}" on ${ctor.name}`);
    }
    const fragment = host.ownerDocument!.createDocumentFragment();
    scope.run(() => untracked(() => def.render(instance, fragment, scope)));
    created.push(...fragment.childNodes);
    scope.add(() => { for (const node of created) node.parentNode?.removeChild(node); });
    host.appendChild(fragment);
    if (typeof hooks.onMount === "function") scope.onMount(() => hooks.onMount!());
    scope.mountWhenReady();
    return { instance, scope, destroy: () => scope.dispose() };
  } catch (error) {
    try { scope.dispose(); } catch (cleanupError) { throw new AggregateError([error, cleanupError], "Component mount and cleanup failed"); }
    throw error;
  }
}
export function mount<T>(ctor: ComponentType<T>, host: Element, options: MountOptions<T> = {}): ComponentRef<T> {
  const ref = instantiate(ctor, host, options);
  const window = host.ownerDocument.defaultView;
  if (window) {
    const onPageHide = (event: PageTransitionEvent) => { if (!event.persisted) ref.destroy(); };
    window.addEventListener("pagehide", onPageHide);
    ref.scope.add(() => window.removeEventListener("pagehide", onPageHide));
  }
  return ref;
}

export function element(_scope: Scope, parent: Node, tag: string, scopeId = ""): HTMLElement {
  const node = parent.ownerDocument!.createElement(tag);
  if (scopeId) node.setAttribute(scopeId.startsWith("data-") ? scopeId : `data-${scopeId}`, "");
  parent.appendChild(node);
  return node;
}
export function text(parent: Node, value: unknown): Text {
  const node = parent.ownerDocument!.createTextNode(value == null ? "" : String(value));
  parent.appendChild(node);
  return node;
}
export function bind(scope: Scope, fn: () => void | Cleanup): Cleanup { return effect(fn, scope); }
export function listen(scope: Scope, node: EventTarget, event: string, handler: (event: any) => void): void {
  const wrapped: EventListener = event => scope.run(() => untracked(() => handler(event)));
  node.addEventListener(event, wrapped);
  scope.add(() => node.removeEventListener(event, wrapped));
}
const urlProperties = new Set(["href", "src", "action", "formaction", "poster", "cite", "background", "xlink:href"]);
function validateProperty(node: any, name: string, value: unknown): void {
  const property = (name.startsWith("attr.") ? name.slice(5) : name).toLowerCase();
  if (property === "innerhtml" || property === "outerhtml" || property === "srcdoc" || property.startsWith("on")) {
    throw new Error(`Unsafe DOM property binding "${name}" is not supported`);
  }
  if (value == null || value === false || node.nodeType !== 1) return;
  if (urlProperties.has(property) || property === "data" && node.tagName === "OBJECT") {
    const normalized = String(value).replace(/[\u0000-\u0020\u007f]/g, "");
    const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(normalized)?.[1]?.toLowerCase();
    if (scheme && !["http", "https", "mailto", "tel", "sms", "ftp", "blob"].includes(scheme)) {
      throw new Error(`Unsafe URL scheme "${scheme}:" in DOM binding "${name}"`);
    }
  }
  if (property === "srcset" || property === "imagesrcset") {
    throw new Error(`Dynamic "${name}" bindings are not supported; bind an individual src URL instead`);
  }
}
export function setProperty(node: any, name: string, value: unknown): void {
  validateProperty(node, name, value);
  if (name.startsWith("attr.")) {
    const attr = name.slice(5);
    if (value == null || value === false) node.removeAttribute(attr);
    else node.setAttribute(attr, String(value));
  } else if (name === "class") node.className = value ?? "";
  else node[name] = value;
}
type BlockRender = (parent: Node, scope: Scope) => void;
interface Region { start: Comment; end: Comment; scope: Scope }
function anchor(parent: Node, label: string): Comment {
  const node = parent.ownerDocument!.createComment(label);
  parent.appendChild(node);
  return node;
}
function removeRange(start: Node, end: Node): void {
  let node: Node | null = start;
  while (node) {
    const next: Node | null = node.nextSibling;
    node.parentNode?.removeChild(node);
    if (node === end) break;
    node = next;
  }
}
function region(parentScope: Scope, before: Node, render: BlockRender): Region {
  const scope = new Scope(parentScope);
  const fragment = before.ownerDocument!.createDocumentFragment();
  const start = anchor(fragment, "angulus:start");
  const end = fragment.ownerDocument.createComment("angulus:end");
  scope.add(() => removeRange(start, end));
  try {
    scope.run(() => untracked(() => render(fragment, scope)));
    fragment.appendChild(end);
    before.parentNode!.insertBefore(fragment, before);
    scope.mountWhenReady();
    return { start, end, scope };
  } catch (error) {
    try { scope.dispose(); } catch (cleanupError) { throw new AggregateError([error, cleanupError]); }
    throw error;
  }
}
export function ifBlock(scope: Scope, parent: Node, test: () => unknown, thenRender: BlockRender, elseRender?: BlockRender): void {
  const end = anchor(parent, "angulus:if");
  let branch: Region | undefined;
  let previous: boolean | undefined;
  scope.add(() => end.remove());
  bind(scope, () => {
    const next = Boolean(test());
    if (next === previous) return;
    branch?.scope.dispose();
    branch = undefined;
    previous = undefined;
    const render = next ? thenRender : elseRender;
    if (render) branch = region(scope, end, render);
    previous = next;
  });
}
export function switchBlock(scope: Scope, parent: Node, value: () => unknown, cases: Array<{ test?: () => unknown; render: BlockRender }>): void {
  const end = anchor(parent, "angulus:switch");
  let previous = -2;
  let branch: Region | undefined;
  scope.add(() => end.remove());
  bind(scope, () => {
    const current = value();
    let next = cases.findIndex(entry => entry.test && current === entry.test());
    if (next < 0) next = cases.findIndex(entry => !entry.test);
    if (next === previous) return;
    branch?.scope.dispose();
    branch = undefined;
    previous = -2;
    if (next >= 0) branch = region(scope, end, cases[next]!.render);
    previous = next;
  });
}
export function forBlock<T>(scope: Scope, parent: Node, items: () => Iterable<T>, key: (item: T, index: number) => unknown, render: (parent: Node, scope: Scope, item: Signal<T>, index: Signal<number>) => void): void {
  const end = anchor(parent, "angulus:for");
  type Row = Region & { item: WritableSignal<T>; index: WritableSignal<number> };
  let rows = new Map<unknown, Row>();
  scope.add(() => { rows.clear(); end.remove(); });
  bind(scope, () => {
    const values = Array.from(items());
    const keys = values.map(key);
    const unique = new Set<unknown>();
    for (const value of keys) {
      if (unique.has(value)) throw new Error(`Duplicate @for key: ${String(value)}`);
      unique.add(value);
    }
    const next = new Map<unknown, Row>();
    const added: Row[] = [];
    try {
      values.forEach((value, index) => {
        const id = keys[index];
        let row = rows.get(id);
        if (!row) {
          const item = signal(value);
          const position = signal(index);
          row = { ...region(scope, end, (parent, child) => render(parent, child, item, position)), item, index: position };
          added.push(row);
        }
        next.set(id, row);
      });
    } catch (error) {
      collectErrors(added.map(row => () => row.scope.dispose()));
      throw error;
    }
    const removed = [...rows].filter(([id]) => !next.has(id)).map(([, row]) => () => row.scope.dispose());
    rows = next;
    values.forEach((value, index) => {
      const row = rows.get(keys[index])!;
      row.item.set(value);
      row.index.set(index);
      const fragment = end.ownerDocument.createDocumentFragment();
      let node: Node | null = row.start;
      while (node) {
        const nextNode: Node | null = node.nextSibling;
        fragment.appendChild(node);
        if (node === row.end) break;
        node = nextNode;
      }
      end.parentNode!.insertBefore(fragment, end);
    });
    collectErrors(removed);
  });
}
export function mountChild<T>(scope: Scope, parent: Node, ctor: ComponentType<T>, values: Record<string, () => unknown> = {}, outputs: Record<string, (value: any) => void> = {}): ComponentRef<T> {
  return instantiate(ctor, parent, { scope }, (instance, childScope) => {
    for (const [name, get] of Object.entries(values)) bind(childScope, () => setInput(instance, name, get()));
    for (const [name, handler] of Object.entries(outputs)) {
      const emitter = (instance as any)[name] as Output<unknown> | undefined;
      if (!emitter?.__output) throw new Error(`Unknown component output "${name}"`);
      childScope.add(emitter.subscribe(value => scope.run(() => untracked(() => handler(value)))));
    }
  });
}
