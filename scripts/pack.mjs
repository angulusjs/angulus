import { spawnSync } from "node:child_process";
import { chmod, copyFile, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { buildPackages } from "./build-packages.mjs";
import { releaseVersion } from "./publish.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const release = resolve(root, ".angulus/release");
export const targets = [
  ["darwin", "x64", "darwin", "amd64"],
  ["darwin", "arm64", "darwin", "arm64"],
  ["linux", "x64", "linux", "amd64"],
  ["linux", "arm64", "linux", "arm64"],
  ["linux", "arm", "linux", "arm"],
  ["win32", "x64", "windows", "amd64"],
  ["win32", "arm64", "windows", "arm64"],
];

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: root, encoding: "utf8", stdio: "inherit", ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} ${args.join(" ")} failed (${result.status}).\n${result.stderr ?? ""}`);
  return result.stdout;
}

async function copySources(source, destination, accept) {
  await mkdir(destination, { recursive: true });
  for (const entry of await readdir(source, { withFileTypes: true })) {
    if (entry.isDirectory()) await copySources(resolve(source, entry.name), resolve(destination, entry.name), accept);
    else if (entry.isFile() && accept(entry.name)) await copyFile(resolve(source, entry.name), resolve(destination, entry.name));
  }
}

export async function packPackages(args = process.argv.slice(2)) {
  if (args.length && (args.length !== 2 || args[0] !== "--tag")) throw new Error("Usage: npm run packages:pack -- [--tag v0.1.0]");
  const manifests = await Promise.all(["core", "router", "tooling"].map(async name =>
    JSON.parse(await readFile(resolve(root, "packages", name, "package.json"), "utf8"))));
  const version = manifests[0].version;
  releaseVersion(`v${version}`);
  if (manifests.some(manifest => manifest.version !== version)) throw new Error("All Angulus package versions must match.");
  if (manifests[1].dependencies["@angulus/core"] !== version) throw new Error("Router must depend on the exact core release version.");
  if (args.length && releaseVersion(args[1]).version !== version) throw new Error(`Release tag ${args[1]} does not match package version ${version}.`);
  // Reject a mismatched tag before compilation or replacing existing artifacts.
  await buildPackages();
  for (const directory of ["stage", "tarballs"]) {
    await rm(resolve(release, directory), { recursive: true, force: true });
    await mkdir(resolve(release, directory), { recursive: true });
  }
  await rm(resolve(release, "manifest.json"), { force: true });
  const packages = [];
  const compilerDependencies = {};
  async function pack(manifest, kind, source, extra = {}) {
    const directory = resolve(release, "stage", manifest.name.replace("@angulus/", ""));
    await mkdir(directory, { recursive: true });
    await writeFile(resolve(directory, "package.json"), `${JSON.stringify(manifest, null, 2)}\n`);
    await copyFile(resolve(root, "LICENSE"), resolve(directory, "LICENSE"));
    await source(directory);
    const npmArgs = ["pack", "--ignore-scripts", "--json", "--pack-destination", resolve(release, "tarballs")];
    const output = run(process.env.npm_execpath ? process.execPath : "npm",
      process.env.npm_execpath ? [process.env.npm_execpath, ...npmArgs] : npmArgs,
      { cwd: directory, stdio: ["ignore", "pipe", "pipe"] });
    const [result] = JSON.parse(output);
    if (result.name !== manifest.name || result.version !== version || !result.integrity) throw new Error(`Unexpected npm pack result for ${manifest.name}`);
    packages.push({
      name: manifest.name, version, tarball: `tarballs/${result.filename}`, kind, ...extra, integrity: result.integrity,
    });
  }
  for (const [platform, arch, goos, goarch] of targets) {
    const name = `@angulus/compiler-${platform}-${arch}`;
    compilerDependencies[name] = version;
    await pack({
      name, version, description: `Native Angulus compiler for ${platform} ${arch}`,
      license: "MIT", repository: { type: "git", url: "https://github.com/angulusjs/angulus.git", directory: "cmd/angulus-compiler" },
      publishConfig: { access: "public" }, os: [platform], cpu: [arch],
      files: ["bin", "LICENSE", "README.md"],
    }, "compiler", async directory => {
      await mkdir(resolve(directory, "bin"));
      const binary = resolve(directory, "bin", platform === "win32" ? "angulus-compiler.exe" : "angulus-compiler");
      run("go", ["build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", binary, "./cmd/angulus-compiler"], {
        env: { ...process.env, CGO_ENABLED: "0", GOOS: goos, GOARCH: goarch, GOARM: "7", GOFLAGS: "", GOWORK: "off" },
      });
      await chmod(binary, 0o755);
      await writeFile(resolve(directory, "README.md"), `# ${name}\n\nPrecompiled Angulus compiler ${version} for ${platform}/${arch}.\nInstalled automatically as an optional dependency of \`@angulus/tooling\`; Go is not required.\n\nLicensed under MIT; see LICENSE.\n`);
    }, { platform, arch });
  }
  for (const manifest of manifests) {
    const name = manifest.name.replace("@angulus/", "");
    const packaged = name === "tooling" ? { ...manifest, optionalDependencies: compilerDependencies } : manifest;
    await pack(packaged, name, async directory => {
      await copyFile(resolve(root, "packages", name, "README.md"), resolve(directory, "README.md"));
      const source = name === "tooling" ? "src" : "dist";
      await copySources(resolve(root, "packages", name, source), resolve(directory, source),
        file => name === "tooling" ? file.endsWith(".mjs") : file.endsWith(".js") || file.endsWith(".d.ts"));
      if (name === "tooling") {
        await copySources(resolve(root, "packages/tooling/types"), resolve(directory, "types"), file => file.endsWith(".d.mts"));
      }
    });
  }
  const manifestFile = resolve(release, "manifest.json");
  await writeFile(manifestFile, `${JSON.stringify({ version, packages }, null, 2)}\n`);
  console.log(`Packed ${packages.length} packages: ${relative(root, manifestFile)}`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await packPackages();
