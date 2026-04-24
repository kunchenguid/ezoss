package daemon

import (
	"errors"
	"testing"
	"time"
)

func newFixedTimeState(interval time.Duration, ticks ...time.Time) *SyncState {
	s := NewSyncState(interval)
	calls := 0
	s.now = func() time.Time {
		t := ticks[calls]
		if calls < len(ticks)-1 {
			calls++
		}
		return t
	}
	return s
}

func TestSyncState_BeginEndCycleAdvancesCounters(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Second)
	s := newFixedTimeState(5*time.Minute, t1, t2)

	s.BeginCycle([]string{"acme/widgets"})
	s.EndCycle()

	snap := s.Snapshot()
	if snap.CycleCount != 1 {
		t.Fatalf("CycleCount = %d, want 1", snap.CycleCount)
	}
	if !snap.LastCycleStart.Equal(t1) {
		t.Fatalf("LastCycleStart = %v, want %v", snap.LastCycleStart, t1)
	}
	if !snap.LastCycleEnd.Equal(t2) {
		t.Fatalf("LastCycleEnd = %v, want %v", snap.LastCycleEnd, t2)
	}
	if !snap.NextCycleAt.Equal(t2.Add(5 * time.Minute)) {
		t.Fatalf("NextCycleAt = %v, want LastCycleEnd + interval", snap.NextCycleAt)
	}
}

func TestSyncState_RepoBeginRepoEndUpdatesCurrentAndPerRepo(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(800 * time.Millisecond)
	s := newFixedTimeState(time.Minute, t1, t2)

	s.BeginCycle([]string{"acme/widgets"})
	s.RepoBegin("acme/widgets", 0, 1)

	snap := s.Snapshot()
	if snap.CurrentRepo != "acme/widgets" || snap.CurrentIndex != 1 || snap.Total != 1 {
		t.Fatalf("snapshot mid-cycle = %+v, want current=acme/widgets index=1 total=1", snap)
	}
	if len(snap.Repos) != 1 || !snap.Repos[0].Syncing {
		t.Fatalf("Repos[0] = %+v, want syncing=true", snap.Repos[0])
	}

	s.RepoEnd("acme/widgets", nil)
	snap = s.Snapshot()
	if snap.Repos[0].Syncing {
		t.Fatal("Repos[0].Syncing should be false after RepoEnd")
	}
	if snap.Repos[0].LastError != "" {
		t.Fatalf("Repos[0].LastError = %q, want empty on success", snap.Repos[0].LastError)
	}
	if !snap.Repos[0].LastSyncEnd.Equal(t2) {
		t.Fatalf("Repos[0].LastSyncEnd = %v, want %v", snap.Repos[0].LastSyncEnd, t2)
	}
}

func TestSyncState_RepoEndRecordsErrorString(t *testing.T) {
	s := NewSyncState(0)
	s.BeginCycle([]string{"acme/widgets"})
	s.RepoBegin("acme/widgets", 0, 1)
	s.RepoEnd("acme/widgets", errors.New("rate limited"))

	snap := s.Snapshot()
	if snap.Repos[0].LastError != "rate limited" {
		t.Fatalf("LastError = %q, want rate limited", snap.Repos[0].LastError)
	}
}

func TestSyncState_HooksBindToInstance(t *testing.T) {
	s := NewSyncState(0)
	hooks := s.Hooks()
	if hooks.OnRepoBegin == nil || hooks.OnRepoEnd == nil {
		t.Fatal("Hooks() returned nil callbacks")
	}
	hooks.OnRepoBegin("acme/widgets", 0, 1)
	hooks.OnRepoEnd("acme/widgets", nil)
	snap := s.Snapshot()
	if len(snap.Repos) != 1 || snap.Repos[0].Repo != "acme/widgets" {
		t.Fatalf("Repos = %+v, want one entry for acme/widgets", snap.Repos)
	}
}

func TestSyncState_PhaseTransitionsThroughSyncAndAgents(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := newFixedTimeState(time.Minute, t1, t1, t1, t1, t1, t1, t1, t1, t1, t1)

	s.BeginCycle([]string{"acme/widgets"})
	if got := s.Snapshot().Phase; got != "sync" {
		t.Fatalf("after BeginCycle phase = %q, want %q", got, "sync")
	}

	s.RepoBegin("acme/widgets", 0, 1)
	s.RepoEnd("acme/widgets", nil)

	s.BeginAgents(3)
	snap := s.Snapshot()
	if snap.Phase != "agents" {
		t.Fatalf("after BeginAgents phase = %q, want %q", snap.Phase, "agents")
	}
	if snap.AgentsTotal != 3 || snap.AgentsDone != 0 {
		t.Fatalf("AgentsTotal/Done = %d/%d, want 3/0", snap.AgentsTotal, snap.AgentsDone)
	}

	s.AgentItemBegin("acme/widgets#1", 0, 3)
	if got := s.Snapshot().CurrentItem; got != "acme/widgets#1" {
		t.Fatalf("CurrentItem = %q, want acme/widgets#1", got)
	}
	s.AgentItemEnd("acme/widgets#1", nil)
	if got := s.Snapshot().AgentsDone; got != 1 {
		t.Fatalf("AgentsDone = %d, want 1 after first item", got)
	}
	s.AgentItemBegin("acme/widgets#2", 1, 3)
	s.AgentItemEnd("acme/widgets#2", errors.New("triage failed"))
	// Errors still count as "done" - the item was attempted.
	if got := s.Snapshot().AgentsDone; got != 2 {
		t.Fatalf("AgentsDone = %d, want 2 after second (errored) item", got)
	}

	s.EndAgents()
	s.EndCycle()
	snap = s.Snapshot()
	if snap.Phase != "" {
		t.Fatalf("after EndCycle phase = %q, want empty", snap.Phase)
	}
	if snap.CurrentItem != "" {
		t.Fatalf("CurrentItem after EndCycle = %q, want empty", snap.CurrentItem)
	}
}

func TestSyncState_RecordCycleDurationFlagsOverrun(t *testing.T) {
	t.Parallel()

	s := NewSyncState(5 * time.Minute)
	s.RecordCycleDuration(2*time.Minute, 5*time.Minute)
	snap := s.Snapshot()
	if snap.LastCycleDuration != 2*time.Minute {
		t.Fatalf("LastCycleDuration = %v, want 2m", snap.LastCycleDuration)
	}
	if snap.LastCycleOverran {
		t.Fatal("LastCycleOverran should be false when duration <= interval")
	}

	s.RecordCycleDuration(7*time.Minute, 5*time.Minute)
	snap = s.Snapshot()
	if snap.LastCycleDuration != 7*time.Minute {
		t.Fatalf("LastCycleDuration = %v, want 7m", snap.LastCycleDuration)
	}
	if !snap.LastCycleOverran {
		t.Fatal("LastCycleOverran should be true when duration > interval")
	}
}

func TestSyncState_HooksWireAllPhaseCallbacks(t *testing.T) {
	s := NewSyncState(0)
	hooks := s.Hooks()
	if hooks.OnSyncBegin == nil || hooks.OnSyncEnd == nil {
		t.Fatal("Hooks() missing sync-phase callbacks")
	}
	if hooks.OnAgentsBegin == nil || hooks.OnAgentItemBegin == nil || hooks.OnAgentItemEnd == nil || hooks.OnAgentsEnd == nil {
		t.Fatal("Hooks() missing agents-phase callbacks")
	}

	s.BeginCycle([]string{"acme/widgets"})
	hooks.OnSyncBegin(1)
	hooks.OnRepoBegin("acme/widgets", 0, 1)
	hooks.OnRepoEnd("acme/widgets", nil)
	hooks.OnSyncEnd()
	hooks.OnAgentsBegin(2)
	hooks.OnAgentItemBegin("acme/widgets#1", 0, 2)
	hooks.OnAgentItemEnd("acme/widgets#1", nil)
	hooks.OnAgentsEnd()

	snap := s.Snapshot()
	if snap.AgentsTotal != 2 || snap.AgentsDone != 1 {
		t.Fatalf("AgentsTotal/Done = %d/%d, want 2/1 from hooks", snap.AgentsTotal, snap.AgentsDone)
	}
}

func TestSyncState_NilReceiverIsSafeToCall(t *testing.T) {
	var s *SyncState
	// All methods should no-op without panic so callers can pass a nil
	// SyncState through the daemon's RunOptions without conditional code.
	s.BeginCycle([]string{"acme/widgets"})
	s.RepoBegin("acme/widgets", 0, 1)
	s.RepoEnd("acme/widgets", nil)
	s.EndCycle()
	if got := s.Snapshot(); got.CycleCount != 0 || len(got.Repos) != 0 {
		t.Fatalf("nil snapshot = %+v, want zero value", got)
	}
}

func TestSyncState_NextCycleAtZeroWhenNoInterval(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := newFixedTimeState(0, t1, t1)
	s.BeginCycle(nil)
	s.EndCycle()
	if !s.Snapshot().NextCycleAt.IsZero() {
		t.Fatal("NextCycleAt should be zero when interval is zero")
	}
}
