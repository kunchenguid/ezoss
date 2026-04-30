package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
	"gopkg.in/yaml.v3"
)

type AgentName = sharedtypes.AgentName

const (
	AgentAuto     AgentName = sharedtypes.AgentAuto
	AgentClaude   AgentName = sharedtypes.AgentClaude
	AgentCodex    AgentName = sharedtypes.AgentCodex
	AgentRovoDev  AgentName = sharedtypes.AgentRovoDev
	AgentOpenCode AgentName = sharedtypes.AgentOpenCode
)

type SyncLabels struct {
	Triaged   bool
	WaitingOn bool `yaml:"waiting_on"`
	Stale     bool `yaml:"stale"`
}

type PRCreateMode string

const (
	PRCreateAuto       PRCreateMode = "auto"
	PRCreateNoMistakes PRCreateMode = "no-mistakes"
	PRCreateGH         PRCreateMode = "gh"
	PRCreateDisabled   PRCreateMode = "disabled"
)

type FixesConfig struct {
	PRCreate PRCreateMode `yaml:"pr_create"`
}

type syncLabelsRaw struct {
	Triaged   *bool `yaml:"triaged"`
	WaitingOn *bool `yaml:"waiting_on"`
	Stale     *bool `yaml:"stale"`
}

type GlobalConfig struct {
	Agent           AgentName
	PollInterval    time.Duration
	StaleThreshold  time.Duration
	IgnoreOlderThan time.Duration
	MergeMethod     string
	Repos           []string
	SyncLabels      SyncLabels
	Fixes           FixesConfig
}

type globalConfigRaw struct {
	Agent           AgentName      `yaml:"agent"`
	PollInterval    string         `yaml:"poll_interval"`
	StaleThreshold  string         `yaml:"stale_threshold"`
	IgnoreOlderThan string         `yaml:"ignore_older_than"`
	MergeMethod     string         `yaml:"merge_method"`
	Repos           []string       `yaml:"repos"`
	SyncLabels      *syncLabelsRaw `yaml:"sync_labels"`
	Fixes           *FixesConfig   `yaml:"fixes"`
}

type globalConfigFile struct {
	Agent           AgentName      `yaml:"agent"`
	PollInterval    string         `yaml:"poll_interval"`
	StaleThreshold  string         `yaml:"stale_threshold"`
	IgnoreOlderThan string         `yaml:"ignore_older_than"`
	MergeMethod     string         `yaml:"merge_method"`
	Repos           []string       `yaml:"repos"`
	Fixes           FixesConfig    `yaml:"fixes"`
	SyncLabels      syncLabelsFile `yaml:"sync_labels"`
}

type syncLabelsFile struct {
	WaitingOn bool `yaml:"waiting_on"`
	Stale     bool `yaml:"stale"`
}

type RepoConfig struct {
	Agent AgentName `yaml:"agent"`
}

type Config struct {
	Agent           AgentName
	PollInterval    time.Duration
	StaleThreshold  time.Duration
	IgnoreOlderThan time.Duration
	MergeMethod     string
	Repos           []string
	SyncLabels      SyncLabels
	Fixes           FixesConfig
}

const defaultMergeMethod = "merge"

func ParseDuration(value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	if strings.HasSuffix(trimmed, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(trimmed, "d"), 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(trimmed)
}

func normalizeMergeMethod(value string) (string, error) {
	method := strings.ToLower(strings.TrimSpace(value))
	if method == "" {
		return defaultMergeMethod, nil
	}
	switch method {
	case "merge", "squash", "rebase":
		return method, nil
	default:
		return "", fmt.Errorf("invalid merge_method %q: must be merge, squash, or rebase", value)
	}
}

func normalizePRCreateMode(value PRCreateMode) (PRCreateMode, error) {
	mode := PRCreateMode(strings.ToLower(strings.TrimSpace(string(value))))
	if mode == "" {
		return PRCreateAuto, nil
	}
	switch mode {
	case PRCreateAuto, PRCreateNoMistakes, PRCreateGH, PRCreateDisabled:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid fixes.pr_create %q: must be auto, no-mistakes, gh, or disabled", value)
	}
}

const defaultConfigYAML = `# ezoss global configuration

# Agent to use for triage
# Options: auto, claude, codex, rovodev, opencode
agent: auto

# Poll GitHub for new items needing triage
poll_interval: 5m

# Surface stale contributor waits after this duration
stale_threshold: 30d

# Skip items whose last update is older than this. Set to 0 to disable.
ignore_older_than: 365d

# Default PR merge method: merge, squash, or rebase
merge_method: merge

# How ezoss should create fix PRs.
# Options: auto, no-mistakes, gh, disabled
fixes:
  pr_create: auto

# Repositories to monitor
repos: []

# Optional ezoss/* state labels to mirror to GitHub.
# ezoss/triaged is always managed automatically.
sync_labels:
  waiting_on: true
  stale: true
`

func defaultSyncLabels() SyncLabels {
	return SyncLabels{Triaged: true, WaitingOn: true, Stale: true}
}

func defaultGlobalConfig() *GlobalConfig {
	return &GlobalConfig{
		Agent:           AgentAuto,
		PollInterval:    5 * time.Minute,
		StaleThreshold:  30 * 24 * time.Hour,
		IgnoreOlderThan: 365 * 24 * time.Hour,
		MergeMethod:     defaultMergeMethod,
		SyncLabels:      defaultSyncLabels(),
		Fixes:           FixesConfig{PRCreate: PRCreateAuto},
	}
}

func formatConfigDuration(value time.Duration) string {
	if value > 0 && value%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", value/(24*time.Hour))
	}
	return value.String()
}

func EnsureDefaultGlobalConfig(path string) {
	if _, err := os.Stat(path); err == nil {
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}

	_ = os.WriteFile(path, []byte(defaultConfigYAML), 0o644)
}

func SaveGlobal(path string, cfg *GlobalConfig) error {
	defaultCfg := defaultGlobalConfig()
	if cfg == nil {
		cfg = defaultCfg
	}

	file := globalConfigFile{
		Agent:           cfg.Agent,
		PollInterval:    cfg.PollInterval.String(),
		StaleThreshold:  formatConfigDuration(cfg.StaleThreshold),
		IgnoreOlderThan: formatConfigDuration(cfg.IgnoreOlderThan),
		MergeMethod:     cfg.MergeMethod,
		Repos:           append([]string(nil), cfg.Repos...),
		Fixes:           cfg.Fixes,
		SyncLabels: syncLabelsFile{
			WaitingOn: cfg.SyncLabels.WaitingOn,
			Stale:     cfg.SyncLabels.Stale,
		},
	}
	if file.Agent == "" {
		file.Agent = AgentAuto
	}
	if file.PollInterval == "0s" || file.PollInterval == "" {
		file.PollInterval = defaultCfg.PollInterval.String()
	}
	if file.StaleThreshold == "0s" || file.StaleThreshold == "" {
		file.StaleThreshold = formatConfigDuration(defaultCfg.StaleThreshold)
	}
	if file.IgnoreOlderThan == "0s" || file.IgnoreOlderThan == "" {
		file.IgnoreOlderThan = formatConfigDuration(defaultCfg.IgnoreOlderThan)
	}
	if !cfg.SyncLabels.Triaged && file.SyncLabels == (syncLabelsFile{}) {
		file.SyncLabels = syncLabelsFile{WaitingOn: defaultCfg.SyncLabels.WaitingOn, Stale: defaultCfg.SyncLabels.Stale}
	}
	mergeMethod, err := normalizeMergeMethod(file.MergeMethod)
	if err != nil {
		return err
	}
	file.MergeMethod = mergeMethod
	prCreate, err := normalizePRCreateMode(file.Fixes.PRCreate)
	if err != nil {
		return err
	}
	file.Fixes.PRCreate = prCreate

	data, err := yaml.Marshal(&file)
	if err != nil {
		return fmt.Errorf("marshal global config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write global config: %w", err)
	}
	return nil
}

func LoadGlobal(path string) (*GlobalConfig, error) {
	cfg := defaultGlobalConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read global config: %w", err)
	}

	var raw globalConfigRaw
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse global config: %w", err)
	}

	if raw.Agent != "" {
		cfg.Agent = raw.Agent
	}
	if raw.PollInterval != "" {
		d, err := ParseDuration(raw.PollInterval)
		if err != nil {
			return nil, fmt.Errorf("parse poll_interval %q: %w", raw.PollInterval, err)
		}
		cfg.PollInterval = d
	}
	if raw.StaleThreshold != "" {
		d, err := ParseDuration(raw.StaleThreshold)
		if err != nil {
			return nil, fmt.Errorf("parse stale_threshold %q: %w", raw.StaleThreshold, err)
		}
		cfg.StaleThreshold = d
	}
	if raw.IgnoreOlderThan != "" {
		d, err := ParseDuration(raw.IgnoreOlderThan)
		if err != nil {
			return nil, fmt.Errorf("parse ignore_older_than %q: %w", raw.IgnoreOlderThan, err)
		}
		cfg.IgnoreOlderThan = d
	}
	if raw.MergeMethod != "" {
		mergeMethod, err := normalizeMergeMethod(raw.MergeMethod)
		if err != nil {
			return nil, err
		}
		cfg.MergeMethod = mergeMethod
	}
	if raw.Fixes != nil {
		prCreate, err := normalizePRCreateMode(raw.Fixes.PRCreate)
		if err != nil {
			return nil, err
		}
		cfg.Fixes.PRCreate = prCreate
	}
	if raw.Repos != nil {
		cfg.Repos = append([]string(nil), raw.Repos...)
	}
	if raw.SyncLabels != nil {
		if raw.SyncLabels.WaitingOn != nil {
			cfg.SyncLabels.WaitingOn = *raw.SyncLabels.WaitingOn
		}
		if raw.SyncLabels.Stale != nil {
			cfg.SyncLabels.Stale = *raw.SyncLabels.Stale
		}
	}

	return cfg, nil
}

func LoadRepo(dir string) (*RepoConfig, error) {
	path := filepath.Join(dir, ".ezoss.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &RepoConfig{}, nil
		}
		return nil, fmt.Errorf("read repo config: %w", err)
	}

	var cfg RepoConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse repo config: %w", err)
	}

	return &cfg, nil
}

func Merge(global *GlobalConfig, repo *RepoConfig) *Config {
	if global == nil {
		global = defaultGlobalConfig()
	}
	if repo == nil {
		repo = &RepoConfig{}
	}

	cfg := &Config{
		Agent:           global.Agent,
		PollInterval:    global.PollInterval,
		StaleThreshold:  global.StaleThreshold,
		IgnoreOlderThan: global.IgnoreOlderThan,
		MergeMethod:     global.MergeMethod,
		Repos:           append([]string(nil), global.Repos...),
		SyncLabels:      global.SyncLabels,
		Fixes:           global.Fixes,
	}
	if repo.Agent != "" {
		cfg.Agent = repo.Agent
	}

	return cfg
}
