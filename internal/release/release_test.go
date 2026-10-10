package release

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	root, err := FindRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".angulus", fmt.Sprintf("release-test-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func fails(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("expected error containing %q, got %v", text, err)
	}
}

func TestReleaseVersion(t *testing.T) {
	for _, test := range []struct {
		tag, version, dist string
		pre                bool
	}{
		{"v0.1.0", "0.1.0", "latest", false}, {"v1.0.0-rc.1", "1.0.0-rc.1", "next", true},
		{"v999999999999999999999.2.3", "999999999999999999999.2.3", "latest", false},
	} {
		value, err := ReleaseVersion(test.tag)
		must(t, err)
		if value.Version != test.version || value.DistTag != test.dist || value.Prerelease != test.pre {
			t.Fatal(value)
		}
	}
	for _, tag := range []string{"0.1.0", "v01.2.3", "v1.2.3-rc.01", "v1.2.3+build", "v1.2", "latest", "v1.2.3;exit", "v1.2.3-00"} {
		_, err := ReleaseVersion(tag)
		fails(t, err, "Release tag")
	}
}

func fixtureArtifacts(t *testing.T) (string, Manifest) {
	t.Helper()
	dir := fixtureDir(t)
	must(t, os.Mkdir(filepath.Join(dir, "tarballs"), 0755))
	manifest := Manifest{Version: "0.1.0"}
	for i, name := range PackageNames() {
		data := []byte("fixture:" + name)
		hash := sha512.Sum512(data)
		path := fmt.Sprintf("tarballs/%d.tgz", i)
		must(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), data, 0644))
		manifest.Packages = append(manifest.Packages, Artifact{Name: name, Version: "0.1.0", Tarball: path, Integrity: "sha512-" + base64.StdEncoding.EncodeToString(hash[:])})
	}
	file := filepath.Join(dir, "manifest.json")
	must(t, writeJSON(file, manifest))
	return file, manifest
}

func TestValidateArtifacts(t *testing.T) {
	file, manifest := fixtureArtifacts(t)
	value, err := ValidateArtifacts(file, "v0.1.0")
	must(t, err)
	var names []string
	for _, item := range value.Packages {
		names = append(names, item.Name)
		if !filepath.IsAbs(item.Tarball) {
			t.Fatal(item)
		}
	}
	if !reflect.DeepEqual(names, PackageNames()) {
		t.Fatal(names)
	}
	_, err = ValidateArtifacts(file, "v0.2.0")
	fails(t, err, "manifest version")
	for _, name := range []string{"@angulus/forms", "@angulus/http", PackageNames()[0]} {
		for i := range manifest.Packages {
			if manifest.Packages[i].Name != name {
				continue
			}
			manifest.Packages[i].Version = "0.2.0"
			must(t, writeJSON(file, manifest))
			_, err = ValidateArtifacts(file, "v0.1.0")
			fails(t, err, "Version mismatch for "+name)
			manifest.Packages[i].Version = "0.1.0"
		}
	}
	must(t, writeJSON(file, manifest))
	must(t, os.WriteFile(value.Packages[0].Tarball, []byte("corrupted"), 0644))
	_, err = ValidateArtifacts(file, "v0.1.0")
	fails(t, err, "integrity mismatch")
}

func TestValidateArtifactSetAndPaths(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "unknown", "absolute", "escape", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			file, manifest := fixtureArtifacts(t)
			text := "exactly seven"
			switch mode {
			case "missing":
				manifest.Packages = manifest.Packages[:11]
			case "duplicate":
				manifest.Packages[0] = manifest.Packages[1]
			case "unknown":
				manifest.Packages[0].Name = "@angulus/unknown"
			case "absolute":
				manifest.Packages[0].Tarball = filepath.Join(filepath.Dir(file), "other.tgz")
				text = "Invalid tarball"
			case "escape":
				outside := filepath.Join(fixtureDir(t), "outside.tgz")
				must(t, os.WriteFile(outside, []byte("outside"), 0644))
				rel, err := filepath.Rel(filepath.Dir(file), outside)
				must(t, err)
				manifest.Packages[0].Tarball = rel
				text = "escapes release"
			case "symlink":
				outside := filepath.Join(fixtureDir(t), "outside.tgz")
				must(t, os.WriteFile(outside, []byte("outside"), 0644))
				link := filepath.Join(filepath.Dir(file), "linked.tgz")
				if err := os.Symlink(outside, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				manifest.Packages[0].Tarball = "linked.tgz"
				text = "escapes release"
			}
			must(t, writeJSON(file, manifest))
			_, err := ValidateArtifacts(file, "v0.1.0")
			fails(t, err, text)
		})
	}
}

type clientFunc func(*http.Request) (*http.Response, error)

func (f clientFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }
func response(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data))}
}

func sampleArtifacts() Artifacts {
	result := Artifacts{Version: Version{Version: "0.1.0", DistTag: "latest"}}
	for _, name := range PackageNames() {
		result.Packages = append(result.Packages, Artifact{Name: name, Version: "0.1.0", Integrity: "sha512-" + name, Tarball: name + ".tgz"})
	}
	return result
}

func TestPublicationPreflight(t *testing.T) {
	sample := sampleArtifacts()
	ctx := context.Background()
	missing := clientFunc(func(*http.Request) (*http.Response, error) { return response(404, nil), nil })
	_, err := PublicationPlan(ctx, sample, false, missing)
	fails(t, err, "bootstrap")
	plan, err := PublicationPlan(ctx, sample, true, missing)
	must(t, err)
	for _, item := range plan {
		if item.Action != "publish" {
			t.Fatal(item)
		}
	}
	for _, status := range []int{401, 403, 429, 500} {
		_, err := PublicationPlan(ctx, sample, false, clientFunc(func(*http.Request) (*http.Response, error) { return response(status, nil), nil }))
		fails(t, err, fmt.Sprintf("HTTP %d", status))
	}
	for _, mode := range []string{"conflict", "invalid", "rollback", "network"} {
		text := map[string]string{"conflict": "different bytes", "invalid": "Invalid npm metadata", "rollback": "latest tag backwards", "network": "No publish attempted"}[mode]
		_, err := PublicationPlan(ctx, sample, false, clientFunc(func(req *http.Request) (*http.Response, error) {
			if mode == "network" {
				return nil, fmt.Errorf("network failure")
			}
			item := sample.Packages[0]
			metadata := map[string]any{"name": item.Name, "versions": map[string]any{}}
			if mode == "invalid" {
				metadata = map[string]any{}
			}
			if mode == "conflict" {
				metadata["versions"] = map[string]any{item.Version: map[string]any{"dist": map[string]any{"integrity": "different"}}}
			}
			if mode == "rollback" {
				metadata["dist-tags"] = map[string]string{"latest": "0.2.0"}
			}
			return response(200, metadata), nil
		}))
		fails(t, err, text)
	}
}

func TestPublicationOrderAndArguments(t *testing.T) {
	sample := sampleArtifacts()
	index := 0
	plan, err := PublicationPlan(context.Background(), sample, false, clientFunc(func(req *http.Request) (*http.Response, error) {
		item := sample.Packages[index]
		index++
		if req.Header.Get("Accept") != "application/vnd.npm.install-v1+json" {
			t.Fatal(req.Header)
		}
		versions := map[string]any{}
		if index <= 2 {
			versions[item.Version] = map[string]any{"dist": map[string]any{"integrity": item.Integrity}}
		}
		return response(200, map[string]any{"name": item.Name, "versions": versions}), nil
	}))
	must(t, err)
	t.Setenv("npm_execpath", "npm-cli.js")
	t.Setenv("npm_node_execpath", "node")
	var calls [][]string
	tool := New(".")
	tool.Out = io.Discard
	tool.Run = func(ctx context.Context, root, command string, args, env []string, capture bool) ([]byte, error) {
		calls = append(calls, args[1:])
		return nil, nil
	}
	must(t, tool.PublishPackages(context.Background(), sample, plan, false))
	if len(calls) != 10 {
		t.Fatal(calls)
	}
	for i, args := range calls {
		expected := []string{"stage", "publish", sample.Packages[i+2].Tarball, "--access", "public", "--tag", "latest", "--ignore-scripts", "--registry", Registry, "--provenance"}
		if !reflect.DeepEqual(args, expected) {
			t.Fatalf("%v != %v", args, expected)
		}
	}
	sample.DistTag = "next"
	calls = nil
	must(t, tool.PublishPackages(context.Background(), sample, plan[2:3], true))
	if calls[0][6] != "next" || strings.Contains(strings.Join(calls[0], " "), "provenance") {
		t.Fatal(calls)
	}
	attempted := 0
	tool.Run = func(context.Context, string, string, []string, []string, bool) ([]byte, error) {
		attempted++
		return nil, fmt.Errorf("authentication failed")
	}
	fails(t, tool.PublishPackages(context.Background(), sample, plan, false), "authentication failed")
	if attempted != 1 {
		t.Fatal(attempted)
	}
}

func workspaceFixture(t *testing.T) (string, []PackageManifest) {
	dir := fixtureDir(t)
	var manifests []PackageManifest
	for _, name := range WorkspacePackages {
		pkg := filepath.Join(dir, "packages", name)
		must(t, os.MkdirAll(pkg, 0755))
		manifest := PackageManifest{"name": "@angulus/" + name, "version": "0.1.0"}
		for _, section := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
			manifest[section] = map[string]any{"@angulus/core": "0.1.0", "@angulus/router": "0.1.0", "@angulus/forms": "0.1.0", "external": "1.2.3"}
		}
		must(t, writeJSON(filepath.Join(pkg, "package.json"), manifest))
		manifests = append(manifests, manifest)
	}
	return dir, manifests
}

func TestManifestVersionsAndPrebuildValidation(t *testing.T) {
	root, manifests := workspaceFixture(t)
	version, err := ValidatePackageManifests(manifests)
	must(t, err)
	if version != "0.1.0" {
		t.Fatal(version)
	}
	for _, name := range []string{"router", "forms", "http"} {
		for _, manifest := range manifests {
			if manifest["name"] != "@angulus/"+name {
				continue
			}
			manifest["version"] = "0.2.0"
			_, err := ValidatePackageManifests(manifests)
			fails(t, err, "versions must match")
			manifest["version"] = "0.1.0"
			deps := manifest["dependencies"].(map[string]any)
			for _, value := range []any{"^0.1.0", "~0.1.0", "*", "0.0.0", nil} {
				deps["@angulus/core"] = value
				_, err := ValidatePackageManifests(manifests)
				fails(t, err, name+" must depend on the exact")
			}
			deps["@angulus/core"] = "0.1.0"
		}
	}
	tool := New(root)
	tool.Run = func(context.Context, string, string, []string, []string, bool) ([]byte, error) {
		t.Fatal("preflight invoked build")
		return nil, nil
	}
	fails(t, tool.Pack(context.Background(), "0.1.0"), "Release tag")
	fails(t, tool.Pack(context.Background(), "v999.0.0"), "does not match package version")
}

func TestVersionUpdatesInternalContractsAndLock(t *testing.T) {
	root, _ := workspaceFixture(t)
	t.Setenv("npm_execpath", "npm-cli.js")
	t.Setenv("npm_node_execpath", "node")
	calls := 0
	tool := New(root)
	tool.Out = io.Discard
	tool.Run = func(ctx context.Context, cwd, command string, args, env []string, capture bool) ([]byte, error) {
		calls++
		if cwd != root || !reflect.DeepEqual(args, []string{"npm-cli.js", "install", "--package-lock-only", "--ignore-scripts"}) {
			t.Fatal(cwd, args)
		}
		return nil, nil
	}
	must(t, tool.UpdateVersion(context.Background(), "0.2.0-rc.1"))
	manifests, err := tool.manifests()
	must(t, err)
	for _, manifest := range manifests {
		if manifest["version"] != "0.2.0-rc.1" {
			t.Fatal(manifest)
		}
		for _, section := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
			deps := manifest[section].(map[string]any)
			if deps["@angulus/core"] != manifest["version"] || deps["@angulus/router"] != manifest["version"] || deps["@angulus/forms"] != manifest["version"] || deps["external"] != "1.2.3" {
				t.Fatal(deps)
			}
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	fails(t, tool.UpdateVersion(context.Background(), "invalid"), "Release tag")
	if calls != 1 {
		t.Fatal(calls)
	}
	tool.Run = func(context.Context, string, string, []string, []string, bool) ([]byte, error) {
		return nil, fmt.Errorf("failure")
	}
	fails(t, tool.UpdateVersion(context.Background(), "0.2.1"), "lockfile update failed")
}

func TestBuildPackagesOrderAndNativeBinary(t *testing.T) {
	root := fixtureDir(t)
	binary := filepath.Join(root, executable("tsgo"))
	must(t, os.WriteFile(binary, nil, 0755))
	t.Setenv("ANGULUS_TSGO", binary)
	for _, name := range RuntimePackages {
		dir := filepath.Join(root, "packages", name, "src")
		must(t, os.MkdirAll(dir, 0755))
		must(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export const value: number = 1;\n"), 0644))
	}
	tool := New(root)
	var names []string
	tool.Run = func(ctx context.Context, cwd, command string, args, env []string, capture bool) ([]byte, error) {
		if command != binary || cwd != root {
			t.Fatal(command, cwd)
		}
		names = append(names, args[1])
		if len(args) != 3 || args[2] != "--emitDeclarationOnly" {
			t.Fatalf("native TSGo must emit declarations only: %v", args)
		}
		return nil, nil
	}
	must(t, tool.BuildPackages(context.Background()))
	var expected []string
	for _, name := range RuntimePackages {
		expected = append(expected, filepath.Join("packages", name, "tsconfig.build.json"))
	}
	if !reflect.DeepEqual(names, expected) {
		t.Fatal(names)
	}
	for _, name := range RuntimePackages {
		content, err := os.ReadFile(filepath.Join(root, "packages", name, "dist", "index.js"))
		must(t, err)
		if strings.Contains(string(content), ": number") || !strings.Contains(string(content), "export") {
			t.Fatalf("esbuild did not emit ESM for %s: %s", name, content)
		}
	}
}

func TestTargetsAndStableComparison(t *testing.T) {
	var names []string
	for _, target := range Targets {
		names = append(names, target.Platform+"-"+target.Arch)
		if target.Platform == "win32" && target.GOOS != "windows" {
			t.Fatal(target)
		}
	}
	if !reflect.DeepEqual(names, []string{"darwin-x64", "darwin-arm64", "linux-x64", "linux-arm64", "linux-arm", "win32-x64", "win32-arm64"}) {
		t.Fatal(names)
	}
	if len(PackageNames()) != 12 || !reflect.DeepEqual(PackageNames()[7:], []string{"@angulus/core", "@angulus/router", "@angulus/forms", "@angulus/http", "@angulus/tooling"}) {
		t.Fatal(PackageNames())
	}
	if compareStable("999999999999999999999.0.0", "999999999999999999998.0.0") != 1 {
		t.Fatal("version integer overflow")
	}
}

func TestPublishAuthorizationAndPrereleaseFlag(t *testing.T) {
	file, _ := fixtureArtifacts(t)
	root := fixtureDir(t)
	dir := filepath.Join(root, ".angulus", "release")
	must(t, os.MkdirAll(dir, 0755))
	var manifest Manifest
	must(t, readJSON(file, &manifest))
	for i := range manifest.Packages {
		source := filepath.Join(filepath.Dir(file), filepath.FromSlash(manifest.Packages[i].Tarball))
		destination := filepath.Join(dir, filepath.FromSlash(manifest.Packages[i].Tarball))
		must(t, os.MkdirAll(filepath.Dir(destination), 0755))
		must(t, copyFile(source, destination))
	}

	must(t, writeJSON(filepath.Join(dir, "manifest.json"), manifest))
	tool := New(root)
	tool.Out = io.Discard
	t.Setenv("RELEASE_PRERELEASE", "false")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITHUB_REPOSITORY", "")
	must(t, tool.Publish(context.Background(), "v0.1.0", true, false))
	fails(t, tool.Publish(context.Background(), "v0.1.0", false, false), "Trusted Publisher")
	t.Setenv("GITHUB_ACTIONS", "true")
	fails(t, tool.Publish(context.Background(), "v0.1.0", false, true), "manual maintainer action")
	t.Setenv("RELEASE_PRERELEASE", "true")
	fails(t, tool.Publish(context.Background(), "v0.1.0", true, false), "prerelease flag")
}

func TestSizeMeasuresCoreAndReportsBudget(t *testing.T) {
	root := fixtureDir(t)
	dir := filepath.Join(root, "packages", "core", "src")
	must(t, os.MkdirAll(dir, 0755))
	must(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export const answer: number = 42;\n"), 0644))
	tool := New(root)
	var out bytes.Buffer
	tool.Out = &out
	must(t, tool.Size())
	var report struct {
		Package                               string
		MinifiedBytes, GzipBytes, BudgetBytes int
		WithinBudget                          bool
	}
	must(t, json.Unmarshal(out.Bytes(), &report))
	if report.Package != "@angulus/core" || report.MinifiedBytes == 0 || report.GzipBytes == 0 || report.BudgetBytes != 10240 || !report.WithinBudget {
		t.Fatal(report)
	}
	must(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export const = ;"), 0644))
	fails(t, tool.Size(), "runtime size transform failed")
}
