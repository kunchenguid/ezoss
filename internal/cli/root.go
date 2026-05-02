package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/kunchenguid/ezoss/internal/agent"
	agentmock "github.com/kunchenguid/ezoss/internal/agent/mock"
	"github.com/kunchenguid/ezoss/internal/buildinfo"
	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/daemon"
	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/doctor"
	fixflow "github.com/kunchenguid/ezoss/internal/fix"
	"github.com/kunchenguid/ezoss/internal/ghclient"
	ghmock "github.com/kunchenguid/ezoss/internal/ghclient/mock"
	"github.com/kunchenguid/ezoss/internal/ipc"
	"github.com/kunchenguid/ezoss/internal/paths"
	prcreator "github.com/kunchenguid/ezoss/internal/pr"
	"github.com/kunchenguid/ezoss/internal/shellenv"
	"github.com/kunchenguid/ezoss/internal/telemetry"
	"github.com/kunchenguid/ezoss/internal/triage"
	"github.com/kunchenguid/ezoss/internal/tui"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
	"github.com/kunchenguid/ezoss/internal/update"
	"github.com/kunchenguid/ezoss/internal/wizard"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

var runDoctor = func(ctx context.Context) []doctor.Result {
	return doctor.DefaultRunner().Run(ctx)
}

var newPaths = paths.New
var openDB = db.Open
var readDaemonStatus = func(pidFile string) (daemon.Status, error) {
	return daemon.ReadStatus(pidFile, nil)
}
var startDaemon = func(pidFile string, useMock bool) error {
	return daemon.Start(pidFile, nil, func() error {
		return launchDaemonProcess(pidFile, useMock)
	})
}
var stopDaemon = func(pidFile string) error {
	return daemon.Stop(pidFile, nil, nil)
}
var installDaemonService = daemon.InstallService
var uninstallDaemonService = daemon.UninstallService
var startDaemonService = daemon.StartService
var stopDaemonService = daemon.StopService
var restartDaemonService = daemon.RestartService
var daemonServiceInstalled = daemon.ServiceInstalled
var runDaemonWithOptions = daemon.RunWithOptions
var installTimestampedLogPipe = daemon.InstallTimestampedLogPipe
var daemonReadyTimeout = 5 * time.Second
var daemonReadyPollInterval = 100 * time.Millisecond
var newGitHubClient = func() itemFetcher {
	return ghclient.New(nil)
}
var newRepoLister = func() repoLister {
	return ghclient.New(nil)
}
var detectCurrentRepo = wizard.DetectCurrentRepo
var runInitWizard = wizard.Run
var initWizardEnabled = func(cmd *cobra.Command) bool {
	stdout, ok := cmd.OutOrStdout().(*os.File)
	if !ok || stdout == nil {
		return false
	}
	return isatty.IsTerminal(stdout.Fd()) && isatty.IsTerminal(os.Stdin.Fd())
}
var newDaemonTriageLister = func() daemonTriageLister {
	return ghclient.New(nil)
}
var lookPath = exec.LookPath
var newAgent = func(name sharedtypes.AgentName, bin string) (triageAgent, error) {
	return agent.New(name, bin)
}
var prepareInvestigationCheckout = func(ctx context.Context, root string, repoID string) (string, error) {
	return preparePersistentInvestigationCheckout(ctx, root, repoID, runGitCommand, runGhCommand)
}
var newLabelEditor = func() labelEditor {
	return ghclient.New(nil)
}
var newApprovalExecutor = func() approvalExecutor {
	return ghclient.New(nil)
}
var loadGlobalConfig = config.LoadGlobal
var prepareFixWorktree = fixflow.PrepareWorktree
var prepareContribWorktree = fixflow.PrepareContribWorktree
var resolvePRCreator = prcreator.Resolve
var createFixPR = prcreator.Create
var runFixGitCommand = runGitCommand
var newDraftEditor = func() draftEditor {
	return envDraftEditor{}
}
var copyTextToClipboard = copyTextWithSystemClipboard
var openURLInBrowser = openURLWithSystemBrowser
var runTUI = openInboxTUI
var runTUIWithActions = tui.RunWithActions
var ipcSubscribe = ipc.Subscribe
var sleep = time.Sleep
var dialDaemonIPC = func(socketPath string) (daemonIPCClient, error) {
	return ipc.Dial(socketPath)
}
var currentWorkingDir = os.Getwd
var applyShellEnv = shellenv.ApplyToProcess
var closeTelemetry = func() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = telemetry.Close(ctx)
}
var registerRootFinalizers sync.Once
var runUpdate = func(ctx context.Context, stdout, stderr io.Writer, opts update.Options) error {
	return update.Run(ctx, stdout, stderr, opts)
}
var startBackgroundUpdateCheck = func() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = update.RunCheck(ctx, io.Discard, update.CheckOptions{Stderr: io.Discard})
	}()
}
var isInteractiveTerminal = func() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		info, err := f.Stat()
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func explainDatabaseLock(err error) error {
	if err == nil {
		return nil
	}

	if !isDatabaseLockError(err) {
		return err
	}

	return errors.New("database is locked; another ezoss process may be using the database, try again in a moment")
}

func isDatabaseLockError(err error) bool {
	if err == nil {
		return false
	}

	return strings.Contains(strings.ToLower(err.Error()), "database is locked")
}

func openDBWithRetry(path string) (*db.DB, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		database, err := openDB(path)
		if err == nil {
			return database, nil
		}
		lastErr = err
		if !isDatabaseLockError(err) || attempt == 1 {
			break
		}
		sleep(100 * time.Millisecond)
	}

	return nil, lastErr
}

const ezossTriagedLabel = "ezoss/triaged"

type itemFetcher interface {
	GetItem(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int) (ghclient.Item, error)
}

type daemonTriageLister interface {
	ListNeedingTriage(ctx context.Context, repo string) ([]ghclient.Item, error)
	ListTriaged(ctx context.Context, repo string, sinceUpdated time.Time) ([]ghclient.Item, error)
	SearchAuthoredOpenPRs(ctx context.Context) ([]ghclient.Item, error)
	SearchAuthoredOpenIssues(ctx context.Context) ([]ghclient.Item, error)
	ListOwnedRepos(ctx context.Context, visibility ghclient.RepoVisibility) ([]string, error)
}

type triageAgent interface {
	Name() string
	Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error)
	Close() error
}

type labelEditor interface {
	EditLabels(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int, add []string, remove []string) error
}

type approvalExecutor interface {
	EditLabels(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int, add []string, remove []string) error
	Comment(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int, body string) error
	Close(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int, comment string) error
	RequestChanges(ctx context.Context, repo string, number int, body string) error
	// Merge returns the method actually used. If the repo doesn't allow
	// the requested method, the implementation falls back to whichever
	// method the repo accepts; callers should compare the returned method
	// against the requested one and surface a notice when they differ.
	Merge(ctx context.Context, repo string, number int, method string) (string, error)
}

type repoLister interface {
	ListOwnedRepos(ctx context.Context, visibility ghclient.RepoVisibility) ([]string, error)
	ListStarredRepos(ctx context.Context) ([]string, error)
}

type daemonIPCClient interface {
	Call(method string, params interface{}, result interface{}) error
	Close() error
}

// draftEditor splits the editor invocation into a Prepare phase that returns
// the *exec.Cmd for bubbletea's tea.ExecProcess to run with the terminal
// released, and a finish callback that reads the result back and cleans up
// after the editor exits. Returning the cmd up-front (instead of invoking
// it inline) is what keeps the alt screen from being clobbered.
type draftEditor interface {
	Prepare(ctx context.Context, initial string) (cmd *exec.Cmd, finish func(execErr error) (string, error), err error)
}

const editedRecommendationDraftHeader = "Draft response:\n"

func NewRootCmd() *cobra.Command {
	registerRootFinalizers.Do(func() {
		cobra.OnFinalize(func() {
			closeTelemetry()
		})
	})

	cmd := &cobra.Command{
		Use:           "ezoss",
		Short:         "Maintainer-side issue and PR triage orchestrator",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if shouldSkipShellEnvSetup(cmd) {
				return nil
			}
			return applyShellEnv()
		},
	}

	cmd.Version = buildinfo.String()
	cmd.SetVersionTemplate("{{printf \"%s %s\\n\" .Name .Version}}")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		telemetry.Track("command", telemetry.Fields{"command": "inbox", "entrypoint": "root"})
		if !isInteractiveTerminal() {
			return renderPendingRecommendations(cmd.OutOrStdout(), true)
		}
		entries, err := loadInboxEntries()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			p, perr := newPaths()
			if perr == nil {
				status, derr := readDaemonStatus(p.PIDPath())
				if derr == nil && status.State != daemon.StateRunning {
					return errors.New("daemon is not running and inbox is empty; run `ezoss daemon start` to begin triage")
				}
			}
		}
		startBackgroundUpdateCheck()
		return runTUI(entries)
	}

	cmd.AddCommand(newDoctorCmd())
	cmd.AddCommand(newDaemonCmd())
	cmd.AddCommand(newFixCmd())
	cmd.AddCommand(newInitCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newStatusCmd())
	cmd.AddCommand(newTriageCmd())
	cmd.AddCommand(newUpdateCmd())

	return cmd
}

func shouldSkipShellEnvSetup(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}

	switch cmd.Name() {
	case "doctor", "help":
		return true
	default:
		return false
	}
}

func newFixCmd() *cobra.Command {
	var prCreate string
	var prepareOnly bool
	cmd := &cobra.Command{
		Use:   "fix <owner/repo#number>",
		Short: "Run a coding agent in an isolated worktree for a fix prompt",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoID, number, err := parseFixTarget(args[0])
			if err != nil {
				return err
			}
			p, err := newPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			cfg, err := loadGlobalConfig(filepath.Join(p.Root(), "config.yaml"))
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			mode := prcreator.Mode(cfg.Fixes.PRCreate)
			if strings.TrimSpace(prCreate) != "" {
				mode = prcreator.Mode(prCreate)
			}
			var resolution prcreator.Resolution
			var entry tui.Entry
			if !prepareOnly {
				entry, err = loadFixEntry(repoID, number)
				if err != nil {
					return err
				}
				if entry.Role == sharedtypes.RoleContributor && contributorPushMode(cfg) == config.ContribPushDisabled {
					return fmt.Errorf("contrib push disabled: refusing to run contributor fix for %s#%d", repoID, number)
				}
			} else {
				resolution, err = resolvePRCreator(mode, lookPath)
				if err != nil {
					return err
				}
			}
			var worktree fixflow.Worktree
			var contributor bool
			if prepareOnly {
				worktree, err = prepareFixWorktree(cmd.Context(), fixflow.WorktreeOptions{Root: p.Root(), RepoID: repoID, Number: number})
			} else {
				worktree, contributor, err = prepareFixWorktreeForEntry(cmd.Context(), p.Root(), p.DBPath(), entry)
			}
			if err != nil {
				return fmt.Errorf("prepare fix worktree: %w", err)
			}
			if !prepareOnly && !contributor {
				resolution, err = resolvePRCreator(mode, lookPath)
				if err != nil {
					return err
				}
			}

			w := cmd.OutOrStdout()
			fmt.Fprintln(w, "fix:")
			fmt.Fprintf(w, "  repo: %s\n", repoID)
			fmt.Fprintf(w, "  number: %d\n", number)
			fmt.Fprintf(w, "  base_checkout: %s\n", worktree.BasePath)
			fmt.Fprintf(w, "  worktree: %s\n", worktree.WorktreePath)
			fmt.Fprintf(w, "  branch: %s\n", worktree.Branch)
			fmt.Fprintf(w, "  base_ref: %s\n", worktree.BaseRef)
			if resolution.Mode != "" {
				fmt.Fprintf(w, "  pr_create: %s\n", resolution.Mode)
				if resolution.Binary != "" {
					fmt.Fprintf(w, "  pr_create_binary: %s\n", resolution.Binary)
				}
				for _, skipped := range resolution.Skipped {
					fmt.Fprintf(w, "  skipped_%s: %s\n", skipped.Mode, skipped.Reason)
				}
			}
			if prepareOnly {
				telemetry.Track("command", telemetry.Fields{"command": "fix", "entrypoint": "fix", "pr_create": string(resolution.Mode), "prepare_only": true})
				return nil
			}
			result, err := runFixEntryWithPrepared(cmd.Context(), entry, w, fixRunPrepared{
				Root:        p.Root(),
				Config:      cfg,
				Worktree:    worktree,
				PRCreate:    resolution,
				Contributor: contributor,
			})
			if err != nil {
				return err
			}
			if result.PRURL != "" {
				fmt.Fprintf(w, "  pr_url: %s\n", result.PRURL)
			}

			telemetry.Track("command", telemetry.Fields{"command": "fix", "entrypoint": "fix", "pr_create": string(resolution.Mode)})
			return nil
		},
	}
	cmd.Flags().StringVar(&prCreate, "pr-create", "", "PR creation mode override (auto, no-mistakes, gh, disabled)")
	cmd.Flags().BoolVar(&prepareOnly, "prepare-only", false, "Only prepare the isolated worktree without running the coding agent")
	return cmd
}

func parseFixTarget(value string) (string, int, error) {
	trimmed := strings.TrimSpace(value)
	repoPart, numberPart, ok := strings.Cut(trimmed, "#")
	if !ok {
		return "", 0, fmt.Errorf("invalid fix target %q: use owner/repo#number", value)
	}
	repoID := strings.TrimSpace(repoPart)
	if repoID == "" || !strings.Contains(repoID, "/") {
		return "", 0, fmt.Errorf("invalid fix target %q: use owner/repo#number", value)
	}
	number, err := strconv.Atoi(strings.TrimSpace(numberPart))
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("invalid fix target %q: issue number must be positive", value)
	}
	return repoID, number, nil
}

type fixRunPrepared struct {
	Root        string
	Config      *config.GlobalConfig
	Worktree    fixflow.Worktree
	PRCreate    prcreator.Resolution
	Contributor bool
}

type fixRunResult struct {
	WorktreePath string
	Branch       string
	PRURL        string
}

func loadFixEntry(repoID string, number int) (tui.Entry, error) {
	entries, err := loadInboxEntries()
	if err != nil {
		return tui.Entry{}, err
	}
	for _, entry := range entries {
		if entry.RepoID == repoID && entry.Number == number {
			if strings.TrimSpace(entry.FixPrompt) == "" {
				return tui.Entry{}, fmt.Errorf("%s#%d has no fix prompt; rerun triage or choose a fix_required option first", repoID, number)
			}
			return entry, nil
		}
	}
	return tui.Entry{}, fmt.Errorf("no active recommendation found for %s#%d", repoID, number)
}

func runFixEntry(ctx context.Context, entry tui.Entry, out io.Writer) (*fixRunResult, error) {
	p, err := newPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	cfg, err := loadGlobalConfig(filepath.Join(p.Root(), "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	worktree, contributor, err := prepareFixWorktreeForEntry(ctx, p.Root(), p.DBPath(), entry)
	if err != nil {
		return nil, fmt.Errorf("prepare fix worktree: %w", err)
	}
	var resolution prcreator.Resolution
	if !contributor {
		resolution, err = resolvePRCreator(prcreator.Mode(cfg.Fixes.PRCreate), lookPath)
		if err != nil {
			return nil, err
		}
	}
	return runFixEntryWithPrepared(ctx, entry, out, fixRunPrepared{
		Root:        p.Root(),
		Config:      cfg,
		Worktree:    worktree,
		PRCreate:    resolution,
		Contributor: contributor,
	})
}

func prepareFixWorktreeForEntry(ctx context.Context, root string, dbPath string, entry tui.Entry) (fixflow.Worktree, bool, error) {
	if entry.Role == sharedtypes.RoleContributor {
		item, err := loadFixItem(dbPath, entry)
		if err != nil {
			return fixflow.Worktree{}, false, err
		}
		if item == nil {
			return fixflow.Worktree{}, false, fmt.Errorf("item %s#%d not found", entry.RepoID, entry.Number)
		}
		if strings.TrimSpace(item.HeadRef) == "" || strings.TrimSpace(item.HeadCloneURL) == "" {
			return fixflow.Worktree{}, false, fmt.Errorf("contrib fix requires head ref and clone URL on item %s", item.ID)
		}
		worktree, err := prepareContribWorktree(ctx, fixflow.ContribWorktreeOptions{
			Root: root, HeadRepo: item.HeadRepo, HeadRef: item.HeadRef, CloneURL: item.HeadCloneURL, Number: item.Number,
		})
		return worktree, true, err
	}
	worktree, err := prepareFixWorktree(ctx, fixflow.WorktreeOptions{Root: root, RepoID: entry.RepoID, Number: entry.Number})
	return worktree, false, err
}

func loadFixItem(dbPath string, entry tui.Entry) (*db.Item, error) {
	database, err := openDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer database.Close()
	return inboxItem(database, entry)
}

func runFixEntryWithPrepared(ctx context.Context, entry tui.Entry, out io.Writer, prepared fixRunPrepared) (*fixRunResult, error) {
	if out == nil {
		out = io.Discard
	}
	if strings.TrimSpace(entry.FixPrompt) == "" {
		return nil, fmt.Errorf("%s#%d has no fix prompt", entry.RepoID, entry.Number)
	}
	if prepared.Contributor && contributorPushMode(prepared.Config) == config.ContribPushDisabled {
		return nil, fmt.Errorf("contrib push disabled: refusing to run contributor fix for %s#%d", entry.RepoID, entry.Number)
	}
	cfg, err := loadFixRunConfig(prepared.Config, prepared.Worktree.WorktreePath)
	if err != nil {
		return nil, err
	}
	agentName := cfg.Agent
	if agentName == "" {
		agentName = config.AgentAuto
	}
	resolvedAgent, bin, err := agent.Resolve(agentName, lookPath)
	if err != nil {
		return nil, err
	}
	agentRunner, err := newAgent(resolvedAgent, bin)
	if err != nil {
		return nil, err
	}
	defer agentRunner.Close()

	worktree := prepared.Worktree
	prompt := promptWithFixWorktree(entry.FixPrompt, worktree.WorktreePath)
	fmt.Fprintf(out, "  agent: %s\n", resolvedAgent)
	result, err := agentRunner.Run(ctx, agent.RunOpts{Prompt: prompt, CWD: worktree.WorktreePath})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("agent returned no result")
	}

	committed, err := commitFixWorktreeChanges(ctx, worktree.WorktreePath, fmt.Sprintf("fix: %s#%d", entry.RepoID, entry.Number))
	if err != nil {
		return nil, err
	}
	if committed {
		fmt.Fprintln(out, "  committed: true")
	} else {
		fmt.Fprintln(out, "  committed: false")
	}
	if err := ensureFixBranchHasCommit(ctx, worktree.WorktreePath, worktree.BaseRef); err != nil {
		return nil, err
	}
	if prepared.Contributor {
		headRef := baseBranch(worktree.BaseRef)
		pushMode := contributorPushMode(prepared.Config)
		if pushMode == config.ContribPushAuto {
			if _, err := runFixGitCommand(ctx, worktree.WorktreePath, nil, "push", "origin", "HEAD:"+headRef); err != nil {
				return nil, fmt.Errorf("push contrib branch %s: %w", headRef, err)
			}
		}
		return &fixRunResult{WorktreePath: worktree.WorktreePath, Branch: headRef}, nil
	}

	created, _, err := createFixPRWithFallback(ctx, prepared.PRCreate, prcreator.CreateOptions{
		RepoID:       entry.RepoID,
		WorktreePath: worktree.WorktreePath,
		Base:         baseBranch(worktree.BaseRef),
		Head:         worktree.Branch,
		Title:        fixPRTitle(entry),
		Body:         fixPRBody(entry),
		Draft:        true,
	})
	if err != nil {
		return nil, err
	}
	return &fixRunResult{WorktreePath: worktree.WorktreePath, Branch: worktree.Branch, PRURL: created.URL}, nil
}

func contributorPushMode(cfg *config.GlobalConfig) config.ContribPushMode {
	if cfg != nil && cfg.Fixes.ContribPush != "" {
		return cfg.Fixes.ContribPush
	}
	return config.ContribPushNoMistakes
}

func promptWithFixWorktree(prompt string, checkout string) string {
	return strings.TrimSpace(prompt) + "\n\nRepository checkout for fixing:\n" + checkout + "\n\nThis checkout is an isolated ezoss fix worktree. Implement the smallest correct fix and add or update regression tests when appropriate. Do not open the pull request yourself; ezoss will handle PR creation according to configuration after the fix run."
}

func loadFixRunConfig(globalCfg *config.GlobalConfig, worktreePath string) (*config.Config, error) {
	repoCfg, err := config.LoadRepo(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("load repo config: %w", err)
	}
	return config.Merge(globalCfg, repoCfg), nil
}

func commitFixWorktreeChanges(ctx context.Context, worktreePath string, message string) (bool, error) {
	status, err := runFixGitCommand(ctx, worktreePath, nil, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(status)) == "" {
		return false, nil
	}
	if _, err := runFixGitCommand(ctx, worktreePath, nil, "add", "-A"); err != nil {
		return false, err
	}
	if _, err := runFixGitCommand(ctx, worktreePath, nil, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

func ensureFixBranchHasCommit(ctx context.Context, worktreePath string, baseRef string) error {
	baseRef = strings.TrimSpace(baseRef)
	if baseRef == "" || baseRef == "HEAD" {
		baseRef = "main"
	}
	out, err := runFixGitCommand(ctx, worktreePath, nil, "rev-list", "--count", baseRef+"..HEAD")
	if err != nil {
		return fmt.Errorf("check fix commits: %w", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("check fix commits: parse rev-list count: %w", err)
	}
	if count <= 0 {
		return errors.New("no fix changes produced; branch has no commits ahead of base")
	}
	return nil
}

func baseBranch(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "refs/remotes/")
	ref = strings.TrimPrefix(ref, "origin/")
	if ref == "" || ref == "HEAD" {
		return "main"
	}
	return ref
}

func fixPRTitle(entry tui.Entry) string {
	title := strings.TrimSpace(entry.Title)
	if title == "" {
		return fmt.Sprintf("Fix %s#%d", entry.RepoID, entry.Number)
	}
	return fmt.Sprintf("Fix %s#%d: %s", entry.RepoID, entry.Number, title)
}

func fixPRBody(entry tui.Entry) string {
	return strings.TrimSpace(fmt.Sprintf("Fixes #%d\n\nGenerated from ezoss recommendation %s.\n", entry.Number, entry.RecommendationID))
}

type cliFixRunner struct {
	root string
	cfg  *config.GlobalConfig
	db   *db.DB
}

func (r cliFixRunner) RunFix(ctx context.Context, job db.FixJob, progress func(db.FixJobUpdate) error) (*daemon.FixResult, error) {
	role, headRepo, headRef, headCloneURL := r.lookupContribInfo(job)
	if role == sharedtypes.RoleContributor {
		return r.runContribFix(ctx, job, progress, headRepo, headRef, headCloneURL)
	}
	entry := tui.Entry{RecommendationID: job.RecommendationID, OptionID: job.OptionID, RepoID: job.RepoID, Number: job.ItemNumber, Kind: job.ItemKind, Title: job.Title, FixPrompt: job.FixPrompt}
	if progress != nil {
		_ = progress(db.FixJobUpdate{Phase: db.FixJobPhasePreparingWorktree, Message: "preparing worktree"})
	}
	worktree, err := prepareFixWorktree(ctx, fixflow.WorktreeOptions{Root: r.root, RepoID: job.RepoID, Number: job.ItemNumber})
	if err != nil {
		return nil, fmt.Errorf("prepare fix worktree: %w", err)
	}
	resolution, err := resolvePRCreator(prcreator.Mode(job.PRCreate), lookPath)
	if err != nil {
		return nil, err
	}
	cfg, err := loadFixRunConfig(r.cfg, worktree.WorktreePath)
	if err != nil {
		return nil, err
	}
	agentName := cfg.Agent
	if agentName == "" {
		agentName = config.AgentAuto
	}
	resolvedAgent, bin, err := agent.Resolve(agentName, lookPath)
	if err != nil {
		return nil, err
	}
	if progress != nil {
		_ = progress(db.FixJobUpdate{Phase: db.FixJobPhaseRunningAgent, Agent: resolvedAgent, Branch: worktree.Branch, WorktreePath: worktree.WorktreePath, Message: "running agent"})
	}
	agentRunner, err := newAgent(resolvedAgent, bin)
	if err != nil {
		return nil, err
	}
	defer agentRunner.Close()
	result, err := agentRunner.Run(ctx, agent.RunOpts{Prompt: promptWithFixWorktree(job.FixPrompt, worktree.WorktreePath), CWD: worktree.WorktreePath})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("agent returned no result")
	}
	if progress != nil {
		_ = progress(db.FixJobUpdate{Phase: db.FixJobPhaseCommitting, Message: "committing changes"})
	}
	if _, err := commitFixWorktreeChanges(ctx, worktree.WorktreePath, fmt.Sprintf("fix: %s#%d", job.RepoID, job.ItemNumber)); err != nil {
		return nil, err
	}
	if err := ensureFixBranchHasCommit(ctx, worktree.WorktreePath, worktree.BaseRef); err != nil {
		return nil, err
	}
	if progress != nil {
		_ = progress(db.FixJobUpdate{Phase: db.FixJobPhasePushing, Message: "pushing branch"})
	}
	created, usedResolution, err := createFixPRWithFallback(ctx, resolution, prcreator.CreateOptions{RepoID: job.RepoID, WorktreePath: worktree.WorktreePath, Base: baseBranch(worktree.BaseRef), Head: worktree.Branch, Title: fixPRTitle(entry), Body: fixPRBody(entry), Draft: true, DetectAttempts: 1, DetectInterval: -1})
	if err != nil {
		if usedResolution.Mode == prcreator.ModeNoMistakes && prcreator.IsNoMistakesDetectionError(err) {
			return &daemon.FixResult{Branch: worktree.Branch, WorktreePath: worktree.WorktreePath, WaitingForPR: true}, nil
		}
		return nil, err
	}
	return &daemon.FixResult{Branch: worktree.Branch, WorktreePath: worktree.WorktreePath, PRURL: created.URL}, nil
}

func createFixPRWithFallback(ctx context.Context, resolution prcreator.Resolution, opts prcreator.CreateOptions) (prcreator.Created, prcreator.Resolution, error) {
	created, err := createFixPR(ctx, resolution.Mode, opts, nil)
	if err == nil || !shouldFallbackFromNoMistakes(resolution, err) {
		return created, resolution, err
	}
	ghResolution, resolveErr := resolvePRCreator(prcreator.ModeGH, lookPath)
	if resolveErr != nil {
		return prcreator.Created{}, resolution, fmt.Errorf("%w (also failed to resolve gh fallback: %v)", err, resolveErr)
	}
	created, err = createFixPR(ctx, ghResolution.Mode, opts, nil)
	return created, ghResolution, err
}

func shouldFallbackFromNoMistakes(resolution prcreator.Resolution, err error) bool {
	if err == nil || resolution.Requested != prcreator.ModeAuto || resolution.Mode != prcreator.ModeNoMistakes {
		return false
	}
	return !prcreator.IsNoMistakesDetectionError(err)
}

// lookupContribInfo reads the item from the local DB to learn whether
// the fix job is for a contributor PR (i.e. a PR ezoss did not open and
// where we can only push to the existing head branch). Returns role
// maintainer if the DB is unavailable or the item is missing - the
// runner then takes the legacy path that creates a new branch + PR
// against the upstream repo.
func (r cliFixRunner) lookupContribInfo(job db.FixJob) (sharedtypes.Role, string, string, string) {
	if r.db == nil {
		return sharedtypes.RoleMaintainer, "", "", ""
	}
	item, err := r.db.GetItem(job.ItemID)
	if err != nil || item == nil {
		return sharedtypes.RoleMaintainer, "", "", ""
	}
	role := item.Role
	if role == "" {
		role = sharedtypes.RoleMaintainer
	}
	return role, item.HeadRepo, item.HeadRef, item.HeadCloneURL
}

// runContribFix pushes commits produced by the agent to the existing
// PR branch on the head repo (typically a fork). It does not create a
// new PR - that already exists. The push is gated by
// fixes.contrib_push: auto pushes immediately, no-mistakes leaves the
// worktree intact and returns instructions, disabled fails before
// running the agent.
func (r cliFixRunner) runContribFix(ctx context.Context, job db.FixJob, progress func(db.FixJobUpdate) error, headRepo, headRef, headCloneURL string) (*daemon.FixResult, error) {
	pushMode := config.ContribPushNoMistakes
	if r.cfg != nil && r.cfg.Fixes.ContribPush != "" {
		pushMode = r.cfg.Fixes.ContribPush
	}
	if pushMode == config.ContribPushDisabled {
		return nil, fmt.Errorf("contrib push disabled: refusing to run contributor fix job for %s#%d (set fixes.contrib_push to auto or no-mistakes to enable)", job.RepoID, job.ItemNumber)
	}
	if strings.TrimSpace(headRef) == "" || strings.TrimSpace(headCloneURL) == "" {
		return nil, fmt.Errorf("contrib fix requires head ref and clone URL on item %s (PR head metadata missing - rerun the contributor sweep)", job.ItemID)
	}
	if progress != nil {
		_ = progress(db.FixJobUpdate{Phase: db.FixJobPhasePreparingWorktree, Message: "preparing contributor worktree"})
	}
	worktree, err := prepareContribWorktree(ctx, fixflow.ContribWorktreeOptions{
		Root: r.root, HeadRepo: headRepo, HeadRef: headRef, CloneURL: headCloneURL, Number: job.ItemNumber,
	})
	if err != nil {
		return nil, fmt.Errorf("prepare contrib worktree: %w", err)
	}
	cfg, err := loadFixRunConfig(r.cfg, worktree.WorktreePath)
	if err != nil {
		return nil, err
	}
	agentName := cfg.Agent
	if agentName == "" {
		agentName = config.AgentAuto
	}
	resolvedAgent, bin, err := agent.Resolve(agentName, lookPath)
	if err != nil {
		return nil, err
	}
	if progress != nil {
		_ = progress(db.FixJobUpdate{Phase: db.FixJobPhaseRunningAgent, Agent: resolvedAgent, Branch: worktree.Branch, WorktreePath: worktree.WorktreePath, Message: "running agent (contributor)"})
	}
	agentRunner, err := newAgent(resolvedAgent, bin)
	if err != nil {
		return nil, err
	}
	defer agentRunner.Close()
	result, err := agentRunner.Run(ctx, agent.RunOpts{Prompt: promptWithFixWorktree(job.FixPrompt, worktree.WorktreePath), CWD: worktree.WorktreePath})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("agent returned no result")
	}
	if progress != nil {
		_ = progress(db.FixJobUpdate{Phase: db.FixJobPhaseCommitting, Message: "committing changes"})
	}
	if _, err := commitFixWorktreeChanges(ctx, worktree.WorktreePath, fmt.Sprintf("fix: %s#%d", job.RepoID, job.ItemNumber)); err != nil {
		return nil, err
	}
	if err := ensureFixBranchHasCommit(ctx, worktree.WorktreePath, worktree.BaseRef); err != nil {
		return nil, err
	}
	if pushMode == config.ContribPushNoMistakes {
		return &daemon.FixResult{
			Branch:       headRef,
			WorktreePath: worktree.WorktreePath,
			WaitingForPR: false,
		}, nil
	}
	if progress != nil {
		_ = progress(db.FixJobUpdate{Phase: db.FixJobPhasePushing, Message: "pushing branch to PR head"})
	}
	if _, err := runFixGitCommand(ctx, worktree.WorktreePath, nil, "push", "origin", "HEAD:"+headRef); err != nil {
		return nil, fmt.Errorf("push contrib branch %s: %w", headRef, err)
	}
	return &daemon.FixResult{
		Branch:       headRef,
		WorktreePath: worktree.WorktreePath,
	}, nil
}

func (r cliFixRunner) DetectPR(ctx context.Context, job db.FixJob) (string, error) {
	args := []string{"pr", "list", "--head", job.Branch, "--state", "all", "--json", "url", "--jq", ".[0].url"}
	if job.RepoID != "" {
		args = append(args, "--repo", job.RepoID)
	}
	out, err := runGhCommand(ctx, job.WorktreePath, args...)
	if err != nil {
		return "", err
	}
	url := strings.TrimSpace(string(out))
	if url == "null" {
		return "", nil
	}
	return url, nil
}

func newUpdateCmd() *cobra.Command {
	var beta bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update ezoss to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := runUpdate(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), update.Options{Beta: beta}); err != nil {
				return err
			}
			telemetry.Track("command", telemetry.Fields{"command": "update", "entrypoint": "update", "beta": beta})
			return nil
		},
	}
	cmd.Flags().BoolVar(&beta, "beta", false, "Include prereleases when picking the target version")
	return cmd
}

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the ezoss daemon",
	}
	cmd.AddCommand(newDaemonRunCmd())
	cmd.AddCommand(newDaemonStartCmd())
	cmd.AddCommand(newDaemonStopCmd())
	cmd.AddCommand(newDaemonRestartCmd())
	cmd.AddCommand(newDaemonInstallCmd())
	cmd.AddCommand(newDaemonUninstallCmd())
	return cmd
}

func newDaemonRunCmd() *cobra.Command {
	var useMock bool

	cmd := &cobra.Command{
		Use:    "run",
		Short:  "Run the ezoss daemon in the foreground",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			if err := p.EnsureDirs(); err != nil {
				return fmt.Errorf("create state directories: %w", err)
			}

			logSink, err := os.OpenFile(filepath.Join(p.LogsDir(), "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return fmt.Errorf("open daemon log: %w", err)
			}
			defer logSink.Close()
			cleanupLog, err := installTimestampedLogPipe(logSink)
			if err != nil {
				return fmt.Errorf("install log pipe: %w", err)
			}
			defer cleanupLog()

			cfg, err := loadGlobalConfig(filepath.Join(p.Root(), "config.yaml"))
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			database, err := openDB(p.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", explainDatabaseLock(err))
			}
			defer database.Close()

			daemonLogger := daemon.NewLogger(os.Stderr)
			if applied := database.MigrationsApplied(); len(applied) > 0 {
				daemonLogger.Info("schema upgraded", "migrations", applied)
			}

			syncState := daemon.NewSyncState(cfg.PollInterval)
			poller := daemon.Poller{
				DB:                 database,
				GitHub:             newDaemonTriageLister(),
				StaleThreshold:     cfg.StaleThreshold,
				IgnoreOlderThan:    cfg.IgnoreOlderThan,
				Hooks:              syncState.Hooks(),
				Logger:             daemonLogger,
				ContribEnabled:     cfg.Contrib.Enabled,
				ContribIgnoreRepos: append([]string(nil), cfg.Contrib.IgnoreRepos...),
			}
			if useMock {
				poller = daemon.Poller{
					DB:                 database,
					GitHub:             ghmock.New(),
					Triage:             mockTriageRunner{},
					AgentsInstructions: readAgentsInstructions(p.Root()),
					StaleThreshold:     cfg.StaleThreshold,
					IgnoreOlderThan:    cfg.IgnoreOlderThan,
					Hooks:              syncState.Hooks(),
					Logger:             daemonLogger,
					ContribEnabled:     cfg.Contrib.Enabled,
					ContribIgnoreRepos: append([]string(nil), cfg.Contrib.IgnoreRepos...),
				}
			} else {
				triageCfg, err := loadDaemonTriageConfig(p.Root(), cfg)
				if err != nil {
					return fmt.Errorf("load daemon triage config: %w", err)
				}
				runner, err := newLiveTriageRunner(p.Root(), triageCfg.Agent)
				if err != nil {
					return fmt.Errorf("create triage runner: %w", err)
				}
				poller.Triage = runner
				poller.Fix = cliFixRunner{root: p.Root(), cfg: cfg, db: database}
				poller.AgentsInstructions = readAgentsInstructions(p.Root())
			}

			fixStart := func(ctx context.Context, params ipc.FixStartParams) (ipc.FixStartResult, error) {
				return createFixJobFromIPC(ctx, database, cfg, params)
			}
			if useMock {
				fixStart = func(context.Context, ipc.FixStartParams) (ipc.FixStartResult, error) {
					return ipc.FixStartResult{}, fmt.Errorf("fix is unavailable in mock mode")
				}
			}

			if err := runDaemonWithOptions(p.PIDPath(), nil, daemon.RunOptions{
				Repos:           append([]string(nil), cfg.Repos...),
				PollInterval:    cfg.PollInterval,
				StaleThreshold:  cfg.StaleThreshold,
				IgnoreOlderThan: cfg.IgnoreOlderThan,
				IPCPath:         p.IPCPath(),
				SyncState:       syncState,
				Logger:          daemonLogger,
				StartupAttrs: []any{
					"version", buildinfo.Version,
					"mock", useMock,
				},
				RecommendationSnapshot: database.ListActiveRecommendations,
				PollOnce: func(ctx context.Context, repos []string) error {
					return daemon.PollOnce(ctx, poller, repos)
				},
				FixStart:       fixStart,
				FixJobSnapshot: database.ListFixJobs,
				NewTicker: func(interval time.Duration) daemon.Ticker {
					return daemon.NewTicker(interval)
				},
			}); err != nil {
				return fmt.Errorf("run daemon: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&useMock, "mock", false, "Use canned GitHub items and recommendations")
	return cmd
}

func newDaemonStartCmd() *cobra.Command {
	var useMock bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the ezoss daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			if err := p.EnsureDirs(); err != nil {
				return fmt.Errorf("create state directories: %w", err)
			}

			if !useMock {
				cfg, err := loadGlobalConfig(filepath.Join(p.Root(), "config.yaml"))
				if err != nil {
					return fmt.Errorf("load config: %w", err)
				}
				if cfg == nil || len(cfg.Repos) == 0 {
					fmt.Fprintln(cmd.ErrOrStderr(), "warning: no repos configured; daemon will run idle.")
					fmt.Fprintln(cmd.ErrOrStderr(), "hint: ezoss init --repo owner/name")
				}
			}

			if !useMock && daemonServiceInstalled(p) {
				if managed, err := startDaemonService(p); err != nil {
					return fmt.Errorf("start daemon: %w", err)
				} else if managed {
					if err := waitForDaemonReady(p.PIDPath()); err != nil {
						return fmt.Errorf("start daemon: %w", err)
					}
					telemetry.Track("command", telemetry.Fields{"command": "daemon_start", "entrypoint": "daemon.start"})
					_, err = fmt.Fprintln(cmd.OutOrStdout(), "started")
					return err
				}
			}

			if err := startDaemon(p.PIDPath(), useMock); err != nil {
				return fmt.Errorf("start daemon: %w", err)
			}
			telemetry.Track("command", telemetry.Fields{"command": "daemon_start", "entrypoint": "daemon.start"})

			_, err = fmt.Fprintln(cmd.OutOrStdout(), "started")
			return err
		},
	}
	cmd.Flags().BoolVar(&useMock, "mock", false, "Use canned GitHub items and recommendations")
	return cmd
}

func newDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the ezoss daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}

			if managed, err := stopDaemonService(p); err != nil {
				return fmt.Errorf("stop daemon: %w", err)
			} else if !managed {
				if err := stopDaemon(p.PIDPath()); err != nil {
					return fmt.Errorf("stop daemon: %w", err)
				}
			}
			telemetry.Track("command", telemetry.Fields{"command": "daemon_stop", "entrypoint": "daemon.stop"})

			_, err = fmt.Fprintln(cmd.OutOrStdout(), daemon.StateStopped)
			return err
		},
	}
}

func newDaemonRestartCmd() *cobra.Command {
	var useMock bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the ezoss daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			if err := p.EnsureDirs(); err != nil {
				return fmt.Errorf("create state directories: %w", err)
			}

			if !useMock && daemonServiceInstalled(p) {
				if managed, err := restartDaemonService(p); err != nil {
					return fmt.Errorf("restart daemon: %w", err)
				} else if managed {
					if err := waitForDaemonReady(p.PIDPath()); err != nil {
						return fmt.Errorf("restart daemon: %w", err)
					}
					telemetry.Track("command", telemetry.Fields{"command": "daemon_restart", "entrypoint": "daemon.restart"})
					_, err = fmt.Fprintln(cmd.OutOrStdout(), "restarted")
					return err
				}
			}

			if err := stopDaemon(p.PIDPath()); err != nil {
				return fmt.Errorf("restart daemon: stop: %w", err)
			}
			if err := startDaemon(p.PIDPath(), useMock); err != nil {
				return fmt.Errorf("restart daemon: start: %w", err)
			}
			telemetry.Track("command", telemetry.Fields{"command": "daemon_restart", "entrypoint": "daemon.restart"})
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "restarted")
			return err
		},
	}
	cmd.Flags().BoolVar(&useMock, "mock", false, "Use canned GitHub items and recommendations")
	return cmd
}

func waitForDaemonReady(pidFile string) error {
	deadline := time.Now().Add(daemonReadyTimeout)
	var lastState string
	var lastErr error
	for {
		status, err := readDaemonStatus(pidFile)
		if err == nil && status.State == daemon.StateRunning {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastState = status.State
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("daemon did not report running: %w", lastErr)
			}
			return fmt.Errorf("daemon did not report running: %s", lastState)
		}
		time.Sleep(daemonReadyPollInterval)
	}
}

func newDaemonInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register the ezoss daemon with the OS service manager (launchd/systemd/schtasks)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			if err := p.EnsureDirs(); err != nil {
				return fmt.Errorf("create state directories: %w", err)
			}

			managed, err := installDaemonService(p)
			if err != nil {
				return fmt.Errorf("install daemon service: %w", err)
			}
			telemetry.Track("command", telemetry.Fields{"command": "daemon_install", "entrypoint": "daemon.install"})

			if !managed {
				_, err = fmt.Fprintln(cmd.ErrOrStderr(), "managed-service install skipped (unsupported platform or test mode)")
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "installed")
			return err
		},
	}
}

func newDaemonUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the ezoss daemon registration from the OS service manager",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}

			if err := uninstallDaemonService(p); err != nil {
				return fmt.Errorf("uninstall daemon service: %w", err)
			}
			telemetry.Track("command", telemetry.Fields{"command": "daemon_uninstall", "entrypoint": "daemon.uninstall"})

			_, err = fmt.Fprintln(cmd.OutOrStdout(), "uninstalled")
			return err
		},
	}
}

func Execute() error {
	return NewRootCmd().Execute()
}

func loadInboxEntries() ([]tui.Entry, error) {
	p, err := newPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	if err := p.EnsureDirs(); err != nil {
		return nil, fmt.Errorf("create state directories: %w", err)
	}

	database, err := openDB(p.DBPath())
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	cfg, err := loadGlobalConfig(filepath.Join(p.Root(), "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	configuredRepos := make(map[string]struct{}, len(cfg.Repos))
	for _, repoID := range cfg.Repos {
		configuredRepos[repoID] = struct{}{}
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		return nil, fmt.Errorf("list active recommendations: %w", err)
	}

	entries := make([]tui.Entry, 0, len(recommendations))
	for _, rec := range recommendations {
		item, err := database.GetItem(rec.ItemID)
		if err != nil {
			return nil, fmt.Errorf("get item %s: %w", rec.ItemID, err)
		}
		if item == nil {
			continue
		}
		_, isConfigured := configuredRepos[item.RepoID]
		totals, err := database.RecommendationTokenTotalsForItem(rec.ItemID)
		if err != nil {
			return nil, fmt.Errorf("recommendation token totals for %s: %w", rec.ItemID, err)
		}
		role := item.Role
		if role == "" {
			role = sharedtypes.RoleMaintainer
		}
		// A contributor item lives in a repo we don't maintain - it
		// shouldn't be flagged as unconfigured even if it isn't in
		// cfg.Repos.
		unconfigured := !isConfigured && role != sharedtypes.RoleContributor
		entry := tui.Entry{
			RecommendationID:  rec.ID,
			RepoID:            item.RepoID,
			Number:            item.Number,
			Kind:              item.Kind,
			Role:              role,
			Author:            item.Author,
			Unconfigured:      unconfigured,
			Title:             item.Title,
			URL:               githubItemURL(item.RepoID, item.Kind, item.Number),
			TokensIn:          totals.TokensIn,
			TokensOut:         totals.TokensOut,
			AgeLabel:          recommendationAgeLabel(rec.CreatedAt),
			ApprovalError:     latestApprovalError(database, rec.ID),
			CurrentWaitingOn:  item.WaitingOn,
			RerunInstructions: rec.RerunInstructions,
			Options:           buildEntryOptions(rec.Options),
		}
		if fixJob, err := database.LatestFixJobForItem(item.ID); err == nil && fixJob != nil {
			entry.FixJobID = fixJob.ID
			entry.FixStatus = string(fixJob.Status)
			entry.FixPhase = string(fixJob.Phase)
			entry.FixMessage = fixJob.Message
			entry.FixError = fixJob.Error
			entry.FixPRURL = fixJob.PRURL
			entry.FixWorktreePath = fixJob.WorktreePath
		}
		entry.SyncActive()
		entries = append(entries, entry)
	}
	return entries, nil
}

// buildEntryOptions converts persisted recommendation options into the
// editable, mirrored EntryOption shape the TUI works with.
func buildEntryOptions(options []db.RecommendationOption) []tui.EntryOption {
	out := make([]tui.EntryOption, 0, len(options))
	for _, opt := range options {
		out = append(out, tui.EntryOption{
			ID:                     opt.ID,
			StateChange:            opt.StateChange,
			OriginalStateChange:    opt.StateChange,
			ProposedLabels:         append([]string(nil), opt.ProposedLabels...),
			OriginalProposedLabels: append([]string(nil), opt.ProposedLabels...),
			Confidence:             opt.Confidence,
			Rationale:              opt.Rationale,
			DraftComment:           opt.DraftComment,
			FixPrompt:              opt.FixPrompt,
			OriginalDraftComment:   opt.DraftComment,
			Followups:              append([]string(nil), opt.Followups...),
			WaitingOn:              opt.WaitingOn,
		})
	}
	return out
}

func openInboxTUI(entries []tui.Entry) error {
	initialStatus := inboxInitialStatus()
	notify, cancel, err := subscribeInboxNotifications()
	actions := inboxModelActions(notify)
	actions.InitialStatus = initialStatus
	if err == nil && cancel != nil {
		defer cancel()
	} else if err != nil {
		actions.InitialStatus = appendStatusLine(actions.InitialStatus, fmt.Sprintf("live updates unavailable: %v", err))
	}
	return runTUIWithActions(entries, actions)
}

func inboxInitialStatus() string {
	p, err := newPaths()
	if err != nil {
		return ""
	}
	daemonStatus, err := readDaemonStatus(p.PIDPath())
	if err != nil {
		return ""
	}
	if daemonStatus.State != daemon.StateRunning {
		return "warning: daemon is not running; this inbox will not refresh until you run `ezoss daemon start`."
	}
	return ""
}

func appendStatusLine(status string, update string) string {
	status = strings.TrimSpace(status)
	update = strings.TrimSpace(update)
	if status == "" {
		return update
	}
	if update == "" {
		return status
	}
	return status + " | " + update
}

func inboxModelActions(notify <-chan struct{}) tui.ModelActions {
	return tui.ModelActions{
		Approve: func(selected []tui.Entry) error {
			return approveInboxEntries(context.Background(), selected)
		},
		Dismiss: func(selected []tui.Entry) error {
			return dismissInboxEntries(context.Background(), selected)
		},
		EditExec: func(entry tui.Entry) (*exec.Cmd, func(error) (tui.Entry, error), error) {
			return prepareInboxEntryEdit(context.Background(), entry)
		},
		Notify: notify,
		Rerun: func(selected []tui.Entry, instructions string) ([]tui.Entry, error) {
			return rerunInboxEntries(context.Background(), selected, instructions)
		},
		CopyPrompt: func(entry tui.Entry) error {
			return copyTextToClipboard(context.Background(), entry.FixPrompt)
		},
		Fix: func(entry tui.Entry) error {
			_, err := startDaemonFixJob(context.Background(), entry)
			return err
		},
		OpenURL: func(entry tui.Entry) error {
			return openURLInBrowser(context.Background(), entry.URL)
		},
		Reload: loadInboxEntries,
	}
}

func subscribeInboxNotifications() (<-chan struct{}, func(), error) {
	p, err := newPaths()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve paths: %w", err)
	}
	events, cancel, err := ipcSubscribe(p.IPCPath(), &ipc.SubscribeParams{})
	if err != nil {
		return nil, nil, err
	}
	notify := make(chan struct{}, 1)
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(notify)
		for {
			select {
			case <-stop:
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				if !isRecommendationEvent(event.Type) {
					continue
				}
				select {
				case notify <- struct{}{}:
				default:
				}
			}
		}
	}()
	return notify, func() {
		once.Do(func() {
			close(stop)
			cancel()
		})
	}, nil
}

func isRecommendationEvent(eventType ipc.EventType) bool {
	switch eventType {
	case ipc.EventRecommendationCreated, ipc.EventRecommendationUpdated, ipc.EventRecommendationRemoved, ipc.EventFixJobCreated, ipc.EventFixJobUpdated:
		return true
	default:
		return false
	}
}

func startDaemonFixJob(ctx context.Context, entry tui.Entry) (ipc.FixStartResult, error) {
	p, err := newPaths()
	if err != nil {
		return ipc.FixStartResult{}, fmt.Errorf("resolve paths: %w", err)
	}
	client, err := dialDaemonIPC(p.IPCPath())
	if err != nil {
		return ipc.FixStartResult{}, fmt.Errorf("connect daemon: %w", err)
	}
	defer client.Close()
	var result ipc.FixStartResult
	if err := client.Call(ipc.MethodFixStart, ipc.FixStartParams{RecommendationID: entry.RecommendationID, OptionID: entry.OptionID}, &result); err != nil {
		return ipc.FixStartResult{}, err
	}
	return result, nil
}

func createFixJobFromIPC(_ context.Context, database *db.DB, cfg *config.GlobalConfig, params ipc.FixStartParams) (ipc.FixStartResult, error) {
	rec, err := database.GetRecommendation(params.RecommendationID)
	if err != nil {
		return ipc.FixStartResult{}, err
	}
	if rec == nil {
		return ipc.FixStartResult{}, fmt.Errorf("recommendation %s not found", params.RecommendationID)
	}
	item, err := database.GetItem(rec.ItemID)
	if err != nil {
		return ipc.FixStartResult{}, err
	}
	if item == nil {
		return ipc.FixStartResult{}, fmt.Errorf("item %s not found", rec.ItemID)
	}
	var option *db.RecommendationOption
	for i := range rec.Options {
		if params.OptionID == "" || rec.Options[i].ID == params.OptionID {
			option = &rec.Options[i]
			break
		}
	}
	if option == nil {
		return ipc.FixStartResult{}, fmt.Errorf("option %s not found", params.OptionID)
	}
	if strings.TrimSpace(option.FixPrompt) == "" {
		return ipc.FixStartResult{}, fmt.Errorf("option has no fix prompt")
	}
	prCreate := config.PRCreateAuto
	agentName := config.AgentAuto
	if cfg != nil {
		prCreate = cfg.Fixes.PRCreate
		agentName = cfg.Agent
	}
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: item.ID, RecommendationID: rec.ID, OptionID: option.ID, RepoID: item.RepoID, ItemNumber: item.Number, ItemKind: item.Kind, Title: item.Title, FixPrompt: option.FixPrompt, Agent: agentName, PRCreate: string(prCreate)})
	if err != nil {
		return ipc.FixStartResult{}, err
	}
	return ipc.FixStartResult{JobID: job.ID, ItemID: job.ItemID, Status: string(job.Status)}, nil
}

func rerunInboxEntries(ctx context.Context, entries []tui.Entry, instructions string) ([]tui.Entry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	instructions = strings.TrimSpace(instructions)
	p, err := newPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	database, err := openDB(p.DBPath())
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	refreshed := make([]tui.Entry, 0, len(entries))
	for _, entry := range entries {
		// A manual rerun is a force-retriage: the user is explicitly
		// invalidating the active recommendation, so supersede it before
		// PollOnce so the agents-stage dedup query treats the item as
		// pending fresh investigation.
		if entry.RecommendationID != "" {
			if err := database.MarkRecommendationSuperseded(entry.RecommendationID, time.Now()); err != nil {
				return nil, fmt.Errorf("rerun item %s#%d: supersede prior recommendation: %w", entry.RepoID, entry.Number, err)
			}
		}
		poller, err := newManualTriagePoller(ctx, p.Root(), database, entry.RepoID, entry.Number, false)
		if err != nil {
			return nil, err
		}
		poller.RerunInstructions = instructions
		if err := daemon.PollOnce(ctx, poller, []string{entry.RepoID}); err != nil {
			return nil, fmt.Errorf("rerun item %s#%d: %w", entry.RepoID, entry.Number, err)
		}
		updated, err := loadInboxEntry(database, entry.RepoID, entry.Number)
		if err != nil {
			return nil, err
		}
		if updated == nil {
			return nil, fmt.Errorf("rerun item %s#%d: no active recommendation found", entry.RepoID, entry.Number)
		}
		refreshed = append(refreshed, *updated)
	}
	return refreshed, nil
}

func approveInboxEntries(ctx context.Context, entries []tui.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	p, err := newPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	database, err := openDB(p.DBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()
	cfg, err := loadGlobalConfig(filepath.Join(p.Root(), "config.yaml"))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	executor := newApprovalExecutor()
	for _, entry := range entries {
		item, err := inboxItem(database, entry)
		if err != nil {
			return err
		}
		finalLabels, removeLabels := approvalLabelEdits(entry, item, cfg.SyncLabels)
		if err := executeApproval(ctx, executor, entry, finalLabels, removeLabels, cfg.MergeMethod); err != nil {
			if persistErr := persistFailedApprovalAttempt(database, entry, finalLabels, time.Now(), err); persistErr != nil {
				return fmt.Errorf("%v (also failed to record approval attempt: %w)", err, persistErr)
			}
			return err
		}
		actedAt := time.Now()
		if err := persistApprovedEntry(database, entry, finalLabels, actedAt); err != nil {
			return err
		}
		if err := markInboxItemTriaged(database, entry); err != nil {
			return err
		}
	}
	return nil
}

func persistApprovedEntry(database *db.DB, entry tui.Entry, finalLabels []string, actedAt time.Time) error {
	optionID, err := resolveOptionID(database, entry)
	if err != nil {
		return err
	}
	if entry.Edited() {
		if _, err := database.EditOption(optionID, entry.DraftComment, finalLabels, entry.StateChange, actedAt); err != nil {
			return fmt.Errorf("edit option %s: %w", optionID, err)
		}
		return nil
	}
	if _, err := database.ApproveOption(optionID, entry.DraftComment, finalLabels, entry.StateChange, actedAt); err != nil {
		return fmt.Errorf("approve option %s: %w", optionID, err)
	}
	return nil
}

// executeApproval applies the recommendation's actions in safety order:
// labels first (idempotent and easy to retry on failure), then the
// destructive action. This way a label-edit failure (e.g. a proposed label
// doesn't exist in the repo, and gh's --add-label is atomic across the batch)
// aborts before we merge or close anything - leaving the user free to fix
// the labels and retry. Comment posting goes with the state change since
// they're combined into single calls for close and request_changes.
func executeApproval(ctx context.Context, executor approvalExecutor, entry tui.Entry, addLabels []string, removeLabels []string, mergeMethod string) error {
	if entry.Role == sharedtypes.RoleContributor {
		switch entry.StateChange {
		case sharedtypes.StateChangeNone, sharedtypes.StateChangeClose, sharedtypes.StateChangeFixRequired, "":
		default:
			return fmt.Errorf("approve %s#%d: contributor entries do not support state_change %q", entry.RepoID, entry.Number, entry.StateChange)
		}
	}

	if len(addLabels) > 0 || len(removeLabels) > 0 {
		if err := executor.EditLabels(ctx, entry.RepoID, entry.Kind, entry.Number, addLabels, removeLabels); err != nil {
			return fmt.Errorf("mark %s#%d triaged: %w", entry.RepoID, entry.Number, err)
		}
	}

	hasComment := strings.TrimSpace(entry.DraftComment) != ""
	switch entry.StateChange {
	case sharedtypes.StateChangeRequestChanges:
		if err := executor.RequestChanges(ctx, entry.RepoID, entry.Number, entry.DraftComment); err != nil {
			return fmt.Errorf("request changes on %s#%d: %w", entry.RepoID, entry.Number, err)
		}
	case sharedtypes.StateChangeClose:
		// Close attaches the comment to the close action when present.
		if err := executor.Close(ctx, entry.RepoID, entry.Kind, entry.Number, entry.DraftComment); err != nil {
			return fmt.Errorf("close %s#%d: %w", entry.RepoID, entry.Number, err)
		}
	case sharedtypes.StateChangeMerge:
		if hasComment {
			if err := executor.Comment(ctx, entry.RepoID, entry.Kind, entry.Number, entry.DraftComment); err != nil {
				return fmt.Errorf("comment on %s#%d: %w", entry.RepoID, entry.Number, err)
			}
		}
		// Merge falls back from the configured method to whichever method
		// the repo actually allows; the chosen method is returned but
		// currently discarded - see comment on approvalExecutor.Merge.
		// The eventual outcome is visible in GitHub's UI; surfacing the
		// chosen method in the activity log is a future polish.
		if _, err := executor.Merge(ctx, entry.RepoID, entry.Number, mergeMethod); err != nil {
			return fmt.Errorf("merge %s#%d: %w", entry.RepoID, entry.Number, err)
		}
	case sharedtypes.StateChangeFixRequired, sharedtypes.StateChangeNone, "":
		if hasComment {
			if err := executor.Comment(ctx, entry.RepoID, entry.Kind, entry.Number, entry.DraftComment); err != nil {
				return fmt.Errorf("comment on %s#%d: %w", entry.RepoID, entry.Number, err)
			}
		}
	default:
		return fmt.Errorf("approve %s#%d: unsupported state_change %q", entry.RepoID, entry.Number, entry.StateChange)
	}

	return nil
}

func approvalLabelEdits(entry tui.Entry, item *db.Item, sync config.SyncLabels) ([]string, []string) {
	if item != nil && item.Role == sharedtypes.RoleContributor {
		return nil, nil
	}
	labels := append([]string(nil), entry.ProposedLabels...)
	return managedLabelEdits(labels, itemWithApprovedWaitingOn(item, entry), sync)
}

func itemWithApprovedWaitingOn(item *db.Item, entry tui.Entry) *db.Item {
	if item == nil || entry.WaitingOn == "" {
		return item
	}
	updated := *item
	updated.WaitingOn = entry.WaitingOn
	return &updated
}

func dismissLabelEdits(item *db.Item, sync config.SyncLabels) ([]string, []string) {
	if item != nil && item.Role == sharedtypes.RoleContributor {
		return nil, nil
	}
	return managedLabelEdits(nil, item, sync)
}

func managedLabelEdits(labels []string, item *db.Item, sync config.SyncLabels) ([]string, []string) {
	labels = append(labels, ezossTriagedLabel)
	remove := make([]string, 0, 3)
	if sync.WaitingOn {
		wanted := waitingOnLabel(item)
		for _, label := range managedWaitingOnLabels {
			if label == wanted {
				labels = append(labels, label)
				continue
			}
			remove = append(remove, label)
		}
	} else {
		remove = append(remove, managedWaitingOnLabels...)
	}
	if sync.Stale {
		if item != nil && item.StaleSince != nil {
			labels = append(labels, ezossStaleLabel)
		} else {
			remove = append(remove, ezossStaleLabel)
		}
	} else {
		remove = append(remove, ezossStaleLabel)
	}
	return uniqueStrings(labels), uniqueStrings(remove)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	unique := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

var managedWaitingOnLabels = []string{
	"ezoss/awaiting-contributor",
	"ezoss/awaiting-maintainer",
}

const ezossStaleLabel = "ezoss/stale"

func waitingOnLabel(item *db.Item) string {
	if item == nil {
		return ""
	}
	switch item.WaitingOn {
	case sharedtypes.WaitingOnContributor:
		return "ezoss/awaiting-contributor"
	case sharedtypes.WaitingOnMaintainer:
		return "ezoss/awaiting-maintainer"
	default:
		return ""
	}
}

func inboxItem(database *db.DB, entry tui.Entry) (*db.Item, error) {
	itemID := fmt.Sprintf("%s#%d", entry.RepoID, entry.Number)
	item, err := database.GetItem(itemID)
	if err != nil {
		return nil, fmt.Errorf("get item %s: %w", itemID, err)
	}
	return item, nil
}

// resolveOptionID returns the option_id the user is acting on. When
// callers (notably tests) construct a tui.Entry without setting
// OptionID, this falls back to the active recommendation's top option
// so the legacy "one option" semantics keep working.
func resolveOptionID(database *db.DB, entry tui.Entry) (string, error) {
	if entry.OptionID != "" {
		return entry.OptionID, nil
	}
	if entry.RecommendationID == "" {
		return "", fmt.Errorf("entry has no option or recommendation id")
	}
	rec, err := database.GetRecommendation(entry.RecommendationID)
	if err != nil {
		return "", err
	}
	if rec == nil || len(rec.Options) == 0 {
		return "", fmt.Errorf("recommendation %s has no options", entry.RecommendationID)
	}
	return rec.Options[0].ID, nil
}

func dismissInboxEntries(ctx context.Context, entries []tui.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	p, err := newPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	database, err := openDB(p.DBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()
	cfg, err := loadGlobalConfig(filepath.Join(p.Root(), "config.yaml"))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	editor := newLabelEditor()
	for _, entry := range entries {
		item, err := inboxItem(database, entry)
		if err != nil {
			return err
		}
		finalLabels, removeLabels := dismissLabelEdits(item, cfg.SyncLabels)
		if len(finalLabels) > 0 || len(removeLabels) > 0 {
			if err := editor.EditLabels(ctx, entry.RepoID, entry.Kind, entry.Number, finalLabels, removeLabels); err != nil {
				return fmt.Errorf("mark %s#%d triaged: %w", entry.RepoID, entry.Number, err)
			}
		}
		actedAt := time.Now()
		optionID, err := resolveOptionID(database, entry)
		if err != nil {
			return err
		}
		if _, err := database.DismissOption(optionID, finalLabels, actedAt); err != nil {
			return fmt.Errorf("dismiss option %s: %w", optionID, err)
		}
		if err := setInboxItemTriaged(database, entry, true); err != nil {
			return err
		}
	}
	return nil
}

func prepareInboxEntryEdit(ctx context.Context, entry tui.Entry) (*exec.Cmd, func(error) (tui.Entry, error), error) {
	cmd, finishContent, err := newDraftEditor().Prepare(ctx, formatEditableRecommendation(entry))
	if err != nil {
		return nil, nil, err
	}
	finish := func(execErr error) (tui.Entry, error) {
		content, err := finishContent(execErr)
		if err != nil {
			return entry, err
		}
		return parseEditedRecommendation(content, entry)
	}
	return cmd, finish, nil
}

func loadInboxEntry(database *db.DB, repoID string, number int) (*tui.Entry, error) {
	itemID := fmt.Sprintf("%s#%d", repoID, number)
	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		return nil, fmt.Errorf("list active recommendations: %w", err)
	}
	for _, rec := range recommendations {
		if rec.ItemID != itemID {
			continue
		}
		item, err := database.GetItem(itemID)
		if err != nil {
			return nil, fmt.Errorf("get item %s: %w", itemID, err)
		}
		if item == nil {
			return nil, nil
		}
		role := item.Role
		if role == "" {
			role = sharedtypes.RoleMaintainer
		}
		totals, err := database.RecommendationTokenTotalsForItem(itemID)
		if err != nil {
			return nil, fmt.Errorf("recommendation token totals for %s: %w", itemID, err)
		}
		entry := &tui.Entry{
			RecommendationID:  rec.ID,
			RepoID:            item.RepoID,
			Number:            item.Number,
			Kind:              item.Kind,
			Role:              role,
			Author:            item.Author,
			Title:             item.Title,
			URL:               githubItemURL(item.RepoID, item.Kind, item.Number),
			TokensIn:          totals.TokensIn,
			TokensOut:         totals.TokensOut,
			AgeLabel:          recommendationAgeLabel(rec.CreatedAt),
			ApprovalError:     latestApprovalError(database, rec.ID),
			CurrentWaitingOn:  item.WaitingOn,
			RerunInstructions: rec.RerunInstructions,
			Options:           buildEntryOptions(rec.Options),
		}
		entry.SyncActive()
		return entry, nil
	}
	return nil, nil
}

func markInboxItemTriaged(database *db.DB, entry tui.Entry) error {
	item, err := inboxItem(database, entry)
	if err != nil {
		return err
	}
	if item == nil {
		return nil
	}
	item.GHTriaged = true
	// Reflect the state change we just executed on GitHub locally so the
	// next poll doesn't see a stale state=open record (the triaged-refresh
	// query is bounded by repo poll cadence, so local state would otherwise
	// drift until the next reconciliation).
	switch entry.StateChange {
	case sharedtypes.StateChangeClose:
		item.State = sharedtypes.ItemStateClosed
	case sharedtypes.StateChangeMerge:
		item.State = sharedtypes.ItemStateMerged
	}
	if entry.WaitingOn != "" {
		item.WaitingOn = entry.WaitingOn
	}
	if err := database.UpsertItem(*item); err != nil {
		return fmt.Errorf("update item %s: %w", item.ID, err)
	}
	return nil
}

func setInboxItemTriaged(database *db.DB, entry tui.Entry, triaged bool) error {
	item, err := inboxItem(database, entry)
	if err != nil {
		return err
	}
	if item == nil {
		return nil
	}
	item.GHTriaged = triaged
	if err := database.UpsertItem(*item); err != nil {
		return fmt.Errorf("update item %s: %w", item.ID, err)
	}
	return nil
}

func persistFailedApprovalAttempt(database *db.DB, entry tui.Entry, finalLabels []string, actedAt time.Time, actedErr error) error {
	decision := sharedtypes.ApprovalDecisionApproved
	if entry.Edited() {
		decision = sharedtypes.ApprovalDecisionEdited
	}
	optionID, err := resolveOptionID(database, entry)
	if err != nil {
		return err
	}
	_, err = database.InsertApproval(db.NewApproval{
		RecommendationID: entry.RecommendationID,
		OptionID:         optionID,
		Decision:         decision,
		FinalComment:     entry.DraftComment,
		FinalLabels:      finalLabels,
		FinalStateChange: entry.StateChange,
		ActedAt:          &actedAt,
		ActedError:       actedErr.Error(),
	})
	if err != nil {
		return fmt.Errorf("record failed approval attempt for %s: %w", entry.RecommendationID, err)
	}
	return nil
}

func formatEditableRecommendation(entry tui.Entry) string {
	labels := strings.Join(entry.ProposedLabels, ", ")
	if labels == "" {
		labels = "none"
	}
	stateChange := string(entry.StateChange)
	if stateChange == "" {
		stateChange = string(sharedtypes.StateChangeNone)
	}
	return fmt.Sprintf("StateChange: %s\nLabels: %s\n\n%s%s", stateChange, labels, editedRecommendationDraftHeader, entry.DraftComment)
}

func parseEditedRecommendation(content string, entry tui.Entry) (tui.Entry, error) {
	parts := strings.SplitN(content, "\n\n"+editedRecommendationDraftHeader, 2)
	if len(parts) != 2 {
		return entry, fmt.Errorf("edited recommendation must include StateChange, Labels, and a Draft response section")
	}

	var stateChangeRaw string
	var labelsRaw string
	for _, line := range strings.Split(parts[0], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return entry, fmt.Errorf("invalid edited recommendation line %q", line)
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "statechange", "state_change", "state":
			stateChangeRaw = strings.TrimSpace(value)
		case "labels":
			labelsRaw = strings.TrimSpace(value)
		default:
			return entry, fmt.Errorf("unknown edited recommendation field %q", strings.TrimSpace(key))
		}
	}

	stateChange, err := parseStateChange(stateChangeRaw)
	if err != nil {
		return entry, err
	}
	labels := parseEditedLabels(labelsRaw)
	entry.StateChange = stateChange
	entry.ProposedLabels = labels
	entry.DraftComment = parts[1]
	return entry, nil
}

func parseStateChange(raw string) (sharedtypes.StateChange, error) {
	value := sharedtypes.StateChange(strings.TrimSpace(raw))
	switch value {
	case sharedtypes.StateChangeNone,
		sharedtypes.StateChangeClose,
		sharedtypes.StateChangeMerge,
		sharedtypes.StateChangeRequestChanges,
		sharedtypes.StateChangeFixRequired:
		return value, nil
	case "":
		return sharedtypes.StateChangeNone, nil
	default:
		return "", fmt.Errorf("unsupported edited state_change %q", raw)
	}
}

func parseEditedLabels(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.EqualFold(trimmed, "none") {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		label := strings.TrimSpace(part)
		if label == "" {
			continue
		}
		labels = append(labels, label)
	}
	return labels
}

func latestApprovalError(database *db.DB, recommendationID string) string {
	approvals, err := database.ListApprovalsForRecommendation(recommendationID)
	if err != nil {
		return ""
	}
	for i := len(approvals) - 1; i >= 0; i-- {
		if strings.TrimSpace(approvals[i].ActedError) != "" {
			return approvals[i].ActedError
		}
	}
	return ""
}

func recommendationAgeLabel(createdAt int64) string {
	if createdAt <= 0 {
		return "now"
	}
	delta := time.Since(time.Unix(createdAt, 0))
	if delta < time.Minute {
		return "now"
	}
	if delta < time.Hour {
		return fmt.Sprintf("%dm", int(delta.Minutes()))
	}
	if delta < 24*time.Hour {
		return fmt.Sprintf("%dh", int(delta.Hours()))
	}
	return fmt.Sprintf("%dd", int(delta.Hours()/24))
}

func launchDaemonProcess(pidFile string, useMock bool) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	logPath := filepath.Join(filepath.Dir(pidFile), "logs", "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()

	args := []string{"daemon", "run"}
	if useMock {
		args = append(args, "--mock")
	}
	proc := exec.Command(executable, args...)
	proc.Stdin = nil
	proc.Stdout = logFile
	proc.Stderr = logFile
	proc.Env = os.Environ()
	proc.SysProcAttr = detachedDaemonProcAttr()

	if err := proc.Start(); err != nil {
		return fmt.Errorf("start process: %w", err)
	}

	return nil
}

type envDraftEditor struct{}

func (envDraftEditor) Prepare(ctx context.Context, initial string) (*exec.Cmd, func(error) (string, error), error) {
	editor, ok := os.LookupEnv("EDITOR")
	if !ok || strings.TrimSpace(editor) == "" {
		return nil, nil, errors.New("EDITOR is not set")
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return nil, nil, errors.New("EDITOR is not set")
	}

	tempFile, err := os.CreateTemp("", "ezoss-draft-*.md")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp draft: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return nil, nil, fmt.Errorf("close temp draft: %w", err)
	}
	if err := os.WriteFile(tempPath, []byte(initial), 0o600); err != nil {
		_ = os.Remove(tempPath)
		return nil, nil, fmt.Errorf("write temp draft: %w", err)
	}

	// Stdio is intentionally left unset: bubbletea's tea.ExecProcess wires
	// the program's input/output onto the cmd after releasing the terminal,
	// so the editor takes over the real tty cleanly.
	cmd := exec.CommandContext(ctx, parts[0], append(parts[1:], tempPath)...)

	finish := func(execErr error) (string, error) {
		defer os.Remove(tempPath)
		if execErr != nil {
			return "", fmt.Errorf("run editor: %w", execErr)
		}
		content, err := os.ReadFile(tempPath)
		if err != nil {
			return "", fmt.Errorf("read temp draft: %w", err)
		}
		return string(content), nil
	}
	return cmd, finish, nil
}

func copyTextWithSystemClipboard(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("prompt is empty")
	}
	commands := clipboardCommands()
	if len(commands) == 0 {
		return errors.New("no clipboard command configured for this platform")
	}
	var lastErr error
	for _, command := range commands {
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("copy to clipboard: %w", lastErr)
}

func clipboardCommands() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{{"clip"}}
	case "linux":
		return [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	default:
		return nil
	}
}

func openURLWithSystemBrowser(ctx context.Context, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return errors.New("url is empty")
	}
	commands := openURLCommands(url)
	if len(commands) == 0 {
		return errors.New("no browser-open command configured for this platform")
	}
	var lastErr error
	for _, command := range commands {
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		if err := cmd.Run(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("open url: %w", lastErr)
}

func openURLCommands(url string) [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"open", url}}
	case "windows":
		return [][]string{{"rundll32", "url.dll,FileProtocolHandler", url}}
	case "linux":
		return [][]string{{"xdg-open", url}}
	default:
		return nil
	}
}

func readAgentsInstructions(root string) string {
	content, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		return ""
	}
	return string(content)
}

type mockTriageRunner struct{}

func (mockTriageRunner) Triage(_ context.Context, req daemon.TriageRequest) (*daemon.TriageResult, error) {
	result := agentmock.Recommend(req.Item)
	return &daemon.TriageResult{
		Agent:          result.Agent,
		Model:          result.Model,
		Recommendation: result.Recommendation,
		TokensIn:       result.TokensIn,
		TokensOut:      result.TokensOut,
	}, nil
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check local ezoss prerequisites",
		RunE: func(cmd *cobra.Command, _ []string) error {
			results := runDoctor(cmd.Context())
			telemetry.Track("command", telemetry.Fields{"command": "doctor", "entrypoint": "doctor"})
			hasFailure := false
			for _, result := range results {
				status := "ok"
				if result.Warning {
					status = "warn"
				} else if !result.OK {
					status = "fail"
					hasFailure = true
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", status, result.Name, strings.TrimSpace(result.Detail)); err != nil {
					return err
				}
			}
			if hasFailure {
				return errors.New("doctor found failing checks")
			}
			return nil
		},
	}
}

func newInitCmd() *cobra.Command {
	var repoIDs []string
	var allOwned bool
	var allPublicOwned bool
	var allPublicOwnedAndStarred bool
	var agent string
	var mergeMethod string
	var pollInterval string
	var staleThreshold string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize ezoss config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bulkFlags := 0
			for _, on := range []bool{allOwned, allPublicOwned, allPublicOwnedAndStarred} {
				if on {
					bulkFlags++
				}
			}
			if bulkFlags > 1 {
				return errors.New("--all-owned, --all-public-owned, and --all-public-owned-and-starred are mutually exclusive")
			}

			p, err := newPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			if err := p.EnsureDirs(); err != nil {
				return fmt.Errorf("create state directories: %w", err)
			}

			configPath := filepath.Join(p.Root(), "config.yaml")
			cfg, err := config.LoadGlobal(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			if agent != "" {
				agentName := config.AgentName(agent)
				if !agentName.IsSupported() {
					return fmt.Errorf("unsupported agent %q", agent)
				}
				cfg.Agent = agentName
			}
			if mergeMethod != "" {
				method := strings.ToLower(strings.TrimSpace(mergeMethod))
				switch method {
				case "merge", "squash", "rebase":
					cfg.MergeMethod = method
				default:
					return fmt.Errorf("parse --merge-method: %q is not merge, squash, or rebase", mergeMethod)
				}
			}
			if pollInterval != "" {
				d, err := config.ParseDuration(pollInterval)
				if err != nil {
					return fmt.Errorf("parse --poll-interval: %w", err)
				}
				cfg.PollInterval = d
			}
			if staleThreshold != "" {
				d, err := config.ParseDuration(staleThreshold)
				if err != nil {
					return fmt.Errorf("parse --stale-threshold: %w", err)
				}
				cfg.StaleThreshold = d
			}

			anyFlag := len(repoIDs) > 0 || allOwned || allPublicOwned || allPublicOwnedAndStarred ||
				agent != "" || mergeMethod != "" || pollInterval != "" || staleThreshold != ""

			collected, err := collectInitRepos(cmd, repoIDs, allOwned, allPublicOwned, allPublicOwnedAndStarred)
			if err != nil {
				return err
			}

			// When the user runs `ezoss init` with no flags in an interactive
			// shell, drop into the wizard so they don't have to remember the
			// `--repo` / `--all-owned` flag spelling.
			if !anyFlag && initWizardEnabled(cmd) {
				wizardRepos, aborted, err := runInitWizardFlow(cmd)
				if err != nil {
					return err
				}
				if aborted {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "init cancelled")
					return nil
				}
				collected = append(collected, wizardRepos...)
			}

			cfg.Repos = mergeRepoIDs(cfg.Repos, collected)

			if err := config.SaveGlobal(configPath, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			telemetry.Track("command", telemetry.Fields{"command": "init", "entrypoint": "init"})
			return writeInitSummary(cmd.OutOrStdout(), configPath, cfg)
		},
	}

	cmd.Flags().StringSliceVar(&repoIDs, "repo", nil, "Repository to monitor (owner/name); repeatable")
	cmd.Flags().BoolVar(&allOwned, "all-owned", false, "Add all repos owned by the authenticated GitHub user")
	cmd.Flags().BoolVar(&allPublicOwned, "all-public-owned", false, "Add all public repos owned by the authenticated GitHub user")
	cmd.Flags().BoolVar(&allPublicOwnedAndStarred, "all-public-owned-and-starred", false, "Add public repos that the authenticated GitHub user both owns and has starred")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent to use (auto, claude, codex, rovodev, opencode)")
	cmd.Flags().StringVar(&mergeMethod, "merge-method", "", "Default PR merge method (merge, squash, rebase)")
	cmd.Flags().StringVar(&pollInterval, "poll-interval", "", "Polling interval duration")
	cmd.Flags().StringVar(&staleThreshold, "stale-threshold", "", "Stale threshold duration")

	return cmd
}

// collectInitRepos resolves the repo IDs supplied via flags. It does NOT
// touch existing config.Repos; merging is the caller's responsibility.
func collectInitRepos(cmd *cobra.Command, repoIDs []string, allOwned, allPublicOwned, allPublicOwnedAndStarred bool) ([]string, error) {
	out := make([]string, 0, len(repoIDs))

	for _, raw := range repoIDs {
		repoID, err := parseRepoID(raw)
		if err != nil {
			return nil, fmt.Errorf("parse --repo: %w", err)
		}
		out = append(out, repoID)
	}

	if allOwned || allPublicOwned || allPublicOwnedAndStarred {
		lister := newRepoLister()
		visibility := ghclient.RepoVisibilityAll
		if allPublicOwned || allPublicOwnedAndStarred {
			visibility = ghclient.RepoVisibilityPublic
		}
		fetched, err := lister.ListOwnedRepos(cmd.Context(), visibility)
		if err != nil {
			return nil, fmt.Errorf("list owned repos: %w", err)
		}

		if allPublicOwnedAndStarred {
			starred, err := lister.ListStarredRepos(cmd.Context())
			if err != nil {
				return nil, fmt.Errorf("list starred repos: %w", err)
			}
			fetched = intersectRepoIDs(fetched, starred)
		}

		out = append(out, fetched...)
	}

	return out, nil
}

// intersectRepoIDs returns the repos present in both lists, preserving the
// order of the first list. Mirrors the wizard helper.
func intersectRepoIDs(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	bSet := make(map[string]struct{}, len(b))
	for _, r := range b {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		bSet[r] = struct{}{}
	}
	out := make([]string, 0, len(a))
	seen := make(map[string]struct{}, len(a))
	for _, r := range a {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, ok := bSet[r]; !ok {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

func runInitWizardFlow(cmd *cobra.Command) ([]string, bool, error) {
	cwd, err := currentWorkingDir()
	if err != nil {
		cwd = ""
	}
	detected, err := detectCurrentRepo(cmd.Context(), cwd)
	if err != nil {
		// A failed detection just means the wizard skips the "use this repo?"
		// shortcut; it's not a fatal error.
		detected = ""
	}

	cfg := wizard.Config{
		Context:      cmd.Context(),
		DetectedRepo: detected,
		ListOwnedRepos: func(ctx context.Context, visibility ghclient.RepoVisibility) ([]string, error) {
			return newRepoLister().ListOwnedRepos(ctx, visibility)
		},
		ListStarredRepos: func(ctx context.Context) ([]string, error) {
			return newRepoLister().ListStarredRepos(ctx)
		},
		Track: func(action string, fields map[string]any) {
			telemetry.Track("init_wizard", telemetry.Fields{
				"action": action,
				"fields": fields,
			})
		},
	}

	result, err := runInitWizard(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("run init wizard: %w", err)
	}
	if result.Err != nil {
		return nil, false, result.Err
	}
	return result.Repos, result.Aborted, nil
}

// mergeRepoIDs returns the union of existing and incoming repo IDs in the
// order existing-first-then-incoming, preserving the existing ordering and
// dropping duplicates.
func mergeRepoIDs(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, repo := range existing {
		if _, ok := seen[repo]; ok {
			continue
		}
		seen[repo] = struct{}{}
		merged = append(merged, repo)
	}
	for _, repo := range incoming {
		if _, ok := seen[repo]; ok {
			continue
		}
		seen[repo] = struct{}{}
		merged = append(merged, repo)
	}
	return merged
}

func writeInitSummary(w io.Writer, configPath string, cfg *config.GlobalConfig) error {
	repoWord := "repos"
	if len(cfg.Repos) == 1 {
		repoWord = "repo"
	}
	if _, err := fmt.Fprintf(w, "initialized %s (%d %s)\n", configPath, len(cfg.Repos), repoWord); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  agent: %s\n  poll: %s\n  stale: %s\n", cfg.Agent, cfg.PollInterval, formatStaleThreshold(cfg.StaleThreshold)); err != nil {
		return err
	}
	for _, repo := range cfg.Repos {
		if _, err := fmt.Fprintf(w, "  - %s\n", repo); err != nil {
			return err
		}
	}
	if len(cfg.Repos) == 0 {
		_, err := fmt.Fprintln(w, "\nNo repos configured. Add one with:\n  ezoss init --repo owner/name")
		return err
	}
	_, err := fmt.Fprintln(w, "\nNext: ezoss daemon start")
	return err
}

func formatStaleThreshold(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	return d.String()
}

func newStatusCmd() *cobra.Command {
	var short bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon and sync status",
		Long: `Show daemon and sync status.

In an interactive terminal, status opens a realtime TUI. In non-interactive
output, status prints rich text status. Use --short for the script-friendly
one-line key=value summary.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			telemetry.Track("command", telemetry.Fields{"command": "status", "entrypoint": "status"})

			collect := func() (statusData, error) {
				return collectStatusData(cmd)
			}

			out := cmd.OutOrStdout()
			if short {
				data, err := collect()
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(out, renderShortStatus(data))
				return err
			}
			if isInteractiveTerminal() {
				return runStatusTUI(cmd.Context(), out, cmd.ErrOrStderr(), statusTUIOptions{
					RefreshInterval: statusTUIRefreshInterval,
					Collect:         collect,
					Now:             time.Now,
				})
			}

			data, err := collect()
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(out, renderRichStatus(data, time.Now()))
			return err
		},
	}
	cmd.Flags().BoolVar(&short, "short", false, "Print the legacy one-line key=value summary")
	return cmd
}

// statusData is everything `ezoss status` needs to render. Built once, then
// formatted by renderShortStatus or renderRichStatus.
type statusData struct {
	daemonState       string
	daemonPID         int
	pending           int
	configuredPending int
	contribPending    int
	contribEnabled    bool
	contribRepos      int
	unconfigured      int
	repos             []string
	sync              *ipc.SyncStatusResult // nil when daemon is stopped or IPC failed
	syncErr           error                 // populated when daemon is running but the sync.status call failed
}

func collectStatusData(cmd *cobra.Command) (statusData, error) {
	p, err := newPaths()
	if err != nil {
		return statusData{}, fmt.Errorf("resolve paths: %w", err)
	}

	cfg, err := config.LoadGlobal(filepath.Join(p.Root(), "config.yaml"))
	if err != nil {
		return statusData{}, fmt.Errorf("load config: %w", err)
	}

	daemonStatus, err := readDaemonStatus(p.PIDPath())
	if err != nil {
		return statusData{}, fmt.Errorf("read daemon status: %w", err)
	}

	data := statusData{
		daemonState:    daemonStatus.State,
		daemonPID:      daemonStatus.PID,
		repos:          append([]string(nil), cfg.Repos...),
		contribEnabled: cfg.Contrib.Enabled,
	}

	configured := make(map[string]struct{}, len(cfg.Repos))
	for _, repoID := range cfg.Repos {
		configured[repoID] = struct{}{}
	}
	if _, err := os.Stat(p.DBPath()); err == nil {
		database, err := openDBWithRetry(p.DBPath())
		if err != nil {
			return statusData{}, fmt.Errorf("open db: %w", explainDatabaseLock(err))
		}
		defer database.Close()

		data.pending, err = database.CountActiveRecommendations()
		if err != nil {
			return statusData{}, fmt.Errorf("read pending recommendations: %w", err)
		}

		recommendations, err := database.ListActiveRecommendations()
		if err != nil {
			return statusData{}, fmt.Errorf("list pending recommendations: %w", err)
		}
		contribReposSeen := make(map[string]struct{})
		for _, rec := range recommendations {
			item, err := database.GetItem(rec.ItemID)
			if err != nil {
				return statusData{}, fmt.Errorf("get item %s: %w", rec.ItemID, err)
			}
			if item == nil {
				return statusData{}, fmt.Errorf("get item %s: not found", rec.ItemID)
			}
			role := item.Role
			if role == "" {
				role = sharedtypes.RoleMaintainer
			}
			if role == sharedtypes.RoleContributor {
				data.contribPending++
				contribReposSeen[item.RepoID] = struct{}{}
				continue
			}
			if _, ok := configured[item.RepoID]; !ok {
				data.unconfigured++
				continue
			}
			data.configuredPending++
		}
		data.contribRepos = len(contribReposSeen)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return statusData{}, fmt.Errorf("stat db: %w", err)
	}

	if daemonStatus.State == daemon.StateRunning {
		sync, err := fetchDaemonSyncStatus(p.IPCPath())
		if err != nil {
			data.syncErr = err
		} else {
			data.sync = sync
		}
	}

	return data, nil
}

// fetchDaemonSyncStatus dials the daemon's IPC socket and calls
// sync.status. Returns the snapshot, or an error if either step fails.
func fetchDaemonSyncStatus(socketPath string) (*ipc.SyncStatusResult, error) {
	client, err := dialDaemonIPC(socketPath)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	var result ipc.SyncStatusResult
	if err := client.Call(ipc.MethodSyncStatus, ipc.SyncStatusParams{}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func repoNounFor(n int) string {
	if n == 1 {
		return "repo"
	}
	return "repos"
}

func recommendationNounFor(n int) string {
	if n == 1 {
		return "recommendation"
	}
	return "recommendations"
}

// renderShortStatus is the legacy `pending=N repos=N daemon=...` one-liner,
// preserved for scripts. Always emits the per-source breakdown when any
// of the non-maintainer buckets are non-zero so scripts don't have to
// special-case the simple all-maintainer setup.
func renderShortStatus(d statusData) string {
	parts := []string{fmt.Sprintf("pending=%d", d.pending)}
	hasBreakdown := d.contribPending > 0 || d.unconfigured > 0
	if hasBreakdown {
		parts = append(parts, fmt.Sprintf("maintainer=%d", d.configuredPending))
		if d.contribPending > 0 {
			parts = append(parts, fmt.Sprintf("contrib=%d", d.contribPending))
		}
		if d.unconfigured > 0 {
			parts = append(parts, fmt.Sprintf("unconfigured=%d", d.unconfigured))
		}
	}
	parts = append(parts, fmt.Sprintf("repos=%d", len(d.repos)))
	if d.contribRepos > 0 {
		parts = append(parts, fmt.Sprintf("contrib_repos=%d", d.contribRepos))
	}
	parts = append(parts, fmt.Sprintf("daemon=%s", d.daemonState))
	if !d.contribEnabled {
		parts = append(parts, "contrib_mode=off")
	}
	return strings.Join(parts, " ")
}

// renderRichStatus is the default human-readable output. It folds in the
// IPC sync.status snapshot when available and lays out per-repo progress.
func renderRichStatus(d statusData, now time.Time) string {
	var b strings.Builder

	if d.daemonState == daemon.StateRunning {
		fmt.Fprintf(&b, "daemon: running (pid %d)\n", d.daemonPID)
	} else {
		fmt.Fprintln(&b, "daemon: stopped")
	}

	// One row per item source: maintainer (configured repos), contributor
	// (auto-discovered via gh search --author=@me), and unconfigured
	// (legacy stragglers from a repo the user removed from config).
	// Each row is self-contained so the user doesn't need to do
	// arithmetic across rows to figure out where things live.
	const labelWidth = 13
	maintainerRepos := len(d.repos)
	fmt.Fprintf(&b, "%-*s %d %s  •  %d pending %s\n",
		labelWidth, "maintainer:", maintainerRepos, repoNounFor(maintainerRepos),
		d.configuredPending, recommendationNounFor(d.configuredPending))

	if d.contribEnabled {
		fmt.Fprintf(&b, "%-*s %d %s  •  %d pending %s\n",
			labelWidth, "contributor:", d.contribRepos, repoNounFor(d.contribRepos),
			d.contribPending, recommendationNounFor(d.contribPending))
	} else {
		fmt.Fprintf(&b, "%-*s disabled (set contrib.enabled: true to track issues/PRs you authored in repos you don't maintain)\n",
			labelWidth, "contributor:")
	}

	if d.unconfigured > 0 {
		fmt.Fprintf(&b, "%-*s %d pending %s (in repos no longer in your config)\n",
			labelWidth, "unconfigured:", d.unconfigured, recommendationNounFor(d.unconfigured))
	}

	switch {
	case d.daemonState != daemon.StateRunning:
		fmt.Fprintln(&b, "sync:   not running - start with `ezoss daemon start`")
	case d.syncErr != nil:
		fmt.Fprintf(&b, "sync:   unavailable (%s)\n", d.syncErr)
	case d.sync != nil:
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, renderSyncSection(d, now))
	}

	return b.String()
}

func renderSyncSection(d statusData, now time.Time) string {
	var b strings.Builder
	sync := d.sync

	header := "Sync: "
	switch {
	case sync.Phase == ipc.PhaseSync:
		header += fmt.Sprintf("data stage  •  %d / %d repos", sync.CurrentIndex, sync.Total)
	case sync.Phase == ipc.PhaseAgents:
		header += fmt.Sprintf("agents stage  •  %d / %d items", sync.AgentsDone, sync.AgentsTotal)
		if sync.CurrentItem != "" {
			header += "  •  current " + sync.CurrentItem
		}
	case !sync.LastCycleEnd.IsZero():
		header += fmt.Sprintf("cycle %d finished %s", sync.CycleCount, formatRelativeTime(now, sync.LastCycleEnd))
		if sync.LastCycleOverran && sync.LastCycleDuration > 0 {
			header += fmt.Sprintf(" (took %s, overran %s interval)", formatDuration(sync.LastCycleDuration), formatDuration(sync.Interval))
		}
		if !sync.NextCycleAt.IsZero() {
			header += fmt.Sprintf("  •  next %s", formatRelativeTime(now, sync.NextCycleAt))
		}
	case sync.CurrentRepo != "":
		// First cycle in flight pre-phase: legacy fallback for daemons
		// that haven't been upgraded.
		header += fmt.Sprintf("first cycle in flight (%d / %d)", sync.CurrentIndex, sync.Total)
	case len(sync.Repos) > 0 || len(d.repos) > 0:
		header += "starting up"
	default:
		header += "idle (no repos configured)"
	}
	fmt.Fprintln(&b, header)

	repoMap := make(map[string]ipc.RepoSyncStatus, len(sync.Repos))
	for _, r := range sync.Repos {
		repoMap[r.Repo] = r
	}

	if len(d.repos) == 0 {
		return strings.TrimRight(b.String(), "\n")
	}

	maxName := 0
	for _, repo := range d.repos {
		if len(repo) > maxName {
			maxName = len(repo)
		}
	}

	for _, repo := range d.repos {
		state, ok := repoMap[repo]
		marker, status := repoStatusLine(state, ok, now)
		fmt.Fprintf(&b, "  %s %-*s  %s\n", marker, maxName, repo, status)
	}

	return strings.TrimRight(b.String(), "\n")
}

func repoStatusLine(state ipc.RepoSyncStatus, present bool, now time.Time) (string, string) {
	switch {
	case present && state.Syncing:
		started := ""
		if !state.LastSyncStart.IsZero() {
			started = fmt.Sprintf(" (%s in)", formatDuration(now.Sub(state.LastSyncStart)))
		}
		return "→", "syncing..." + started
	case present && state.LastError != "":
		return "✗", "error: " + truncateErr(state.LastError, 80) + " " + formatRelativeTime(now, state.LastSyncEnd)
	case present && !state.LastSyncEnd.IsZero():
		return "✓", "synced"
	default:
		return "·", "pending first sync"
	}
}

// formatRelativeTime renders a time as a human-friendly duration relative
// to now. Past times use "X ago"; future times use "in X". Zero returns "".
func formatRelativeTime(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	diff := t.Sub(now)
	if diff >= 0 {
		return "in " + formatDuration(diff)
	}
	return formatDuration(-diff) + " ago"
}

// formatDuration is a compact duration renderer geared for CLI output.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncateErr(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return s[:n-1] + "…"
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List pending recommendations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			telemetry.Track("command", telemetry.Fields{"command": "list", "entrypoint": "list"})
			return renderPendingRecommendations(cmd.OutOrStdout(), false)
		},
	}
}

func renderPendingRecommendations(out io.Writer, rerunInTerminal bool) error {
	p, err := newPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}

	daemonStatus, err := readDaemonStatus(p.PIDPath())
	if err != nil {
		return fmt.Errorf("read daemon status: %w", err)
	}

	if _, err := os.Stat(p.DBPath()); errors.Is(err, fs.ErrNotExist) {
		_, err = fmt.Fprintln(out, "no pending recommendations")
		return err
	} else if err != nil {
		return fmt.Errorf("stat db: %w", err)
	}

	database, err := openDBWithRetry(p.DBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", explainDatabaseLock(err))
	}
	defer database.Close()

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		return fmt.Errorf("list recommendations: %w", err)
	}
	if len(recommendations) == 0 {
		_, err = fmt.Fprintln(out, "no pending recommendations")
		return err
	}

	cfg, err := loadGlobalConfig(filepath.Join(p.Root(), "config.yaml"))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	configured := make(map[string]struct{}, len(cfg.Repos))
	for _, r := range cfg.Repos {
		configured[r] = struct{}{}
	}

	type pendingRecommendationRow struct {
		rec          db.Recommendation
		item         *db.Item
		isConfigured bool
	}

	rows := make([]pendingRecommendationRow, 0, len(recommendations))
	orphanRepos := make([]string, 0)
	seenOrphans := make(map[string]struct{})
	orphanCount := 0
	for _, rec := range recommendations {
		item, err := database.GetItem(rec.ItemID)
		if err != nil {
			return fmt.Errorf("get item %s: %w", rec.ItemID, err)
		}
		if item == nil {
			return fmt.Errorf("get item %s: not found", rec.ItemID)
		}

		_, isConfigured := configured[item.RepoID]
		if !isConfigured {
			orphanCount++
			if _, seen := seenOrphans[item.RepoID]; !seen {
				seenOrphans[item.RepoID] = struct{}{}
				orphanRepos = append(orphanRepos, item.RepoID)
			}
		}

		rows = append(rows, pendingRecommendationRow{rec: rec, item: item, isConfigured: isConfigured})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].isConfigured != rows[j].isConfigured {
			return rows[i].isConfigured
		}
		if rows[i].rec.CreatedAt != rows[j].rec.CreatedAt {
			return rows[i].rec.CreatedAt > rows[j].rec.CreatedAt
		}
		return rows[i].rec.ID > rows[j].rec.ID
	})

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ITEM\tKIND\tACTION\tCONFIDENCE\tAGE\tTITLE\tURL"); err != nil {
		return err
	}

	for _, row := range rows {
		itemLabel := row.rec.ItemID
		if !row.isConfigured {
			itemLabel += " (unconfigured)"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			itemLabel,
			row.item.Kind,
			listActionLabel(row.rec),
			recommendationDisplayConfidence(row.rec),
			compactRelativeAge(row.rec.CreatedAt),
			row.item.Title,
			githubItemURL(row.item.RepoID, row.item.Kind, row.item.Number),
		); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	noun := "recommendations"
	if len(recommendations) == 1 {
		noun = "recommendation"
	}
	summary := fmt.Sprintf("%d pending %s", len(recommendations), noun)
	if orphanCount > 0 && orphanCount < len(recommendations) {
		configuredCount := len(recommendations) - orphanCount
		summary += fmt.Sprintf(" (%d configured, %d unconfigured)", configuredCount, orphanCount)
	}
	hint := "Run `ezoss` to review in the TUI."
	if rerunInTerminal {
		hint = "Re-run `ezoss` in a terminal to review in the TUI."
	}
	if _, err := fmt.Fprintf(out, "\n%s. %s\n", summary, hint); err != nil {
		return err
	}
	if daemonStatus.State != daemon.StateRunning {
		if _, err := fmt.Fprintln(out, "warning: daemon is not running; this inbox will not refresh until you run `ezoss daemon start`."); err != nil {
			return err
		}
	}

	if len(orphanRepos) > 0 {
		sort.Strings(orphanRepos)
		recClause := "recommendations are"
		if orphanCount == 1 {
			recClause = "recommendation is"
		}
		repoClause := "repos"
		if len(orphanRepos) == 1 {
			repoClause = "a repo"
		}
		if _, err := fmt.Fprintf(out, "note: %d %s for %s not in your config (%s); the daemon will not refresh %s.\n",
			orphanCount,
			recClause,
			repoClause,
			strings.Join(orphanRepos, ", "),
			pronounForCount(orphanCount),
		); err != nil {
			return err
		}
		hintArgs := make([]string, 0, len(orphanRepos))
		for _, repoID := range orphanRepos {
			hintArgs = append(hintArgs, fmt.Sprintf("--repo %s", repoID))
		}
		if _, err := fmt.Fprintf(out, "      add with `ezoss init %s`\n", strings.Join(hintArgs, " ")); err != nil {
			return err
		}
	}

	return nil
}

// recommendationDisplayConfidence returns the top-pick option's confidence
// for surfaces that show one value per recommendation (CLI list, status).
func recommendationDisplayConfidence(rec db.Recommendation) sharedtypes.Confidence {
	if len(rec.Options) == 0 {
		return ""
	}
	return rec.Options[0].Confidence
}

func pronounForCount(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// listActionLabel describes the proposed action of the recommendation's
// top-pick option, e.g. "comment + close", "merge", "labels only", or
// "mark triaged" when nothing is proposed. Multi-option recommendations
// show the top pick with a trailing " (+N alts)" hint.
func listActionLabel(rec db.Recommendation) string {
	if len(rec.Options) == 0 {
		return "mark triaged"
	}
	primary := rec.Options[0]
	parts := make([]string, 0, 3)
	if strings.TrimSpace(primary.DraftComment) != "" {
		parts = append(parts, "comment")
	}
	switch primary.StateChange {
	case sharedtypes.StateChangeClose:
		parts = append(parts, "close")
	case sharedtypes.StateChangeMerge:
		parts = append(parts, "merge")
	case sharedtypes.StateChangeRequestChanges:
		parts = append(parts, "request changes")
	}
	var label string
	if len(parts) == 0 {
		if len(primary.ProposedLabels) > 0 {
			label = "labels only"
		} else {
			label = "mark triaged"
		}
	} else {
		label = strings.Join(parts, " + ")
	}
	if alts := len(rec.Options) - 1; alts > 0 {
		label += fmt.Sprintf(" (+%d alts)", alts)
	}
	return label
}

func compactRelativeAge(createdAt int64) string {
	if createdAt <= 0 {
		return "-"
	}

	age := time.Since(time.Unix(createdAt, 0))
	if age < 0 {
		age = 0
	}

	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
	}
}

func newTriageCmd() *cobra.Command {
	var useMock bool

	cmd := &cobra.Command{
		Use:   "triage <repo>#<number>",
		Short: "Manually triage a single GitHub item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoID, number, err := parseItemTarget(args[0])
			if err != nil {
				return fmt.Errorf("parse target: %w", err)
			}

			p, err := newPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			if err := p.EnsureDirs(); err != nil {
				return fmt.Errorf("create state directories: %w", err)
			}

			database, err := openDB(p.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer database.Close()

			poller, err := newManualTriagePoller(cmd.Context(), p.Root(), database, repoID, number, useMock)
			if err != nil {
				return err
			}

			if err := daemon.PollOnce(cmd.Context(), poller, []string{repoID}); err != nil {
				return fmt.Errorf("triage item: %w", err)
			}

			telemetry.Track("command", telemetry.Fields{"command": "triage", "entrypoint": "triage"})

			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "triaged %s#%d\n", repoID, number); err != nil {
				return err
			}

			itemTargetID := fmt.Sprintf("%s#%d", repoID, number)
			item, err := database.GetItem(itemTargetID)
			if err != nil {
				return fmt.Errorf("load item: %w", err)
			}
			rec, err := latestActiveRecommendationForItem(database, itemTargetID)
			if err != nil {
				return fmt.Errorf("load recommendation: %w", err)
			}
			if item != nil && rec != nil {
				action := listActionLabel(*rec)
				confidence := string(recommendationDisplayConfidence(*rec))
				if confidence == "" {
					confidence = "unknown"
				}
				if _, err := fmt.Fprintf(out, "  action:     %s (%s confidence)\n", action, confidence); err != nil {
					return err
				}
				if url := githubItemURL(item.RepoID, item.Kind, item.Number); url != "" {
					if _, err := fmt.Fprintf(out, "  URL:        %s\n", url); err != nil {
						return err
					}
				}
				if _, err := fmt.Fprintln(out, "\nRun `ezoss` to review details and approve/edit."); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&useMock, "mock", false, "Use canned GitHub items and recommendations")
	return cmd
}

func latestActiveRecommendationForItem(database *db.DB, itemID string) (*db.Recommendation, error) {
	recs, err := database.ListActiveRecommendations()
	if err != nil {
		return nil, err
	}
	var latest *db.Recommendation
	for i := range recs {
		if recs[i].ItemID != itemID {
			continue
		}
		if latest == nil || recs[i].CreatedAt > latest.CreatedAt {
			r := recs[i]
			latest = &r
		}
	}
	return latest, nil
}

func parseItemTarget(value string) (string, int, error) {
	index := strings.LastIndex(value, "#")
	if index <= 0 || index == len(value)-1 {
		return "", 0, fmt.Errorf("%q is not owner/name#number", value)
	}

	repoID, err := parseRepoID(value[:index])
	if err != nil {
		return "", 0, fmt.Errorf("%q is not owner/name#number", value)
	}

	number, err := strconv.Atoi(value[index+1:])
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("%q is not owner/name#number", value)
	}
	return repoID, number, nil
}

func parseRepoID(value string) (string, error) {
	owner, name, ok := strings.Cut(value, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("%q is not owner/name", value)
	}
	return owner + "/" + name, nil
}

func loadMockItem(ctx context.Context, repoID string, number int) (ghclient.Item, error) {
	client := ghmock.New()
	item, err := loadItem(ctx, client, repoID, number)
	if err == nil {
		return item, nil
	}
	if !isMissingGitHubItemError(err) {
		return ghclient.Item{}, err
	}
	fixtures, ferr := client.ListNeedingTriage(ctx, repoID)
	if ferr != nil || len(fixtures) == 0 {
		return ghclient.Item{}, err
	}
	targets := make([]string, 0, len(fixtures))
	for _, f := range fixtures {
		targets = append(targets, fmt.Sprintf("%s#%d (%s)", f.Repo, f.Number, f.Kind))
	}
	return ghclient.Item{}, fmt.Errorf("%w; mock fixtures: %s", err, strings.Join(targets, ", "))
}

func loadItem(ctx context.Context, client itemFetcher, repoID string, number int) (ghclient.Item, error) {
	var firstErr error
	for _, kind := range []string{"issue", "pr"} {
		var itemKind = sharedKind(kind)
		item, err := client.GetItem(ctx, repoID, itemKind, number)
		if err == nil {
			return item, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if !isMissingGitHubItemError(err) {
			return ghclient.Item{}, fmt.Errorf("gh item view %s#%d: %w", repoID, number, err)
		}
	}
	if firstErr != nil {
		return ghclient.Item{}, fmt.Errorf("gh item view %s#%d: not found", repoID, number)
	}
	return ghclient.Item{}, fmt.Errorf("gh item view %s#%d: not found", repoID, number)
}

func isMissingGitHubItemError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "mock item not found") ||
		strings.Contains(message, "not found") ||
		strings.Contains(message, "could not resolve to") ||
		strings.Contains(message, "no issue matches") ||
		strings.Contains(message, "no pull requests match")
}

func newManualTriagePoller(ctx context.Context, root string, database *db.DB, repoID string, number int, useMock bool) (daemon.Poller, error) {
	if useMock {
		item, err := loadMockItem(ctx, repoID, number)
		if err != nil {
			return daemon.Poller{}, fmt.Errorf("load item: %w", err)
		}
		return daemon.Poller{
			DB:                 database,
			GitHub:             singleItemLister{item: item},
			Triage:             mockTriageRunner{},
			AgentsInstructions: readAgentsInstructions(root),
		}, nil
	}

	cfg, err := loadLiveTriageConfig(root, repoID)
	if err != nil {
		return daemon.Poller{}, err
	}
	item, err := loadItem(ctx, newGitHubClient(), repoID, number)
	if err != nil {
		return daemon.Poller{}, fmt.Errorf("load item: %w", err)
	}

	runner, err := newLiveTriageRunner(root, cfg.Agent)
	if err != nil {
		return daemon.Poller{}, fmt.Errorf("create triage runner: %w", err)
	}

	return daemon.Poller{
		DB:                 database,
		GitHub:             singleItemLister{item: item},
		Triage:             runner,
		AgentsInstructions: readAgentsInstructions(root),
	}, nil
}

func loadLiveTriageConfig(root string, repoID string) (*config.Config, error) {
	globalCfg, err := config.LoadGlobal(filepath.Join(root, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if globalCfg != nil {
		if len(globalCfg.Repos) > 1 {
			return config.Merge(globalCfg, nil), nil
		}
		if len(globalCfg.Repos) == 1 && globalCfg.Repos[0] != repoID {
			return config.Merge(globalCfg, nil), nil
		}
	}
	cwd, err := currentWorkingDir()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	repoCfg, err := loadNearestRepoConfig(cwd)
	if err != nil {
		return nil, fmt.Errorf("load repo config: %w", err)
	}
	return config.Merge(globalCfg, repoCfg), nil
}

func loadDaemonTriageConfig(root string, globalCfg *config.GlobalConfig) (*config.Config, error) {
	if globalCfg == nil {
		return config.Merge(nil, nil), nil
	}
	if len(globalCfg.Repos) != 1 {
		return config.Merge(globalCfg, nil), nil
	}
	cwd, err := currentWorkingDir()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	repoCfg, err := loadNearestRepoConfig(cwd)
	if err != nil {
		return nil, fmt.Errorf("load repo config: %w", err)
	}
	return config.Merge(globalCfg, repoCfg), nil
}

func loadNearestRepoConfig(dir string) (*config.RepoConfig, error) {
	current := dir
	for {
		repoCfg, err := config.LoadRepo(current)
		if err != nil {
			return nil, err
		}
		if repoCfg.Agent != "" {
			return repoCfg, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return repoCfg, nil
		}
		current = parent
	}
}

func newLiveTriageRunner(stateRoot string, agentName sharedtypes.AgentName) (*liveTriageRunner, error) {
	resolvedName, bin, err := agent.Resolve(agentName, lookPath)
	if err != nil {
		return nil, err
	}
	cwd, err := currentWorkingDir()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	return &liveTriageRunner{name: resolvedName, bin: bin, cwd: cwd, stateRoot: stateRoot}, nil
}

type liveTriageRunner struct {
	name      sharedtypes.AgentName
	bin       string
	cwd       string
	stateRoot string
}

func (r *liveTriageRunner) Triage(ctx context.Context, req daemon.TriageRequest) (*daemon.TriageResult, error) {
	agentRunner, err := newAgent(r.name, r.bin)
	if err != nil {
		return nil, err
	}
	defer agentRunner.Close()

	cwd := r.cwd
	prompt := req.Prompt
	var releaseCheckoutLock func() error
	if r.stateRoot != "" && strings.TrimSpace(req.Item.Repo) != "" {
		release, err := acquireInvestigationCheckoutLock(ctx, r.stateRoot, req.Item.Repo)
		if err != nil {
			return nil, fmt.Errorf("lock investigation checkout: %w", err)
		}
		releaseCheckoutLock = release
		defer func() { _ = releaseCheckoutLock() }()

		checkout, err := prepareInvestigationCheckout(ctx, r.stateRoot, req.Item.Repo)
		if err != nil {
			return nil, fmt.Errorf("prepare investigation checkout: %w", err)
		}
		if checkout != "" {
			cwd = checkout
			prompt = promptWithInvestigationCheckout(prompt, checkout)
		}
	}

	result, err := agentRunner.Run(ctx, agent.RunOpts{
		Prompt:     prompt,
		CWD:        cwd,
		JSONSchema: cloneJSON(req.Schema),
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("agent returned no result")
	}

	recommendation, err := triage.Parse(result.Output)
	if err != nil {
		return nil, err
	}

	return &daemon.TriageResult{
		Agent:          r.name,
		Model:          agentRunner.Name(),
		Recommendation: recommendation,
		TokensIn:       result.Usage.TotalInputTokens(),
		TokensOut:      result.Usage.OutputTokens,
	}, nil
}

func acquireInvestigationCheckoutLock(ctx context.Context, root string, repoID string) (func() error, error) {
	lockDir := filepath.Join(root, "investigations", ".locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	lockPath := filepath.Join(lockDir, investigationRepoDirName(repoID)+".lock")

	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("write lock owner: %w", err)
			}
			if err := file.Close(); err != nil {
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("close lock owner: %w", err)
			}
			return func() error {
				return os.Remove(lockPath)
			}, nil
		}
		if !os.IsExist(err) {
			if _, statErr := os.Stat(lockPath); statErr != nil {
				return nil, fmt.Errorf("acquire lock: %w", err)
			}
		}
		removed, staleErr := removeStaleInvestigationCheckoutLock(lockPath)
		if staleErr != nil {
			return nil, staleErr
		}
		if removed {
			continue
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("acquire lock: %w", err)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func removeStaleInvestigationCheckoutLock(lockPath string) (bool, error) {
	info, err := os.Stat(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("inspect lock: %w", err)
	}
	if info.IsDir() {
		if err := os.RemoveAll(lockPath); err != nil {
			return false, fmt.Errorf("remove stale lock: %w", err)
		}
		return true, nil
	}

	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("read lock owner: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("remove stale lock: %w", err)
		}
		return true, nil
	}
	if processExists(pid) {
		return false, nil
	}
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove stale lock: %w", err)
	}
	return true, nil
}

type gitCommandRunner func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)

// ghCommandRunner runs a `gh` subcommand. Used for the initial repo
// clone so we go through gh's credential helper (which understands
// SSO grants, fine-grained tokens, and token refresh) instead of
// reusing `gh auth token` as a bearer header on a raw `git clone`,
// which silently breaks for SSO-required orgs and fine-grained tokens.
type ghCommandRunner func(ctx context.Context, dir string, args ...string) ([]byte, error)

var runGhCommand ghCommandRunner = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitNoPromptEnv()...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// gitNoPromptEnv disables git's interactive credential fallbacks so a
// missing or unauthorized token surfaces as a clean exit code instead
// of git opening /dev/tty to prompt for a username/password. Git
// ignores stdin and opens /dev/tty directly when one is available,
// so closing stdin is not enough on its own.
func gitNoPromptEnv() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=true",
		"GCM_INTERACTIVE=never",
	}
}

func runGitCommand(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitNoPromptEnv()...)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func preparePersistentInvestigationCheckout(ctx context.Context, root string, repoID string, run gitCommandRunner, runGh ghCommandRunner) (string, error) {
	repoID = strings.TrimSpace(repoID)
	if repoID == "" || !strings.Contains(repoID, "/") {
		return "", fmt.Errorf("invalid repo %q", repoID)
	}
	if run == nil {
		run = runGitCommand
	}
	if runGh == nil {
		runGh = runGhCommand
	}

	investigationsDir := filepath.Join(root, "investigations")
	checkout := filepath.Join(investigationsDir, investigationRepoDirName(repoID))
	if err := os.MkdirAll(investigationsDir, 0o755); err != nil {
		return "", fmt.Errorf("create investigations dir: %w", err)
	}

	if _, err := os.Stat(filepath.Join(checkout, ".git")); err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect checkout: %w", err)
		}
		if err := os.RemoveAll(checkout); err != nil {
			return "", fmt.Errorf("remove invalid checkout: %w", err)
		}
		// Use `gh repo clone` so the clone authenticates via gh's
		// credential helper (which handles SSO grants, fine-grained
		// tokens, and token refresh) rather than reusing `gh auth
		// token` as a bare bearer header. The bearer-header path
		// fails silently for SSO-required orgs because gh's session
		// authorization is not carried by the OAuth token alone.
		if _, err := runGh(ctx, investigationsDir, "repo", "clone", repoID, checkout); err != nil {
			return "", err
		}
	}

	if _, err := run(ctx, checkout, nil, "fetch", "--prune", "origin"); err != nil {
		return "", err
	}
	_, _ = run(ctx, checkout, nil, "remote", "set-head", "origin", "-a")
	defaultRef := "origin/main"
	if out, err := run(ctx, checkout, nil, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		ref := strings.TrimSpace(string(out))
		if ref != "" && ref != "origin/HEAD" {
			defaultRef = ref
		}
	}
	if _, err := run(ctx, checkout, nil, "reset", "--hard"); err != nil {
		return "", err
	}
	if _, err := run(ctx, checkout, nil, "clean", "-fdx"); err != nil {
		return "", err
	}
	if _, err := run(ctx, checkout, nil, "checkout", "--detach", defaultRef); err != nil {
		return "", err
	}
	if _, err := run(ctx, checkout, nil, "reset", "--hard", defaultRef); err != nil {
		return "", err
	}
	if _, err := run(ctx, checkout, nil, "clean", "-fdx"); err != nil {
		return "", err
	}
	return checkout, nil
}

func investigationRepoDirName(repoID string) string {
	return strings.NewReplacer("/", "__", "\\", "__", ":", "_").Replace(repoID)
}

func promptWithInvestigationCheckout(prompt string, checkout string) string {
	return strings.TrimSpace(prompt) + "\n\nRepository checkout for investigation:\n" + checkout + "\n\nThis checkout is managed by ezoss for daemon-initiated agent work. Use it as the local repository context for code investigation. Do not push, publish branches, open pull requests, or mutate GitHub from this checkout. Local edits are scratch and will be discarded before a future agent run."
}

func cloneJSON(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	cloned := make(json.RawMessage, len(data))
	copy(cloned, data)
	return cloned
}

type singleItemLister struct {
	item ghclient.Item
}

func (l singleItemLister) ListNeedingTriage(_ context.Context, repo string) ([]ghclient.Item, error) {
	if l.item.Repo != repo {
		return nil, fmt.Errorf("unexpected repo %q", repo)
	}
	return []ghclient.Item{l.item}, nil
}

func (l singleItemLister) ListTriaged(_ context.Context, _ string, _ time.Time) ([]ghclient.Item, error) {
	return nil, nil
}

func sharedKind(kind string) sharedtypes.ItemKind {
	if kind == "pr" {
		return sharedtypes.ItemKindPR
	}
	return sharedtypes.ItemKindIssue
}

func githubItemURL(repoID string, kind sharedtypes.ItemKind, number int) string {
	if strings.TrimSpace(repoID) == "" || number <= 0 {
		return ""
	}
	segment := "issues"
	if kind == sharedtypes.ItemKindPR {
		segment = "pull"
	}
	return fmt.Sprintf("https://github.com/%s/%s/%d", repoID, segment, number)
}
