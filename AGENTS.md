# AGENTS.md

This file provides shared guidance for coding agents working in this repository. `CLAUDE.md` points here for Claude Code compatibility.

## Project

`ezoss` is a single-user, maintainer-side orchestrator written in Go.
A background daemon polls configured GitHub repos, runs a coding agent (`claude`, `codex`, `rovodev`, or `opencode`) against any issue or PR that does not yet carry the `ezoss/triaged` label, stores a structured recommendation in a local SQLite cache, and surfaces drafts in a Bubble Tea TUI inbox where the maintainer approves, queues fixes, edits, marks triaged, or reruns.
Nothing is posted to GitHub until the maintainer approves an action, queues a fix job, or runs `ezoss fix`; daemon fix jobs run only after the maintainer queues them.

`README.md` is the user-facing surface. This file is the agent-facing implementation guide.

## Common commands

```sh
make build              # builds ./bin/ezoss with version ldflags
make test               # go test ./...
go test ./internal/cli  # run one package
go test ./internal/cli -run TestRoot   # run one test
make lint               # go vet ./...
make fmt                # go fmt ./...
make fmt-check          # CI gate; fails if gofmt would change tracked files
make dist               # cross-compile release archives + checksums into ./dist
make install            # go install + install/restart the local daemon
make demo               # vhs demo.tape (requires VHS)
```

CI runs `fmt-check`, `lint`, `test`, and `build` on Ubuntu, macOS, and Windows; installer smoke tests for shell and PowerShell; packaged archive smoke checks; and release archive verification. `make fmt-check` is the same formatting gate, so run it locally before pushing.

`make install` triggers `ezoss daemon install` and `ezoss daemon restart` after install; failures fail the target. Set `EZOSS_SKIP_DAEMON=1` to skip those side effects.

## Architecture

### Process topology

There are two long-lived processes plus on-demand CLI invocations:

- The **CLI** (`cmd/ezoss` -> `internal/cli`) is one cobra binary that fans out to subcommands (`doctor`, `fix`, `init`, `list`, `status`, `triage`, `update`, `daemon {run,start,stop,restart,install,uninstall}`) and, with no args, opens the **inbox TUI**.
- The **daemon** is the same binary invoked as `ezoss daemon run`, started in the background by `daemon start`. PID lives at `~/.ezoss/daemon.pid`.
- The CLI and TUI talk to a running daemon over a **JSON-RPC IPC channel** at `~/.ezoss/daemon.sock` (Unix domain socket / Windows named pipe).
  `fix.start` queues fix jobs, `sync.status` reports daemon progress, and `ipc.Subscribe` streams `recommendation_*` and `fix_job_*` events so the TUI updates live.

All on-disk state lives under the path returned by `internal/paths` (`~/.ezoss` by default; overridable via the `AM_HOME` env var, useful in tests):
- `ezoss.db` - SQLite (modernc.org/sqlite, pure Go, no CGO)
- `daemon.pid`, `daemon.sock`
- `logs/`
- `investigations/` - managed per-repo checkouts the agent runs against
- `fixes/` - isolated worktrees used by coding-agent fix jobs
- `update-check.json` - cached self-update state
- Optional `AGENTS.md` whose contents get appended to every triage prompt

### Triage pipeline (the core loop)

`internal/daemon/poll.go` runs each cycle in three sequential stages:

1. **Stage A (sync):** for each configured repo, call the GitHub client (`internal/ghclient`, which shells out to `gh`) to list items missing `ezoss/triaged` and items recently re-triaged.
   Reconcile into the `items` table.
   Phase reported as `"sync"`.
2. **Stage B (fixes):** reclaim stale running fix jobs, detect PRs for jobs waiting on `no-mistakes`, then claim at most one queued fix job.
   If fix work happened, the cycle stops before agent triage so fix runs do not contend with new triage runs.
3. **Stage C (agents):** for each item lacking a current recommendation, build a prompt via `internal/triage.PromptWithRerunInstructions`, hand it plus `triage.Schema()` to the resolved `agent.Agent`, parse the structured JSON output via `triage.Parse`, and write a `recommendations` row plus one row per option in `recommendation_options`.
   Phase reported as `"agents"`.
   A per-item timeout (default 30m, `Poller.PerItemTriageTimeout`) prevents one stuck subprocess from wedging the daemon.

Fix work comes from `fix.start` in the TUI path or from `ezoss fix <owner/repo#number>` in the CLI path.
`cliFixRunner` prepares an isolated worktree under `~/.ezoss/fixes`, resolves repo/global agent config, runs the selected agent with the option's `fix_prompt`, commits produced changes, and creates a draft PR according to `fixes.pr_create`.

Maintainer-provided TUI rerun instructions are threaded through `Poller.RerunInstructions`, appended to the agent prompt as private context, and stored on the refreshed `recommendations` row. Guided reruns use `InsertRecommendationReplacingActiveBefore` so an older in-flight triage result cannot supersede a newer active recommendation.

The agent's contract is the `Recommendation` JSON schema in `internal/triage/triage.go` - a list of self-contained `RecommendationOption` entries, each with `state_change` (`none|close|merge|request_changes|fix_required`), `rationale`, `waiting_on`, `draft_comment`, `fix_prompt`, `confidence`, optional `followups`.
Use `fix_required` plus `fix_prompt` when the item should be handed to a coding agent before it can be closed.
The agent is asked to return 2-3 options when there are multiple reasonable next steps.
**User-namespaced labels are deliberately not part of the agent contract** (the agent has no reliable view of which labels exist in the repo); only the `ezoss/*` namespace is managed automatically.

For PRs without prior issue-level agreement on the approach, the prompt instructs the agent to set `state_change: none` and ask in `draft_comment` rather than going straight to `request_changes` or `merge`.

### Agent backend layer

`internal/agent` defines a single `Agent` interface (`Name() / Run(ctx, RunOpts) / Close()`) with implementations for `claude`, `codex`, `rovodev`, `opencode`. `agent.Resolve` walks `autoProbeOrder` (claude -> codex -> opencode -> rovodev) when the user picks `auto`. `RunOpts.JSONSchema` requests structured output; `OnChunk` streams partial text. `TokenUsage.TotalInputTokens()` adds cached + cache-creation to plain input tokens (matches what users see in `claude /usage`).

Tests for each agent should not require the real binary; the package ships a `mock` subpackage and the daemon supports a `--mock` flag for canned items and recommendations so the triage pipeline can be exercised without `gh`, agent binaries, or network.
Mock daemon mode does not run coding-agent fix jobs.

### Data model

Schema lives in `internal/db/schema.go`. Migrations are **additive only**, applied via `ensureColumnExists` in `db.Open`. There is also a `backfillRecommendationOptions` migration that splits legacy single-row recommendations into `recommendation_options`; keep that idempotent if you change the shape further. Key tables:

- `repos`, `items` (issues + PRs interleaved, distinguished by `kind`)
- `recommendations` (one per agent run on an item, including optional rerun instructions) with legacy single-row fields kept for backfill
- `recommendation_options` (the agent's proposed alternatives, ordered by `position`)
- `fix_jobs` (daemon-backed coding-agent runs for selected fix prompts, including branch, worktree path, PR URL, status, phase, and errors)
- `approvals` (the maintainer's decision; `option_id` points at the chosen option)

`gh_triaged` on `items` mirrors the GitHub label state. The label is the public source of truth: removing it on GitHub re-queues the item for triage.

### TUI

`internal/tui/tui.go` is a Bubble Tea program (`bubbletea` + `bubbles` + `lipgloss`).
It pins `lipgloss.SetColorProfile(termenv.ANSI)` for portable styling.
The TUI subscribes to the daemon over IPC and reacts to recommendation and fix-job events; it can also operate against the DB directly (used in tests and when no daemon is running).
Layout is inbox list on top, details pane below, action bar.
The `f` action queues a fix job for the active option when a fix prompt exists.

### Configuration

Precedence (low -> high): built-in defaults -> `~/.ezoss/config.yaml` (`internal/config.LoadGlobal`) -> per-repo `.ezoss.yaml` at the repo root (currently `agent` only).
Agent values: `auto`, `claude`, `codex`, `rovodev`, `opencode`.
Merge methods: `merge`, `squash`, `rebase`.
Fix PR creation modes under `fixes.pr_create`: `auto`, `no-mistakes`, `gh`, `disabled`.
Durations parse Go `time.Duration` plus the suffix `d` for days (e.g. `30d`).

### Self-update

`internal/update` implements a GitHub Releases auto-updater. `update.MaybeNotifyAndCheck` runs in `main` to print a notice if a newer cached release exists and to refresh the cache in the background; `update.MaybeHandleBackgroundCheck` intercepts the internal `--update-check` flag before cobra parses argv so the spawned background process never reaches user-facing code. `ezoss update` runs the foreground download/replace.

### Versioning

Build version is injected via `-ldflags` into `internal/buildinfo.Version` (defaults to `dev`). `UMAMI_WEBSITE_ID` is a build-time constant for optional Umami telemetry; users can override at runtime via `EZOSS_UMAMI_WEBSITE_ID`. Releases run through `release-please` (config in `.release-please-config.json`); **do not hand-edit `CHANGELOG.md` or the release manifest**.

## Conventions specific to this codebase

- **Go 1.26.** Pure Go - SQLite is `modernc.org/sqlite`, no CGO required, so cross-compilation in `make dist` works for all six platform/arch targets.
- **`internal/cli/root.go` uses package-level function variables** (e.g. `runDoctor`, `openDB`, `newAgent`) as seams that tests swap out. When adding a new external dependency to a CLI command, follow the same pattern instead of calling the concrete function directly.
- **Platform-specific files** use the `_unix.go` / `_windows.go` suffix convention (see `internal/daemon/process_*.go`, `internal/ipc/transport_*.go`, `internal/update/spawn_*.go`). Mirror that when adding new platform-conditional code.
- **The `ezoss/triaged` label is sacred** - it is the only GitHub-visible signal of triage state and is always managed by the daemon regardless of `sync_labels` config.
- Tests should not require `gh`, agent binaries, or network. Use the mock packages under `internal/agent/mock` and `internal/ghclient/mock`, the `--mock` daemon flag, or `paths.WithRoot` + a temp dir for filesystem isolation.
