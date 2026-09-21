import { readFile, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { releaseVersion } from "./publish.mjs";

const workspace = resolve(dirname(fileURLToPath(import.meta.url)), "..");

function updateLock(root) {
  const command = process.env.npm_execpath ? process.execPath : "npm";
  const args = [...(process.env.npm_execpath ? [process.env.npm_execpath] : []), "install", "--package-lock-only", "--ignore-scripts"];
  const result = spawnSync(command, args, { cwd: root, stdio: "inherit" });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`Lockfile update failed (exit ${result.status}). Package manifests are updated; rerun npm install --package-lock-only --ignore-scripts before committing.`);
}

export async function updateVersion(version, { root = workspace, refreshLock = updateLock } = {}) {
  releaseVersion(`v${version}`);
  const directories = ["core", "router", "tooling"];
  const manifests = await Promise.all(directories.map(async directory => {
    const file = resolve(root, "packages", directory, "package.json");
    return { file, manifest: JSON.parse(await readFile(file, "utf8")) };
  }));
  const names = new Set(manifests.map(item => item.manifest.name));
  for (const { file, manifest } of manifests) {
    manifest.version = version;
    for (const section of ["dependencies", "devDependencies", "peerDependencies", "optionalDependencies"]) {
      for (const name of Object.keys(manifest[section] ?? {})) if (names.has(name)) manifest[section][name] = version;
    }
    await writeFile(file, `${JSON.stringify(manifest, null, 2)}\n`);
  }
  await refreshLock(root);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (process.argv.length !== 3) throw new Error("Usage: npm run release:version -- 0.1.1");
  const version = process.argv[2];
  await updateVersion(version);
  console.log(`Angulus packages updated to ${version}; review and commit manifests and package-lock.json before tagging v${version}.`);
}
