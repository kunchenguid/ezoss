package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseWorkflowUploadUsesClobber(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	if !strings.Contains(text, "gh release upload ${{ needs.release-please.outputs.tag_name }} --clobber dist/*") {
		t.Fatalf("release workflow upload step does not use --clobber:\n%s", text)
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

func TestReleaseWorkflowRunsDocsBuildBeforePublishingAssets(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Set up Node.js",
		"uses: actions/setup-node@v4",
		"node-version: 20",
		"- name: Run docs validation",
		"run: make docs-build",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, text)
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

func TestReleasePleaseManifestKeepsCurrentSeedVersion(t *testing.T) {
	manifestPath := filepath.Join("..", "..", ".release-please-manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", manifestPath, err)
	}

	text := string(data)
	checks := []string{
		`".": "0.1.0"`,
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("release-please manifest missing %q:\n%s", want, text)
		}
	}
}

func TestDocsAstroConfigSetsGitHubPagesBasePath(t *testing.T) {
	configPath := filepath.Join("..", "..", "docs", "astro.config.mjs")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", configPath, err)
	}

	text := string(data)
	if !strings.Contains(text, "site: 'https://kunchenguid.github.io/ezoss'") {
		t.Fatalf("docs astro config does not declare the published site URL:\n%s", text)
	}
	if !strings.Contains(text, "base: '/ezoss'") {
		t.Fatalf("docs astro config does not set the GitHub Pages base path:\n%s", text)
	}
}

func TestDocsWorkflowBuildsAndDeploysGitHubPages(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "docs.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"pages: write",
		"id-token: write",
		"uses: actions/configure-pages@v5",
		"run: make docs-build",
		"uses: actions/upload-pages-artifact@v3",
		"path: docs/dist",
		"uses: actions/deploy-pages@v4",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs workflow missing %q:\n%s", want, text)
		}
	}
}

func TestDocsWorkflowKeepsExpectedPublishTriggerAndConcurrency(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "docs.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"push:",
		"branches:",
		"- main",
		"paths:",
		"- docs/**",
		"- .github/workflows/docs.yml",
		"- Makefile",
		"workflow_dispatch:",
		"concurrency:",
		"group: docs",
		"cancel-in-progress: true",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs workflow missing %q:\n%s", want, text)
		}
	}
}

func TestDocsWorkflowUsesLeastPrivilegeJobPermissions(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "docs.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	if strings.Contains(text, "permissions:\n  contents: read\n  pages: write\n  id-token: write") {
		t.Fatalf("docs workflow should not grant deploy permissions workflow-wide:\n%s", text)
	}

	checks := []string{
		"build:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read",
		"deploy:\n    needs: build\n    runs-on: ubuntu-latest\n    permissions:\n      pages: write\n      id-token: write",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs workflow missing %q:\n%s", want, text)
		}
	}
}

func TestDocsWorkflowScopesGitHubPagesEnvironmentToDeployOnly(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "docs.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	if strings.Contains(text, "build:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n    environment:\n      name: github-pages") {
		t.Fatalf("docs workflow should not attach the github-pages environment to the build job:\n%s", text)
	}

	if !strings.Contains(text, "deploy:\n    needs: build\n    runs-on: ubuntu-latest\n    permissions:\n      pages: write\n      id-token: write\n    environment:\n      name: github-pages") {
		t.Fatalf("docs workflow should keep the github-pages environment on the deploy job:\n%s", text)
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
		"uses: actions/setup-node@v4",
		"run: make docs-build",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
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
		"Start-ThreadJob",
		"python -m http.server 8765 --bind 127.0.0.1 --directory $root",
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

func TestCIWorkflowBuildsDocsOnUbuntu(t *testing.T) {
	workflowPath := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	text := string(data)
	checks := []string{
		"- name: Set up Node.js",
		"uses: actions/setup-node@v4",
		"if: matrix.os == 'ubuntu-latest'",
		"node-version: 20",
		"- name: Build docs",
		"run: make docs-build",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("ci workflow missing %q:\n%s", want, text)
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

func TestGitIgnoreKeepsGeneratedReleaseAndDocsArtifactsOutOfGit(t *testing.T) {
	gitignorePath := filepath.Join("..", "..", ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", gitignorePath, err)
	}

	text := string(data)
	checks := []string{
		"/bin/",
		"/dist/",
		"/docs/.astro/",
		"/docs/node_modules/",
		"/docs/dist/",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf(".gitignore missing %q:\n%s", want, text)
		}
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
		"The agent drafts. The maintainer decides.",
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
		"You stay in control. The agent drafts. The maintainer decides.",
		"- **Private by default** - agent rationale, draft comments, and token usage stay in local SQLite until you approve an action.",
		"- **GitHub-native state** - triage visibility is mirrored back to GitHub with `ezoss/*` labels so co-maintainers can see what's going on.",
		"- **Actually usable loop** - daemon polling, one-off triage, a Bubble Tea inbox, and approval/edit/rerun flows already work end to end.",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README hero section missing %q:\n%s", want, text)
		}
	}
}

func TestREADMEExposesPublishedDocsLink(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"href=\"https://kunchenguid.github.io/ezoss/\"",
		"alt=\"Docs\"",
		"Docs: https://kunchenguid.github.io/ezoss/",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README docs link missing %q:\n%s", want, text)
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
		"href=\"https://kunchenguid.github.io/ezoss/\"",
		"alt=\"Docs\"",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README badge row missing %q:\n%s", want, text)
		}
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
		".PHONY: build dist demo docs-build install test lint fmt fmt-check",
		"demo: build",
		"vhs demo.tape",
	} {
		if !strings.Contains(makefileText, want) {
			t.Fatalf("Makefile demo flow missing %q:\n%s", want, makefileText)
		}
	}

	tapePath := filepath.Join("..", "..", "demo.tape")
	tapeData, err := os.ReadFile(tapePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", tapePath, err)
	}

	tapeText := string(tapeData)
	for _, want := range []string{
		"Output demo.gif",
		"Require ./bin/ezoss",
		"./bin/ezoss init --repo kunchenguid/ezoss --agent auto",
		"./bin/ezoss daemon start --mock",
		"sleep 3 && ./bin/ezoss status",
		"./bin/ezoss list",
		"./bin/ezoss daemon stop",
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
		"merge_method: merge",
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

	text := string(data)
	checks := []string{
		"## CLI Reference",
		"| `ezoss` | Open the inbox TUI from the local recommendations database |",
		"| `ezoss doctor` | Check local prerequisites including `gh`, agent availability, daemon state, and SQLite access |",
		"| `ezoss init` | Create or update `~/.ezoss/config.yaml` |",
		"| `ezoss status` | Print a one-line summary of pending recommendations and configured repos |",
		"| `ezoss list` | Print pending recommendations in a text format |",
		"| `ezoss triage <repo>#<number>` | Manually triage one issue or PR |",
		"| `ezoss update` | Download and install the latest released binary for the current platform |",
		"| `ezoss daemon start` | Start the background poller |",
		"| `ezoss daemon stop` | Stop the background poller |",
		"| `ezoss daemon status` | Show whether the daemon is running |",
		"### Flags",
		"| `daemon start` | `--mock` | Use canned GitHub items and recommendations |",
		"| `triage <repo>#<number>` | `--mock` | Triage against canned fixtures instead of live GitHub + agent backends |",
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

func TestREADMEHowItWorksSectionMatchesCurrentMaintainerFirstFlow(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	text := string(data)
	checks := []string{
		"## How It Works",
		"poll for items without",
		"ezoss/triaged",
		"│ daemon poller      │",
		"│ agent backend      │",
		"│ local SQLite cache │",
		"│ inbox TUI          │",
		"│ GitHub labels /    │",
		"- **GitHub is the visible truth** - `ezoss/triaged` is the public signal that an item has already been handled.",
		"- **Local DB is the private memory** - drafts, rationales, approvals, and token accounting stay on disk under `~/.ezoss/`.",
		"- **Approval is explicit** - nothing gets posted, closed, merged, or labeled until you do it from the inbox.",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("README how-it-works section missing %q:\n%s", want, text)
		}
	}
}

func TestDocsQuickStartUsesSingleInitCommand(t *testing.T) {
	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	text := string(data)
	checks := []string{
		"ezoss init --repo kunchenguid/ezoss --agent auto --merge-method squash",
		"$ ezoss daemon start --mock",
		"$ ezoss status",
		"$ ezoss list",
		"$ ezoss",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs quick start missing %q:\n%s", want, text)
		}
	}

	if strings.Contains(text, "$ ezoss daemon start\n") {
		t.Fatalf("docs quick start should use mock mode for the daemon walkthrough:\n%s", text)
	}

	if strings.Contains(text, "$ ezoss init --merge-method squash") {
		t.Fatalf("docs quick start still contains a second standalone init command:\n%s", text)
	}
}

func TestDocsHeroMatchesCurrentReleaseMessaging(t *testing.T) {
	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	text := string(data)
	checks := []string{
		"<p class=\"eyebrow\">ezoss docs</p>",
		"Run your open source inbox like a review queue, not a background tax.",
		"polls GitHub for untriaged issues and PRs",
		"approve, edit, skip, or rerun from a terminal inbox before anything touches GitHub.",
		"<a class=\"button primary\" href=\"https://github.com/kunchenguid/ezoss\">View on GitHub</a>",
		"<a class=\"button secondary\" href=\"#install\">Install</a>",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs hero section missing %q:\n%s", want, text)
		}
	}
}

func TestDocsInstallSectionMatchesReleaseInstallPaths(t *testing.T) {
	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	text := string(data)
	checks := []string{
		"href=\"#install\"",
		"<section id=\"install\" class=\"section\">",
		"<h2>Install</h2>",
		"go install github.com/kunchenguid/ezoss/cmd/ezoss@latest",
		"curl -fsSL https://raw.githubusercontent.com/kunchenguid/ezoss/main/install.sh | sh",
		"iwr https://raw.githubusercontent.com/kunchenguid/ezoss/main/install.ps1 -useb | iex",
		"git clone https://github.com/kunchenguid/ezoss.git",
		"make build",
		"./bin/ezoss --version",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs install section missing %q:\n%s", want, text)
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

	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	docsData, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	docsText := string(docsData)
	for _, want := range []string{
		"checksums.txt",
		"GitHub releases",
		"SHA-256",
	} {
		if !strings.Contains(docsText, want) {
			t.Fatalf("docs install section missing %q:\n%s", want, docsText)
		}
	}
}

func TestDocsFooterLinksToLicenseAndSourceRepo(t *testing.T) {
	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	text := string(data)
	checks := []string{
		"<footer>",
		"href=\"https://github.com/kunchenguid/ezoss/blob/main/LICENSE\"",
		">MIT licensed</a>",
		"href=\"https://github.com/kunchenguid/ezoss\"",
		"source",
		"repo</a>",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs footer missing %q:\n%s", want, text)
		}
	}
}

func TestDocsConfigurationExampleMatchesCurrentConfigSurface(t *testing.T) {
	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	text := string(data)
	checks := []string{
		"const configExample = `agent: auto",
		"merge_method: merge",
		"sync_labels:",
		"waiting_on: true",
		"stale: true`",
		"Per-repo overrides can",
		"live in <code>.ezoss.yaml</code>",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs configuration section missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "triaged: true") {
		t.Fatalf("docs configuration section should not advertise sync_labels.triaged as configurable:\n%s", text)
	}
}

func TestDocsImportantCommandsMatchCurrentPublicCLI(t *testing.T) {
	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	text := string(data)
	checks := []string{
		"<h2>Important commands</h2>",
		"<h3><code>ezoss doctor</code></h3>",
		"Check GitHub auth, agent availability, daemon health, and local database access.",
		"<h3><code>ezoss status</code></h3>",
		"Print a one-line summary of configured repos and pending recommendations.",
		"<h3><code>ezoss list</code></h3>",
		"Dump pending recommendations as text without opening the TUI.",
		"<h3><code>ezoss update</code></h3>",
		"Download and replace the running binary with the latest release for the current platform.",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs important commands missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ezoss daemon run") {
		t.Fatalf("docs page should not expose the hidden daemon run command:\n%s", text)
	}
}

func TestDocsCoreLoopAndRequirementsMatchCurrentReleaseContract(t *testing.T) {
	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	text := string(data)
	checks := []string{
		"<h2>Core loop</h2>",
		"GitHub stays the visible source of truth",
		"local SQLite database keeps draft comments, agent rationale, and token usage",
		"<h3>1. Poll GitHub</h3>",
		"Fetch issues and PRs missing the <code>ezoss/triaged</code> label.",
		"<h3>2. Ask your agent</h3>",
		"Claude, Codex, Rovo Dev, or OpenCode returns a structured recommendation.",
		"<h3>3. Review privately</h3>",
		"Approve, edit, skip, or rerun from the local TUI inbox without exposing drafts.",
		"<h3>4. Sync the outcome</h3>",
		"Approved actions execute with <code>gh</code> and mirror triage labels back to GitHub.",
		"<h2>Requirements</h2>",
		"<li><code>gh auth login</code> already configured for the GitHub account you want to use.</li>",
		"<li>One supported agent backend installed locally.</li>",
		"<li>Writable state directory under <code>~/.ezoss</code>.</li>",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs core loop or requirements missing %q:\n%s", want, text)
		}
	}
}

func TestDocsInboxActionsMatchCurrentTUIWorkflow(t *testing.T) {
	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	text := string(data)
	checks := []string{
		"<h2>What the inbox can do</h2>",
		"<strong><code>a</code> approve</strong>",
		"Execute the recommended GitHub action and sync triage labels.",
		"<strong><code>e</code> edit</strong>",
		"Open the draft in your editor before approval.",
		"<strong><code>s</code> skip</strong>",
		"Dismiss the recommendation and stamp the GitHub triaged label.",
		"<strong><code>r</code> rerun</strong>",
		"Re-triage the item and replace the active recommendation in place.",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Fatalf("docs inbox actions missing %q:\n%s", want, text)
		}
	}
}

func TestReleaseDocsAndREADMEDescribePRApprovalBeforeReview(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	readmeData, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", readmePath, err)
	}

	readmeText := string(readmeData)
	for _, want := range []string{
		"PRs without prior agreement can be routed into a maintainer approval step before code review.",
		"`request_approval_for_review`",
	} {
		if !strings.Contains(readmeText, want) {
			t.Fatalf("README missing PR approval-before-review guidance %q:\n%s", want, readmeText)
		}
	}

	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	docsData, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	docsText := string(docsData)
	for _, want := range []string{
		"PRs without prior agreement can pause for maintainer approval before any code review happens.",
		"<code>request_approval_for_review</code>",
	} {
		if !strings.Contains(docsText, want) {
			t.Fatalf("docs missing PR approval-before-review guidance %q:\n%s", want, docsText)
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
		"make docs-build # Install docs deps and build ./docs",
		"make install    # go install the CLI",
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
}

func TestReleaseDocsAndREADMEExposeMITLicense(t *testing.T) {
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

	docsPath := filepath.Join("..", "..", "docs", "src", "pages", "index.astro")
	docsData, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", docsPath, err)
	}

	docsText := string(docsData)
	if !strings.Contains(docsText, ">MIT licensed</a>") || !strings.Contains(docsText, "source") || !strings.Contains(docsText, "repo</a>") {
		t.Fatalf("docs footer missing MIT license statement:\n%s", docsText)
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
