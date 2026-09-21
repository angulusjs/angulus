import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { rm } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(import.meta.url);

export async function buildPackages() {
  const tsgo = resolve(dirname(require.resolve("@typescript/native-preview/package.json")), "bin/tsgo");
  // Router declarations resolve the built core package, so order is significant.
  for (const name of ["core", "router"]) {
    await rm(resolve(root, "packages", name, "dist"), { recursive: true, force: true });
    const result = spawnSync(process.execPath, [tsgo, "-p", `packages/${name}/tsconfig.build.json`], {
      cwd: root, stdio: "inherit",
    });
    if (result.error) throw result.error;
    if (result.status !== 0) throw new Error(`Building @angulus/${name} failed (${result.status}).`);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await buildPackages();
