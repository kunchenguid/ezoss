# ezoss - plan

A maintainer-side agent orchestrator that watches your open source repos, triages every new issue and PR with your coding agent, and drops recommended next steps into a TUI where you approve or edit them before anything goes out.

The goal is to cut the "what is this issue even about, and what should I do with it" tax to near zero. The maintainer stays in charge - the agent never posts, closes, or labels on its own. It only drafts. The orchestrator executes.

## Reference: `no-mistakes`

Lift the skeleton from `~/github/kunchenguid/no-mistakes` wholesale where it makes sense:

- Go 1.25 + cobra CLI, `cmd/ezoss` entrypoint, most code under `internal/`.
- `internal/agent/` - `Agent` interface with `Name()/Run(ctx, RunOpts)/Close()`, plus implementations for `claude`, `codex`, `rovodev`, `opencode`. Supports streaming chunks and structured JSON-schema output. **Copy this whole package, adjust nothing semantic.**
- `internal/daemon/` - background process, signal handling, PID file, crash recovery. Our daemon is periodic rather than event-driven, but the lifecycle shape is the same.
- `internal/ipc/` - unix socket / named pipe JSON-RPC between CLI and daemon, with a streaming `Subscribe` method. Used so the TUI can attach to a running daemon and stream updates live.
- `internal/db/` - SQLite via `modernc.org/sqlite` (pure Go, no CGO, cross-platform). Additive migrations in `schema.go`.
- `internal/tui/` - `bubbletea` + `bubbles` + `lipgloss`. **Follow `DESIGN.md` from no-mistakes verbatim** (ANSI palette, boxed sections, gutter, action bar, footer, status icons).
- `internal/update/` - GitHub Releases self-update, background check, archive swap.
- `internal/config/` - YAML at `~/.ezoss/config.yaml`, plus per-repo `.ezoss.yaml`.
- `internal/paths/`, `internal/buildinfo/`, `internal/telemetry/`, `internal/shellenv/` - same patterns.
- Release pipeline: `release-please` + GitHub Actions matrix (darwin/linux/windows x amd64/arm64), `Makefile` with `build/dist/install/test/lint/fmt`, `install.sh` / `install.ps1`, Astro docs site.

What's genuinely new for `ezoss` (vs. copy-paste from no-mistakes):

- The daemon's job is polling GitHub instead of running a git gate.
- The data model is issues/PRs/recommendations instead of runs/steps/findings.
- The TUI is a queue/inbox view instead of a pipeline view.
- There's a GitHub client layer (`internal/ghclient/`) that wraps `gh` CLI.

## User-facing shape

```
$ ezoss init                 # pick repos, agent, poll cadence
$ ezoss daemon start         # launches background poller
$ ezoss                      # opens TUI inbox of pending recs
$ ezoss status               # one-line summary for shell prompts
$ ezoss doctor               # checks gh auth, agent bin, daemon, db
```

Typical loop:

1. Contributor opens issue #42 on your repo.
2. Daemon's next poll picks it up: it has no `ezoss/triaged` label yet.
3. Daemon prepares a managed repo checkout under `~/.ezoss/investigations`, then invokes your agent with the issue URL, checkout path, and a structured-output schema. The recommendation lands in the local DB.
4. You open the TUI. Issue #42 shows in the "needs your review" queue with the agent's draft response, fix prompt when present, suggested labels, and proposed next action.
5. You approve as-is, copy the fix prompt, edit, or reject. On approval, the orchestrator executes via `gh` CLI and stamps `ezoss/triaged` on the issue.
6. If anyone (you, a co-maintainer) removes that label later, the daemon re-triages on next poll.

## TUI layout

Follows `DESIGN.md` primitives. Rough sketch:

```
╭─ Inbox (12 pending, 3 repos) ─────────────────────────────────────────╮
│  > [ ] ● kunchenguid/no-mistakes #142  2h                             │
│           Bug: pipeline hangs on rebase conflict                      │
│           → comment + label: bug, needs-repro                         │
│    [ ] ▲ kunchenguid/no-mistakes PR#139  5h                           │
│           feat: add opencode streaming                                │
│           → request changes (3 comments) + label: needs-work          │
│    [ ] ● other-repo #88  1d                                           │
│           Question about install on Windows                           │
│           → answer + close as resolved                                │
╰───────────────────────────────────────────────────────────────────────╯
 a approve  c copy prompt  e edit  s skip  r rerun  d details │ ␣ toggle
╭─ Details ─────────────────────────────────────────────────────────────╮
│  Rationale: the error trace in the body matches a known race in ...   │
│  Draft response:                                                      │
│    Thanks for the repro! Can you also share the output of `...`?      │
│  Fix prompt:                                                          │
│    Fix https://github.com/... by reproducing the failure and testing. │
│  Proposed labels: bug, needs-repro                                    │
│  Confidence: medium  ·  tokens: 12.4k in / 1.1k out                   │
╰───────────────────────────────────────────────────────────────────────╯
 q quit  ? help
```

Two-pane list/detail. Inbox on top, detail below. `j/k` moves cursor; up/down arrows scroll overflowing detail content. `a` approves the selected item(s), `c` copies the active option's coding-agent fix prompt, `e` opens `$EDITOR` for the draft, `s` dismisses (won't retrigger unless the `ezoss/triaged` label is removed on GitHub), `r` reruns triage.

The details pane always shows cumulative token usage for the item - every triage/re-triage gets attributed so the maintainer can see what the agent is costing them per issue.

## State model

**GitHub is the source of truth. Local DB only holds things that should stay private** - draft recommendations, fix prompts, agent rationales, token counts, and the approval audit trail. Anything the maintainer would want a co-maintainer to see (triage status, waiting-on signals, resolution) lives on GitHub as labels or native state.

The "have we triaged this yet" signal is a single GitHub label: **`ezoss/triaged`**. Applied by the orchestrator after the maintainer approves and the action executes. If anyone removes it, the next poll picks the item up again and the old recommendation is superseded.

Other useful labels the orchestrator may apply on approval (all opt-in via config):

- `ezoss/awaiting-contributor`, `ezoss/awaiting-maintainer` - reflects the `waiting_on` signal on GitHub so co-maintainers can filter.
- `ezoss/stale` - set when we've been waiting on the contributor past the stale threshold.

`waiting_on` is inferred from GitHub activity (last commenter, CI state) and re-computed on every poll. It's not authoritative on its own; the label is the visible form.

Per-item local cache fields (just enough to render the TUI without hammering GitHub):

| field           | values                                                     |
| --------------- | ---------------------------------------------------------- |
| `kind`          | `issue` \| `pr`                                            |
| `is_draft`      | true for draft PRs / WIP-titled PRs - skipped from triage  |
| `gh_triaged`    | bool, mirror of `ezoss/triaged` label on GitHub    |
| `waiting_on`    | `maintainer` \| `contributor` \| `ci` \| `none` (inferred) |
| `last_event_at` | timestamp of latest GH event we've seen                    |
| `stale_since`   | when the item crossed the stale threshold, if applicable   |

Anything not in this table we fetch fresh from GitHub when needed.

## Schema (first cut)

```sql
CREATE TABLE repos (
    id              TEXT PRIMARY KEY,   -- owner/name
    default_branch  TEXT,
    last_poll_at    INTEGER,
    created_at      INTEGER
);

CREATE TABLE items (                    -- thin cache of GH items we've seen
    id              TEXT PRIMARY KEY,   -- owner/name#number
    repo_id         TEXT NOT NULL REFERENCES repos(id),
    kind            TEXT NOT NULL,      -- 'issue' | 'pr'
    number          INTEGER NOT NULL,
    title           TEXT,
    author          TEXT,
    state           TEXT,               -- 'open' | 'closed' | 'merged'
    is_draft        INTEGER NOT NULL DEFAULT 0,
    gh_triaged      INTEGER NOT NULL DEFAULT 0,
    waiting_on      TEXT,
    last_event_at   INTEGER,
    stale_since     INTEGER,
    created_at      INTEGER,
    updated_at      INTEGER
);

CREATE TABLE recommendations (
    id               TEXT PRIMARY KEY,
    item_id          TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    agent            TEXT NOT NULL,
    model            TEXT,
    rationale        TEXT,
    draft_comment    TEXT,
    followups        TEXT,
    proposed_labels  TEXT,              -- JSON array
    state_change     TEXT,              -- 'none' | 'close' | 'merge' | 'request_changes'
    confidence       TEXT,              -- 'low' | 'medium' | 'high'
    tokens_in        INTEGER,
    tokens_out       INTEGER,
    created_at       INTEGER,
    superseded_at    INTEGER
);

CREATE TABLE recommendation_options (
    id                 TEXT PRIMARY KEY,
    recommendation_id  TEXT NOT NULL REFERENCES recommendations(id) ON DELETE CASCADE,
    position           INTEGER NOT NULL,
    state_change       TEXT,
    rationale          TEXT,
    draft_comment      TEXT,
    fix_prompt         TEXT,
    proposed_labels    TEXT,
    confidence         TEXT,
    waiting_on         TEXT,
    followups          TEXT,
    created_at         INTEGER
);

CREATE TABLE approvals (
    id                 TEXT PRIMARY KEY,
    recommendation_id  TEXT NOT NULL REFERENCES recommendations(id),
    option_id          TEXT REFERENCES recommendation_options(id) ON DELETE SET NULL,
    decision           TEXT NOT NULL,   -- 'approved' | 'edited' | 'rejected' | 'dismissed'
    final_comment      TEXT,
    final_labels       TEXT,
    final_state_change TEXT,
    acted_at           INTEGER,         -- when the gh call actually happened
    acted_error        TEXT,
    created_at         INTEGER
);
```

Recommendations are immutable and superseded when re-triage runs. Full history is preserved. Token counts on the recommendations table roll up for per-item cost display in the TUI.

## GitHub integration

Use `gh` CLI rather than the REST/GraphQL SDK directly:

- No separate auth path - user already has `gh auth login`.
- Shell out via `exec.CommandContext`, same as how `no-mistakes` drives the agent binaries.
- `gh api` gives us GraphQL when we need it (e.g. fetching issue + comments + reactions in one round trip).
- `gh issue list --json ...` / `gh pr list --json ...` for bulk reads.

Polling strategy:

- Every `poll_interval` (default 5 min) per repo.
- `gh issue list --search "-label:ezoss/triaged"` / same for PRs - gets us everything that needs triage in one query.
- Skip items where `isDraft=true` or the title matches a WIP pattern (`WIP:`, `[WIP]`, `[draft]`).
- Items that had the label and no longer do come back into the queue automatically - the query is the trigger.
- For re-computing `waiting_on` and stale detection, also fetch items with the label on a slower cadence (default 1h).
- Backoff on rate limit (`x-ratelimit-remaining` from `gh api`).

**Webhooks are deliberately out of scope for v1.** They need a public endpoint or a tunnel service, which is a big complexity jump. Polling gets us 90% of the value.

## Agent pipeline

For each item that needs triage, run one agent call with a structured-output schema:

```json
{
  "options": [
    {
      "state_change": "none | close | merge | request_changes",
      "rationale": "short paragraph explaining what this is and why",
      "waiting_on": "maintainer | contributor | ci | none",
      "draft_comment": "markdown text or empty",
      "fix_prompt": "coding-agent handoff prompt with original URL, or empty",
      "confidence": "low | medium | high",
      "followups": ["optional list of things the maintainer might want to check"]
    }
  ]
}
```

The prompt is intentionally minimal:

- The issue or PR URL.
- The contents of `~/.ezoss/AGENTS.md` if it exists (user-supplied instructions - voice profile, custom guidance, house rules - injected verbatim into every prompt).
- A brief note that the agent should inspect the managed checkout and any issue body, comments, diff, linked issues, or CI context it needs.
- For legitimate actionable issues or PRs, return `fix_prompt` with the original URL, investigation context, acceptance criteria, and verification steps; otherwise leave it empty.

We don't pre-fetch issue context or stuff the prompt. Ezoss prepares the repo checkout, the agent decides what to look at, and local scratch edits are discarded before a future run. The agent interface already supports streaming, so the TUI can show rationale appearing live when the user runs `rerun`.

### PR-specific flow

PRs branch on whether there's a prior agreement to build the thing:

1. Agent first checks: is this PR linked to an issue where the approach was already discussed and agreed upon?
2. **Yes** → proceed to code review. Produce a top-level review comment as `draft_comment` and set `state_change: request_changes` or `state_change: merge` based on the review.
3. **No** → set `state_change: none`. The draft comment should explain what the PR is doing and ask the maintainer whether the approach is wanted before any code review happens. The maintainer can approve that option, which posts the question to the PR, or manually edit it to do a code review anyway via `e`.

Line-level review comments stay out of v1 - top-level review only. Upgrade later.

### Stale handling

Default behavior, not opt-in:

- When `waiting_on = contributor` for more than `stale_threshold` (default 30 days), surface a "stale - consider closing" recommendation in the inbox.
- Approved stale recs translate to a `gh issue close` with a polite comment (drafted by the agent, using AGENTS.md voice).

## Acting on approvals

Once the user approves a recommendation option, the orchestrator posts `draft_comment` when present, applies lifecycle labels independently, then translates `state_change` to `gh` commands:

- `none` → no state transition; post only the draft comment and labels.
- `close` → `gh issue close <n> -c <body>` (close with a comment when present).
- `merge` → `gh pr merge <n> --squash/--merge/--rebase` (default configurable).
- `request_changes` → `gh pr review <n> --request-changes -b <body>`.

After any successful action, add the `ezoss/triaged` label to the item (and `ezoss/awaiting-*` if configured). That's what keeps the item out of the next poll's triage queue.

Errors from `gh` surface back into the `approvals` row and into the TUI as a red banner on the item.

## Config

`~/.ezoss/config.yaml`:

```yaml
agent: auto # auto | claude | codex | rovodev | opencode
poll_interval: 5m
stale_threshold: 30d
repos:
  - owner/name
  - owner/other
sync_labels: # which ezoss/* labels to mirror to GH
  triaged: true
  waiting_on: true # writes ezoss/awaiting-{contributor,maintainer}
  stale: true
```

`~/.ezoss/AGENTS.md` is free-form; whatever the user puts there is appended to every triage prompt. Great place for voice profile, project-specific triage heuristics, or "always ask for a repro before labeling as bug."

## Multi-repo

Single daemon, many repos. Repos configured in `~/.ezoss/config.yaml`. Agent choice is global with per-repo override via `.ezoss.yaml` in the repo root. GitHub auth is whatever `gh` is currently authed as - multi-account is out of scope for now.

## Phased build

**Phase 0 - scaffolding**

- `cmd/ezoss/main.go`, cobra root, `doctor`, `--version`.
- Copy `internal/paths`, `internal/buildinfo`, `internal/shellenv` from no-mistakes, rename module.
- Makefile, `.github/workflows/{ci,release}.yml`, release-please config.
- Minimum CI: gofmt, vet, test matrix.

**Phase 1 - core loop, no TUI**

- Copy `internal/agent/` verbatim.
- `internal/ghclient/` - thin wrapper over `gh` with `ListNeedingTriage`, `GetItem`, action methods, label add/remove.
- `internal/db/` with schema above.
- `internal/daemon/` with poll loop (filtering by absence of `ezoss/triaged`, skipping drafts), triage dispatch, structured-output agent call, stale detection pass.
- CLI: `init`, `daemon {start,stop,status}`, `triage <repo>#<n>` (manual trigger), `list` (dump pending recs as text).
- `--mock` flag on `daemon start` (and wherever else useful) that swaps the `ghclient` and `agent` implementations for fakes returning canned items and recommendations. Lets us exercise poll → triage → DB → TUI end-to-end without hitting GitHub or burning agent tokens. Fixtures live under `internal/ghclient/mock/` and `internal/agent/mock/`; the fakes should produce a realistic mix (issues, PRs, drafts, stale items, varying confidences) so the TUI has something interesting to render.

**Phase 2 - TUI**

- Copy `internal/ipc/`, `internal/tui/` skeleton.
- Inbox list view, detail pane with token usage, approve/copy prompt/edit/skip/rerun actions.
- Live subscription so new triages land in the TUI without a refresh.
- Follow `DESIGN.md` primitives exactly.

**Phase 3 - update, docs, polish**

- Copy `internal/update/`, wire into release workflow.
- Astro docs site (`docs/`), `install.sh`, `install.ps1`.
- `demo.tape` for VHS recording, README trim.
- Telemetry if desired (same pattern as no-mistakes - opt-in umami).

**Phase 4 - nice to haves**

- Batch operations ("approve all `needs-repro` labels").
- Line-level PR review comments.
- Duplicate issue detection (vector similarity over closed issues).
- Webhook mode for faster reactions.
