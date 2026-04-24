package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
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

func RunWithOptions(pidFile string, sigCh <-chan os.Signal, opts RunOptions) error {
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

	var notify func(ipc.Event)
	if opts.IPCPath != "" {
		var stopIPC func()
		stopIPCFn, notifyFn, err := startIPCServer(opts.IPCPath, opts.RecommendationSnapshot, opts.SyncState)
		if err != nil {
			return fmt.Errorf("start ipc server: %w", err)
		}
		stopIPC = stopIPCFn
		notify = notifyFn
		defer stopIPC()
	}

	if err := runPollLoop(sigCh, opts, notify); err != nil {
		return err
	}

	return nil
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
		<-sigCh
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

	before, err := recommendationSnapshot(opts)
	if err != nil {
		return fmt.Errorf("snapshot recommendations: %w", err)
	}
	cycleStart := time.Now()
	opts.SyncState.BeginCycle(opts.Repos)
	if err := opts.PollOnce(context.Background(), append([]string(nil), opts.Repos...)); err != nil {
		opts.SyncState.EndCycle()
		opts.SyncState.RecordCycleDuration(time.Since(cycleStart), interval)
		if handled, waitErr := handlePollError(sigCh, interval, sleep, customSleep, err); handled {
			if waitErr != nil {
				return waitErr
			}
			goto waitForTicker
		}
		return fmt.Errorf("poll daemon loop: %w", err)
	}
	opts.SyncState.EndCycle()
	opts.SyncState.RecordCycleDuration(time.Since(cycleStart), interval)
	if err := emitRecommendationEvents(opts, before, notify); err != nil {
		return err
	}

waitForTicker:
	ticker := newTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			return nil
		case <-ticker.Chan():
			before, err := recommendationSnapshot(opts)
			if err != nil {
				return fmt.Errorf("snapshot recommendations: %w", err)
			}
			cycleStart := time.Now()
			opts.SyncState.BeginCycle(opts.Repos)
			pollErr := opts.PollOnce(context.Background(), append([]string(nil), opts.Repos...))
			opts.SyncState.EndCycle()
			cycleDuration := time.Since(cycleStart)
			opts.SyncState.RecordCycleDuration(cycleDuration, interval)
			if cycleDuration > interval {
				// Cycle ran longer than the configured poll
				// interval. Drop any tick that queued during
				// the overrun so the next iteration waits for
				// the next regular tick boundary instead of
				// running another cycle back-to-back.
				if drainOverrunTick(ticker) {
					fmt.Fprintf(os.Stderr, "cycle took %s, exceeds poll interval %s; skipping queued tick\n", cycleDuration, interval)
				} else {
					fmt.Fprintf(os.Stderr, "cycle took %s, exceeds poll interval %s\n", cycleDuration, interval)
				}
			}
			if pollErr != nil {
				if handled, waitErr := handlePollError(sigCh, interval, sleep, customSleep, pollErr); handled {
					if waitErr != nil {
						return waitErr
					}
					continue
				}
				return fmt.Errorf("poll daemon loop: %w", pollErr)
			}
			if err := emitRecommendationEvents(opts, before, notify); err != nil {
				return err
			}
		}
	}
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

func handlePollError(sigCh <-chan os.Signal, fallbackDelay time.Duration, sleep func(time.Duration), customSleep bool, err error) (bool, error) {
	delay, ok := joinedRateLimitDelay(err)
	if !ok {
		return false, nil
	}

	if delay <= 0 {
		delay = fallbackDelay
	}
	if delay < 0 {
		delay = 0
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

func joinedRateLimitDelay(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}

	maxDelay := time.Duration(0)
	foundRateLimit := false

	var walk func(error) bool
	walk = func(current error) bool {
		if current == nil {
			return true
		}

		if multi, ok := current.(multiUnwrapper); ok {
			children := multi.Unwrap()
			if len(children) == 0 {
				return true
			}
			for _, child := range children {
				if !walk(child) {
					return false
				}
			}
			return true
		}

		var rateLimitErr *ghclient.RateLimitError
		if !errors.As(current, &rateLimitErr) {
			return false
		}
		foundRateLimit = true
		if rateLimitErr.RetryAfter > maxDelay {
			maxDelay = rateLimitErr.RetryAfter
		}
		return true
	}

	if !walk(err) || !foundRateLimit {
		return 0, false
	}

	return maxDelay, true
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
