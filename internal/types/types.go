package types

import (
	"database/sql/driver"
	"fmt"
)

type AgentName string

const (
	AgentAuto     AgentName = "auto"
	AgentClaude   AgentName = "claude"
	AgentCodex    AgentName = "codex"
	AgentRovoDev  AgentName = "rovodev"
	AgentOpenCode AgentName = "opencode"
)

func (a AgentName) IsSupported() bool {
	switch a {
	case AgentAuto, AgentClaude, AgentCodex, AgentRovoDev, AgentOpenCode:
		return true
	default:
		return false
	}
}

type ItemKind string

const (
	ItemKindIssue ItemKind = "issue"
	ItemKindPR    ItemKind = "pr"
)

// Role records whether ezoss is acting as the maintainer of the repo
// (handles writes such as labels, comments, merges) or as a contributor
// in a repo it does not own (read-only on the repo, push only to the PR
// head branch). Items default to maintainer for backwards compatibility.
type Role string

const (
	RoleMaintainer  Role = "maintainer"
	RoleContributor Role = "contributor"
)

func (r *Role) Scan(src any) error {
	return scanStringEnum("Role", src, (*string)(r))
}

func (r Role) Value() (driver.Value, error) {
	if r == "" {
		return string(RoleMaintainer), nil
	}
	return string(r), nil
}

func (r Role) IsValid() bool {
	switch r {
	case RoleMaintainer, RoleContributor:
		return true
	default:
		return false
	}
}

func (k *ItemKind) Scan(src any) error {
	return scanStringEnum("ItemKind", src, (*string)(k))
}

func (k ItemKind) Value() (driver.Value, error) {
	return string(k), nil
}

type ItemState string

const (
	ItemStateOpen   ItemState = "open"
	ItemStateClosed ItemState = "closed"
	ItemStateMerged ItemState = "merged"
)

func (s *ItemState) Scan(src any) error {
	return scanStringEnum("ItemState", src, (*string)(s))
}

func (s ItemState) Value() (driver.Value, error) {
	return string(s), nil
}

type WaitingOn string

const (
	WaitingOnMaintainer  WaitingOn = "maintainer"
	WaitingOnContributor WaitingOn = "contributor"
	WaitingOnCI          WaitingOn = "ci"
	WaitingOnNone        WaitingOn = "none"
)

func (w *WaitingOn) Scan(src any) error {
	return scanStringEnum("WaitingOn", src, (*string)(w))
}

func (w WaitingOn) Value() (driver.Value, error) {
	return string(w), nil
}

// StateChange represents the agent's proposed state transition or handoff.
// Most values map to GitHub item transitions composed with DraftComment on a
// recommendation. StateChangeFixRequired hands the item to a coding-agent fix
// prompt before a final GitHub state change.
type StateChange string

const (
	StateChangeNone           StateChange = "none"
	StateChangeClose          StateChange = "close"
	StateChangeMerge          StateChange = "merge"
	StateChangeRequestChanges StateChange = "request_changes"
	StateChangeFixRequired    StateChange = "fix_required"
)

func (s *StateChange) Scan(src any) error {
	return scanStringEnum("StateChange", src, (*string)(s))
}

func (s StateChange) Value() (driver.Value, error) {
	return string(s), nil
}

type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

func (c *Confidence) Scan(src any) error {
	return scanStringEnum("Confidence", src, (*string)(c))
}

func (c Confidence) Value() (driver.Value, error) {
	return string(c), nil
}

type ApprovalDecision string

const (
	ApprovalDecisionApproved  ApprovalDecision = "approved"
	ApprovalDecisionEdited    ApprovalDecision = "edited"
	ApprovalDecisionRejected  ApprovalDecision = "rejected"
	ApprovalDecisionDismissed ApprovalDecision = "dismissed"
)

func (d *ApprovalDecision) Scan(src any) error {
	return scanStringEnum("ApprovalDecision", src, (*string)(d))
}

func (d ApprovalDecision) Value() (driver.Value, error) {
	return string(d), nil
}

func scanStringEnum(name string, src any, dest *string) error {
	switch v := src.(type) {
	case string:
		*dest = v
		return nil
	case []byte:
		*dest = string(v)
		return nil
	case nil:
		*dest = ""
		return nil
	default:
		return fmt.Errorf("scan %s from %T", name, src)
	}
}
