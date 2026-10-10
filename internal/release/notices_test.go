package release

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeNoticesIncludeDependencyAndGoRuntimeLicenses(t *testing.T) {
	root := t.TempDir()
	var output []byte
	modules := []string{"github.com/evanw/esbuild", "github.com/tdewolff/parse/v2", "golang.org/x/sys"}
	for index, path := range modules {
		dir := filepath.Join(root, string(rune('a'+index)))
		must(t, os.MkdirAll(dir, 0755))
		must(t, os.WriteFile(filepath.Join(dir, "LICENSE.md"), []byte("Copyright "+path), 0644))
		data, err := json.Marshal(map[string]string{"Path": path, "Version": "v1.0.0", "Dir": dir})
		must(t, err)
		output = append(output, data...)
		output = append(output, '\n')
	}
	tool := New(root)
	tool.Run = func(_ context.Context, cwd, command string, args, env []string, capture bool) ([]byte, error) {
		if cwd != root || command != "go" || !capture || strings.Join(args[:3], " ") != "list -m -json" {
			t.Fatalf("unexpected license discovery command: %s %v", command, args)
		}
		return output, nil
	}
	notices, err := tool.nativeNotices(context.Background())
	must(t, err)
	for _, path := range modules {
		if !strings.Contains(string(notices), "Copyright "+path) {
			t.Fatalf("missing native dependency attribution for %s", path)
		}
	}
	if !strings.Contains(string(notices), "The Go Authors") {
		t.Fatal("missing Go runtime license")
	}
}

func TestModuleLicenseRequiresAnActualLicenseFile(t *testing.T) {
	_, err := moduleLicense(t.TempDir())
	fails(t, err, "neither LICENSE nor LICENSE.md")
}

func TestRuntimePackagingKeepsLinkedSourceMaps(t *testing.T) {
	source, destination := t.TempDir(), filepath.Join(t.TempDir(), "dist")
	must(t, os.WriteFile(filepath.Join(source, "index.js"), []byte("export {};\n//# sourceMappingURL=index.js.map\n"), 0644))
	must(t, os.WriteFile(filepath.Join(source, "index.js.map"), []byte(`{"version":3}`), 0644))
	must(t, os.WriteFile(filepath.Join(source, "index.d.ts"), []byte("export {};\n"), 0644))
	must(t, copySources(source, destination, false))
	for _, file := range []string{"index.js", "index.js.map", "index.d.ts"} {
		_, err := os.Stat(filepath.Join(destination, file))
		must(t, err)
	}
}
