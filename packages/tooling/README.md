# @angulus/tooling

Node launcher for the Angulus Go CLI. The package contains no Vite plugin,
JavaScript compiler pipeline, or JavaScript library-build API.

```sh
npm install @angulus/core
npm install --save-dev @angulus/tooling
npx angulus check
npx angulus build
```

Node.js 22.12+ is required. The published tooling package selects the matching
prebuilt `@angulus/compiler-<platform>-<arch>` optional dependency, then passes
arguments, stdio, and termination signals to its Go executable. Keep optional
dependencies enabled. Supported targets: macOS x64/arm64, Linux x64/arm64/arm,
and Windows x64/arm64. A published application does not need Go; a source
checkout requires Go 1.26+.

The Go CLI currently supports `check`, `build`, `serve`, `preview`, and `test`,
all with `--root <project-directory>`. `build` and `preview` also accept
`--out`; `serve` accepts `--host` and `--port`. Application builds emit ESM via
esbuild's public Go API. TypeScript checking runs native TSGo as a subprocess
using the project's `tsconfig.json` when present.

Components use ordinary exported TypeScript classes and `*.component.json`
descriptors. Required descriptor fields are `selector`, `module`, `export`, and
`template`; optional fields are `styles`, `imports` and `customElements`.
Template source is parsed and Go generates separate facade/render files.

Current limitations are intentional and explicit:

- `build --lib` emits ESM, native TSGo declarations and versioned metadata.
  Descriptor imports support installed library `{module, export}` references.
- TSGo generated contracts check member visibility, typed/required inputs,
  output payloads, DOM property/event types, two-way values and control-flow
  narrowing. Private/protected input/output declarations fail even when unused.
  Expressions preserve the restricted grammar: no statements, assignment,
  arrows, or `this`. Expression diagnostics map to original HTML tokens;
  structural errors identify binding/element spans.
- Component CSS is scoped and parsed; relative assets are embedded and
  keyframes are renamed. `@import`, nesting, `:host`, `:global`, and `:root`
  are unsupported. Reachable component styles load with their modules,
  including lazy routes, and source maps refer to original TS/HTML/CSS.
- No Vite integration, JavaScript `buildLibrary` API, DI/providers, multiple
  named library entries, or Angular `@Component` decorator is provided.
- `angulus test` runs configured test argv or `npm test` in the selected directory.
- `serve` polls and rebuilds. CSS-only edits preserve mounted state and update
  styles; TS/HTML edits reload and errors appear in a development overlay. There
  are persistent incremental esbuild contexts and TSGo build-information caches,
  but no Vite-compatible HMR. Initial source errors are recoverable without
  restarting the server.

Successful `build` (including `--lib`) and initial/incremental `serve` builds
print elapsed time, including template/type checking and output generation.
`--json` emits version-1 revisioned NDJSON events; `built` includes `durationMs`.
`generate component <name>`
creates TS/descriptor/HTML/CSS/test files. `angulus.config.json` supports host/port,
development-only HTTP proxies, test argv and `library.entry`; CLI flags override
configured host/port.

See the repository [README](https://github.com/angulusjs/angulus) for the
descriptor schema, build behavior, and current project limitations.
The package includes `component.schema.json` for optional `$schema` editor
completion. Platform packages include embedded third-party license notices.
