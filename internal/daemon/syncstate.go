package daemon

import (
	"sync"
	"time"

	"github.com/kunchenguid/ezoss/internal/ipc"
)

// SyncState is the daemon's in-memory record of poll-cycle progress. It is
// thread-safe and serializes a snapshot for the IPC sync.status method.
type SyncState struct {
	mu sync.RWMutex

	now func() time.Time

	interval       time.Duration
	cycleCount     int
	lastCycleStart time.Time
	lastCycleEnd   time.Time
	nextCycleAt    time.Time

	phase        string
	currentRepo  string
	currentIndex int
	currentTotal int

	agentsTotal int
	agentsDone  int
	currentItem string

	lastCycleDuration time.Duration
	lastCycleOverran  bool

	repos     []repoSyncRecord
	repoIndex map[string]int
}


type repoSyncRecord struct {
	repo          string
	lastSyncStart time.Time
	lastSyncEnd   time.Time
	lastError     string
	syncing       bool
}

// NewSyncState constructs a SyncState configured with the given poll
// interval. The interval is reported back via the snapshot so callers can
// reason about cadence.
func NewSyncState(interval time.Duration) *SyncState {
	return &SyncState{
		now:       func() time.Time { return time.Now().UTC() },
		interval:  interval,
		repoIndex: make(map[string]int),
	}
}

// BeginCycle records the start of a poll cycle over the given repos
// and enters the sync phase.
func (s *SyncState) BeginCycle(repos []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCycleStart = s.now()
	s.currentTotal = len(repos)
	s.phase = ipc.PhaseSync
	s.nextCycleAt = time.Time{}
	s.agentsTotal = 0
	s.agentsDone = 0
	s.currentItem = ""
	for _, repoID := range repos {
		s.ensureRepoLocked(repoID)
	}
}

// EndCycle marks the cycle as finished and computes the next-cycle
// timestamp using the configured interval.
func (s *SyncState) EndCycle() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cycleCount++
	s.lastCycleEnd = now
	s.phase = ""
	s.currentRepo = ""
	s.currentIndex = 0
	s.currentItem = ""
	if s.interval > 0 {
		s.nextCycleAt = now.Add(s.interval)
	}
}

// BeginAgents transitions from the sync phase to the agents phase and
// seeds the expected total number of items to triage.
func (s *SyncState) BeginAgents(total int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = ipc.PhaseAgents
	s.agentsTotal = total
	s.agentsDone = 0
	s.currentRepo = ""
	s.currentIndex = 0
	s.currentItem = ""
}

// EndAgents clears the in-flight item but leaves AgentsDone/Total in
// place so a subsequent Snapshot can report final counts.
func (s *SyncState) EndAgents() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentItem = ""
}

// AgentItemBegin records the start of a single triage invocation.
func (s *SyncState) AgentItemBegin(itemID string, idx, total int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentItem = itemID
	if total > s.agentsTotal {
		s.agentsTotal = total
	}
}

// AgentItemEnd records the completion (success or failure) of a single
// triage invocation. Failures still increment AgentsDone since the
// item was attempted.
func (s *SyncState) AgentItemEnd(_ string, _ error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentsDone++
	s.currentItem = ""
}

// RecordCycleDuration captures how long the most recent cycle ran and
// flags it as overrun when duration exceeds interval. Callers use this
// to surface "cycle took longer than poll interval" in IPC.
func (s *SyncState) RecordCycleDuration(duration, interval time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCycleDuration = duration
	s.lastCycleOverran = interval > 0 && duration > interval
}

// RepoBegin marks a repo as currently syncing.
func (s *SyncState) RepoBegin(repo string, idx, total int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentRepo = repo
	s.currentIndex = idx + 1
	s.currentTotal = total
	i := s.ensureRepoLocked(repo)
	s.repos[i].syncing = true
	s.repos[i].lastSyncStart = s.now()
}

// RepoEnd records the result of a repo sync. A non-nil err sets the
// repo's last_error; nil clears it.
func (s *SyncState) RepoEnd(repo string, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.ensureRepoLocked(repo)
	s.repos[i].syncing = false
	s.repos[i].lastSyncEnd = s.now()
	if err != nil {
		s.repos[i].lastError = err.Error()
	} else {
		s.repos[i].lastError = ""
	}
}

// Hooks returns PollHooks bound to this state. Pass into Poller.Hooks so
// PollOnce can publish per-repo and per-item progress without knowing
// about SyncState.
func (s *SyncState) Hooks() PollHooks {
	if s == nil {
		return PollHooks{}
	}
	return PollHooks{
		OnSyncBegin:      func(int) {},
		OnRepoBegin:      s.RepoBegin,
		OnRepoEnd:        s.RepoEnd,
		OnSyncEnd:        func() {},
		OnAgentsBegin:    s.BeginAgents,
		OnAgentItemBegin: s.AgentItemBegin,
		OnAgentItemEnd:   s.AgentItemEnd,
		OnAgentsEnd:      s.EndAgents,
	}
}

// Snapshot returns a serializable copy suitable for IPC.
func (s *SyncState) Snapshot() ipc.SyncStatusResult {
	if s == nil {
		return ipc.SyncStatusResult{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	repos := make([]ipc.RepoSyncStatus, 0, len(s.repos))
	for _, r := range s.repos {
		repos = append(repos, ipc.RepoSyncStatus{
			Repo:          r.repo,
			LastSyncStart: r.lastSyncStart,
			LastSyncEnd:   r.lastSyncEnd,
			LastError:     r.lastError,
			Syncing:       r.syncing,
		})
	}
	return ipc.SyncStatusResult{
		Interval:       s.interval,
		CycleCount:     s.cycleCount,
		LastCycleStart: s.lastCycleStart,
		LastCycleEnd:   s.lastCycleEnd,
		NextCycleAt:    s.nextCycleAt,
		Phase:          s.phase,
		CurrentRepo:    s.currentRepo,
		CurrentIndex:   s.currentIndex,
		Total:          s.currentTotal,
		AgentsTotal:       s.agentsTotal,
		AgentsDone:        s.agentsDone,
		CurrentItem:       s.currentItem,
		LastCycleDuration: s.lastCycleDuration,
		LastCycleOverran:  s.lastCycleOverran,
		Repos:             repos,
	}
}

func (s *SyncState) ensureRepoLocked(repo string) int {
	if i, ok := s.repoIndex[repo]; ok {
		return i
	}
	idx := len(s.repos)
	s.repos = append(s.repos, repoSyncRecord{repo: repo})
	s.repoIndex[repo] = idx
	return idx
}

// PollHooks lets callers observe poll-cycle progress. All fields are
// optional; PollOnce checks for nil before invoking each.
type PollHooks struct {
	// Sync stage (per-repo GitHub data fetch).
	OnSyncBegin func(total int)
	OnRepoBegin func(repo string, idx, total int)
	OnRepoEnd   func(repo string, err error)
	OnSyncEnd   func()
	// Agents stage (per-item triage).
	OnAgentsBegin    func(total int)
	OnAgentItemBegin func(itemID string, idx, total int)
	OnAgentItemEnd   func(itemID string, err error)
	OnAgentsEnd      func()
}
