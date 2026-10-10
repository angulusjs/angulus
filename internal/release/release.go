// Package release owns workspace builds and npm release orchestration.
package release

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"angulus/internal/project"
	"github.com/evanw/esbuild/pkg/api"
)

var RuntimePackages = []string{"core", "router", "forms", "http"}
var WorkspacePackages = []string{"core", "router", "forms", "http", "tooling"}

type Target struct{ Platform, Arch, GOOS, GOARCH string }

var Targets = []Target{
	{"darwin", "x64", "darwin", "amd64"}, {"darwin", "arm64", "darwin", "arm64"},
	{"linux", "x64", "linux", "amd64"}, {"linux", "arm64", "linux", "arm64"},
	{"linux", "arm", "linux", "arm"}, {"win32", "x64", "windows", "amd64"},
	{"win32", "arm64", "windows", "arm64"},
}

type Runner func(context.Context, string, string, []string, []string, bool) ([]byte, error)

type Tool struct {
	Root string
	Run  Runner
	Out  io.Writer
}

func New(root string) *Tool { return &Tool{Root: root, Run: run, Out: os.Stdout} }

func run(ctx context.Context, root, command string, args, env []string, capture bool) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = os.Stdin
	if capture {
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w\n%s%s", command, strings.Join(args, " "), err, output, stderr.Bytes())
		}
		return output, nil
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return nil, cmd.Run()
}

func (t *Tool) npm(ctx context.Context, root string, args []string, capture bool) ([]byte, error) {
	command := "npm"
	if script := os.Getenv("npm_execpath"); script != "" {
		command = os.Getenv("npm_node_execpath")
		if command == "" {
			command = "node"
		}
		args = append([]string{script}, args...)
	} else if runtime.GOOS == "windows" {
		// Invoke the npm JavaScript entrypoint directly instead of a .cmd shell.
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, err
		}
		script := filepath.Join(filepath.Dir(node), "node_modules", "npm", "bin", "npm-cli.js")
		if _, err := os.Stat(script); err != nil {
			return nil, fmt.Errorf("cannot find npm CLI; run through npm run: %w", err)
		}
		command, args = node, append([]string{script}, args...)
	}
	return t.Run(ctx, root, command, args, nil, capture)
}

func FindRoot(start string) (string, error) {
	root, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root, nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", fmt.Errorf("cannot find Angulus workspace root from %s", start)
		}
		root = parent
	}
}

func executable(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func (t *Tool) BuildCLI(ctx context.Context) error {
	dir := filepath.Join(t.Root, ".angulus", "bin")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, name := range []string{"angulus", "angulus-release"} {
		if _, err := t.Run(ctx, t.Root, "go", []string{"build", "-o", filepath.Join(dir, executable(name)), "./cmd/" + name}, nil, false); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tool) BuildPackages(ctx context.Context) error {
	platform, arch := runtime.GOOS, runtime.GOARCH
	if platform == "windows" {
		platform = "win32"
	}
	if arch == "amd64" {
		arch = "x64"
	}
	binary := os.Getenv("ANGULUS_TSGO")
	if binary == "" {
		binary = filepath.Join(t.Root, "node_modules", "@typescript", "native-preview-"+platform+"-"+arch, "lib", executable("tsgo"))
	}
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("native TSGo not found: install dependencies or set ANGULUS_TSGO: %w", err)
	}
	for _, name := range RuntimePackages {
		if err := os.RemoveAll(filepath.Join(t.Root, "packages", name, "dist")); err != nil {
			return err
		}
		if _, err := t.Run(ctx, t.Root, binary, []string{"-p", filepath.Join("packages", name, "tsconfig.build.json"), "--emitDeclarationOnly"}, nil, false); err != nil {
			return fmt.Errorf("building @angulus/%s: %w", name, err)
		}
		result := api.Build(api.BuildOptions{
			AbsWorkingDir: t.Root,
			EntryPoints:   []string{filepath.Join("packages", name, "src", "index.ts")},
			Outfile:       filepath.Join(t.Root, "packages", name, "dist", "index.js"),
			Format:        api.FormatESModule,
			Target:        api.ES2022,
			Platform:      api.PlatformBrowser,
			Sourcemap:     api.SourceMapLinked,
			Write:         true,
			LogLevel:      api.LogLevelSilent,
		})
		if len(result.Errors) != 0 {
			return fmt.Errorf("building @angulus/%s ESM: %s", name,
				strings.Join(api.FormatMessages(result.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage, Color: false}), "\n"))
		}
	}
	demo := filepath.Join(t.Root, "examples", "demo")
	if info, err := os.Stat(demo); err == nil && info.IsDir() {
		result := project.Prepare(demo)
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.Severity == "error" {
				return fmt.Errorf("preparing demo facades: %s: %s", diagnostic.File, diagnostic.Message)
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("locating demo sources: %w", err)
	}
	return nil
}

type PackageManifest map[string]any

func readJSON(file string, value any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeJSON(file string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(data, '\n'), 0644)
}

func (t *Tool) manifests() ([]PackageManifest, error) {
	result := make([]PackageManifest, len(WorkspacePackages))
	for i, name := range WorkspacePackages {
		if err := readJSON(filepath.Join(t.Root, "packages", name, "package.json"), &result[i]); err != nil {
			return nil, err
		}
		if result[i]["name"] != "@angulus/"+name {
			return nil, fmt.Errorf("unexpected package name in packages/%s/package.json", name)
		}
	}
	return result, nil
}

func ValidatePackageManifests(manifests []PackageManifest) (string, error) {
	if len(manifests) != len(WorkspacePackages) {
		return "", fmt.Errorf("expected all five workspace packages")
	}
	version, _ := manifests[0]["version"].(string)
	if _, err := ReleaseVersion("v" + version); err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, manifest := range manifests {
		name, _ := manifest["name"].(string)
		if seen[name] {
			return "", fmt.Errorf("duplicate workspace package %s", name)
		}
		seen[name] = true
		if manifest["version"] != version {
			return "", fmt.Errorf("all Angulus package versions must match")
		}
		if name == "@angulus/router" || name == "@angulus/forms" || name == "@angulus/http" {
			deps, _ := manifest["dependencies"].(map[string]any)
			if deps["@angulus/core"] != version {
				return "", fmt.Errorf("%s must depend on the exact core release version", name)
			}
		}
	}
	for _, name := range WorkspacePackages {
		if !seen["@angulus/"+name] {
			return "", fmt.Errorf("missing workspace package @angulus/%s", name)
		}
	}
	return version, nil
}

func (t *Tool) UpdateVersion(ctx context.Context, version string) error {
	if _, err := ReleaseVersion("v" + version); err != nil {
		return err
	}
	manifests, err := t.manifests()
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, manifest := range manifests {
		names[manifest["name"].(string)] = true
	}
	for i, manifest := range manifests {
		manifest["version"] = version
		for _, section := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
			if deps, ok := manifest[section].(map[string]any); ok {
				for name := range deps {
					if names[name] || strings.HasPrefix(name, "@angulus/compiler-") {
						deps[name] = version
					}
				}
			}
		}
		if err := writeJSON(filepath.Join(t.Root, "packages", WorkspacePackages[i], "package.json"), manifest); err != nil {
			return err
		}
	}
	if _, err := t.npm(ctx, t.Root, []string{"install", "--package-lock-only", "--ignore-scripts"}, false); err != nil {
		return fmt.Errorf("lockfile update failed; manifests are updated, rerun npm install --package-lock-only --ignore-scripts: %w", err)
	}
	fmt.Fprintf(t.Out, "Angulus packages updated to %s; review and commit manifests and package-lock.json before tagging v%s.\n", version, version)
	return nil
}
