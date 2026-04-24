package paths

import (
	"path/filepath"
	"testing"
)

func TestIPCPath(t *testing.T) {
	root := t.TempDir()
	p := WithRoot(root)

	if got, want := p.IPCPath(), filepath.Join(root, "daemon.sock"); got != want {
		t.Fatalf("IPCPath() = %q, want %q", got, want)
	}
}
