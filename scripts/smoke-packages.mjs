import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmod, mkdir, readFile, realpath, rename, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { validateArtifacts } from "./release-binary.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const release = resolve(root, ".angulus/release");

export async function smokePackages() {
  const manifestFile = resolve(release, "manifest.json");
  const manifest = JSON.parse(await readFile(manifestFile, "utf8"));
  const artifacts = await validateArtifacts(manifestFile, `v${manifest.version}`);
  const host = artifacts.packages.find(item => item.platform === process.platform && item.arch === process.arch);
  assert.ok(host, `No packed CLI for ${process.platform}-${process.arch}`);

  const smoke = resolve(release, "smoke");
  await rm(smoke, { recursive: true, force: true });
  const project = resolve(smoke, "project");
  const stubs = resolve(smoke, "bin");
  await mkdir(resolve(project, "src"), { recursive: true });
  await mkdir(stubs);
  await writeFile(resolve(project, "package.json"), JSON.stringify({
    name: "angulus-install-smoke",
    private: true,
    type: "module",
    scripts: { test: "node -e \"console.log('isolated app test ran')\"" },
  }, null, 2));
  await writeFile(resolve(project, "src/counter.component.json"), JSON.stringify({
    selector: "app-counter",
    module: "./counter.ts",
    export: "Counter",
    template: "./counter.html",
  }));
  await writeFile(resolve(project, "src/counter.ts"), "export class Counter { count = 1; }\n");
  await writeFile(resolve(project, "src/counter.html"), "<p>{{ count }}</p>\n");
  await writeFile(resolve(project, "src/main.ts"), 'import "./counter.component.generated";\n');
  await writeFile(resolve(project, "index.html"), '<!doctype html><script type="module" src="./src/main.ts"></script>\n');

  const marker = resolve(smoke, "go-invoked");
  const goStub = process.platform === "win32" ? "go.cmd" : "go";
  await writeFile(resolve(stubs, goStub), process.platform === "win32"
    ? `@echo off\r\necho GO_MUST_NOT_RUN>"${marker}"\r\nexit /b 99\r\n`
    : `#!${process.execPath}\nimport { writeFileSync } from 'node:fs'; writeFileSync(${JSON.stringify(marker)}, 'GO_MUST_NOT_RUN'); process.exit(99);\n`);
  await chmod(resolve(stubs, goStub), 0o755);
  const env = {
    ...process.env,
    NODE_PATH: "",
    NODE_OPTIONS: "",
    PATH: `${stubs}${process.platform === "win32" ? ";" : ":"}${process.env.PATH ?? ""}`,
  };
  function run(command, args, expected = 0, extraEnv = {}) {
    const result = spawnSync(command, args, { cwd: project, env: { ...env, ...extraEnv }, encoding: "utf8", timeout: 180_000 });
    if (result.error) throw result.error;
    if (expected === 0 && result.status !== 0) throw new Error(`${command} ${args.join(" ")} failed (${result.status}):\n${result.stdout}\n${result.stderr}`);
    if (expected !== 0 && (result.status === 0 || result.signal)) throw new Error(`Expected actionable failure, got ${result.status}: ${result.stderr}`);
    return result;
  }

  const selected = [...artifacts.packages.filter(item => item.kind !== "compiler"), host];
  const installArgs = [
    "install", "--ignore-scripts", "--include=optional", "--workspaces=false", "--no-audit", "--no-fund",
    "--registry=https://registry.npmjs.org",
    ...selected.map(item => item.tarball),
  ];
  run(process.env.npm_execpath ? process.execPath : "npm",
    process.env.npm_execpath ? [process.env.npm_execpath, ...installArgs] : installArgs);

  const nodeModules = await realpath(resolve(project, "node_modules"));
  const tooling = resolve(nodeModules, "@angulus/tooling");
  const cli = resolve(tooling, "src/cli.mjs");
  const toolingRequire = createRequire(resolve(tooling, "package.json"));
  const isolation = await import("./package-isolation.mjs");
  await isolation.checkInstalledDependencies(resolve(project, "package.json"), nodeModules);
  for (const name of ["@angulus/core", "@angulus/router", "@angulus/forms", "@angulus/http"]) {
    const file = await realpath(toolingRequire.resolve(name));
    assert.ok(file.startsWith(nodeModules), `${name} must resolve within the isolated installation`);
  }
  const toolingManifest = JSON.parse(await readFile(resolve(tooling, "package.json"), "utf8"));
  assert.deepEqual(Object.keys(toolingManifest.exports ?? {}), [], "The Go CLI exposes no JavaScript compiler/library/Vite APIs");
  assert.deepEqual(Object.keys(toolingManifest.dependencies), ["@typescript/native-preview"]);
  const schema = JSON.parse(await readFile(resolve(tooling, "component.schema.json"), "utf8"));
  assert.deepEqual(schema.required, ["selector", "module", "export", "template"]);

  const binary = resolve(nodeModules, host.name, "bin", process.platform === "win32" ? "angulus.exe" : "angulus");
  const notices = await readFile(resolve(nodeModules, host.name, "THIRD_PARTY_NOTICES"), "utf8");
  for (const dependency of ["github.com/evanw/esbuild", "github.com/tdewolff/parse/v2", "golang.org/x/sys", "The Go Authors"]) {
    assert.ok(notices.includes(dependency), `Native package must include the ${dependency} license`);
  }
  const trace = resolve(smoke, "cli-invoked.log");
  const guard = resolve(smoke, "guard.mjs");
  await writeFile(guard, `
import childProcess from "node:child_process";
import { appendFileSync } from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { basename, resolve } from "node:path";
for (const method of ["spawn", "spawnSync", "execFile", "execFileSync"]) {
  const original = childProcess[method];
  childProcess[method] = function(command, ...args) {
    if (/^go(?:\\\\.exe|\\\\.cmd)?$/i.test(basename(String(command)))) throw new Error("GO_MUST_NOT_RUN");
    if (resolve(String(command)) === ${JSON.stringify(binary)}) appendFileSync(${JSON.stringify(trace)}, "native\\\\n");
    return original.call(this, command, ...args);
  };
}
syncBuiltinESMExports();
`);
  const guarded = { NODE_OPTIONS: `--import=${pathToFileURL(guard).href}` };

  const npmExec = process.env.npm_execpath ? [process.execPath, [process.env.npm_execpath]] : ["npm", []];
  const checkArgs = ["exec", "--", "angulus", "check", "--root", project];
  const check = run(npmExec[0], [...npmExec[1], ...checkArgs], 0, guarded);
  assert.match(check.stdout, /Checked 1 component/);
  const build = run(process.execPath, [cli, "build", "--root", project], 0, guarded);
  assert.match(build.stdout, /as ESM/);
  assert.ok((await readFile(resolve(project, "dist/index.html"), "utf8")).includes("main.js"));
  const appTest = run(process.execPath, [cli, "test", "--root", project], 0, guarded);
  assert.match(appTest.stdout, /isolated app test ran/);
  assert.ok((await readFile(trace, "utf8")).split("native").length >= 3, "CLI commands must invoke the installed native executable");

  const compilerPackage = resolve(nodeModules, host.name);
  const hidden = resolve(smoke, "cli-disabled");
  await rename(compilerPackage, hidden);
  try {
    const result = run(process.execPath, [cli, "check", "--root", project], 1, guarded);
    assert.match(result.stderr, /optional dependencies enabled|--include=optional/);
    assert.ok(result.stderr.includes(host.name), "Missing CLI error must identify the host package");
    assert.doesNotMatch(result.stderr, /GO_MUST_NOT_RUN/);
  } finally {
    await rename(hidden, compilerPackage);
  }
  await assert.rejects(readFile(marker), { code: "ENOENT" }, "Smoke must never invoke Go");
  console.log(`Package smoke passed: ${project}\nNo workspace dependency leakage; installed ${host.name}@${manifest.version} invoked directly.`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await smokePackages();
