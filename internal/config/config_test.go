package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadGlobalDefaults(t *testing.T) {
	cfg, err := LoadGlobal(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}

	if cfg.Agent != AgentAuto {
		t.Fatalf("Agent = %q, want %q", cfg.Agent, AgentAuto)
	}
	if cfg.PollInterval != 5*time.Minute {
		t.Fatalf("PollInterval = %v, want %v", cfg.PollInterval, 5*time.Minute)
	}
	if cfg.StaleThreshold != 30*24*time.Hour {
		t.Fatalf("StaleThreshold = %v, want %v", cfg.StaleThreshold, 30*24*time.Hour)
	}
	if cfg.IgnoreOlderThan != 365*24*time.Hour {
		t.Fatalf("IgnoreOlderThan = %v, want %v", cfg.IgnoreOlderThan, 365*24*time.Hour)
	}
	if !cfg.SyncLabels.Triaged || !cfg.SyncLabels.WaitingOn || !cfg.SyncLabels.Stale {
		t.Fatalf("SyncLabels = %+v, want all true", cfg.SyncLabels)
	}
	if len(cfg.Repos) != 0 {
		t.Fatalf("Repos = %v, want empty", cfg.Repos)
	}
	if cfg.Fixes.PRCreate != PRCreateAuto {
		t.Fatalf("Fixes.PRCreate = %q, want %q", cfg.Fixes.PRCreate, PRCreateAuto)
	}
	if !cfg.Contrib.Enabled {
		t.Fatalf("Contrib.Enabled = false, want true (contributor mode is on by default)")
	}
}

func TestLoadGlobalContribExplicitOptOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("contrib:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal error: %v", err)
	}
	if cfg.Contrib.Enabled {
		t.Fatal("explicit contrib.enabled: false must override the default true")
	}
}

func TestSaveGlobalPreservesLoadedContribExplicitOptOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("contrib:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}

	if err := SaveGlobal(path, cfg); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	got, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal() after save error = %v", err)
	}
	if got.Contrib.Enabled {
		t.Fatal("contrib.enabled = true, want explicit false preserved")
	}
}

func TestLoadGlobalContribIgnoreReposKeepsDefaultEnabled(t *testing.T) {
	// Regression: a user who only sets ignore_repos (no enabled key)
	// must keep the default true. Earlier versions used a plain bool
	// here and silently disabled contrib mode in this case.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("contrib:\n  ignore_repos:\n    - noisy/repo\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal error: %v", err)
	}
	if !cfg.Contrib.Enabled {
		t.Fatalf("Contrib.Enabled = false, want true (only ignore_repos set, enabled key absent)")
	}
	if len(cfg.Contrib.IgnoreRepos) != 1 || cfg.Contrib.IgnoreRepos[0] != "noisy/repo" {
		t.Fatalf("IgnoreRepos = %v, want [noisy/repo]", cfg.Contrib.IgnoreRepos)
	}
}

func TestEnsureDefaultGlobalConfigCreatesLoadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")

	EnsureDefaultGlobalConfig(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"agent: auto",
		"poll_interval: 5m",
		"stale_threshold: 30d",
		"ignore_older_than: 365d",
		"pr_create: auto",
		"waiting_on: true",
		"stale: true",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("default config missing %q", want)
		}
	}
	if strings.Contains(content, "triaged:") {
		t.Fatalf("default config should not expose sync_labels.triaged anymore: %q", content)
	}

	cfg, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}
	if cfg.Agent != AgentAuto {
		t.Fatalf("Agent = %q, want %q", cfg.Agent, AgentAuto)
	}
}

func TestEnsureDefaultGlobalConfigDoesNotOverwriteExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	want := "agent: codex\n"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	EnsureDefaultGlobalConfig(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != want {
		t.Fatalf("file content = %q, want %q", string(data), want)
	}
}

func TestSaveGlobalWritesLoadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	want := &GlobalConfig{
		Agent:          AgentCodex,
		PollInterval:   10 * time.Minute,
		StaleThreshold: 14 * 24 * time.Hour,
		MergeMethod:    "squash",
		Fixes:          FixesConfig{PRCreate: PRCreateNoMistakes},
		Repos:          []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"},
		SyncLabels:     SyncLabels{Triaged: true, WaitingOn: false, Stale: true},
	}

	if err := SaveGlobal(path, want); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	got, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}
	if got.Agent != want.Agent {
		t.Fatalf("Agent = %q, want %q", got.Agent, want.Agent)
	}
	if got.PollInterval != want.PollInterval {
		t.Fatalf("PollInterval = %v, want %v", got.PollInterval, want.PollInterval)
	}
	if got.StaleThreshold != want.StaleThreshold {
		t.Fatalf("StaleThreshold = %v, want %v", got.StaleThreshold, want.StaleThreshold)
	}
	if got.MergeMethod != want.MergeMethod {
		t.Fatalf("MergeMethod = %q, want %q", got.MergeMethod, want.MergeMethod)
	}
	if got.Fixes.PRCreate != want.Fixes.PRCreate {
		t.Fatalf("Fixes.PRCreate = %q, want %q", got.Fixes.PRCreate, want.Fixes.PRCreate)
	}
	if strings.Join(got.Repos, ",") != strings.Join(want.Repos, ",") {
		t.Fatalf("Repos = %v, want %v", got.Repos, want.Repos)
	}
	if got.SyncLabels != want.SyncLabels {
		t.Fatalf("SyncLabels = %+v, want %+v", got.SyncLabels, want.SyncLabels)
	}
}

func TestSaveGlobalWritesDayBasedStaleThresholdWhenWholeDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := SaveGlobal(path, &GlobalConfig{StaleThreshold: 14 * 24 * time.Hour}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "stale_threshold: 14d") {
		t.Fatalf("saved config = %q, want stale_threshold serialized as 14d", string(data))
	}
}

func TestSaveGlobalDefaultsMissingStaleThresholdToDayBasedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := SaveGlobal(path, &GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "stale_threshold: 30d") {
		t.Fatalf("saved config = %q, want stale_threshold serialized as 30d", string(data))
	}
}

func TestSaveGlobalDefaultsMissingSyncLabelsToEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := SaveGlobal(path, &GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	got, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}
	if got.SyncLabels != defaultSyncLabels() {
		t.Fatalf("SyncLabels = %+v, want %+v", got.SyncLabels, defaultSyncLabels())
	}
}

func TestSaveGlobalPreservesDisabledOptionalSyncLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := SaveGlobal(path, &GlobalConfig{SyncLabels: SyncLabels{Triaged: true, WaitingOn: false, Stale: false}}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	got, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}
	if got.SyncLabels.WaitingOn {
		t.Fatalf("SyncLabels.WaitingOn = true, want false")
	}
	if got.SyncLabels.Stale {
		t.Fatalf("SyncLabels.Stale = true, want false")
	}
}

func TestLoadGlobalFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := `agent: codex
poll_interval: 10m
stale_threshold: 168h
merge_method: rebase
fixes:
  pr_create: gh
repos:
  - kunchenguid/no-mistakes
  - kunchenguid/ezoss
sync_labels:
  triaged: true
  waiting_on: false
  stale: true
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}

	if cfg.Agent != AgentCodex {
		t.Fatalf("Agent = %q, want %q", cfg.Agent, AgentCodex)
	}
	if cfg.PollInterval != 10*time.Minute {
		t.Fatalf("PollInterval = %v, want %v", cfg.PollInterval, 10*time.Minute)
	}
	if cfg.StaleThreshold != 168*time.Hour {
		t.Fatalf("StaleThreshold = %v, want %v", cfg.StaleThreshold, 168*time.Hour)
	}
	if cfg.MergeMethod != "rebase" {
		t.Fatalf("MergeMethod = %q, want %q", cfg.MergeMethod, "rebase")
	}
	if cfg.Fixes.PRCreate != PRCreateGH {
		t.Fatalf("Fixes.PRCreate = %q, want %q", cfg.Fixes.PRCreate, PRCreateGH)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("Repos len = %d, want 2", len(cfg.Repos))
	}
	if cfg.SyncLabels.WaitingOn {
		t.Fatalf("SyncLabels.WaitingOn = true, want false")
	}
}

func TestLoadGlobalRejectsInvalidFixPRCreateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := `fixes:
  pr_create: magic
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadGlobal(path)
	if err == nil {
		t.Fatal("LoadGlobal() error = nil, want invalid pr_create error")
	}
	if !strings.Contains(err.Error(), "invalid fixes.pr_create") {
		t.Fatalf("LoadGlobal() error = %v, want invalid fixes.pr_create", err)
	}
}

func TestLoadGlobalDropsLegacyTriagedSyncLabelSettingOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := `sync_labels:
  triaged: false
  waiting_on: false
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}

	if cfg.SyncLabels.WaitingOn {
		t.Fatalf("SyncLabels.WaitingOn = true, want false")
	}
	if !cfg.SyncLabels.Stale {
		t.Fatalf("SyncLabels.Stale = false, want true")
	}

	savedPath := filepath.Join(t.TempDir(), "saved.yaml")
	if err := SaveGlobal(savedPath, &GlobalConfig{
		Agent:          cfg.Agent,
		PollInterval:   cfg.PollInterval,
		StaleThreshold: cfg.StaleThreshold,
		MergeMethod:    cfg.MergeMethod,
		Repos:          cfg.Repos,
		SyncLabels:     cfg.SyncLabels,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	saved, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(saved), "triaged:") {
		t.Fatalf("saved config should not expose legacy sync_labels.triaged: %q", string(saved))
	}
}

func TestLoadGlobalAcceptsDayDurationSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := "stale_threshold: 30d\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}

	if cfg.StaleThreshold != 30*24*time.Hour {
		t.Fatalf("StaleThreshold = %v, want %v", cfg.StaleThreshold, 30*24*time.Hour)
	}
}

func TestLoadGlobalInvalidDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("poll_interval: nope\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadGlobal(path)
	if err == nil {
		t.Fatal("LoadGlobal() error = nil, want error")
	}
}

func TestLoadGlobalInvalidMergeMethod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("merge_method: fast-forward\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadGlobal(path)
	if err == nil {
		t.Fatal("LoadGlobal() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "merge_method") {
		t.Fatalf("LoadGlobal() error = %v, want merge_method detail", err)
	}
}

func TestSaveGlobalRejectsInvalidMergeMethod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	err := SaveGlobal(path, &GlobalConfig{MergeMethod: "fast-forward"})
	if err == nil {
		t.Fatal("SaveGlobal() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "merge_method") {
		t.Fatalf("SaveGlobal() error = %v, want merge_method detail", err)
	}
}

func TestLoadRepoDefaults(t *testing.T) {
	cfg, err := LoadRepo(t.TempDir())
	if err != nil {
		t.Fatalf("LoadRepo() error = %v", err)
	}
	if cfg.Agent != "" {
		t.Fatalf("Agent = %q, want empty", cfg.Agent)
	}
}

func TestLoadRepoFromFile(t *testing.T) {
	dir := t.TempDir()
	contents := "agent: opencode\n"
	if err := os.WriteFile(filepath.Join(dir, ".ezoss.yaml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadRepo(dir)
	if err != nil {
		t.Fatalf("LoadRepo() error = %v", err)
	}
	if cfg.Agent != AgentOpenCode {
		t.Fatalf("Agent = %q, want %q", cfg.Agent, AgentOpenCode)
	}
}

func TestMergeRepoOverridesAgent(t *testing.T) {
	global := &GlobalConfig{Agent: AgentClaude, PollInterval: 5 * time.Minute, StaleThreshold: 30 * 24 * time.Hour, SyncLabels: defaultSyncLabels()}
	repo := &RepoConfig{Agent: AgentRovoDev}

	cfg := Merge(global, repo)

	if cfg.Agent != AgentRovoDev {
		t.Fatalf("Agent = %q, want %q", cfg.Agent, AgentRovoDev)
	}
	if cfg.PollInterval != 5*time.Minute {
		t.Fatalf("PollInterval = %v, want %v", cfg.PollInterval, 5*time.Minute)
	}
	if !cfg.SyncLabels.Triaged {
		t.Fatalf("SyncLabels.Triaged = false, want true")
	}
}
