package project

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckerDependenciesPreserveSymlinkedPathIdentity(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "real/node_modules/type-only/index.d.ts", "export interface RemoteType { value: number; }")
	aliasRoot := filepath.Join(root, "alias")
	alias := filepath.Join(aliasRoot, "node_modules", "type-only", "index.d.ts")
	if runtime.GOOS == "windows" {
		// Hard links exercise file identity without requiring symlink privileges.
		if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, alias); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Symlink(filepath.Join(root, "real"), aliasRoot); err != nil {
		t.Fatal(err)
	}
	if !samePath(path, alias) || !samePath(alias, path) {
		t.Fatalf("symlink and resolved paths must identify the same dependency: %s, %s", alias, path)
	}

	result := Result{Root: aliasRoot}
	addCheckerDependencies(&result, path+"\n")
	dependencies := mergeBuildDependencies(aliasRoot, nil, result.Dependencies)
	if len(dependencies) != 1 || !samePath(dependencies[0], alias) {
		t.Fatalf("resolved checker dependency must remain accessible through the project alias: %v", dependencies)
	}
	other := writeFixture(t, root, "real/node_modules/type-only/other.d.ts", "export {};")
	if samePath(alias, other) {
		t.Fatal("distinct declarations must not compare equal")
	}
}
