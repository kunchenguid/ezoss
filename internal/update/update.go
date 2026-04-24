package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/kunchenguid/ezoss/internal/buildinfo"
	"github.com/kunchenguid/ezoss/internal/paths"
)

const (
	appName            = "ezoss"
	repoName           = "kunchenguid/ezoss"
	maxAPIResponseSize = 5 << 20
	maxDownloadSize    = 100 << 20
	maxExtractedSize   = 100 << 20
)

var githubAPIBaseURL = "https://api.github.com"

type CheckOptions struct {
	AppName        string
	Repo           string
	CurrentVersion string
	APIBaseURL     string
	HTTPClient     *http.Client
	CachePath      string
	Now            func() time.Time
	Stderr         io.Writer
}

type Options struct {
	AppName        string
	Repo           string
	CurrentVersion string
	APIBaseURL     string
	HTTPClient     *http.Client
	CachePath      string
	ExecutablePath string
	Platform       platformSpec
	Now            func() time.Time
	Stderr         io.Writer
	// Beta, when true, considers prereleases when picking the latest
	// version. Implementation hits /repos/<r>/releases (the list endpoint)
	// and selects the highest semver-parseable, non-draft tag. When false
	// (default) we hit /releases/latest, which GitHub already filters to
	// the latest non-prerelease tag.
	Beta bool
	// ResetDaemon, when non-nil, is invoked after the binary is replaced
	// successfully. A nil value disables the reset (used in tests).
	// When the field is left as the zero value, Run uses the package
	// default which dispatches to the managed service or PID-file flow.
	ResetDaemon func() error
	// SkipDaemonReset disables the post-update daemon reset entirely. Set
	// this in tests where a daemon does not exist for the temp AM_HOME.
	SkipDaemonReset bool
	// SkipDaemonPathCheck disables the pre-update guard that aborts when
	// the running daemon's executable path differs from the binary that
	// would be replaced. Used by tests that don't model a running daemon.
	SkipDaemonPathCheck bool
}

type latestRelease struct {
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type updatePlan struct {
	LatestVersion   string
	UpdateAvailable bool
	ArchiveName     string
	Archive         releaseAsset
	Checksums       releaseAsset
}

func RunCheck(ctx context.Context, stdout io.Writer, opts CheckOptions) error {
	u, err := newChecker(stdout, opts)
	if err != nil {
		return err
	}
	return u.run(ctx)
}

func Run(ctx context.Context, stdout, stderr io.Writer, opts Options) error {
	u, err := newUpdater(stdout, stderr, opts)
	if err != nil {
		return err
	}
	return u.run(ctx)
}

type updater struct {
	appName             string
	repo                string
	currentVersion      string
	apiBaseURL          string
	httpClient          *http.Client
	cachePath           string
	executablePath      string
	platform            platformSpec
	now                 func() time.Time
	stdout              io.Writer
	stderr              io.Writer
	resetDaemon         func() error
	paths               *paths.Paths
	spawnBackground     func(currentVersion string) error
	includePrereleases  bool
	skipDaemonPathCheck bool
}

type checker struct {
	appName        string
	repo           string
	currentVersion string
	apiBaseURL     string
	httpClient     *http.Client
	cachePath      string
	now            func() time.Time
	stdout         io.Writer
	stderr         io.Writer
}

func newChecker(stdout io.Writer, opts CheckOptions) (*checker, error) {
	app := opts.AppName
	if app == "" {
		app = appName
	}
	repo := opts.Repo
	if repo == "" {
		repo = repoName
	}
	currentVersion := opts.CurrentVersion
	if currentVersion == "" {
		currentVersion = buildinfo.CurrentVersion()
	}
	apiBaseURL := strings.TrimRight(opts.APIBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = githubAPIBaseURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	cachePath := opts.CachePath
	if cachePath == "" {
		p, err := paths.New()
		if err != nil {
			return nil, fmt.Errorf("resolve paths: %w", err)
		}
		cachePath = p.UpdateCheckPath()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &checker{
		appName:        app,
		repo:           repo,
		currentVersion: currentVersion,
		apiBaseURL:     apiBaseURL,
		httpClient:     httpClient,
		cachePath:      cachePath,
		now:            now,
		stdout:         stdout,
		stderr:         opts.Stderr,
	}, nil
}

func newUpdater(stdout, stderr io.Writer, opts Options) (*updater, error) {
	app := opts.AppName
	if app == "" {
		app = appName
	}
	repo := opts.Repo
	if repo == "" {
		repo = repoName
	}
	currentVersion := opts.CurrentVersion
	if currentVersion == "" {
		currentVersion = buildinfo.CurrentVersion()
	}
	apiBaseURL := strings.TrimRight(opts.APIBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = githubAPIBaseURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	var resolvedPaths *paths.Paths
	cachePath := opts.CachePath
	if cachePath == "" {
		p, err := paths.New()
		if err != nil {
			return nil, fmt.Errorf("resolve paths: %w", err)
		}
		resolvedPaths = p
		cachePath = p.UpdateCheckPath()
	}
	executablePath := opts.ExecutablePath
	if executablePath == "" {
		var err error
		executablePath, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve executable: %w", err)
		}
	}
	platform := opts.Platform
	if platform.GOOS == "" {
		platform.GOOS = runtime.GOOS
	}
	if platform.GOARCH == "" {
		platform.GOARCH = runtime.GOARCH
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	resetDaemon := opts.ResetDaemon
	if resetDaemon == nil && !opts.SkipDaemonReset {
		p := resolvedPaths
		if p == nil {
			fresh, err := paths.New()
			if err == nil {
				p = fresh
			}
		}
		if p != nil {
			resetDaemon = func() error { return resetDaemonHook(p) }
		}
	}
	return &updater{
		appName:             app,
		repo:                repo,
		currentVersion:      currentVersion,
		apiBaseURL:          apiBaseURL,
		httpClient:          httpClient,
		cachePath:           cachePath,
		executablePath:      executablePath,
		platform:            platform,
		now:                 now,
		stdout:              stdout,
		stderr:              stderr,
		resetDaemon:         resetDaemon,
		paths:               resolvedPaths,
		spawnBackground:     defaultSpawnBackground,
		includePrereleases:  opts.Beta,
		skipDaemonPathCheck: opts.SkipDaemonPathCheck,
	}, nil
}

func (c *checker) run(ctx context.Context) error {
	if isDevVersion(c.currentVersion) {
		_, err := fmt.Fprintf(c.stdoutWriter(), "self-update unavailable for development builds (%s)\n", c.currentVersion)
		return err
	}

	latest, err := c.latestRelease(ctx)
	if err != nil {
		return err
	}
	if err := writeCache(c.cachePath, &checkCache{CheckedAt: c.now(), LatestVersion: latest}); err != nil {
		return err
	}

	cmp, err := compareVersions(c.currentVersion, latest)
	if err != nil {
		return err
	}
	if cmp >= 0 {
		_, err = fmt.Fprintf(c.stdoutWriter(), "%s is already up to date (%s)\n", c.appName, c.currentVersion)
		return err
	}
	_, err = fmt.Fprintf(c.stdoutWriter(), "update available: %s -> %s\n", c.currentVersion, latest)
	return err
}

func (u *updater) run(ctx context.Context) error {
	if isDevVersion(u.currentVersion) {
		_, err := fmt.Fprintf(u.stdoutWriter(), "self-update unavailable for development builds (%s)\n", u.currentVersion)
		return err
	}

	plan, err := u.checkLatest(ctx)
	if err != nil {
		return err
	}
	if err := writeCache(u.cachePath, &checkCache{CheckedAt: u.now(), LatestVersion: plan.LatestVersion}); err != nil {
		return err
	}
	if !plan.UpdateAvailable {
		_, err := fmt.Fprintf(u.stdoutWriter(), "%s is already up to date (%s)\n", u.appName, u.currentVersion)
		return err
	}
	if err := u.ensureDaemonUsesCurrentExecutable(); err != nil {
		return err
	}

	archiveData, err := u.downloadAsset(ctx, plan.Archive.BrowserDownloadURL, maxDownloadSize)
	if err != nil {
		return err
	}
	checksumsData, err := u.downloadAsset(ctx, plan.Checksums.BrowserDownloadURL, maxDownloadSize)
	if err != nil {
		return err
	}
	checksums, err := parseChecksums(checksumsData)
	if err != nil {
		return err
	}
	want, ok := checksums[plan.ArchiveName]
	if !ok {
		return fmt.Errorf("checksum not found for %s", plan.ArchiveName)
	}
	if err := verifyChecksum(archiveData, want); err != nil {
		return err
	}
	binaryData, err := u.extractBinary(archiveData)
	if err != nil {
		return err
	}
	if err := replaceExecutable(u.executablePath, binaryData); err != nil {
		return err
	}
	if u.resetDaemon != nil {
		if err := u.resetDaemon(); err != nil {
			var resetErr *daemonResetError
			if errors.As(err, &resetErr) && resetErr.Offline() {
				return fmt.Errorf("updated %s to %s, but daemon is offline: %w", u.appName, plan.LatestVersion, err)
			}
			return fmt.Errorf("updated %s to %s, but failed to reset daemon: %w", u.appName, plan.LatestVersion, err)
		}
	}
	_, err = fmt.Fprintf(u.stdoutWriter(), "updated %s from %s to %s\n", u.appName, u.currentVersion, plan.LatestVersion)
	return err
}

func (u *updater) checkLatest(ctx context.Context) (updatePlan, error) {
	release, err := u.latestRelease(ctx)
	if err != nil {
		return updatePlan{}, err
	}
	cmp, err := compareVersions(u.currentVersion, release.TagName)
	if err != nil {
		return updatePlan{}, err
	}
	plan := updatePlan{
		LatestVersion:   release.TagName,
		UpdateAvailable: cmp < 0,
	}
	if !plan.UpdateAvailable {
		return plan, nil
	}
	archive, checksums, err := pickReleaseAssets(u.appName, release.TagName, release.Assets, u.platform)
	if err != nil {
		return updatePlan{}, err
	}
	plan.ArchiveName = archive.Name
	plan.Archive = archive
	plan.Checksums = checksums
	return plan, nil
}

func (c *checker) latestRelease(ctx context.Context) (string, error) {
	release, err := fetchLatestRelease(ctx, c.httpClient, c.apiBaseURL, c.repo)
	if err != nil {
		return "", err
	}
	return release.TagName, nil
}

func (u *updater) latestRelease(ctx context.Context) (latestRelease, error) {
	if u.includePrereleases {
		return fetchHighestRelease(ctx, u.httpClient, u.apiBaseURL, u.repo)
	}
	return fetchLatestRelease(ctx, u.httpClient, u.apiBaseURL, u.repo)
}

func fetchLatestRelease(ctx context.Context, client *http.Client, apiBaseURL, repo string) (latestRelease, error) {
	resp, err := latestReleaseResponse(ctx, client, apiBaseURL, repo)
	if err != nil {
		return latestRelease{}, err
	}
	defer resp.Body.Close()

	var release latestRelease
	limited := io.LimitReader(resp.Body, maxAPIResponseSize+1)
	if err := json.NewDecoder(limited).Decode(&release); err != nil {
		return latestRelease{}, fmt.Errorf("decode latest release: %w", err)
	}
	if release.TagName == "" {
		return latestRelease{}, fmt.Errorf("decode latest release: empty tag_name")
	}
	return release, nil
}

func latestReleaseResponse(ctx context.Context, client *http.Client, apiBaseURL, repo string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+path.Join("/repos/", repo, "/releases/latest"), nil)
	if err != nil {
		return nil, fmt.Errorf("build latest release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetch latest release: unexpected status %s", resp.Status)
	}
	return resp, nil
}

// fetchHighestRelease lists all releases and returns the highest semver-
// parseable, non-draft tag - including prereleases. Used when --beta is set.
//
// We deliberately do not cap pagination here: GitHub's default page size is
// 30 and that is enough for any project to surface its current and recent
// prereleases. Projects with thousands of unmaintained tags would still get
// the most recent page, which is what users care about.
func fetchHighestRelease(ctx context.Context, client *http.Client, apiBaseURL, repo string) (latestRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+path.Join("/repos/", repo, "/releases"), nil)
	if err != nil {
		return latestRelease{}, fmt.Errorf("build releases request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return latestRelease{}, fmt.Errorf("fetch releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return latestRelease{}, fmt.Errorf("fetch releases: unexpected status %s", resp.Status)
	}

	limited := io.LimitReader(resp.Body, maxAPIResponseSize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return latestRelease{}, fmt.Errorf("read releases: %w", err)
	}
	if int64(len(body)) > maxAPIResponseSize {
		return latestRelease{}, fmt.Errorf("releases response exceeds %d bytes", maxAPIResponseSize)
	}

	var releases []latestRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return latestRelease{}, fmt.Errorf("decode releases: %w", err)
	}

	var best *latestRelease
	var bestVer semVersion
	for i := range releases {
		r := &releases[i]
		if r.Draft || r.TagName == "" {
			continue
		}
		v, err := parseVersion(r.TagName)
		if err != nil {
			continue
		}
		if best == nil || v.compare(bestVer) > 0 {
			best = r
			bestVer = v
		}
	}
	if best == nil {
		return latestRelease{}, fmt.Errorf("no releases found for %s", repo)
	}
	return *best, nil
}

func (u *updater) downloadAsset(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	if err := ensureHTTPS(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build asset request: %w", err)
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download asset: unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read asset: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download asset exceeds %d bytes", limit)
	}
	return data, nil
}

func (c *checker) stdoutWriter() io.Writer {
	if c.stdout == nil {
		return io.Discard
	}
	return c.stdout
}

func (u *updater) stdoutWriter() io.Writer {
	if u.stdout == nil {
		return io.Discard
	}
	return u.stdout
}

func isDevVersion(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" || version == "dev" || version == "(devel)" {
		return true
	}
	// debug.ReadBuildInfo() returns Go pseudo-versions like
	// `v0.0.0-20260424223654-88d1dd693833` for builds off a non-tagged
	// commit, with `+dirty` appended when the working tree has uncommitted
	// changes. Neither is a real release, so self-update prompts are
	// inappropriate.
	if strings.HasPrefix(version, "v0.0.0-") || strings.HasPrefix(version, "0.0.0-") {
		return true
	}
	if strings.Contains(version, "+dirty") {
		return true
	}
	return false
}

// MaybeHandleBackgroundCheck inspects argv (without the program name) and, if
// the first two elements are `--update-check <version>`, refreshes the cache
// silently and returns (true, err). Otherwise returns (false, nil).
//
// The expected wiring is for cmd/<app>/main.go to call this BEFORE cobra
// parses args so the internal flag never reaches the user-facing CLI.
func MaybeHandleBackgroundCheck(args []string) (bool, error) {
	if len(args) != 2 || args[0] != backgroundFlag {
		return false, nil
	}
	u, err := newUpdater(io.Discard, io.Discard, Options{SkipDaemonReset: true, CurrentVersion: args[1]})
	if err != nil {
		return true, err
	}
	return true, u.refreshCache(context.Background())
}

// MaybeNotifyAndCheck prints a "new version available" hint to stderr if the
// cache is fresh and shows an upgrade is available, and spawns a detached
// background check if the cache is stale. Both are best-effort and never
// block the calling command. Skipped on dev builds, on the `update` command,
// and when EZOSS_NO_UPDATE_CHECK=1 is set.
func MaybeNotifyAndCheck(args []string, stderr io.Writer) {
	u, err := newUpdater(io.Discard, stderr, Options{SkipDaemonReset: true})
	if err != nil {
		return
	}
	u.maybeNotifyAndCheck(args)
}

// CachedLatestVersion returns the cached latest version when an update is
// available, or "" otherwise. Useful for status banners.
func CachedLatestVersion() string {
	u, err := newUpdater(io.Discard, io.Discard, Options{SkipDaemonReset: true})
	if err != nil {
		return ""
	}
	return u.cachedLatestVersion()
}

func (u *updater) refreshCache(ctx context.Context) error {
	plan, err := u.checkLatest(ctx)
	if err != nil {
		return err
	}
	return writeCache(u.cachePath, &checkCache{
		CheckedAt:     u.now(),
		LatestVersion: plan.LatestVersion,
	})
}

func (u *updater) maybeNotifyAndCheck(args []string) {
	if isDevVersion(u.currentVersion) || os.Getenv(noUpdateCheckEnv) == "1" {
		return
	}
	if len(args) > 0 && (args[0] == "update" || args[0] == backgroundFlag) {
		return
	}
	cache := readCache(u.cachePath)
	if cache != nil {
		cmp, err := compareVersions(u.currentVersion, cache.LatestVersion)
		if err == nil && cmp < 0 {
			fmt.Fprintf(u.stderrWriter(), "\033[33mA new version of %s is available: %s -> %s\nRun \"%s update\" to update\033[0m\n",
				u.appName, u.currentVersion, cache.LatestVersion, u.appName)
		}
	}
	if cacheStale(cache, u.currentVersion, u.now()) && u.spawnBackground != nil {
		_ = u.spawnBackground(u.currentVersion)
	}
}

func (u *updater) cachedLatestVersion() string {
	if u == nil || isDevVersion(u.currentVersion) || os.Getenv(noUpdateCheckEnv) == "1" {
		return ""
	}
	cache := readCache(u.cachePath)
	if cache == nil {
		return ""
	}
	cmp, err := compareVersions(u.currentVersion, cache.LatestVersion)
	if err != nil || cmp >= 0 {
		return ""
	}
	return cache.LatestVersion
}

func (u *updater) stderrWriter() io.Writer {
	if u.stderr == nil {
		return io.Discard
	}
	return u.stderr
}
