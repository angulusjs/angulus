# npm releases

Angulus packages are published from **GitHub Releases** using **npm Trusted
Publishing (OIDC)**. The workflow is
[`.github/workflows/release.yml`](../.github/workflows/release.yml).
No npm token is stored in GitHub secrets.

Creating this workflow does not publish a package or configure npm account
settings. A maintainer must complete the one-time setup below.

## What is published

All packages share one version:

- `@angulus/core`: ESM JavaScript and TypeScript declarations.
- `@angulus/router`: ESM JavaScript and declarations; depends on the same core version.
- `@angulus/tooling`: CLI, Vite plugin and native TypeScript checker integration.
- `@angulus/compiler-darwin-x64`
- `@angulus/compiler-darwin-arm64`
- `@angulus/compiler-linux-x64`
- `@angulus/compiler-linux-arm64`
- `@angulus/compiler-linux-arm`
- `@angulus/compiler-win32-x64`
- `@angulus/compiler-win32-arm64`

The compiler packages contain prebuilt Go executables and declare npm `os`/`cpu`
constraints. The packed tooling manifest pins all seven as optional dependencies;
npm installs the compatible one. **Application developers do not need Go.**
The compiler and tooling versions must match. Missing optional dependencies or
unsupported platforms produce explicit errors rather than compiling Go inside
the user's application. Source-checkout development still requires Go.

The Go binaries are cross-compiled without cgo. This is not a claim that all
seven platforms have complete browser-test coverage: CI runs the framework tests
on Linux/macOS and the browser workflow on Linux. Native npm installation smoke
tests run on the platform executing the packaging check.

## One-time setup

1. Own or create the **`angulus` npm scope**, and grant the maintainer publish
   rights. The GitHub organization `angulusjs` does not grant rights to the npm
   scope automatically.
2. Create the GitHub Environment **`npm`** in `angulusjs/angulus`.
   Configure required reviewers and restrict deployment to protected release
   tags. Protect `v*` tags against unreviewed creation, changes and deletion.
3. Ensure the package repository is public for public npm provenance.
4. **Bootstrap the packages once, manually.** npm Trusted Publisher configuration
   belongs to existing packages. For a new package, first publish its actual
   tested release from an authorized maintainer machine using npm login/2FA;
   do not create placeholder packages or introduce an automation token.
5. For **each of the ten packages**, open npm package settings → Trusted
   Publisher and add GitHub Actions:

   | Field | Value |
   | --- | --- |
   | Organization/user | `angulusjs` |
   | Repository | `angulus` |
   | Workflow filename | `release.yml` |
   | Environment | `npm` |

   The workflow filename is not the display name or `.github/workflows/...`.
6. After a successful OIDC release, npm recommends enabling
   **Require two-factor authentication and disallow tokens** and revoking
   obsolete publish tokens. No `NPM_TOKEN` or `NODE_AUTH_TOKEN` secret is needed.

Reference: [npm Trusted Publishers](https://docs.npmjs.com/trusted-publishers/).
The publisher job installs pinned npm **12.0.2** with Node.js **22.22.3**.
Trusted Publishing requires a sufficiently recent npm CLI; the npm 10 bundled
with some Node.js 22 installations is not enough.

### Manual first publication

Run this only when intentionally publishing the first real release. The setup
task itself does not execute these commands:

```sh
npm ci
npm test
npm run typecheck
npm run build
npx playwright install chromium
npm run test:browser
npm run packages:pack -- --tag v0.1.0
npm run packages:smoke
npm run release:publish -- --tag v0.1.0 --dry-run

# Sign in interactively; do not put credentials in source or command arguments.
npm login
npm run release:publish -- --tag v0.1.0 --bootstrap
```

The bootstrap command validates all tarball hashes and registry versions first,
then publishes real artifacts in dependency order. npm may request 2FA.
It is forbidden in GitHub Actions; it is not an automatic token fallback.
It does not claim GitHub provenance for a local build.

After publishing `0.1.0`, configure all ten Trusted Publishers, and release
`0.1.1` (or another new version) through GitHub. Do not attempt to overwrite
`0.1.0`.

## Normal release

1. Start from a reviewed working tree with passing CI.
2. Update the shared version and internal dependencies:

   ```sh
   npm run release:version -- 0.1.1
   npm ci
   npm test
   npm run typecheck
   npm run packages:pack -- --tag v0.1.1
   npm run packages:smoke
   npm run release:publish -- --tag v0.1.1 --dry-run
   ```

   Review and commit the three package manifests and lockfile. The version helper
   does not commit, tag, publish, or push.
3. Create and push the tag **`v0.1.1`** pointing at that reviewed commit.
4. Create and **publish a GitHub Release** for that exact tag.
   Merely pushing a tag or saving a release draft does not publish npm packages.
5. The preparation job checks out the tag, installs the lockfile, runs tests and
   the browser workflow, builds all compiler targets, packs the packages, and
   installs the tarballs into a dedicated application under
   `.angulus/release/smoke/project` with its own `node_modules`. That smoke test
   rejects workspace symlinks and dependency resolution outside the installation,
   checks actual imports and CLI checks/tests/build, and verifies operation
   without Go. The dependency audit skips optional packages whose `os`, `cpu`,
   or Linux `libc` metadata excludes the host (for example, musl binaries on a
   glibc runner). Compatible optional packages still cannot resolve from the
   ancestor workspace; unknown libc is checked conservatively, not skipped.
6. Review the `npm` environment approval. The publisher downloads the verified
   tarballs from the same run, verifies their SHA-512 integrity and tag/version
   agreement, preflights the npm registry, and publishes with OIDC and provenance.
   It does not rebuild artifacts or install project dependencies in the
   credential-bearing job.

Preparation has only `contents: read`. Only the protected publisher job receives
`id-token: write`. Release jobs are serialized and not cancelled mid-publication.
The workflow is restricted to `angulusjs/angulus`; forks do not publish.

### Prereleases

Use a semantic prerelease version such as `0.2.0-rc.1` and tag
`v0.2.0-rc.1`. Mark its GitHub Release as **prerelease**.

- Stable versions publish with npm dist-tag **`latest`**.
- Prerelease versions publish with **`next`** and do not replace `latest`.
- The GitHub prerelease flag must match the tag.
- Leading-zero versions, non-semantic tags and build-metadata suffixes are
  rejected.

Install a prerelease with matching versions for all three public packages.

## Failures and reruns

There is no atomic multi-package publish operation on npm. Before the first
publish, the script checks **every** package for conflicting existing versions,
missing initial bootstrap, registry failures and local artifact corruption.

If the publisher fails midway, use **Re-run failed jobs** on the same workflow
run while its verified artifacts are retained (14 days). It skips already
published versions only when registry integrity matches the verified tarball
exactly. It never silently ignores authentication/network errors, overwrites a
version, or moves an existing release's dist-tag on a rerun.
A newly published stable release also cannot move `latest` backwards to an
older stable version.

If the existing version has different bytes, investigate and create a new
version. Do not delete/recreate a tag or unpublish packages to work around it.
If Trusted Publishing authentication fails, check the npm publisher's owner,
repository, workflow filename, environment and publish permissions. The CLI's
registry preflight is not a substitute for npm authorization.

## Local checks

```sh
npm run packages:pack
npm run packages:smoke
npm run release:publish -- --tag v0.1.0 --dry-run
```

The dry run validates the manifest and tarballs and prints the ordered plan.
It makes no registry writes and does not require npm login or an OIDC token.
It does **not** prove that npm permissions/Trusted Publisher configuration are
correct. Only an authorized real release can verify that external setup.

Generated staging directories, tarballs and the release manifest are under
ignored `.angulus/release/`. Keep them out of source control.
