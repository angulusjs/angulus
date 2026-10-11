package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const libraryMetadataVersion = 1

// LibraryComponentMetadata identifies a public compiled component without
// evaluating its JavaScript implementation.
type LibraryComponentMetadata struct {
	Selector string `json:"selector"`
	Module   string `json:"module"`
	Export   string `json:"export"`
}

// LibraryMetadata is the versioned descriptor-import contract published by a
// compiled library.
type LibraryMetadata struct {
	Version    int                        `json:"version"`
	Components []LibraryComponentMetadata `json:"components"`
}

type libraryManifest struct {
	Name                 string                     `json:"name"`
	Version              string                     `json:"version"`
	Description          string                     `json:"description,omitempty"`
	License              string                     `json:"license,omitempty"`
	Private              bool                       `json:"private,omitempty"`
	Dependencies         map[string]string          `json:"dependencies,omitempty"`
	OptionalDependencies map[string]string          `json:"optionalDependencies,omitempty"`
	PeerDependencies     map[string]string          `json:"peerDependencies,omitempty"`
	PeerDependenciesMeta map[string]map[string]bool `json:"peerDependenciesMeta,omitempty"`
	Engines              map[string]string          `json:"engines,omitempty"`
	Repository           json.RawMessage            `json:"repository,omitempty"`
	Author               json.RawMessage            `json:"author,omitempty"`
	Contributors         json.RawMessage            `json:"contributors,omitempty"`
	Homepage             string                     `json:"homepage,omitempty"`
	Bugs                 json.RawMessage            `json:"bugs,omitempty"`
	Keywords             []string                   `json:"keywords,omitempty"`
	Funding              json.RawMessage            `json:"funding,omitempty"`
	PublishConfig        map[string]any             `json:"publishConfig,omitempty"`
}

type libraryConfig struct {
	Library struct {
		Entry string `json:"entry"`
	} `json:"library"`
}

// ReadLibraryMetadata reads and validates the versioned metadata emitted into
// a library package.
func ReadLibraryMetadata(path string) (LibraryMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LibraryMetadata{}, err
	}
	var metadata LibraryMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return LibraryMetadata{}, fmt.Errorf("invalid library component metadata: %w", err)
	}
	if metadata.Version != libraryMetadataVersion {
		return LibraryMetadata{}, fmt.Errorf("unsupported library component metadata version %d", metadata.Version)
	}
	seenEntries := make(map[string]bool, len(metadata.Components))
	for _, component := range metadata.Components {
		modulePath := filepath.Clean(filepath.FromSlash(component.Module))
		if !selectorPattern.MatchString(component.Selector) || !exportPattern.MatchString(component.Export) ||
			!strings.HasPrefix(component.Module, "./") || filepath.IsAbs(component.Module) || modulePath == ".." ||
			strings.HasPrefix(modulePath, ".."+string(filepath.Separator)) ||
			strings.Contains(component.Module, `\`) ||
			filepath.ToSlash(modulePath) != strings.TrimPrefix(component.Module, "./") {
			return LibraryMetadata{}, fmt.Errorf("invalid component entry in library metadata: %+v", component)
		}
		entryKey := component.Selector + "\x00" + component.Module + "\x00" + component.Export
		if seenEntries[entryKey] {
			return LibraryMetadata{}, fmt.Errorf("duplicate component export %q from %q in library metadata", component.Export, component.Module)
		}
		seenEntries[entryKey] = true
	}
	return metadata, nil
}

// ResolveLibraryComponent finds a dependency's versioned metadata and resolves
// the requested named export to its selector and package-relative module.
func ResolveLibraryComponent(root, moduleSpecifier, exportName string) (LibraryComponentMetadata, error) {
	metadata, _, subpath, err := installedLibraryMetadata(root, moduleSpecifier)
	if err != nil {
		return LibraryComponentMetadata{}, err
	}
	if metadata == nil {
		return LibraryComponentMetadata{}, fmt.Errorf("library package import %q has no component metadata", moduleSpecifier)
	}
	components := libraryMetadataExports(*metadata, subpath)
	var found *LibraryComponentMetadata
	for _, component := range components {
		if component.Export == exportName {
			if found != nil {
				return LibraryComponentMetadata{}, fmt.Errorf("library export %q is ambiguous; import a component subpath", exportName)
			}
			found = &component
		}
	}
	if found != nil {
		return *found, nil
	}
	return LibraryComponentMetadata{}, fmt.Errorf("library package import %q has no component export %q", moduleSpecifier, exportName)
}

func installedLibraryMetadata(root, moduleSpecifier string) (*LibraryMetadata, string, string, error) {
	if moduleSpecifier == "" || strings.HasPrefix(moduleSpecifier, ".") || filepath.IsAbs(moduleSpecifier) {
		return nil, "", "", errors.New("library import must name an installed package")
	}
	parts := strings.Split(moduleSpecifier, "/")
	packagePath := parts[0]
	packageName := parts[0]
	subpath := ""
	if strings.HasPrefix(packagePath, "@") {
		if len(parts) < 2 || parts[1] == "" {
			return nil, "", "", fmt.Errorf("invalid scoped package name %q", moduleSpecifier)
		}
		packagePath = filepath.Join(parts[0], parts[1])
		packageName += "/" + parts[1]
		subpath = strings.Join(parts[2:], "/")
	} else {
		subpath = strings.Join(parts[1:], "/")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, "", "", err
	}
	packageDir := ""
	for dir := filepath.Clean(absRoot); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "node_modules", filepath.FromSlash(packagePath))
		if fileExists(filepath.Join(candidate, "package.json")) {
			packageDir = candidate
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	if packageDir == "" {
		return nil, "", "", fmt.Errorf("library package %q is not installed", packageName)
	}
	manifestData, err := os.ReadFile(filepath.Join(packageDir, "package.json"))
	if err != nil {
		return nil, "", "", err
	}
	var manifest struct {
		Name              string `json:"name"`
		AngulusComponents string `json:"angulusComponents"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, "", "", fmt.Errorf("invalid package manifest for %q: %w", packageName, err)
	}
	if manifest.Name != packageName {
		return nil, "", "", fmt.Errorf("installed package name %q does not match import %q", manifest.Name, moduleSpecifier)
	}
	metadataFile := manifest.AngulusComponents
	if metadataFile == "" {
		metadataFile = "angulus.components.json"
	}
	metadataPath, err := cleanRelative(packageDir, packageDir, metadataFile)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid component metadata path in %q: %w", packageName, err)
	}
	metadata, err := ReadLibraryMetadata(metadataPath)
	if os.IsNotExist(err) && manifest.AngulusComponents == "" {
		return nil, packageDir, subpath, nil
	}
	if err != nil {
		return nil, "", "", err
	}
	return &metadata, packageDir, subpath, nil
}

func libraryMetadataExports(metadata LibraryMetadata, subpath string) []LibraryComponentMetadata {
	hasRoot := false
	for _, component := range metadata.Components {
		hasRoot = hasRoot || component.Module == "./index.js"
	}
	var exports []LibraryComponentMetadata
	for _, component := range metadata.Components {
		publicSubpath := strings.HasPrefix(component.Module, "./components/") &&
			strings.TrimPrefix(component.Module, "./") == subpath
		if subpath == "" && (!hasRoot || component.Module == "./index.js") || subpath != "" && publicSubpath {
			exports = append(exports, component)
		}
	}
	return exports
}

// BuildLibrary validates and packages a component library as ESM with
// declaration files and versioned selector metadata. The output is staged and
// only replaces the destination after every build stage succeeds.
func BuildLibrary(ctx context.Context, root, outDir string) Result {
	result := Check(ctx, root)
	// A barrel-only library has no local descriptors; declaration emit below
	// still validates its complete TypeScript project before publication.
	if len(result.Components) == 0 && len(result.Diagnostics) == 1 && result.Diagnostics[0].Code == "P0005" {
		result.Diagnostics = nil
	}
	if result.hasErrors() {
		return result
	}
	manifestPath := filepath.Join(result.Root, "package.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		addLibraryError(&result, manifestPath, "P0700", err)
		return result
	}
	var manifest libraryManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		addLibraryError(&result, manifestPath, "P0700", fmt.Errorf("invalid package manifest: %w", err))
		return result
	}
	if strings.TrimSpace(manifest.Name) == "" || strings.TrimSpace(manifest.Version) == "" {
		addLibraryError(&result, manifestPath, "P0700", errors.New("library package.json must declare name and version"))
		return result
	}
	if manifest.PeerDependencies["@angulus/core"] == "" ||
		manifest.Dependencies["@angulus/core"] != "" ||
		manifest.OptionalDependencies["@angulus/core"] != "" {
		addLibraryError(&result, manifestPath, "P0701",
			errors.New("declare @angulus/core as a peerDependency, not a dependency, so the library shares the application's runtime"))
		return result
	}
	for _, component := range result.Components {
		component.ScopeID = libraryScopeID(result.Root, component.Path, manifest.Name, manifest.Version)
	}
	writeGenerated(&result)
	if result.hasErrors() {
		return result
	}

	entryName, err := libraryEntry(result.Root)
	if err != nil {
		addLibraryError(&result, filepath.Join(result.Root, "angulus.config.json"), "P0702", err)
		return result
	}
	entry, err := cleanRelative(result.Root, result.Root, entryName)
	if err != nil || filepath.Ext(entry) != ".ts" || strings.HasSuffix(entry, ".d.ts") ||
		strings.Contains(filepath.ToSlash(entry), "/node_modules/") {
		addLibraryError(&result, manifestPath, "P0702", errors.New("library entry must be a TypeScript source file inside the project"))
		return result
	}
	if !fileExists(entry) {
		addLibraryError(&result, entry, "P0702", errors.New("library entry file does not exist"))
		return result
	}

	outPath := outDir
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(result.Root, outPath)
	}
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		addLibraryError(&result, outDir, "P0703", err)
		return result
	}
	outAbs = filepath.Clean(outAbs)
	relOut, err := filepath.Rel(result.Root, outAbs)
	firstDir := strings.Split(relOut, string(filepath.Separator))[0]
	if err != nil || relOut == "." || relOut == ".." || strings.HasPrefix(relOut, ".."+string(filepath.Separator)) ||
		strings.HasPrefix(firstDir, ".") || firstDir == "public" ||
		relOut == "node_modules" || strings.HasPrefix(relOut, "node_modules"+string(filepath.Separator)) ||
		relOut == ".angulus" || strings.HasPrefix(relOut, ".angulus"+string(filepath.Separator)) {
		addLibraryError(&result, outAbs, "P0703", errors.New("library output must be inside the project without replacing its root, public assets, hidden directories, or node_modules"))
		return result
	}
	for _, component := range result.Components {
		for _, source := range []string{component.Path, component.Module, component.Template, component.Facade, component.Render, component.ScopedCSS} {
			if source == "" {
				continue
			}
			rel, relErr := filepath.Rel(outAbs, source)
			if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				addLibraryError(&result, outAbs, "P0703", errors.New("library output overlaps a component source or generated file"))
				return result
			}
		}
	}
	if config := filepath.Join(result.Root, "tsconfig.json"); !fileExists(config) {
		addLibraryError(&result, config, "P0704", errors.New("library declaration generation requires a tsconfig.json"))
		return result
	}

	staging, err := os.MkdirTemp(filepath.Join(result.Root, ".angulus"), "library-staging-")
	if err != nil {
		addLibraryError(&result, filepath.Join(result.Root, ".angulus"), "P0705", err)
		return result
	}
	defer os.RemoveAll(staging)
	typesDir := filepath.Join(staging, "types")
	if err := os.MkdirAll(typesDir, 0755); err != nil {
		addLibraryError(&result, typesDir, "P0705", err)
		return result
	}
	if err := emitLibraryDeclarations(ctx, result.Root, typesDir, &result); err != nil {
		if !result.hasErrors() {
			addLibraryError(&result, typesDir, "P0706", err)
		}
		return result
	}
	if err := makeDeclarationsPortable(typesDir, result.Root); err != nil {
		addLibraryError(&result, typesDir, "P0706", err)
		return result
	}

	entries := []api.EntryPoint{{InputPath: entry, OutputPath: "index"}}
	metadata := LibraryMetadata{Version: libraryMetadataVersion}
	seenSelectors := make(map[string]bool)
	for _, component := range result.Components {
		if seenSelectors[component.Descriptor.Selector] {
			addLibraryError(&result, component.Path, "P0707", fmt.Errorf("duplicate library component selector %q", component.Descriptor.Selector))
			return result
		}
		seenSelectors[component.Descriptor.Selector] = true
		outputName := filepath.ToSlash(filepath.Join("components", component.Descriptor.Selector))
		entries = append(entries, api.EntryPoint{InputPath: component.Facade, OutputPath: outputName})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].OutputPath < entries[j].OutputPath })
	built := api.Build(api.BuildOptions{
		AbsWorkingDir:       result.Root,
		EntryPointsAdvanced: entries,
		Outdir:              staging,
		Bundle:              true,
		Splitting:           true,
		Format:              api.FormatESModule,
		Platform:            api.PlatformBrowser,
		Target:              api.ES2020,
		External:            libraryExternals(manifest),
		Metafile:            true,
		Write:               true,
		LogLevel:            api.LogLevelSilent,
	})
	for _, message := range built.Errors {
		result.Diagnostics = append(result.Diagnostics, esbuildDiagnostic(message, "error"))
	}
	for _, message := range built.Warnings {
		result.Diagnostics = append(result.Diagnostics, esbuildDiagnostic(message, "warning"))
	}
	if len(built.Errors) != 0 {
		return result
	}
	rootExports, err := libraryMetafileExports(built.Metafile, "index.js")
	if err != nil {
		addLibraryError(&result, entry, "P0707", err)
		return result
	}
	rootComponents, err := libraryRootComponentExports(entry, result.Components)
	if err != nil {
		addLibraryError(&result, entry, "P0707", err)
		return result
	}
	for _, component := range result.Components {
		outputName := filepath.ToSlash(filepath.Join("components", component.Descriptor.Selector)) + ".js"
		exports, err := libraryMetafileExports(built.Metafile, outputName)
		if err != nil {
			addLibraryError(&result, component.Path, "P0707", err)
			return result
		}
		if !exports[component.Descriptor.Export] {
			addLibraryError(&result, component.Path, "P0707",
				fmt.Errorf("compiled component subpath %q does not export %q", outputName, component.Descriptor.Export))
			return result
		}
		metadata.Components = append(metadata.Components, LibraryComponentMetadata{
			Selector: component.Descriptor.Selector,
			Module:   "./" + outputName,
			Export:   component.Descriptor.Export,
		})
	}
	for _, component := range rootComponents {
		if !rootExports[component.Export] {
			exported, err := libraryEmittedStarComponent(filepath.Join(staging, "index.js"), entry, component)
			if err != nil || !exported {
				if err == nil {
					err = fmt.Errorf("resolved component export %q is missing from compiled library entry", component.Export)
				}
				addLibraryError(&result, entry, "P0707", err)
				return result
			}
		}
		metadata.Components = append(metadata.Components, component)
	}
	sort.Slice(metadata.Components, func(i, j int) bool {
		if metadata.Components[i].Selector != metadata.Components[j].Selector {
			return metadata.Components[i].Selector < metadata.Components[j].Selector
		}
		if metadata.Components[i].Module != metadata.Components[j].Module {
			return metadata.Components[i].Module < metadata.Components[j].Module
		}
		return metadata.Components[i].Export < metadata.Components[j].Export
	})

	style, err := libraryStyles(result.Components)
	if err != nil {
		addLibraryError(&result, "", "P0708", err)
		return result
	}
	if style != "" {
		if err := os.WriteFile(filepath.Join(staging, "style.css"), []byte(style), 0644); err != nil {
			addLibraryError(&result, filepath.Join(staging, "style.css"), "P0708", err)
			return result
		}
		if err := os.WriteFile(filepath.Join(staging, "style.d.ts"), []byte("export {};\n"), 0644); err != nil {
			addLibraryError(&result, filepath.Join(staging, "style.d.ts"), "P0708", err)
			return result
		}
	}
	metadataData, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		addLibraryError(&result, filepath.Join(staging, "angulus.components.json"), "P0709", err)
		return result
	}
	if err := os.WriteFile(filepath.Join(staging, "angulus.components.json"), append(metadataData, '\n'), 0644); err != nil {
		addLibraryError(&result, filepath.Join(staging, "angulus.components.json"), "P0709", err)
		return result
	}
	if err := writeLibraryManifest(staging, result.Root, entry, manifest, result.Components, style != ""); err != nil {
		addLibraryError(&result, filepath.Join(staging, "package.json"), "P0709", err)
		return result
	}
	for _, name := range []string{"README.md", "LICENSE"} {
		source := filepath.Join(result.Root, name)
		if !fileExists(source) {
			continue
		}
		data, err := os.ReadFile(source)
		if err != nil {
			addLibraryError(&result, source, "P0709", err)
			return result
		}
		if err := os.WriteFile(filepath.Join(staging, name), data, 0644); err != nil {
			addLibraryError(&result, filepath.Join(staging, name), "P0709", err)
			return result
		}
	}
	if err := publishTree(staging, outAbs); err != nil {
		addLibraryError(&result, outAbs, "P0710", err)
	}
	return result
}

func addLibraryError(result *Result, file, code string, err error) {
	result.Diagnostics = append(result.Diagnostics, Diagnostic{File: file, Code: code, Message: err.Error(), Severity: "error"})
}

func libraryScopeID(root, descriptor, name, version string) string {
	rel, _ := filepath.Rel(root, descriptor)
	hash := sha256.Sum256([]byte(name + "@" + version + ":" + filepath.ToSlash(rel)))
	return "data-a-" + hex.EncodeToString(hash[:5])
}

func libraryEntry(root string) (string, error) {
	configPath := filepath.Join(root, "angulus.config.json")
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	entry := "src/index.ts"
	if err == nil {
		var config libraryConfig
		if err := json.Unmarshal(data, &config); err != nil {
			return "", fmt.Errorf("invalid Angulus configuration: %w", err)
		}
		if config.Library.Entry != "" {
			entry = config.Library.Entry
		}
	}
	if filepath.IsAbs(entry) || filepath.Clean(entry) == ".." || strings.HasPrefix(filepath.Clean(entry), ".."+string(filepath.Separator)) {
		return "", errors.New("library entry must be a relative path inside the project")
	}
	return entry, nil
}

func emitLibraryDeclarations(ctx context.Context, root, outDir string, result *Result) error {
	tsgo := nativeTSGo(root)
	if tsgo == "" {
		return errors.New("native TSGo executable not found; install the @typescript/native-preview platform package or set ANGULUS_TSGO")
	}
	args := []string{
		"--project", filepath.Join(root, "tsconfig.json"),
		"--declaration", "--emitDeclarationOnly", "--noEmit", "false", "--noEmitOnError",
		"--declarationMap", "false", "--incremental", "false",
		"--rootDir", root, "--outDir", outDir, "--pretty", "false",
	}
	cmd := exec.CommandContext(ctx, tsgo, args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if len(output) != 0 {
		result.Diagnostics = append(result.Diagnostics, parseTSGoDiagnostics(string(output))...)
	}
	if err == nil {
		return nil
	}
	return fmt.Errorf("native TSGo declaration emit failed: %w\n%s", err, strings.TrimSpace(string(output)))
}

func makeDeclarationsPortable(typesDir, projectRoot string) error {
	return filepath.WalkDir(typesDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".d.ts") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(data)
		tokens, err := tokenizeLibraryModule(source)
		if err != nil {
			return fmt.Errorf("parse declaration module specifiers in %s: %w", path, err)
		}
		specifiers := libraryDeclarationModuleSpecifiers(tokens)
		if len(specifiers) == 0 {
			if strings.Contains(source, projectRoot) {
				return fmt.Errorf("declaration contains a build-machine path: %s", path)
			}
			return nil
		}
		var output strings.Builder
		offset := 0
		for _, token := range specifiers {
			module := token.value
			if libraryAbsoluteModulePath(module) ||
				strings.Contains(module, projectRoot) {
				return fmt.Errorf("declaration contains a non-portable module path %q: %s", module, path)
			}
			if strings.HasPrefix(module, ".") {
				if !strings.HasSuffix(module, ".d.ts") {
					extension := filepath.Ext(module)
					switch extension {
					case ".js", ".mjs", ".cjs", ".json", ".css":
					case ".ts", ".tsx":
						module = strings.TrimSuffix(module, extension) + ".js"
					case ".mts":
						module = strings.TrimSuffix(module, extension) + ".mjs"
					case ".cts":
						module = strings.TrimSuffix(module, extension) + ".cjs"
					default:
						module += ".js"
					}
				}
			}
			output.WriteString(source[offset:token.start])
			if module == token.value {
				output.WriteString(source[token.start:token.end])
			} else {
				output.WriteString(strconv.Quote(module))
			}
			offset = token.end
		}
		output.WriteString(source[offset:])
		if strings.Contains(output.String(), projectRoot) {
			return fmt.Errorf("declaration contains a build-machine path: %s", path)
		}
		if err := os.WriteFile(path, []byte(output.String()), 0644); err != nil {
			return err
		}
		return nil
	})
}

func libraryAbsoluteModulePath(module string) bool {
	if filepath.IsAbs(filepath.FromSlash(module)) || filepath.VolumeName(filepath.FromSlash(module)) != "" {
		return true
	}
	if strings.HasPrefix(module, "file://") || strings.HasPrefix(module, "//") || strings.HasPrefix(module, `\`) {
		return true
	}
	if len(module) >= 2 && ((module[0] >= 'A' && module[0] <= 'Z') || (module[0] >= 'a' && module[0] <= 'z')) &&
		module[1] == ':' {
		return true
	}
	return strings.HasPrefix(module, `\\`)
}

func libraryExternals(manifest libraryManifest) []string {
	external := make(map[string]bool)
	for name := range manifest.Dependencies {
		external[name] = true
	}
	for name := range manifest.PeerDependencies {
		external[name] = true
	}
	for name := range manifest.OptionalDependencies {
		external[name] = true
	}
	result := make([]string, 0, len(external))
	for name := range external {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func libraryStyles(components []*Component) (string, error) {
	var styles strings.Builder
	for _, component := range components {
		if component.ScopedCSS == "" {
			continue
		}
		data, err := os.ReadFile(component.ScopedCSS)
		if err != nil {
			return "", err
		}
		if styles.Len() > 0 {
			styles.WriteByte('\n')
		}
		styles.Write(data)
	}
	return styles.String(), nil
}

func writeLibraryManifest(root, projectRoot, entry string, source libraryManifest, components []*Component, hasStyle bool) error {
	entryRel, err := filepath.Rel(projectRoot, entry)
	if err != nil {
		return err
	}
	types := "./types/" + filepath.ToSlash(strings.TrimSuffix(entryRel, filepath.Ext(entryRel))+".d.ts")
	exports := map[string]any{
		".":                         map[string]string{"types": types, "import": "./index.js", "default": "./index.js"},
		"./angulus.components.json": map[string]string{"default": "./angulus.components.json"},
	}
	for _, component := range components {
		prefix := strings.TrimSuffix(component.Path, ".component.json")
		rel, err := filepath.Rel(projectRoot, prefix)
		if err != nil {
			return err
		}
		declaration := "./types/" + filepath.ToSlash(rel) + ".component.generated.d.ts"
		module := "./components/" + component.Descriptor.Selector + ".js"
		exports["./components/"+component.Descriptor.Selector+".js"] =
			map[string]string{"types": declaration, "import": module, "default": module}
	}
	if hasStyle {
		exports["./style.css"] = map[string]string{"types": "./style.d.ts", "default": "./style.css"}
	}
	published := map[string]any{
		"name": source.Name, "version": source.Version, "type": "module",
		"main": "./index.js", "types": types, "exports": exports,
		"angulusComponents": "./angulus.components.json",
		"sideEffects":       []string{"**/*.css"},
		"files":             []string{"**/*.js", "**/*.css", "style.d.ts", "types", "angulus.components.json", "LICENSE", "README.md"},
	}
	for key, value := range map[string]any{
		"description": source.Description, "license": source.License, "private": source.Private,
		"dependencies": source.Dependencies, "optionalDependencies": source.OptionalDependencies,
		"peerDependencies": source.PeerDependencies, "peerDependenciesMeta": source.PeerDependenciesMeta,
		"engines": source.Engines, "repository": source.Repository, "author": source.Author,
		"contributors": source.Contributors, "homepage": source.Homepage, "bugs": source.Bugs,
		"keywords": source.Keywords, "funding": source.Funding, "publishConfig": source.PublishConfig,
	} {
		if value != nil && value != "" && value != false && stringValue(value) != "null" {
			published[key] = value
		}
	}
	data, err := json.MarshalIndent(published, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "package.json"), append(data, '\n'), 0644)
}

func stringValue(value any) string {
	if raw, ok := value.(json.RawMessage); ok {
		return string(raw)
	}
	return ""
}
