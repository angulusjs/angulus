import { access, readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const toolingRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const platforms = new Set([
  "darwin-x64", "darwin-arm64",
  "linux-x64", "linux-arm64", "linux-arm",
  "win32-x64", "win32-arm64",
]);

export async function compilerLocation({ platform = process.platform, arch = process.arch } = {}) {
  const target = `${platform}-${arch}`;
  if (!platforms.has(target)) throw new Error(`Angulus compiler does not support ${target}. Supported platforms: ${[...platforms].join(", ")}`);
  const executable = platform === "win32" ? "angulus-compiler.exe" : "angulus-compiler";
  const sourceRoot = resolve(toolingRoot, "../..");
  // A published package is never allowed to build Go in the consumer's project.
  if (toolingRoot === resolve(sourceRoot, "packages/tooling")) {
    try {
      await access(resolve(sourceRoot, "go.mod"));
      return { binary: resolve(sourceRoot, ".angulus/bin", executable), sourceRoot };
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
    }
  }
  const manifest = JSON.parse(await readFile(resolve(toolingRoot, "package.json"), "utf8"));
  const name = `@angulus/compiler-${target}`;
  let compilerManifest;
  try { compilerManifest = require.resolve(`${name}/package.json`); }
  catch (error) {
    if (error.code !== "MODULE_NOT_FOUND") throw error;
    throw new Error(`Missing ${name}@${manifest.version}. Install Angulus with optional dependencies enabled (npm install --include=optional), or install ${name}@${manifest.version} explicitly. Go is not required.`, { cause: error });
  }
  const installed = JSON.parse(await readFile(compilerManifest, "utf8"));
  if (installed.name !== name || installed.version !== manifest.version) {
    throw new Error(`Angulus compiler version mismatch: expected ${name}@${manifest.version}, found ${installed.name}@${installed.version}. Reinstall matching tooling and compiler packages.`);
  }
  const binary = resolve(dirname(compilerManifest), "bin", executable);
  await access(binary);
  return { binary };
}
