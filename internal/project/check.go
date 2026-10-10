package project

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var tsgoDiagnostic = regexp.MustCompile(`^(.*)\((\d+),(\d+)\): (error|warning) (TS[0-9]+): (.*)$`)

// Prepare discovers descriptors and refreshes only the generated files
// recorded as Angulus-owned. It never evaluates or rewrites user TypeScript.
func Prepare(root string) Result {
	result := Discover(root)
	if !result.hasErrors() {
		writeGenerated(&result)
	}
	if len(result.Components) == 0 && !result.hasErrors() {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: result.Root, Code: "P0005",
			Message: "No *.component.json component descriptors were found", Severity: "error"})
	}
	return result
}

// Check runs descriptor/template preparation followed by the native TSGo
// executable. TSGo is intentionally invoked as a subprocess, never through
// its JavaScript API or Node launcher.
func Check(ctx context.Context, root string) Result {
	result := Prepare(root)
	if result.hasErrors() {
		return result
	}
	tsgo := nativeTSGo(result.Root)
	if tsgo == "" {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: result.Root, Code: "P0500",
			Message: "Native TSGo executable not found. Install the @typescript/native-preview platform package or set ANGULUS_TSGO to its native binary.", Severity: "error"})
		return result
	}
	args := []string{"--noEmit", "--pretty", "false", "--listFiles"}
	config := filepath.Join(result.Root, "tsconfig.json")
	if fileExists(config) {
		args = append(args, "--project", config, "--incremental", "--tsBuildInfoFile",
			filepath.Join(result.Root, ".angulus", "project-check.tsbuildinfo"))
	} else {
		files, scanErr := projectTypeScriptFiles(result.Root)
		if scanErr != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: result.Root, Code: "P0503",
				Message: "Cannot discover TypeScript files: " + scanErr.Error(), Severity: "error"})
			return result
		}
		args = append(args, "--ignoreConfig", "--target", "ES2022", "--module", "ESNext",
			"--moduleResolution", "Bundler", "--strict", "--skipLibCheck", "--lib", "ES2022,DOM,DOM.Iterable")
		args = append(args, files...)
	}

	cmd := exec.CommandContext(ctx, tsgo, args...)
	cmd.Dir = result.Root
	output, err := cmd.CombinedOutput()
	addCheckerDependencies(&result, string(output))
	if len(output) > 0 {
		parsed := parseTSGoDiagnostics(string(output))
		setDiagnosticPositions(result.Root, parsed)
		mapTemplateDiagnostics(result.Root, result.Components, parsed)
		mapDescriptorDiagnostics(result.Root, result.Components, parsed)
		result.Diagnostics = append(result.Diagnostics, parsed...)
	}
	if fileExists(config) && len(result.Components) > 0 && ctx.Err() == nil {
		templateDiagnostics, templateErr := checkAllTemplateContracts(ctx, tsgo, &result)
		mapTemplateDiagnostics(result.Root, result.Components, templateDiagnostics)
		mapDescriptorDiagnostics(result.Root, result.Components, templateDiagnostics)
		result.Diagnostics = appendUniqueDiagnostics(result.Diagnostics, templateDiagnostics)
		if templateErr != nil && !hasErrors(templateDiagnostics) {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{File: config, Code: "P0501",
				Message: fmt.Sprintf("Native TSGo template check failed: %v", templateErr), Severity: "error"})
		}
	}
	if err != nil && !hasErrors(result.Diagnostics) {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{File: config, Code: "P0501",
			Message: fmt.Sprintf("Native TSGo failed: %v\n%s", err, strings.TrimSpace(string(output))), Severity: "error"})
	}
	return result
}

func checkAllTemplateContracts(ctx context.Context, tsgo string, result *Result) ([]Diagnostic, error) {
	root, components := result.Root, result.Components
	config := filepath.Join(root, "tsconfig.json")
	checkConfigDir := filepath.Join(root, ".angulus")
	configFile, err := os.CreateTemp(checkConfigDir, "template-check-*.json")
	if err != nil {
		return nil, fmt.Errorf("create template-check TSGo config: %w", err)
	}
	configPath := configFile.Name()
	defer os.Remove(configPath)

	extends, err := filepath.Rel(filepath.Dir(configPath), config)
	if err != nil {
		_ = configFile.Close()
		return nil, fmt.Errorf("resolve project tsconfig for template checks: %w", err)
	}
	files := make([]string, 0, len(components))
	for _, component := range components {
		file, relErr := filepath.Rel(filepath.Dir(configPath), templateCheckPath(component.Render))
		if relErr != nil {
			_ = configFile.Close()
			return nil, fmt.Errorf("resolve generated template checker: %w", relErr)
		}
		files = append(files, filepath.ToSlash(file))
	}
	configData, err := json.Marshal(struct {
		Extends         string         `json:"extends"`
		Files           []string       `json:"files"`
		Include         []string       `json:"include"`
		Exclude         []string       `json:"exclude"`
		References      []any          `json:"references"`
		CompilerOptions map[string]any `json:"compilerOptions"`
	}{
		Extends: filepath.ToSlash(extends), Files: files, Include: []string{}, Exclude: []string{},
		References: []any{}, CompilerOptions: map[string]any{
			"noEmit": true, "rootDir": root, "incremental": true,
			"tsBuildInfoFile": filepath.Join(root, ".angulus", "template-check.tsbuildinfo"),
		},
	})
	if err != nil {
		_ = configFile.Close()
		return nil, fmt.Errorf("encode template-check TSGo config: %w", err)
	}
	if _, err := configFile.Write(configData); err != nil {
		_ = configFile.Close()
		return nil, fmt.Errorf("write template-check TSGo config: %w", err)
	}
	if err := configFile.Close(); err != nil {
		return nil, fmt.Errorf("close template-check TSGo config: %w", err)
	}

	cmd := exec.CommandContext(ctx, tsgo, "--noEmit", "--pretty", "false", "--listFiles", "--project", configPath)
	cmd.Dir = root
	output, runErr := cmd.CombinedOutput()
	addCheckerDependencies(result, string(output))
	diagnostics := parseTSGoDiagnostics(string(output))
	setDiagnosticPositions(root, diagnostics)
	if runErr != nil && len(diagnostics) == 0 {
		return diagnostics, fmt.Errorf("%w\n%s", runErr, strings.TrimSpace(string(output)))
	}
	return diagnostics, runErr
}

func addCheckerDependencies(result *Result, output string) {
	seen := make(map[string]bool)
	for _, path := range result.Dependencies {
		seen[pathKey(path)] = true
	}
	add := func(path string) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(result.Root, path)
		}
		path = filepath.Clean(path)
		if !seen[pathKey(path)] {
			seen[pathKey(path)] = true
			result.Dependencies = append(result.Dependencies, path)
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if filepath.IsAbs(line) && fileExists(line) {
			add(line)
		}
	}
	for _, component := range result.Components {
		for _, dependency := range component.Imports {
			if !dependency.IsLibrary {
				continue
			}
			specifier := dependency.ImportModule
			parts := strings.Split(specifier, "/")
			name := parts[0]
			if strings.HasPrefix(name, "@") && len(parts) > 1 {
				name += "/" + parts[1]
			}
			for dir := result.Root; ; dir = filepath.Dir(dir) {
				packageDir := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
				manifestPath := filepath.Join(packageDir, "package.json")
				if data, err := os.ReadFile(manifestPath); err == nil {
					add(manifestPath)
					var manifest struct {
						AngulusComponents string `json:"angulusComponents"`
					}
					if err := json.Unmarshal(data, &manifest); err == nil {
						if manifest.AngulusComponents == "" {
							manifest.AngulusComponents = "angulus.components.json"
						}
						if path, err := cleanRelative(packageDir, packageDir, manifest.AngulusComponents); err == nil {
							add(path)
						}
					}
					break
				}
				if parent := filepath.Dir(dir); parent == dir {
					break
				}
			}
		}
	}
	sort.Strings(result.Dependencies)
}

func setDiagnosticPositions(root string, diagnostics []Diagnostic) {
	for index := range diagnostics {
		diagnostic := &diagnostics[index]
		if diagnostic.File == "" || diagnostic.Line < 1 || diagnostic.Column < 1 {
			continue
		}
		if !filepath.IsAbs(diagnostic.File) {
			diagnostic.File = filepath.Join(root, diagnostic.File)
		}
		source, err := os.ReadFile(diagnostic.File)
		if err != nil {
			continue
		}
		line, offset := 1, 0
		for _, r := range string(source) {
			if line == diagnostic.Line {
				break
			}
			offset += utf16Length(string(r))
			if r == '\n' {
				line++
			}
		}
		diagnostic.Start = offset + diagnostic.Column - 1
		diagnostic.End = diagnostic.Start
		if diagnostic.Start < utf16Length(string(source)) {
			diagnostic.End++
		}
	}
}

func mapDescriptorDiagnostics(root string, components []*Component, diagnostics []Diagnostic) {
	for _, component := range components {
		source, err := os.ReadFile(component.Path)
		if err != nil {
			continue
		}
		for index := range diagnostics {
			diagnostic := &diagnostics[index]
			file := diagnostic.File
			if !filepath.IsAbs(file) {
				file = filepath.Join(root, file)
			}
			if !samePath(file, component.Facade) && !samePath(file, templateCheckPath(component.Render)) {
				continue
			}
			generated, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			lines := strings.Split(string(generated), "\n")
			if diagnostic.Line < 1 || diagnostic.Line > len(lines) {
				continue
			}
			line := lines[diagnostic.Line-1]
			field := ""
			switch {
			case strings.Contains(line, "__angulusCtor"), strings.Contains(line, "import type {"+component.Descriptor.Export+" as __AngulusContext"):
				field = "export"
				if diagnostic.Code == "TS2307" {
					field = "module"
				}
			case strings.HasPrefix(line, "import type {") && strings.Contains(line, "__AngulusChild"):
				field = "imports"
			}
			if field == "" {
				continue
			}
			offset := fieldOffset(string(source), field)
			if offset < 0 {
				continue
			}
			diagnostic.File = component.Path
			diagnostic.Start = utf16Length(string(source[:offset]))
			diagnostic.End = diagnostic.Start + len(field) + 2
			setSourceLocation(diagnostic, string(source))
		}
	}
}

func appendUniqueDiagnostics(existing, added []Diagnostic) []Diagnostic {
	seen := make(map[string]bool, len(existing))
	for _, diagnostic := range existing {
		key := fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s", diagnostic.File, diagnostic.Line, diagnostic.Column,
			diagnostic.Code, diagnostic.Message)
		seen[key] = true
	}
	for _, diagnostic := range added {
		key := fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s", diagnostic.File, diagnostic.Line, diagnostic.Column,
			diagnostic.Code, diagnostic.Message)
		if !seen[key] {
			existing = append(existing, diagnostic)
			seen[key] = true
		}
	}
	return existing
}

func mapTemplateDiagnostics(root string, components []*Component, diagnostics []Diagnostic) {
	for _, component := range components {
		checkFile := templateCheckPath(component.Render)
		checkSource, err := os.ReadFile(checkFile)
		if err != nil {
			continue
		}
		templateSource, err := os.ReadFile(component.Template)
		if err != nil {
			continue
		}
		var mappings []templateCheckMap
		var expressions []templateExpressionMap
		for _, line := range strings.Split(string(checkSource), "\n") {
			if data, ok := strings.CutPrefix(line, "// @angulus-expression-map "); ok {
				var mapping templateExpressionMap
				if err := json.Unmarshal([]byte(data), &mapping); err == nil {
					expressions = append(expressions, mapping)
				}
				continue
			}
			fields := strings.Fields(line)
			if len(fields) != 6 || fields[0] != "//" || fields[1] != "@angulus-template-map" {
				continue
			}
			values := [4]int{}
			valid := true
			for i := range values {
				value, parseErr := strconv.Atoi(fields[i+2])
				if parseErr != nil {
					valid = false
					break
				}
				values[i] = value
			}
			if valid {
				mappings = append(mappings, templateCheckMap{
					firstLine: values[0], lastLine: values[1], sourceStart: values[2], sourceEnd: values[3],
				})
			}
		}
		for i := range diagnostics {
			file := diagnostics[i].File
			if !filepath.IsAbs(file) {
				file = filepath.Join(root, file)
			}
			if !samePath(file, checkFile) {
				continue
			}
			exact := false
			for _, mapping := range expressions {
				offset := diagnostics[i].Start - mapping.GeneratedStart
				if offset < 0 || offset >= len(mapping.Offsets)-1 {
					continue
				}
				diagnostics[i].File = component.Template
				diagnostics[i].Start = mapping.SourceStart + mapping.Offsets[offset]
				diagnostics[i].End = diagnostics[i].Start + 1
				setSourceLocation(&diagnostics[i], string(templateSource))
				exact = true
				break
			}
			if exact {
				continue
			}
			for _, mapping := range mappings {
				if diagnostics[i].Line < mapping.firstLine || diagnostics[i].Line > mapping.lastLine {
					continue
				}
				diagnostics[i].File = component.Template
				diagnostics[i].Start = mapping.sourceStart
				diagnostics[i].End = mapping.sourceEnd
				setSourceLocation(&diagnostics[i], string(templateSource))
				break
			}
		}
	}
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil {
		left = leftAbs
	}
	if rightErr == nil {
		right = rightAbs
	}
	if pathKey(left) == pathKey(right) {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func projectTypeScriptFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && path != root {
			name := entry.Name()
			if name == "node_modules" || name == ".angulus" || name == ".git" || name == "dist" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
		}
		if !entry.IsDir() {
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if ext == ".ts" || ext == ".tsx" || ext == ".mts" || ext == ".cts" {
				files = append(files, path)
			}
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func parseTSGoDiagnostics(output string) []Diagnostic {
	var result []Diagnostic
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if filepath.IsAbs(strings.TrimSpace(line)) && fileExists(strings.TrimSpace(line)) {
			continue
		}
		if m := tsgoDiagnostic.FindStringSubmatch(line); m != nil {
			file := m[1]
			if !filepath.IsAbs(file) {
				file = filepath.Clean(file)
			}
			start := 0
			if n, err := fmt.Sscanf(m[2], "%d", &start); err != nil || n != 1 {
				start = 0
			}
			column := 1
			if n, err := fmt.Sscanf(m[3], "%d", &column); err != nil || n != 1 {
				column = 1
			}
			result = append(result, Diagnostic{File: file, Line: start, Column: column, Code: m[5], Message: m[6],
				Severity: map[string]string{"error": "error", "warning": "warning"}[m[4]]})
		} else if strings.Contains(line, "error") || strings.Contains(line, "warning") {
			result = append(result, Diagnostic{Code: "P0502", Message: line, Severity: "error"})
		}
	}
	return result
}

// CheckWithTimeout is useful for command-line callers that do not have a
// cancellation context.
func CheckWithTimeout(root string, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return Check(ctx, root)
}

func hasErrors(diagnostics []Diagnostic) bool {
	for _, d := range diagnostics {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

func ensureRootExists(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("project root %s is not a directory", root)
	}
	return nil
}
