import { test } from "node:test";
import assert from "node:assert/strict";
import { cp, mkdir, realpath, rm, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { randomUUID } from "node:crypto";
import { fileURLToPath, pathToFileURL } from "node:url";
import { cliLocation } from "../src/binary.mjs";

const workspace = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
const source = resolve(dirname(fileURLToPath(import.meta.url)), "../src/binary.mjs");

async function installedTooling(t) {
  const directory = resolve(workspace, ".angulus", `binary-test-${randomUUID()}`);
  const root = resolve(directory, "node_modules/@angulus/tooling");
  t.after(() => rm(directory, { recursive: true, force: true }));
  await mkdir(resolve(root, "src"), { recursive: true });
  await writeFile(resolve(root, "package.json"), JSON.stringify({ name: "@angulus/tooling", version: "0.3.0", type: "module" }));
  await cp(source, resolve(root, "src/binary.mjs"));
  const { cliLocation: locate } = await import(pathToFileURL(resolve(root, "src/binary.mjs")).href);
  async function platformCli(target, version = "0.3.0", name = `@angulus/compiler-${target}`) {
    const path = resolve(directory, "node_modules", name);
    await mkdir(resolve(path, "bin"), { recursive: true });
    await writeFile(resolve(path, "package.json"), JSON.stringify({ name, version }));
    const executable = target.startsWith("win32-") ? "angulus.exe" : "angulus";
    await writeFile(resolve(path, "bin", executable), "test executable");
    return resolve(path, "bin", executable);
  }
  return { locate, platformCli };
}

test("workspace launcher selects the locally built Go CLI", async () => {
  const location = await cliLocation();
  assert.equal(location.sourceRoot, workspace);
  assert.equal(location.binary, resolve(workspace, ".angulus/bin", process.platform === "win32" ? "angulus.exe" : "angulus"));
});

test("installed tooling reports a missing optional platform CLI clearly", async t => {
  const { locate } = await installedTooling(t);
  await assert.rejects(locate({ platform: "linux", arch: "x64" }), /Missing @angulus\/compiler-linux-x64@0\.3\.0.*optional dependencies/);
  await assert.rejects(locate({ platform: "freebsd", arch: "x64" }), /does not support freebsd-x64/);
});

test("installed tooling resolves the matching platform package", async t => {
  const { locate, platformCli } = await installedTooling(t);
  for (const [platform, arch] of [["darwin", "arm64"], ["linux", "x64"], ["win32", "arm64"]]) {
    const binary = await platformCli(`${platform}-${arch}`);
    assert.deepEqual(await locate({ platform, arch }), { binary: await realpath(binary) });
  }
});

test("installed tooling rejects wrong-version packages and missing binaries", async t => {
  const { locate, platformCli } = await installedTooling(t);
  await platformCli("linux-x64", "0.4.0");
  await assert.rejects(locate({ platform: "linux", arch: "x64" }), /version mismatch/);
  const binary = await platformCli("linux-x64");
  await rm(binary);
  await assert.rejects(locate({ platform: "linux", arch: "x64" }), { code: "ENOENT" });
});
