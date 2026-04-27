package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ghclient"
	"github.com/kunchenguid/ezoss/internal/ipc"
)

var ErrAlreadyRunning = errors.New("daemon already running")

type Launcher func() error

type PollFunc func(ctx context.Context, repos []string) error

type RunOptions struct {
	Repos                  []string
	PollInterval           time.Duration
	StaleThreshold         time.Duration
	IgnoreOlderThan        time.Duration
	IPCPath                string
	PollOnce               PollFunc
	NewTicker              func(time.Duration) Ticker
	Sleep                  func(time.Duration)
	RecommendationSnapshot func() ([]db.Recommendation, error)
	// SyncState, if non-nil, is updated as the poll loop runs and exposed
	// over IPC via MethodSyncStatus. The caller is responsible for wiring
	// SyncState.Hooks() into the Poller it backs PollOnce with.
	SyncState *SyncState
	// ProcessChecker, if non-nil, overrides the OS-level alive check
	// used to detect a pre-existing daemon. Tests inject a stub so they
	// can simulate live or dead pids without spawning real processes.
	ProcessChecker ProcessChecker
	// Logger receives lifecycle and per-cycle events. Optional; nil
	// silently drops every log call so tests don't have to construct one.
	Logger *slog.Logger
	// StartupAttrs are attached to the "daemon started" log line. Use it
	// to surface things the loop itself can't observe (build version,
	// config knobs, etc).
	StartupAttrs []any
}

type Ticker interface {
	Chan() <-chan time.Time
	Stop()
}

type realTicker struct {
	ticker *time.Ticker
}

func NewTicker(interval time.Duration) Ticker {
	return realTicker{ticker: time.NewTicker(interval)}
}

func (r realTicker) Chan() <-chan time.Time {
	return r.ticker.C
}

func (r realTicker) Stop() {
	r.ticker.Stop()
}

func Start(pidFile string, check ProcessChecker, launch Launcher) error {
	if launch == nil {
		return errors.New("launch daemon: nil launcher")
	}

	status, err := ReadStatus(pidFile, check)
	if err != nil {
		return fmt.Errorf("read daemon status: %w", err)
	}
	if status.State == StateRunning {
		return ErrAlreadyRunning
	}
	if status.Stale {
		if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale pid file: %w", err)
		}
	}
	if err := launch(); err != nil {
		return fmt.Errorf("launch daemon: %w", err)
	}
	return nil
}

func Run(pidFile string, sigCh <-chan os.Signal) error {
	return RunWithOptions(pidFile, sigCh, RunOptions{})
}

func RunWithOptions(pidFile string, sigCh <-chan os.Signal, opts RunOptions) (retErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err := fmt.Errorf("daemon panic: %v", recovered)
			opts.log().Error("daemon panic", "panic", recovered, "stack", string(debug.Stack()))
			opts.log().Error("daemon exiting on error", "err", err)
			retErr = err
		}
	}()

	if sigCh == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(ch)
		sigCh = ch
	}

	// Refuse to start if another daemon is already holding the pidfile.
	// Stale pidfiles (process is dead) are removed and we proceed.
	status, err := ReadStatus(pidFile, opts.ProcessChecker)
	if err != nil {
		return fmt.Errorf("inspect existing pid file: %w", err)
	}
	if status.State == StateRunning {
		return fmt.Errorf("%w (pid %d)", ErrAlreadyRunning, status.PID)
	}
	if status.Stale {
		if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale pid file: %w", err)
		}
	}

	myPID := fmt.Sprintf("%d", os.Getpid())
	if err := os.WriteFile(pidFile, []byte(myPID), 0o644); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	defer func() {
		pidData, err := os.ReadFile(pidFile)
		if err == nil && string(pidData) == myPID {
			_ = os.Remove(pidFile)
		}
	}()

	startAttrs := []any{
		"pid", os.Getpid(),
		"repos", len(opts.Repos),
		"poll_interval", opts.PollInterval,
		"stale_threshold", opts.StaleThreshold,
		"ignore_older_than", opts.IgnoreOlderThan,
	}
	startAttrs = append(startAttrs, opts.StartupAttrs...)
	opts.log().Info("daemon started", startAttrs...)

	var notify func(ipc.Event)
	if opts.IPCPath != "" {
		var stopIPC func()
		stopIPCFn, notifyFn, err := startIPCServer(opts.IPCPath, opts.RecommendationSnapshot, opts.SyncState)
		if err != nil {
			opts.log().Error("ipc startup failed", "err", err)
			return fmt.Errorf("start ipc server: %w", err)
		}
		opts.log().Info("ipc listening", "socket", opts.IPCPath)
		stopIPC = stopIPCFn
		notify = notifyFn
		defer stopIPC()
	}

	if err := runPollLoop(sigCh, opts, notify); err != nil {
		opts.log().Error("daemon exiting on error", "err", err)
		return err
	}

	opts.log().Info("daemon stopped")
	return nil
}

func (o RunOptions) log() *slog.Logger {
	if o.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return o.Logger
}

func startIPCServer(socketPath string, snapshot func() ([]db.Recommendation, error), syncState *SyncState) (func(), func(ipc.Event), error) {
	srv := ipc.NewServer()
	broadcaster := newEventBroadcaster()
	srv.Handle(ipc.MethodHealth, func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return ipc.HealthResult{Status: "ok"}, nil
	})
	srv.Handle(ipc.MethodSyncStatus, func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return syncState.Snapshot(), nil
	})
	srv.HandleStream(ipc.MethodSubscribe, func(ctx context.Context, raw json.RawMessage, send func(interface{}) error) error {
		var params ipc.SubscribeParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return err
			}
		}
		events, cancel := broadcaster.Subscribe(params.ItemID)
		defer cancel()
		if snapshot != nil {
			recommendations, err := snapshot()
			if err != nil {
				return err
			}
			for _, recommendation := range recommendations {
				if params.ItemID != "" && params.ItemID != recommendation.ItemID {
					continue
				}
				if err := send(ipc.Event{
					Type:             ipc.EventRecommendationCreated,
					RecommendationID: recommendation.ID,
					ItemID:           recommendation.ItemID,
				}); err != nil {
					return err
				}
			}
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case event, ok := <-events:
				if !ok {
					return nil
				}
				if err := send(event); err != nil {
					return err
				}
			}
		}
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(socketPath)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		client, err := ipc.Dial(socketPath)
		if err == nil {
			_ = client.Close()
			break
		}
		select {
		case serveErr := <-errCh:
			if serveErr != nil {
				return nil, nil, serveErr
			}
			return nil, nil, errors.New("ipc server exited before startup")
		default:
		}
		if time.Now().After(deadline) {
			srv.Close()
			<-errCh
			return nil, nil, fmt.Errorf("ipc server not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	return func() {
		srv.Close()
		<-errCh
		broadcaster.Close()
		_ = os.Remove(socketPath)
	}, broadcaster.Publish, nil
}

func runPollLoop(sigCh <-chan os.Signal, opts RunOptions, notify func(ipc.Event)) error {
	if opts.PollOnce == nil || len(opts.Repos) == 0 {
		opts.log().Info("poll loop idle", "reason", reasonForIdle(opts))
		<-sigCh
		opts.log().Info("received signal", "action", "stop")
		return nil
	}

	interval := opts.PollInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	newTicker := opts.NewTicker
	if newTicker == nil {
		newTicker = func(interval time.Duration) Ticker {
			return realTicker{ticker: time.NewTicker(interval)}
		}
	}
	sleep := opts.Sleep
	customSleep := sleep != nil
	if sleep == nil {
		sleep = time.Sleep
	}

	cycleNum := 0
	runCycle := func(ticker Ticker) (cycleErr, fatalErr error) {
		cycleNum++
		before, err := recommendationSnapshot(opts)
		if err != nil {
			return fmt.Errorf("snapshot recommendations: %w", err), nil
		}
		opts.log().Info("cycle started", "cycle", cycleNum, "repos", len(opts.Repos))
		cycleStart := time.Now()
		opts.SyncState.BeginCycle(opts.Repos)
		pollErr := opts.PollOnce(context.Background(), append([]string(nil), opts.Repos...))
		opts.SyncState.EndCycle()
		cycleDuration := time.Since(cycleStart)
		opts.SyncState.RecordCycleDuration(cycleDuration, interval)

		newRecs := -1
		if pollErr == nil && opts.RecommendationSnapshot != nil {
			if after, snapErr := opts.RecommendationSnapshot(); snapErr == nil {
				newRecs = countNewRecommendations(before, after)
			}
		}

		attrs := []any{"cycle", cycleNum, "duration", cycleDuration}
		if newRecs >= 0 {
			attrs = append(attrs, "new_recs", newRecs)
		}
		if pollErr != nil {
			attrs = append(attrs, "err", pollErr)
			opts.log().Warn("cycle done", attrs...)
		} else {
			opts.log().Info("cycle done", attrs...)
		}

		if cycleDuration > interval && ticker != nil {
			drained := drainOverrunTick(ticker)
			opts.log().Warn("cycle exceeded poll interval",
				"cycle", cycleNum,
				"duration", cycleDuration,
				"interval", interval,
				"drained_queued_tick", drained,
			)
		}

		if pollErr != nil {
			return pollErr, nil
		}
		if err := emitRecommendationEvents(opts, before, notify); err != nil {
			return err, nil
		}
		return nil, nil
	}

	cycleErr, fatalErr := runCycle(nil)
	if fatalErr != nil {
		return fatalErr
	}
	if cycleErr != nil {
		if handled, waitErr := handlePollError(sigCh, interval, sleep, customSleep, cycleErr, opts.log()); handled {
			if waitErr != nil {
				return waitErr
			}
			goto waitForTicker
		}
	}

waitForTicker:
	ticker := newTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			opts.log().Info("received signal", "action", "stop")
			return nil
		case <-ticker.Chan():
			cycleErr, fatalErr := runCycle(ticker)
			if fatalErr != nil {
				return fatalErr
			}
			if cycleErr != nil {
				_, waitErr := handlePollError(sigCh, interval, sleep, customSleep, cycleErr, opts.log())
				if waitErr != nil {
					return waitErr
				}
			}
		}
	}
}

func countNewRecommendations(before, after []db.Recommendation) int {
	beforeIDs := make(map[string]struct{}, len(before))
	for _, r := range before {
		beforeIDs[r.ID] = struct{}{}
	}
	n := 0
	for _, r := range after {
		if _, ok := beforeIDs[r.ID]; !ok {
			n++
		}
	}
	return n
}

func reasonForIdle(opts RunOptions) string {
	if opts.PollOnce == nil {
		return "no poll function"
	}
	return "no repos configured"
}

// drainOverrunTick drops a single tick from the ticker channel if one
// is waiting. Used after a cycle that ran longer than the configured
// poll interval so the next iteration waits for the next natural tick
// boundary instead of starting another cycle from a queued tick.
func drainOverrunTick(ticker Ticker) bool {
	select {
	case <-ticker.Chan():
		return true
	default:
		return false
	}
}

func handlePollError(sigCh <-chan os.Signal, fallbackDelay time.Duration, sleep func(time.Duration), customSleep bool, err error, log *slog.Logger) (bool, error) {
	delay, reason, ok := joinedPollRetryDelay(err)
	if !ok {
		return false, nil
	}

	if delay <= 0 {
		delay = fallbackDelay
	}
	if delay < 0 {
		delay = 0
	}

	if log != nil {
		log.Warn(reason, "delay", delay, "err", err)
	}

	if customSleep {
		sleep(delay)
		return true, nil
	}

	if delay == 0 {
		select {
		case <-sigCh:
			return true, nil
		default:
		}
		return true, nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-sigCh:
		return true, nil
	case <-timer.C:
		return true, nil
	}
}

type multiUnwrapper interface {
	Unwrap() []error
}

func joinedPollRetryDelay(err error) (time.Duration, string, bool) {
	if err == nil {
		return 0, "", false
	}

	maxDelay := time.Duration(0)
	foundRateLimit := false
	foundTransient := false
	foundOther := false

	var walk func(error)
	walk = func(current error) {
		if current == nil {
			return
		}

		if multi, ok := current.(multiUnwrapper); ok {
			children := multi.Unwrap()
			if len(children) == 0 {
				return
			}
			for _, child := range children {
				walk(child)
			}
			return
		}

		var rateLimitErr *ghclient.RateLimitError
		if errors.As(current, &rateLimitErr) {
			foundRateLimit = true
			if rateLimitErr.RetryAfter > maxDelay {
				maxDelay = rateLimitErr.RetryAfter
			}
			return
		}

		if isTransientGitHubServerError(current) {
			foundTransient = true
			return
		}

		foundOther = true
	}
	walk(err)

	if !foundRateLimit && !foundTransient && !foundOther {
		return 0, "", false
	}

	switch {
	case foundOther:
		return maxDelay, "poll error", true
	case foundRateLimit && !foundTransient:
		return maxDelay, "rate limited", true
	case foundTransient && !foundRateLimit:
		return maxDelay, "transient poll error", true
	default:
		return maxDelay, "retryable poll error", true
	}
}

func isTransientGitHubServerError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "http 500") ||
		strings.Contains(message, "http 502") ||
		strings.Contains(message, "http 503") ||
		strings.Contains(message, "http 504") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "i/o timeout") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "network is unreachable") ||
		strings.Contains(message, "operation timed out") ||
		strings.Contains(message, "tls handshake timeout") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "internal server error") ||
		strings.Contains(message, "bad gateway") ||
		strings.Contains(message, "service unavailable") ||
		strings.Contains(message, "gateway timeout")
}

func emitRecommendationEvents(opts RunOptions, before []db.Recommendation, notify func(ipc.Event)) error {
	if notify == nil || opts.RecommendationSnapshot == nil {
		return nil
	}
	after, err := opts.RecommendationSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot recommendations: %w", err)
	}
	for _, event := range diffRecommendationEvents(before, after) {
		notify(event)
	}
	return nil
}

func recommendationSnapshot(opts RunOptions) ([]db.Recommendation, error) {
	if opts.RecommendationSnapshot == nil {
		return nil, nil
	}
	return opts.RecommendationSnapshot()
}

func diffRecommendationEvents(before []db.Recommendation, after []db.Recommendation) []ipc.Event {
	beforeByID := make(map[string]db.Recommendation, len(before))
	for _, recommendation := range before {
		beforeByID[recommendation.ID] = recommendation
	}
	afterByID := make(map[string]db.Recommendation, len(after))
	for _, recommendation := range after {
		afterByID[recommendation.ID] = recommendation
	}

	events := make([]ipc.Event, 0, len(before)+len(after))
	for _, recommendation := range after {
		if _, ok := beforeByID[recommendation.ID]; ok {
			continue
		}
		events = append(events, ipc.Event{
			Type:             ipc.EventRecommendationCreated,
			RecommendationID: recommendation.ID,
			ItemID:           recommendation.ItemID,
		})
	}
	for _, recommendation := range before {
		if _, ok := afterByID[recommendation.ID]; ok {
			continue
		}
		events = append(events, ipc.Event{
			Type:             ipc.EventRecommendationRemoved,
			RecommendationID: recommendation.ID,
			ItemID:           recommendation.ItemID,
		})
	}
	return events
}

type eventBroadcaster struct {
	mu          sync.Mutex
	nextID      int
	subscribers map[int]eventSubscriber
	closed      bool
}

type eventSubscriber struct {
	itemID string
	ch     chan ipc.Event
}

func newEventBroadcaster() *eventBroadcaster {
	return &eventBroadcaster{subscribers: make(map[int]eventSubscriber)}
}

func (b *eventBroadcaster) Subscribe(itemID string) (<-chan ipc.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan ipc.Event, 16)
	if b.closed {
		close(ch)
		return ch, func() {}
	}
	id := b.nextID
	b.nextID++
	b.subscribers[id] = eventSubscriber{itemID: itemID, ch: ch}
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		subscriber, ok := b.subscribers[id]
		if !ok {
			return
		}
		delete(b.subscribers, id)
		close(subscriber.ch)
	}
}

func (b *eventBroadcaster) Publish(event ipc.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, subscriber := range b.subscribers {
		if subscriber.itemID != "" && subscriber.itemID != event.ItemID {
			continue
		}
		select {
		case subscriber.ch <- event:
		default:
		}
	}
}

func (b *eventBroadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, subscriber := range b.subscribers {
		delete(b.subscribers, id)
		close(subscriber.ch)
	}
}
