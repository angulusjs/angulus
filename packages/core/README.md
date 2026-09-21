# @angulus/core

Components use `@Component({selector, templateUrl, styleUrl?, imports?, customElements?})`.
The decorator is a no-op compile-time marker; mounting an uncompiled class throws.
The compiler attaches a render function with `defineComponent(Class, (ctx, parent, scope) => {}, scopeId)`.
There is no component registry, runtime template parser, or reflection.

`mount(Class, host, {inputs: {name: value}})` returns `{instance, scope, destroy()}`.
Each mount constructs a new instance. Inputs are callable read-only `Input<T>` values;
`input(default)` is optional and `input.required<T>()` has a `RequiredInput<T>` type.
Missing required values fail mount. They are available before the first render and
mount hooks, **not in the constructor or field initializers**. `output<T>()` exposes
`emit(value)` and `subscribe(handler)` (which returns an unsubscribe function).
Use the compiler, not `defineComponent`, in ordinary application code.
Public root mounts dispose automatically on non-persisted `pagehide` (including
full-page HMR reloads). Persisted BFCache transitions retain their state. Explicit
`destroy()` removes that listener and remains safe to call repeatedly. Application
bootstrap must still dispose its router and any resources it owns outside mounts.

Checker integrations can extract contracts with `InputValue<typeof instance.name>`
and `OutputValue<typeof instance.changed>`. A generated helper should prevent the
value argument from widening the inferred input type:
`function checkInput<I extends Input<unknown>>(input: I, value: InputValue<NoInfer<I>>): void {}`.
`Input` carries `__input: true`; required inputs also carry `__requiredInput: true`.

`signal(value)` exposes a callable reader, `.set(value)`, and `.update(fn)`.
`computed(fn)` is lazy and tracks dynamic dependencies. Ordinary fields do not
become reactive. `effect(fn)` runs immediately, then once per microtask batch;
return a cleanup function to run before reruns and disposal. Effects created in
a component scope are automatically disposed. Standalone effects return a disposer.
`flushSync(fn?)` drains queued effects synchronously. `batch(fn)` documents a batch:
all synchronous writes already share the microtask batch. `untracked(fn)` suppresses
dependency collection. Errors propagate; asynchronous update errors are uncaught
microtask errors rather than silent failures.

Register `onMount(() => cleanup?)` and `onDestroy(cleanup)` during construction,
render, or another active scope. Alternatively implement class methods
`onMount(): void | (() => void)` and `onDestroy(): void`.
Order: constructor, inputs and output connections, render, registered mount hooks,
class mount hook. A child finishes mounting before its parent's mount hooks.
Destroy disposes owned scopes, effects, listeners and rendered nodes; cleanup runs
in reverse registration order. Every cleanup is attempted even if another throws.
Do not assume DOM remains attached in destroy hooks. DOM creation in a compiled
render is staged in a fragment; mount hooks run after the completed tree has
been inserted into the host.

For advanced integration, `new Scope(parent?)` (or `createScope`) provides
`run(fn)`, `add(cleanup)`, `mount()`, and idempotent `dispose()`.
`effect(fn, scope)` explicitly owns an effect. A new scope defaults to the active
scope. The DOM helpers (`element`, `text`, `bind`, `listen`, `setProperty`,
`ifBlock`, `forBlock`, `switchBlock`, `mountChild`) are compiler-facing contracts.
Blocks own anchored DOM ranges. Keyed rows preserve node/component identity on
reorder, update item/index signals, and reject duplicate keys before changing rows.
`mountChild` renders into the supplied parent and connects reactive input getters
and output handlers. Only HTML elements are currently supported (no SVG namespace API).

Dynamic URL bindings allow relative URLs and `http:`, `https:`, `mailto:`, `tel:`,
`sms:`, `ftp:`, and `blob:` schemes. Other explicit schemes, including `javascript:`
and `data:`, throw visible errors. Dynamic `srcset`/`imagesrcset` bindings are not
supported; use `src`. Raw HTML (`innerHTML`, `outerHTML`, `srcdoc`) and `on*`
property/attribute bindings are prohibited; use template event bindings instead.
These restrictions are not a substitute for application URL authorization or CSP.
