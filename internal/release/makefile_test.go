package release_test

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestMakeBuildSucceedsWithoutPreexistingBinDirectory(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	worktree := t.TempDir()

	for _, name := range []string{"go.mod", "go.sum", "Makefile"} {
		copyFile(t, filepath.Join(repoRoot, name), filepath.Join(worktree, name))
	}
	copyDir(t, filepath.Join(repoRoot, "cmd"), filepath.Join(worktree, "cmd"))
	copyDir(t, filepath.Join(repoRoot, "internal"), filepath.Join(worktree, "internal"))

	if _, err := os.Stat(filepath.Join(worktree, "bin")); !os.IsNotExist(err) {
		t.Fatalf("bin directory should not exist before make build, err = %v", err)
	}

	cmd := exec.Command("make", "build")
	cmd.Dir = worktree
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make build error = %v, output = %s", err, output)
	}

	if _, err := os.Stat(filepath.Join(worktree, "bin", hostBinaryName())); err != nil {
		t.Fatalf("expected built binary in bin/: %v", err)
	}
}

func TestMakeBuildEmbedsRequestedVersion(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	worktree := t.TempDir()

	for _, name := range []string{"go.mod", "go.sum", "Makefile"} {
		copyFile(t, filepath.Join(repoRoot, name), filepath.Join(worktree, name))
	}
	copyDir(t, filepath.Join(repoRoot, "cmd"), filepath.Join(worktree, "cmd"))
	copyDir(t, filepath.Join(repoRoot, "internal"), filepath.Join(worktree, "internal"))

	cmd := exec.Command("make", "build", "VERSION=test-build")
	cmd.Dir = worktree
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make build VERSION=test-build error = %v, output = %s", err, output)
	}

	versionCmd := exec.Command(filepath.Join(worktree, "bin", hostBinaryName()), "--version")
	versionOutput, err := versionCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("built binary --version error = %v, output = %s", err, versionOutput)
	}
	if !strings.Contains(string(versionOutput), "test-build") {
		t.Fatalf("built binary version output = %q, want substring %q", string(versionOutput), "test-build")
	}
}

func TestMakeInstallSucceedsWithFreshGOBIN(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	worktree := t.TempDir()

	for _, name := range []string{"go.mod", "go.sum", "Makefile"} {
		copyFile(t, filepath.Join(repoRoot, name), filepath.Join(worktree, name))
	}
	copyDir(t, filepath.Join(repoRoot, "cmd"), filepath.Join(worktree, "cmd"))
	copyDir(t, filepath.Join(repoRoot, "internal"), filepath.Join(worktree, "internal"))

	gobin := filepath.Join(t.TempDir(), "bin")
	if _, err := os.Stat(gobin); !os.IsNotExist(err) {
		t.Fatalf("GOBIN directory should not exist before make install, err = %v", err)
	}

	cmd := exec.Command("make", "install")
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(), "GOBIN="+gobin, "EZOSS_SKIP_DAEMON=1", "EZOSS_NO_UPDATE_CHECK=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make install error = %v, output = %s", err, output)
	}

	if _, err := os.Stat(filepath.Join(gobin, hostBinaryName())); err != nil {
		t.Fatalf("expected installed binary in GOBIN: %v", err)
	}
}

func TestMakeInstallEmbedsRequestedVersion(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	worktree := t.TempDir()

	for _, name := range []string{"go.mod", "go.sum", "Makefile"} {
		copyFile(t, filepath.Join(repoRoot, name), filepath.Join(worktree, name))
	}
	copyDir(t, filepath.Join(repoRoot, "cmd"), filepath.Join(worktree, "cmd"))
	copyDir(t, filepath.Join(repoRoot, "internal"), filepath.Join(worktree, "internal"))

	gobin := filepath.Join(t.TempDir(), "bin")
	cmd := exec.Command("make", "install", "VERSION=test-install")
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(), "GOBIN="+gobin, "EZOSS_SKIP_DAEMON=1", "EZOSS_NO_UPDATE_CHECK=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make install VERSION=test-install error = %v, output = %s", err, output)
	}

	versionCmd := exec.Command(filepath.Join(gobin, hostBinaryName()), "--version")
	versionCmd.Env = append(os.Environ(), "EZOSS_NO_UPDATE_CHECK=1")
	versionOutput, err := versionCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installed binary --version error = %v, output = %s", err, versionOutput)
	}
	if !strings.Contains(string(versionOutput), "test-install") {
		t.Fatalf("installed binary version output = %q, want substring %q", string(versionOutput), "test-install")
	}
}

func TestMakeInstallInstallsDaemonServiceBeforeRestarting(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("ReadFile(Makefile) error = %v", err)
	}

	text := string(data)
	installCmd := `"$$install_bin/$(HOST_BINARY)" daemon install >/dev/null 2>&1 || true`
	restartCmd := `"$$install_bin/$(HOST_BINARY)" daemon restart >/dev/null 2>&1 || true`
	installIdx := strings.Index(text, installCmd)
	if installIdx < 0 {
		t.Fatalf("make install does not install the daemon service before restart; missing %q", installCmd)
	}
	restartIdx := strings.Index(text, restartCmd)
	if restartIdx < 0 {
		t.Fatalf("make install does not restart the daemon; missing %q", restartCmd)
	}
	if installIdx > restartIdx {
		t.Fatalf("make install restarts daemon before installing supervised service")
	}
}

func TestMakeDistProducesExpectedReleaseArtifacts(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	worktree := t.TempDir()

	for _, name := range []string{"go.mod", "go.sum", "Makefile", "LICENSE", "README.md"} {
		copyFile(t, filepath.Join(repoRoot, name), filepath.Join(worktree, name))
	}
	copyDir(t, filepath.Join(repoRoot, "cmd"), filepath.Join(worktree, "cmd"))
	copyDir(t, filepath.Join(repoRoot, "internal"), filepath.Join(worktree, "internal"))

	cmd := exec.Command("make", "dist", "VERSION=test-dist")
	cmd.Dir = worktree
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make dist error = %v, output = %s", err, output)
	}

	entries, err := os.ReadDir(filepath.Join(worktree, "dist"))
	if err != nil {
		t.Fatalf("ReadDir(dist) error = %v", err)
	}

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)

	want := []string{
		"checksums.txt",
		"ezoss-test-dist-darwin-amd64.tar.gz",
		"ezoss-test-dist-darwin-arm64.tar.gz",
		"ezoss-test-dist-linux-amd64.tar.gz",
		"ezoss-test-dist-linux-arm64.tar.gz",
		"ezoss-test-dist-windows-amd64.zip",
		"ezoss-test-dist-windows-arm64.zip",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("dist entries = %v, want %v", got, want)
	}

	checksumsPath := filepath.Join(worktree, "dist", "checksums.txt")
	checksumsData, err := os.ReadFile(checksumsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", checksumsPath, err)
	}
	checksumsText := string(checksumsData)
	for _, name := range want[1:] {
		if !strings.Contains(checksumsText, name) {
			t.Fatalf("checksums.txt does not mention %q:\n%s", name, checksumsText)
		}
	}

	for _, archiveName := range want[1:] {
		archivePath := filepath.Join(worktree, "dist", archiveName)
		dirName := strings.TrimSuffix(strings.TrimSuffix(archiveName, ".tar.gz"), ".zip")
		binaryName := "ezoss"
		var entries []string
		if strings.HasSuffix(archiveName, ".zip") {
			binaryName += ".exe"
			entries = zipFileEntries(t, archivePath)
		} else {
			entries = tarGzEntries(t, archivePath)
		}

		assertArchiveEntriesContain(t, entries,
			filepath.ToSlash(filepath.Join(dirName, binaryName)),
			filepath.ToSlash(filepath.Join(dirName, "LICENSE")),
			filepath.ToSlash(filepath.Join(dirName, "README.md")),
		)
	}

	archiveName := "ezoss-test-dist-" + runtime.GOOS + "-" + runtime.GOARCH
	binaryPath := extractReleaseBinary(t, filepath.Join(worktree, "dist"), archiveName)
	versionCmd := exec.Command(binaryPath, "--version")
	versionOutput, err := versionCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("built release binary --version error = %v, output = %s", err, versionOutput)
	}
	if !strings.Contains(string(versionOutput), "test-dist") {
		t.Fatalf("built release binary version output = %q, want substring %q", string(versionOutput), "test-dist")
	}
}

func TestMakeDistFallsBackToSHA256SumWhenShasumIsUnavailable(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	worktree := t.TempDir()

	for _, name := range []string{"go.mod", "go.sum", "Makefile", "LICENSE", "README.md"} {
		copyFile(t, filepath.Join(repoRoot, name), filepath.Join(worktree, name))
	}
	copyDir(t, filepath.Join(repoRoot, "cmd"), filepath.Join(worktree, "cmd"))
	copyDir(t, filepath.Join(repoRoot, "internal"), filepath.Join(worktree, "internal"))

	toolPath := makeDistToolPathWithoutShasum(t)
	cmd := exec.Command("make", "dist", "VERSION=test-sha256sum")
	cmd.Dir = worktree
	cmd.Env = append([]string{"PATH=" + testPathWithoutShasum(t, toolPath), "GOROOT=" + runtime.GOROOT()}, filteredEnv(os.Environ(), "PATH", "GOROOT")...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make dist with sha256sum fallback error = %v, output = %s", err, output)
	}

	checksumsPath := filepath.Join(worktree, "dist", "checksums.txt")
	checksumsData, err := os.ReadFile(checksumsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", checksumsPath, err)
	}
	if !strings.Contains(string(checksumsData), "ezoss-test-sha256sum-") {
		t.Fatalf("checksums.txt = %q, want generated test-sha256sum entries", string(checksumsData))
	}
}

func extractReleaseBinary(t *testing.T, distDir, archiveBase string) string {
	t.Helper()

	archivePath := filepath.Join(distDir, archiveBase+".tar.gz")
	binaryName := "ezoss"
	if runtime.GOOS == "windows" {
		archivePath = filepath.Join(distDir, archiveBase+".zip")
		binaryName += ".exe"
	}

	extractDir := filepath.Join(distDir, "extract-"+archiveBase)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", extractDir, err)
	}

	if runtime.GOOS == "windows" {
		extractZip(t, archivePath, extractDir)
	} else {
		extractTarGz(t, archivePath, extractDir)
	}

	return filepath.Join(extractDir, archiveBase, binaryName)
}

func hostBinaryName() string {
	if runtime.GOOS == "windows" {
		return "ezoss.exe"
	}
	return "ezoss"
}

func extractTarGz(t *testing.T, archivePath, dstDir string) {
	t.Helper()

	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", archivePath, err)
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip.NewReader(%q) error = %v", archivePath, err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next(%q) error = %v", archivePath, err)
		}
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(filepath.Join(dstDir, header.Name), 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v", filepath.Join(dstDir, header.Name), err)
			}
			continue
		}

		path := filepath.Join(dstDir, header.Name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode())
		if err != nil {
			t.Fatalf("OpenFile(%q) error = %v", path, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			t.Fatalf("Copy(%q) error = %v", path, err)
		}
		if err := out.Close(); err != nil {
			t.Fatalf("Close(%q) error = %v", path, err)
		}
	}
}

func extractZip(t *testing.T, archivePath, dstDir string) {
	t.Helper()

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("zip.OpenReader(%q) error = %v", archivePath, err)
	}
	defer zr.Close()

	for _, file := range zr.File {
		path := filepath.Join(dstDir, file.Name)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v", path, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
		}
		in, err := file.Open()
		if err != nil {
			t.Fatalf("File.Open(%q) error = %v", file.Name, err)
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode())
		if err != nil {
			_ = in.Close()
			t.Fatalf("OpenFile(%q) error = %v", path, err)
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = in.Close()
			_ = out.Close()
			t.Fatalf("Copy(%q) error = %v", path, err)
		}
		if err := in.Close(); err != nil {
			_ = out.Close()
			t.Fatalf("Close(%q) error = %v", file.Name, err)
		}
		if err := out.Close(); err != nil {
			t.Fatalf("Close(%q) error = %v", path, err)
		}
	}
}

func tarGzEntries(t *testing.T, archivePath string) []string {
	t.Helper()

	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", archivePath, err)
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip.NewReader(%q) error = %v", archivePath, err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	var entries []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next(%q) error = %v", archivePath, err)
		}
		entries = append(entries, header.Name)
	}

	return entries
}

func zipFileEntries(t *testing.T, archivePath string) []string {
	t.Helper()

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("zip.OpenReader(%q) error = %v", archivePath, err)
	}
	defer zr.Close()

	entries := make([]string, 0, len(zr.File))
	for _, file := range zr.File {
		entries = append(entries, file.Name)
	}

	return entries
}

func assertArchiveEntriesContain(t *testing.T, got []string, want ...string) {
	t.Helper()

	gotSet := make(map[string]struct{}, len(got))
	for _, name := range got {
		gotSet[name] = struct{}{}
	}

	for _, name := range want {
		if _, ok := gotSet[name]; !ok {
			t.Fatalf("archive entries %v do not contain %q", got, name)
		}
	}
}

func copyDir(t *testing.T, srcDir, dstDir string) {
	t.Helper()

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", srcDir, err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", dstDir, err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())
		if entry.IsDir() {
			copyDir(t, srcPath, dstPath)
			continue
		}
		copyFile(t, srcPath, dstPath)
	}
}

func copyFile(t *testing.T, srcPath, dstPath string) {
	t.Helper()

	srcFile, err := os.Open(srcPath)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", srcPath, err)
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", srcPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(dstPath), err)
	}

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		t.Fatalf("OpenFile(%q) error = %v", dstPath, err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		t.Fatalf("Copy(%q -> %q) error = %v", srcPath, dstPath, err)
	}
}

func makeDistToolPathWithoutShasum(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"make", "go", "cp", "rm", "mkdir"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("LookPath(%q) error = %v", name, err)
		}
		if err := os.Symlink(path, filepath.Join(dir, name)); err != nil {
			t.Fatalf("Symlink(%q) error = %v", name, err)
		}
	}

	shasumPath, err := exec.LookPath("shasum")
	if err != nil {
		t.Fatalf("LookPath(%q) error = %v", "shasum", err)
	}

	sha256sumPath := filepath.Join(dir, "sha256sum")
	sha256sumScript := "#!/bin/sh\nexec \"" + shasumPath + "\" -a 256 \"$@\"\n"
	if err := os.WriteFile(sha256sumPath, []byte(sha256sumScript), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", sha256sumPath, err)
	}

	return dir
}

func testPathWithoutShasum(t *testing.T, toolPath string) string {
	t.Helper()

	pathEntries := []string{toolPath}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(entry, "shasum")); err == nil {
			continue
		}
		if runtime.GOOS == "windows" {
			if _, err := os.Stat(filepath.Join(entry, "shasum.exe")); err == nil {
				continue
			}
		}
		pathEntries = append(pathEntries, entry)
	}
	return strings.Join(pathEntries, string(os.PathListSeparator))
}

func filteredEnv(env []string, keys ...string) []string {
	prefixes := make([]string, 0, len(keys))
	for _, key := range keys {
		prefixes = append(prefixes, key+"=")
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		skip := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, entry)
		}
	}

	return filtered
}
