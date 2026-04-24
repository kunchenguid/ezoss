package wizard

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// DetectCurrentRepo runs `git remote get-url origin` in the given directory
// and parses the result to an "owner/name" GitHub repo identifier. Returns
// an empty string with no error when no GitHub remote is configured.
func DetectCurrentRepo(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// `git remote get-url` exits non-zero when the remote is missing.
			// Treat that as "no remote configured" rather than a hard error so
			// the wizard can fall through to the manual-entry path.
			return "", nil
		}
		return "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return ParseGitHubRemoteURL(strings.TrimSpace(string(out))), nil
}

// ParseGitHubRemoteURL extracts "owner/name" from a GitHub remote URL.
// Supports the SSH (git@github.com:owner/name(.git)) and HTTPS
// (https://github.com/owner/name(.git)) forms. Returns "" for any URL that
// doesn't point at github.com.
func ParseGitHubRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var path string
	switch {
	case strings.HasPrefix(raw, "git@github.com:"):
		path = strings.TrimPrefix(raw, "git@github.com:")
	case strings.HasPrefix(raw, "ssh://git@github.com/"):
		path = strings.TrimPrefix(raw, "ssh://git@github.com/")
	case strings.HasPrefix(raw, "https://github.com/"):
		path = strings.TrimPrefix(raw, "https://github.com/")
	case strings.HasPrefix(raw, "http://github.com/"):
		path = strings.TrimPrefix(raw, "http://github.com/")
	default:
		return ""
	}

	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")

	owner, name, ok := strings.Cut(path, "/")
	if !ok {
		return ""
	}
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return owner + "/" + name
}
