<h1 align="center">ezoss</h1>
<p align="center">
  <a href="https://github.com/kunchenguid/ezoss/actions/workflows/ci.yml"
    ><img
      alt="CI"
      src="https://img.shields.io/github/actions/workflow/status/kunchenguid/ezoss/ci.yml?style=flat-square&label=ci"
  /></a>
  <a href="https://github.com/kunchenguid/ezoss/actions/workflows/release.yml"
    ><img
      alt="Release"
      src="https://img.shields.io/github/actions/workflow/status/kunchenguid/ezoss/release.yml?style=flat-square&label=release"
  /></a>
  <a
    href="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue?style=flat-square"
    ><img
      alt="Platform"
      src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue?style=flat-square"
  /></a>
  <a href="https://x.com/kunchenguid"
    ><img
      alt="X"
      src="https://img.shields.io/badge/X-@kunchenguid-black?style=flat-square"
  /></a>
  <a href="https://discord.gg/Wsy2NpnZDu"
    ><img
      alt="Discord"
      src="https://img.shields.io/discord/1439901831038763092?style=flat-square&label=discord"
  /></a>
</p>

<h3 align="center">Turn your issue queue into a reviewable inbox instead of a background tax.</h3>

If you maintain open source long enough, every new issue and PR starts with the same drag: what is this actually asking, is it legit, what should happen next, and do I need to context-switch into the repo right now?

`ezoss` handles that first pass for you. It polls your repos, also tracks issues and PRs you authored in repos you do not maintain, runs your coding agent against each untriaged item, stores a private recommendation locally, and lets you review or edit the draft before anything touches GitHub.

You stay in control. The agent drafts. You decide.

<p align="center">
  <img src="https://raw.githubusercontent.com/kunchenguid/ezoss/main/demo.gif" alt="ezoss demo" width="800" />
</p>

- **Private by default** - agent rationale, draft comments, fix prompts, and token usage stay in local SQLite until you approve an action.
- **GitHub-native maintainer state** - maintainer triage visibility is mirrored back to GitHub with `ezoss/*` labels, while contributor items stay local.
- **Actually usable loop** - daemon polling, one-off triage, a Bubble Tea inbox, and approval/fix-PR/copy-prompt/edit/rerun flows already work end to end.
- **PRs can pause before review** - PRs without prior agreement can be routed into a maintainer approval step before code review.

## Quick Start

```sh
$ ezoss init --repo kunchenguid/ezoss --agent auto
initialized /Users/you/.ezoss/config.yaml (1 repos)

$ ezoss daemon start --mock
started

$ ezoss status
# opens the realtime status TUI

$ ezoss list
kunchenguid/ezoss#42	issue	comment	low	panic in sync loop

$ ezoss
# opens the mock inbox TUI for approve, copy prompt, edit, mark triaged, open, and rerun
```

## Install

**macOS / Linux installer**

```sh
curl -fsSL https://raw.githubusercontent.com/kunchenguid/ezoss/main/install.sh | sh
```

**Windows PowerShell installer**

```powershell
iwr https://raw.githubusercontent.com/kunchenguid/ezoss/main/install.ps1 -useb | iex
```

**Go install**

```sh
go install github.com/kunchenguid/ezoss/cmd/ezoss@latest
```

**From source**

```sh
git clone https://github.com/kunchenguid/ezoss.git
cd ezoss
make build
./bin/ezoss --version
```

Every GitHub release also includes platform archives plus `checksums.txt` if you prefer a manual download and verification path.

Live triage requires `gh auth login`, `git`, one supported agent backend, and a writable state directory under `~/.ezoss`.

Copying fix prompts from the inbox also needs a platform clipboard command: `pbcopy` on macOS, `clip` on Windows, or `wl-copy`, `xclip`, or `xsel` on Linux.

Opening items from the inbox needs a platform browser command: `open` on macOS, `rundll32` on Windows, or `xdg-open` on Linux.

Opening fix PRs needs `gh`; `fixes.pr_create: no-mistakes` also needs `no-mistakes`.

## How It Works

```
┌────────────────────┐
│ GitHub issue / PR  │
└─────────┬──────────┘
          │ maintainer label poll
          │ + contributor search
          ▼
┌────────────────────┐
│ daemon poller      │
└─────────┬──────────┘
          │ prepares checkout + prompt + schema
          ▼
┌────────────────────┐
│ agent backend      │
│ claude/codex/etc.  │
└─────────┬──────────┘
          │ structured recommendation
          ▼
┌────────────────────┐
│ local SQLite cache │
└─────────┬──────────┘
          │ review/edit/approve/fix
          ▼
┌────────────────────┐
│ inbox TUI          │
└─────────┬──────────┘
          │ execute approved gh action
          │ or queued fix job
          ▼
┌────────────────────┐
│ GitHub labels /    │
│ comments / reviews │
│ / fix branch work  │
└────────────────────┘
```

- **GitHub is the maintainer truth** - for configured repos, `ezoss/triaged` is the public signal that an item has already been handled.
  The daemon also watches for new comments, reviews, or commits after that label and can put the item back in the inbox even if the label remains.
  Contributor items are found with `gh search prs/issues --author=@me`, do not edit upstream labels, and are tracked with local sweep metadata.
- **Local DB is the private memory** - drafts, fix prompts, rationales, approvals, and token accounting stay on disk under `~/.ezoss/`.
  Rerun instructions are stored there too.
- **Checkouts are managed** - live triage clones/fetches repos under `~/.ezoss/investigations`, runs the agent there, and discards scratch edits before future runs.
- **Contributor mode is automatic** - by default, the daemon searches for open issues and PRs authored by you in repos you do not maintain, marks them with a `contrib` badge in the inbox, and uses contributor-safe actions instead of maintainer actions.
- **Fixes use isolated worktrees** - `fix_required` options can queue daemon-backed jobs under `~/.ezoss/fixes`, run the selected coding agent, commit changes, and either create maintainer draft PRs according to `fixes.pr_create` or prepare contributor PR branch updates according to `fixes.contrib_push`.
  Re-queueing a fix for the same item replaces a queued or waiting-for-PR job; if the agent is already running, retry after that job advances or finishes.
- **Polling is deliberate** - v1 avoids webhook complexity; maintainer items re-triage when the GitHub label disappears or post-label comments, reviews, or commits appear, and contributor items re-triage from local self-activity tracking.
- **Approval is explicit** - comments, labels, closes, merges, maintainer fix PRs, and contributor PR branch updates only happen after you approve an inbox action, queue a fix job, or run `ezoss fix`.
- **PR review is gated when needed** - unsolicited PRs can surface as `state_change: none` with a draft comment asking whether the approach is wanted before the tool drafts code review feedback.

## Inbox Actions

| Key     | Action       | Description                                                               |
| ------- | ------------ | ------------------------------------------------------------------------- |
| `a`     | Approve      | Execute the selected GitHub action; maintainer items sync labels, contributor items are marked handled locally |
| `c`     | Copy prompt  | Copy the active option's coding-agent fix prompt when one exists          |
| `f`     | Fix          | Queue or replace a daemon-backed coding-agent fix job when a fix prompt exists |
| `F`     | Filter       | Cycle role filter through all, maintainer, and contributor items           |
| `e`     | Edit         | Open the draft in your editor before approval                             |
| `m`     | Mark triaged | Stamp `ezoss/triaged` for maintainer items, or mark contributor items handled locally |
| `o`     | Open         | Open the current item's GitHub page in your browser                       |
| `r`     | Rerun        | Re-triage the item and replace the active recommendation                  |
| `j`/`k` | Navigate     | Move between inbox items; use arrow keys to scroll overflowing text        |

## CLI Reference

| Command                        | Description                                                                                   |
| ------------------------------ | --------------------------------------------------------------------------------------------- |
| `ezoss`                        | Open the inbox TUI from the local recommendations database                                    |
| `ezoss doctor`                 | Check local prerequisites including `gh`, agent availability, daemon state, and SQLite access |
| `ezoss init`                   | Create or update `~/.ezoss/config.yaml`                                                       |
| `ezoss status`                 | Open the realtime status TUI; in non-interactive output, print rich text status               |
| `ezoss status --short`         | Print a one-line summary of pending recommendations, configured repos, and contributor state  |
| `ezoss list`                   | Print pending recommendations in a text format, including contributor markers                 |
| `ezoss fix <repo>#<number>`    | Run or replace the active fix prompt in an isolated worktree; maintainer PRs and contributor pushes follow config |
| `ezoss triage <repo>#<number>` | Manually triage one issue or PR                                                               |
| `ezoss update`                 | Download and install the latest released binary for the current platform                      |
| `ezoss daemon start`           | Start the background poller                                                                   |
| `ezoss daemon stop`            | Stop the background poller                                                                    |
| `ezoss daemon status`          | Show whether the daemon is running                                                            |

`ezoss status --short` always prints `pending`, `repos`, and `daemon`.
When pending recommendations include non-configured maintainer repos or contributor items, it also prints `maintainer`, `unconfigured`, and/or `contrib` counts.
It prints `contrib_repos` when contributor recommendations span one or more repos, and `contrib_mode=off` when contributor mode is disabled.

### Flags

| Command                  | Flag                | Description                                                            |
| ------------------------ | ------------------- | ---------------------------------------------------------------------- |
| `daemon start`           | `--mock`            | Use canned GitHub items and recommendations                            |
| `status`                 | `--short`           | Print a one-line key=value summary                                     |
| `triage <repo>#<number>` | `--mock`            | Triage against canned fixtures instead of live GitHub + agent backends |
| `fix <repo>#<number>`    | `--pr-create`       | Override maintainer fix PR creation: `auto`, `no-mistakes`, `gh`, or `disabled` |
| `fix <repo>#<number>`    | `--prepare-only`    | Prepare the isolated worktree without running the coding agent         |
| `init`                   | `--repo`            | Repository to monitor, repeatable                                      |
| `init`                   | `--agent`           | Agent backend: `auto`, `claude`, `codex`, `rovodev`, `opencode`        |
| `init`                   | `--merge-method`    | Default PR merge method: `merge`, `squash`, or `rebase`                |
| `init`                   | `--poll-interval`   | Poll cadence as a duration like `5m`                                   |
| `init`                   | `--stale-threshold` | Stale threshold as a duration like `30d` or `720h`                     |

## Configuration

Global config lives at `~/.ezoss/config.yaml`.

Per-repo overrides live in `.ezoss.yaml` at the repo root and currently support overriding `agent`.

`merge_method` controls how approved PR merges execute and supports `merge`, `squash`, or `rebase`.

`fixes.pr_create` controls how fix PRs are created and supports `auto`, `no-mistakes`, `gh`, or `disabled`.
`auto` prefers `no-mistakes` when both `no-mistakes` and `gh` are available, then uses `gh` when `no-mistakes` is unavailable or fails before PR detection.
`no-mistakes` pushes to the no-mistakes remote and uses `gh` to detect the created PR.
If daemon detection misses the PR, the inbox keeps the job in `waiting_for_pr` and shows `cd <worktree> && no-mistakes attach` for manual recovery.
`gh` pushes to origin and runs `gh pr create --draft`.
`disabled` commits the fix branch in the worktree without opening a PR.

`fixes.contrib_push` controls contributor PR fixes and supports `auto`, `no-mistakes`, or `disabled`.
Contributor fixes update the existing PR head branch instead of creating a new PR.
Contributor fix jobs apply to authored PRs, not authored issues.
`auto` pushes commits back to that branch.
`no-mistakes` is the default and leaves the worktree for manual review and push.
`disabled` refuses contributor fix jobs before pushing.

`contrib.enabled` controls contributor mode and defaults to `true`.
When enabled, the daemon searches for open issues and PRs authored by you in repos you do not maintain.
Use `contrib.ignore_repos` to suppress noisy upstream repos by exact `owner/name` match, or set `enabled: false` to only triage configured maintainer repos.

```yaml
# ~/.ezoss/config.yaml
agent: auto
poll_interval: 5m
stale_threshold: 30d
merge_method: merge
fixes:
  pr_create: auto
  contrib_push: no-mistakes
contrib:
  enabled: true
  ignore_repos: []
repos:
  - kunchenguid/ezoss
sync_labels:
  waiting_on: true
  stale: true
```

For maintainer items, `ezoss/triaged` is always managed automatically because it is the public source-of-truth signal for whether an item has already been handled.
New comments, reviews, or commits after that label can still re-queue the item locally.

Precedence is simple:

1. Built-in defaults
2. `~/.ezoss/config.yaml`
3. Per-repo `.ezoss.yaml` overrides

If `~/.ezoss/AGENTS.md` exists, its contents are appended to every triage prompt.

Daemon logs are written to `~/.ezoss/logs/daemon.log`. Set `EZOSS_LOG_LEVEL` to `debug`, `info`, `warn`, or `error` to adjust structured log verbosity.

## Development

```sh
make build      # Build ./bin/ezoss
make demo       # Regenerate demo.gif with VHS and ffmpeg
make dist       # Cross-compile release archives into ./dist
make install    # go install + daemon install/restart; fails on daemon errors unless EZOSS_SKIP_DAEMON=1
make test       # Run Go tests
make lint       # Run go vet
make fmt        # Format Go code
make fmt-check  # Fail if gofmt would change tracked Go files
```

## License

MIT. See [LICENSE](LICENSE).
