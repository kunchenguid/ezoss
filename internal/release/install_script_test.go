package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPowerShellScriptHasTopLevelParamBlock(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.ps1")
	contents, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", installPath, err)
	}

	text := string(contents)
	if !strings.HasPrefix(text, "param(") {
		t.Fatalf("install.ps1 should start with a top-level param block, got prefix %q", text[:min(len(text), 32)])
	}

	if !strings.Contains(text, "[string]$Version") {
		t.Fatalf("install.ps1 is missing the Version parameter")
	}
	if !strings.Contains(text, "[string]$BinDir") {
		t.Fatalf("install.ps1 is missing the BinDir parameter")
	}
	if !strings.Contains(text, "[switch]$Help") {
		t.Fatalf("install.ps1 is missing the Help parameter")
	}
	if !strings.Contains(text, "$ErrorActionPreference = 'Stop'") {
		t.Fatalf("install.ps1 should keep strict error handling enabled")
	}
}

func TestInstallPowerShellScriptHelpDocumentsSupportedEnvironmentOverrides(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.ps1")
	contents, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", installPath, err)
	}

	text := string(contents)
	if !strings.Contains(text, "Usage: install.ps1 [-Version <tag>] [-BinDir <dir>] [-Help]") {
		t.Fatalf("install.ps1 help text does not describe the documented parameters")
	}
	for _, envName := range []string{"OWNER", "REPO", "BINARY", "BIN_DIR", "VERSION", "GITHUB_API_BASE", "GITHUB_DOWNLOAD_BASE"} {
		if !strings.Contains(text, envName) {
			t.Fatalf("install.ps1 help text does not mention %s", envName)
		}
	}
}

func TestInstallPowerShellScriptResolvesLatestVersionAndInstallsWindowsBinary(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.ps1")
	contents, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", installPath, err)
	}

	text := string(contents)
	checks := []string{
		`$apiBase = if ($env:GITHUB_API_BASE) { $env:GITHUB_API_BASE } else { 'https://api.github.com' }`,
		`$downloadBase = if ($env:GITHUB_DOWNLOAD_BASE) { $env:GITHUB_DOWNLOAD_BASE } else { 'https://github.com' }`,
		`Invoke-RestMethod -Uri "$ApiBase/repos/$Owner/$Repo/releases/latest"`,
		`$archiveName = "${binary}-${resolvedVersion}-windows-${arch}.zip"`,
		`$url = "$downloadBase/$owner/$repo/releases/download/$resolvedVersion/$archiveName"`,
		`Invoke-WebRequest -Uri $url -OutFile $archivePath`,
		`Expand-Archive -Path $archivePath -DestinationPath $tmpDir -Force`,
		`$binaryPath = Join-Path $tmpDir "${binary}-${resolvedVersion}-windows-${arch}/${binary}.exe"`,
		`Copy-Item -Path $binaryPath -Destination (Join-Path $BinDir "${binary}.exe") -Force`,
		`Write-Output "installed $binary to $(Join-Path $BinDir "${binary}.exe")"`,
	}

	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("install.ps1 missing %q:\n%s", want, text)
		}
	}
}

func TestInstallPowerShellScriptDownloadsAndVerifiesChecksumsBeforeInstall(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.ps1")
	contents, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", installPath, err)
	}

	text := string(contents)
	checks := []string{
		`$checksumsName = 'checksums.txt'`,
		`$checksumsUrl = "$downloadBase/$owner/$repo/releases/download/$resolvedVersion/$checksumsName"`,
		`$checksumsPath = Join-Path $tmpDir $checksumsName`,
		`Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath`,
		`Get-FileHash -Algorithm SHA256 -Path $archivePath`,
		`Get-Content -Path $checksumsPath`,
		`$expectedChecksum = ($checksums`,
		`throw "checksum not found for $archiveName in $checksumsName"`,
		`throw "checksum mismatch for ${archiveName}: got $actualChecksum expected $expectedChecksum"`,
	}

	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("install.ps1 missing %q:\n%s", want, text)
		}
	}
}

func TestInstallPowerShellScriptRejectsEmptyVersionAndBinDir(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.ps1")
	contents, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", installPath, err)
	}

	text := string(contents)
	checks := []string{
		`if ([string]::IsNullOrWhiteSpace($Version)) {`,
		`throw 'version must not be empty'`,
		`if ([string]::IsNullOrWhiteSpace($BinDir)) {`,
		`throw 'bin dir must not be empty'`,
	}

	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("install.ps1 missing %q:\n%s", want, text)
		}
	}
}

func TestInstallPowerShellScriptPreservesExplicitlyEmptyEnvOverridesForValidation(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.ps1")
	contents, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", installPath, err)
	}

	text := string(contents)
	checks := []string{
		`if (-not $PSBoundParameters.ContainsKey('Version')) {`,
		`if (Test-Path Env:VERSION) {`,
		`$Version = $env:VERSION`,
		`} else {`,
		`$Version = 'latest'`,
		`if (-not $PSBoundParameters.ContainsKey('BinDir')) {`,
		`if (Test-Path Env:BIN_DIR) {`,
		`$BinDir = $env:BIN_DIR`,
		`$BinDir = Join-Path $HOME 'bin'`,
	}

	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("install.ps1 missing %q:\n%s", want, text)
		}
	}

	for _, forbidden := range []string{
		`[string]$Version = $(if ($env:VERSION) { $env:VERSION } else { 'latest' })`,
		`[string]$BinDir = $(if ($env:BIN_DIR) { $env:BIN_DIR } else { (Join-Path $HOME 'bin') })`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("install.ps1 should not treat explicitly empty env overrides as unset; found %q:\n%s", forbidden, text)
		}
	}
}

func TestInstallPowerShellScriptSelectsArchiveFromOSArchitecture(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.ps1")
	contents, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", installPath, err)
	}

	text := string(contents)
	checks := []string{
		`$archName = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()`,
		`'x64' { $arch = 'amd64' }`,
		`'arm64' { $arch = 'arm64' }`,
	}

	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("install.ps1 missing %q:\n%s", want, text)
		}
	}

	if strings.Contains(text, `[System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant()`) {
		t.Fatalf("install.ps1 should not select archives from ProcessArchitecture:\n%s", text)
	}
}

func TestInstallShellScriptReportsMissingVersionValue(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath, "--version")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly succeeded")
	}

	text := string(output)
	if !strings.Contains(text, "missing value for --version") {
		t.Fatalf("install.sh output %q does not mention missing --version value", text)
	}
	if !strings.Contains(text, "Usage: install.sh") {
		t.Fatalf("install.sh output %q does not include usage", text)
	}
}

func TestInstallShellScriptRejectsFlagAsVersionValue(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath, "--version", "--help")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly succeeded")
	}

	text := string(output)
	if !strings.Contains(text, "missing value for --version") {
		t.Fatalf("install.sh output %q does not mention missing --version value", text)
	}
	if !strings.Contains(text, "Usage: install.sh") {
		t.Fatalf("install.sh output %q does not include usage", text)
	}
}

func TestInstallShellScriptReportsMissingBinDirValue(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath, "--bin-dir")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly succeeded")
	}

	text := string(output)
	if !strings.Contains(text, "missing value for --bin-dir") {
		t.Fatalf("install.sh output %q does not mention missing --bin-dir value", text)
	}
	if !strings.Contains(text, "Usage: install.sh") {
		t.Fatalf("install.sh output %q does not include usage", text)
	}
}

func TestInstallShellScriptRejectsFlagAsBinDirValue(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath, "--bin-dir", "--help")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly succeeded")
	}

	text := string(output)
	if !strings.Contains(text, "missing value for --bin-dir") {
		t.Fatalf("install.sh output %q does not mention missing --bin-dir value", text)
	}
	if !strings.Contains(text, "Usage: install.sh") {
		t.Fatalf("install.sh output %q does not include usage", text)
	}
}

func TestInstallShellScriptRejectsEmptyVersionValue(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath, "--version", "")
	cmd.Env = append(os.Environ(), "PATH=")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly succeeded")
	}

	text := string(output)
	if !strings.Contains(text, "version must not be empty") {
		t.Fatalf("install.sh output %q does not mention empty version", text)
	}
}

func TestInstallShellScriptRejectsEmptyBinDirValue(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath, "--version", "v1.2.3", "--bin-dir", "")
	cmd.Env = append(os.Environ(), "PATH=")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly succeeded")
	}

	text := string(output)
	if !strings.Contains(text, "bin dir must not be empty") {
		t.Fatalf("install.sh output %q does not mention empty bin dir", text)
	}
}

func TestInstallShellScriptRejectsEmptyVersionEnv(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath)
	cmd.Env = append(os.Environ(), "PATH=", "VERSION=")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly succeeded")
	}

	text := string(output)
	if !strings.Contains(text, "version must not be empty") {
		t.Fatalf("install.sh output %q does not mention empty version env", text)
	}
}

func TestInstallShellScriptRejectsEmptyBinDirEnv(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath, "--version", "v1.2.3")
	cmd.Env = append(os.Environ(), "PATH=", "BIN_DIR=")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly succeeded")
	}

	text := string(output)
	if !strings.Contains(text, "bin dir must not be empty") {
		t.Fatalf("install.sh output %q does not mention empty bin dir env", text)
	}
}

func TestInstallShellScriptHelpMentionsHelpFlag(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath, "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh --help error = %v, output = %q", err, string(output))
	}

	text := string(output)
	if !strings.Contains(text, "--help") {
		t.Fatalf("install.sh --help output %q does not mention --help", text)
	}
	if !strings.Contains(text, "-h") {
		t.Fatalf("install.sh --help output %q does not mention -h", text)
	}
}

func TestInstallShellScriptHelpDocumentsSupportedEnvironmentOverrides(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", installPath, "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh --help error = %v, output = %q", err, string(output))
	}

	text := string(output)
	for _, envName := range []string{"OWNER", "REPO", "BINARY", "BIN_DIR", "VERSION", "GITHUB_API_BASE", "GITHUB_DOWNLOAD_BASE"} {
		if !strings.Contains(text, envName) {
			t.Fatalf("install.sh --help output %q does not mention %s", text, envName)
		}
	}
}

func TestInstallShellScriptSupportsGitHubBaseOverrides(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	contents, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", installPath, err)
	}

	text := string(contents)
	checks := []string{
		`GITHUB_API_BASE=${GITHUB_API_BASE:-https://api.github.com}`,
		`GITHUB_DOWNLOAD_BASE=${GITHUB_DOWNLOAD_BASE:-https://github.com}`,
		`github_api "${GITHUB_API_BASE}/repos/${OWNER}/${REPO}/releases/latest"`,
		`checksums_url="${GITHUB_DOWNLOAD_BASE}/${OWNER}/${REPO}/releases/download/${resolved_version}/checksums.txt"`,
		`url="${GITHUB_DOWNLOAD_BASE}/${OWNER}/${REPO}/releases/download/${resolved_version}/${archive_name}"`,
	}

	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("install.sh missing %q:\n%s", want, text)
		}
	}
}

func TestInstallShellScriptResolvesLatestVersionAndInstallsBinary(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	stubDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", stubDir, err)
	}

	logPath := filepath.Join(t.TempDir(), "curl.log")
	checksumLogPath := filepath.Join(t.TempDir(), "shasum.log")
	installLogPath := filepath.Join(t.TempDir(), "install.log")
	installBinDir := filepath.Join(t.TempDir(), "out")

	writeExecutable(t, filepath.Join(stubDir, "uname"), `#!/bin/sh
case "$1" in
	-s) printf 'Linux\n' ;;
	-m) printf 'x86_64\n' ;;
	*) printf 'unexpected uname arg: %s\n' "$1" >&2; exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(stubDir, "curl"), `#!/bin/sh
set -eu
log_path=${TEST_CURL_LOG:?}
printf '%s\n' "$*" >> "$log_path"
if [ "$1" = "-fsSL" ] && [ "$2" = "-H" ]; then
	printf '{"tag_name":"v1.2.3"}'
	exit 0
fi
if [ "$1" = "-fsSL" ] && [ "$2" = "https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/checksums.txt" ] && [ "$3" = "-o" ]; then
	cat <<'EOF' > "$4"
abc123  ezoss-v1.2.3-linux-amd64.tar.gz
EOF
	exit 0
fi
if [ "$1" = "-fsSL" ] && [ "$2" = "https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/ezoss-v1.2.3-linux-amd64.tar.gz" ] && [ "$3" = "-o" ]; then
	: > "$4"
	exit 0
fi
printf 'unexpected curl args: %s\n' "$*" >&2
exit 1
`)
	writeExecutable(t, filepath.Join(stubDir, "shasum"), `#!/bin/sh
set -eu
log_path=${TEST_SHASUM_LOG:?}
printf '%s\n' "$*" >> "$log_path"
checksum_file=$4
grep 'ezoss-v1.2.3-linux-amd64.tar.gz' "$checksum_file" >/dev/null
`)
	writeExecutable(t, filepath.Join(stubDir, "tar"), `#!/bin/sh
set -eu
dest=
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-C" ]; then
		dest=$2
		shift 2
		continue
	fi
	shift
done
[ -n "$dest" ] || { printf 'missing -C for tar\n' >&2; exit 1; }
mkdir -p "$dest/ezoss-v1.2.3-linux-amd64"
printf '#!/bin/sh\nexit 0\n' > "$dest/ezoss-v1.2.3-linux-amd64/ezoss"
chmod +x "$dest/ezoss-v1.2.3-linux-amd64/ezoss"
`)
	writeExecutable(t, filepath.Join(stubDir, "install"), `#!/bin/sh
set -eu
log_path=${TEST_INSTALL_LOG:?}
src=$3
dst=$4
printf '%s -> %s\n' "$src" "$dst" >> "$log_path"
mkdir -p "$(dirname "$dst")"
cp "$src" "$dst"
chmod 0755 "$dst"
`)

	cmd := exec.Command("sh", installPath)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TEST_CURL_LOG="+logPath,
		"TEST_SHASUM_LOG="+checksumLogPath,
		"TEST_INSTALL_LOG="+installLogPath,
		"BIN_DIR="+installBinDir,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh error = %v, output = %s", err, output)
	}

	installedPath := filepath.Join(installBinDir, "ezoss")
	if _, err := os.Stat(installedPath); err != nil {
		t.Fatalf("expected installed binary at %q: %v", installedPath, err)
	}

	curlLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", logPath, err)
	}
	for _, want := range []string{
		"-fsSL -H Accept: application/vnd.github+json https://api.github.com/repos/kunchenguid/ezoss/releases/latest",
		"-fsSL https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/checksums.txt -o",
		"-fsSL https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/ezoss-v1.2.3-linux-amd64.tar.gz -o",
	} {
		if !strings.Contains(string(curlLog), want) {
			t.Fatalf("curl log %q does not contain %q", string(curlLog), want)
		}
	}

	shasumLog, err := os.ReadFile(checksumLogPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", checksumLogPath, err)
	}
	if !strings.Contains(string(shasumLog), "-a 256 -c") {
		t.Fatalf("shasum log %q does not contain checksum verification invocation", string(shasumLog))
	}

	installLog, err := os.ReadFile(installLogPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", installLogPath, err)
	}
	if !strings.Contains(string(installLog), filepath.Join("ezoss-v1.2.3-linux-amd64", "ezoss")+" -> "+installedPath) {
		t.Fatalf("install log %q does not mention installed binary path", string(installLog))
	}

	text := string(output)
	if !strings.Contains(text, "installed ezoss to "+installedPath) {
		t.Fatalf("install.sh output %q does not mention final install path", text)
	}
}

func TestInstallShellScriptDownloadsAndVerifiesChecksumsBeforeInstall(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	stubDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", stubDir, err)
	}

	logPath := filepath.Join(t.TempDir(), "curl.log")
	checksumLogPath := filepath.Join(t.TempDir(), "shasum.log")
	installBinDir := filepath.Join(t.TempDir(), "out")

	writeExecutable(t, filepath.Join(stubDir, "uname"), `#!/bin/sh
case "$1" in
	-s) printf 'Linux\n' ;;
	-m) printf 'x86_64\n' ;;
	*) printf 'unexpected uname arg: %s\n' "$1" >&2; exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(stubDir, "curl"), `#!/bin/sh
set -eu
log_path=${TEST_CURL_LOG:?}
printf '%s\n' "$*" >> "$log_path"
if [ "$1" = "-fsSL" ] && [ "$2" = "-H" ]; then
	printf '{"tag_name":"v1.2.3"}'
	exit 0
fi
if [ "$1" = "-fsSL" ] && [ "$2" = "https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/checksums.txt" ] && [ "$3" = "-o" ]; then
	cat <<'EOF' > "$4"
abc123  ezoss-v1.2.3-linux-amd64.tar.gz
EOF
	exit 0
fi
if [ "$1" = "-fsSL" ] && [ "$2" = "https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/ezoss-v1.2.3-linux-amd64.tar.gz" ] && [ "$3" = "-o" ]; then
	: > "$4"
	exit 0
fi
printf 'unexpected curl args: %s\n' "$*" >&2
exit 1
`)
	writeExecutable(t, filepath.Join(stubDir, "shasum"), `#!/bin/sh
set -eu
log_path=${TEST_SHASUM_LOG:?}
printf '%s\n' "$*" >> "$log_path"
checksum_file=$4
grep 'ezoss-v1.2.3-linux-amd64.tar.gz' "$checksum_file" >/dev/null
`)
	writeExecutable(t, filepath.Join(stubDir, "tar"), `#!/bin/sh
set -eu
dest=
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-C" ]; then
		dest=$2
		shift 2
		continue
	fi
	shift
done
[ -n "$dest" ] || { printf 'missing -C for tar\n' >&2; exit 1; }
mkdir -p "$dest/ezoss-v1.2.3-linux-amd64"
printf '#!/bin/sh\nexit 0\n' > "$dest/ezoss-v1.2.3-linux-amd64/ezoss"
chmod +x "$dest/ezoss-v1.2.3-linux-amd64/ezoss"
`)
	writeExecutable(t, filepath.Join(stubDir, "install"), `#!/bin/sh
set -eu
src=$3
dst=$4
mkdir -p "$(dirname "$dst")"
cp "$src" "$dst"
chmod 0755 "$dst"
`)

	cmd := exec.Command("sh", installPath)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TEST_CURL_LOG="+logPath,
		"TEST_SHASUM_LOG="+checksumLogPath,
		"BIN_DIR="+installBinDir,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh error = %v, output = %s", err, output)
	}

	shasumLog, err := os.ReadFile(checksumLogPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", checksumLogPath, err)
	}
	if !strings.Contains(string(shasumLog), "-a 256 -c") {
		t.Fatalf("shasum log %q does not contain checksum verification invocation", string(shasumLog))
	}

	curlLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", logPath, err)
	}
	for _, want := range []string{
		"-fsSL -H Accept: application/vnd.github+json https://api.github.com/repos/kunchenguid/ezoss/releases/latest",
		"-fsSL https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/checksums.txt -o",
		"-fsSL https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/ezoss-v1.2.3-linux-amd64.tar.gz -o",
	} {
		if !strings.Contains(string(curlLog), want) {
			t.Fatalf("curl log %q does not contain %q", string(curlLog), want)
		}
	}
}

func TestInstallShellScriptVerifiesChecksumAgainstDownloadedArchivePath(t *testing.T) {
	installPath, err := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("Abs(install.sh) error = %v", err)
	}
	stubDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", stubDir, err)
	}

	installBinDir := filepath.Join(t.TempDir(), "out")
	workingDir := t.TempDir()

	writeExecutable(t, filepath.Join(stubDir, "uname"), `#!/bin/sh
case "$1" in
	-s) printf 'Linux\n' ;;
	-m) printf 'x86_64\n' ;;
	*) printf 'unexpected uname arg: %s\n' "$1" >&2; exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(stubDir, "curl"), `#!/bin/sh
set -eu
if [ "$1" = "-fsSL" ] && [ "$2" = "-H" ]; then
	printf '{"tag_name":"v1.2.3"}'
	exit 0
fi
if [ "$1" = "-fsSL" ] && [ "$2" = "https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/checksums.txt" ] && [ "$3" = "-o" ]; then
	cat <<'EOF' > "$4"
abc123  ezoss-v1.2.3-linux-amd64.tar.gz
EOF
	exit 0
fi
if [ "$1" = "-fsSL" ] && [ "$2" = "https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/ezoss-v1.2.3-linux-amd64.tar.gz" ] && [ "$3" = "-o" ]; then
	: > "$4"
	exit 0
fi
printf 'unexpected curl args: %s\n' "$*" >&2
exit 1
`)
	writeExecutable(t, filepath.Join(stubDir, "shasum"), `#!/bin/sh
set -eu
checksum_file=$4
entry=$(cat "$checksum_file")
file_path=${entry#*  }
[ -n "$file_path" ] || { printf 'missing checksum entry path\n' >&2; exit 1; }
[ -f "$file_path" ] || { printf 'checksum target not found: %s\n' "$file_path" >&2; exit 1; }
`)
	writeExecutable(t, filepath.Join(stubDir, "tar"), `#!/bin/sh
set -eu
dest=
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-C" ]; then
		dest=$2
		shift 2
		continue
	fi
	shift
done
[ -n "$dest" ] || { printf 'missing -C for tar\n' >&2; exit 1; }
mkdir -p "$dest/ezoss-v1.2.3-linux-amd64"
printf '#!/bin/sh\nexit 0\n' > "$dest/ezoss-v1.2.3-linux-amd64/ezoss"
chmod +x "$dest/ezoss-v1.2.3-linux-amd64/ezoss"
`)
	writeExecutable(t, filepath.Join(stubDir, "install"), `#!/bin/sh
set -eu
src=$3
dst=$4
mkdir -p "$(dirname "$dst")"
cp "$src" "$dst"
chmod 0755 "$dst"
`)

	cmd := exec.Command("sh", installPath)
	cmd.Dir = workingDir
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BIN_DIR="+installBinDir,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh error = %v, output = %s", err, output)
	}

	installedPath := filepath.Join(installBinDir, "ezoss")
	if _, err := os.Stat(installedPath); err != nil {
		t.Fatalf("expected installed binary at %q: %v", installedPath, err)
	}
}

func TestInstallShellScriptFallsBackToSha256sumWhenShasumIsUnavailable(t *testing.T) {
	installPath := filepath.Join("..", "..", "install.sh")
	stubDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", stubDir, err)
	}

	checksumLogPath := filepath.Join(t.TempDir(), "sha256sum.log")
	installBinDir := filepath.Join(t.TempDir(), "out")

	writeExecutable(t, filepath.Join(stubDir, "uname"), `#!/bin/sh
case "$1" in
	-s) printf 'Linux\n' ;;
	-m) printf 'x86_64\n' ;;
	*) printf 'unexpected uname arg: %s\n' "$1" >&2; exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(stubDir, "curl"), `#!/bin/sh
set -eu
if [ "$1" = "-fsSL" ] && [ "$2" = "-H" ]; then
	printf '{"tag_name":"v1.2.3"}'
	exit 0
fi
if [ "$1" = "-fsSL" ] && [ "$2" = "https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/checksums.txt" ] && [ "$3" = "-o" ]; then
	cat <<'EOF' > "$4"
abc123  ezoss-v1.2.3-linux-amd64.tar.gz
EOF
	exit 0
fi
if [ "$1" = "-fsSL" ] && [ "$2" = "https://github.com/kunchenguid/ezoss/releases/download/v1.2.3/ezoss-v1.2.3-linux-amd64.tar.gz" ] && [ "$3" = "-o" ]; then
	: > "$4"
	exit 0
fi
printf 'unexpected curl args: %s\n' "$*" >&2
exit 1
`)
	writeExecutable(t, filepath.Join(stubDir, "sed"), "#!/bin/sh\nexec /usr/bin/sed \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "head"), "#!/bin/sh\nexec /usr/bin/head \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "grep"), "#!/bin/sh\nexec /usr/bin/grep \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "mktemp"), "#!/bin/sh\nexec /usr/bin/mktemp \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "mkdir"), "#!/bin/sh\nexec /bin/mkdir \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "rm"), "#!/bin/sh\nexec /bin/rm \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "dirname"), "#!/bin/sh\nexec /usr/bin/dirname \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "cp"), "#!/bin/sh\nexec /bin/cp \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "chmod"), "#!/bin/sh\nexec /bin/chmod \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "cat"), "#!/bin/sh\nexec /bin/cat \"$@\"\n")
	writeExecutable(t, filepath.Join(stubDir, "sha256sum"), `#!/bin/sh
set -eu
log_path=${TEST_SHA256SUM_LOG:?}
printf '%s\n' "$*" >> "$log_path"
checksum_file=$2
grep 'ezoss-v1.2.3-linux-amd64.tar.gz' "$checksum_file" >/dev/null
`)
	writeExecutable(t, filepath.Join(stubDir, "tar"), `#!/bin/sh
set -eu
dest=
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-C" ]; then
		dest=$2
		shift 2
		continue
	fi
	shift
done
[ -n "$dest" ] || { printf 'missing -C for tar\n' >&2; exit 1; }
mkdir -p "$dest/ezoss-v1.2.3-linux-amd64"
printf '#!/bin/sh\nexit 0\n' > "$dest/ezoss-v1.2.3-linux-amd64/ezoss"
chmod +x "$dest/ezoss-v1.2.3-linux-amd64/ezoss"
`)
	writeExecutable(t, filepath.Join(stubDir, "install"), `#!/bin/sh
set -eu
src=$3
dst=$4
mkdir -p "$(dirname "$dst")"
cp "$src" "$dst"
chmod 0755 "$dst"
`)

	cmd := exec.Command("sh", installPath)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir,
		"TEST_SHA256SUM_LOG="+checksumLogPath,
		"BIN_DIR="+installBinDir,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh error = %v, output = %s", err, output)
	}

	checksumLog, err := os.ReadFile(checksumLogPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", checksumLogPath, err)
	}
	if !strings.Contains(string(checksumLog), "-c") {
		t.Fatalf("sha256sum log %q does not contain checksum verification invocation", string(checksumLog))
	}

	installedPath := filepath.Join(installBinDir, "ezoss")
	if _, err := os.Stat(installedPath); err != nil {
		t.Fatalf("expected installed binary at %q: %v", installedPath, err)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
