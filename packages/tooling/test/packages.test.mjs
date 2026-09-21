import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { packPackages, targets } from "../../../scripts/pack.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
const readManifest = name => readFile(resolve(root, "packages", name, "package.json"), "utf8").then(JSON.parse);

test("publishable runtime manifests expose compiled ESM and declaration files", async () => {
  for (const name of ["core", "router"]) {
    const manifest = await readManifest(name);
    assert.equal(manifest.exports["."].import, "./dist/index.js");
    assert.equal(manifest.exports["."].types, "./dist/index.d.ts");
    assert.equal(manifest.type, "module");
    assert.equal(manifest.license, "MIT");
    assert.equal(manifest.publishConfig.access, "public");
    assert.ok(manifest.files.includes("dist"));
    assert.ok(manifest.files.includes("LICENSE"));
  }
  const core = await readManifest("core");
  const router = await readManifest("router");
  const tooling = await readManifest("tooling");
  assert.equal(router.version, core.version);
  assert.equal(tooling.version, core.version);
  assert.equal(router.dependencies["@angulus/core"], core.version);
  assert.equal(tooling.optionalDependencies, undefined, "Unpublished compiler dependencies belong only in staged manifests");
  assert.ok(tooling.files.includes("types"));
  assert.deepEqual(tooling.exports["./vite"], {
    types: "./types/vite.d.mts",
    import: "./src/vite.mjs",
    default: "./src/vite.mjs",
  });
  assert.deepEqual(tooling.exports["./library"], {
    types: "./types/library.d.mts",
    import: "./src/library.mjs",
    default: "./src/library.mjs",
  });
});

test("compiler package targets cover the seven supported npm OS/CPU pairs", () => {
  assert.deepEqual(targets.map(([platform, arch]) => `${platform}-${arch}`), [
    "darwin-x64", "darwin-arm64", "linux-x64", "linux-arm64", "linux-arm", "win32-x64", "win32-arm64",
  ]);
  assert.deepEqual(targets.filter(([platform]) => platform === "win32").map(([, , goos]) => goos), ["windows", "windows"]);
});

test("packing rejects invalid arguments and tag mismatches before starting builds", async () => {
  await assert.rejects(packPackages(["--unknown"]), /Usage:/);
  await assert.rejects(packPackages(["--tag", "0.1.0"]), /Release tag/);
  const { version } = await readManifest("core");
  const different = version === "999.0.0" ? "998.0.0" : "999.0.0";
  await assert.rejects(packPackages(["--tag", `v${different}`]), /does not match package version/);
});
