package ipc

import (
	"encoding/json"
	"sync/atomic"
	"time"
)

const (
	MethodHealth     = "health"
	MethodSubscribe  = "subscribe"
	MethodSyncStatus = "sync.status"
	MethodFixStart   = "fix.start"
)

// Poll-cycle phases reported via SyncStatusResult.Phase. Empty string
// means the daemon is idle between cycles.
const (
	PhaseSync   = "sync"
	PhaseAgents = "agents"
)

const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      int64           `json:"id"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      int64           `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

type HealthParams struct{}

type HealthResult struct {
	Status string `json:"status"`
}

type SubscribeParams struct {
	ItemID string `json:"item_id,omitempty"`
}

type SyncStatusParams struct{}

// SyncStatusResult describes the daemon's current and historical sync
// progress. Fields are zero-valued until the daemon completes its first
// cycle.
type SyncStatusResult struct {
	// Interval is the configured poll interval.
	Interval time.Duration `json:"interval"`
	// CycleCount is the number of poll cycles the daemon has finished.
	CycleCount int `json:"cycle_count"`
	// LastCycleStart / LastCycleEnd bracket the most recent poll cycle.
	LastCycleStart time.Time `json:"last_cycle_start"`
	LastCycleEnd   time.Time `json:"last_cycle_end"`
	// NextCycleAt is when the next poll cycle is scheduled to start.
	NextCycleAt time.Time `json:"next_cycle_at"`
	// Phase reports which stage of the cycle is in flight: "sync" while
	// repo data is being fetched, "agents" while triage is running, or
	// empty when the daemon is idle between cycles.
	Phase string `json:"phase,omitempty"`
	// CurrentRepo, CurrentIndex, and Total describe the in-flight sync
	// stage. Only set while phase = "sync".
	CurrentRepo  string `json:"current_repo,omitempty"`
	CurrentIndex int    `json:"current_index,omitempty"`
	Total        int    `json:"total,omitempty"`
	// AgentsTotal/AgentsDone describe progress through the agents stage.
	// CurrentItem is the item currently being triaged.
	AgentsTotal int    `json:"agents_total,omitempty"`
	AgentsDone  int    `json:"agents_done,omitempty"`
	CurrentItem string `json:"current_item,omitempty"`
	// LastCycleDuration is how long the most recently finished cycle
	// took. LastCycleOverran is true when that duration exceeded the
	// configured Interval - the daemon drained any tick that queued
	// during the overrun and is waiting for the next regular boundary.
	LastCycleDuration time.Duration `json:"last_cycle_duration,omitempty"`
	LastCycleOverran  bool          `json:"last_cycle_overran,omitempty"`
	// Repos is the per-repo timeline. Only repos the daemon has observed at
	// least once appear here.
	Repos []RepoSyncStatus `json:"repos"`
}

// RepoSyncStatus is the per-repo slice of SyncStatusResult.
type RepoSyncStatus struct {
	Repo          string    `json:"repo"`
	LastSyncStart time.Time `json:"last_sync_start"`
	LastSyncEnd   time.Time `json:"last_sync_end"`
	LastError     string    `json:"last_error,omitempty"`
	Syncing       bool      `json:"syncing,omitempty"`
}

type EventType string

const (
	EventRecommendationCreated EventType = "recommendation_created"
	EventRecommendationUpdated EventType = "recommendation_updated"
	EventRecommendationRemoved EventType = "recommendation_removed"
	EventDaemonStatus          EventType = "daemon_status"
	EventFixJobCreated         EventType = "fix_job_created"
	EventFixJobUpdated         EventType = "fix_job_updated"
)

type Event struct {
	Type             EventType `json:"type"`
	RecommendationID string    `json:"recommendation_id,omitempty"`
	ItemID           string    `json:"item_id,omitempty"`
	FixJobID         string    `json:"fix_job_id,omitempty"`
	Status           *string   `json:"status,omitempty"`
	Message          *string   `json:"message,omitempty"`
}

type FixStartParams struct {
	RecommendationID string `json:"recommendation_id"`
	OptionID         string `json:"option_id,omitempty"`
}

type FixStartResult struct {
	JobID  string `json:"job_id"`
	ItemID string `json:"item_id,omitempty"`
	Status string `json:"status"`
}

var reqID atomic.Int64

func NewRequest(method string, params interface{}) (*Request, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return &Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  raw,
		ID:      reqID.Add(1),
	}, nil
}

func NewResponse(id int64, result interface{}) (*Response, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{
		JSONRPC: "2.0",
		Result:  raw,
		ID:      id,
	}, nil
}

func NewErrorResponse(id int64, code int, message string) *Response {
	return &Response{
		JSONRPC: "2.0",
		Error:   &RPCError{Code: code, Message: message},
		ID:      id,
	}
}
