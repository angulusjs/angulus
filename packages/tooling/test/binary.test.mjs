import { test } from "node:test";
import assert from "node:assert/strict";
import { cp, mkdir, mkdtemp, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { compilerLocation } from "../src/binary.mjs";

const source = resolve(dirname(fileURLToPath(import.meta.url)), "../src/binary.mjs");

async function installedTooling(t) {
  const directory = await mkdtemp(resolve(tmpdir(), "angulus-binary-test-"));
  t.after(() => rm(directory, { recursive: true }));
  const root = resolve(directory, "node_modules/@angulus/tooling");
  await mkdir(resolve(root, "src"), { recursive: true });
  await writeFile(resolve(root, "package.json"), JSON.stringify({ name: "@angulus/tooling", version: "0.1.0", type: "module" }));
  await cp(source, resolve(root, "src/binary.mjs"));
  const { compilerLocation: locate } = await import(pathToFileURL(resolve(root, "src/binary.mjs")).href);
  async function compiler(target, version = "0.1.0", name = `@angulus/compiler-${target}`) {
    const path = resolve(directory, `node_modules/@angulus/compiler-${target}`);
    await mkdir(resolve(path, "bin"), { recursive: true });
    await writeFile(resolve(path, "package.json"), JSON.stringify({ name, version }));
    const executable = target.startsWith("win32-") ? "angulus-compiler.exe" : "angulus-compiler";
    await writeFile(resolve(path, "bin", executable), "test executable");
    return resolve(path, "bin", executable);
  }
  return { locate, compiler };
}

test("workspace compiler uses explicit source checkout fallback", async () => {
  const location = await compilerLocation();
  assert.equal(location.sourceRoot, resolve(dirname(source), "../../.."));
  assert.ok(location.binary.includes(".angulus"));
});

test("published tooling reports missing optional binary without trying to build Go", async t => {
  const { locate } = await installedTooling(t);
  await assert.rejects(locate({ platform: "linux", arch: "x64" }), /Missing @angulus\/compiler-linux-x64@0\.1\.0.*optional dependencies/);
  await assert.rejects(locate({ platform: "freebsd", arch: "x64" }), /does not support freebsd-x64/);
});

test("published tooling loads exactly matched platform packages", async t => {
  const { locate, compiler } = await installedTooling(t);
  for (const [platform, arch] of [["darwin", "arm64"], ["linux", "x64"], ["win32", "arm64"]]) {
    const binary = await compiler(`${platform}-${arch}`);
    assert.deepEqual(await locate({ platform, arch }), { binary: await realpath(binary) });
  }
});

test("published tooling rejects mismatched compiler versions and missing executables", async t => {
  const { locate, compiler } = await installedTooling(t);
  await compiler("linux-x64", "0.2.0");
  await assert.rejects(locate({ platform: "linux", arch: "x64" }), /version mismatch/);
  const binary = await compiler("linux-x64");
  await rm(binary);
  await assert.rejects(locate({ platform: "linux", arch: "x64" }), { code: "ENOENT" });
});
