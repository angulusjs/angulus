package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Artifact struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Tarball   string `json:"tarball"`
	Kind      string `json:"kind,omitempty"`
	Platform  string `json:"platform,omitempty"`
	Arch      string `json:"arch,omitempty"`
	Integrity string `json:"integrity"`
	Action    string `json:"action,omitempty"`
}

type Manifest struct {
	Version  string     `json:"version"`
	Packages []Artifact `json:"packages"`
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, info.Mode().Perm())
}

func copySources(source, destination string, tooling bool) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		output := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(output, 0755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		name := entry.Name()
		if (tooling && strings.HasSuffix(name, ".mjs")) || (!tooling && (strings.HasSuffix(name, ".js") ||
			strings.HasSuffix(name, ".js.map") || strings.HasSuffix(name, ".d.ts"))) {
			return copyFile(path, output)
		}
		return nil
	})
}

func (t *Tool) Pack(ctx context.Context, tag string) error {
	manifests, err := t.manifests()
	if err != nil {
		return err
	}
	version, err := ValidatePackageManifests(manifests)
	if err != nil {
		return err
	}
	if tag != "" {
		release, err := ReleaseVersion(tag)
		if err != nil {
			return err
		}
		if release.Version != version {
			return fmt.Errorf("release tag %s does not match package version %s", tag, version)
		}
	}
	// Preflight must precede compilation or destruction of previous artifacts.
	if err := t.BuildPackages(ctx); err != nil {
		return err
	}
	notices, err := t.nativeNotices(ctx)
	if err != nil {
		return err
	}
	directory := filepath.Join(t.Root, ".angulus", "release")
	for _, name := range []string{"stage", "tarballs"} {
		if err := os.RemoveAll(filepath.Join(directory, name)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(directory, name), 0755); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(directory, "manifest.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	result := Manifest{Version: version}
	pack := func(manifest PackageManifest, artifact Artifact, source func(string) error) error {
		stage := filepath.Join(directory, "stage", strings.TrimPrefix(artifact.Name, "@angulus/"))
		if err := os.MkdirAll(stage, 0755); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(stage, "package.json"), manifest); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(t.Root, "LICENSE"), filepath.Join(stage, "LICENSE")); err != nil {
			return err
		}
		if err := source(stage); err != nil {
			return err
		}
		output, err := t.npm(ctx, stage, []string{"pack", "--ignore-scripts", "--json", "--pack-destination", filepath.Join(directory, "tarballs")}, true)
		if err != nil {
			return err
		}
		var packed []struct{ Name, Version, Filename, Integrity string }
		if err := json.Unmarshal(output, &packed); err != nil {
			return fmt.Errorf("invalid npm pack response: %w", err)
		}
		if len(packed) != 1 || packed[0].Name != artifact.Name || packed[0].Version != version || packed[0].Integrity == "" ||
			filepath.Base(packed[0].Filename) != packed[0].Filename || !strings.HasSuffix(packed[0].Filename, ".tgz") {
			return fmt.Errorf("unexpected npm pack result for %s", artifact.Name)
		}
		artifact.Version, artifact.Tarball, artifact.Integrity = version, "tarballs/"+packed[0].Filename, packed[0].Integrity
		result.Packages = append(result.Packages, artifact)
		return nil
	}
	dependencies := map[string]any{}
	for _, target := range Targets {
		name := "@angulus/compiler-" + target.Platform + "-" + target.Arch
		dependencies[name] = version
		manifest := PackageManifest{
			"name": name, "version": version, "description": "Native Angulus Go CLI for " + target.Platform + " " + target.Arch,
			"license": "MIT", "repository": map[string]any{"type": "git", "url": "https://github.com/angulusjs/angulus.git", "directory": "cmd/angulus"},
			"publishConfig": map[string]any{"access": "public"}, "os": []string{target.Platform}, "cpu": []string{target.Arch},
			"files": []string{"bin", "LICENSE", "THIRD_PARTY_NOTICES", "README.md"},
		}
		err := pack(manifest, Artifact{Name: name, Kind: "compiler", Platform: target.Platform, Arch: target.Arch}, func(stage string) error {
			if err := os.WriteFile(filepath.Join(stage, "THIRD_PARTY_NOTICES"), notices, 0644); err != nil {
				return err
			}
			if err := os.Mkdir(filepath.Join(stage, "bin"), 0755); err != nil {
				return err
			}
			binaryName := "angulus"
			if target.Platform == "win32" {
				binaryName += ".exe"
			}
			binary := filepath.Join(stage, "bin", binaryName)
			_, err := t.Run(ctx, t.Root, "go", []string{"build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", binary, "./cmd/angulus"},
				[]string{"CGO_ENABLED=0", "GOOS=" + target.GOOS, "GOARCH=" + target.GOARCH, "GOARM=7", "GOFLAGS=", "GOWORK=off"}, false)
			if err != nil {
				return err
			}
			if err := os.Chmod(binary, 0755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(stage, "README.md"), []byte(fmt.Sprintf("# %s\n\nPrecompiled Angulus Go CLI %s for %s/%s.\nInstalled automatically as an optional dependency of `@angulus/tooling`; Go is not required.\n\nAngulus is licensed under MIT; see LICENSE. Embedded dependency and Go runtime licenses are included in THIRD_PARTY_NOTICES.\n", name, version, target.Platform, target.Arch)), 0644)
		})
		if err != nil {
			return err
		}
	}
	for _, manifest := range manifests {
		name := strings.TrimPrefix(manifest["name"].(string), "@angulus/")
		if name == "tooling" {
			manifest["optionalDependencies"] = dependencies
		}
		err := pack(manifest, Artifact{Name: manifest["name"].(string), Kind: name}, func(stage string) error {
			if err := copyFile(filepath.Join(t.Root, "packages", name, "README.md"), filepath.Join(stage, "README.md")); err != nil {
				return err
			}
			source := "dist"
			if name == "tooling" {
				source = "src"
				if err := copyFile(filepath.Join(t.Root, "packages", name, "component.schema.json"),
					filepath.Join(stage, "component.schema.json")); err != nil {
					return err
				}
			}
			return copySources(filepath.Join(t.Root, "packages", name, source), filepath.Join(stage, source), name == "tooling")
		})
		if err != nil {
			return err
		}
	}
	file := filepath.Join(directory, "manifest.json")
	if err := writeJSON(file, result); err != nil {
		return err
	}
	if _, err := ValidateArtifacts(file, "v"+version); err != nil {
		return err
	}
	fmt.Fprintf(t.Out, "Packed %d packages: %s\n", len(result.Packages), file)
	return nil
}
