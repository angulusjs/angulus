import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { checkInstalledDependencies, hostPlatform } from "../../../scripts/package-isolation.mjs";

const glibc = { platform: "linux", arch: "x64", libc: "glibc" };
const musl = { platform: "linux", arch: "x64", libc: "musl" };

test("host libc detection distinguishes glibc, musl and unknown without guessing", () => {
  const report = (header, sharedObjects = []) => ({ getReport: () => ({ header, sharedObjects }) });
  assert.deepEqual(hostPlatform("linux", "x64", report({ glibcVersionRuntime: "2.39" })), glibc);
  assert.deepEqual(hostPlatform("linux", "x64", report({}, ["/lib/ld-musl-x86_64.so.1"])), musl);
  assert.equal(hostPlatform("linux", "x64", report({})).libc, undefined);
  assert.deepEqual(hostPlatform("darwin", "arm64", { getReport() { assert.fail("Non-Linux must not inspect libc"); } }),
    { platform: "darwin", arch: "arm64", libc: undefined });
});

async function fixture(t) {
  const root = await mkdtemp(resolve(tmpdir(), "angulus-isolation-"));
  t.after(() => rm(root, { recursive: true }));
  const project = resolve(root, "project");
  const nodeModules = resolve(project, "node_modules");
  const manifestFile = resolve(project, "package.json");
  await mkdir(nodeModules, { recursive: true });
  await writeFile(manifestFile, JSON.stringify({ name: "app", dependencies: { lightningcss: "1.0.0" } }));
  async function pkg(name, fields = {}, external = false) {
    const directory = resolve(external ? resolve(root, "node_modules") : nodeModules, name);
    await mkdir(directory, { recursive: true });
    await writeFile(resolve(directory, "package.json"), JSON.stringify({ name, version: "1.0.0", ...fields }));
    return directory;
  }
  const check = host => checkInstalledDependencies(manifestFile, nodeModules, host);
  return { pkg, check, nodeModules };
}

test("glibc smoke ignores an unused ancestor musl optional package", async t => {
  const { pkg, check } = await fixture(t);
  await pkg("lightningcss", {
    optionalDependencies: { "lightningcss-linux-x64-gnu": "1.0.0", "lightningcss-linux-x64-musl": "1.0.0" },
  });
  await pkg("lightningcss-linux-x64-gnu", { os: ["linux"], cpu: ["x64"], libc: ["glibc"] });
  await pkg("lightningcss-linux-x64-musl", { os: ["linux"], cpu: ["x64"], libc: ["musl"] }, true);
  await check(glibc);
  await assert.rejects(check(musl), /outside isolated node_modules.*lightningcss-linux-x64-musl/);
});

test("musl smoke ignores ancestor glibc optional binaries, not a usable musl fallback", async t => {
  const { pkg, check } = await fixture(t);
  await pkg("lightningcss", {
    optionalDependencies: { "lightningcss-linux-x64-gnu": "1.0.0", "lightningcss-linux-x64-musl": "1.0.0" },
  });
  await pkg("lightningcss-linux-x64-musl", { os: "linux", cpu: "x64", libc: "musl" });
  await pkg("lightningcss-linux-x64-gnu", { os: "linux", cpu: "x64", libc: "glibc" }, true);
  await check(musl);
  await assert.rejects(check(glibc), /outside isolated node_modules.*lightningcss-linux-x64-gnu/);
});

test("OS and CPU restrictions apply only to optional packages", async t => {
  const { pkg, check } = await fixture(t);
  await pkg("lightningcss", { optionalDependencies: { native: "1.0.0" } });
  await pkg("native", { os: ["darwin"], cpu: ["arm64"] }, true);
  await check(glibc);
  await assert.rejects(check({ platform: "darwin", arch: "arm64" }), /outside isolated/);
  await pkg("lightningcss", { dependencies: { native: "1.0.0" } });
  await assert.rejects(check(glibc), /outside isolated/);
});

test("compatible optional dependencies and optional peers cannot leak from ancestors", async t => {
  const { pkg, check } = await fixture(t);
  await pkg("lightningcss", { optionalDependencies: { native: "1.0.0" } });
  await pkg("native", {}, true);
  await assert.rejects(check(glibc), /outside isolated/);
  await pkg("lightningcss", {
    peerDependencies: { native: "1.0.0" }, peerDependenciesMeta: { native: { optional: true } },
  });
  await assert.rejects(check(glibc), /outside isolated/);
});

test("unknown libc never exempts an ancestor native dependency", async t => {
  const { pkg, check } = await fixture(t);
  await pkg("lightningcss", { optionalDependencies: { native: "1.0.0" } });
  await pkg("native", { os: ["linux"], libc: ["musl"] }, true);
  await assert.rejects(check({ platform: "linux", arch: "x64" }), /outside isolated/);
});

test("missing optional packages are allowed, missing required packages are rejected", async t => {
  const { pkg, check } = await fixture(t);
  await pkg("lightningcss", { optionalDependencies: { "angulus-absent-test-dependency": "1.0.0" } });
  await check(glibc);
  await pkg("lightningcss", {
    dependencies: { "angulus-absent-test-dependency": "1.0.0" },
    peerDependenciesMeta: { "angulus-absent-test-dependency": { optional: true } },
  });
  await assert.rejects(check(glibc), /Missing installed dependency/);
});

test("negative platform restrictions are honored without ignoring a compatible binary", async t => {
  const { pkg, check } = await fixture(t);
  await pkg("lightningcss", { optionalDependencies: { native: "1.0.0" } });
  await pkg("native", { os: ["!linux"] }, true);
  await check(glibc);
  await assert.rejects(check({ platform: "darwin", arch: "arm64" }), /outside isolated/);
  await pkg("native", { os: ["linux"], cpu: ["!arm64"] }, true);
  await assert.rejects(check(glibc), /outside isolated/);
  await check({ platform: "linux", arch: "arm64", libc: "glibc" });
});

test("an installed symlink to an incompatible ancestor package is still rejected", async t => {
  const { pkg, check, nodeModules } = await fixture(t);
  await pkg("lightningcss", { optionalDependencies: { native: "1.0.0" } });
  const target = await pkg("native", { os: ["darwin"] }, true);
  await symlink(target, resolve(nodeModules, "native"), "dir");
  await assert.rejects(check(glibc), /outside isolated/);
});
