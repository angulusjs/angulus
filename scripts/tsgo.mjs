import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(import.meta.url);

export function tsgoBinary(resolver = require) {
  const platformPackage = `@typescript/native-preview-${process.platform}-${process.arch}`;
  const packageFile = resolver.resolve(`${platformPackage}/package.json`);
  const executable = process.platform === "win32" ? "tsgo.exe" : "tsgo";
  return resolve(dirname(packageFile), "lib", executable);
}

export function runTsgo(args, { cwd = root } = {}) {
  const result = spawnSync(tsgoBinary(), args, { cwd, stdio: "inherit" });
  if (result.error) throw result.error;
  return result.status ?? 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  process.exitCode = runTsgo(process.argv.slice(2));
}
