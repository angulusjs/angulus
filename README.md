# Angulus

Angulus is an experimental TypeScript component runtime with external HTML
templates and a Go-based project CLI. Use the descriptor model below and treat
the explicit limitations as part of the supported contract. This is not
Angular-compatible tooling.

## Toolchain and installation

Applications require Node.js 22.12+ and npm. Install the runtime packages and
the CLI:

```sh
npm install @angulus/core
npm install --save-dev @angulus/tooling
npx angulus check
npx angulus build
```

The published tooling package is a small Node launcher. It selects and runs a
matching prebuilt Go CLI from an npm optional platform package; it does not
contain the compiler or a JavaScript build pipeline. Keep optional dependencies
enabled. Supported CLI targets are macOS x64/arm64, Linux x64/arm64/arm, and
Windows x64/arm64. Go is not required to use a published CLI package.

The CLI discovers `*.component.json` descriptors below `--root` (default `.`).
It checks descriptor/template syntax, invokes native TSGo as a subprocess for
TypeScript checking, and uses esbuild's public Go API to emit browser ESM for
application builds. It does not run Vite. A source checkout requires Go 1.26+
and the pinned native TypeScript package.

## Component descriptors

Describe each component in JSON; component TypeScript remains ordinary source
and is not decorated or rewritten:

```json
{
  "selector": "app-counter",
  "module": "./counter.ts",
  "export": "Counter",
  "template": "./counter.html",
  "imports": [],
  "customElements": []
}
```

Paths are relative to the descriptor. `selector`, `module`, `export`, and
`template` are required. `styles` is an array of stylesheet paths,
`imports` accepts source descriptor paths and installed library references
(`{ "module": "@example/widgets/components/ui-badge.js", "export": "Badge" }`).
`customElements` explicitly allows otherwise unknown custom element names.
For editor completion, optionally set `$schema` to the installed
`@angulus/tooling/component.schema.json` using a path relative to the descriptor.

```ts
import { signal } from "@angulus/core";

export class Counter {
  readonly count = signal(0);
  increment(): void {
    this.count.update(value => value + 1);
  }
}
```

```html
<button (click)="increment()">Count: {{ count() }}</button>
```

The Go compiler generates separate facade and render modules without modifying
the user-authored class. The first supported template surface is intentionally
limited; do not infer Angular decorator metadata or behavior from a class.

## Current limitations

- `angulus build --lib` emits ESM, native TSGo declarations and versioned
  component metadata. See [component libraries](docs/libraries.md).
- Generated TSGo contracts check member visibility, required/typed inputs,
  output payloads, DOM properties/events, two-way bindings and template
  control-flow narrowing. Expressions use a restricted parsed grammar, not
  full TypeScript: assignments, arrow functions, `this`, assertions, and
  statements are rejected. Expression diagnostics identify original HTML
  tokens; structural binding errors identify their binding or element span.
  Private/protected input and output declarations fail even when unused.
- Component `styles` are parsed and scoped to the component's generated
  `data-a-*` attribute. Keyframes are renamed consistently across `styles[]`,
  relative `url()` assets are embedded, and unsupported `@import`, nesting,
  `:host`, `:global`, or `:root` constructs produce diagnostics. Current builds
  emit only reachable component styles; styles for lazy components are loaded
  when their component module is evaluated, not bundled into the eager page.
- Providers/DI are not available: `providers` descriptor fields fail explicitly.
  A library uses one configured public entrypoint; multiple named entrypoints
  are not supported. Source maps compose generated render/scoped styles back
  to original TypeScript, HTML, and CSS.
- `angulus test` runs configured test argv or falls back to `npm test`; it does
  not discover tests itself.
- `serve` polls source files and rebuilds. CSS-only edits update linked styles
  without recreating component instances; TS/HTML edits reload the page.
  Compilation errors appear in the terminal and development browser overlay.
  esbuild contexts and native TSGo build-information caches are retained for
  incremental rebuilds. Invalid initial source shows a recoverable error page;
  correcting it does not require restarting the server. This is not
  Vite-compatible HMR.

Commands are `check`, `build`, `serve`, `preview`, and `test`; each accepts
`--root`. `build` and `preview` also accept `--out`. `serve` accepts `--host`
and `--port`. `--json` emits version-1 revisioned NDJSON for automation.
`angulus generate component <name>` creates ordinary TS/JSON/HTML/CSS/test files.
`angulus.config.json` supports `host`, `port`, development `proxy`, test argv,
and `library.entry`. Build output is ESM; preview serves an existing output directory
and is not a production server.

## Development and validation

```sh
npm ci
npm test
npm run typecheck
npm run test:browser
npm run packages:pack
npm run packages:smoke
```

`npm ci` builds the local Go CLI and the runtime packages. The npm package
smoke test installs the tarballs into an isolated project, verifies the native
CLI package is invoked without Go, and exercises descriptor check/build.
GitHub Actions validates Go, TypeScript, browser runtime, package and
isolated-install paths. `examples/demo` is migrated to descriptors and is the
end-to-end CLI example.

The supported browser runtime packages are `@angulus/core`, `@angulus/router`,
`@angulus/forms`, and `@angulus/http`. See their package READMEs for APIs.
Release and OIDC procedures are documented in [docs/releases.md](docs/releases.md).
Source builds, packaging, size measurement, versioning and release preflight
are implemented in Go (`cmd/angulus-release`); Node remains only the npm binary
launcher, native TSGo launcher, and runtime/test tooling.

From source, start the migrated demo with `npm run dev`; direct Windows usage
is `.angulus\bin\angulus.exe serve --root examples\demo`. Runtime JavaScript is
emitted by embedded esbuild and declarations by TSGo. Native npm packages also
include their embedded dependencies' license notices.
