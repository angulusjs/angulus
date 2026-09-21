import { createHash } from "node:crypto";
import { readFile, realpath } from "node:fs/promises";
import { spawn } from "node:child_process";
import { dirname, isAbsolute, relative, resolve, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const workspace = resolve(dirname(fileURLToPath(import.meta.url)), "..");
export const registry = "https://registry.npmjs.org";
export const packageNames = [
  ...["darwin-x64", "darwin-arm64", "linux-x64", "linux-arm64", "linux-arm", "win32-x64", "win32-arm64"].map(target => `@angulus/compiler-${target}`),
  "@angulus/core", "@angulus/router", "@angulus/tooling",
];

export function releaseVersion(tag) {
  const match = /^v((0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?)$/.exec(tag ?? "");
  if (!match || match[5]?.split(".").some(part => /^\d+$/.test(part) && part.length > 1 && part.startsWith("0"))) {
    throw new Error("Release tag must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-prerelease (no leading zeroes or build metadata).");
  }
  return { version: match[1], prerelease: Boolean(match[5]), distTag: match[5] ? "next" : "latest" };
}

export async function validateArtifacts(manifestFile, tag, { read = readFile, canonical = realpath } = {}) {
  const release = releaseVersion(tag);
  const manifest = JSON.parse(await read(manifestFile, "utf8"));
  if (manifest.version !== release.version || !Array.isArray(manifest.packages)) throw new Error("Release tag and packed manifest version must match.");
  const names = manifest.packages.map(item => item.name);
  if (names.length !== packageNames.length || new Set(names).size !== names.length || packageNames.some(name => !names.includes(name))) {
    throw new Error("Release must contain exactly seven platform compiler packages plus core, router and tooling.");
  }
  const root = await canonical(dirname(manifestFile));
  const result = [];
  for (const name of packageNames) {
    const item = manifest.packages.find(candidate => candidate.name === name);
    if (item.version !== release.version) throw new Error(`Version mismatch for ${name}.`);
    if (typeof item.tarball !== "string" || isAbsolute(item.tarball) || !item.tarball.endsWith(".tgz")) throw new Error(`Invalid tarball for ${name}.`);
    const tarball = await canonical(resolve(root, item.tarball));
    const path = relative(root, tarball);
    if (!path || path === ".." || path.startsWith(`..${sep}`) || isAbsolute(path)) throw new Error(`Tarball escapes release directory: ${name}.`);
    const bytes = await read(tarball);
    const integrity = `sha512-${createHash("sha512").update(bytes).digest("base64")}`;
    if (item.integrity !== integrity) throw new Error(`Tarball integrity mismatch for ${name}.`);
    result.push({ ...item, tarball, integrity });
  }
  return { ...release, packages: result };
}

export async function publicationPlan(artifacts, { bootstrap = false, fetchMetadata = fetch } = {}) {
  const plan = [];
  for (const item of artifacts.packages) {
    const response = await fetchMetadata(`${registry}/${encodeURIComponent(item.name)}`, {
      headers: { accept: "application/vnd.npm.install-v1+json" },
      signal: AbortSignal.timeout(30_000),
    });
    if (response.status === 404) {
      if (!bootstrap) throw new Error(`${item.name} does not exist on npm. A maintainer must bootstrap the package and configure its Trusted Publisher before an OIDC release.`);
      plan.push({ ...item, action: "publish" });
      continue;
    }
    if (!response.ok) throw new Error(`npm registry preflight failed for ${item.name}: HTTP ${response.status}. No publish attempted.`);
    const metadata = await response.json();
    if (metadata.name !== item.name || !metadata.versions || typeof metadata.versions !== "object") throw new Error(`Invalid npm metadata for ${item.name}.`);
    const published = metadata.versions[item.version];
    if (published) {
      if (published.dist?.integrity !== item.integrity) throw new Error(`${item.name}@${item.version} already exists with different bytes. Bump the version; npm releases are immutable.`);
      plan.push({ ...item, action: "skip" });
      continue;
    }
    const latest = metadata["dist-tags"]?.latest;
    if (artifacts.distTag === "latest" && latest) {
      const latestRelease = releaseVersion(`v${latest}`);
      if (!latestRelease.prerelease) {
        const current = latestRelease.version.split(".").map(BigInt);
        const next = artifacts.version.split(".").map(BigInt);
        const difference = next.findIndex((part, index) => part !== current[index]);
        if (difference < 0 || next[difference] < current[difference]) {
          throw new Error(`Refusing to move ${item.name}'s latest tag backwards from ${latest} to ${item.version}.`);
        }
      }
    }
    plan.push({ ...item, action: "publish" });
  }
  return plan;
}

function npm(args, options = {}) {
  return new Promise((resolveExit, reject) => {
    if (process.platform === "win32" && !process.env.npm_execpath) {
      reject(new Error("Run publishing through npm run release:publish on Windows."));
      return;
    }
    const executable = process.env.npm_execpath ? process.execPath : "npm";
    const commandArgs = process.env.npm_execpath ? [process.env.npm_execpath, ...args] : args;
    const child = spawn(executable, commandArgs, { stdio: "inherit", ...options });
    child.once("error", reject);
    child.once("close", (code, signal) => code === 0 ? resolveExit() : reject(new Error(`npm ${args[0]} failed (exit ${code}, signal ${signal ?? "none"}).`)));
  });
}

export async function publishPackages(artifacts, plan, { bootstrap = false, execute = npm } = {}) {
  for (const item of plan) {
    if (item.action === "skip") {
      console.log(`Already published with identical integrity: ${item.name}@${item.version}`);
      continue;
    }
    await execute([
      "publish", item.tarball, "--access", "public", "--tag", artifacts.distTag,
      "--ignore-scripts", "--registry", registry,
      ...(!bootstrap ? ["--provenance"] : []),
    ]);
  }
}

export async function main(args = process.argv.slice(2)) {
  let tag;
  let dryRun = false;
  let bootstrap = false;
  for (let index = 0; index < args.length; index++) {
    if (args[index] === "--tag") tag = args[++index];
    else if (args[index] === "--dry-run") dryRun = true;
    else if (args[index] === "--bootstrap") bootstrap = true;
    else throw new Error(`Unknown publish argument: ${args[index]}`);
  }
  const artifacts = await validateArtifacts(resolve(workspace, ".angulus/release/manifest.json"), tag);
  if (process.env.RELEASE_PRERELEASE !== undefined && String(artifacts.prerelease) !== process.env.RELEASE_PRERELEASE) {
    throw new Error("GitHub release prerelease flag must match the semantic version tag.");
  }
  if (dryRun) {
    console.log(JSON.stringify({ ...artifacts, dryRun: true }, null, 2));
    return;
  }
  if (bootstrap && process.env.GITHUB_ACTIONS) throw new Error("Initial bootstrap is a manual maintainer action, not a token-based CI fallback.");
  if (!bootstrap && (process.env.GITHUB_ACTIONS !== "true" || process.env.GITHUB_REPOSITORY !== "angulusjs/angulus" ||
      !process.env.ACTIONS_ID_TOKEN_REQUEST_URL || !process.env.ACTIONS_ID_TOKEN_REQUEST_TOKEN)) {
    throw new Error("Publishing requires the angulusjs/angulus GitHub Actions Trusted Publisher with id-token: write. Use --dry-run to validate locally.");
  }
  // Preflight every package before the first write, so authentication/bootstrap or
  // conflicting-version failures do not intentionally leave a partial release.
  const plan = await publicationPlan(artifacts, { bootstrap });
  await publishPackages(artifacts, plan, { bootstrap });
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
