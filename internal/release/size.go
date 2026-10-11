package release

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/evanw/esbuild/pkg/api"
)

func (t *Tool) Size() error {
	source, err := os.ReadFile(filepath.Join(t.Root, "packages", "core", "src", "index.ts"))
	if err != nil {
		return err
	}
	result := api.Transform(string(source), api.TransformOptions{
		Loader: api.LoaderTS, Target: api.ES2022,
		MinifyWhitespace: true, MinifyIdentifiers: true, MinifySyntax: true,
		LogLevel: api.LogLevelSilent,
	})
	if len(result.Errors) != 0 {
		return fmt.Errorf("runtime size transform failed: %s", result.Errors[0].Text)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(result.Code); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	const budget = 10 * 1024
	return json.NewEncoder(t.Out).Encode(struct {
		Package      string `json:"package"`
		Scope        string `json:"scope"`
		Minified     int    `json:"minifiedBytes"`
		Gzip         int    `json:"gzipBytes"`
		Budget       int    `json:"budgetBytes"`
		WithinBudget bool   `json:"withinBudget"`
	}{"@angulus/core", "all public core exports, no router/app/dev tooling", len(result.Code), compressed.Len(), budget, compressed.Len() <= budget})
}
