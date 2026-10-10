package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"angulus/internal/project"
)

const helpText = `Angulus commands:
  angulus serve [--root path] [--host host] [--port port] [--json]
  angulus check [--root path] [--json]
  angulus build [--lib] [--root path] [--out directory] [--json]
  angulus preview [--root path] [--host host] [--port port] [--out directory]
  angulus test [--root path] [--json]
  angulus generate component <kebab-case-name> [--root path] [--force] [--json]

Optional angulus.config.json: {"port":5173,"host":"localhost",
  "proxy":{"/api":"http://localhost:3000"},
  "test":["node","--test","src/app.test.ts"],"library":{"entry":"src/index.ts"}}
JSON output is NDJSON: {version:1,type,revision,...}. "listening" means HTTP
ready, not type-safe; "checked" contains diagnostics and may be stale.
`

type options struct {
	root       string
	host       string
	port       int
	hostSet    bool
	portSet    bool
	out        string
	json       bool
	library    bool
	force      bool
	help       bool
	positional []string
}

type eventEmitter struct {
	json bool
	out  *os.File
}

func (e eventEmitter) emit(event map[string]any) {
	if e.json {
		event["version"] = 1
		data, err := json.Marshal(event)
		if err == nil {
			fmt.Fprintln(e.out, string(data))
		}
	}
}

func main() {
	code, err := runCLI(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		if hasArgument(os.Args[1:], "--json") {
			eventEmitter{json: true, out: os.Stdout}.emit(map[string]any{
				"type": "error", "revision": 0, "message": err.Error(),
			})
		} else {
			fmt.Fprintln(os.Stderr, "Angulus:", err)
		}
		if code == 0 {
			code = 1
		}
	}
	if code != 0 {
		os.Exit(code)
	}
}

func runCLI(args []string, stdout, stderr *os.File) (int, error) {
	opts, err := parseOptions(args)
	if err != nil {
		return 1, err
	}
	emit := eventEmitter{json: opts.json, out: stdout}
	if opts.help || len(opts.positional) == 0 || opts.positional[0] == "help" {
		fmt.Fprint(stdout, helpText)
		return 0, nil
	}
	command := opts.positional[0]
	rest := opts.positional[1:]
	if opts.library && command != "build" {
		return 1, errors.New("--lib is supported only by angulus build")
	}
	switch command {
	case "check", "build", "serve", "preview", "test":
		if len(rest) != 0 {
			return 1, fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
		}
	case "generate":
		if len(rest) != 2 || rest[0] != "component" {
			return 1, errors.New("usage: angulus generate component <name>")
		}
	default:
		return 1, fmt.Errorf("unknown command: %s", command)
	}
	root, err := filepath.Abs(opts.root)
	if err != nil {
		return 1, err
	}
	if command == "generate" {
		files, err := generateComponent(root, rest[1], opts.force)
		if err != nil {
			return 1, err
		}
		if opts.json {
			emit.emit(map[string]any{"type": "generated", "revision": 1, "files": files})
		} else {
			for _, file := range files {
				fmt.Fprintf(stdout, "Created %s\n", file)
			}
		}
		return 0, nil
	}
	if !isDirectory(root) {
		return 1, fmt.Errorf("project root does not exist: %s", root)
	}
	config, err := project.LoadConfig(root)
	if err != nil {
		return 1, err
	}
	host, port := networkOptions(opts, config, command)
	if command == "test" {
		return runTests(root, config.Test, emit, stderr)
	}
	switch command {
	case "check":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		result := project.Check(ctx, root)
		reportChecked(emit, stderr, result, 1)
		if hasErrors(result.Diagnostics) {
			return 1, errors.New("project check failed")
		}
		if !opts.json {
			fmt.Fprintf(stdout, "Checked %d component(s) successfully.\n", len(result.Components))
		}
		return 0, nil
	case "build":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		out := opts.out
		if out == "" {
			out = "dist"
		}
		var result project.Result
		if opts.library {
			result = project.BuildLibrary(ctx, root, out)
		} else {
			result = project.Build(ctx, root, out)
		}
		reportChecked(emit, stderr, result, 1)
		if hasErrors(result.Diagnostics) {
			return 1, errors.New("project build failed")
		}
		outputPath := absoluteFrom(root, out)
		if opts.json {
			emit.emit(map[string]any{"type": "built", "revision": 1, "directory": outputPath})
		} else {
			fmt.Fprintf(stdout, "Built %d component(s) as ESM in %s.\n", len(result.Components), outputPath)
		}
		return 0, nil
	case "serve":
		out := opts.out
		if out == "" {
			out = filepath.Join(".angulus", "dev")
		}
		return serve(root, out, host, port, config.Proxy, emit, stdout, stderr)
	case "preview":
		out := opts.out
		if out == "" {
			out = "dist"
		}
		return preview(root, out, host, port, emit, stdout)
	default:
		return 1, fmt.Errorf("unknown command: %s", command)
	}
}

func parseOptions(args []string) (options, error) {
	root, err := os.Getwd()
	if err != nil {
		return options{}, err
	}
	opts := options{root: root}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--json":
			opts.json = true
		case "--lib":
			opts.library = true
		case "--force":
			opts.force = true
		case "--help", "-h":
			opts.help = true
		case "--root", "--host", "--port", "--out":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
				return options{}, fmt.Errorf("%s requires a value", arg)
			}
			i++
			switch arg {
			case "--root":
				opts.root = args[i]
			case "--host":
				if strings.TrimSpace(args[i]) == "" {
					return options{}, errors.New("host must be a nonempty string")
				}
				opts.host, opts.hostSet = args[i], true
			case "--port":
				opts.port, err = project.ParsePort(args[i])
				if err != nil {
					return options{}, err
				}
				opts.portSet = true
			case "--out":
				opts.out = args[i]
			}
		default:
			if strings.HasPrefix(arg, "-") {
				return options{}, fmt.Errorf("unknown option: %s", arg)
			}
			opts.positional = append(opts.positional, arg)
		}
	}
	return opts, nil
}

func hasArgument(args []string, expected string) bool {
	for _, arg := range args {
		if arg == expected {
			return true
		}
	}
	return false
}

func networkOptions(opts options, config project.Config, command string) (string, int) {
	host := config.Host
	if opts.hostSet {
		host = opts.host
	}
	if host == "" {
		host = "localhost"
	}
	port := config.Port
	if opts.portSet {
		port = opts.port
	}
	if port == 0 {
		if command == "preview" {
			port = 4173
		} else {
			port = 5173
		}
	}
	return host, port
}

func reportChecked(emit eventEmitter, stderr *os.File, result project.Result, revision int) {
	reportCheckedState(emit, stderr, result, revision, false)
}

func reportCheckedState(emit eventEmitter, stderr *os.File, result project.Result, revision int, stale bool) {
	if emit.json {
		valid := !hasErrors(result.Diagnostics)
		emit.emit(map[string]any{"type": "checked", "revision": revision,
			"diagnostics": result.Diagnostics, "valid": valid, "stale": stale})
		return
	}
	if !stale {
		printDiagnostics(stderr, result.Diagnostics)
	}
}

func serve(root, out, host string, port int, proxies map[string]string, emit eventEmitter, stdout, stderr *os.File) (int, error) {
	if !isFile(filepath.Join(root, "index.html")) {
		return 1, errors.New("serve requires an index.html module application entry")
	}
	staticRoot, err := project.ResolveBuildOutput(root, out)
	if err != nil {
		return 1, fmt.Errorf("invalid development output: %w", err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return 1, fmt.Errorf("cannot start development server: %w", err)
	}
	defer listener.Close()
	builder := project.NewBuilder(root, out, project.BuildOptions{Mode: project.BuildDevelopment})
	defer builder.Close()
	ctx, cancel, signals := interruptContext()
	defer cancel()
	result := builder.Build(ctx)
	reportChecked(emit, stderr, result, 1)
	if !hasErrors(result.Diagnostics) && (!isDirectory(staticRoot) || !isFile(filepath.Join(staticRoot, "index.html"))) {
		return 1, errors.New("serve requires an index.html module application entry")
	}
	lastScripts, err := javaScriptSnapshot(staticRoot)
	if err != nil {
		return 1, err
	}
	server, err := project.NewServerWithProxy(staticRoot, true, proxies)
	if err != nil {
		_ = listener.Close()
		return 1, err
	}
	if hasErrors(result.Diagnostics) {
		server.BroadcastError(project.ErrorEventMessage(result.Diagnostics))
	}
	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	defer httpServer.Close()
	errCh := make(chan error, 2)
	go func() { errCh <- httpServer.Serve(listener) }()
	url := "http://" + listener.Addr().String()
	if emit.json {
		emit.emit(map[string]any{"type": "listening", "revision": 0, "url": url})
	} else {
		fmt.Fprintf(stdout, "Angulus dev server: %s\n", url)
	}
	revision := 1
	go func() {
		err := project.WatchSources(ctx, root, []string{staticRoot}, builder.Dependencies, func(paths []string) {
			dependencies := builder.Dependencies()
			before, snapshotErr := project.CaptureSources(root, []string{staticRoot}, dependencies)
			if snapshotErr != nil {
				errCh <- fmt.Errorf("capture source revision: %w", snapshotErr)
				return
			}
			result := builder.Build(ctx)
			if ctx.Err() != nil {
				return
			}
			revision++
			after, snapshotErr := project.CaptureSources(root, []string{staticRoot}, dependencies)
			if snapshotErr != nil {
				errCh <- fmt.Errorf("capture source revision: %w", snapshotErr)
				return
			}
			if !before.Equal(after) {
				reportCheckedState(emit, stderr, result, revision, true)
				return
			}
			var scripts map[string][32]byte
			if !hasErrors(result.Diagnostics) {
				var snapshotErr error
				scripts, snapshotErr = javaScriptSnapshot(staticRoot)
				if snapshotErr != nil {
					result.Diagnostics = append(result.Diagnostics, project.Diagnostic{
						File: staticRoot, Code: "P0800", Message: snapshotErr.Error(), Severity: "error",
					})
				}
			}
			reportChecked(emit, stderr, result, revision)
			if hasErrors(result.Diagnostics) {
				message := project.ErrorEventMessage(result.Diagnostics)
				if !emit.json {
					fmt.Fprintln(stderr, message)
				} else {
					emit.emit(map[string]any{"type": "error", "revision": revision, "message": message})
				}
				server.BroadcastError(message)
				return
			}
			cssOnly := len(paths) > 0 && maps.Equal(lastScripts, scripts)
			for _, file := range paths {
				if strings.ToLower(filepath.Ext(file)) != ".css" {
					cssOnly = false
					break
				}
			}
			if cssOnly {
				server.Broadcast("css")
			} else {
				server.Broadcast("reload")
			}
			lastScripts = scripts
		})
		if err != nil {
			errCh <- fmt.Errorf("watch source changes: %w", err)
		}
	}()
	select {
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := httpServer.Shutdown(shutdown); err != nil {
			return 1, err
		}
		return signalExitCode(signals), nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return 0, nil
		}
		cancel()
		return 1, err
	}
}

func javaScriptSnapshot(root string) (map[string][32]byte, error) {
	files := make(map[string][32]byte)
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return files, nil
	} else if err != nil {
		return nil, err
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[relative] = sha256.Sum256(data)
		return nil
	})
	return files, err
}

func preview(root, out, host string, port int, emit eventEmitter, stdout *os.File) (int, error) {
	staticRoot := absoluteFrom(root, out)
	if !isDirectory(staticRoot) {
		return 1, fmt.Errorf("preview output directory does not exist: %s", staticRoot)
	}
	if !isFile(filepath.Join(staticRoot, "index.html")) {
		return 1, fmt.Errorf("preview output is missing index.html: %s", staticRoot)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return 1, fmt.Errorf("cannot start preview server: %w", err)
	}
	server := &http.Server{Handler: project.NewServer(staticRoot, false).Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, cancel, signals := interruptContext()
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()
	url := "http://" + listener.Addr().String()
	if emit.json {
		emit.emit(map[string]any{"type": "listening", "revision": 0, "url": url})
	} else {
		fmt.Fprintf(stdout, "Angulus preview: %s\n", url)
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return signalExitCode(signals), nil
	}
	return 1, err
}

func interruptContext() (context.Context, context.CancelFunc, <-chan os.Signal) {
	ctx, cancel := context.WithCancel(context.Background())
	notified := make(chan os.Signal, 1)
	caught := make(chan os.Signal, 1)
	signal.Notify(notified, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case received := <-notified:
			caught <- received
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(notified)
		cancel()
	}, caught
}

func signalExitCode(signals <-chan os.Signal) int {
	select {
	case received := <-signals:
		if received == syscall.SIGTERM {
			return 143
		}
		return 130
	default:
		return 0
	}
}

func runTests(root string, argv []string, emit eventEmitter, stderr *os.File) (int, error) {
	ctx, cancel, signals := interruptContext()
	defer cancel()
	return runTestsWithContext(ctx, signals, root, argv, emit, stderr)
}

func runTestsWithContext(ctx context.Context, signals <-chan os.Signal, root string, argv []string, emit eventEmitter, stderr *os.File) (int, error) {
	if !isDirectory(root) {
		return 1, fmt.Errorf("test root does not exist: %s", root)
	}
	if len(argv) == 0 {
		if runtime.GOOS == "windows" {
			argv = []string{"cmd.exe", "/d", "/s", "/c", "npm test"}
		} else {
			argv = []string{"npm", "test"}
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = stderr, stderr
	if !emit.json {
		cmd.Stdout = emit.out
	}
	pathValue := filepath.Join(root, "node_modules", ".bin")
	if existing := os.Getenv("PATH"); existing != "" {
		pathValue += string(os.PathListSeparator) + existing
	}
	cmd.Env = append(os.Environ(), "PATH="+pathValue)
	err := cmd.Run()
	code := 0
	if err != nil {
		if ctx.Err() != nil {
			code = signalExitCode(signals)
			if code == 0 {
				code = 130
			}
		} else {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else {
				return 1, err
			}
		}
	}
	emit.emit(map[string]any{"type": "tested", "revision": 1, "code": code})
	return code, nil
}

var componentName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

func generateComponent(root, name string, force bool) ([]string, error) {
	if !componentName.MatchString(name) {
		return nil, errors.New("component name must be kebab-case without paths (for example: product-card)")
	}
	src := filepath.Join(root, "src")
	if info, err := os.Lstat(src); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to write through symlink: %s", src)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	directory := filepath.Join(src, name)
	if info, err := os.Lstat(directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to write through symlink: %s", directory)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	className := ""
	for _, part := range strings.Split(name, "-") {
		className += strings.ToUpper(part[:1]) + part[1:]
	}
	className += "Component"
	files := []struct {
		name    string
		content string
	}{
		{name + ".ts", fmt.Sprintf("import { signal } from \"@angulus/core\";\n\nexport class %s {\n  readonly count = signal(0);\n\n  increment(): void {\n    this.count.update(value => value + 1);\n  }\n}\n", className)},
		{name + ".component.json", fmt.Sprintf("{\n  \"selector\": \"app-%s\",\n  \"module\": \"./%s.ts\",\n  \"export\": \"%s\",\n  \"template\": \"./%s.html\",\n  \"styles\": [\"./%s.css\"]\n}\n", name, name, className, name, name)},
		{name + ".html", fmt.Sprintf("<section class=\"%s\">\n  <span>Count: {{ count() }}</span>\n  <button (click)=\"increment()\">Increment</button>\n</section>\n", name)},
		{name + ".css", fmt.Sprintf(".%s {\n  display: flex;\n  align-items: center;\n  gap: 12px;\n}\n", name)},
		{name + ".test.ts", fmt.Sprintf("import { test } from \"node:test\";\nimport assert from \"node:assert/strict\";\nimport { %s } from \"./%s\";\n\ntest(\"component instances have independent state\", () => {\n  const first = new %s();\n  const second = new %s();\n  first.increment();\n  assert.equal(first.count(), 1);\n  assert.equal(second.count(), 0);\n});\n", className, name, className, className)},
	}
	destinations := make([]string, 0, len(files))
	for _, file := range files {
		destination := filepath.Join(directory, file.name)
		info, err := os.Lstat(destination)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("refusing to write through symlink: %s", destination)
			}
			if !force {
				return nil, fmt.Errorf("refusing to overwrite %s; use --force", destination)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		destinations = append(destinations, destination)
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return nil, err
	}
	for index, file := range files {
		flags := os.O_WRONLY | os.O_CREATE
		if force {
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_EXCL
		}
		output, err := os.OpenFile(destinations[index], flags, 0644)
		if err != nil {
			return nil, err
		}
		_, writeErr := output.WriteString(file.content)
		closeErr := output.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	sort.Strings(destinations)
	return destinations, nil
}

func absoluteFrom(root, output string) string {
	if filepath.IsAbs(output) {
		return filepath.Clean(output)
	}
	return filepath.Join(root, output)
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func hasErrors(diagnostics []project.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			return true
		}
	}
	return false
}

func printDiagnostics(output *os.File, diagnostics []project.Diagnostic) {
	for _, diagnostic := range diagnostics {
		location := diagnostic.File
		if diagnostic.Line > 0 {
			location += fmt.Sprintf(":%d:%d", diagnostic.Line, diagnostic.Column)
		}
		if location != "" {
			location += ": "
		}
		fmt.Fprintf(output, "%s%s %s\n", location, diagnostic.Code, diagnostic.Message)
	}
}
