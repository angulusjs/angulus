# Publishing component libraries

`angulus build --lib` builds a library rather than an HTML application. Templates
are checked and compiled at library build time. Consumers install ordinary ESM
JavaScript, TypeScript declarations, scoped CSS, and small compiler metadata
files; the original TypeScript, HTML, and CSS sources are not required.

This command and `@angulus/tooling/library` are new tooling features. Use a
tooling release containing them for **both** the library and its consumers;
the previously published `0.1.1` tooling does not support library metadata.

## Create a library

An example source `package.json`:

```json
{
  "name": "@my-org/ui",
  "version": "0.1.0",
  "type": "module",
  "license": "MIT",
  "scripts": {
    "build": "angulus build --lib"
  },
  "peerDependencies": {
    "@angulus/core": "^0.1.1"
  },
  "publishConfig": {
    "access": "public"
  }
}
```

Install the current tooling and the peer runtime for local development:

```sh
npm install --save-dev @angulus/tooling @angulus/core
```

Keep `@angulus/core` in `peerDependencies`, not `dependencies` or
`optionalDependencies`: a library and its application must share the same runtime.
The build externalizes declared dependencies, optional dependencies, and peer
dependencies, including their subpath imports. Development-only dependencies
can be bundled. The generated package omits development dependencies and scripts.

Use a `tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "strict": true,
    "experimentalDecorators": true,
    "noEmit": true,
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "types": [],
    "skipLibCheck": true
  },
  "include": ["src/**/*.ts"]
}
```

Create `src/button.ts`:

```ts
import { Component, input, output } from "@angulus/core";

@Component({
  selector: "ui-button",
  templateUrl: "./button.html",
  styleUrl: "./button.css",
})
export class Button {
  readonly label = input.required<string>();
  readonly pressed = output<void>();

  press(): void {
    this.pressed.emit();
  }
}
```

`src/button.html`:

```html
<button (click)="press()">{{ label() }}</button>
```

`src/button.css`:

```css
button { border-radius: 6px; padding: 8px 16px; }
```

Export the public API from `src/index.ts`:

```ts
export { Button } from "./button";
```

Named aliases and `export *` barrels are supported. Components still must be
named exported classes; default component exports are not supported.

The default entry is `src/index.ts`. To change it, use `angulus.config.json`:

```json
{
  "library": {
    "entry": "src/public-api.ts"
  }
}
```

## Build, pack, publish

```sh
npm run build
npm pack ./dist
# Inspect/test the archive before publishing:
npm publish ./dist --access public
```

The build does **not** publish anything. Publish `dist`, not the source project.
It contains its own generated `package.json` with ESM/type exports, copied
package metadata, dependencies, and optional `README.md` / `LICENSE`.
The source project's manifest is not rewritten. `dist` is a generated output
directory and is replaced on successful bundling.

The output includes:

- `index.js`: compiled ESM entry, with additional chunks when necessary.
- `types/`: native TypeScript declarations preserving the public API.
- `*.d.ts.angulus.json` alongside component declarations: versioned selectors
  used by the consumer's static template checker. Do not remove these files.
- `style.css`, when styles exist: scoped library CSS, exported as
  `@my-org/ui/style.css` and marked as a side effect for bundlers.

No absolute build-machine paths or source maps are required by this format.
Scopes include the package name/version so identically named component files
in different libraries do not share CSS scope identifiers.

### Style encapsulation

Extracting CSS into `style.css` does not make component selectors global.
For example, `button` becomes `button[data-f-...]`, and the compiled render
function adds the matching attribute to its elements. Each component has its
own scope; parent selectors do not target child component internals.
Animation names and their `@keyframes` definitions are scoped as well.

This is selector-based encapsulation, **not Shadow DOM**. Global application
rules can still affect library elements, CSS inheritance and custom properties
still work, and global constructs such as `@font-face` are not isolated.
The stylesheet export includes a declaration so strict TypeScript applications
can import it without adding a wildcard CSS module declaration.

Library templates and styles are validated before bundling. Declaration
generation uses the native TypeScript compiler. Invalid templates, missing
inputs, and incompatible types fail the build; no publication is attempted.

## Import in an application

```sh
npm install @my-org/ui
```

Import the stylesheet once in the application's entry module:

```ts
import "@my-org/ui/style.css";
```

Use a library component in an application component:

```ts
import { Component } from "@angulus/core";
import { Button } from "@my-org/ui";

@Component({
  selector: "app-home",
  templateUrl: "./home.html",
  imports: [Button],
})
export class Home {
  save(): void {
    console.log("Saved");
  }
}
```

```html
<ui-button [label]="'Save'" (pressed)="save()" />
```

The consumer checks required inputs, their value types, and output event types
against installed declarations. It does not compile the library's templates or
execute library code to discover selectors. The same imports work in
`angulus serve`, `angulus check`, and `angulus build`.
The Vite plugin deduplicates and prebundles `@angulus/core` so the application
and its libraries also share the runtime during development, including linked
workspace installations.

A library can import components from another compiled library in the same way.
Declare that package as a dependency or peer dependency; explicitly include any
required dependency stylesheets in the application.

## Programmatic build

```ts
import { buildLibrary } from "@angulus/tooling/library";

const result = await buildLibrary({
  root: "/absolute/path/to/ui",
  entry: "src/index.ts",
  onEvent(event) {
    if (event.type === "checked") console.log(event.diagnostics);
  },
});

console.log(result.directory, result.entry, result.types, result.style);
```

The API returns absolute output paths and closes its compiler process on success
or failure. `style` is absent for libraries without CSS. `angulus build --lib
--json` reports a `checked` event followed by `built` with the same paths.

The initial format supports one public ESM entry plus the optional CSS export,
not CommonJS or independently configured secondary entry points. Stylesheets
are explicitly imported by consumers; they are not injected by the library at
runtime. Existing application builds without `--lib` are unchanged.
