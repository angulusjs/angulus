import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
const readManifest = name => readFile(resolve(root, "packages", name, "package.json"), "utf8").then(JSON.parse);

test("publishable runtime manifests expose compiled ESM and declaration files", async () => {
  for (const name of ["core", "router", "forms", "http"]) {
    const manifest = await readManifest(name);
    assert.equal(manifest.exports["."].import, "./dist/index.js");
    assert.equal(manifest.exports["."].types, "./dist/index.d.ts");
    assert.equal(manifest.exports["."].default, "./dist/index.js");
    assert.equal(manifest.main, "./dist/index.js");
    assert.equal(manifest.types, "./dist/index.d.ts");
    assert.equal(manifest.type, "module");
    assert.equal(manifest.license, "MIT");
    assert.equal(manifest.publishConfig.access, "public");
    assert.ok(manifest.files.includes("dist"));
    assert.ok(manifest.files.includes("LICENSE"));
  }
  const core = await readManifest("core");
  const router = await readManifest("router");
  const forms = await readManifest("forms");
  const http = await readManifest("http");
  const tooling = await readManifest("tooling");
  assert.equal(router.version, core.version);
  assert.equal(forms.version, core.version);
  assert.equal(http.version, core.version);
  assert.equal(tooling.version, core.version);
  assert.equal(router.dependencies["@angulus/core"], core.version);
  assert.equal(forms.dependencies["@angulus/core"], core.version);
  assert.equal(http.dependencies["@angulus/core"], core.version);
  assert.equal(http.sideEffects, false);
  assert.equal(forms.sideEffects, false);
  assert.equal(tooling.optionalDependencies, undefined, "Unpublished compiler dependencies belong only in staged manifests");
  assert.deepEqual(tooling.bin, { angulus: "./src/cli.mjs" });
  assert.deepEqual(tooling.exports ?? {}, {});
  assert.deepEqual(tooling.dependencies, { "@typescript/native-preview": "7.0.0-dev.20260707.2" });
  assert.ok(tooling.files.includes("src/*.mjs"));
  assert.ok(tooling.files.includes("component.schema.json"));
  const schema = JSON.parse(await readFile(resolve(root, "packages/tooling/component.schema.json"), "utf8"));
  assert.equal(schema.additionalProperties, false);
  assert.deepEqual(schema.required, ["selector", "module", "export", "template"]);
});

test("Go CLI source build and native TypeScript commands select Go and TSGo executables", async () => {
  const rootManifest = JSON.parse(await readFile(resolve(root, "package.json"), "utf8"));
  assert.equal(rootManifest.scripts.compiler, "go run ./cmd/angulus-release build-cli");
  assert.equal(rootManifest.scripts.typecheck, "node scripts/tsgo.mjs --noEmit -p tsconfig.json");
  const pack = await readFile(resolve(root, "internal/release/pack.go"), "utf8");
  assert.ok(pack.includes("./cmd/angulus"));
  assert.match(pack, /binaryName \+= "\.exe"/);
  assert.equal(pack.includes("cmd/angulus-compiler"), false);
});
