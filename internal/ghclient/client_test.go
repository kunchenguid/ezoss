package ghclient

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func TestListNeedingTriageFiltersDraftAndWIPItems(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{
			{
				stdout: `[
					{"number": 42, "title": "panic in sync loop", "author": {"login": "alice"}, "state": "OPEN", "labels": [{"name": "bug"}], "updatedAt": "2026-04-19T10:00:00Z", "url": "https://github.com/acme/widgets/issues/42"},
					{"number": 43, "title": "WIP: investigate flakes", "author": {"login": "bob"}, "state": "OPEN", "labels": [], "updatedAt": "2026-04-19T11:00:00Z", "url": "https://github.com/acme/widgets/issues/43"}
				]`,
			},
			{
				stdout: `[
					{"number": 7, "title": "feat: ship it", "author": {"login": "carol"}, "state": "OPEN", "isDraft": false, "labels": [{"name": "enhancement"}], "updatedAt": "2026-04-19T12:00:00Z", "url": "https://github.com/acme/widgets/pull/7"},
					{"number": 8, "title": "ready eventually", "author": {"login": "dave"}, "state": "OPEN", "isDraft": true, "labels": [], "updatedAt": "2026-04-19T13:00:00Z", "url": "https://github.com/acme/widgets/pull/8"},
					{"number": 9, "title": "[draft] polish docs", "author": {"login": "erin"}, "state": "OPEN", "isDraft": false, "labels": [], "updatedAt": "2026-04-19T14:00:00Z", "url": "https://github.com/acme/widgets/pull/9"}
				]`,
			},
		},
	}

	client := New(runner)
	items, err := client.ListNeedingTriage(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatalf("ListNeedingTriage returned error: %v", err)
	}

	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 gh calls, got %d", len(runner.calls))
	}

	expected := []Item{
		{
			Repo:      "acme/widgets",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    42,
			Title:     "panic in sync loop",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			Labels:    []string{"bug"},
			URL:       "https://github.com/acme/widgets/issues/42",
			UpdatedAt: mustParseTime(t, "2026-04-19T10:00:00Z"),
		},
		{
			Repo:      "acme/widgets",
			Kind:      sharedtypes.ItemKindPR,
			Number:    7,
			Title:     "feat: ship it",
			Author:    "carol",
			State:     sharedtypes.ItemStateOpen,
			Labels:    []string{"enhancement"},
			URL:       "https://github.com/acme/widgets/pull/7",
			UpdatedAt: mustParseTime(t, "2026-04-19T12:00:00Z"),
		},
	}

	if !reflect.DeepEqual(items, expected) {
		t.Fatalf("items mismatch\n got: %#v\nwant: %#v", items, expected)
	}
	if runner.calls[0].args[0] != "issue" || runner.calls[1].args[0] != "pr" {
		t.Fatalf("unexpected command order: %#v", runner.calls)
	}
	if !containsArg(runner.calls[0].args, "-label:ezoss/triaged") {
		t.Fatalf("issue list command missing search filter: %#v", runner.calls[0].args)
	}
}

func TestListTriagedFiltersDraftAndWIPItems(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{
			{
				stdout: `[
					{"number": 52, "title": "follow-up needed", "author": {"login": "alice"}, "state": "OPEN", "labels": [{"name": "ezoss/triaged"}], "updatedAt": "2026-04-19T10:00:00Z", "url": "https://github.com/acme/widgets/issues/52"},
					{"number": 53, "title": "[WIP] hold off", "author": {"login": "bob"}, "state": "OPEN", "labels": [{"name": "ezoss/triaged"}], "updatedAt": "2026-04-19T11:00:00Z", "url": "https://github.com/acme/widgets/issues/53"}
				]`,
			},
			{
				stdout: `[
					{"number": 14, "title": "ready for maintainer", "author": {"login": "carol"}, "state": "OPEN", "isDraft": false, "labels": [{"name": "ezoss/triaged"}], "updatedAt": "2026-04-19T12:00:00Z", "url": "https://github.com/acme/widgets/pull/14"},
					{"number": 15, "title": "still drafting", "author": {"login": "dave"}, "state": "OPEN", "isDraft": true, "labels": [{"name": "ezoss/triaged"}], "updatedAt": "2026-04-19T13:00:00Z", "url": "https://github.com/acme/widgets/pull/15"}
				]`,
			},
		},
	}

	client := New(runner)
	items, err := client.ListTriaged(context.Background(), "acme/widgets", time.Time{})
	if err != nil {
		t.Fatalf("ListTriaged returned error: %v", err)
	}

	expected := []Item{
		{
			Repo:      "acme/widgets",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    52,
			Title:     "follow-up needed",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			Labels:    []string{"ezoss/triaged"},
			URL:       "https://github.com/acme/widgets/issues/52",
			UpdatedAt: mustParseTime(t, "2026-04-19T10:00:00Z"),
		},
		{
			Repo:      "acme/widgets",
			Kind:      sharedtypes.ItemKindPR,
			Number:    14,
			Title:     "ready for maintainer",
			Author:    "carol",
			State:     sharedtypes.ItemStateOpen,
			Labels:    []string{"ezoss/triaged"},
			URL:       "https://github.com/acme/widgets/pull/14",
			UpdatedAt: mustParseTime(t, "2026-04-19T12:00:00Z"),
		},
	}

	if !reflect.DeepEqual(items, expected) {
		t.Fatalf("items mismatch\n got: %#v\nwant: %#v", items, expected)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 gh calls, got %d", len(runner.calls))
	}
	if !containsArg(runner.calls[0].args, "label:ezoss/triaged") {
		t.Fatalf("issue list command missing triaged search filter: %#v", runner.calls[0].args)
	}
	if got := flagValue(runner.calls[0].args, "--state"); got != "open" {
		t.Fatalf("zero-since ListTriaged --state = %q, want %q", got, "open")
	}
}

// TestListTriagedWithSinceUpdatedUsesDeltaSearch verifies the delta-bounded
// query: when a non-zero since timestamp is passed, the search filter
// includes updated:>=<rfc3339> and --state is "all" so closed items that
// changed recently get reconciled. Without this, items closed outside ezoss
// (or via the tool, before fix #1 propagates) leave local state stuck at
// "open" indefinitely.
func TestListTriagedWithSinceUpdatedUsesDeltaSearch(t *testing.T) {
	t.Parallel()

	since := mustParseTime(t, "2026-04-25T15:30:00Z")
	runner := &stubRunner{
		responses: []stubResponse{
			{stdout: `[]`},
			{stdout: `[]`},
		},
	}

	client := New(runner)
	if _, err := client.ListTriaged(context.Background(), "acme/widgets", since); err != nil {
		t.Fatalf("ListTriaged returned error: %v", err)
	}

	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 gh calls, got %d", len(runner.calls))
	}
	for i, call := range runner.calls {
		if got := flagValue(call.args, "--state"); got != "all" {
			t.Fatalf("call[%d] --state = %q, want %q", i, got, "all")
		}
		search := flagValue(call.args, "--search")
		if !strings.Contains(search, "label:ezoss/triaged") {
			t.Fatalf("call[%d] --search = %q, want it to contain triaged label filter", i, search)
		}
		wantUpdated := "updated:>=" + since.UTC().Format(time.RFC3339)
		if !strings.Contains(search, wantUpdated) {
			t.Fatalf("call[%d] --search = %q, want it to contain %q", i, search, wantUpdated)
		}
	}
}

func TestGetItemReturnsIssueBodyAndLabels(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{{
			stdout: `{"number": 21, "title": "panic in parser", "body": "stacktrace", "author": {"login": "zoe"}, "state": "CLOSED", "labels": [{"name": "bug"}, {"name": "needs-repro"}], "updatedAt": "2026-04-18T08:30:00Z", "url": "https://github.com/acme/widgets/issues/21"}`,
		}},
	}

	client := New(runner)
	item, err := client.GetItem(context.Background(), "acme/widgets", sharedtypes.ItemKindIssue, 21)
	if err != nil {
		t.Fatalf("GetItem returned error: %v", err)
	}

	expected := Item{
		Repo:      "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    21,
		Title:     "panic in parser",
		Body:      "stacktrace",
		Author:    "zoe",
		State:     sharedtypes.ItemStateClosed,
		Labels:    []string{"bug", "needs-repro"},
		URL:       "https://github.com/acme/widgets/issues/21",
		UpdatedAt: mustParseTime(t, "2026-04-18T08:30:00Z"),
	}

	if !reflect.DeepEqual(item, expected) {
		t.Fatalf("item mismatch\n got: %#v\nwant: %#v", item, expected)
	}
	if len(runner.calls) != 1 || runner.calls[0].args[0] != "issue" || runner.calls[0].args[1] != "view" {
		t.Fatalf("unexpected gh call: %#v", runner.calls)
	}
}

func TestGetItemOmitsIsDraftFieldForIssues(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{{
			stdout: `{"number": 21, "title": "panic", "body": "", "author": {"login": "zoe"}, "state": "OPEN", "labels": [], "updatedAt": "2026-04-18T08:30:00Z", "url": "https://github.com/acme/widgets/issues/21"}`,
		}},
	}

	client := New(runner)
	if _, err := client.GetItem(context.Background(), "acme/widgets", sharedtypes.ItemKindIssue, 21); err != nil {
		t.Fatalf("GetItem returned error: %v", err)
	}

	jsonArg := jsonFieldsArg(t, runner.calls[0].args)
	if containsJSONField(jsonArg, "isDraft") {
		t.Fatalf("issue view --json should not request isDraft, got %q", jsonArg)
	}
}

func TestGetItemKeepsIsDraftFieldForPRs(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{{
			stdout: `{"number": 7, "title": "feat", "body": "", "author": {"login": "zoe"}, "state": "OPEN", "isDraft": false, "labels": [], "updatedAt": "2026-04-18T08:30:00Z", "url": "https://github.com/acme/widgets/pull/7"}`,
		}},
	}

	client := New(runner)
	if _, err := client.GetItem(context.Background(), "acme/widgets", sharedtypes.ItemKindPR, 7); err != nil {
		t.Fatalf("GetItem returned error: %v", err)
	}

	jsonArg := jsonFieldsArg(t, runner.calls[0].args)
	if !containsJSONField(jsonArg, "isDraft") {
		t.Fatalf("pr view --json should request isDraft, got %q", jsonArg)
	}
}

func TestListNeedingTriageOmitsIsDraftFieldForIssues(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{
			{stdout: `[]`},
			{stdout: `[]`},
		},
	}

	client := New(runner)
	if _, err := client.ListNeedingTriage(context.Background(), "acme/widgets"); err != nil {
		t.Fatalf("ListNeedingTriage returned error: %v", err)
	}

	issueJSON := jsonFieldsArg(t, runner.calls[0].args)
	if containsJSONField(issueJSON, "isDraft") {
		t.Fatalf("issue list --json should not request isDraft, got %q", issueJSON)
	}
	prJSON := jsonFieldsArg(t, runner.calls[1].args)
	if !containsJSONField(prJSON, "isDraft") {
		t.Fatalf("pr list --json should request isDraft, got %q", prJSON)
	}
}

func TestGetItemReturnsRunnerError(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{{err: errors.New("boom")}},
	}

	client := New(runner)
	_, err := client.GetItem(context.Background(), "acme/widgets", sharedtypes.ItemKindPR, 5)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "gh pr view acme/widgets#5: boom" {
		t.Fatalf("unexpected error %q", got)
	}
}

func TestListNeedingTriageReturnsRateLimitErrorWithRetryAfter(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{{err: errors.New("secondary rate limit: retry after 1m30s")}},
	}

	client := New(runner)
	_, err := client.ListNeedingTriage(context.Background(), "acme/widgets")
	if err == nil {
		t.Fatal("expected error")
	}

	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		t.Fatalf("expected RateLimitError, got %T: %v", err, err)
	}
	if rateLimitErr.RetryAfter != 90*time.Second {
		t.Fatalf("RetryAfter = %v, want 90s", rateLimitErr.RetryAfter)
	}
}

func TestGetItemReturnsRateLimitErrorWithoutRetryAfterWhenUnavailable(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{{err: errors.New("GraphQL: API rate limit exceeded for user ID 123. Please wait a few minutes before you try again.")}},
	}

	client := New(runner)
	_, err := client.GetItem(context.Background(), "acme/widgets", sharedtypes.ItemKindPR, 5)
	if err == nil {
		t.Fatal("expected error")
	}

	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		t.Fatalf("expected RateLimitError, got %T: %v", err, err)
	}
	if rateLimitErr.RetryAfter != 0 {
		t.Fatalf("RetryAfter = %v, want 0", rateLimitErr.RetryAfter)
	}
}

func TestListTriagedReturnsRateLimitErrorWithWordRetryAfter(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{{err: errors.New("secondary rate limit: retry after 2 minutes")}},
	}

	client := New(runner)
	_, err := client.ListTriaged(context.Background(), "acme/widgets", time.Time{})
	if err == nil {
		t.Fatal("expected error")
	}

	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		t.Fatalf("expected RateLimitError, got %T: %v", err, err)
	}
	if rateLimitErr.RetryAfter != 2*time.Minute {
		t.Fatalf("RetryAfter = %v, want 2m", rateLimitErr.RetryAfter)
	}

}

func TestParseRetryAfterSupportsWordDurations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    time.Duration
	}{
		{name: "seconds", message: "secondary rate limit: retry after 45 seconds", want: 45 * time.Second},
		{name: "minutes", message: "secondary rate limit: retry after 2 minutes", want: 2 * time.Minute},
		{name: "hours", message: "secondary rate limit: retry after 3 hours", want: 3 * time.Hour},
		{name: "mixed", message: "secondary rate limit: retry after 1 minute 30 seconds", want: 90 * time.Second},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parseRetryAfter(tt.message); got != tt.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

func TestEditLabelsUpdatesGitHubLabels(t *testing.T) {
	t.Parallel()

	// Adds go in a single call; each remove gets its own call so that a
	// single missing label doesn't fail the whole batch (see
	// TestEditLabelsTreatsLabelNotFoundOnRemoveAsSuccess).
	runner := &stubRunner{responses: []stubResponse{{}, {}}}

	client := New(runner)
	err := client.EditLabels(
		context.Background(),
		"acme/widgets",
		sharedtypes.ItemKindIssue,
		42,
		[]string{"ezoss/triaged", "bug"},
		[]string{"ezoss/awaiting-contributor"},
	)
	if err != nil {
		t.Fatalf("EditLabels returned error: %v", err)
	}

	wantCalls := [][]string{
		{"issue", "edit", "42", "--repo", "acme/widgets", "--add-label", "ezoss/triaged,bug"},
		{"issue", "edit", "42", "--repo", "acme/widgets", "--remove-label", "ezoss/awaiting-contributor"},
	}
	if len(runner.calls) != len(wantCalls) {
		t.Fatalf("expected %d gh calls, got %d (%#v)", len(wantCalls), len(runner.calls), runner.calls)
	}
	for i, want := range wantCalls {
		if !reflect.DeepEqual(runner.calls[i].args, want) {
			t.Fatalf("call[%d] args mismatch\n got: %#v\nwant: %#v", i, runner.calls[i].args, want)
		}
	}
}

// TestEditLabelsAutoCreatesMissingEzossManagedLabel pins the regression
// where the first approve/skip on a freshly configured repo failed with
// "'ezoss/triaged' not found" because the managed label hadn't been
// created yet. ezoss owns the ezoss/* namespace per the design intent
// ("ezoss/triaged is always managed automatically"), so the client
// auto-creates managed labels on demand and retries the edit.
func TestEditLabelsAutoCreatesMissingEzossManagedLabel(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{err: errors.New("could not add label: 'ezoss/triaged' not found")},
		{},
		{},
	}}

	client := New(runner)
	err := client.EditLabels(
		context.Background(),
		"kunchenguid/axi",
		sharedtypes.ItemKindPR,
		9,
		[]string{"ezoss/triaged"},
		nil,
	)
	if err != nil {
		t.Fatalf("EditLabels should auto-create missing ezoss/* labels, got: %v", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("expected 3 calls (failed add, label create, retried add), got %d (%#v)", len(runner.calls), runner.calls)
	}
	wantCreate := []string{"label", "create", "ezoss/triaged", "--repo", "kunchenguid/axi", "--description", "Managed by ezoss"}
	if !reflect.DeepEqual(runner.calls[1].args, wantCreate) {
		t.Fatalf("create-label args mismatch\n got: %#v\nwant: %#v", runner.calls[1].args, wantCreate)
	}
	wantRetry := []string{"pr", "edit", "9", "--repo", "kunchenguid/axi", "--add-label", "ezoss/triaged"}
	if !reflect.DeepEqual(runner.calls[2].args, wantRetry) {
		t.Fatalf("retry args mismatch\n got: %#v\nwant: %#v", runner.calls[2].args, wantRetry)
	}
}

// TestEditLabelsAutoCreatesMultipleMissingEzossLabels verifies the loop
// handles the case where several ezoss/* labels are missing at once - each
// retry creates the next reported missing label until the bulk add lands.
func TestEditLabelsAutoCreatesMultipleMissingEzossLabels(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{err: errors.New("could not add label: 'ezoss/triaged' not found")},
		{}, // create ezoss/triaged
		{err: errors.New("could not add label: 'ezoss/awaiting-maintainer' not found")},
		{}, // create ezoss/awaiting-maintainer
		{}, // retry succeeds
	}}

	client := New(runner)
	err := client.EditLabels(
		context.Background(),
		"kunchenguid/axi",
		sharedtypes.ItemKindPR,
		9,
		[]string{"ezoss/triaged", "ezoss/awaiting-maintainer"},
		nil,
	)
	if err != nil {
		t.Fatalf("EditLabels failed: %v", err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 calls, got %d (%#v)", len(runner.calls), runner.calls)
	}
}

// TestEditLabelsDoesNotAutoCreateUserNamespacedLabels ensures we only
// auto-create labels in the ezoss/* namespace; if the agent proposes a
// user-managed label like "bug" that doesn't exist in the repo, the user
// should see the error and add the label themselves rather than have
// ezoss silently create labels in their namespace.
func TestEditLabelsDoesNotAutoCreateUserNamespacedLabels(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{err: errors.New("could not add label: 'bug' not found")},
	}}

	client := New(runner)
	err := client.EditLabels(
		context.Background(),
		"acme/widgets",
		sharedtypes.ItemKindIssue,
		42,
		[]string{"bug"},
		nil,
	)
	if err == nil {
		t.Fatalf("expected error when user-namespaced label is missing")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call (no auto-create attempt), got %d (%#v)", len(runner.calls), runner.calls)
	}
}

// TestEditLabelsTreatsLabelNotFoundOnRemoveAsSuccess pins the regression
// where approving a PR/issue would fail post-action with
// "'ezoss/awaiting-maintainer' not found" because the managed waiting-on
// label wasn't actually on the item (or in the repo). The desired end
// state - label absent - is already true, so the remove is a no-op.
func TestEditLabelsTreatsLabelNotFoundOnRemoveAsSuccess(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{},
		{err: errors.New("could not remove label: 'ezoss/awaiting-maintainer' not found")},
		{},
	}}

	client := New(runner)
	err := client.EditLabels(
		context.Background(),
		"kunchenguid/axi",
		sharedtypes.ItemKindPR,
		9,
		nil,
		[]string{"ezoss/awaiting-contributor", "ezoss/awaiting-maintainer", "ezoss/stale"},
	)
	if err != nil {
		t.Fatalf("EditLabels should swallow label-not-found on remove, got error: %v", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("expected one call per remove label (3 calls), got %d (%#v)", len(runner.calls), runner.calls)
	}
}

// TestEditLabelsPropagatesNonLabelErrorsOnRemove ensures we only swallow
// the specific "label not found" error - other failures (auth, repo-not-
// found, network) still surface so the user can fix them.
func TestEditLabelsPropagatesNonLabelErrorsOnRemove(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{err: errors.New("HTTP 403: forbidden")},
	}}

	client := New(runner)
	err := client.EditLabels(
		context.Background(),
		"acme/widgets",
		sharedtypes.ItemKindIssue,
		42,
		nil,
		[]string{"ezoss/awaiting-contributor"},
	)
	if err == nil {
		t.Fatalf("EditLabels should propagate non-label errors")
	}
}

func TestEditLabelsSkipsRunnerWhenNoLabelChanges(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{}

	client := New(runner)
	err := client.EditLabels(context.Background(), "acme/widgets", sharedtypes.ItemKindIssue, 42, nil, nil)
	if err != nil {
		t.Fatalf("EditLabels returned error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no gh calls, got %d", len(runner.calls))
	}
}

func TestCommentPostsDraftResponse(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{{}}}

	client := New(runner)
	err := client.Comment(context.Background(), "acme/widgets", sharedtypes.ItemKindPR, 7, "Thanks for the PR")
	if err != nil {
		t.Fatalf("Comment returned error: %v", err)
	}

	want := []string{"pr", "comment", "7", "--repo", "acme/widgets", "--body", "Thanks for the PR"}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("unexpected gh call: %#v", runner.calls)
	}
}

func TestCloseClosesItemWithComment(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{{}}}

	client := New(runner)
	err := client.Close(context.Background(), "acme/widgets", sharedtypes.ItemKindIssue, 21, "Closing as stale")
	if err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	want := []string{"issue", "close", "21", "--repo", "acme/widgets", "--comment", "Closing as stale"}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("unexpected gh call: %#v", runner.calls)
	}
}

func TestRequestChangesSubmitsPRReview(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{{}}}

	client := New(runner)
	err := client.RequestChanges(context.Background(), "acme/widgets", 9, "Please add tests")
	if err != nil {
		t.Fatalf("RequestChanges returned error: %v", err)
	}

	want := []string{"pr", "review", "9", "--repo", "acme/widgets", "--request-changes", "--body", "Please add tests"}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("unexpected gh call: %#v", runner.calls)
	}
}

func TestMergeMergesPRWithConfiguredMethod(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{stdout: `{"allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":true}`},
		{},
	}}

	client := New(runner)
	used, err := client.Merge(context.Background(), "acme/widgets", 9, "squash")
	if err != nil {
		t.Fatalf("Merge returned error: %v", err)
	}
	if used != "squash" {
		t.Fatalf("Merge used = %q, want squash", used)
	}

	wantAPI := []string{"api", "repos/acme/widgets"}
	wantMerge := []string{"pr", "merge", "9", "--repo", "acme/widgets", "--squash"}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls (api + merge), got %d (%#v)", len(runner.calls), runner.calls)
	}
	if !reflect.DeepEqual(runner.calls[0].args, wantAPI) {
		t.Fatalf("api call args = %#v, want %#v", runner.calls[0].args, wantAPI)
	}
	if !reflect.DeepEqual(runner.calls[1].args, wantMerge) {
		t.Fatalf("merge call args = %#v, want %#v", runner.calls[1].args, wantMerge)
	}
}

func TestMergeDefaultsToMergeMethodWhenUnset(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{stdout: `{"allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":true}`},
		{},
	}}

	client := New(runner)
	used, err := client.Merge(context.Background(), "acme/widgets", 9, "")
	if err != nil {
		t.Fatalf("Merge returned error: %v", err)
	}
	if used != "merge" {
		t.Fatalf("used = %q, want merge (the default when nothing is requested and merge is allowed)", used)
	}

	wantMerge := []string{"pr", "merge", "9", "--repo", "acme/widgets", "--merge"}
	if !reflect.DeepEqual(runner.calls[1].args, wantMerge) {
		t.Fatalf("merge call args = %#v, want %#v", runner.calls[1].args, wantMerge)
	}
}

// TestMergeFallsBackWhenRequestedMethodNotAllowed pins the regression where
// approving a merge with the global default method ("merge") would fail
// because the repo only allowed squash. The Merge call now queries the
// repo's allowed methods first and falls back through squash > rebase >
// merge when the requested one isn't available.
func TestMergeFallsBackWhenRequestedMethodNotAllowed(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{stdout: `{"allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":false}`},
		{}, // squash succeeds
	}}

	client := New(runner)
	used, err := client.Merge(context.Background(), "kunchenguid/gnhf", 96, "merge")
	if err != nil {
		t.Fatalf("Merge should fall back, got error: %v", err)
	}
	if used != "squash" {
		t.Fatalf("used = %q, want squash (the only allowed method)", used)
	}

	wantMerge := []string{"pr", "merge", "96", "--repo", "kunchenguid/gnhf", "--squash"}
	if !reflect.DeepEqual(runner.calls[1].args, wantMerge) {
		t.Fatalf("merge call args = %#v, want %#v", runner.calls[1].args, wantMerge)
	}
}

// TestMergeFailsWhenNoMethodAllowed returns a clear error when the repo
// disables every merge method (an unusual but possible configuration).
func TestMergeFailsWhenNoMethodAllowed(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{stdout: `{"allow_merge_commit":false,"allow_squash_merge":false,"allow_rebase_merge":false}`},
	}}

	client := New(runner)
	_, err := client.Merge(context.Background(), "acme/widgets", 9, "merge")
	if err == nil {
		t.Fatal("expected error when no merge method is allowed")
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Fatalf("error should explain that no merge method is allowed, got: %v", err)
	}
}

// TestMergePrefersSquashWhenRequestedNotAllowedAndMultipleAvailable verifies
// the fallback order: squash > rebase > merge. When the user asked for
// rebase but the repo allows {merge, squash}, prefer squash.
func TestMergePrefersSquashWhenRequestedNotAllowedAndMultipleAvailable(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{
		{stdout: `{"allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":false}`},
		{},
	}}

	client := New(runner)
	used, err := client.Merge(context.Background(), "acme/widgets", 9, "rebase")
	if err != nil {
		t.Fatalf("Merge returned error: %v", err)
	}
	if used != "squash" {
		t.Fatalf("used = %q, want squash (preferred fallback over merge)", used)
	}
}

func TestListOwnedReposReturnsNameWithOwnerStrings(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{{
		stdout: `[{"nameWithOwner":"kunchenguid/ezoss"},{"nameWithOwner":"kunchenguid/no-mistakes"}]`,
	}}}

	client := New(runner)
	repos, err := client.ListOwnedRepos(context.Background(), RepoVisibilityAll)
	if err != nil {
		t.Fatalf("ListOwnedRepos returned error: %v", err)
	}
	want := []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"}
	if !reflect.DeepEqual(repos, want) {
		t.Fatalf("repos = %v, want %v", repos, want)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 gh call, got %d", len(runner.calls))
	}
	args := runner.calls[0].args
	if args[0] != "repo" || args[1] != "list" {
		t.Fatalf("unexpected command: %v", args)
	}
	if !containsArg(args, "--source") {
		t.Fatalf("missing --source flag: %v", args)
	}
	if !containsArg(args, "--no-archived") {
		t.Fatalf("missing --no-archived flag: %v", args)
	}
	if containsArg(args, "--visibility") {
		t.Fatalf("RepoVisibilityAll should not pass --visibility: %v", args)
	}
}

func TestListOwnedReposPassesVisibilityPublicFilter(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{{stdout: `[]`}}}

	client := New(runner)
	if _, err := client.ListOwnedRepos(context.Background(), RepoVisibilityPublic); err != nil {
		t.Fatalf("ListOwnedRepos returned error: %v", err)
	}

	args := runner.calls[0].args
	idx := -1
	for i, arg := range args {
		if arg == "--visibility" {
			idx = i
			break
		}
	}
	if idx < 0 || idx == len(args)-1 || args[idx+1] != "public" {
		t.Fatalf("expected --visibility public, got args %v", args)
	}
}

func TestListOwnedReposSkipsBlankEntries(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{{
		stdout: `[{"nameWithOwner":"   "},{"nameWithOwner":"kunchenguid/ezoss"},{"nameWithOwner":""}]`,
	}}}

	client := New(runner)
	repos, err := client.ListOwnedRepos(context.Background(), RepoVisibilityAll)
	if err != nil {
		t.Fatalf("ListOwnedRepos returned error: %v", err)
	}
	if !reflect.DeepEqual(repos, []string{"kunchenguid/ezoss"}) {
		t.Fatalf("repos = %v, want [kunchenguid/ezoss]", repos)
	}
}

func TestListStarredReposReturnsFullNamesAcrossPages(t *testing.T) {
	t.Parallel()

	// `gh api --paginate --jq '[...]'` emits one bracketed array per page
	// separated by a newline.
	runner := &stubRunner{responses: []stubResponse{{
		stdout: "[\"acme/widgets\",\"foo/bar\"]\n[\"baz/qux\",\"acme/widgets\"]\n",
	}}}

	client := New(runner)
	repos, err := client.ListStarredRepos(context.Background())
	if err != nil {
		t.Fatalf("ListStarredRepos returned error: %v", err)
	}
	want := []string{"acme/widgets", "foo/bar", "baz/qux"}
	if !reflect.DeepEqual(repos, want) {
		t.Fatalf("repos = %v, want %v (should dedupe across pages)", repos, want)
	}

	args := runner.calls[0].args
	if args[0] != "api" || args[1] != "user/starred" {
		t.Fatalf("unexpected command: %v", args)
	}
	if !containsArg(args, "--paginate") {
		t.Fatalf("missing --paginate flag: %v", args)
	}
	if !containsArg(args, "--jq") {
		t.Fatalf("missing --jq flag: %v", args)
	}
}

func TestListStarredReposPropagatesRunnerError(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{{err: errors.New("not authenticated")}}}

	client := New(runner)
	_, err := client.ListStarredRepos(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gh api user/starred") {
		t.Fatalf("expected gh api user/starred prefix, got %q", err.Error())
	}
}

func TestListOwnedReposPropagatesRunnerError(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{{err: errors.New("not authenticated")}}}

	client := New(runner)
	_, err := client.ListOwnedRepos(context.Background(), RepoVisibilityAll)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gh repo list") {
		t.Fatalf("expected gh repo list prefix, got %q", err.Error())
	}
}

func TestCommentReturnsRunnerError(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{responses: []stubResponse{{err: errors.New("boom")}}}

	client := New(runner)
	err := client.Comment(context.Background(), "acme/widgets", sharedtypes.ItemKindIssue, 5, "hello")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "gh issue comment acme/widgets#5: boom" {
		t.Fatalf("unexpected error %q", got)
	}
}

func TestSearchAuthoredOpenPRsExtractsHeadInfo(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{{
			stdout: `[
				{"number": 99, "title": "fix race in cache", "author": {"login": "kun"}, "state": "OPEN", "isDraft": false, "labels": [], "updatedAt": "2026-04-29T12:00:00Z", "url": "https://github.com/upstream/widgets/pull/99",
				 "repository": {"nameWithOwner": "upstream/widgets", "name": "widgets", "url": "https://github.com/upstream/widgets", "owner": {"login": "upstream"}}},
				{"number": 100, "title": "WIP: drafty", "author": {"login": "kun"}, "state": "OPEN", "isDraft": false, "labels": [], "updatedAt": "2026-04-29T13:00:00Z", "url": "https://github.com/upstream/widgets/pull/100",
				 "repository": {"nameWithOwner": "upstream/widgets"}},
				{"number": 101, "title": "draft, hold", "author": {"login": "kun"}, "state": "OPEN", "isDraft": true, "labels": [], "updatedAt": "2026-04-29T14:00:00Z", "url": "https://github.com/upstream/widgets/pull/101",
				 "repository": {"nameWithOwner": "upstream/widgets"}}
			]`,
		}, {
			stdout: `{"headRepository": {"nameWithOwner": "kun/widgets", "name": "widgets", "url": "https://github.com/kun/widgets", "owner": {"login": "kun"}}, "headRepositoryOwner": {"login": "kun"}, "headRefName": "fix-cache-race"}`,
		}},
	}

	client := New(runner)
	items, err := client.SearchAuthoredOpenPRs(context.Background())
	if err != nil {
		t.Fatalf("SearchAuthoredOpenPRs error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item after WIP/draft filter, got %d: %#v", len(items), items)
	}
	got := items[0]
	if got.Repo != "upstream/widgets" {
		t.Fatalf("Repo = %q, want upstream/widgets", got.Repo)
	}
	if got.Number != 99 {
		t.Fatalf("Number = %d, want 99", got.Number)
	}
	if got.HeadRepo != "kun/widgets" {
		t.Fatalf("HeadRepo = %q, want kun/widgets", got.HeadRepo)
	}
	if got.HeadRef != "fix-cache-race" {
		t.Fatalf("HeadRef = %q, want fix-cache-race", got.HeadRef)
	}
	if got.HeadCloneURL != "https://github.com/kun/widgets.git" {
		t.Fatalf("HeadCloneURL = %q, want https://github.com/kun/widgets.git", got.HeadCloneURL)
	}
	if !containsArg(runner.calls[0].args, "search") || !containsArg(runner.calls[0].args, "prs") {
		t.Fatalf("expected gh search prs invocation, got %#v", runner.calls[0].args)
	}
	if !containsArg(runner.calls[0].args, "@me") {
		t.Fatalf("expected --author @me, got %#v", runner.calls[0].args)
	}
	jsonFields := argValue(runner.calls[0].args, "--json")
	for _, unsupported := range []string{"headRepository", "headRepositoryOwner", "headRefName"} {
		if strings.Contains(jsonFields, unsupported) {
			t.Fatalf("search JSON fields include unsupported %q: %q", unsupported, jsonFields)
		}
	}
	if !containsArg(runner.calls[1].args, "pr") || !containsArg(runner.calls[1].args, "view") || !containsArg(runner.calls[1].args, "99") || !hasArgValue(runner.calls[1].args, "--repo", "upstream/widgets") {
		t.Fatalf("expected gh pr view for head metadata, got %#v", runner.calls[1].args)
	}
}

func TestSearchAuthoredOpenPRsTreatsLimitSizedResultsAsTruncated(t *testing.T) {
	t.Parallel()

	const limit = 1000
	var b strings.Builder
	b.WriteString("[")
	for i := 1; i <= limit; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		n := strconv.Itoa(i)
		b.WriteString(`{"number":` + n + `,"title":"ready","author":{"login":"kun"},"state":"OPEN","isDraft":false,"labels":[],"updatedAt":"2026-04-29T12:00:00Z","url":"https://github.com/upstream/widgets/pull/` + n + `","repository":{"nameWithOwner":"upstream/widgets"},"headRefName":"fix"}`)
	}
	b.WriteString("]")
	runner := &stubRunner{responses: []stubResponse{{stdout: b.String()}}}

	client := New(runner)
	_, err := client.SearchAuthoredOpenPRs(context.Background())
	if err == nil {
		t.Fatal("SearchAuthoredOpenPRs error = nil, want truncated result error")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("SearchAuthoredOpenPRs error = %q, want truncated", err.Error())
	}
	if !hasArgValue(runner.calls[0].args, "--limit", strconv.Itoa(limit)) {
		t.Fatalf("expected --limit %d, got %#v", limit, runner.calls[0].args)
	}
}

func TestSearchAuthoredOpenIssuesReturnsAuthoredIssues(t *testing.T) {
	t.Parallel()

	runner := &stubRunner{
		responses: []stubResponse{{
			stdout: `[
				{"number": 23, "title": "Track contributions", "author": {"login": "kun"}, "state": "OPEN", "labels": [], "updatedAt": "2026-04-29T15:00:00Z", "url": "https://github.com/kunchenguid/ezoss/issues/23",
				 "repository": {"nameWithOwner": "kunchenguid/ezoss"}}
			]`,
		}},
	}

	client := New(runner)
	items, err := client.SearchAuthoredOpenIssues(context.Background())
	if err != nil {
		t.Fatalf("SearchAuthoredOpenIssues error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Repo != "kunchenguid/ezoss" || items[0].Number != 23 {
		t.Fatalf("unexpected item %#v", items[0])
	}
	if items[0].Kind != sharedtypes.ItemKindIssue {
		t.Fatalf("Kind = %q, want issue", items[0].Kind)
	}
	if !containsArg(runner.calls[0].args, "search") || !containsArg(runner.calls[0].args, "issues") {
		t.Fatalf("expected gh search issues invocation, got %#v", runner.calls[0].args)
	}
}

type stubRunner struct {
	responses []stubResponse
	calls     []stubCall
	index     int
}

type stubResponse struct {
	stdout string
	err    error
}

type stubCall struct {
	args []string
}

func (s *stubRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	s.calls = append(s.calls, stubCall{args: append([]string(nil), args...)})
	if s.index >= len(s.responses) {
		return nil, errors.New("unexpected call")
	}
	response := s.responses[s.index]
	s.index++
	return []byte(response.stdout), response.err
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return parsed
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasArgValue(args []string, flag string, value string) bool {
	return argValue(args, flag) == value
}

func argValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func flagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func jsonFieldsArg(t *testing.T, args []string) string {
	t.Helper()
	for i, arg := range args {
		if arg == "--json" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("missing --json arg in %#v", args)
	return ""
}

func containsJSONField(raw string, want string) bool {
	for _, field := range strings.Split(raw, ",") {
		if field == want {
			return true
		}
	}
	return false
}
