# Angulus

A working, experimental SPA framework: TypeScript classes, external HTML/CSS,
a Go template compiler, fine-grained reactive DOM updates, and Vite.
This repository includes distributable npm packages and a GitHub Releases
publishing workflow. The framework is experimental, not production-certified.

## Quick start from source

Requirements: Node.js 22.12+ (tested on 22.22), npm, and Go 1.26+.
The pinned native TypeScript package supplies platform-specific binaries for
macOS arm64/x64, Linux arm/arm64/x64, and Windows arm64/x64.
The complete browser workflow is tested on macOS arm64.

```sh
npm ci
npm run dev
```

Installation builds the Go compiler. The single development command starts the
demo at `http://localhost:5173`. No hand-written Vite configuration is needed.
Go must remain available when rebuilding the compiler from source. Installation
also builds the core/router JavaScript and TypeScript declarations.

```sh
npm run check
npm run build
npm run preview
npm exec angulus -- test --root examples/demo
```

### Using npm releases

Once a release is published, an application can install:

```sh
npm install @angulus/core @angulus/router
npm install --save-dev @angulus/tooling
npx angulus serve
```

The published runtime packages contain JavaScript and type declarations, not
TypeScript-only entrypoints. The CLI installs a matching prebuilt Go compiler
through platform-specific optional npm dependencies; **application developers
do not need Go**. Keep npm optional dependencies enabled.

If your application's `tsconfig.json` includes `"types": ["node"]` (as the demo
does), or its tests import `node:test` / `node:assert`, install Node.js types in
the application too:

```sh
npm install --save-dev @types/node@22
```

Angulus preserves the application's TypeScript configuration; it does not
provide ambient Node.js types automatically.

For maintainers, see [npm release setup and procedure](docs/releases.md),
including the initial manual bootstrap and per-package Trusted Publisher
configuration. No packages are published by `npm ci`, builds or pull-request CI.

### Publishing your own components

Use `angulus build --lib` to build a component library into a publishable `dist`
package containing ESM, declarations, selector metadata, and scoped CSS.
Applications import named components from that package in `@Component.imports`
and explicitly import its `style.css`. The same builder is available as
`buildLibrary()` from `@angulus/tooling/library`.

See [component library setup, publication, and consumption](docs/libraries.md).
This requires a tooling release containing library support (not tooling `0.1.1`).

Preview serves the built application locally; it is not a production server.
For deployment, configure the host to rewrite **HTML navigation requests** to
`index.html`, but never missing assets or `/api` requests.

## Components

```ts
import { Component, computed, signal } from "@angulus/core";

@Component({
  selector: "app-counter",
  templateUrl: "./counter.html",
  styleUrl: "./counter.css",
})
export class Counter {
  readonly count = signal(0);
  readonly enough = computed(() => this.count() >= 10);
  increment(): void { this.count.update(n => n + 1); }
}
```

```html
<button (click)="increment()">Count: {{ count() }}</button>
@if (enough()) { <p>Reached ten.</p> } @else { <p>Keep going.</p> }
```

Each mount constructs a new class instance. Signals declared in fields are not
shared. Public members are implicitly in template scope; ordinary fields do not
become reactive. Event method calls retain the component's `this`.

Metadata is never executed by the compiler. The first version accepts one named,
exported component class per module, a named `Component` import from
`@angulus/core` (aliases are allowed), literal `selector`, `templateUrl` and optional
`styleUrl`, an `imports` array of named component imports, and a literal
`customElements` array. Template and stylesheet paths are relative to the class
file. Default component exports, computed metadata, barrel component re-exports,
and namespace decorator imports are not supported.

```ts
import { Component, input, output } from "@angulus/core";

@Component({ selector: "product-card", templateUrl: "./card.html" })
export class ProductCard {
  readonly product = input.required<{ id: string; name: string }>();
  readonly selected = output<string>();
  select(): void { this.selected.emit(this.product().id); }
}
```

The parent imports `ProductCard`, lists it in `@Component({ imports: [ProductCard],
... })`, and uses:

```html
<product-card [product]="product()" (selected)="select($event)" />
```

Required inputs, input value types, output payloads, and public member visibility
are checked using native TypeScript. Required inputs are installed before the
first render and mount hook; reading an unset required input throws. Outputs are
explicit subscriptions, not bubbling DOM events. There is no global selector
registry or content projection. Unknown tags are errors; real web components
must be listed explicitly in `customElements`. Their own property/event schemas
are not inferred: custom-element events use the DOM `Event` type.

## Templates

| Syntax | Meaning |
| --- | --- |
| `{{ count() }}` | Reactive text node, never raw HTML |
| `[disabled]="busy()"` | DOM property or component input |
| `(click)="save($event)"` | DOM event or component output |
| `[(value)]="name"` | Writable string signal on a text `input` |
| `@if (condition) { ... } @else { ... }` | Scoped conditional branch |
| `@for (item of items(); track item.id) { ... }` | Keyed list; `$index` is available |
| `@switch (kind()) { @case ("a") { ... } @default { ... } }` | Scoped switch |

List reordering preserves matching DOM nodes and component instances. Item and
index values are updated independently. Removed entries are disposed. Duplicate
keys throw explicitly rather than silently reusing the wrong state.
Loop variable names beginning with `__`, and `ctx`, `host`, `scope`, `$event`,
and `$index`, are reserved for compiler/runtime bindings.

Expressions support identifiers, literals, property/index access, optional
chaining, calls, arrays and non-computed object literals, parentheses, arithmetic,
comparisons, `in`, boolean/nullish operators, `typeof`, and ternaries. Free names
refer to public component members, except loop locals, event `$event`, and
`undefined`/`NaN`/`Infinity`. `this` is implicit, not expression syntax.
Assignments, multiple statements, comma expressions, assertions, `new`,
arrow functions, spread, `await`, and arbitrary globals are rejected. Put complex
logic in a public typed method. Calls can have side effects; use event handlers
for mutations, not rendering expressions.

Attribute interpolation, SVG/MathML, projection, `@empty`, template variables,
checkbox/select two-way binding, and dynamic input types are not implemented.
Text two-way binding cannot also specify `[value]` or `(input)`.
Unsafe HTML properties, inline `on*` attributes, and script/style embedding are
rejected. Load global CSS explicitly with a TypeScript CSS import.
Dynamic URL properties validate supported schemes at runtime; `javascript:`,
`data:`, and similar executable/unsupported schemes are rejected. Dynamic
`srcset`/`imageSrcset` lists are not supported and fail template checking.

### Local CSS

PostCSS and its selector/value parsers add a component-specific attribute to
selectors and generated elements. Parent rules do not style the child's internal
elements. Animation names/keyframes are namespaced; at-rules and pseudo-elements
remain structured. Relative asset URLs resolve from the original CSS file.
Custom properties and inherited CSS properties still inherit normally.

CSS nesting, `:host`, `:root`, `:global`, and `@import` are rejected in scoped
styles. Global styles are ordinary explicit imports, as in the demo. This is
attribute scoping, not Shadow DOM or a security boundary.
If a keyframe name is also an animation shorthand keyword (such as `ease`),
use explicit `animation-name` or choose a non-keyword name.

## Reactivity and lifecycle

Signals are callable getters with `set` and `update`. Computed values cache and
track their current dynamic dependencies. Effects run initially and are scheduled
after signal writes; updates are batched in a microtask. `flushSync()` is available
for deterministic tests. Mutating an object in place does not notify subscribers:
publish a new value through the signal.

Every component, conditional branch, and keyed item owns a disposal scope.
Reactive bindings, event handlers, and output subscriptions belong to that scope.
Changing branches, removing items, unmounting components, or replacing routes
releases those resources. Computed reads remain synchronous while DOM work is
batched. Mount hooks run after initial rendering. Destruction runs cleanup and
removes the mounted tree; it is not a subset of Angular's full lifecycle.

Register `onMount(() => cleanup)` and `onDestroy(cleanup)` during construction,
or implement class methods `onMount()` and `onDestroy()`. Required inputs cannot
be read in constructors/field initializers; they are available during rendering
and mount hooks. Children mount before parent hooks. Cleanup runs in reverse
registration order, and all cleanups are attempted even if one throws.
See [the complete core API](packages/core/README.md).

## Router and bootstrap

```ts
import { createRouter } from "@angulus/router";

const router = createRouter({
  outlet: document.querySelector<HTMLElement>("#app")!,
  routes: [
    { path: "/", load: () => import("./home").then(m => m.Home) },
    {
      path: "/products/:id",
      load: () => import("./products/product").then(m => m.Product),
      inputs: params => ({ id: params.id }),
    },
  ],
  notFound: () => import("./not-found").then(m => m.NotFound),
});
await router.start();
await router.navigate("/products/42");
// Call router.destroy() when the application owner is disposed.
```

Routes support explicit parameters, lazy imports, history navigation, back/forward,
and not-found handling. Replacing a route destroys its old component tree.
Stale lazy imports cannot overwrite a newer navigation. Load errors display an
alert and a Retry button. There are no client guards pretending to provide
server authorization.

Anchors are not intercepted globally: the demo's bootstrap explicitly handles
`a[data-link]`, preserving modified clicks and external navigation.
Bootstrap also owns `pagehide`/HMR disposal, retaining scopes on persisted
BFCache transitions. See [router contracts](packages/router/README.md) and
[the working bootstrap](examples/demo/src/main.ts).

## CLI

All commands accept `--root <application-directory>` (default: current directory).

```sh
angulus serve
angulus check
angulus check --json
angulus build
angulus preview
angulus test
angulus generate component profile
```

`serve` uses Vite, local-only binding by default, and a strict port: an occupied
port produces an error instead of silently selecting another port.
Startup failures exit nonzero after pending compiler startup and shutdown finish;
no compiler or file watcher is intentionally left running after a failed bind.
`build` must pass the full template and TypeScript check before Vite runs.
`test` executes the application's configured command, not the framework suite.
Generation creates TS/HTML/CSS and a runnable test; existing files are protected
unless `--force` is explicitly provided.
Names are kebab-case without path separators; `profile` creates
`src/profile/profile.{ts,html,css}` and `src/profile/profile.test.ts`.

Optional `angulus.config.json`:

```json
{
  "port": 5173,
  "host": "localhost",
  "proxy": { "/api": "http://localhost:3000" },
  "test": ["node", "--import", "tsx", "--test", "src/counter/counter.test.ts"]
}
```

The test command is an argument array, spawned without a shell. For advanced Vite
integration, the plugin is exported by `@angulus/tooling/vite`.

### Diagnostics and JSON

Each diagnostic contains:

```json
{
  "file": "/absolute/path/counter.html",
  "start": 10,
  "end": 11,
  "line": 1,
  "column": 11,
  "code": "TS2339",
  "message": "Property 'missing' does not exist on type 'Counter'.",
  "severity": "error"
}
```

Offsets are zero-based UTF-16 offsets; lines/columns are one-based. Template type
diagnostics are translated from generated verification files back to HTML.
Malformed metadata points to TypeScript; CSS errors point to CSS. Full checking
includes unopened lazy components selected by the application's `tsconfig.json`.
Do not exclude source features from that configuration.

`serve --json` emits newline-delimited events with protocol version and revision
IDs. HTTP readiness is a separate event from completion of a successful check:
a listening server can still have invalid source code. Diagnostics from a
previous revision must not be interpreted as approval of a newer revision.

```json
{"version":1,"type":"listening","revision":0,"url":"http://localhost:5173/","compilerPid":12345}
{"version":1,"type":"checking","revision":1}
{"version":1,"type":"checked","revision":1,"diagnostics":[],"valid":true,"stale":false}
```

`check --json` emits the `checked` envelope with the same diagnostic format and
returns a nonzero exit code on errors. `valid` describes that revision only;
`stale: true` means a newer revision is already pending. Startup/tool failures
emit `type: "error"` with a message and return nonzero.

### HMR

CSS changes use Vite CSS HMR and retain component state. HTML or TypeScript changes
use explicit full-page reloads: preserving state across structural or class
changes is not promised. The old document and its resources are discarded.
Errors appear in the terminal/JSON stream and Vite browser overlay. Vite watches
external HTML/CSS dependencies, including components not yet requested by a
browser. SIGINT/SIGTERM and server shutdown stop the Go subprocess.

## Compiler architecture

1. The JS TypeScript 6 parser reads static metadata without evaluating source.
2. A long-lived Go process parses nested HTML/control flow into a positioned AST.
3. A restricted expression frontend qualifies class names and loop locals;
   semantic analysis resolves only explicitly imported component selectors.
4. Go generates imperative DOM operations against the core runtime. Vite handles
   TypeScript lowering, the module graph, CSS delivery, chunks, and production
   bundling. No HTML parser/compiler is shipped to browsers.
5. Verification TypeScript preserves lexical loop scopes, control-flow narrowing,
   DOM event types, and component input/output contracts.
6. A separate pinned native TypeScript executable checks original source and
   verification modules. Diagnostics are remapped to source coordinates.

The Go transport is NDJSON over stdin/stdout with version `1`, request IDs,
`parse`/`generate`/`cancel`/`shutdown` operations, structured results, cancellation,
and explicit crash reporting. Logs use stderr. It is reused across transformations;
there is no compiler process per changed file.

The adapter uses `@typescript/native-preview@7.0.0-dev.20260707.2` and its actual
`tsgo` executable. The package has unstable APIs; this implementation deliberately
does not depend on them or import internal Go packages. The pinned CLI supports
watch/incremental builds. Angulus uses serialized incremental checks with a
`.tsbuildinfo` file, not the watch protocol. Diagnostics are parsed from pinned,
non-colored English CLI output because no stable diagnostic API is assumed.
This is real type checking, not transpilation. TypeScript 6 syntax parsing and
native TypeScript 7 checking are separate roles.

Generated source maps include original TypeScript/HTML; CSS maps retain the
original stylesheet. Template runtime mappings identify template operations;
type-diagnostic mappings additionally track expression character offsets.
Generated checking files and the compiler binary live under ignored `.angulus`
directories.

## Reproducible validation

```sh
npm ci
npm run compiler
npm test
npm run typecheck
npm run check
npm run build
npx playwright install chromium
npm run test:browser
npm run size
npm run packages:pack
npm run packages:smoke
```

Dependencies and native platform packages are pinned by `package-lock.json`.
Go uses the standard library and needs no `go.sum`.
The browser test uses an isolated copy of the demo, edits CSS/HTML, checks actual
DOM/state, exercises lazy navigation and history, verifies check/build failures,
builds and previews the SPA, and checks compiler termination.

### GitHub Actions

[Angulus CI](.github/workflows/ci.yml) runs on pushes, pull requests, and manual
dispatches. Linux and macOS jobs install Node.js 22 and the Go version from
`go.mod`, then run `npm ci` (including the Go compiler build), formatting,
`go vet`, race tests, framework tests, native type checking, full template checks,
application tests, a production build, and runtime size measurement.
The Linux job also packs all npm artifacts and tests an isolated application
installation with its own dependencies, rejecting workspace symlinks and
dependency resolution outside that installation.
The Linux job also installs Chromium/system dependencies and runs the complete
browser workflow. Failed browser runs upload traces and screenshots for seven
days. No repository secrets or publishing credentials are required.
The actions themselves use Node.js 24; application commands still use Node.js 22.

`npm run size` reports real minified and gzip sizes of **all core exports**,
excluding router, application, sourcemaps, and development tools. The gzip target
is 10 KiB; the script reports whether that budget is met rather than hiding it.
The measured all-exports core bundle in this workspace is **11,424 bytes
minified / 3,907 bytes gzip** (Vite 8.3, ES2022 target, gzip level 9).

## Scope and next steps

This is a deliberately small first version, not an Angular compatibility layer.
There is no virtual DOM, runtime template compilation, reflection, global change
detection, compulsory global store, SSR, or hydration. Packages are consumed from
either this source workspace or built npm artifacts with precompiled platform
compilers. Registry publication requires the maintainer setup described above.

The next stages are nested layouts; route/application providers; forms and
validation; server-state/query caching; architectural-boundary checks;
multi-application workspaces; `inspect --json` and `check --affected`; an LSP;
version migrations; and localization. None has a placeholder public API here.
