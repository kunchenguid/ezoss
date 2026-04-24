package update

import "os/exec"

// commandFor is a thin shim so daemon.go can build an *exec.Cmd without an
// extra os/exec import in that file's hot path.
var commandFor = exec.Command
