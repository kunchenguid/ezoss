package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewUsesAMHomeWhenSet(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AM_HOME", root)

	p, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if got := p.Root(); got != root {
		t.Fatalf("Root() = %q, want %q", got, root)
	}
}

func TestEnsureDirsCreatesExpectedDirectories(t *testing.T) {
	root := t.TempDir()
	p := WithRoot(filepath.Join(root, ".ezoss"))

	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}

	for _, dir := range []string{p.Root(), p.LogsDir()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", dir)
		}
	}
}
