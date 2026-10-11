package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigValidatesSupportedFields(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "angulus.config.json")
	if _, err := LoadConfig(root); err != nil {
		t.Fatalf("missing config should be optional: %v", err)
	}
	data := `{"host":"127.0.0.1","port":4321,"proxy":{"/api":"http://localhost:3000"},"test":["node","--test","app.test.ts"],"library":{"entry":"src/index.ts"}}`
	if err := os.WriteFile(configPath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "127.0.0.1" || config.Port != 4321 ||
		config.Proxy["/api"] != "http://localhost:3000" ||
		len(config.Test) != 3 || config.Library.Entry != "src/index.ts" {
		t.Fatalf("decoded config = %+v", config)
	}
}

func TestLoadConfigRejectsUnsupportedAndMalformedFields(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{"unknown top-level field", `{"host":"localhost","ignored":true}`, "unknown field"},
		{"unknown library field", `{"library":{"entry":"src/index.ts","other":true}}`, "unknown field"},
		{"zero port", `{"port":0}`, "port must be"},
		{"noninteger port", `{"port":1.5}`, "port must be"},
		{"empty test argv", `{"test":[]}`, "test must be"},
		{"empty test argument", `{"test":["node",""]}`, "test must be"},
		{"invalid proxy target", `{"proxy":{"/api":"ftp://localhost"}}`, "HTTP or HTTPS"},
		{"invalid proxy prefix", `{"proxy":{"api":"http://localhost"}}`, "URL path prefix"},
		{"null config", `null`, "expected an object"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "angulus.config.json"), []byte(testCase.json), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(root)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("LoadConfig error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestLoadConfigAcceptsIntegerValuedDecimalPort(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "angulus.config.json"), []byte(`{"port":5173.0}`), 0644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.Port != 5173 {
		t.Fatalf("port = %d, want 5173", config.Port)
	}
}
