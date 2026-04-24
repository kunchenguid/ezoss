package release_test

import (
	"testing"

	"github.com/kunchenguid/ezoss/internal/release"
)

func TestArchiveNameUsesPlatformSpecificExtension(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		goos    string
		goarch  string
		want    string
	}{
		{
			name:    "unix archive",
			version: "v1.2.3",
			goos:    "linux",
			goarch:  "amd64",
			want:    "ezoss-v1.2.3-linux-amd64.tar.gz",
		},
		{
			name:    "windows archive",
			version: "v1.2.3",
			goos:    "windows",
			goarch:  "arm64",
			want:    "ezoss-v1.2.3-windows-arm64.zip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := release.ArchiveName(tt.version, tt.goos, tt.goarch); got != tt.want {
				t.Fatalf("ArchiveName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBinaryNameAddsExeOnWindows(t *testing.T) {
	t.Parallel()

	if got := release.BinaryName("windows"); got != "ezoss.exe" {
		t.Fatalf("BinaryName(windows) = %q, want %q", got, "ezoss.exe")
	}

	if got := release.BinaryName("darwin"); got != "ezoss" {
		t.Fatalf("BinaryName(darwin) = %q, want %q", got, "ezoss")
	}
}

func TestDirectoryNamePreservesVersionPrefix(t *testing.T) {
	t.Parallel()

	if got := release.DirectoryName("v1.2.3", "darwin", "arm64"); got != "ezoss-v1.2.3-darwin-arm64" {
		t.Fatalf("DirectoryName() = %q", got)
	}
}
