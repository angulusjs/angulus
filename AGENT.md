# Angulus repository guide

This file describes the current repository. The migration proposal in
`docs/go-tooling-migration.md` is a plan, not evidence that every planned feature
is implemented. Older instructions describing Vite and `@Component` are
superseded.

## Architecture in this checkout

- Browser packages (`packages/core`, `router`, `forms`, `http`) are TypeScript
  ESM runtime libraries.
- Project tooling is Go: `cmd/angulus` is the application CLI and
  `internal/project` handles descriptor discovery, generated facades, checking,
  building, serving and preview.
- The HTML/template parser and generator are in `internal/compiler`.
- Application bundles are ESM and use the public Go API from
  `github.com/evanw/esbuild/pkg/api`.
- TypeScript checking is delegated to the native TSGo executable as a
  subprocess. Do not route this through the TypeScript JavaScript API or through
  the `node_modules/.bin/tsgo` Node launcher.
- `@angulus/tooling` is only a small Node launcher: platform-binary selection,
  argv, inherited stdio and signal forwarding. Platform binaries are packaged
  by `internal/release` as optional dependencies, with versions coupled to the
  tooling package. Never compile Go inside a consuming application.
- `cmd/angulus-release` and `internal/release` own source CLI/runtime builds,
  size measurement, packaging, versioning and publication preflight. npm scripts
  invoke this Go command; npm remains the external package manager. The Node
  smoke/isolation scripts are test harnesses, not production orchestration.
  Do not weaken integrity/version checks or publish during development.

## Component input model

Components are discovered from `*.component.json` files. The required fields
are `selector`, `module`, `export`, and `template`. Optional fields are `styles`,
`imports` (source descriptor paths or library `{module, export}` references),
and `customElements`. Source paths resolve
relative to the descriptor. The exported class is ordinary TypeScript: do not
add or document `@Component`, run source code during discovery, or rewrite
user-authored TypeScript. Go-generated facade and render files are separate,
tracked as compiler-owned artifacts, and must not overwrite user files.

## Explicit limitations

- There is no Vite plugin or JavaScript compiler pipeline in `@angulus/tooling`.
- Library builds emit ESM, TSGo declarations and version-1 component metadata;
  installed descriptors resolve selectors without evaluating library code.
  The JavaScript library API is removed; use `build --lib`.
- Template checking uses generated TypeScript contracts with native TSGo to
  check referenced member visibility, required/typed component inputs, output
  payloads, DOM property/event types, two-way values and `@if`/`@switch`/`@for`
  narrowing. Diagnostics map to their HTML expression, binding or element
  span for structural errors and exact original tokens for expressions.
  Expressions use a restricted parsed grammar, not
  the complete TypeScript language. TSGo also checks project files selected by
  `tsconfig.json`.
  Reject unused private/protected input/output declarations as well. Preserve
  the existing restrictions on assignments, arrows, `this`, and statements.
- Scoped CSS is implemented for component `styles[]`, including selector
  scoping, keyframes and relative URL embedding. Component styles are currently
  loaded with reachable component modules, including lazy routes; `@import`, CSS nesting,
  `:host`, `:global` and `:root` are diagnosed as unsupported.
- Providers/DI and multiple named library entrypoints are not implemented.
  Source maps compose back to original TS/HTML/CSS. Never accept `providers`
  with no effect.
- Development builds reuse public esbuild contexts, rebuilding their entry
  graph as needed. Native TSGo checks use persistent build-information caches.
  Startup source errors are recoverable through the running server, current
  errors replay to new browser connections, and stale checks must not clear
  current errors.
- `examples/demo` uses the ordinary-class descriptor model; generated
  facades/render/template-check/CSS files are compiler-owned adjacent files
  and ignored by Git.

When changing behavior or documentation, describe these boundaries accurately
and do not add placeholder APIs for unsupported features.

## Useful commands

```sh
go test ./...
npm ci
npm test
npm run typecheck
npm run test:browser
npm run packages:pack
npm run packages:smoke
```

`npm ci` builds `.angulus/bin/angulus` and builds the runtime ESM/declarations.
`npm test` runs Go and runtime/tooling regression tests. Package smoke tests use
published tarballs in an isolated project and must not rely on workspace
symlinks or a Go installation. `npm run typecheck` starts native TSGo directly.
`npm run test:browser` exercises the Go development server, runtime behavior,
CSS state preservation, template reload/error overlay, build, and preview.

CLI commands currently available are `check`, `build`, `serve`, `preview`, and
`test`. TypeScript diagnostics come from TSGo; application builds are browser
ESM via esbuild's Go API. `test` invokes `npm test` in the application root.
The CLI supports `--json` version-1 revisioned events, `generate component`
(including a working unit test),
and validated `angulus.config.json` host/port/proxy/test/library settings.
Do not reintroduce Vite integration or decorator/source rewriting.

Keep changes focused, run relevant existing tests, and do not modify unrelated
user files or publish packages without explicit authorization.
