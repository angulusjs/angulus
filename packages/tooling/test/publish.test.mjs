import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { packageNames, publicationPlan, publishPackages, releaseVersion, validateArtifacts } from "../../../scripts/publish.mjs";
import { updateVersion } from "../../../scripts/version.mjs";

test("release versions enforce exact semantic tags and separate prerelease dist-tags", () => {
  assert.deepEqual(releaseVersion("v0.1.0"), { version: "0.1.0", prerelease: false, distTag: "latest" });
  assert.deepEqual(releaseVersion("v1.0.0-rc.1"), { version: "1.0.0-rc.1", prerelease: true, distTag: "next" });
  for (const tag of ["0.1.0", "v01.2.3", "v1.2.3-rc.01", "v1.2.3+build", "v1.2", "latest", "v1.2.3;exit"]) {
    assert.throws(() => releaseVersion(tag), /Release tag/);
  }
});

async function artifacts(t) {
  const directory = await mkdtemp(resolve(tmpdir(), "angulus-release-test-"));
  t.after(() => rm(directory, { recursive: true }));
  await mkdir(resolve(directory, "tarballs"));
  const manifest = { version: "0.1.0", packages: [] };
  for (const [index, name] of packageNames.entries()) {
    const bytes = Buffer.from(`fixture:${name}`);
    const tarball = `tarballs/${index}.tgz`;
    manifest.packages.push({
      name, version: "0.1.0", tarball,
      integrity: `sha512-${createHash("sha512").update(bytes).digest("base64")}`,
    });
    await writeFile(resolve(directory, tarball), bytes);
  }
  const file = resolve(directory, "manifest.json");
  const save = () => writeFile(file, JSON.stringify(manifest));
  await save();
  return { manifest, file, save, directory };
}

test("all tarball versions and bytes are validated before registry access", async t => {
  const { manifest, file, save, directory } = await artifacts(t);
  const validated = await validateArtifacts(file, "v0.1.0");
  assert.deepEqual(validated.packages.map(item => item.name), packageNames);
  await assert.rejects(validateArtifacts(file, "v0.2.0"), /manifest version/);
  await writeFile(resolve(directory, manifest.packages[0].tarball), "different bytes");
  await assert.rejects(validateArtifacts(file, "v0.1.0"), /integrity mismatch/);
  manifest.packages[0].version = "0.2.0";
  await save();
  await assert.rejects(validateArtifacts(file, "v0.1.0"), /Version mismatch/);
});

test("release rejects missing, duplicate and escaping tarballs", async t => {
  const { manifest, file, save } = await artifacts(t);
  const original = manifest.packages[0];
  manifest.packages[0] = { ...manifest.packages[1] };
  await save();
  await assert.rejects(validateArtifacts(file, "v0.1.0"), /exactly seven/);
  manifest.packages[0] = { ...original, tarball: "/tmp/escaped.tgz" };
  await save();
  await assert.rejects(validateArtifacts(file, "v0.1.0"), /Invalid tarball/);
  manifest.packages.pop();
  await save();
  await assert.rejects(validateArtifacts(file, "v0.1.0"), /exactly seven/);
});

const sample = {
  version: "0.1.0",
  distTag: "latest",
  packages: packageNames.map(name => ({ name, version: "0.1.0", integrity: `sha512-${name}`, tarball: `/release/${name.slice(9)}.tgz` })),
};
const metadata = (item, published) => new Response(JSON.stringify({
  name: item.name,
  versions: published ? { [item.version]: { dist: { integrity: published } } } : {},
}));

test("OIDC refuses new package names but manual bootstrap permits them", async () => {
  const fetchMetadata = async () => new Response("not found", { status: 404 });
  await assert.rejects(publicationPlan(sample, { fetchMetadata }), /bootstrap.*Trusted Publisher/);
  const plan = await publicationPlan(sample, { fetchMetadata, bootstrap: true });
  assert.ok(plan.every(item => item.action === "publish"));
});

test("registry failures and conflicting existing releases are never silently skipped", async () => {
  for (const status of [401, 403, 429, 500]) {
    await assert.rejects(publicationPlan(sample, { fetchMetadata: async () => new Response("", { status }) }), new RegExp(`HTTP ${status}`));
  }
  await assert.rejects(publicationPlan(sample, { fetchMetadata: async () => metadata(sample.packages[0], "sha512-conflict") }), /different bytes/);
  await assert.rejects(publicationPlan(sample, { fetchMetadata: async () => new Response("{}") }), /Invalid npm metadata/);
});

test("partial releases skip identical published versions and stage remaining artifacts in dependency order", async () => {
  let index = 0;
  const plan = await publicationPlan(sample, {
    fetchMetadata: async () => {
      const item = sample.packages[index++];
      return metadata(item, index <= 2 ? item.integrity : undefined);
    },
  });
  const calls = [];
  await publishPackages(sample, plan, { execute: async args => { calls.push(args); } });
  assert.deepEqual(calls, sample.packages.slice(2).map(item => [
    "stage", "publish", item.tarball, "--access", "public", "--tag", "latest",
    "--ignore-scripts", "--registry", "https://registry.npmjs.org", "--provenance",
  ]));
});

test("manual bootstrap stages prereleases with next and without CI provenance", async () => {
  const calls = [];
  await publishPackages({ ...sample, distTag: "next" }, [{ ...sample.packages[0], action: "publish" }], {
    bootstrap: true,
    execute: async args => { calls.push(args); },
  });
  assert.deepEqual(calls, [[
    "stage", "publish", sample.packages[0].tarball, "--access", "public", "--tag", "next",
    "--ignore-scripts", "--registry", "https://registry.npmjs.org",
  ]]);
});

test("a late stable release cannot roll back the registry's latest version", async () => {
  await assert.rejects(publicationPlan(sample, {
    fetchMetadata: async () => new Response(JSON.stringify({
      name: sample.packages[0].name,
      versions: {},
      "dist-tags": { latest: "0.2.0" },
    })),
  }), /latest tag backwards/);
});

test("publishing stops at the first npm failure", async () => {
  let attempted = 0;
  await assert.rejects(publishPackages(sample, sample.packages.map(item => ({ ...item, action: "publish" })), {
    execute: async () => { attempted++; throw new Error("npm authentication failed"); },
  }), /authentication failed/);
  assert.equal(attempted, 1);
});

test("version helper keeps all package versions and internal contracts aligned", async t => {
  const root = await mkdtemp(resolve(tmpdir(), "angulus-version-test-"));
  t.after(() => rm(root, { recursive: true }));
  for (const name of ["core", "router", "tooling"]) {
    const directory = resolve(root, "packages", name);
    await mkdir(directory, { recursive: true });
    await writeFile(resolve(directory, "package.json"), JSON.stringify({
      name: `@angulus/${name}`, version: "0.1.0",
      dependencies: { "@angulus/core": "0.1.0", vite: "8.3.0" },
      peerDependencies: { "@angulus/router": "0.1.0" },
    }));
  }
  let locks = 0;
  await updateVersion("0.2.0-rc.1", { root, refreshLock: async directory => { assert.equal(directory, root); locks++; } });
  for (const name of ["core", "router", "tooling"]) {
    const manifest = JSON.parse(await readFile(resolve(root, "packages", name, "package.json"), "utf8"));
    assert.equal(manifest.version, "0.2.0-rc.1");
    assert.equal(manifest.dependencies["@angulus/core"], manifest.version);
    assert.equal(manifest.peerDependencies["@angulus/router"], manifest.version);
    assert.equal(manifest.dependencies.vite, "8.3.0");
  }
  assert.equal(locks, 1);
  await assert.rejects(updateVersion("invalid", { root }), /Release tag/);
});
