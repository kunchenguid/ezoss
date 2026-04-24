package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/paths"
)

func fakePathsFor(root string) *paths.Paths {
	p := paths.WithRoot(root)
	_ = p.EnsureDirs()
	return p
}

func TestRunCheckReportsAvailableUpdateAndWritesCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/kunchenguid/ezoss/releases/latest" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.4","assets":[]}`))
	}))
	defer server.Close()

	cachePath := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
	stdout := &bytes.Buffer{}

	err := RunCheck(context.Background(), stdout, CheckOptions{
		AppName:        "ezoss",
		Repo:           "kunchenguid/ezoss",
		CurrentVersion: "v1.2.3",
		APIBaseURL:     server.URL,
		HTTPClient:     server.Client(),
		CachePath:      cachePath,
		Now: func() time.Time {
			return now
		},
	})
	if err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}

	if got := stdout.String(); got != "update available: v1.2.3 -> v1.2.4\n" {
		t.Fatalf("output = %q", got)
	}

	cache := readCache(cachePath)
	if cache == nil {
		t.Fatal("readCache() = nil")
	}
	if cache.LatestVersion != "v1.2.4" {
		t.Fatalf("LatestVersion = %q", cache.LatestVersion)
	}
	if !cache.CheckedAt.Equal(now) {
		t.Fatalf("CheckedAt = %v, want %v", cache.CheckedAt, now)
	}
}

func TestRunCheckReportsUpToDateVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","assets":[]}`))
	}))
	defer server.Close()

	stdout := &bytes.Buffer{}
	err := RunCheck(context.Background(), stdout, CheckOptions{
		AppName:        "ezoss",
		Repo:           "kunchenguid/ezoss",
		CurrentVersion: "v1.2.3",
		APIBaseURL:     server.URL,
		HTTPClient:     server.Client(),
	})
	if err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}

	if got := stdout.String(); got != "ezoss is already up to date (v1.2.3)\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestRunCheckRejectsDevelopmentBuilds(t *testing.T) {
	stdout := &bytes.Buffer{}
	err := RunCheck(context.Background(), stdout, CheckOptions{
		AppName:        "ezoss",
		Repo:           "kunchenguid/ezoss",
		CurrentVersion: "dev",
	})
	if err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "self-update unavailable for development builds") {
		t.Fatalf("output = %q", got)
	}
}

func TestRunReplacesExecutable(t *testing.T) {
	allowInsecureDownloads = true
	t.Cleanup(func() { allowInsecureDownloads = false })

	archiveName := "ezoss-v1.2.4-darwin-arm64.tar.gz"
	archive := makeTarGz(t, map[string][]byte{
		"bin/ezoss": []byte("new-binary"),
	})
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/kunchenguid/ezoss/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v1.2.4","assets":[{"name":%q,"browser_download_url":%q},{"name":"checksums.txt","browser_download_url":%q}]}`,
				archiveName,
				server.URL+"/archive",
				server.URL+"/checksums",
			)
		case "/archive":
			_, _ = w.Write(archive)
		case "/checksums":
			_, _ = fmt.Fprint(w, checksums)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	execPath := filepath.Join(t.TempDir(), "ezoss")
	if err := os.WriteFile(execPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	err := Run(context.Background(), stdout, nil, Options{
		AppName:             "ezoss",
		Repo:                "kunchenguid/ezoss",
		CurrentVersion:      "v1.2.3",
		APIBaseURL:          server.URL,
		HTTPClient:          server.Client(),
		ExecutablePath:      execPath,
		CachePath:           filepath.Join(t.TempDir(), "update-check.json"),
		Platform:            platformSpec{GOOS: "darwin", GOARCH: "arm64"},
		SkipDaemonReset:     true,
		SkipDaemonPathCheck: true,
		Now: func() time.Time {
			return time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	content, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-binary" {
		t.Fatalf("executable content = %q", string(content))
	}
	if got := stdout.String(); got != "updated ezoss from v1.2.3 to v1.2.4\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestRunWithBetaPicksHighestSemverIncludingPrereleases(t *testing.T) {
	allowInsecureDownloads = true
	t.Cleanup(func() { allowInsecureDownloads = false })

	archiveName := "ezoss-v1.3.0-rc.1-darwin-arm64.tar.gz"
	archive := makeTarGz(t, map[string][]byte{
		"bin/ezoss": []byte("rc-binary"),
	})
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	listHits := 0
	latestHits := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/kunchenguid/ezoss/releases":
			listHits++
			fmt.Fprintf(w, `[
				{"tag_name":"v1.2.4","draft":false,"prerelease":false,"assets":[]},
				{"tag_name":"v1.3.0-rc.1","draft":false,"prerelease":true,"assets":[
					{"name":%q,"browser_download_url":%q},
					{"name":"checksums.txt","browser_download_url":%q}
				]},
				{"tag_name":"v1.2.5-alpha","draft":false,"prerelease":true,"assets":[]}
			]`, archiveName, server.URL+"/archive", server.URL+"/checksums")
		case "/repos/kunchenguid/ezoss/releases/latest":
			latestHits++
			t.Fatal("--beta must not call /releases/latest")
		case "/archive":
			_, _ = w.Write(archive)
		case "/checksums":
			_, _ = fmt.Fprint(w, checksums)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	execPath := filepath.Join(t.TempDir(), "ezoss")
	if err := os.WriteFile(execPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	err := Run(context.Background(), stdout, nil, Options{
		AppName:             "ezoss",
		Repo:                "kunchenguid/ezoss",
		CurrentVersion:      "v1.2.4",
		APIBaseURL:          server.URL,
		HTTPClient:          server.Client(),
		ExecutablePath:      execPath,
		CachePath:           filepath.Join(t.TempDir(), "update-check.json"),
		Platform:            platformSpec{GOOS: "darwin", GOARCH: "arm64"},
		Beta:                true,
		SkipDaemonReset:     true,
		SkipDaemonPathCheck: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if listHits != 1 {
		t.Fatalf("/releases hits = %d, want 1", listHits)
	}
	if latestHits != 0 {
		t.Fatalf("/releases/latest hits = %d, want 0 under --beta", latestHits)
	}

	content, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "rc-binary" {
		t.Fatalf("executable content = %q, want rc-binary", string(content))
	}
	if got := stdout.String(); got != "updated ezoss from v1.2.4 to v1.3.0-rc.1\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestEnsureDaemonUsesCurrentExecutableSucceedsWhenNoDaemonRunning(t *testing.T) {
	// PID file does not exist → ReadStatus returns StateStopped → guard
	// passes silently.
	tmp := t.TempDir()
	u := &updater{
		executablePath: filepath.Join(tmp, "ezoss"),
		paths:          fakePathsFor(tmp),
	}
	if err := u.ensureDaemonUsesCurrentExecutable(); err != nil {
		t.Fatalf("guard should pass when no daemon is running, got %v", err)
	}
}

func TestEnsureDaemonUsesCurrentExecutableNoOpWhenSkipFlagSet(t *testing.T) {
	tmp := t.TempDir()
	u := &updater{
		executablePath:      filepath.Join(tmp, "ezoss"),
		paths:               fakePathsFor(tmp),
		skipDaemonPathCheck: true,
	}
	// Even with a real (live) PID, the guard returns nil immediately.
	pidFile := u.paths.PIDPath()
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)
	if err := u.ensureDaemonUsesCurrentExecutable(); err != nil {
		t.Fatalf("guard should be a no-op when skipDaemonPathCheck is set, got %v", err)
	}
}

func TestRunWithoutBetaIgnoresPrereleases(t *testing.T) {
	listHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/kunchenguid/ezoss/releases/latest":
			// GitHub /releases/latest already filters out prereleases server-side.
			fmt.Fprint(w, `{"tag_name":"v1.2.4","assets":[]}`)
		case "/repos/kunchenguid/ezoss/releases":
			listHits++
			t.Fatal("non-beta must not call the list endpoint")
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	cachePath := filepath.Join(t.TempDir(), "update-check.json")
	stdout := &bytes.Buffer{}
	if err := RunCheck(context.Background(), stdout, CheckOptions{
		AppName:        "ezoss",
		Repo:           "kunchenguid/ezoss",
		CurrentVersion: "v1.2.3",
		APIBaseURL:     server.URL,
		HTTPClient:     server.Client(),
		CachePath:      cachePath,
	}); err != nil {
		t.Fatalf("RunCheck() error = %v", err)
	}
	if listHits != 0 {
		t.Fatalf("list endpoint hits = %d, want 0", listHits)
	}
	if !strings.Contains(stdout.String(), "v1.2.4") {
		t.Fatalf("output %q missing latest tag", stdout.String())
	}
}
