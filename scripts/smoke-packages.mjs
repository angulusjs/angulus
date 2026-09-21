import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmod, copyFile, mkdir, readFile, readdir, realpath, rename, rm, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { validateArtifacts } from "./publish.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const release = resolve(root, ".angulus/release");

async function copyDemo(source, destination) {
  await mkdir(destination, { recursive: true });
  for (const entry of await readdir(source, { withFileTypes: true })) {
    if (entry.name.startsWith(".") || ["node_modules", "dist"].includes(entry.name)) continue;
    if (entry.isDirectory()) await copyDemo(resolve(source, entry.name), resolve(destination, entry.name));
    else if (entry.isFile() && /\.(ts|html|css|json)$/.test(entry.name)) await copyFile(resolve(source, entry.name), resolve(destination, entry.name));
  }
}

export async function smokePackages() {
  const manifestFile = resolve(release, "manifest.json");
  const manifest = JSON.parse(await readFile(manifestFile, "utf8"));
  const artifacts = await validateArtifacts(manifestFile, `v${manifest.version}`);
  const host = artifacts.packages.find(item => item.platform === process.platform && item.arch === process.arch);
  assert.ok(host, `No packed compiler for ${process.platform}-${process.arch}`);
  const smoke = resolve(release, "smoke");
  await rm(smoke, { recursive: true, force: true });
  const project = resolve(smoke, "project");
  const stubs = resolve(smoke, "bin");
  await mkdir(project, { recursive: true });
  await mkdir(stubs);
  await writeFile(resolve(project, "package.json"), JSON.stringify({ name: "angulus-install-smoke", private: true, type: "module" }, null, 2));
  await copyDemo(resolve(root, "examples/demo"), project);
  await writeFile(resolve(project, "src/vite-contract.ts"), `
import defaultAngulus, { angulus, type CheckEvent } from "@angulus/tooling/vite";
import type { Plugin } from "vite";
const plugin: Plugin = angulus({
  checkBuild: true,
  onEvent(event) {
    const typedEvent: CheckEvent = event;
    if (typedEvent.type === "checked") {
      const valid: boolean = typedEvent.valid;
      void valid;
    }
  },
});
const defaultPlugin: Plugin = defaultAngulus();
void [plugin, defaultPlugin];
`);
  const marker = resolve(smoke, "go-invoked");
  const goStub = process.platform === "win32" ? "go.cmd" : "go";
  await writeFile(resolve(stubs, goStub), process.platform === "win32"
    ? `@echo off\r\necho GO_MUST_NOT_RUN>"${marker}"\r\nexit /b 99\r\n`
    : `#!${process.execPath}\nimport { writeFileSync } from 'node:fs'; writeFileSync(${JSON.stringify(marker)}, 'GO_MUST_NOT_RUN'); process.exit(99);\n`);
  await chmod(resolve(stubs, goStub), 0o755);
  const env = { ...process.env, NODE_PATH: "", NODE_OPTIONS: "", PATH: `${stubs}${process.platform === "win32" ? ";" : ":"}${process.env.PATH ?? ""}` };
  function run(command, args, expected = 0, extraEnv = {}) {
    const result = spawnSync(command, args, { cwd: project, env: { ...env, ...extraEnv }, encoding: "utf8", timeout: 180_000 });
    if (result.error) throw result.error;
    if (expected === 0 && result.status !== 0) throw new Error(`${command} ${args.join(" ")} failed (${result.status}):\n${result.stdout}\n${result.stderr}`);
    if (expected !== 0 && (result.status === 0 || result.signal)) throw new Error(`Expected actionable failure, got ${result.status}: ${result.stderr}`);
    if (expected === 0) process.stdout.write(result.stdout);
    return result;
  }
  const rootManifest = JSON.parse(await readFile(resolve(root, "package.json"), "utf8"));
  const selected = [...artifacts.packages.filter(item => item.kind !== "compiler"), host];
  const installArgs = [
    "install", "--ignore-scripts", "--include=optional", "--workspaces=false", "--no-audit", "--no-fund",
    "--registry=https://registry.npmjs.org",
    ...selected.map(item => item.tarball),
    `@types/node@${rootManifest.devDependencies["@types/node"]}`, `tsx@${rootManifest.devDependencies.tsx}`,
  ];
  run(process.env.npm_execpath ? process.execPath : "npm", process.env.npm_execpath ? [process.env.npm_execpath, ...installArgs] : installArgs);
  const nodeModules = await realpath(resolve(project, "node_modules"));
  const guard = resolve(smoke, "guard.mjs");
  const trace = resolve(smoke, "native-compiler.log");
  const tooling = resolve(nodeModules, "@angulus/tooling");
  const compilerPackage = resolve(nodeModules, host.name);
  const binary = resolve(compilerPackage, "bin", process.platform === "win32" ? "angulus-compiler.exe" : "angulus-compiler");
  // This guard records actual child-process invocations, rather than merely
  // asserting that a platform package happened to be installed.
  await writeFile(guard, `
import childProcess from "node:child_process";
import { appendFileSync } from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { basename, resolve } from "node:path";
for (const method of ["spawn", "spawnSync", "execFile", "execFileSync"]) {
  const original = childProcess[method];
  childProcess[method] = function(command, ...args) {
    if (/^go(?:\\.exe|\\.cmd)?$/i.test(basename(String(command)))) throw new Error("GO_MUST_NOT_RUN");
    if (resolve(String(command)) === ${JSON.stringify(binary)}) appendFileSync(${JSON.stringify(trace)}, "native\\n");
    return original.call(this, command, ...args);
  };
}
syncBuiltinESMExports();
`);
  const guardedEnv = { NODE_OPTIONS: `--import=${pathToFileURL(guard).href}` };
  const probe = resolve(project, "probe.mjs");
  await writeFile(probe, `
import assert from "node:assert/strict";
import { realpath, readFile, readdir } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, isAbsolute, relative, resolve, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { signal, computed } from "@angulus/core";
import { createRouter } from "@angulus/router";
const nodeModules = ${JSON.stringify(nodeModules)};
function inside(file) {
  const part = relative(nodeModules, file);
  assert.ok(part && part !== ".." && !part.startsWith(".." + sep) && !isAbsolute(part), "Resolved outside isolated node_modules: " + file);
}
async function checkTree(directory) {
  for (const item of await readdir(directory, { withFileTypes: true })) {
    const file = resolve(directory, item.name);
    if (item.isSymbolicLink()) inside(await realpath(file));
    else if (item.isDirectory()) await checkTree(file);
  }
}
await checkTree(nodeModules);
const visitedPackages = new Set();
async function checkDependencies(manifestFile) {
  if (visitedPackages.has(manifestFile)) return;
  visitedPackages.add(manifestFile);
  const manifest = JSON.parse(await readFile(manifestFile, "utf8"));
  const require = createRequire(manifestFile);
  const dependencies = {
    ...manifest.dependencies,
    ...manifest.peerDependencies,
    ...manifest.optionalDependencies,
  };
  for (const name of Object.keys(dependencies)) {
    const optional = Object.hasOwn(manifest.optionalDependencies ?? {}, name)
      || manifest.peerDependenciesMeta?.[name]?.optional;
    let dependencyManifest;
    for (const searchPath of require.resolve.paths(name) ?? []) {
      try {
        dependencyManifest = await realpath(resolve(searchPath, name, "package.json"));
        break;
      } catch (error) {
        if (error.code !== "ENOENT" && error.code !== "ENOTDIR") throw error;
      }
    }
    if (!dependencyManifest) {
      assert.ok(optional, "Missing installed dependency " + name + " required by " + manifest.name);
      continue;
    }
    inside(dependencyManifest);
    await checkDependencies(dependencyManifest);
  }
}
await checkDependencies(${JSON.stringify(resolve(project, "package.json"))});
const toolingRequire = createRequire(${JSON.stringify(resolve(tooling, "package.json"))});
for (const name of ["@angulus/core", "@angulus/router", "@angulus/tooling/vite"]) {
  const file = await realpath(fileURLToPath(import.meta.resolve(name)));
  inside(file);
  assert.ok(!file.endsWith(".ts"), "Runtime exports must be compiled JS: " + file);
}
const toolingManifest = JSON.parse(await readFile(${JSON.stringify(resolve(tooling, "package.json"))}, "utf8"));
for (const name of Object.keys(toolingManifest.dependencies)) inside(await realpath(toolingRequire.resolve(name)));
inside(await realpath(toolingRequire.resolve(${JSON.stringify(`${host.name}/package.json`)})));
const count = signal(2);
const doubled = computed(() => count() * 2);
assert.equal(doubled(), 4);
count.set(3);
assert.equal(doubled(), 6);
assert.equal(typeof createRouter, "function");
const { compilerLocation } = await import(pathToFileURL(${JSON.stringify(resolve(tooling, "src/binary.mjs"))}));
const location = await compilerLocation();
assert.equal(location.sourceRoot, undefined);
assert.equal(location.binary, ${JSON.stringify(binary)});
const { CompilerClient } = await import(pathToFileURL(${JSON.stringify(resolve(tooling, "src/client.mjs"))}));
const compiler = new CompilerClient();
try { await compiler.start(); assert.ok(compiler.pid); } finally { await compiler.close(); }
console.log("Plain Node runtime imports and installed native compiler passed.");
`);
  run(process.execPath, [probe], 0, guardedEnv);
  const cli = resolve(tooling, "src/cli.mjs");
  for (const command of ["check", "test", "build"]) {
    const before = await readFile(trace, "utf8");
    run(process.execPath, [cli, command, "--root", project], 0, guardedEnv);
    if (command !== "test") assert.ok((await readFile(trace, "utf8")).length > before.length, `${command} must execute the installed compiler`);
  }
  assert.match(await readFile(resolve(project, "dist/index.html"), "utf8"), /<script\b/);
  const hiddenCompiler = resolve(smoke, "compiler-disabled");
  await rename(compilerPackage, hiddenCompiler);
  try {
    const result = run(process.execPath, [cli, "check", "--root", project], 1, guardedEnv);
    assert.match(result.stderr, /optional dependencies enabled|--include=optional/);
    assert.ok(result.stderr.includes(host.name), "Missing compiler error must identify the required platform package");
    assert.doesNotMatch(result.stderr, /GO_MUST_NOT_RUN/);
  } finally {
    await rename(hiddenCompiler, compilerPackage);
  }
  await assert.rejects(readFile(marker), { code: "ENOENT" }, "Smoke must never invoke Go");
  console.log(`Package smoke passed: ${project}\nNo Go or workspace package symlinks; native ${host.name}@${manifest.version} verified.`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await smokePackages();
