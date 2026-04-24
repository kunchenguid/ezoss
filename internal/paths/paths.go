package paths

import (
	"os"
	"path/filepath"
)

type Paths struct {
	root string
}

func New() (*Paths, error) {
	if env := os.Getenv("AM_HOME"); env != "" {
		return &Paths{root: env}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Paths{root: filepath.Join(home, ".ezoss")}, nil
}

func WithRoot(root string) *Paths {
	return &Paths{root: root}
}

func (p *Paths) Root() string    { return p.root }
func (p *Paths) LogsDir() string { return filepath.Join(p.root, "logs") }
func (p *Paths) DBPath() string  { return filepath.Join(p.root, "ezoss.db") }
func (p *Paths) PIDPath() string { return filepath.Join(p.root, "daemon.pid") }
func (p *Paths) IPCPath() string { return filepath.Join(p.root, "daemon.sock") }
func (p *Paths) UpdateCheckPath() string {
	return filepath.Join(p.root, "update-check.json")
}

func (p *Paths) EnsureDirs() error {
	for _, dir := range []string{p.root, p.LogsDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
