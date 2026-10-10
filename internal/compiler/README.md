# Go template compiler

The Go CLI uses this package in-process. Run `go run ./cmd/angulus check
--root <project>` to discover `*.component.json` files, parse their templates,
generate compiler-owned facades/render modules, and invoke native TSGo.
`go run ./cmd/angulus build --root <project>` additionally bundles browser ESM
with esbuild's public Go API.

The parser supports text/interpolation, element and property/event bindings,
text-input `[(value)]`, `@if`/`@else`, tracked `@for`, and `@switch`. It returns
UTF-16 offsets for template diagnostics. The generator emits DOM operations
against `@angulus/core`; it does not execute source or template strings.

The project frontend parses a restricted expression grammar with operator
precedence, member/element access, calls, optional chaining, literals, arrays,
objects, and simple arrow callbacks. It qualifies free component members as
`ctx.member`, preserves property keys and lexical shadowing, and treats `@for`
render locals as signals while track expressions receive plain values.
Unsupported TypeScript syntax is diagnosed rather than passed through as if it
had been analyzed.

Preparation emits a TypeScript contract checker alongside each render module.
Native TSGo checks component member visibility and expression types, required
and typed child inputs, typed child outputs, DOM property/event types, two-way
signal values, and narrowing through generated `@if`, `@switch`, and `@for`
control flow. Checker diagnostics map back to the originating HTML expression,
binding, or element span. This remains a restricted template language, not a
general TypeScript parser; diagnostics identify the relevant HTML span rather
than a precise token mapping.

Scoped CSS uses the public `github.com/tdewolff/parse/v2/css` parser. Selectors
are scoped per compound, keyframes are renamed across a component's ordered
styles, and relative URL resources are embedded. Scoped `@import`, nesting,
`:host`, `:global`, and `:root` are diagnosed. CSS output is linked eagerly in
the current CLI; lazy-route CSS splitting is not implemented.

Generated files are written beside their descriptor and recorded in
`.angulus/project-generated.json`. The CLI refuses to overwrite user files that
collide with generated names and removes stale generated files only when their
compiler marker is intact.

Run `go test ./...` for parser, generator, project, CSS, build and server tests.
