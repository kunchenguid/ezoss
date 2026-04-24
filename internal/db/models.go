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
	ID           string
	ItemID       string
	Agent        sharedtypes.AgentName
	Model        string
	TokensIn     int
	TokensOut    int
	CreatedAt    int64
	SupersededAt *time.Time
	Options      []RecommendationOption
}

type RecommendationOption struct {
	ID               string
	RecommendationID string
	Position         int
	StateChange      sharedtypes.StateChange
	Rationale        string
	DraftComment     string
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
	ItemID    string
	Agent     sharedtypes.AgentName
	Model     string
	TokensIn  int
	TokensOut int
	Options   []NewRecommendationOption
}

type NewRecommendationOption struct {
	StateChange    sharedtypes.StateChange
	Rationale      string
	DraftComment   string
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
