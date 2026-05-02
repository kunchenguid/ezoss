package mock

import (
	"context"
	"fmt"
	"time"

	"github.com/kunchenguid/ezoss/internal/ghclient"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

type Client struct{}

func New() *Client {
	return &Client{}
}

func (c *Client) ListNeedingTriage(_ context.Context, repo string) ([]ghclient.Item, error) {
	now := time.Date(2026, time.April, 19, 12, 0, 0, 0, time.UTC)
	return []ghclient.Item{
		{
			Repo:      repo,
			Kind:      sharedtypes.ItemKindIssue,
			Number:    42,
			Title:     "panic in sync loop",
			Body:      "The daemon crashes after a repo poll.",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			Labels:    []string{"bug"},
			URL:       fmt.Sprintf("https://github.com/%s/issues/42", repo),
			UpdatedAt: now.Add(-2 * time.Hour),
		},
		{
			Repo:      repo,
			Kind:      sharedtypes.ItemKindPR,
			Number:    7,
			Title:     "feat: add streaming to mock agent",
			Body:      "This adds event streaming for the mock integration.",
			Author:    "carol",
			State:     sharedtypes.ItemStateOpen,
			Labels:    []string{"enhancement"},
			URL:       fmt.Sprintf("https://github.com/%s/pull/7", repo),
			UpdatedAt: now.Add(-5 * time.Hour),
		},
		{
			Repo:      repo,
			Kind:      sharedtypes.ItemKindIssue,
			Number:    88,
			Title:     "follow-up needed on contributor repro",
			Body:      "Contributor has not replied with the requested logs in 32 days.",
			Author:    "dave",
			State:     sharedtypes.ItemStateOpen,
			Labels:    []string{"question"},
			URL:       fmt.Sprintf("https://github.com/%s/issues/88", repo),
			UpdatedAt: now.Add(-32 * 24 * time.Hour),
		},
	}, nil
}

func (c *Client) ListTriaged(_ context.Context, repo string, _ time.Time) ([]ghclient.Item, error) {
	now := time.Date(2026, time.April, 19, 12, 0, 0, 0, time.UTC)
	return []ghclient.Item{
		{
			Repo:      repo,
			Kind:      sharedtypes.ItemKindIssue,
			Number:    99,
			Title:     "waiting on contributor follow-up",
			Body:      "Contributor was asked for more detail and has not replied yet.",
			Author:    "erin",
			State:     sharedtypes.ItemStateOpen,
			Labels:    []string{"ezoss/triaged", "ezoss/awaiting-contributor"},
			URL:       fmt.Sprintf("https://github.com/%s/issues/99", repo),
			UpdatedAt: now.Add(-6 * time.Hour),
		},
	}, nil
}

func (c *Client) SearchAuthoredOpenPRs(_ context.Context) ([]ghclient.Item, error) {
	now := time.Date(2026, time.April, 19, 12, 0, 0, 0, time.UTC)
	return []ghclient.Item{
		{
			Repo:         "upstream/widgets",
			Kind:         sharedtypes.ItemKindPR,
			Number:       321,
			Title:        "fix race in cache",
			Body:         "Fixes the cache race condition you flagged in #310.",
			Author:       "kun",
			State:        sharedtypes.ItemStateOpen,
			Labels:       []string{},
			URL:          "https://github.com/upstream/widgets/pull/321",
			UpdatedAt:    now.Add(-3 * time.Hour),
			HeadRepo:     "kun/widgets",
			HeadRef:      "fix-cache-race",
			HeadCloneURL: "https://github.com/kun/widgets.git",
		},
	}, nil
}

func (c *Client) ListOwnedRepos(_ context.Context, _ ghclient.RepoVisibility) ([]string, error) {
	return nil, nil
}

func (c *Client) SearchAuthoredOpenIssues(_ context.Context) ([]ghclient.Item, error) {
	now := time.Date(2026, time.April, 19, 12, 0, 0, 0, time.UTC)
	return []ghclient.Item{
		{
			Repo:      "upstream/widgets",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    310,
			Title:     "cache race triggers panic",
			Body:      "Repros under load with two writers, see attached log.",
			Author:    "kun",
			State:     sharedtypes.ItemStateOpen,
			Labels:    []string{},
			URL:       "https://github.com/upstream/widgets/issues/310",
			UpdatedAt: now.Add(-12 * time.Hour),
		},
	}, nil
}

func (c *Client) GetItem(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int) (ghclient.Item, error) {
	items, err := c.ListNeedingTriage(ctx, repo)
	if err != nil {
		return ghclient.Item{}, err
	}
	for _, item := range items {
		if item.Kind == kind && item.Number == number {
			return item, nil
		}
	}
	return ghclient.Item{}, fmt.Errorf("mock item not found: %s %s#%d", kind, repo, number)
}
