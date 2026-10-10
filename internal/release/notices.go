package release

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func (t *Tool) nativeNotices(ctx context.Context) ([]byte, error) {
	modules := []string{"github.com/evanw/esbuild", "github.com/tdewolff/parse/v2", "golang.org/x/sys"}
	output, err := t.Run(ctx, t.Root, "go", append([]string{"list", "-m", "-json"}, modules...), nil, true)
	if err != nil {
		return nil, fmt.Errorf("resolve native dependency licenses: %w", err)
	}
	var notices strings.Builder
	notices.WriteString("Third-party software included in the Angulus native CLI\n\n")
	decoder := json.NewDecoder(bytes.NewReader(output))
	for _, expected := range modules {
		var module struct{ Path, Version, Dir string }
		if err := decoder.Decode(&module); err != nil {
			return nil, fmt.Errorf("read dependency license location for %s: %w", expected, err)
		}
		if module.Path != expected || module.Dir == "" {
			return nil, fmt.Errorf("missing module directory for dependency %s", expected)
		}
		license, err := moduleLicense(module.Dir)
		if err != nil {
			return nil, fmt.Errorf("read license for %s: %w", module.Path, err)
		}
		fmt.Fprintf(&notices, "=== %s %s ===\n\n%s\n\n", module.Path, module.Version, license)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("unexpected Go module license metadata after required dependencies")
	}
	license, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if err != nil {
		return nil, fmt.Errorf("read Go runtime license: %w", err)
	}
	fmt.Fprintf(&notices, "=== Go runtime %s ===\n\n%s\n", runtime.Version(), license)
	return []byte(notices.String()), nil
}

func moduleLicense(directory string) ([]byte, error) {
	for _, name := range []string{"LICENSE", "LICENSE.md"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err == nil {
			return data, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("dependency contains neither LICENSE nor LICENSE.md")
}
