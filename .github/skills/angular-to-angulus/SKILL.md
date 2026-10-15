---
name: angular-to-angulus
description: Migrate or rewrite an Angular application to Angulus. Use when replacing Angular components, templates, dependency injection, routing, forms, HTTP, lifecycle, or CLI tooling with Angulus while preserving application behavior and identifying unsupported features.
---

# Rewrite Angular to Angulus

Perform a working migration, not a search-and-replace of package names. Angulus
is an experimental SPA framework, not an Angular compatibility layer. Preserve
the application's behavior, accessibility, styling, URLs, and backend contracts.
Never silently remove a feature because Angulus does not implement it.

## Establish the source of truth

Read the target repository's instructions and inspect its working tree before
editing. Preserve unrelated changes. Use the APIs available in the selected
Angulus release, not assumptions based on similarly named Angular APIs.

In an Angulus source checkout, consult these files locally. Otherwise consult
the published package documentation and these upstream references, checking
that the selected release supports the features being used:

- [Framework guide](https://github.com/angulusjs/angulus/blob/main/README.md)
- [Core API](https://github.com/angulusjs/angulus/blob/main/packages/core/README.md)
- [Router API](https://github.com/angulusjs/angulus/blob/main/packages/router/README.md)
- [Forms API](https://github.com/angulusjs/angulus/blob/main/packages/forms/README.md)
- [HTTP API](https://github.com/angulusjs/angulus/blob/main/packages/http/README.md)
- [Demo bootstrap](https://github.com/angulusjs/angulus/blob/main/examples/demo/src/main.ts)
- [Demo TypeScript configuration](https://github.com/angulusjs/angulus/blob/main/examples/demo/tsconfig.json)

Upstream main can describe features not yet published. Do not invent missing
exports, compiler syntax, CLI flags, or an automatic migration command.

## 1. Inventory and agree on scope

Inspect `package.json`, the lockfile, `angular.json` or workspace/project
configuration, TypeScript configuration, bootstrap, routes, components,
templates, styles, services, tests, and deployment configuration.

Identify:

- Application entrypoints and workspace boundaries; shared or published libraries.
- Standalone components and NgModules, inputs/outputs, mutable view state,
  lifecycle hooks, queries, directives, pipes, and content projection.
- Providers and injection tokens, service lifetimes, RxJS streams, subscriptions,
  HTTP interceptors, authentication, and error handling.
- Nested routes, guards, redirects, resolvers, URL parameters, and base paths.
- Reactive/template-driven forms, custom controls, arrays, async validation,
  disabled state, and submission behavior.
- Angular-dependent UI libraries, SVG, animations, Sass, localization, SSR,
  hydration, service workers, assets, environment configuration, and proxy rules.

Capture existing behavior with the smallest relevant tests and browser flows.
Record baseline failures separately from migration regressions.

Classify features as directly portable, requiring explicit application code,
or blocked pending a design decision. Ask before changing user-visible behavior,
security requirements, service lifetimes, or architecture. If multiple apps
exist, establish which one is being migrated. Do not convert the Angulus
framework itself or unrelated applications.

Migrate one vertical slice first: bootstrap, one route or root component, a child
component, state, styling, and tests. Expand after that slice works. An incremental
migration does not imply Angular and Angulus can share a component tree; use
separate entrypoints unless interoperability has been explicitly designed.

## 2. Replace application tooling

Use the target application's package manager and update its lockfile. Install
`@angulus/core` and development dependency `@angulus/tooling`; add
`@angulus/router`, `@angulus/forms`, and `@angulus/http` only as needed. Select
matching versions from a release containing the required features.

Published tooling requires Node.js 22.12+ and optional platform dependencies
enabled; application developers do not need Go. Working from framework source
also requires the Go version specified by that checkout.

Create or adapt:

- A Vite-style `index.html` with an application host and a module script pointing
  to the TypeScript entrypoint.
- Strict TypeScript configuration using ES2022, ESNext modules, Bundler resolution,
  `experimentalDecorators`, and DOM libraries, following the demo. Preserve
  relevant application options and include every source component, including
  lazy routes. Replace Angular-only config inheritance rather than retaining a
  dependency on removed tooling.
- Scripts invoking `angulus serve`, `angulus check`, `angulus build`,
  `angulus preview`, and the application's test runner. Commands accept
  `--root <application-directory>` for a non-current application root.
- Optional `angulus.config.json` for port, host, `/api` proxy, and a test command
  expressed as an argument array. `angulus test` runs this configured command;
  it is not a built-in Angular TestBed replacement.
- Explicit imports for global CSS and required library styles, plus asset paths
  and environment handling appropriate to Vite. Never expose server secrets
  through client-side environment variables.

Install `@types/node` when the app requests Node types or uses Node test APIs,
and a test runner such as `tsx` only if the chosen test command needs it.
Do not copy the framework workspace's development dependencies wholesale.

## 3. Rewrite components and state

Use one named, exported component class per module and a named `Component`
import from `@angulus/core`. Metadata must be statically analyzable:
literal `selector`, external `templateUrl`, optional external `styleUrl`,
explicit named component imports in `imports`, and literal `customElements`
when needed. Extract inline Angular templates/styles into separate files.
Do not use default component exports, computed metadata, component barrel
re-exports, or namespace decorator imports.

Remove Angular-only metadata such as `standalone`, `providers`,
`changeDetection`, and `encapsulation` only after replacing its behavior where
necessary. NgModules and Angular directives do not belong in Angulus `imports`.

Map contracts deliberately:

- Mutable state rendered in templates becomes `signal(initial)`, read as
  `state()`, written with `.set()` or `.update()`. Publish new object/array
  references; in-place mutation does not notify subscribers.
- Derived state becomes `computed(() => ...)`. Ordinary fields remain useful
  for immutable data but are not reactive.
- Inputs become `input(defaultValue)` or `input.required<T>()`; outputs become
  `output<T>()` and `.emit(value)`. Update all callers and signal reads. Do not
  assume aliases, transforms, model inputs, or setters are supported.
- Event handlers are public typed methods. Keep mutations out of render
  expressions and account for microtask-batched DOM updates.
- Replace constructor injection and `inject()` with explicit application-owned
  dependencies or component inputs. Angulus mounts classes without Angular DI.
  Preserve per-component versus shared lifetimes; do not turn every service into
  a global singleton.
- Keep RxJS only where it remains useful and Angular-independent. Bridge stream
  values to signals with explicit error handling and lifecycle-owned
  unsubscription; do not assume an `async` pipe or Angular RxJS interop exists.

Example component:

```ts
import { Component, computed, input, output, signal } from "@angulus/core";

@Component({
  selector: "app-counter",
  templateUrl: "./counter.html",
  styleUrl: "./counter.css",
})
export class Counter {
  readonly label = input.required<string>();
  readonly count = signal(0);
  readonly doubled = computed(() => this.count() * 2);
  readonly changed = output<number>();

  increment(): void {
    this.count.update(value => value + 1);
    this.changed.emit(this.count());
  }
}
```

```html
<button (click)="increment()">{{ label() }}: {{ count() }}</button>
<p>Double: {{ doubled() }}</p>
```

The parent imports `Counter` directly, lists it in component `imports`, and
binds `[label]` and `(changed)` explicitly.

## 4. Translate templates and CSS

Retain supported interpolation, DOM property bindings, and event bindings,
updating expressions to read signals. Convert `*ngIf`, `*ngFor`, and Angular
switch directives to `@if`, `@for`, and `@switch`.

Use `@for (item of items(); track item.id)` with stable, unique keys; `$index`
is available. Do not use index keys for reorderable stateful rows. Duplicate
keys throw. Implement an explicit empty-state conditional rather than `@empty`.
Loop locals cannot use `__` prefixes or reserved names `ctx`, `host`, `scope`,
`$event`, and `$index`.

`[(value)]="name"` requires a writable string signal on an `input` with no
`type` or with literal `type="text"`. Do not add `()` to the binding target or
combine it with `[value]` or `(input)`. For checkboxes, selects, and numeric
inputs, use ordinary property/event bindings and typed methods that validate
the event target and convert its value.

Move pipes, unsupported globals, assignments, multi-statement handlers, arrow
functions, casts, `new`, spread, and other unsupported expression logic into
typed component methods or computed values. Public members are implicitly in
scope; do not use explicit `this` in template expressions.

Do not carry over `ngClass`, `ngStyle`, `[class.foo]`, `[style.foo]`, or
`[attr.foo]` blindly: replace them using documented DOM bindings or an explicit
design supported by the selected compiler. Attribute interpolation, template
references, projection, SVG/MathML, and dynamic input types are not implemented.
Angular libraries cannot become Angulus components merely by changing imports.
List actual web components in `customElements`; their event type is DOM `Event`,
not an inferred custom payload.

Raw HTML properties, inline `on*` attributes, script/style embedding, and dynamic
`srcset` are prohibited. Do not bypass these restrictions to reproduce Angular
HTML injection or sanitizer APIs.

Scoped CSS does not support nesting, `:host`, `:root`, `:global`, or `@import`.
Flatten or compile source styles to supported CSS and replace host selectors
with explicit markup/classes where appropriate. Use explicit global CSS imports
for genuinely global rules; do not make every component style global. Preserve
relative assets, responsive behavior, theme inheritance, and animation behavior.

## 5. Preserve lifecycle and resource ownership

Use construction for setup that does not read required inputs. Inputs are
available before rendering and mount hooks, not in constructors or eagerly
evaluated field initializers. Lazy computed reads can derive from inputs.

Use class `onMount()` / `onDestroy()` methods, or register core `onMount` /
`onDestroy` callbacks during construction. Mount hooks run after initial
rendering, children before parents; a mount hook can return cleanup.
Do not blindly rename `ngOnInit`, `ngOnChanges`, `ngAfterViewInit`, or queries:
their timing and semantics differ. Model changing inputs with reactive
derivations/effects where appropriate, and redesign DOM queries explicitly.

Return cleanup from effects and dispose subscriptions, timers, listeners, and
requests. Component-owned reactive resources are scope-managed, but resources
created outside those scopes need an explicit owner. Destruction must not assume
the DOM remains attached. Use `flushSync()` for deterministic reactive tests.

## 6. Port forms, HTTP, and routing

**Forms:** use `formControl`, `formGroup`, and `Validators` from
`@angulus/forms`, not Angular form directives. Bind text controls with
`[(value)]="form.controls.name.value"` and mark touch explicitly on blur or
submit. Handle native form submit with a typed method and `preventDefault()`.
Preserve error messages, validation, reset, and submission states. Programmatic
writes can mark controls dirty; reset baselines and validators differ from
Angular. Dynamic arrays, async validators, disabled-control exclusion, and
ControlValueAccessor have no automatic equivalent.

**HTTP:** use `createHttpClient()` for explicit Promise-based JSON requests and
`httpResource()` for reactive GET reads. Resources expose `value`, `status`,
`error`, `isLoading`, and `hasValue` signals; factories may return `undefined`
to pause. Superseded requests are aborted; component-owned resources dispose
automatically. Mutations stay in explicit action handlers, never reactive
factories, with application-owned loading/error state and refresh via `reload()`.
Preserve headers, credentials, CSRF protection, cancellation, response validation,
and error UX. Generics do not validate JSON; use `parse` where needed.
Interceptors, caching, uploads, streaming, and non-JSON responses require an
explicit alternative, not invented client options.

**Bootstrap and routing:** for an unrouted app, use `mount(Root, host)` from
`@angulus/core` on a compiled component. For routing, use
`createRouter({ outlet, routes, notFound })` and await `router.start()`.
The router exclusively owns its outlet; do not mount a root component into
the same element. Load named constructors with
`() => import("./home").then(module => module.Home)` and map path/query inputs
through `inputs: (params, url) => ({ ... })`.

Use `router.navigate()` and preserve base paths, queries, history, and deep
links. Replace `routerLink` with explicit navigation handling; ordinary anchors
are not intercepted. Preserve modified clicks, downloads, external URLs, and
non-self targets. Follow the demo's pagehide/HMR cleanup, retaining state during
persisted BFCache transitions.

Nested layouts, guards, redirects, resolvers, and route providers are not
drop-in APIs. Agree on a replacement rather than deleting them. Preserve
server-side authorization; navigation checks are not a security boundary.
Configure deployment to rewrite HTML navigation requests to `index.html`,
never missing assets or API requests.

## 7. Verify parity and finish

Run checks against the migrated application root, not just the framework demo:

```sh
npx angulus check --root <application-directory>
npx angulus build --root <application-directory>
```

Use the equivalent local CLI invocation for the application's package manager.
Run its configured unit/integration tests (`angulus test` when configured) and
browser flows. Replace Angular TestBed-specific setup with appropriate compiled
component/browser coverage; decorator-only uncompiled classes cannot be mounted.
Do not weaken strictness, exclude failing routes, or edit generated compiler
artifacts to make checks pass.

Verify initial rendering, input/output contracts, updates after async work,
list identity on reorder, form validation/reset/submission, failed requests,
navigation/back/forward/deep links, not-found behavior, keyboard interaction,
styling, and cleanup on route changes/unmount. Include loading and error states.
A listening development server alone is not evidence that checking succeeded.

Only after all in-scope functionality works, remove unused Angular packages,
Zone.js, obsolete bootstrap/build configuration, and replaced code. Keep
dependencies still required by unmigrated apps or libraries. Search the migrated
scope for remaining Angular imports and template constructs, inspect the diff,
and update application setup, testing, and deployment documentation.

Report what migrated, verification results, and any unresolved blockers or
intentional approved differences. Do not claim a complete rewrite while
unsupported features, placeholder implementations, or failing checks remain.
