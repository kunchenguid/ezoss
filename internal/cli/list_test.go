package cli

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/daemon"
	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/telemetry"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
	_ "modernc.org/sqlite"
)

func TestRootCommandIncludesListSubcommand(t *testing.T) {
	cmd := NewRootCmd()

	got, _, err := cmd.Find([]string{"list"})
	if err != nil {
		t.Fatalf("Find(list) error = %v", err)
	}
	if got == nil || got.Name() != "list" {
		t.Fatalf("Find(list) = %v, want list command", got)
	}
}

func TestListCommandPrintsNoPendingRecommendationsWithoutDB(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); got != "no pending recommendations\n" {
		t.Fatalf("output = %q, want %q", got, "no pending recommendations\n")
	}
}

func TestListCommandPrintsActiveRecommendations(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "kunchenguid/ezoss#42",
		RepoID:    "kunchenguid/ezoss",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		Author:    "octocat",
		State:     sharedtypes.ItemStateOpen,
		GHTriaged: false,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange:  sharedtypes.StateChangeNone,
			Confidence:   sharedtypes.ConfidenceMedium,
			DraftComment: "Thanks for the report.",
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	wantFragments := []string{
		"ITEM",
		"KIND",
		"ACTION",
		"CONFIDENCE",
		"TITLE",
		"URL",
		"kunchenguid/ezoss#42",
		"issue",
		"comment",
		"medium",
		"Bug: triage queue stalls",
		"https://github.com/kunchenguid/ezoss/issues/42",
		"1 pending recommendation",
		"ezoss",
	}
	for _, frag := range wantFragments {
		if !strings.Contains(got, frag) {
			t.Fatalf("output = %q, missing fragment %q", got, frag)
		}
	}
}

func TestListCommandIncludesPullRequestURLForPRKind(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:     "kunchenguid/ezoss#7",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindPR,
		Number: 7,
		Title:  "feat: add streaming",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "kunchenguid/ezoss#7",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange:  sharedtypes.StateChangeNone,
			DraftComment: "Can you confirm the approach before I review?",
			Confidence:   sharedtypes.ConfidenceMedium,
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); !strings.Contains(got, "https://github.com/kunchenguid/ezoss/pull/7") {
		t.Fatalf("output = %q, want PR URL", got)
	}
	if got := buf.String(); !strings.Contains(got, "comment") {
		t.Fatalf("output = %q, want 'comment' action label", got)
	}
}

func TestListCommandUsesFriendlyLabelForNoActionRecommendations(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:     "acme/widgets#11",
		RepoID: "acme/widgets",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 11,
		Title:  "Question already answered",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#11",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceHigh,
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "mark triaged") {
		t.Fatalf("output = %q, want friendly no-action label", got)
	}
	if strings.Contains(got, "	none	") || strings.Contains(got, " none ") {
		t.Fatalf("output = %q, did not want internal no-action label", got)
	}
}

func TestListCommandIncludesRecommendationAgeColumn(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	dbPath := filepath.Join(tempRoot, "ezoss.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:     "kunchenguid/ezoss#7",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindPR,
		Number: 7,
		Title:  "feat: add streaming",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "kunchenguid/ezoss#7",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("sqlDB.Close() error = %v", err)
		}
	})
	if _, err := sqlDB.Exec(`UPDATE recommendations SET created_at = ? WHERE id = ?`, time.Now().Add(-5*time.Minute).Unix(), rec.ID); err != nil {
		t.Fatalf("UPDATE recommendations created_at error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	for _, frag := range []string{"AGE", "5m", "https://github.com/kunchenguid/ezoss/pull/7"} {
		if !strings.Contains(got, frag) {
			t.Fatalf("output = %q, missing fragment %q", got, frag)
		}
	}
}

func TestListCommandWarnsWhenDaemonIsStopped(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalReadDaemonStatus := readDaemonStatus
	t.Cleanup(func() {
		newPaths = originalNewPaths
		readDaemonStatus = originalReadDaemonStatus
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	readDaemonStatus = func(string) (daemon.Status, error) {
		return daemon.Status{State: daemon.StateStopped}, nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:     "kunchenguid/ezoss#42",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "seed",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceMedium,
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	for _, frag := range []string{
		"1 pending recommendation",
		"warning: daemon is not running; this inbox will not refresh until you run `ezoss daemon start`.",
	} {
		if !strings.Contains(got, frag) {
			t.Fatalf("output = %q, missing fragment %q", got, frag)
		}
	}
}

func TestListCommandWarnsForRecommendationsFromUnconfiguredRepos(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	for _, repoID := range []string{"kunchenguid/ezoss", "orphan/alpha", "orphan/beta"} {
		if err := database.UpsertRepo(db.Repo{ID: repoID, DefaultBranch: "main"}); err != nil {
			t.Fatalf("UpsertRepo(%s) error = %v", repoID, err)
		}
	}
	seed := []struct {
		repoID string
		number int
	}{
		{"kunchenguid/ezoss", 42},
		{"orphan/alpha", 1},
		{"orphan/beta", 2},
	}
	for _, s := range seed {
		if err := database.UpsertItem(db.Item{
			ID:     fmt.Sprintf("%s#%d", s.repoID, s.number),
			RepoID: s.repoID,
			Kind:   sharedtypes.ItemKindIssue,
			Number: s.number,
			Title:  "seed",
			State:  sharedtypes.ItemStateOpen,
		}); err != nil {
			t.Fatalf("UpsertItem(%s#%d) error = %v", s.repoID, s.number, err)
		}
		if _, err := database.InsertRecommendation(db.NewRecommendation{
			ItemID: fmt.Sprintf("%s#%d", s.repoID, s.number),
			Agent:  sharedtypes.AgentClaude,
			Options: []db.NewRecommendationOption{{
				StateChange: sharedtypes.StateChangeNone,
				Confidence:  sharedtypes.ConfidenceMedium,
			}},
		}); err != nil {
			t.Fatalf("InsertRecommendation(%s#%d) error = %v", s.repoID, s.number, err)
		}
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	wantFragments := []string{
		"note:",
		"2 recommendations are for repos not in your config",
		"orphan/alpha",
		"orphan/beta",
		"ezoss init --repo orphan/alpha --repo orphan/beta",
	}
	for _, frag := range wantFragments {
		if !strings.Contains(got, frag) {
			t.Fatalf("output = %q, missing fragment %q", got, frag)
		}
	}
}

func TestListCommandShowsConfiguredReposBeforeUnconfiguredOnes(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	dbPath := filepath.Join(tempRoot, "ezoss.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	for _, repoID := range []string{"kunchenguid/ezoss", "orphan/alpha"} {
		if err := database.UpsertRepo(db.Repo{ID: repoID, DefaultBranch: "main"}); err != nil {
			t.Fatalf("UpsertRepo(%s) error = %v", repoID, err)
		}
	}

	configuredItemID := "kunchenguid/ezoss#42"
	if err := database.UpsertItem(db.Item{
		ID:     configuredItemID,
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "configured item",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem(configured) error = %v", err)
	}
	configuredRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: configuredItemID,
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation(configured) error = %v", err)
	}

	orphanItemID := "orphan/alpha#7"
	if err := database.UpsertItem(db.Item{
		ID:     orphanItemID,
		RepoID: "orphan/alpha",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 7,
		Title:  "orphan item",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem(orphan) error = %v", err)
	}
	orphanRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: orphanItemID,
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation(orphan) error = %v", err)
	}

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("sqlDB.Close() error = %v", err)
		}
	})
	if _, err := sqlDB.Exec(`UPDATE recommendations SET created_at = ? WHERE id = ?`, time.Now().Add(-2*time.Minute).Unix(), configuredRec.ID); err != nil {
		t.Fatalf("set configured recommendation created_at: %v", err)
	}
	if _, err := sqlDB.Exec(`UPDATE recommendations SET created_at = ? WHERE id = ?`, time.Now().Add(-time.Minute).Unix(), orphanRec.ID); err != nil {
		t.Fatalf("set orphan recommendation created_at: %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	configuredIndex := strings.Index(got, configuredItemID)
	orphanIndex := strings.Index(got, orphanItemID)
	if configuredIndex == -1 || orphanIndex == -1 {
		t.Fatalf("output = %q, missing expected rows", got)
	}
	if configuredIndex > orphanIndex {
		t.Fatalf("output = %q, want configured repo row before unconfigured repo row", got)
	}
}

func TestListCommandMarksUnconfiguredRowsInline(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	for _, repoID := range []string{"kunchenguid/ezoss", "orphan/alpha"} {
		if err := database.UpsertRepo(db.Repo{ID: repoID, DefaultBranch: "main"}); err != nil {
			t.Fatalf("UpsertRepo(%s) error = %v", repoID, err)
		}
	}

	for _, seed := range []struct {
		repoID string
		number int
		title  string
	}{
		{repoID: "kunchenguid/ezoss", number: 42, title: "configured item"},
		{repoID: "orphan/alpha", number: 7, title: "orphan item"},
	} {
		itemID := fmt.Sprintf("%s#%d", seed.repoID, seed.number)
		if err := database.UpsertItem(db.Item{
			ID:     itemID,
			RepoID: seed.repoID,
			Kind:   sharedtypes.ItemKindIssue,
			Number: seed.number,
			Title:  seed.title,
			State:  sharedtypes.ItemStateOpen,
		}); err != nil {
			t.Fatalf("UpsertItem(%s) error = %v", itemID, err)
		}
		if _, err := database.InsertRecommendation(db.NewRecommendation{
			ItemID: itemID,
			Agent:  sharedtypes.AgentClaude,
			Options: []db.NewRecommendationOption{{
				StateChange: sharedtypes.StateChangeNone,
				Confidence:  sharedtypes.ConfidenceMedium,
			}},
		}); err != nil {
			t.Fatalf("InsertRecommendation(%s) error = %v", itemID, err)
		}
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "orphan/alpha#7 (unconfigured)") {
		t.Fatalf("output = %q, want inline unconfigured marker", got)
	}
	if strings.Contains(got, "kunchenguid/ezoss#42 (unconfigured)") {
		t.Fatalf("output = %q, did not want configured repo row marked unconfigured", got)
	}
}

func TestListCommandSummarizesConfiguredAndUnconfiguredQueueCounts(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	for _, repoID := range []string{"kunchenguid/ezoss", "orphan/alpha"} {
		if err := database.UpsertRepo(db.Repo{ID: repoID, DefaultBranch: "main"}); err != nil {
			t.Fatalf("UpsertRepo(%s) error = %v", repoID, err)
		}
	}

	for _, seed := range []struct {
		repoID string
		number int
	}{
		{repoID: "kunchenguid/ezoss", number: 42},
		{repoID: "orphan/alpha", number: 7},
	} {
		itemID := fmt.Sprintf("%s#%d", seed.repoID, seed.number)
		if err := database.UpsertItem(db.Item{
			ID:     itemID,
			RepoID: seed.repoID,
			Kind:   sharedtypes.ItemKindIssue,
			Number: seed.number,
			Title:  itemID,
			State:  sharedtypes.ItemStateOpen,
		}); err != nil {
			t.Fatalf("UpsertItem(%s) error = %v", itemID, err)
		}
		if _, err := database.InsertRecommendation(db.NewRecommendation{
			ItemID: itemID,
			Agent:  sharedtypes.AgentClaude,
			Options: []db.NewRecommendationOption{{
				StateChange: sharedtypes.StateChangeNone,
				Confidence:  sharedtypes.ConfidenceMedium,
			}},
		}); err != nil {
			t.Fatalf("InsertRecommendation(%s) error = %v", itemID, err)
		}
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "2 pending recommendations (1 configured, 1 unconfigured).") {
		t.Fatalf("output = %q, want queue summary to break out configured and unconfigured counts", got)
	}
}

func TestListCommandExplainsDatabaseLockErrors(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalOpenDB := openDB
	t.Cleanup(func() {
		newPaths = originalNewPaths
		openDB = originalOpenDB
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	openDB = func(string) (*db.DB, error) {
		return nil, errors.New("migrate db: database is locked (261)")
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempRoot, "ezoss.db"), []byte("seed"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"list"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want database lock guidance")
	}
	msg := err.Error()
	for _, want := range []string{"database is locked", "another ezoss process may be using the database", "try again in a moment"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
	if strings.Contains(msg, "(261)") {
		t.Fatalf("error = %q, should hide raw sqlite error code", msg)
	}
}

func TestListCommandOmitsOrphanNoteWhenAllReposAreConfigured(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:     "kunchenguid/ezoss#42",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "seed",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceMedium,
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); strings.Contains(got, "not in your config") {
		t.Fatalf("output = %q, unexpected orphan warning", got)
	}
}

func TestListCommandHintsWhenAllRecsAreForUnconfiguredRepos(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "orphan/only", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:     "orphan/only#1",
		RepoID: "orphan/only",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 1,
		Title:  "seed",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "orphan/only#1",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceMedium,
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	wantFragments := []string{
		"note:",
		"1 recommendation is for a repo not in your config",
		"orphan/only",
		"ezoss init --repo orphan/only",
	}
	for _, frag := range wantFragments {
		if !strings.Contains(got, frag) {
			t.Fatalf("output = %q, missing fragment %q", got, frag)
		}
	}
}

func TestListCommandRetriesTransientDatabaseLock(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalOpenDB := openDB
	t.Cleanup(func() {
		newPaths = originalNewPaths
		openDB = originalOpenDB
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempRoot, "ezoss.db"), []byte("seed"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "retry.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	var attempts int32
	openDB = func(string) (*db.DB, error) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			return nil, errors.New("migrate db: database is locked (261)")
		}
		return database, nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("open attempts = %d, want 2", got)
	}
	if got := buf.String(); got != "no pending recommendations\n" {
		t.Fatalf("output = %q, want %q", got, "no pending recommendations\n")
	}
}

func TestListCommandTracksTelemetryEvent(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	telemetrySink := &telemetrySinkStub{}
	resetTelemetry := telemetry.SetDefaultForTesting(telemetrySink)
	t.Cleanup(func() {
		newPaths = originalNewPaths
		resetTelemetry()
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(telemetrySink.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(telemetrySink.events))
	}
	if telemetrySink.events[0].Name != "command" {
		t.Fatalf("telemetry event name = %q, want %q", telemetrySink.events[0].Name, "command")
	}
	if telemetrySink.events[0].Fields["command"] != "list" {
		t.Fatalf("telemetry command = %v, want %q", telemetrySink.events[0].Fields["command"], "list")
	}
	if telemetrySink.events[0].Fields["entrypoint"] != "list" {
		t.Fatalf("telemetry entrypoint = %v, want %q", telemetrySink.events[0].Fields["entrypoint"], "list")
	}
}
