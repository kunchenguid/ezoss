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
  <a href="https://kunchenguid.github.io/ezoss/"
    ><img
      alt="Docs"
      src="https://img.shields.io/badge/docs-GitHub%20Pages-6e56cf?style=flat-square"
  /></a>
</p>

<h3 align="center">Turn your issue queue into a reviewable inbox instead of a background tax.</h3>

If you maintain open source long enough, every new issue and PR starts with the same drag: what is this actually asking, is it legit, what should happen next, and do I need to context-switch into the repo right now?

`ezoss` handles that first pass for you. It polls your repos, runs your coding agent against each untriaged issue or PR, stores a private recommendation locally, and lets you review or edit the draft before anything touches GitHub.

You stay in control. The agent drafts. The maintainer decides.

<p align="center">
  <img src="https://raw.githubusercontent.com/kunchenguid/ezoss/main/demo.gif" alt="ezoss demo" width="800" />
</p>

Docs: https://kunchenguid.github.io/ezoss/

- **Private by default** - agent rationale, draft comments, fix prompts, and token usage stay in local SQLite until you approve an action.
- **GitHub-native state** - triage visibility is mirrored back to GitHub with `ezoss/*` labels so co-maintainers can see what's going on.
- **Actually usable loop** - daemon polling, one-off triage, a Bubble Tea inbox, and approval/copy-prompt/edit/rerun flows already work end to end.
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
# opens the inbox TUI for approve, copy prompt, edit, skip, and rerun
```

## Install

**Go install**

```sh
go install github.com/kunchenguid/ezoss/cmd/ezoss@latest
```

**macOS / Linux installer**

```sh
curl -fsSL https://raw.githubusercontent.com/kunchenguid/ezoss/main/install.sh | sh
```

**Windows PowerShell installer**

```powershell
iwr https://raw.githubusercontent.com/kunchenguid/ezoss/main/install.ps1 -useb | iex
```

**From source**

```sh
git clone https://github.com/kunchenguid/ezoss.git
cd ezoss
make build
./bin/ezoss --version
```

Every GitHub release also includes platform archives plus `checksums.txt` if you prefer a manual download and verification path.

Live triage requires `gh`, `git`, and one supported agent backend available locally. Copying fix prompts from the inbox also needs a platform clipboard command: `pbcopy` on macOS, `clip` on Windows, or `wl-copy`, `xclip`, or `xsel` on Linux.

## How It Works

```
┌────────────────────┐
│ GitHub issue / PR  │
└─────────┬──────────┘
          │ poll for items without
          │ ezoss/triaged
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
          │ review/edit/approve
          ▼
┌────────────────────┐
│ inbox TUI          │
└─────────┬──────────┘
          │ execute approved gh action
          ▼
┌────────────────────┐
│ GitHub labels /    │
│ comments / reviews │
└────────────────────┘
```

- **GitHub is the visible truth** - `ezoss/triaged` is the public signal that an item has already been handled.
- **Local DB is the private memory** - drafts, fix prompts, rationales, approvals, and token accounting stay on disk under `~/.ezoss/`.
- **Checkouts are managed** - live triage clones/fetches repos under `~/.ezoss/investigations`, runs the agent there, and discards scratch edits before future runs.
- **Polling is deliberate** - v1 avoids webhook complexity and just re-triages when the GitHub label disappears.
- **Approval is explicit** - nothing gets posted, closed, merged, or labeled until you do it from the inbox.
- **PR review is gated when needed** - unsolicited PRs can surface as `state_change: none` with a draft comment asking whether the approach is wanted before the tool drafts code review feedback.

## CLI Reference

| Command | Description |
| --- | --- |
| `ezoss` | Open the inbox TUI from the local recommendations database |
| `ezoss doctor` | Check local prerequisites including `gh`, agent availability, daemon state, and SQLite access |
| `ezoss init` | Create or update `~/.ezoss/config.yaml` |
| `ezoss status` | Open the realtime status TUI; in non-interactive output, print rich text status |
| `ezoss status --short` | Print a one-line summary of pending recommendations and configured repos |
| `ezoss list` | Print pending recommendations in a text format |
| `ezoss triage <repo>#<number>` | Manually triage one issue or PR |
| `ezoss update` | Download and install the latest released binary for the current platform |
| `ezoss daemon start` | Start the background poller |
| `ezoss daemon stop` | Stop the background poller |
| `ezoss daemon status` | Show whether the daemon is running |

### Flags

| Command | Flag | Description |
| --- | --- | --- |
| `daemon start` | `--mock` | Use canned GitHub items and recommendations |
| `status` | `--short` | Print a one-line key=value summary |
| `triage <repo>#<number>` | `--mock` | Triage against canned fixtures instead of live GitHub + agent backends |
| `init` | `--repo` | Repository to monitor, repeatable |
| `init` | `--agent` | Agent backend: `auto`, `claude`, `codex`, `rovodev`, `opencode` |
| `init` | `--merge-method` | Default PR merge method: `merge`, `squash`, or `rebase` |
| `init` | `--poll-interval` | Poll cadence as a duration like `5m` |
| `init` | `--stale-threshold` | Stale threshold as a duration like `30d` or `720h` |

## Configuration

Global config lives at `~/.ezoss/config.yaml`.

Per-repo overrides live in `.ezoss.yaml` at the repo root and currently support overriding `agent`.

`merge_method` controls how approved PR merges execute and supports `merge`, `squash`, or `rebase`.

```yaml
# ~/.ezoss/config.yaml
agent: auto
poll_interval: 5m
stale_threshold: 30d
merge_method: merge
repos:
  - kunchenguid/ezoss
sync_labels:
  waiting_on: true
  stale: true
```

`ezoss/triaged` is always managed automatically because it is the public source-of-truth signal for whether an item has already been handled.

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
make docs-build # Install docs deps and build ./docs
make install    # go install + best-effort daemon install/restart
make test       # Run Go tests
make lint       # Run go vet
make fmt        # Format Go code
make fmt-check  # Fail if gofmt would change tracked Go files
```

## License

MIT. See [LICENSE](LICENSE).
