package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"angulus/internal/project"
)

func outputFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func readFile(t *testing.T, file *os.File) string {
	t.Helper()
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReportBuiltIncludesDuration(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		stdout := outputFile(t)
		reportBuilt(eventEmitter{json: jsonOutput, out: stdout}, stdout, project.Result{}, 2, "dist", 1250*time.Millisecond)
		output := readFile(t, stdout)
		if !jsonOutput {
			if !strings.Contains(output, "(1.25s)") {
				t.Fatalf("missing build duration: %s", output)
			}
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(output), &event); err != nil {
			t.Fatal(err)
		}
		if event["type"] != "built" || event["revision"] != float64(2) ||
			event["directory"] != "dist" || event["durationMs"] != float64(1250) {
			t.Fatalf("unexpected build timing event: %#v", event)
		}
	}
}

func TestNetworkOptionsRespectCommandLineOverrides(t *testing.T) {
	config := project.Config{Host: "0.0.0.0", Port: 3210}
	opts, err := parseOptions([]string{"serve", "--host", "127.0.0.1", "--port", "4567"})
	if err != nil {
		t.Fatal(err)
	}

	host, port := networkOptions(opts, config, "serve")
	if host != "127.0.0.1" || port != 4567 {
		t.Fatalf("resolved address = %s:%d", host, port)
	}
	opts, err = parseOptions([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	host, port = networkOptions(opts, config, "serve")
	if host != config.Host || port != config.Port {
		t.Fatalf("config address not used: %s:%d", host, port)
	}
	host, port = networkOptions(options{}, project.Config{}, "preview")
	if host != "localhost" || port != 4173 {
		t.Fatalf("preview defaults = %s:%d", host, port)
	}
}

func TestJavaScriptSnapshotDetectsChangesButIgnoresStyles(t *testing.T) {
	root := t.TempDir()
	js := filepath.Join(root, "app.js")
	css := filepath.Join(root, "app.css")
	if err := os.WriteFile(js, []byte("export const value = 1;"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(css, []byte("body { color: red; }"), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := javaScriptSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(css, []byte("body { color: blue; }"), 0644); err != nil {
		t.Fatal(err)
	}
	after, err := javaScriptSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before["app.js"] != after["app.js"] {
		t.Fatal("CSS edit changed JavaScript snapshot")
	}
	if err := os.WriteFile(js, []byte("export const value = 2;"), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := javaScriptSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if before["app.js"] == changed["app.js"] {
		t.Fatal("JavaScript graph change went undetected")
	}
}

func TestGenerateComponentWritesPlainTypeScriptAndNDJSON(t *testing.T) {
	root := t.TempDir()
	stdout, stderr := outputFile(t), outputFile(t)
	code, err := runCLI([]string{"generate", "component", "product-card", "--root", root, "--json"}, stdout, stderr)
	if err != nil || code != 0 {
		t.Fatalf("generate result = %d, %v", code, err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(readFile(t, stdout))), &event); err != nil {
		t.Fatal(err)
	}
	if event["version"] != float64(1) || event["type"] != "generated" || event["revision"] != float64(1) {
		t.Fatalf("generated event = %#v", event)
	}
	source, err := os.ReadFile(filepath.Join(root, "src", "product-card", "product-card.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "@Component") || strings.Contains(string(source), "generated") ||
		!strings.Contains(string(source), "export class ProductCardComponent") {
		t.Fatalf("generated TypeScript should be ordinary, untransformed source: %s", source)
	}
	for _, name := range []string{"product-card.component.json", "product-card.html", "product-card.css", "product-card.test.ts"} {
		if _, err := os.Stat(filepath.Join(root, "src", "product-card", name)); err != nil {
			t.Fatal(err)
		}
	}
	if readFile(t, stderr) != "" {
		t.Fatalf("JSON mode wrote unexpected diagnostics: %s", readFile(t, stderr))
	}
}

func TestJSONCheckEmitsDiagnosticsWithoutStdoutNoise(t *testing.T) {
	root := t.TempDir()
	stdout, stderr := outputFile(t), outputFile(t)
	code, err := runCLI([]string{"check", "--root", root, "--json"}, stdout, stderr)
	if code != 1 || err == nil {
		t.Fatalf("check result = %d, %v", code, err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(readFile(t, stdout))), &event); err != nil {
		t.Fatal(err)
	}
	if event["version"] != float64(1) || event["type"] != "checked" ||
		event["revision"] != float64(1) || event["valid"] != false {
		t.Fatalf("checked event = %#v", event)
	}
	if readFile(t, stderr) != "" {
		t.Fatalf("JSON mode wrote diagnostics outside the event stream: %s", readFile(t, stderr))
	}
}

func TestJSONCheckReportsStaleRevisionWithoutClearingLatestErrors(t *testing.T) {
	output := outputFile(t)
	reportCheckedState(eventEmitter{json: true, out: output}, outputFile(t),
		project.Result{Diagnostics: []project.Diagnostic{}}, 3, true)
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(readFile(t, output))), &event); err != nil {
		t.Fatal(err)
	}
	if event["revision"] != float64(3) || event["stale"] != true || event["valid"] != true {
		t.Fatalf("stale checked event = %#v", event)
	}
}

func TestConfiguredTestUsesArgvAndReportsOutputSeparately(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANGULUS_TEST_HELPER", "output")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := outputFile(t), outputFile(t)
	eventOutput := outputFile(t)
	emit := eventEmitter{json: true, out: eventOutput}
	code, err := runTests(root, []string{executable, "-test.run=TestAngulusSubprocessHelper"}, emit, stderr)
	if err != nil || code != 0 {
		t.Fatalf("configured test result = %d, %v", code, err)
	}
	if strings.Contains(readFile(t, stdout), "child output") {
		t.Fatal("child test output leaked to CLI stdout")
	}
	childOutput := readFile(t, stderr)
	if !strings.Contains(childOutput, "child output") || !strings.Contains(childOutput, "child diagnostic") {
		t.Fatalf("child output was not forwarded to stderr: %s", childOutput)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(readFile(t, eventOutput))), &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "tested" || event["code"] != float64(0) {
		t.Fatalf("test event = %#v", event)
	}
}

func TestConfiguredTestCancellationStopsSubprocess(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANGULUS_TEST_HELPER", "wait")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stderr, eventOutput := outputFile(t), outputFile(t)
	started := time.Now()
	code, err := runTestsWithContext(ctx, make(chan os.Signal, 1), root,
		[]string{executable, "-test.run=TestAngulusSubprocessHelper"},
		eventEmitter{json: true, out: eventOutput}, stderr)
	if err != nil {
		t.Fatal(err)
	}
	if code != 130 || time.Since(started) > 4*time.Second {
		t.Fatalf("cancelled test returned code %d after %s", code, time.Since(started))
	}
	if !strings.Contains(readFile(t, stderr), "child waiting") {
		t.Fatal("configured test subprocess did not start before cancellation")
	}
}

func TestAngulusSubprocessHelper(t *testing.T) {
	switch os.Getenv("ANGULUS_TEST_HELPER") {
	case "output":
		_, _ = os.Stdout.WriteString("child output\n")
		_, _ = os.Stderr.WriteString("child diagnostic\n")
	case "wait":
		_, _ = os.Stderr.WriteString("child waiting\n")
		time.Sleep(10 * time.Second)
	}
}

func TestRunCLIReportsMalformedConfigAsError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "angulus.config.json"), []byte(`{"surprise":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := outputFile(t), outputFile(t)
	code, err := runCLI([]string{"test", "--root", root, "--json"}, stdout, stderr)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("CLI config error = %d, %v", code, err)
	}
	if readFile(t, stdout) != "" || readFile(t, stderr) != "" {
		t.Fatal("runCLI should leave top-level error event formatting to main")
	}
}

func TestSignalExitStatus(t *testing.T) {
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	if got := signalExitCode(signals); got != 143 {
		t.Fatalf("SIGTERM exit code = %d", got)
	}
}
