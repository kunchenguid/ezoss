package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var releasePleaseVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

func TestReleaseWorkflowUploadUsesClobber(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"gh release upload ${{ needs.release-please.outputs.tag_name }} --clobber \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-amd64.tar.gz \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-arm64.tar.gz \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-linux-amd64.tar.gz \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-linux-arm64.tar.gz \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-windows-amd64.zip \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-windows-arm64.zip \\",
		"dist/checksums.txt",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow upload step missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "gh release upload ${{ needs.release-please.outputs.tag_name }} --clobber dist/*") {
		t.Fatalf("release workflow upload step must not pass dist/* because smoke directories may exist:\n%s", text)
	}
}

func TestReleaseWorkflowUsesReleasePleaseAndPublishesTaggedArchives(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"contents: write",
		"pull-requests: write",
		"uses: googleapis/release-please-action@v4",
		"config-file: .release-please-config.json",
		"manifest-file: .release-please-manifest.json",
		"publish-assets:",
		"if: ${{ needs.release-please.outputs.release_created == 'true' }}",
		"run: make dist VERSION=${{ needs.release-please.outputs.tag_name }}",
		"GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowKeepsExpectedTriggerAndConcurrency(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"push:",
		"branches:",
		"- main",
		"concurrency:",
		"group: release",
		"cancel-in-progress: false",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowKeepsExpectedReleasePleaseOutputs(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"outputs:",
		"release_created: ${{ steps.release.outputs.release_created }}",
		"tag_name: ${{ steps.release.outputs.tag_name }}",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowUsesLeastPrivilegeJobPermissions(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"release-please:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: write\n      pull-requests: write",
		"publish-assets:\n    needs:\n      - release-please\n      - smoke-install-windows\n      - smoke-archive-macos\n      - smoke-archive-windows\n    if: ${{ needs.release-please.outputs.release_created == 'true' }}\n    runs-on: ubuntu-latest\n    permissions:\n      contents: write",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowVerifiesDistArtifactsBeforeUpload(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Verify dist artifacts",
		"test -f dist/ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-amd64.tar.gz",
		"test -f dist/ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-arm64.tar.gz",
		"test -f dist/ezoss-${{ needs.release-please.outputs.tag_name }}-linux-amd64.tar.gz",
		"test -f dist/ezoss-${{ needs.release-please.outputs.tag_name }}-linux-arm64.tar.gz",
		"test -f dist/ezoss-${{ needs.release-please.outputs.tag_name }}-windows-amd64.zip",
		"test -f dist/ezoss-${{ needs.release-please.outputs.tag_name }}-windows-arm64.zip",
		"test -f dist/checksums.txt",
		"grep 'ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-amd64.tar.gz' dist/checksums.txt",
		"grep 'ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-arm64.tar.gz' dist/checksums.txt",
		"grep 'ezoss-${{ needs.release-please.outputs.tag_name }}-linux-amd64.tar.gz' dist/checksums.txt",
		"grep 'ezoss-${{ needs.release-please.outputs.tag_name }}-linux-arm64.tar.gz' dist/checksums.txt",
		"grep 'ezoss-${{ needs.release-please.outputs.tag_name }}-windows-amd64.zip' dist/checksums.txt",
		"grep 'ezoss-${{ needs.release-please.outputs.tag_name }}-windows-arm64.zip' dist/checksums.txt",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowVerifiesChecksumIntegrityBeforeUpload(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"while read -r checksum filename; do",
		"[ -n \"$checksum\" ] || continue",
		"actual_cmd=\"\"",
		"if command -v shasum >/dev/null 2>&1; then",
		"actual_cmd=\"shasum -a 256\"",
		"elif command -v sha256sum >/dev/null 2>&1; then",
		"actual_cmd=\"sha256sum\"",
		"actual=$($actual_cmd \"dist/$filename\" | cut -d' ' -f1)",
		"if [ \"$actual\" != \"$checksum\" ]; then",
		"checksum mismatch for $filename: expected $checksum got $actual",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "sha256sum -c dist/checksums.txt") {
		t.Fatalf("release workflow should not rely on sha256sum -c with dist/checksums.txt path prefixes:\n%s", text)
	}
}

func TestReleaseWorkflowVerifiesArchiveContentsBeforeUpload(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"for archive in \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-amd64.tar.gz \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-arm64.tar.gz \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-linux-amd64.tar.gz \\",
		"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-linux-arm64.tar.gz; do",
		"tar -tzf \"$archive\" | grep \"$dir/LICENSE\"",
		"tar -tzf \"$archive\" | grep \"$dir/README.md\"",
		"tar -tzf \"$archive\" | grep \"$dir/ezoss\"",
		"python - <<'PY'",
		"'dist/ezoss-${{ needs.release-please.outputs.tag_name }}-windows-amd64.zip': {",
		"'ezoss-${{ needs.release-please.outputs.tag_name }}-windows-amd64/LICENSE'",
		"'ezoss-${{ needs.release-please.outputs.tag_name }}-windows-amd64/README.md'",
		"'ezoss-${{ needs.release-please.outputs.tag_name }}-windows-amd64/ezoss.exe'",
		"'dist/ezoss-${{ needs.release-please.outputs.tag_name }}-windows-arm64.zip': {",
		"'ezoss-${{ needs.release-please.outputs.tag_name }}-windows-arm64/LICENSE'",
		"'ezoss-${{ needs.release-please.outputs.tag_name }}-windows-arm64/README.md'",
		"'ezoss-${{ needs.release-please.outputs.tag_name }}-windows-arm64/ezoss.exe'",
		"missing windows archive entries in {archive_path}: {missing}",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowExecutesExtractedReleaseBinaryBeforeUpload(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"rm -rf dist/smoke-release",
		"mkdir -p dist/smoke-release",
		"tar -xzf dist/ezoss-${{ needs.release-please.outputs.tag_name }}-linux-amd64.tar.gz -C dist/smoke-release",
		"version_output=$(./dist/smoke-release/ezoss-${{ needs.release-please.outputs.tag_name }}-linux-amd64/ezoss --version)",
		"printf '%s\\n' \"$version_output\" | grep \"${{ needs.release-please.outputs.tag_name }}\"",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowDoesNotTryToExecuteWindowsBinaryOnUbuntu(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	if !strings.Contains(text, "runs-on: ubuntu-latest") {
		t.Fatalf("release workflow should keep publishing on ubuntu:\n%s", text)
	}
	for _, forbidden := range []string{
		"subprocess.run([str(binary_path), '--version'], check=True)",
		"wine",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("release workflow should not try to execute windows binaries on ubuntu; found %q:\n%s", forbidden, text)
		}
	}
}

func TestReleaseWorkflowRunsGoValidationBeforePublishingAssets(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Run Go validation",
		"run: |\n          make fmt-check\n          make lint\n          make test",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowDoesNotBuildDocsSiteBeforePublishingAssets(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	forbidden := []string{
		"- name: Set up Node.js",
		"uses: actions/setup-node@v4",
		"node-version: 20",
		"- name: Run docs validation",
		"run: make docs-build",
	}
	for _, value := range forbidden {
		if strings.Contains(text, value) {
			t.Fatalf("release workflow should not contain docs site build step %q:\n%s", value, text)
		}
	}
}

func TestReleaseWorkflowRunsUnixInstallerSmokeCheckBeforePublishingAssets(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Shell installer end-to-end smoke check",
		"go build -ldflags \"-X github.com/kunchenguid/ezoss/internal/buildinfo.Version=${{ needs.release-please.outputs.tag_name }}\" -o ./bin/ezoss ./cmd/ezoss",
		"release_root=\"$PWD/dist/install-fixture-unix-release\"",
		"printf '{\"tag_name\":\"${{ needs.release-please.outputs.tag_name }}\"}' > \"$latest_release_path\"",
		"cp ./bin/ezoss \"$archive_root/ezoss\"",
		"python3 -m http.server 8767 --bind 127.0.0.1 --directory \"$release_root\" >/tmp/ezoss-release-install-smoke.log 2>&1 &",
		"GITHUB_API_BASE=http://127.0.0.1:8767 \\",
		"GITHUB_DOWNLOAD_BASE=http://127.0.0.1:8767 \\",
		"sh ./install.sh --bin-dir \"$bin_dir\"",
		"version_output=$(\"$bin_dir/ezoss\" --version)",
		"printf '%s\\n' \"$version_output\" | grep \"${{ needs.release-please.outputs.tag_name }}\"",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowUnixInstallerSmokeCheckSupportsChecksumFallback(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"checksum_cmd=\"\"",
		"if command -v shasum >/dev/null 2>&1; then",
		"checksum_cmd=\"shasum -a 256\"",
		"elif command -v sha256sum >/dev/null 2>&1; then",
		"checksum_cmd=\"sha256sum\"",
		`checksum=$($checksum_cmd "$archive_path" | cut -d' ' -f1)`,
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowRunsWindowsInstallerSmokeCheckBeforePublishingAssets(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"smoke-install-windows:",
		"needs: release-please",
		"runs-on: windows-latest",
		"if: ${{ needs.release-please.outputs.release_created == 'true' }}",
		"$version = '${{ needs.release-please.outputs.tag_name }}'",
		"go build -ldflags \"-X github.com/kunchenguid/ezoss/internal/buildinfo.Version=${{ needs.release-please.outputs.tag_name }}\" -o ezoss.exe ./cmd/ezoss",
		"releaseRoot = Join-Path $PWD 'dist/install-fixture-windows-release'",
		"latestPath = Join-Path $apiDir 'latest'",
		"ezoss-$version-windows-amd64.zip",
		"serverLog = Join-Path $releaseRoot 'http-server.log'",
		"$python = (Get-Command python).Source",
		"Start-Process -FilePath $python",
		"http://127.0.0.1:8768/repos/fixture/repo/releases/latest",
		"throw \"fixture server did not become ready. Log: $log\"",
		"GITHUB_API_BASE = 'http://127.0.0.1:8768'",
		"GITHUB_DOWNLOAD_BASE = 'http://127.0.0.1:8768'",
		"powershell -ExecutionPolicy Bypass -File ./install.ps1 -BinDir $binDir",
		`$versionOutput = & (Join-Path $binDir 'ezoss.exe') --version`,
		`if ($versionOutput -notmatch [regex]::Escape($version)) {`,
		"publish-assets:\n    needs:\n      - release-please\n      - smoke-install-windows",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowRunsMacOSPackagedArchiveSmokeCheckBeforePublishingAssets(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"smoke-archive-macos:",
		"needs: release-please",
		"runs-on: macos-latest",
		"if: ${{ needs.release-please.outputs.release_created == 'true' }}",
		"make dist VERSION=${{ needs.release-please.outputs.tag_name }}",
		"go_arch=$(go env GOARCH)",
		"archive=\"dist/ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-$go_arch.tar.gz\"",
		"rm -rf dist/smoke-release-macos",
		"mkdir -p dist/smoke-release-macos",
		"tar -xzf \"$archive\" -C dist/smoke-release-macos",
		"version_output=$(\"dist/smoke-release-macos/ezoss-${{ needs.release-please.outputs.tag_name }}-darwin-$go_arch/ezoss\" --version)",
		"printf '%s\\n' \"$version_output\" | grep \"${{ needs.release-please.outputs.tag_name }}\"",
		"publish-assets:\n    needs:\n      - release-please\n      - smoke-install-windows\n      - smoke-archive-macos",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowRunsWindowsPackagedArchiveSmokeCheckBeforePublishingAssets(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"smoke-archive-windows:",
		"needs: release-please",
		"runs-on: windows-latest",
		"if: ${{ needs.release-please.outputs.release_created == 'true' }}",
		"make dist VERSION=${{ needs.release-please.outputs.tag_name }}",
		"$extractRoot = Join-Path $PWD 'dist/smoke-release-windows'",
		"$zipPath = Join-Path $PWD \"dist/ezoss-$version-windows-amd64.zip\"",
		"Remove-Item -Path $extractRoot -Recurse -Force -ErrorAction SilentlyContinue",
		"Expand-Archive -Path $zipPath -DestinationPath $extractRoot -Force",
		"$versionOutput = & (Join-Path $extractRoot \"ezoss-$version-windows-amd64/ezoss.exe\") --version",
		`if ($versionOutput -notmatch [regex]::Escape($version)) {`,
		`throw "packaged binary version output '$versionOutput' did not contain $version"`,
		"publish-assets:\n    needs:\n      - release-please\n      - smoke-install-windows\n      - smoke-archive-macos\n      - smoke-archive-windows",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
		}
	}
}

func TestReleasePleaseConfigKeepsCurrentGoReleaseContract(t *testing.T) {
	configPath := filepath.Join("..", "..", ".release-please-config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", configPath, err)
	}

	text := string(data)
	checks := []string{
		`"$schema": "https://raw.githubusercontent.com/googleapis/release-please/main/schemas/config.json"`,
		`"release-type": "go"`,
		`"packages": {`,
		`".": {`,
		`"component": "ezoss"`,
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release-please config missing %q:\n%s", want, text)
		}
	}
}

func TestReleasePleaseManifestKeepsRootPackageVersion(t *testing.T) {
	manifestPath := filepath.Join("..", "..", ".release-please-manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", manifestPath, err)
	}

	var manifest map[string]string
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("release-please manifest is not valid JSON: %v\n%s", err, data)
	}

	version, ok := manifest["."]
	if !ok {
		t.Fatalf("release-please manifest missing root package entry: %s", data)
	}
	if !releasePleaseVersionPattern.MatchString(version) {
		t.Fatalf("release-please manifest root package version %q is not semver-like", version)
	}
}

func TestDocsSiteAndPagesWorkflowAreRemoved(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "docs"),
		filepath.Join("..", "..", ".github", "workflows", "docs.yml"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %q to be removed, stat error = %v", path, err)
		}
	}
}

func TestCIWorkflowCoversCrossPlatformValidationAndReleaseBuilds(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- ubuntu-latest",
		"- macos-latest",
		"run: make fmt-check",
		"run: make lint",
		"run: make test",
		"run: make build",
		"run: make dist VERSION=test-ci",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{
		"uses: actions/setup-node@v4",
		"node-version: 20",
		"run: make docs-build",
		"Build docs",
		"docs:",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("ci workflow should not contain docs site validation %q:\n%s", forbidden, text)
		}
	}
}

func TestCIWorkflowVerifiesReleaseArchivesContainLicenseReadmeAndBinary(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"Compress-Archive -Path $archiveRoot -DestinationPath $zipPath -Force",
		"for archive in \\",
		"tar -tzf \"$archive\" | grep \"$dir/LICENSE\"",
		"tar -tzf \"$archive\" | grep \"$dir/README.md\"",
		"tar -tzf \"$archive\" | grep \"$dir/ezoss\"",
		"python - <<'PY'",
		"expected_archives = {",
		"ezoss-test-ci-windows-amd64/LICENSE",
		"ezoss-test-ci-windows-amd64/README.md",
		"ezoss-test-ci-windows-amd64/ezoss.exe",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowVerifiesEveryReleaseArchiveContainsLicenseReadmeAndBinary(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"for archive in \\",
		"dist/ezoss-test-ci-darwin-amd64.tar.gz \\",
		"dist/ezoss-test-ci-darwin-arm64.tar.gz \\",
		"dist/ezoss-test-ci-linux-amd64.tar.gz \\",
		"dist/ezoss-test-ci-linux-arm64.tar.gz; do",
		"dir=$(basename \"$archive\" .tar.gz)",
		"tar -tzf \"$archive\" | grep \"$dir/LICENSE\"",
		"tar -tzf \"$archive\" | grep \"$dir/README.md\"",
		"tar -tzf \"$archive\" | grep \"$dir/ezoss\"",
		"expected_archives = {",
		"'dist/ezoss-test-ci-windows-amd64.zip': {",
		"'dist/ezoss-test-ci-windows-arm64.zip': {",
		"LICENSE",
		"README.md",
		"ezoss.exe",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowRunsMacOSPackagedArchiveSmokeCheck(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: macOS packaged archive smoke check",
		"if: matrix.os == 'macos-latest'",
		"rm -rf dist/macos-smoke",
		"make dist VERSION=test-ci",
		"go_arch=$(go env GOARCH)",
		"archive=\"dist/ezoss-test-ci-darwin-$go_arch.tar.gz\"",
		"mkdir -p dist/macos-smoke",
		"tar -xzf \"$archive\" -C dist/macos-smoke",
		"version_output=$(\"dist/macos-smoke/ezoss-test-ci-darwin-$go_arch/ezoss\" --version)",
		"printf '%s\\n' \"$version_output\" | grep \"test-ci\"",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowKeepsExpectedPushAndPullRequestTriggers(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"push:",
		"branches:",
		"- main",
		"pull_request:",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowKeepsExpectedConcurrencyPolicy(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"concurrency:",
		"group: ci-${{ github.ref }}",
		"cancel-in-progress: true",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowUsesLeastPrivilegePermissions(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"permissions:\n  contents: read",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowIncludesWindowsBuildSmokeCheck(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"windows-build:",
		"runs-on: windows-latest",
		"go test ./...",
		"go build ./cmd/ezoss",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowRunsFormattingAndVetChecksOnWindows(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"windows-build:",
		"runs-on: windows-latest",
		"go test ./...",
		"go vet ./...",
		"gofmt -l",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "if ($fmtOutput)") {
		t.Fatalf("ci workflow should fail the windows job when gofmt reports files:\n%s", text)
	}
}

func TestCIWorkflowRunsWindowsInstallerSmokeCheck(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: PowerShell installer help",
		"pwsh -NoProfile -File ./install.ps1 -Help",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowRunsWindowsInstallerEndToEndSmokeCheck(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: PowerShell installer end-to-end smoke check",
		"go build -ldflags \"-X github.com/kunchenguid/ezoss/internal/buildinfo.Version=$version\" -o ezoss.exe ./cmd/ezoss",
		"$version = 'v1.2.3'",
		"$releaseRoot = Join-Path $PWD 'dist/install-fixture'",
		"$serverLog = Join-Path $releaseRoot 'http-server.log'",
		"$python = (Get-Command python).Source",
		"Start-Process -FilePath $python",
		"http://127.0.0.1:8765/repos/fixture/repo/releases/latest",
		"throw \"fixture server did not become ready. Log: $log\"",
		"$env:OWNER = 'fixture'",
		"$env:REPO = 'repo'",
		"$env:GITHUB_API_BASE = 'http://127.0.0.1:8765'",
		"$env:GITHUB_DOWNLOAD_BASE = 'http://127.0.0.1:8765'",
		"pwsh -NoProfile -File ./install.ps1 -BinDir $binDir",
		"$versionOutput = & (Join-Path $binDir 'ezoss.exe') --version",
		"if ($versionOutput -notmatch [regex]::Escape($version)) {",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowRunsShellInstallerSmokeCheck(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Shell installer help",
		"run: sh ./install.sh --help",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowRunsShellInstallerEndToEndSmokeCheck(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Shell installer end-to-end smoke check",
		"go build -ldflags \"-X github.com/kunchenguid/ezoss/internal/buildinfo.Version=$version\" -o ./bin/ezoss ./cmd/ezoss",
		"release_root=\"$PWD/dist/install-fixture-unix\"",
		"mkdir -p \"$api_dir\" \"$archive_root\" \"$bin_dir\"",
		"printf '{\"tag_name\":\"v1.2.3\"}' > \"$latest_release_path\"",
		"python3 -m http.server 8766 --bind 127.0.0.1 --directory \"$release_root\"",
		"OWNER=fixture \\",
		"REPO=repo \\",
		"GITHUB_API_BASE=http://127.0.0.1:8766 \\",
		"GITHUB_DOWNLOAD_BASE=http://127.0.0.1:8766 \\",
		"sh ./install.sh --bin-dir \"$bin_dir\"",
		"version_output=$(\"$bin_dir/ezoss\" --version)",
		"printf '%s\\n' \"$version_output\" | grep \"$version\"",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowUnixInstallerSmokeCheckSupportsChecksumFallback(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"checksum_cmd=\"\"",
		"if command -v shasum >/dev/null 2>&1; then",
		"checksum_cmd=\"shasum -a 256\"",
		"elif command -v sha256sum >/dev/null 2>&1; then",
		"checksum_cmd=\"sha256sum\"",
		`checksum=$($checksum_cmd "$archive_path" | cut -d' ' -f1)`,
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowDoesNotBuildDocsSite(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	forbidden := []string{
		"- name: Set up Node.js",
		"uses: actions/setup-node@v4",
		"node-version: 20",
		"- name: Build docs",
		"run: make docs-build",
	}
	for _, value := range forbidden {
		if strings.Contains(text, value) {
			t.Fatalf("ci workflow should not contain docs site build step %q:\n%s", value, text)
		}
	}
}

func TestCIWorkflowRunsBuiltBinarySmokeChecks(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Built binary version smoke check",
		"version_output=$(./bin/ezoss --version)",
		"printf '%s\\n' \"$version_output\"",
		"printf '%s\\n' \"$version_output\" | grep \"dev\"",
		"- name: Windows built binary version smoke check",
		`$versionOutput = .\ezoss.exe --version`,
		`if ($versionOutput -notmatch 'dev') {`,
		`throw "built binary version output '$versionOutput' did not contain dev"`,
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowRunsWindowsPackagedBinarySmokeCheck(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Windows packaged binary smoke check",
		"shell: pwsh",
		"go build -ldflags \"-X github.com/kunchenguid/ezoss/internal/buildinfo.Version=test-ci\" -o ezoss.exe ./cmd/ezoss",
		"$versionOutput = & (Join-Path $extractRoot 'ezoss-test-ci-windows-amd64/ezoss.exe') --version",
		"if ($versionOutput -notmatch 'test-ci') {",
		`throw "packaged binary version output '$versionOutput' did not contain test-ci"`,
		"$archiveRoot = Join-Path $PWD 'dist/smoke-windows/ezoss-test-ci-windows-amd64'",
		"Remove-Item -Path $archiveRoot -Recurse -Force -ErrorAction SilentlyContinue",
		"Copy-Item .\\ezoss.exe (Join-Path $archiveRoot 'ezoss.exe')",
		"Copy-Item .\\LICENSE (Join-Path $archiveRoot 'LICENSE')",
		"Copy-Item .\\README.md (Join-Path $archiveRoot 'README.md')",
		"$zipPath = Join-Path $PWD 'dist/ezoss-test-ci-windows-amd64.zip'",
		"Remove-Item -Path $zipPath -Force -ErrorAction SilentlyContinue",
		"Compress-Archive -Path $archiveRoot -DestinationPath $zipPath -Force",
		"Remove-Item -Path $extractRoot -Recurse -Force -ErrorAction SilentlyContinue",
		"Expand-Archive -Path $zipPath -DestinationPath $extractRoot -Force",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowVerifiesDistArtifactsAfterBuild(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Verify dist artifacts",
		"test -f dist/ezoss-test-ci-darwin-amd64.tar.gz",
		"test -f dist/ezoss-test-ci-darwin-arm64.tar.gz",
		"test -f dist/ezoss-test-ci-linux-amd64.tar.gz",
		"test -f dist/ezoss-test-ci-linux-arm64.tar.gz",
		"test -f dist/ezoss-test-ci-windows-amd64.zip",
		"test -f dist/ezoss-test-ci-windows-arm64.zip",
		"test -f dist/checksums.txt",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowVerifiesChecksumsCoverEveryReleaseArtifact(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"grep 'ezoss-test-ci-darwin-amd64.tar.gz' dist/checksums.txt",
		"grep 'ezoss-test-ci-darwin-arm64.tar.gz' dist/checksums.txt",
		"grep 'ezoss-test-ci-linux-amd64.tar.gz' dist/checksums.txt",
		"grep 'ezoss-test-ci-linux-arm64.tar.gz' dist/checksums.txt",
		"grep 'ezoss-test-ci-windows-amd64.zip' dist/checksums.txt",
		"grep 'ezoss-test-ci-windows-arm64.zip' dist/checksums.txt",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestCIWorkflowVerifiesChecksumIntegrityForEveryReleaseArtifact(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"while read -r checksum filename; do",
		"[ -n \"$checksum\" ] || continue",
		"actual_cmd=\"\"",
		"if command -v shasum >/dev/null 2>&1; then",
		"actual_cmd=\"shasum -a 256\"",
		"elif command -v sha256sum >/dev/null 2>&1; then",
		"actual_cmd=\"sha256sum\"",
		"actual=$($actual_cmd \"dist/$filename\" | cut -d' ' -f1)",
		"if [ \"$actual\" != \"$checksum\" ]; then",
		"checksum mismatch for $filename: expected $checksum got $actual",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "sha256sum -c dist/checksums.txt") {
		t.Fatalf("ci workflow should not rely on sha256sum -c with dist/checksums.txt path prefixes:\n%s", text)
	}
}

func TestCIWorkflowExecutesExtractedReleaseBinary(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"rm -rf dist/smoke-ci",
		"mkdir -p dist/smoke-ci",
		"tar -xzf dist/ezoss-test-ci-linux-amd64.tar.gz -C dist/smoke-ci",
		"version_output=$(./dist/smoke-ci/ezoss-test-ci-linux-amd64/ezoss --version)",
		"printf '%s\\n' \"$version_output\" | grep \"test-ci\"",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
		}
	}
}

func TestGitIgnoreKeepsGeneratedReleaseArtifactsOutOfGit(t *testing.T) {
	gitignorePath := filepath.Join("..", "..", ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", gitignorePath, err)
	}

	text := string(data)
	checks := []string{
		"/bin/",
		"/dist/",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf(".gitignore missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "/docs/") {
		t.Fatalf(".gitignore should not keep docs site artifact ignores after docs removal:\n%s", text)
	}
}

func TestREADMEQuickStartMatchesCurrentMockListOutput(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"$ ezoss daemon start --mock",
		"$ ezoss list",
		"kunchenguid/ezoss#42\tissue\tcomment\tlow\tpanic in sync loop",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README quick start missing %q:\n%s", want, text)
		}
	}
}

func TestREADMEShowsCheckedInDemoGif(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"<img src=\"https://raw.githubusercontent.com/kunchenguid/ezoss/main/demo.gif\" alt=\"ezoss demo\" width=\"800\" />",
		"The agent drafts. You decide.",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README demo section missing %q:\n%s", want, text)
		}
	}
}

func TestREADMEHeroMatchesCurrentReleaseMessaging(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"<h1 align=\"center\">ezoss</h1>",
		"Turn your issue queue into a reviewable inbox instead of a background tax.",
		"You stay in control. The agent drafts. You decide.",
		"- **Private by default** - agent rationale, draft comments, fix prompts, and token usage stay in local SQLite until you approve an action.",
		"- **GitHub-native maintainer state** - maintainer triage visibility is mirrored back to GitHub with `ezoss/*` labels, while contributor items stay local.",
		"- **Actually usable loop** - daemon polling, one-off triage, a Bubble Tea inbox, and approval/fix-PR/copy-prompt/edit/rerun flows already work end to end.",
		"- **PRs can pause before review** - PRs without prior agreement can be routed into a maintainer approval step before code review.",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README hero section missing %q:\n%s", want, text)
		}
	}
}

func TestREADMEDoesNotLinkToPublishedDocsSite(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	forbidden := []string{
		"href=\"https://kunchenguid.github.io/ezoss/\"",
		"alt=\"Docs\"",
		"Docs: https://kunchenguid.github.io/ezoss/",
	}
	for _, value := range forbidden {
		if strings.Contains(text, value) {
			t.Fatalf("README should not link to removed docs site %q:\n%s", value, text)
		}
	}
}

func TestREADMEBadgesKeepCurrentReleaseDiscoveryLinks(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"href=\"https://github.com/kunchenguid/ezoss/actions/workflows/ci.yml\"",
		"alt=\"CI\"",
		"href=\"https://github.com/kunchenguid/ezoss/actions/workflows/release.yml\"",
		"alt=\"Release\"",
		"href=\"https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue?style=flat-square\"",
		"alt=\"Platform\"",
		"href=\"https://x.com/kunchenguid\"",
		"alt=\"X\"",
		"href=\"https://discord.gg/Wsy2NpnZDu\"",
		"alt=\"Discord\"",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README badge row missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "docs-GitHub") || strings.Contains(text, "alt=\"Docs\"") {
		t.Fatalf("README badge row should not include a docs badge after docs site removal:\n%s", text)
	}
}

func TestDemoTapeAndMakefileMatchShippedDemoFlow(t *testing.T) {
	makefilePath := filepath.Join("..", "..", "Makefile")
	makefileData, err := os.ReadFile(makefilePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", makefilePath, err)
	}

	makefileText := string(makefileData)
	for _, want := range []string{
		".PHONY: build dist demo install test lint fmt fmt-check",
		"demo: build",
		"vhs demo.tape",
		"ffmpeg -y -i demo_raw.gif",
		"demo.gif",
		"rm -f demo_raw.gif",
	} {
		if !strings.Contains(makefileText, want) {
			t.Fatalf("Makefile demo flow missing %q:\n%s", want, makefileText)
		}
	}
	if strings.Contains(makefileText, "docs-build") {
		t.Fatalf("Makefile should not keep docs-build target after docs site removal:\n%s", makefileText)
	}

	tapePath := filepath.Join("..", "..", "demo.tape")
	tapeData, err := os.ReadFile(tapePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", tapePath, err)
	}

	tapeText := string(tapeData)
	for _, want := range []string{
		"Output demo_raw.gif",
		"Require ./bin/ezoss",
		"export PATH=$PWD/bin:$PATH",
		"ezoss init --repo kunchenguid/ezoss --agent auto",
		"ezoss daemon start --mock",
		"sleep 3 && ezoss status",
		"ezoss",
		"ezoss daemon stop",
	} {
		if !strings.Contains(tapeText, want) {
			t.Fatalf("demo.tape missing %q:\n%s", want, tapeText)
		}
	}
}

func TestREADMEInstallSectionMatchesReleaseInstallPaths(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"## Install",
		"go install github.com/kunchenguid/ezoss/cmd/ezoss@latest",
		"curl -fsSL https://raw.githubusercontent.com/kunchenguid/ezoss/main/install.sh | sh",
		"iwr https://raw.githubusercontent.com/kunchenguid/ezoss/main/install.ps1 -useb | iex",
		"git clone https://github.com/kunchenguid/ezoss.git",
		"make build",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README install section missing %q:\n%s", want, text)
		}
	}
}

func TestREADMEConfigurationExampleMatchesCurrentConfigSurface(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"## Configuration",
		"merge_method` controls how approved PR merges execute and supports `merge`, `squash`, or `rebase`.",
		"`fixes.pr_create` controls how fix PRs are created and supports `auto`, `no-mistakes`, `gh`, or `disabled`.",
		"`auto` prefers `no-mistakes` when both `no-mistakes` and `gh` are available, then uses `gh` when `no-mistakes` is unavailable or fails before PR detection.",
		"If daemon detection misses the PR, the inbox keeps the job in `waiting_for_pr` and shows `cd <worktree> && no-mistakes attach` for manual recovery.",
		"merge_method: merge",
		"fixes:",
		"pr_create: auto",
		"sync_labels:",
		"waiting_on: true",
		"stale: true",
		"Per-repo overrides live in `.ezoss.yaml` at the repo root and currently support overriding `agent`.",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README configuration section missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "triaged: true") {
		t.Fatalf("README configuration section should not advertise sync_labels.triaged as configurable:\n%s", text)
	}
}

func TestREADMECLIReferenceMatchesCurrentCommandSurface(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := normalizeMarkdownTableRows(string(data))
	checks := []string{
		"## CLI Reference",
		"| `ezoss` | Open the inbox TUI from the local recommendations database |",
		"| `ezoss doctor` | Check local prerequisites including `gh`, agent availability, daemon state, and SQLite access |",
		"| `ezoss init` | Create or update `~/.ezoss/config.yaml` |",
		"| `ezoss status` | Open the realtime status TUI; in non-interactive output, print rich text status |",
		"| `ezoss status --short` | Print a one-line summary of pending recommendations, configured repos, and contributor state |",
		"| `ezoss list` | Print pending recommendations in a text format, including contributor markers |",
		"| `ezoss fix <repo>#<number>` | Run the active fix prompt directly in an isolated worktree; maintainer PRs and contributor pushes follow config |",
		"| `ezoss triage <repo>#<number>` | Manually triage one issue or PR |",
		"| `ezoss update` | Download and install the latest released binary for the current platform |",
		"| `ezoss daemon start` | Start the background poller |",
		"| `ezoss daemon stop` | Stop the background poller |",
		"| `ezoss daemon status` | Show whether the daemon is running |",
		"### Flags",
		"| `daemon start` | `--mock` | Use canned GitHub items and recommendations |",
		"| `triage <repo>#<number>` | `--mock` | Triage against canned fixtures instead of live GitHub + agent backends |",
		"| `fix <repo>#<number>` | `--pr-create` | Override maintainer fix PR creation: `auto`, `no-mistakes`, `gh`, or `disabled` |",
		"| `fix <repo>#<number>` | `--prepare-only` | Prepare the isolated worktree without running the coding agent |",
		"| `init` | `--repo` | Repository to monitor, repeatable |",
		"| `init` | `--agent` | Agent backend: `auto`, `claude`, `codex`, `rovodev`, `opencode` |",
		"| `init` | `--merge-method` | Default PR merge method: `merge`, `squash`, or `rebase` |",
		"| `init` | `--poll-interval` | Poll cadence as a duration like `5m` |",
		"| `init` | `--stale-threshold` | Stale threshold as a duration like `30d` or `720h` |",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README CLI reference missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "| `ezoss daemon run` |") {
		t.Fatalf("README CLI reference should not expose the hidden daemon run command:\n%s", text)
	}
}

func normalizeMarkdownTableRows(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		for j := 1; j < len(cells)-1; j++ {
			cells[j] = strings.TrimSpace(cells[j])
		}
		lines[i] = "| " + strings.Join(cells[1:len(cells)-1], " | ") + " |"
	}
	return strings.Join(lines, "\n")
}

func TestREADMEHowItWorksSectionMatchesCurrentMaintainerFirstFlow(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"## How It Works",
		"maintainer label poll",
		"+ contributor search",
		"ezoss/triaged",
		"│ daemon poller      │",
		"│ agent backend      │",
		"│ local SQLite cache │",
		"│ inbox TUI          │",
		"│ GitHub labels /    │",
		"│ / fix branch work  │",
		"or queued fix job",
		"- **GitHub is the maintainer truth** - for configured repos, `ezoss/triaged` is the public signal that an item has already been handled.",
		"Contributor items are found with `gh search prs/issues --author=@me`, do not edit upstream labels, and are tracked with local sweep metadata.",
		"- **Local DB is the private memory** - drafts, fix prompts, rationales, approvals, and token accounting stay on disk under `~/.ezoss/`.",
		"- **Contributor mode is automatic** - by default, the daemon searches for open issues and PRs authored by you in repos you do not maintain, marks them with a `contrib` badge in the inbox, and uses contributor-safe actions instead of maintainer actions.",
		"- **Fixes use isolated worktrees** - `fix_required` options can queue daemon-backed jobs under `~/.ezoss/fixes`, run the selected coding agent, commit changes, and either create maintainer draft PRs according to `fixes.pr_create` or prepare contributor PR branch updates according to `fixes.contrib_push`.",
		"- **Approval is explicit** - comments, labels, closes, merges, maintainer fix PRs, and contributor PR branch updates only happen after you approve an inbox action, queue a fix job, or run `ezoss fix`.",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README how-it-works section missing %q:\n%s", want, text)
		}
	}
}

func TestREADMEInboxActionsAndRequirementsCoverShippedWorkflow(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := normalizeMarkdownTableRows(string(data))
	checks := []string{
		"Live triage requires `gh auth login`, `git`, one supported agent backend, and a writable state directory under `~/.ezoss`.",
		"Copying fix prompts from the inbox also needs a platform clipboard command",
		"Opening fix PRs needs `gh`; `fixes.pr_create: no-mistakes` also needs `no-mistakes`.",
		"## Inbox Actions",
		"| `a` | Approve | Execute the selected GitHub action; maintainer items sync labels, contributor items are marked handled locally |",
		"| `c` | Copy prompt | Copy the active option's coding-agent fix prompt when one exists |",
		"| `f` | Fix | Queue or replace a daemon-backed coding-agent fix job when a fix prompt exists |",
		"| `e` | Edit | Open the draft in your editor before approval |",
		"| `F` | Filter | Cycle role filter through all, maintainer, and contributor items |",
		"| `m` | Mark triaged | Stamp `ezoss/triaged` for maintainer items, or mark contributor items handled locally |",
		"| `o` | Open | Open the current item's GitHub page in your browser |",
		"| `r` | Rerun | Re-triage the item and replace the active recommendation |",
		"| `j`/`k` | Navigate | Move between inbox items; use arrow keys to scroll overflowing text |",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README inbox workflow section missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseInstallDocsMentionManualAssetVerification(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	readmeData, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	readmeText := string(readmeData)
	for _, want := range []string{
		"checksums.txt",
		"GitHub release",
	} {
		if !strings.Contains(readmeText, want) {
			t.Fatalf("README install section missing %q:\n%s", want, readmeText)
		}
	}
}

func TestREADMEDescribesPRApprovalBeforeReview(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	readmeData, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	readmeText := string(readmeData)
	for _, want := range []string{
		"PRs without prior agreement can be routed into a maintainer approval step before code review.",
		"unsolicited PRs can surface as `state_change: none` with a draft comment asking whether the approach is wanted before the tool drafts code review feedback.",
	} {
		if !strings.Contains(readmeText, want) {
			t.Fatalf("README missing PR approval-before-review guidance %q:\n%s", want, readmeText)
		}
	}
}

func TestREADMEDevelopmentSectionMatchesSupportedLocalWorkflows(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"## Development",
		"make build      # Build ./bin/ezoss",
		"make dist       # Cross-compile release archives into ./dist",
		"make install    # go install + daemon install/restart; fails on daemon errors unless EZOSS_SKIP_DAEMON=1",
		"make test       # Run Go tests",
		"make lint       # Run go vet",
		"make fmt        # Format Go code",
		"make fmt-check  # Fail if gofmt would change tracked Go files",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README development section missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "docs-build") || strings.Contains(text, "./docs") {
		t.Fatalf("README development section should not mention removed docs site workflows:\n%s", text)
	}
}

func TestREADMEExposesMITLicense(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	readmeData, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	readmeText := string(readmeData)
	for _, want := range []string{
		"## License",
		"MIT",
		"[LICENSE](LICENSE)",
	} {
		if !strings.Contains(readmeText, want) {
			t.Fatalf("README license section missing %q:\n%s", want, readmeText)
		}
	}
}

func TestLicenseFileKeepsStandardMITGrant(t *testing.T) {
	licensePath := filepath.Join("..", "..", "LICENSE")
	data, err := os.ReadFile(licensePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", licensePath, err)
	}

	text := string(data)
	checks := []string{
		"MIT License",
		"Copyright (c) 2026 Kun Cheng",
		"Permission is hereby granted, free of charge, to any person obtaining a copy",
		"The above copyright notice and this permission notice shall be included in all",
		"THE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("LICENSE missing %q:\n%s", want, text)
		}
	}
}
