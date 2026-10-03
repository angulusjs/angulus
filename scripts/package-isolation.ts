import assert from "node:assert/strict";
import { readFile, realpath } from "node:fs/promises";
import { createRequire } from "node:module";
import { isAbsolute, relative, resolve, sep } from "node:path";

interface RuntimeReport {
  getReport(): object;
}

interface HostPlatform {
  platform: string;
  arch: string;
  libc?: string;
}

interface PackageManifest {
  name?: string;
  dependencies?: Record<string, string>;
  peerDependencies?: Record<string, string>;
  optionalDependencies?: Record<string, string>;
  peerDependenciesMeta?: Record<string, { optional?: boolean }>;
  os?: string | string[];
  cpu?: string | string[];
  libc?: string | string[];
}

const hasCode = (error: unknown, ...codes: string[]): error is NodeJS.ErrnoException =>
  error instanceof Error && "code" in error && typeof error.code === "string" && codes.includes(error.code);

export function hostPlatform(platform: string = process.platform, arch: string = process.arch, report: RuntimeReport = { getReport: () => process.report.getReport() }): HostPlatform {
  let libc: string | undefined;
  if (platform === "linux") {
    const details = report.getReport();
    const header = "header" in details && typeof details.header === "object" && details.header !== null
      ? details.header : undefined;
    const glibcVersionRuntime = header && "glibcVersionRuntime" in header ? header.glibcVersionRuntime : undefined;
    const sharedObjects = "sharedObjects" in details && Array.isArray(details.sharedObjects) ? details.sharedObjects : [];
    if (glibcVersionRuntime) libc = "glibc";
    else if (sharedObjects.some(file => /\/(?:ld-musl-|libc\.musl-)/.test(file))) libc = "musl";
  }
  return { platform, arch, libc };
}

function excludes(rules: string | string[] | undefined, current: string | undefined): boolean {
  // Unknown libc must not exempt an otherwise usable dependency from isolation.
  if (!rules || !current) return false;
  const values = Array.isArray(rules) ? rules : [rules];
  if (values.includes(`!${current}`)) return true;
  return values.some(value => !value.startsWith("!")) && !values.includes(current) && !values.includes("any");
}

export async function checkInstalledDependencies(manifestFile: string, nodeModules: string, host: HostPlatform = hostPlatform()): Promise<void> {
  const root = await realpath(nodeModules);
  const visited = new Set();
  function isInside(file: string): boolean {
    const part = relative(root, file);
    return part !== "" && part !== ".." && !part.startsWith(`..${sep}`) && !isAbsolute(part);
  }
  function inside(file: string): void {
    assert.ok(isInside(file), `Resolved outside isolated node_modules: ${file}`);
  }
  async function visit(file: string): Promise<void> {
    file = await realpath(file);
    if (visited.has(file)) return;
    visited.add(file);
    const manifest: PackageManifest = JSON.parse(await readFile(file, "utf8"));
    const require = createRequire(file);
    const dependencies = {
      ...manifest.dependencies,
      ...manifest.peerDependencies,
      ...manifest.optionalDependencies,
    };
    for (const name of Object.keys(dependencies)) {
      const optional = Object.hasOwn(manifest.optionalDependencies ?? {}, name)
        || (!Object.hasOwn(manifest.dependencies ?? {}, name) && manifest.peerDependenciesMeta?.[name]?.optional === true);
      let dependencyManifest: string | undefined;
      let candidate: string | undefined;
      for (const searchPath of require.resolve.paths(name) ?? []) {
        candidate = resolve(searchPath, name, "package.json");
        try {
          dependencyManifest = await realpath(candidate);
          break;
        } catch (error) {
          if (!hasCode(error, "ENOENT", "ENOTDIR")) throw error;
        }
      }
      if (!dependencyManifest) {
        assert.ok(optional, `Missing installed dependency ${name} required by ${manifest.name}`);
        continue;
      }
      if (candidate && isInside(candidate)) inside(dependencyManifest);
      if (optional) {
        const dependency: PackageManifest = JSON.parse(await readFile(dependencyManifest, "utf8"));
        if (excludes(dependency.os, host.platform) || excludes(dependency.cpu, host.arch)
          || (host.platform === "linux" && excludes(dependency.libc, host.libc))) continue;
      }
      inside(dependencyManifest);
      await visit(dependencyManifest);
    }
  }
  await visit(manifestFile);
}
