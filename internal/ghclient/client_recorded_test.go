package ghclient

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

// Recorded fixtures live in testdata/ and were captured from real `gh`
// invocations against public repos owned by the project author. Free-form
// fields (titles, bodies, commit messages, issue/PR descriptions) have been
// scrubbed to placeholder strings; everything that affects schema (types,
// presence/absence of fields, sub-object shapes) is preserved.
//
// To re-record a fixture run the matching `gh` command from the
// internal/ghclient/testdata directory; see the comment on each test for the
// exact command. Re-scrub free-form fields with the jq snippets at the bottom
// of this file before committing.

const fixtureDir = "testdata"

// --- decode-regression tests ----------------------------------------------
//
// Each test feeds a recorded payload into the matching client method via the
// stubRunner used elsewhere in this package and asserts the decode step
// succeeds. These would have caught the actor.id-as-number bug that wedged
// the daemon for kunchenguid/gnhf#109, and they catch the same class of bug
// for every other endpoint ezoss decodes.

// Re-record: `gh pr view 28 --repo kunchenguid/ezoss --json number,title,body,author,state,isDraft,labels,updatedAt,url > testdata/pr_view.json`
func TestRecordedFixture_GetItem_PR(t *testing.T) {
	t.Parallel()
	payload := readFixture(t, "pr_view.json")
	runner := &stubRunner{responses: []stubResponse{{stdout: string(payload)}}}
	client := New(runner)
	if _, err := client.GetItem(context.Background(), "kunchenguid/ezoss", sharedtypes.ItemKindPR, 28); err != nil {
		t.Fatalf("GetItem: %v", err)
	}
}

// Re-record: `gh pr view 28 --repo kunchenguid/ezoss --json headRepository,headRepositoryOwner,headRefName > testdata/pr_view_headrefs.json`
func TestRecordedFixture_PopulatePRHeadRefs(t *testing.T) {
	t.Parallel()
	payload := readFixture(t, "pr_view_headrefs.json")
	runner := &stubRunner{responses: []stubResponse{{stdout: string(payload)}}}
	client := New(runner)
	item := &Item{Repo: "kunchenguid/ezoss", Number: 28}
	if err := client.populatePRHeadRefs(context.Background(), item); err != nil {
		t.Fatalf("populatePRHeadRefs: %v", err)
	}
}

// Re-record: `gh pr list --repo kunchenguid/ezoss --state all --limit 5 --json number,title,body,author,state,isDraft,labels,updatedAt,url > testdata/pr_list.json`
func TestRecordedFixture_ListPRs(t *testing.T) {
	t.Parallel()
	payload := readFixture(t, "pr_list.json")
	runner := &stubRunner{responses: []stubResponse{{stdout: string(payload)}}}
	client := New(runner)
	if _, err := client.listItems(context.Background(), "kunchenguid/ezoss", sharedtypes.ItemKindPR, "", "all"); err != nil {
		t.Fatalf("listItems PR: %v", err)
	}
}

// Re-record: `gh issue list --repo kunchenguid/ezoss --state all --limit 5 --json number,title,body,author,state,labels,updatedAt,url > testdata/issue_list.json`
func TestRecordedFixture_ListIssues(t *testing.T) {
	t.Parallel()
	payload := readFixture(t, "issue_list.json")
	runner := &stubRunner{responses: []stubResponse{{stdout: string(payload)}}}
	client := New(runner)
	if _, err := client.listItems(context.Background(), "kunchenguid/ezoss", sharedtypes.ItemKindIssue, "", "all"); err != nil {
		t.Fatalf("listItems issue: %v", err)
	}
}

// Re-record: `gh search prs --author=@me --state=open --limit=10 --json number,title,body,author,state,isDraft,labels,updatedAt,url,repository > testdata/search_prs.json`
//
// SearchAuthoredOpenPRs makes one extra `pr view` call per result to populate
// head refs. The stub feeds the same head-refs fixture for each.
func TestRecordedFixture_SearchAuthoredOpenPRs(t *testing.T) {
	t.Parallel()
	search := readFixture(t, "search_prs.json")
	headrefs := readFixture(t, "pr_view_headrefs.json")
	var entries []json.RawMessage
	if err := json.Unmarshal(search, &entries); err != nil {
		t.Fatalf("count search entries: %v", err)
	}
	responses := make([]stubResponse, 0, 1+len(entries))
	responses = append(responses, stubResponse{stdout: string(search)})
	for range entries {
		responses = append(responses, stubResponse{stdout: string(headrefs)})
	}
	runner := &stubRunner{responses: responses}
	client := New(runner)
	if _, err := client.SearchAuthoredOpenPRs(context.Background()); err != nil {
		t.Fatalf("SearchAuthoredOpenPRs: %v", err)
	}
}

// Re-record: `gh search issues --author=@me --state=open --limit=10 --json number,title,body,author,state,labels,updatedAt,url,repository > testdata/search_issues.json`
func TestRecordedFixture_SearchAuthoredOpenIssues(t *testing.T) {
	t.Parallel()
	payload := readFixture(t, "search_issues.json")
	runner := &stubRunner{responses: []stubResponse{{stdout: string(payload)}}}
	client := New(runner)
	if _, err := client.SearchAuthoredOpenIssues(context.Background()); err != nil {
		t.Fatalf("SearchAuthoredOpenIssues: %v", err)
	}
}

// Re-record: `gh api repos/kunchenguid/ezoss > testdata/api_repos_repo.json`
func TestRecordedFixture_RepoMergeOptions(t *testing.T) {
	t.Parallel()
	payload := readFixture(t, "api_repos_repo.json")
	runner := &stubRunner{responses: []stubResponse{{stdout: string(payload)}}}
	client := New(runner)
	if _, err := client.RepoMergeOptions(context.Background(), "kunchenguid/ezoss"); err != nil {
		t.Fatalf("RepoMergeOptions: %v", err)
	}
}

// Re-record: `gh api user/repos --method GET --paginate -f affiliation=owner -f per_page=100 -f visibility=public --jq '[.[] | select(.fork == false and .archived == false) | .full_name]' > testdata/api_user_repos.json`
func TestRecordedFixture_ListOwnedRepos(t *testing.T) {
	t.Parallel()
	payload := readFixture(t, "api_user_repos.json")
	runner := &stubRunner{responses: []stubResponse{{stdout: string(payload)}}}
	client := New(runner)
	if _, err := client.ListOwnedRepos(context.Background(), RepoVisibilityPublic); err != nil {
		t.Fatalf("ListOwnedRepos: %v", err)
	}
}

// Re-record: `gh api user/starred --paginate --jq '[.[] | .full_name]' > testdata/api_user_starred.json`
func TestRecordedFixture_ListStarredRepos(t *testing.T) {
	t.Parallel()
	payload := readFixture(t, "api_user_starred.json")
	runner := &stubRunner{responses: []stubResponse{{stdout: string(payload)}}}
	client := New(runner)
	if _, err := client.ListStarredRepos(context.Background()); err != nil {
		t.Fatalf("ListStarredRepos: %v", err)
	}
}

// --- timeline decode-regression -------------------------------------------

// TestDecodeRecordedTimelineFixtures decodes real `gh api .../timeline`
// payloads captured from production. It guards against schema drift in any
// field the modeled timeline structs touch.
//
// Re-record: `gh api repos/<owner>/<name>/issues/<num>/timeline --paginate > testdata/timeline_<...>.json`
func TestDecodeRecordedTimelineFixtures(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob(filepath.Join(fixtureDir, "timeline_*.json"))
	if err != nil {
		t.Fatalf("glob testdata: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no recorded timeline fixtures found in testdata/")
	}

	for _, path := range matches {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			payload, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			events, err := decodeTimelineItems(payload)
			if err != nil {
				t.Fatalf("decodeTimelineItems: %v", err)
			}
			if len(events) == 0 {
				t.Fatal("decoded zero events from fixture")
			}
		})
	}
}

// --- schema canaries ------------------------------------------------------
//
// These tests strict-decode every sub-object of the modeled response shapes
// against snapshot structs that mirror the fields GitHub returned at capture
// time. If GitHub adds a new field, the canary fires with the fixture path,
// the field name, and the raw payload, prompting a human to either model the
// new field or extend the snapshot.

// TestRecordedTimelineSubStructSchemaSnapshot canaries the raw REST-API
// timeline endpoint. The actor.id type mismatch that wedged the daemon was
// in this surface area.
func TestRecordedTimelineSubStructSchemaSnapshot(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob(filepath.Join(fixtureDir, "timeline_*.json"))
	if err != nil {
		t.Fatalf("glob testdata: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no recorded timeline fixtures found in testdata/")
	}

	for _, path := range matches {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			payload, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var raws []map[string]json.RawMessage
			if err := json.Unmarshal(payload, &raws); err != nil {
				t.Fatalf("unmarshal raw events: %v", err)
			}
			for i, ev := range raws {
				// Auto-detect every user/commit/label-shaped sub-object on the
				// event by sniffing key sets. This way future event types
				// (review_requested adds requested_reviewer/review_requester;
				// assigned adds assignee/assigner; etc.) get canaried without
				// having to enumerate them by name.
				for key, raw := range ev {
					switch sniffShape(raw) {
					case shapeRESTUser:
						strictDecode(t, i, key, raw, &restUserSnapshot{})
					case shapeCommitIdentity:
						// Skip the timeline event's top-level "author" when it
						// is a commit-identity (committed events) - already
						// checked via shape sniffing. Nothing to do; the
						// commitIdentitySnapshot below handles it generically.
						strictDecode(t, i, key, raw, &commitIdentitySnapshot{})
					case shapeRESTLabel:
						strictDecode(t, i, key, raw, &restLabelSnapshot{})
					}
				}
			}
		})
	}
}

// TestRecordedListItemSubStructSchemaSnapshot canaries the gh-CLI `--json`
// surface used by issue/pr list, view, and search. gh stabilizes this output
// format relative to the underlying GraphQL, so drift is rarer than on raw
// `gh api`, but is still possible (gh upgrades, graphql additions).
func TestRecordedListItemSubStructSchemaSnapshot(t *testing.T) {
	t.Parallel()

	type entry struct {
		path  string
		array bool // true if fixture is [obj, ...]; false if a single obj
	}
	fixtures := []entry{
		{"pr_view.json", false},
		{"pr_view_headrefs.json", false},
		{"pr_list.json", true},
		{"issue_list.json", true},
		{"search_prs.json", true},
		{"search_issues.json", true},
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.path, func(t *testing.T) {
			t.Parallel()
			payload := readFixture(t, fx.path)
			var items []map[string]json.RawMessage
			if fx.array {
				if err := json.Unmarshal(payload, &items); err != nil {
					t.Fatalf("unmarshal array: %v", err)
				}
			} else {
				var single map[string]json.RawMessage
				if err := json.Unmarshal(payload, &single); err != nil {
					t.Fatalf("unmarshal object: %v", err)
				}
				items = []map[string]json.RawMessage{single}
			}
			for i, item := range items {
				strictDecode(t, i, "author", item["author"], &cliAuthorSnapshot{})
				strictDecode(t, i, "repository", item["repository"], &cliRepositorySnapshot{})
				strictDecode(t, i, "headRepository", item["headRepository"], &cliRepositorySnapshot{})
				strictDecode(t, i, "headRepositoryOwner", item["headRepositoryOwner"], &cliOwnerSnapshot{})
				if labels := item["labels"]; len(labels) > 0 {
					var labelArr []json.RawMessage
					if err := json.Unmarshal(labels, &labelArr); err != nil {
						t.Fatalf("event %d labels: %v", i, err)
					}
					for j, raw := range labelArr {
						strictDecode(t, i, "labels["+itoa(j)+"]", raw, &cliLabelSnapshot{})
					}
				}
			}
		})
	}
}

// --- helpers --------------------------------------------------------------

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(fixtureDir, name)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return payload
}

// sniffShape returns which snapshot the raw JSON object should be checked
// against, by looking at its key set. Returns shapeUnknown for non-objects,
// arrays, scalars, or objects that don't match a known shape.
type subObjectShape int

const (
	shapeUnknown subObjectShape = iota
	shapeRESTUser
	shapeCommitIdentity
	shapeRESTLabel
)

func sniffShape(raw json.RawMessage) subObjectShape {
	if len(raw) == 0 {
		return shapeUnknown
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return shapeUnknown
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return shapeUnknown
	}
	_, hasLogin := keys["login"]
	_, hasID := keys["id"]
	if hasLogin && hasID {
		return shapeRESTUser
	}
	_, hasDate := keys["date"]
	_, hasEmail := keys["email"]
	_, hasName := keys["name"]
	if hasDate && hasEmail && hasName {
		return shapeCommitIdentity
	}
	_, hasColor := keys["color"]
	if hasName && hasColor && !hasLogin {
		return shapeRESTLabel
	}
	return shapeUnknown
}

func strictDecode(t *testing.T, eventIndex int, field string, raw json.RawMessage, dst any) {
	t.Helper()
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("event %d: %s schema drift - GitHub returned a new field not in the snapshot: %v\npayload: %s\n\nIf the new field matters to ezoss, model it on the production struct. Either way, add it to the snapshot in client_recorded_test.go to clear this canary.", eventIndex, field, err, string(raw))
		}
		t.Fatalf("event %d: %s decode failed: %v\npayload: %s", eventIndex, field, err, string(raw))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// --- snapshot structs -----------------------------------------------------
//
// REST-API shapes (raw `gh api`) - have many fields ezoss doesn't model.

type restUserSnapshot struct {
	AvatarURL         string `json:"avatar_url"`
	EventsURL         string `json:"events_url"`
	FollowersURL      string `json:"followers_url"`
	FollowingURL      string `json:"following_url"`
	GistsURL          string `json:"gists_url"`
	GravatarID        string `json:"gravatar_id"`
	HTMLURL           string `json:"html_url"`
	ID                int64  `json:"id"`
	Login             string `json:"login"`
	NodeID            string `json:"node_id"`
	OrganizationsURL  string `json:"organizations_url"`
	ReceivedEventsURL string `json:"received_events_url"`
	ReposURL          string `json:"repos_url"`
	SiteAdmin         bool   `json:"site_admin"`
	StarredURL        string `json:"starred_url"`
	SubscriptionsURL  string `json:"subscriptions_url"`
	Type              string `json:"type"`
	URL               string `json:"url"`
	UserViewType      string `json:"user_view_type"`
}

type commitIdentitySnapshot struct {
	Date  string `json:"date"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type restLabelSnapshot struct {
	Color string `json:"color"`
	Name  string `json:"name"`
}

// gh CLI `--json` shapes - much narrower than REST since gh prunes to
// requested fields. The snapshot covers what gh returns today for the
// fields ezoss requests; gh has been observed to add fields here over
// time (e.g. is_bot was added to author).

// cliAuthorSnapshot is a union of every author field observed across gh CLI
// call sites. `gh issue/pr view` and `gh issue/pr list` return {id, is_bot,
// login, name}; `gh search prs/issues` returns {id, is_bot, login, type,
// url}. The snapshot lists all of them so the canary fires only on truly
// new fields.
type cliAuthorSnapshot struct {
	ID    string `json:"id"`
	IsBot bool   `json:"is_bot"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	URL   string `json:"url"`
}

type cliLabelSnapshot struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

type cliRepositorySnapshot struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	NameWithOwner string            `json:"nameWithOwner"`
	URL           string            `json:"url"`
	Owner         *cliOwnerSnapshot `json:"owner"`
}

type cliOwnerSnapshot struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}
