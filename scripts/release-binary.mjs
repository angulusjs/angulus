import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// Test-only adapter: validation and integrity policy live in the native tool.
export async function validateArtifacts(manifestFile, tag) {
  const binary = resolve(root, ".angulus", "bin", process.platform === "win32" ? "angulus-release.exe" : "angulus-release");
  const result = spawnSync(binary, ["validate", "--manifest", manifestFile, "--tag", tag], { encoding: "utf8" });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(result.stderr.trim());
  return JSON.parse(result.stdout);
}
