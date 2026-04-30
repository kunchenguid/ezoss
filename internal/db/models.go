package db

import (
	"time"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

type Repo struct {
	ID                   string
	DefaultBranch        string
	LastPollAt           *time.Time
	LastTriagedRefreshAt *time.Time
	CreatedAt            int64
}

type Item struct {
	ID          string
	RepoID      string
	Kind        sharedtypes.ItemKind
	Number      int
	Title       string
	Author      string
	State       sharedtypes.ItemState
	IsDraft     bool
	GHTriaged   bool
	WaitingOn   sharedtypes.WaitingOn
	LastEventAt *time.Time
	StaleSince  *time.Time
	CreatedAt   int64
	UpdatedAt   int64
}

// Recommendation is one agent run on an item. The proposed actions live
// on Options - typically 2-3 when there are multiple reasonable next
// steps, or one when there's truly one obvious resolution. Options are
// ordered by Position, with 0 being the agent's top pick.
type Recommendation struct {
	ID                string
	ItemID            string
	Agent             sharedtypes.AgentName
	Model             string
	TokensIn          int
	TokensOut         int
	RerunInstructions string
	CreatedAt         int64
	CreatedAtNanos    int64
	SupersededAt      *time.Time
	Options           []RecommendationOption
}

type RecommendationOption struct {
	ID               string
	RecommendationID string
	Position         int
	StateChange      sharedtypes.StateChange
	Rationale        string
	DraftComment     string
	FixPrompt        string
	Followups        []string
	ProposedLabels   []string
	Confidence       sharedtypes.Confidence
	WaitingOn        sharedtypes.WaitingOn
	CreatedAt        int64
}

type RecommendationTokenTotals struct {
	TokensIn  int
	TokensOut int
}

type NewRecommendation struct {
	ItemID            string
	Agent             sharedtypes.AgentName
	Model             string
	TokensIn          int
	TokensOut         int
	RerunInstructions string
	Options           []NewRecommendationOption
}

type NewRecommendationOption struct {
	StateChange    sharedtypes.StateChange
	Rationale      string
	DraftComment   string
	FixPrompt      string
	Followups      []string
	ProposedLabels []string
	Confidence     sharedtypes.Confidence
	WaitingOn      sharedtypes.WaitingOn
}

type Approval struct {
	ID               string
	RecommendationID string
	OptionID         string
	Decision         sharedtypes.ApprovalDecision
	FinalComment     string
	FinalLabels      []string
	FinalStateChange sharedtypes.StateChange
	ActedAt          *time.Time
	ActedError       string
	CreatedAt        int64
}

type NewApproval struct {
	RecommendationID string
	OptionID         string
	Decision         sharedtypes.ApprovalDecision
	FinalComment     string
	FinalLabels      []string
	FinalStateChange sharedtypes.StateChange
	ActedAt          *time.Time
	ActedError       string
}

type FixJobStatus string

const (
	FixJobStatusQueued    FixJobStatus = "queued"
	FixJobStatusRunning   FixJobStatus = "running"
	FixJobStatusSucceeded FixJobStatus = "succeeded"
	FixJobStatusFailed    FixJobStatus = "failed"
	FixJobStatusCancelled FixJobStatus = "cancelled"
)

type FixJobPhase string

const (
	FixJobPhaseQueued            FixJobPhase = "queued"
	FixJobPhasePreparingWorktree FixJobPhase = "preparing_worktree"
	FixJobPhaseRunningAgent      FixJobPhase = "running_agent"
	FixJobPhaseCommitting        FixJobPhase = "committing"
	FixJobPhasePushing           FixJobPhase = "pushing"
	FixJobPhaseWaitingForPR      FixJobPhase = "waiting_for_pr"
	FixJobPhasePROpened          FixJobPhase = "pr_opened"
	FixJobPhaseFailed            FixJobPhase = "failed"
)

type FixJob struct {
	ID               string
	ItemID           string
	RecommendationID string
	OptionID         string
	RepoID           string
	ItemNumber       int
	ItemKind         sharedtypes.ItemKind
	Title            string
	FixPrompt        string
	Agent            sharedtypes.AgentName
	PRCreate         string
	Branch           string
	WorktreePath     string
	PRURL            string
	Status           FixJobStatus
	Phase            FixJobPhase
	Message          string
	Error            string
	CreatedAt        int64
	StartedAt        *time.Time
	UpdatedAt        int64
	CompletedAt      *time.Time
}

type NewFixJob struct {
	ItemID           string
	RecommendationID string
	OptionID         string
	RepoID           string
	ItemNumber       int
	ItemKind         sharedtypes.ItemKind
	Title            string
	FixPrompt        string
	Agent            sharedtypes.AgentName
	PRCreate         string
}

type FixJobUpdate struct {
	Status       FixJobStatus
	Phase        FixJobPhase
	Message      string
	Error        string
	Agent        sharedtypes.AgentName
	Branch       string
	WorktreePath string
	PRURL        string
}
