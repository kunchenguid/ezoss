package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestCurrentVersionPrefersExplicitVersion(t *testing.T) {
	originalVersion := Version
	t.Cleanup(func() {
		Version = originalVersion
	})

	Version = "v1.2.3"

	if got := CurrentVersion(); got != "v1.2.3" {
		t.Fatalf("CurrentVersion() = %q, want %q", got, "v1.2.3")
	}
}

func TestCurrentVersionKeepsExplicitDevVersion(t *testing.T) {
	originalVersion := Version
	originalReadBuildInfo := readBuildInfo
	t.Cleanup(func() {
		Version = originalVersion
		readBuildInfo = originalReadBuildInfo
	})

	Version = "dev"
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{
				Version: "v0.0.0-20260426183701-03f2ae8aa228",
			},
		}, true
	}

	if got := CurrentVersion(); got != "dev" {
		t.Fatalf("CurrentVersion() = %q, want %q", got, "dev")
	}
}

func withBuildVars(t *testing.T) {
	t.Helper()
	originalVersion := Version
	originalCommit := Commit
	originalDate := Date
	t.Cleanup(func() {
		Version = originalVersion
		Commit = originalCommit
		Date = originalDate
	})
}

func TestStringIncludesVersionCommitAndDate(t *testing.T) {
	withBuildVars(t)

	Version = "v1.2.3"
	Commit = "abc123"
	Date = "2026-04-18"

	if got := String(); got != "v1.2.3 (abc123) 2026-04-18" {
		t.Fatalf("String() = %q, want %q", got, "v1.2.3 (abc123) 2026-04-18")
	}
}

func TestStringOmitsUnknownCommitAndDate(t *testing.T) {
	withBuildVars(t)

	Version = "v1.2.3"
	Commit = "unknown"
	Date = "unknown"

	if got := String(); got != "v1.2.3" {
		t.Fatalf("String() = %q, want %q", got, "v1.2.3")
	}
}

func TestStringOmitsUnknownDateOnly(t *testing.T) {
	withBuildVars(t)

	Version = "v1.2.3"
	Commit = "abc123"
	Date = "unknown"

	if got := String(); got != "v1.2.3 (abc123)" {
		t.Fatalf("String() = %q, want %q", got, "v1.2.3 (abc123)")
	}
}

func TestStringOmitsUnknownCommitOnly(t *testing.T) {
	withBuildVars(t)

	Version = "v1.2.3"
	Commit = "unknown"
	Date = "2026-04-18"

	if got := String(); got != "v1.2.3 2026-04-18" {
		t.Fatalf("String() = %q, want %q", got, "v1.2.3 2026-04-18")
	}
}

func TestStringForDevBuildOmitsPlaceholders(t *testing.T) {
	withBuildVars(t)

	Version = "dev"
	Commit = "unknown"
	Date = "unknown"

	if got := String(); got != "dev" {
		t.Fatalf("String() = %q, want %q", got, "dev")
	}
}
