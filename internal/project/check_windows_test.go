package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSamePathRecognizesWindowsPathAliases(t *testing.T) {
	path := writeFixture(t, t.TempDir(), "long-dependency-directory/declaration-file.d.ts", "export {};")
	if !samePath(path, strings.ToUpper(path)) {
		t.Fatal("case variants must identify the same Windows file")
	}

	longPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	size, err := windows.GetShortPathName(longPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, size)
	length, err := windows.GetShortPathName(longPath, &buffer[0], size)
	if err != nil {
		t.Fatal(err)
	}
	shortPath := windows.UTF16ToString(buffer[:length])
	if filepath.Clean(shortPath) == filepath.Clean(path) {
		t.Skip("8.3 short names are disabled on this filesystem")
	}
	if !samePath(path, shortPath) {
		t.Fatalf("short path %s must identify %s", shortPath, path)
	}
	result := Result{Root: filepath.Dir(path)}
	addCheckerDependencies(&result, shortPath+"\n")
	if len(result.Dependencies) != 1 || !samePath(result.Dependencies[0], path) {
		t.Fatalf("short-name checker dependency was lost: %v", result.Dependencies)
	}

	other := writeFixture(t, filepath.Dir(path), "other.d.ts", "export {};")
	if samePath(shortPath, other) {
		t.Fatal("distinct files must not compare equal")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if samePath(path, shortPath) {
		t.Fatal("different nonexistent paths must not compare equal")
	}
}
