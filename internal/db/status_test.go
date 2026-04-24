package db

import (
	"testing"
	"time"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func TestCountActiveRecommendations(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(Item{
		ID:     "kunchenguid/ezoss#42",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "Bug: triage queue stalls",
		Author: "octocat",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(singleOptionRec("kunchenguid/ezoss#42", sharedtypes.AgentClaude, NewRecommendationOption{
		StateChange: sharedtypes.StateChangeNone,
		Confidence:  sharedtypes.ConfidenceMedium,
	}))
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	count, err := database.CountActiveRecommendations()
	if err != nil {
		t.Fatalf("CountActiveRecommendations() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("CountActiveRecommendations() = %d, want 1", count)
	}

	if err := database.MarkRecommendationSuperseded(rec.ID, time.Unix(1713000000, 0)); err != nil {
		t.Fatalf("MarkRecommendationSuperseded() error = %v", err)
	}

	count, err = database.CountActiveRecommendations()
	if err != nil {
		t.Fatalf("CountActiveRecommendations() after supersede error = %v", err)
	}
	if count != 0 {
		t.Fatalf("CountActiveRecommendations() after supersede = %d, want 0", count)
	}
}

func TestRecommendationTokenTotalsForItem(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(Item{
		ID:     "kunchenguid/ezoss#42",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "Bug: triage queue stalls",
		Author: "octocat",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if err := database.UpsertItem(Item{
		ID:     "kunchenguid/ezoss#43",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 43,
		Title:  "Question: how does polling work?",
		Author: "octocat",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}

	for _, rec := range []NewRecommendation{
		{
			ItemID:    "kunchenguid/ezoss#42",
			Agent:     sharedtypes.AgentClaude,
			TokensIn:  1200,
			TokensOut: 150,
			Options:   []NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceMedium}},
		},
		{
			ItemID:    "kunchenguid/ezoss#42",
			Agent:     sharedtypes.AgentClaude,
			TokensIn:  300,
			TokensOut: 20,
			Options:   []NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceHigh}},
		},
		{
			ItemID:    "kunchenguid/ezoss#43",
			Agent:     sharedtypes.AgentClaude,
			TokensIn:  999,
			TokensOut: 40,
			Options:   []NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceLow}},
		},
	} {
		if _, err := database.InsertRecommendation(rec); err != nil {
			t.Fatalf("InsertRecommendation() error = %v", err)
		}
	}

	totals, err := database.RecommendationTokenTotalsForItem("kunchenguid/ezoss#42")
	if err != nil {
		t.Fatalf("RecommendationTokenTotalsForItem() error = %v", err)
	}
	if totals.TokensIn != 1500 || totals.TokensOut != 170 {
		t.Fatalf("RecommendationTokenTotalsForItem() = %+v, want in=1500 out=170", totals)
	}
}
