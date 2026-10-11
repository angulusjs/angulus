# Component libraries

Build a component library with the native Go CLI:

```sh
angulus build --lib
```

The default library entry is `src/index.ts`; set `library.entry` in
`angulus.config.json` to use a different TypeScript entry. The package manifest
must include a name, version, and `@angulus/core` peer dependency. The build
checks the complete project, emits ESM with esbuild and TypeScript declarations with TSGo,
and publishes the output to `dist` only after all stages succeed.

The public TypeScript entry must export compiled facades, not undecorated source
classes, so importing the library registers the same constructor with its render:

```ts
export { Badge } from "./badge.component.generated";
```

Public aliases and barrel re-exports are supported:

```ts
export { Badge as StatusBadge } from "./badge.component.generated";
export * from "./buttons";
```

Metadata records actual public entry exports, not guessed class names.
Type-only exports and defaults do not become named component imports. Consumers
can use `{ "module": "@example/widgets", "export": "StatusBadge" }` for a public
root alias, or the explicit component subpath below.

The generated package exposes its root module and declarations, individual
compiled components under `components/<selector>.js`, and `style.css` when
components have scoped styles. `angulus.components.json` is versioned metadata
for descriptor-based component imports; it records each selector alongside
its package module and named export. Consumers can import a library component
explicitly:

```json
{
  "imports": [
    { "module": "@example/widgets/components/ui-badge.js", "export": "Badge" }
  ]
}
```

The component selector is read from the installed package's metadata rather
than by evaluating package JavaScript. A package-only `module` specifier is
supported when the named export identifies one component unambiguously; use
the component subpath when multiple selectors expose the same class name. Keep
`@angulus/core` external as a peer dependency so the application and library
share one runtime instance.

Existing pre-migration `.d.ts.angulus.json` sidecars are not the descriptor-era
metadata contract. Rebuild libraries with the Go CLI before using them in
descriptor `imports`.

Import `@example/widgets/style.css` from the application's TypeScript entry
when the library publishes styles. Declare `*.css` modules in the application's
TypeScript assets declarations. Library CSS is not implicitly loaded by
descriptor metadata.

The former JavaScript `@angulus/tooling/library` build API is removed; library
compilation now belongs to the Go tooling. Application builds without `--lib`
remain browser ESM builds and are not publishable libraries.
