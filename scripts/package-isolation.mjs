import assert from "node:assert/strict";
import { readFile, realpath } from "node:fs/promises";
import { createRequire } from "node:module";
import { isAbsolute, relative, resolve, sep } from "node:path";

export function hostPlatform(platform = process.platform, arch = process.arch, report = process.report) {
  let libc;
  if (platform === "linux") {
    const { header, sharedObjects } = report.getReport();
    if (header.glibcVersionRuntime) libc = "glibc";
    else if (sharedObjects.some(file => /\/(?:ld-musl-|libc\.musl-)/.test(file))) libc = "musl";
  }
  return { platform, arch, libc };
}

function excludes(rules, current) {
  // Unknown libc must not exempt an otherwise usable dependency from isolation.
  if (!rules || !current) return false;
  const values = Array.isArray(rules) ? rules : [rules];
  if (values.includes(`!${current}`)) return true;
  return values.some(value => !value.startsWith("!")) && !values.includes(current) && !values.includes("any");
}

export async function checkInstalledDependencies(manifestFile, nodeModules, host = hostPlatform()) {
  const root = await realpath(nodeModules);
  const visited = new Set();
  function isInside(file) {
    const part = relative(root, file);
    return part && part !== ".." && !part.startsWith(`..${sep}`) && !isAbsolute(part);
  }
  function inside(file) {
    assert.ok(isInside(file), `Resolved outside isolated node_modules: ${file}`);
  }
  async function visit(file) {
    file = await realpath(file);
    if (visited.has(file)) return;
    visited.add(file);
    const manifest = JSON.parse(await readFile(file, "utf8"));
    const require = createRequire(file);
    const dependencies = {
      ...manifest.dependencies,
      ...manifest.peerDependencies,
      ...manifest.optionalDependencies,
    };
    for (const name of Object.keys(dependencies)) {
      const optional = Object.hasOwn(manifest.optionalDependencies ?? {}, name)
        || (!Object.hasOwn(manifest.dependencies ?? {}, name) && manifest.peerDependenciesMeta?.[name]?.optional === true);
      let dependencyManifest;
      let candidate;
      for (const searchPath of require.resolve.paths(name) ?? []) {
        candidate = resolve(searchPath, name, "package.json");
        try {
          dependencyManifest = await realpath(candidate);
          break;
        } catch (error) {
          if (error.code !== "ENOENT" && error.code !== "ENOTDIR") throw error;
        }
      }
      if (!dependencyManifest) {
        assert.ok(optional, `Missing installed dependency ${name} required by ${manifest.name}`);
        continue;
      }
      if (isInside(candidate)) inside(dependencyManifest);
      if (optional) {
        const dependency = JSON.parse(await readFile(dependencyManifest, "utf8"));
        if (excludes(dependency.os, host.platform) || excludes(dependency.cpu, host.arch)
          || (host.platform === "linux" && excludes(dependency.libc, host.libc))) continue;
      }
      inside(dependencyManifest);
      await visit(dependencyManifest);
    }
  }
  await visit(manifestFile);
}
