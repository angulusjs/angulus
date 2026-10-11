package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the supported angulus.config.json contract.
type Config struct {
	Host    string            `json:"host,omitempty"`
	Port    int               `json:"port,omitempty"`
	Proxy   map[string]string `json:"proxy,omitempty"`
	Test    []string          `json:"test,omitempty"`
	Library LibraryConfig     `json:"library,omitempty"`
}

// LibraryConfig selects the TypeScript entry used by library builds.
type LibraryConfig struct {
	Entry string `json:"entry,omitempty"`
}

// LoadConfig reads and validates angulus.config.json from root. Missing config
// files are equivalent to an empty configuration.
func LoadConfig(root string) (Config, error) {
	file := filepath.Join(root, "angulus.config.json")
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read angulus.config.json: %w", err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Config{}, errors.New("invalid angulus.config.json: expected an object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Config{}, fmt.Errorf("invalid angulus.config.json: %w", err)
	}
	port := 0
	if rawPort, ok := fields["port"]; ok {
		numberDecoder := json.NewDecoder(bytes.NewReader(rawPort))
		numberDecoder.UseNumber()
		var value any
		if err := numberDecoder.Decode(&value); err != nil {
			return Config{}, errors.New("invalid angulus.config.json: port must be an integer between 1 and 65535")
		}
		number, ok := value.(json.Number)
		if !ok {
			return Config{}, errors.New("invalid angulus.config.json: port must be an integer between 1 and 65535")
		}
		valueFloat, err := number.Float64()
		if err != nil || math.IsInf(valueFloat, 0) || math.IsNaN(valueFloat) ||
			math.Trunc(valueFloat) != valueFloat || valueFloat < 1 || valueFloat > 65535 {
			return Config{}, errors.New("invalid angulus.config.json: port must be an integer between 1 and 65535")
		}
		port = int(valueFloat)
		delete(fields, "port")
	}
	if raw, ok := fields["host"]; ok {
		var host string
		if err := json.Unmarshal(raw, &host); err != nil || strings.TrimSpace(host) == "" {
			return Config{}, errors.New("invalid angulus.config.json: host must be a nonempty string")
		}
	}
	for name, opener := range map[string]byte{"proxy": '{', "test": '[', "library": '{'} {
		if raw, ok := fields[name]; ok && (len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != opener) {
			return Config{}, fmt.Errorf("invalid angulus.config.json: %s must be a %s", name, map[byte]string{'{': "object", '[': "nonempty command argv array"}[opener])
		}
	}
	if rawLibrary, ok := fields["library"]; ok {
		var library map[string]json.RawMessage
		if err := json.Unmarshal(rawLibrary, &library); err != nil {
			return Config{}, fmt.Errorf("invalid angulus.config.json: library must be an object: %w", err)
		}
		if rawEntry, hasEntry := library["entry"]; hasEntry {
			var entry string
			if err := json.Unmarshal(rawEntry, &entry); err != nil || strings.TrimSpace(entry) == "" {
				return Config{}, errors.New(`invalid angulus.config.json: library.entry must be a nonempty TypeScript path`)
			}
		}
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return Config{}, fmt.Errorf("invalid angulus.config.json: %w", err)
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("invalid angulus.config.json: %w", err)
	}
	config.Port = port
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid angulus.config.json: %w", err)
	}
	return config, nil
}

// Validate rejects unsupported or malformed values instead of silently
// accepting configuration that the native CLI cannot honor.
func (c Config) Validate() error {
	if c.Host != "" && strings.TrimSpace(c.Host) == "" {
		return errors.New("host must be a nonempty string")
	}
	if c.Port < 0 || c.Port > 65535 {
		return errors.New("port must be an integer between 1 and 65535")
	}
	for prefix, target := range c.Proxy {
		if prefix == "" || !strings.HasPrefix(prefix, "/") ||
			path.Clean(prefix) != prefix || strings.ContainsAny(prefix, "?#") {
			return fmt.Errorf("proxy key %q must be a clean URL path prefix", prefix)
		}
		parsed, err := url.Parse(target)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host == "" {
			return fmt.Errorf("proxy target for %q must be an HTTP or HTTPS URL", prefix)
		}
	}
	if c.Test != nil {
		if len(c.Test) == 0 {
			return errors.New("test must be a nonempty command argv array")
		}
		for _, arg := range c.Test {
			if arg == "" || strings.ContainsRune(arg, '\x00') {
				return errors.New("test must be a nonempty command argv array")
			}
		}
	}
	if c.Library.Entry != "" && strings.TrimSpace(c.Library.Entry) == "" {
		return errors.New(`library.entry must be a nonempty TypeScript path`)
	}
	return nil
}

// ParsePort validates a command-line port value.
func ParsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("port must be an integer between 1 and 65535")
	}
	return port, nil
}
