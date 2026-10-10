package project

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	htmlstd "html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	parse "github.com/tdewolff/parse/v2"
	htmllexer "github.com/tdewolff/parse/v2/html"
)

type buildHTMLAttribute struct {
	value      string
	start, end int
}

type buildHTMLTag struct {
	name       string
	start, end int
	attributes map[string]buildHTMLAttribute
}

func buildHTMLTags(source string) []buildHTMLTag {
	input := parse.NewInputString(source)
	lexer := htmllexer.NewLexer(input)
	var tags []buildHTMLTag
	var current *buildHTMLTag
	for {
		start := input.Offset()
		token, _ := lexer.Next()
		end := input.Offset()
		switch token {
		case htmllexer.ErrorToken:
			return tags
		case htmllexer.StartTagToken:
			current = &buildHTMLTag{name: strings.ToLower(string(lexer.Text())), start: start,
				attributes: make(map[string]buildHTMLAttribute)}
		case htmllexer.AttributeToken:
			if current == nil {
				continue
			}
			name := strings.ToLower(string(lexer.AttrKey()))
			// HTML uses the first occurrence of a duplicate attribute.
			if _, exists := current.attributes[name]; exists {
				continue
			}
			value := string(lexer.AttrVal())
			if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
				value = value[1 : len(value)-1]
			}
			current.attributes[name] = buildHTMLAttribute{value: htmlstd.UnescapeString(value), start: start, end: end}
		case htmllexer.StartTagCloseToken, htmllexer.StartTagVoidToken:
			if current != nil {
				current.end = end
				tags = append(tags, *current)
				current = nil
			}
		}
	}
}

func (tag buildHTMLTag) moduleSource() string {
	if tag.name == "script" && strings.EqualFold(strings.TrimSpace(tag.attributes["type"].value), "module") {
		return strings.TrimSpace(tag.attributes["src"].value)
	}
	return ""
}

func (tag buildHTMLTag) stylesheetHref() string {
	if tag.name == "link" {
		for _, rel := range strings.Fields(tag.attributes["rel"].value) {
			if strings.EqualFold(rel, "stylesheet") {
				return strings.TrimSpace(tag.attributes["href"].value)
			}
		}
	}
	return ""
}

func externalHTMLResource(value string) bool {
	return strings.HasPrefix(value, "//") || strings.Contains(value, ":")
}

func rewriteBuildHTMLTags(source string, transform func(buildHTMLTag, string) string) string {
	var output strings.Builder
	last := 0
	for _, tag := range buildHTMLTags(source) {
		output.WriteString(source[last:tag.start])
		output.WriteString(transform(tag, source[tag.start:tag.end]))
		last = tag.end
	}
	output.WriteString(source[last:])
	return output.String()
}

func rewriteBuildHTMLAttribute(tag buildHTMLTag, raw, name, value string) string {
	attribute := tag.attributes[name]
	start, end := attribute.start-tag.start, attribute.end-tag.start
	return raw[:start] + " " + name + `="` + htmlstd.EscapeString(value) + `"` + raw[end:]
}

// BuildMode selects application bundle optimizations.
type BuildMode int

const (
	// BuildProduction minifies application output and emits external source
	// maps without linking them from the published JavaScript or CSS.
	BuildProduction BuildMode = iota
	// BuildDevelopment keeps output readable and links external source maps
	// so browser devtools resolve original TypeScript and CSS sources.
	BuildDevelopment
)

func (mode BuildMode) String() string {
	if mode == BuildDevelopment {
		return "development"
	}
	return "production"
}

// BuildOptions configures application builds.
type BuildOptions struct {
	Mode BuildMode
}

// BuilderStats reports how a Builder satisfied its esbuild invocations.
type BuilderStats struct {
	// Contexts counts esbuild contexts created because the entry graph or
	// bundling options changed (including the first build).
	Contexts int
	// Rebuilds counts incremental rebuilds of an existing esbuild context.
	Rebuilds int
}

// buildContextDir is the stable esbuild outdir used by build contexts. It is
// never written to (Write is false); output files are materialized into a
// fresh per-build staging directory and published transactionally.
var buildContextDir = filepath.Join(".angulus", "build-context")

// Build performs a complete check and bundles all component facades plus
// module-script entrypoints discovered in index.html to ESM via esbuild's Go
// API. Results are emitted into a staging directory before being published.
// Build is stateless and produces a minified production bundle.
func Build(ctx context.Context, root, outDir string) Result {
	return BuildWithOptions(ctx, root, outDir, BuildOptions{Mode: BuildProduction})
}

// BuildWithOptions is a stateless Build with explicit options.
func BuildWithOptions(ctx context.Context, root, outDir string, options BuildOptions) Result {
	builder := NewBuilder(root, outDir, options)
	defer builder.Close()
	return builder.Build(ctx)
}

// Builder performs repeated application builds for one root/output pair. It
// keeps a persistent esbuild context that is reused through Rebuild while the
// entry graph and options are unchanged, and recreated otherwise. Every Build
// still runs the full Check and publishes output transactionally.
type Builder struct {
	root    string
	outDir  string
	options BuildOptions

	mu           sync.Mutex
	context      api.BuildContext
	key          string
	closed       bool
	stats        BuilderStats
	dependencies []string
}

// NewBuilder creates an incremental builder. Call Close to release the
// esbuild context.
func NewBuilder(root, outDir string, options BuildOptions) *Builder {
	return &Builder{root: root, outDir: outDir, options: options}
}

// Stats returns context creation and rebuild counters.
func (b *Builder) Stats() BuilderStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

// Dependencies returns checker/discovery dependencies and the last successful
// esbuild input graph. Failed checks retain runtime inputs while adding newly
// discovered type/metadata dependencies so the watcher can trigger recovery.
func (b *Builder) Dependencies() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.dependencies...)
}

// Close disposes the esbuild context. It is safe to call more than once;
// subsequent Build calls report an error.
func (b *Builder) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.disposeLocked()
	return nil
}

func (b *Builder) disposeLocked() {
	if b.context != nil {
		b.context.Dispose()
		b.context = nil
	}
	b.key = ""
}

type buildPlan struct {
	entries   []api.EntryPoint
	indexPath string
	index     []byte
}

// Build checks the project, bundles it, and publishes the output directory
// only when every step succeeds; on failure the previous output is untouched.
func (b *Builder) Build(ctx context.Context) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Result{Root: b.root, Diagnostics: []Diagnostic{{File: b.root, Code: "P0606", Message: "Builder is closed", Severity: "error"}}}
	}
	outAbs, outputErr := ResolveBuildOutput(b.root, b.outDir)
	if outputErr != nil {
		root, _ := filepath.Abs(b.root)
		return Result{Root: root, Diagnostics: []Diagnostic{{File: outAbsOrInput(outAbs, b.outDir), Code: "P0600", Message: outputErr.Error(), Severity: "error"}}}
	}
	watchedDependencies := append([]string(nil), b.dependencies...)
	before, snapshotErr := CaptureSources(b.root, []string{outAbs}, watchedDependencies)
	if snapshotErr != nil {
		return Result{Root: b.root, Diagnostics: []Diagnostic{{File: b.root, Code: "P0604",
			Message: "Cannot capture build inputs: " + snapshotErr.Error(), Severity: "error"}}}
	}
	result := Check(ctx, b.root)
	b.dependencies = mergeBuildDependencies(result.Root, b.dependencies, result.Dependencies)
	if result.hasErrors() {
		return result
	}
	if canceledBuild(ctx, &result) {
		return result
	}
	outAbs, ok := resolveBuildOutput(&result, b.outDir)
	if !ok {
		return result
	}
	plan, ok := collectBuildEntries(&result)
	if !ok {
		return result
	}
	contextOut := filepath.Join(result.Root, buildContextDir)
	built, ok := b.bundleLocked(ctx, &result, plan.entries, contextOut)
	if !ok {
		return result
	}

	stagingBase := filepath.Join(result.Root, ".angulus")
	if err := os.MkdirAll(stagingBase, 0755); err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: stagingBase, Code: "P0600", Message: err.Error(), Severity: "error"})
		return result
	}
	staging, err := os.MkdirTemp(stagingBase, "build-staging-")
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: stagingBase, Code: "P0600", Message: err.Error(), Severity: "error"})
		return result
	}
	defer os.RemoveAll(staging)

	if err := writeOutputFiles(built.OutputFiles, contextOut, staging); err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: staging, Code: "P0604", Message: err.Error(), Severity: "error"})
		return result
	}
	if len(plan.index) > 0 {
		html := attachCSSBundles(string(plan.index), result.Root, cssBundles(result.Root, contextOut, built.Metafile))
		html = rewriteModuleScripts(html)
		html = rootRelativeStyles(html)
		if err := os.WriteFile(filepath.Join(staging, "index.html"), []byte(html), 0644); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: plan.indexPath, Code: "P0604", Message: err.Error(), Severity: "error"})
			return result
		}
	}
	if publicDir := filepath.Join(result.Root, "public"); fileExistsDir(publicDir) {
		if err := copyTree(publicDir, staging); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: publicDir, Code: "P0604", Message: err.Error(), Severity: "error"})
			return result
		}
	}
	if canceledBuild(ctx, &result) {
		return result
	}
	after, snapshotErr := CaptureSources(result.Root, []string{outAbs}, watchedDependencies)
	if snapshotErr != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: result.Root, Code: "P0604",
			Message: "Cannot verify build inputs: " + snapshotErr.Error(), Severity: "error"})
		return result
	}
	if !before.Equal(after) {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: result.Root, Code: "P0608",
			Message: "Build inputs changed during compilation; stale output was not published", Severity: "error"})
		return result
	}
	if err := publishTree(staging, outAbs); err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: outAbs, Code: "P0604", Message: err.Error(), Severity: "error"})
	}
	return result
}

func canceledBuild(ctx context.Context, result *Result) bool {
	if err := ctx.Err(); err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: result.Root, Code: "P0607", Message: "Build canceled: " + err.Error(), Severity: "error"})
		return true
	}
	return false
}

func resolveBuildOutput(result *Result, outDir string) (string, bool) {
	outAbs, err := ResolveBuildOutput(result.Root, outDir)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: outDir, Code: "P0600", Message: err.Error(), Severity: "error"})
		return "", false
	}
	for _, component := range result.Components {
		sources := append([]string{component.Path, component.Module, component.Template, component.Facade, component.Render}, component.Styles...)
		sources = append(sources, component.ScopedCSS)
		for _, source := range sources {
			if source != "" && pathsOverlap(outAbs, source) {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{File: outAbs, Code: "P0600", Message: "Output directory overlaps a component source or generated file", Severity: "error"})
				return "", false
			}
		}
	}
	return outAbs, true
}

// ResolveBuildOutput validates and resolves the application output path without
// requiring a successful project check. Serve callers can reject unsafe output
// paths before responding to source-check diagnostics.
func ResolveBuildOutput(root, outDir string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absRoot = filepath.Clean(absRoot)
	outPath := outDir
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(absRoot, outPath)
	}
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		return "", err
	}
	outAbs = filepath.Clean(outAbs)
	relOut, _ := filepath.Rel(absRoot, outAbs)
	firstDir := strings.Split(relOut, string(filepath.Separator))[0]
	if outAbs == absRoot || relOut == ".." || strings.HasPrefix(relOut, ".."+string(filepath.Separator)) ||
		strings.HasPrefix(firstDir, ".") && firstDir != ".angulus" || firstDir == "public" ||
		relOut == ".angulus" || strings.HasPrefix(relOut, filepath.Join(".angulus", "build-staging-")) ||
		relOut == buildContextDir || strings.HasPrefix(relOut, buildContextDir+string(filepath.Separator)) ||
		relOut == "node_modules" || strings.HasPrefix(relOut, "node_modules"+string(filepath.Separator)) {
		return outAbs, fmt.Errorf("output directory must be inside the project and must not replace its root, .angulus, or node_modules")
	}
	if publicDir := filepath.Join(absRoot, "public"); pathsOverlap(outAbs, publicDir) {
		return outAbs, fmt.Errorf("output directory overlaps public assets")
	}
	indexPath := filepath.Join(absRoot, "index.html")
	if pathsOverlap(outAbs, indexPath) {
		return outAbs, fmt.Errorf("output directory overlaps the HTML entrypoint")
	}
	if data, readErr := os.ReadFile(indexPath); readErr == nil {
		for _, tag := range buildHTMLTags(string(data)) {
			src := tag.moduleSource()
			if src == "" || externalHTMLResource(src) {
				continue
			}
			input, pathErr := cleanRelative(absRoot, absRoot, strings.TrimPrefix(strings.TrimSpace(src), "/"))
			if pathErr == nil && pathsOverlap(outAbs, input) {
				return outAbs, fmt.Errorf("output directory overlaps module entrypoint %s", src)
			}
		}
		for _, tag := range buildHTMLTags(string(data)) {
			href := tag.stylesheetHref()
			if href == "" || externalHTMLResource(href) {
				continue
			}
			input, pathErr := cleanRelative(absRoot, absRoot, strings.TrimPrefix(strings.TrimSpace(href), "/"))
			if pathErr == nil && pathsOverlap(outAbs, input) {
				return outAbs, fmt.Errorf("output directory overlaps stylesheet entrypoint %s", href)
			}
		}
	} else if !os.IsNotExist(readErr) {
		return outAbs, fmt.Errorf("cannot inspect HTML entrypoint: %w", readErr)
	}
	return outAbs, nil
}

func outAbsOrInput(resolved, input string) string {
	if resolved != "" {
		return resolved
	}
	return input
}

func pathsOverlap(first, second string) bool {
	firstAbs, firstErr := filepath.Abs(first)
	secondAbs, secondErr := filepath.Abs(second)
	if firstErr != nil || secondErr != nil {
		return false
	}
	return withinOrEqual(firstAbs, secondAbs) || withinOrEqual(secondAbs, firstAbs)
}

func withinOrEqual(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func collectBuildEntries(result *Result) (buildPlan, bool) {
	var plan buildPlan
	scriptEntries := 0
	plan.indexPath = filepath.Join(result.Root, "index.html")
	if data, err := os.ReadFile(plan.indexPath); err == nil {
		plan.index = data
		for _, tag := range buildHTMLTags(string(data)) {
			href := tag.stylesheetHref()
			if href == "" || externalHTMLResource(href) {
				continue
			}
			input, pathErr := cleanRelative(result.Root, result.Root, strings.TrimPrefix(href, "/"))
			if pathErr != nil || !fileExists(input) {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{File: plan.indexPath, Code: "P0610", Message: "Stylesheet entry does not exist: " + href, Severity: "error"})
				continue
			}
			rel, _ := filepath.Rel(result.Root, input)
			plan.entries = append(plan.entries, api.EntryPoint{InputPath: input, OutputPath: strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))})
		}
		for _, tag := range buildHTMLTags(string(data)) {
			src := tag.moduleSource()
			if src == "" || externalHTMLResource(src) {
				continue
			}
			src = strings.TrimPrefix(src, "/")
			input, pathErr := cleanRelative(result.Root, result.Root, src)
			if pathErr != nil || !fileExists(input) {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{File: plan.indexPath, Code: "P0601", Message: "Module script entry does not exist: " + src, Severity: "error"})
				continue
			}
			rel, _ := filepath.Rel(result.Root, input)
			output := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
			plan.entries = append(plan.entries, api.EntryPoint{InputPath: input, OutputPath: output})
			scriptEntries++
		}
	} else if !os.IsNotExist(err) {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: plan.indexPath, Code: "P0601", Message: err.Error(), Severity: "error"})
	}
	if result.hasErrors() {
		return plan, false
	}
	if scriptEntries == 0 {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: plan.indexPath, Code: "P0602", Message: "index.html must contain at least one local type=\"module\" script entrypoint", Severity: "error"})
		return plan, false
	}
	sort.Slice(plan.entries, func(i, j int) bool { return plan.entries[i].OutputPath < plan.entries[j].OutputPath })
	for i := 1; i < len(plan.entries); i++ {
		if plan.entries[i-1].OutputPath == plan.entries[i].OutputPath {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: plan.entries[i].InputPath, Code: "P0603", Message: "Duplicate build entry output: " + plan.entries[i].OutputPath, Severity: "error"})
			return plan, false
		}
	}
	return plan, true
}

func applicationBuildOptions(root, contextOut string, entries []api.EntryPoint, options BuildOptions) api.BuildOptions {
	built := api.BuildOptions{
		AbsWorkingDir:       root,
		EntryPointsAdvanced: entries,
		Outdir:              contextOut,
		Outbase:             root,
		EntryNames:          "[dir]/[name]",
		ChunkNames:          "chunks/[name]-[hash]",
		AssetNames:          "assets/[name]-[hash]",
		Bundle:              true,
		Splitting:           true,
		Format:              api.FormatESModule,
		Platform:            api.PlatformBrowser,
		Target:              api.ES2020,
		Sourcemap:           api.SourceMapExternal,
		Metafile:            true,
		Write:               false,
		LogLevel:            api.LogLevelSilent,
		Plugins:             []api.Plugin{componentStylePlugin(root)},
	}
	if options.Mode == BuildDevelopment {
		built.Sourcemap = api.SourceMapLinked
		built.SourcesContent = api.SourcesContentInclude
	} else {
		built.MinifyWhitespace = true
		built.MinifyIdentifiers = true
		built.MinifySyntax = true
	}
	return built
}

func componentStylePlugin(root string) api.Plugin {
	return api.Plugin{Name: "angulus-component-styles", Setup: func(build api.PluginBuild) {
		build.OnLoad(api.OnLoadOptions{Filter: `\.component\.styles\.generated\.css$`}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
			relative, err := filepath.Rel(root, args.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			href, err := json.Marshal("/" + filepath.ToSlash(relative))
			if err != nil {
				return api.OnLoadResult{}, err
			}
			contents := fmt.Sprintf(`if(typeof document!=="undefined"){const url=new URL(%s,document.baseURI).href;if(!Array.from(document.querySelectorAll("link")).some(link=>link.rel==="stylesheet"&&new URL(link.href).pathname===new URL(url).pathname)){const link=document.createElement("link");link.rel="stylesheet";link.href=url;document.head.append(link);}}`, href)
			css, err := os.ReadFile(args.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			const mapMarker = "sourceMappingURL=data:application/json;base64,"
			if start := strings.LastIndex(string(css), mapMarker); start >= 0 {
				encoded := strings.Fields(string(css)[start+len(mapMarker):])
				if len(encoded) == 0 {
					return api.OnLoadResult{}, fmt.Errorf("missing generated stylesheet source map: %s", args.Path)
				}
				data, err := base64.StdEncoding.DecodeString(encoded[0])
				if err != nil {
					return api.OnLoadResult{}, err
				}
				var sourceMap sourceMapDocument
				if err := json.Unmarshal(data, &sourceMap); err != nil {
					return api.OnLoadResult{}, err
				}
				if len(sourceMap.Sources) > 0 && len(sourceMap.SourcesContent) > 0 {
					source := filepath.Join(filepath.Dir(args.Path), filepath.FromSlash(sourceMap.Sources[0]))
					contents = inlineSourceMap(args.Path+".js", contents,
						[]sourceMapPoint{{Generated: 0, Source: source, Original: 0}},
						map[string]string{source: ""})
				}
			}
			return api.OnLoadResult{Contents: &contents, Loader: api.LoaderJS, WatchFiles: []string{args.Path}}, nil
		})
	}}
}

// buildContextKey fingerprints everything baked into an esbuild context:
// the root, stable outdir, options, and ordered entry graph.
func buildContextKey(root, contextOut string, entries []api.EntryPoint, options BuildOptions) string {
	var key strings.Builder
	fmt.Fprintf(&key, "%s\x00%s\x00%s\n", root, contextOut, options.Mode)
	for _, entry := range entries {
		fmt.Fprintf(&key, "%s\x00%s\n", entry.InputPath, entry.OutputPath)
	}
	return key.String()
}

// bundleLocked runs esbuild through the cached context, creating or
// recreating it when the entry graph or options changed. The caller's
// context cancels an in-flight rebuild.
func (b *Builder) bundleLocked(ctx context.Context, result *Result, entries []api.EntryPoint, contextOut string) (api.BuildResult, bool) {
	key := buildContextKey(result.Root, contextOut, entries, b.options)
	if b.context == nil || b.key != key {
		b.disposeLocked()
		esContext, contextErr := api.Context(applicationBuildOptions(result.Root, contextOut, entries, b.options))
		if contextErr != nil {
			for _, message := range contextErr.Errors {
				result.Diagnostics = append(result.Diagnostics, esbuildDiagnostic(message, "error"))
			}
			if !result.hasErrors() {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{File: result.Root, Code: "P0605", Message: "esbuild context could not be created", Severity: "error"})
			}
			return api.BuildResult{}, false
		}
		b.context, b.key = esContext, key
		b.stats.Contexts++
	} else {
		b.stats.Rebuilds++
	}

	esContext := b.context
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			esContext.Cancel()
		case <-done:
		}
	}()
	built := esContext.Rebuild()
	close(done)
	<-stopped

	if canceledBuild(ctx, result) {
		return built, false
	}
	for _, message := range built.Errors {
		result.Diagnostics = append(result.Diagnostics, esbuildDiagnostic(message, "error"))
	}
	for _, message := range built.Warnings {
		result.Diagnostics = append(result.Diagnostics, esbuildDiagnostic(message, "warning"))
	}
	if len(built.Errors) == 0 {
		var metadata struct {
			Inputs map[string]json.RawMessage `json:"inputs"`
		}
		if err := json.Unmarshal([]byte(built.Metafile), &metadata); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: result.Root, Code: "P0605",
				Message: "Invalid esbuild dependency metadata: " + err.Error(), Severity: "error"})
			return built, false
		}
		dependencies := make([]string, 0, len(metadata.Inputs))
		for input := range metadata.Inputs {
			if !filepath.IsAbs(input) {
				input = filepath.Join(result.Root, filepath.FromSlash(input))
			}
			dependencies = append(dependencies, filepath.Clean(input))
		}
		sort.Strings(dependencies)
		var styles []api.EntryPoint
		for _, dependency := range dependencies {
			if strings.HasSuffix(dependency, ".component.styles.generated.css") {
				relative, err := filepath.Rel(result.Root, dependency)
				if err != nil {
					result.Diagnostics = append(result.Diagnostics, Diagnostic{File: dependency, Code: "P0605", Message: err.Error(), Severity: "error"})
					return built, false
				}
				styles = append(styles, api.EntryPoint{InputPath: dependency,
					OutputPath: strings.TrimSuffix(filepath.ToSlash(relative), ".css")})
			}
		}
		if len(styles) > 0 {
			styleOptions := applicationBuildOptions(result.Root, contextOut, styles, b.options)
			styleOptions.Plugins = nil
			styleBuild := api.Build(styleOptions)
			for _, message := range styleBuild.Errors {
				result.Diagnostics = append(result.Diagnostics, esbuildDiagnostic(message, "error"))
			}
			for _, message := range styleBuild.Warnings {
				result.Diagnostics = append(result.Diagnostics, esbuildDiagnostic(message, "warning"))
			}
			if len(styleBuild.Errors) != 0 {
				return built, false
			}
			built.OutputFiles = append(built.OutputFiles, styleBuild.OutputFiles...)
		}
		b.dependencies = mergeBuildDependencies(result.Root, dependencies, result.Dependencies)
	}

	return built, len(built.Errors) == 0
}

func mergeBuildDependencies(root string, groups ...[]string) []string {
	seen := make(map[string]bool)
	var dependencies []string
	for _, group := range groups {
		for _, dependency := range group {
			if dependency == "" {
				continue
			}
			if !filepath.IsAbs(dependency) {
				dependency = filepath.Join(root, filepath.FromSlash(dependency))
			}
			dependency = filepath.Clean(dependency)
			key := pathKey(dependency)
			if !seen[key] {
				seen[key] = true
				dependencies = append(dependencies, dependency)
			}
		}
	}
	sort.Strings(dependencies)
	return dependencies
}

func writeOutputFiles(files []api.OutputFile, contextOut, staging string) error {
	for _, file := range files {
		rel, err := filepath.Rel(contextOut, file.Path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("esbuild output escapes the build directory: %s", file.Path)
		}
		target := filepath.Join(staging, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, file.Contents, 0644); err != nil {
			return err
		}
	}
	return nil
}
func cssBundles(root, staging, metafile string) map[string]string {
	type output struct {
		EntryPoint string `json:"entryPoint"`
		CSSBundle  string `json:"cssBundle"`
	}
	var data struct {
		Outputs map[string]output `json:"outputs"`
	}
	result := make(map[string]string)
	if json.Unmarshal([]byte(metafile), &data) != nil {
		return result
	}
	for _, item := range data.Outputs {
		if item.EntryPoint == "" || item.CSSBundle == "" {
			continue
		}
		entry := item.EntryPoint
		if !filepath.IsAbs(entry) {
			entry = filepath.Join(root, filepath.FromSlash(entry))
		}
		cssPath := ""
		candidates := []string{item.CSSBundle}
		if !filepath.IsAbs(item.CSSBundle) {
			candidates = []string{
				filepath.Join(root, filepath.FromSlash(item.CSSBundle)),
				filepath.Join(staging, filepath.FromSlash(item.CSSBundle)),
			}
		}
		for _, candidate := range candidates {
			rel, err := filepath.Rel(staging, candidate)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				cssPath = filepath.ToSlash(rel)
				break
			}
		}
		if cssPath != "" {
			result[filepath.Clean(entry)] = cssPath
		}
	}
	return result
}

func attachCSSBundles(html, root string, bundles map[string]string) string {
	return rewriteBuildHTMLTags(html, func(tag buildHTMLTag, raw string) string {
		src := tag.moduleSource()
		if src == "" || externalHTMLResource(src) {
			return raw
		}
		entry, err := cleanRelative(root, root, strings.TrimPrefix(src, "/"))
		if err != nil {
			return raw
		}
		cssPath := bundles[filepath.Clean(entry)]
		if cssPath == "" {
			return raw
		}
		return fmt.Sprintf(`<link rel="stylesheet" href="/%s">`+"\n%s", htmlstd.EscapeString(cssPath), raw)
	})
}

func esbuildDiagnostic(message api.Message, severity string) Diagnostic {
	file := ""
	start, end, line, column := 0, 0, 0, 0
	if message.Location != nil {
		file = message.Location.File
		start = message.Location.Column
		end = start + 1
		line = message.Location.Line
		column = message.Location.Column + 1
	}
	text := message.Text
	if message.Location != nil && message.Location.LineText != "" {
		text += ": " + strings.TrimSpace(message.Location.LineText)
	}
	return Diagnostic{File: file, Start: start, End: end, Line: line, Column: column, Code: "P0605", Message: text, Severity: severity}
}

func rewriteModuleScripts(html string) string {
	return rewriteBuildHTMLTags(html, func(tag buildHTMLTag, raw string) string {
		src := tag.moduleSource()
		if src == "" || externalHTMLResource(src) {
			return raw
		}
		relative := strings.TrimPrefix(src, "/")
		ext := filepath.Ext(relative)
		if ext == "" {
			return raw
		}
		js := "/" + strings.TrimPrefix(strings.TrimSuffix(relative, ext), "./") + ".js"
		return rewriteBuildHTMLAttribute(tag, raw, "src", js)
	})
}

func rootRelativeStyles(html string) string {
	return rewriteBuildHTMLTags(html, func(tag buildHTMLTag, raw string) string {
		href := tag.stylesheetHref()
		if href == "" || strings.HasPrefix(href, "/") || externalHTMLResource(href) {
			return raw
		}
		return rewriteBuildHTMLAttribute(tag, raw, "href", "/"+strings.TrimPrefix(href, "./"))
	})
}

func fileExistsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("public asset symlinks are not supported: %s", path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		inCloseErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return inCloseErr
	})
}

func publishTree(staging, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	var backup string
	if _, err := os.Stat(destination); err == nil {
		if !fileExistsDir(destination) {
			return fmt.Errorf("output path exists and is not a directory")
		}
		backup, err = os.MkdirTemp(filepath.Dir(destination), ".angulus-backup-")
		if err != nil {
			return err
		}
		if err := os.Remove(backup); err != nil {
			return err
		}
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		if backup != "" {
			_ = os.Rename(backup, destination)
		}
		return err
	}
	if backup != "" {
		return os.RemoveAll(backup)
	}
	return nil
}
