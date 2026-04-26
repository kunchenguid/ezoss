package triage

import (
	"encoding/json"
	"fmt"
	"strings"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

// Recommendation is the agent's structured triage output. Options is a
// non-empty list of resolutions the agent is proposing for the item;
// the agent is encouraged to return 2-3 options whenever there are
// multiple reasonable next steps, and to return a single option only
// when there's truly one obvious resolution.
type Recommendation struct {
	Options []RecommendationOption `json:"options"`
}

// RecommendationOption is one self-contained proposed resolution. The
// action it proposes is decomposed: post DraftComment if non-empty and
// apply StateChange (none|close|merge|request_changes). User-namespaced
// labels are deliberately not part of the agent's contract - the agent
// has no reliable view of which labels exist in the target repo, and any
// proposed but missing label used to break the atomic gh edit --add-label
// call. Lifecycle labels in the ezoss/* namespace are managed
// automatically downstream.
type RecommendationOption struct {
	StateChange  sharedtypes.StateChange `json:"state_change"`
	Rationale    string                  `json:"rationale"`
	WaitingOn    sharedtypes.WaitingOn   `json:"waiting_on"`
	DraftComment string                  `json:"draft_comment"`
	Confidence   sharedtypes.Confidence  `json:"confidence"`
	Followups    []string                `json:"followups,omitempty"`
}

var schema = json.RawMessage(`{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"options": {
			"type": "array",
			"minItems": 1,
			"description": "Ordered list of proposed resolutions, agent's top pick first. Prefer returning multiple options (2-3) whenever there are multiple reasonable next steps; return one option only when there's truly one obvious resolution. Don't pad with weak alternatives.",
			"items": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"state_change": {
						"type": "string",
						"description": "The state transition to apply. 'none' means no state change.",
						"enum": ["none", "close", "merge", "request_changes"]
					},
					"rationale": {"type": "string"},
					"waiting_on": {
						"type": "string",
						"enum": ["maintainer", "contributor", "ci", "none"]
					},
					"draft_comment": {
						"type": "string",
						"description": "Comment to post on the item. Empty string means do not post a comment."
					},
					"confidence": {
						"type": "string",
						"enum": ["low", "medium", "high"]
					},
					"followups": {
						"type": "array",
						"items": {"type": "string"}
					}
				},
				"required": ["state_change", "rationale", "waiting_on", "draft_comment", "confidence"]
			}
		}
	},
	"required": ["options"]
}`)

func Schema() json.RawMessage {
	return append(json.RawMessage(nil), schema...)
}

func Prompt(itemURL string, agentsInstructions string) string {
	var b strings.Builder
	b.WriteString("Triage this GitHub issue or pull request and return structured JSON matching the provided schema.\n\n")
	b.WriteString("Item URL:\n")
	b.WriteString(itemURL)
	b.WriteString("\n\n")
	b.WriteString("Inspect the managed repository checkout provided in the execution context, plus any issue comments, pull request diff, linked issues, or CI context you need before deciding. Do not create ad hoc clones unless the provided checkout is unavailable.\n\n")
	b.WriteString("Return one or more options. Each option is a self-contained proposed resolution with these fields:\n")
	b.WriteString("- draft_comment: the comment to post on the item, or empty string if no comment.\n")
	b.WriteString("- state_change: the state transition to apply: 'none', 'close', 'merge', or 'request_changes'.\n")
	b.WriteString("- waiting_on: who the item is waiting on after this action.\n")
	b.WriteString("- confidence: how sure you are this is the right resolution.\n\n")
	b.WriteString("How many options to return:\n")
	b.WriteString("- Prefer returning multiple options (2-3) whenever there are multiple reasonable next steps. Surfacing the alternatives helps the maintainer pick instead of forcing them to think of the alternatives themselves. Examples: close-as-stale vs. one-more-nudge, merge-as-is vs. request small changes, ask for repro vs. close as 'works for me', approve label-only vs. ask a clarifying question.\n")
	b.WriteString("- Return one option only when there's truly one obvious resolution and no other next step is reasonable (e.g. unambiguous duplicate, fully approved PR ready to merge, spam).\n")
	b.WriteString("- Order options with your top pick first.\n")
	b.WriteString("- Don't pad with weak alternatives just to fill the list. Each option must be a genuinely reasonable next step on its own.\n\n")
	b.WriteString("Common combinations within an option:\n")
	b.WriteString("- ask the contributor a question: draft_comment set, state_change 'none'.\n")
	b.WriteString("- explain why we are closing and close: draft_comment set, state_change 'close'.\n")
	b.WriteString("- merge an approved PR: state_change 'merge' (draft_comment optional).\n")
	b.WriteString("- request changes on a PR: draft_comment set with the review feedback, state_change 'request_changes'.\n")
	b.WriteString("- mark triaged with no further action: draft_comment empty, state_change 'none'.\n\n")
	b.WriteString("If the item is a pull request, first check whether it is linked to an issue where the approach was already discussed and agreed upon. If there is no prior agreement, set state_change 'none' and use draft_comment to ask whether the approach is wanted - do not request_changes or merge until the approach is confirmed. If there is prior agreement, proceed with code review and choose request_changes or merge based on the review.\n")
	if strings.TrimSpace(agentsInstructions) != "" {
		b.WriteString("\nUser instructions from ~/.ezoss/AGENTS.md:\n")
		b.WriteString(strings.TrimSpace(agentsInstructions))
		b.WriteString("\n")
	}
	return b.String()
}

func Parse(data []byte) (*Recommendation, error) {
	var rec Recommendation
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("decode triage recommendation: %w", err)
	}
	if len(rec.Options) == 0 {
		return nil, fmt.Errorf("recommendation missing options")
	}
	for i := range rec.Options {
		opt := &rec.Options[i]
		if !isSupportedStateChange(opt.StateChange) {
			return nil, fmt.Errorf("option %d: unsupported state_change %q", i, opt.StateChange)
		}
		if !isSupportedWaitingOn(opt.WaitingOn) {
			return nil, fmt.Errorf("option %d: unsupported waiting_on %q", i, opt.WaitingOn)
		}
		if !isSupportedConfidence(opt.Confidence) {
			return nil, fmt.Errorf("option %d: unsupported confidence %q", i, opt.Confidence)
		}
	}
	return &rec, nil
}

func isSupportedStateChange(value sharedtypes.StateChange) bool {
	switch value {
	case sharedtypes.StateChangeNone,
		sharedtypes.StateChangeClose,
		sharedtypes.StateChangeMerge,
		sharedtypes.StateChangeRequestChanges:
		return true
	default:
		return false
	}
}

func isSupportedWaitingOn(value sharedtypes.WaitingOn) bool {
	switch value {
	case sharedtypes.WaitingOnMaintainer,
		sharedtypes.WaitingOnContributor,
		sharedtypes.WaitingOnCI,
		sharedtypes.WaitingOnNone:
		return true
	default:
		return false
	}
}

func isSupportedConfidence(value sharedtypes.Confidence) bool {
	switch value {
	case sharedtypes.ConfidenceLow, sharedtypes.ConfidenceMedium, sharedtypes.ConfidenceHigh:
		return true
	default:
		return false
	}
}
