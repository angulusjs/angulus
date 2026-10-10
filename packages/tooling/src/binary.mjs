import { access, readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const toolingRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const targets = new Set([
  "darwin-x64", "darwin-arm64",
  "linux-x64", "linux-arm64", "linux-arm",
  "win32-x64", "win32-arm64",
]);

export async function cliLocation({ platform = process.platform, arch = process.arch } = {}) {
  const target = `${platform}-${arch}`;
  if (!targets.has(target)) {
    throw new Error(`Angulus CLI does not support ${target}. Supported platforms: ${[...targets].join(", ")}`);
  }

  const executable = platform === "win32" ? "angulus.exe" : "angulus";
  const sourceRoot = resolve(toolingRoot, "../..");
  if (toolingRoot === resolve(sourceRoot, "packages/tooling")) {
    const binary = resolve(sourceRoot, ".angulus/bin", executable);
    try {
      await access(binary);
      return { binary, sourceRoot };
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      return { binary, sourceRoot };
    }
  }

  const manifest = JSON.parse(await readFile(resolve(toolingRoot, "package.json"), "utf8"));
  const name = `@angulus/compiler-${target}`;
  let packageFile;
  try {
    packageFile = require.resolve(`${name}/package.json`);
  } catch (error) {
    if (error.code !== "MODULE_NOT_FOUND") throw error;
    throw new Error(`Missing ${name}@${manifest.version}. Install Angulus with optional dependencies enabled (npm install --include=optional), or install ${name}@${manifest.version} explicitly.`, { cause: error });
  }
  const installed = JSON.parse(await readFile(packageFile, "utf8"));
  if (installed.name !== name || installed.version !== manifest.version) {
    throw new Error(`Angulus CLI version mismatch: expected ${name}@${manifest.version}, found ${installed.name}@${installed.version}. Reinstall matching tooling and CLI packages.`);
  }
  const binary = resolve(dirname(packageFile), "bin", executable);
  await access(binary);
  return { binary };
}
