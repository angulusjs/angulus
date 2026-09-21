# @angulus/tooling

The Angulus CLI, Go template compiler integration, native TypeScript checker,
and Vite plugin.

```sh
npm install @angulus/core @angulus/router
npm install --save-dev @angulus/tooling
npx angulus serve
```

Published packages include a matching prebuilt compiler via npm optional
dependencies. **Go is not required in application projects.** Keep optional
dependencies enabled. Supported targets: macOS x64/arm64, Linux x64/arm64/arm,
and Windows x64/arm64. Node.js 22.12+ is required.

Applications using `"types": ["node"]` in `tsconfig.json`, including the demo,
must also install `npm install --save-dev @types/node@22`. These types are also
needed by the generated component tests, which import `node:test` and
`node:assert`. The checker inherits your application's TypeScript configuration.

Commands: `angulus serve`, `check [--json]`, `build`, `preview`, `test`, and
`generate component <name>`. All accept `--root <project-directory>`.

`angulus build --lib` compiles a publishable ESM component library from
`src/index.ts` (override with `library.entry` in `angulus.config.json`). Declare
`@angulus/core` as a peer dependency. Publish the generated package with
`npm publish ./dist`, not the source directory. Consumers import components
from the package and explicitly import `<package>/style.css` when it has styles.
The `types` directory includes selector metadata required by the template checker.
See the [library guide](https://github.com/angulusjs/angulus/blob/main/docs/libraries.md).

For programmatic builds:

```js
import { buildLibrary } from "@angulus/tooling/library";
const result = await buildLibrary({ root: process.cwd(), entry: "src/index.ts" });
console.log(result.directory);
```

Components use TypeScript classes decorated with `@Component`, external HTML
templates and optional scoped CSS. A Vite configuration is not required.
For advanced integration:

```js
import { angulus } from "@angulus/tooling/vite";

export default { plugins: [angulus()] };
```

`angulus.config.json` optionally configures `host`, `port`, API `proxy`, and
the application's `test` command as an argument array. Production builds run
full type/template checks before bundling; `preview` is not a production server.

The framework is experimental. Documentation, examples, release procedure,
and known limitations: https://github.com/angulusjs/angulus
