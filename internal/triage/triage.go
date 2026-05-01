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
// apply StateChange (none|close|merge|request_changes|fix_required).
// FixPrompt carries the coding-agent handoff for fix_required options.
// User-namespaced labels are deliberately not part of the agent's contract -
// the agent has no reliable view of which labels exist in the target repo, and any
// proposed but missing label used to break the atomic gh edit --add-label
// call. Lifecycle labels in the ezoss/* namespace are managed
// automatically downstream.
type RecommendationOption struct {
	StateChange  sharedtypes.StateChange `json:"state_change"`
	Rationale    string                  `json:"rationale"`
	WaitingOn    sharedtypes.WaitingOn   `json:"waiting_on"`
	DraftComment string                  `json:"draft_comment"`
	FixPrompt    string                  `json:"fix_prompt"`
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
						"description": "The state transition to apply. 'none' means no state change. 'fix_required' means the item is legitimate and should be handed to a coding agent before closing.",
						"enum": ["none", "close", "merge", "request_changes", "fix_required"]
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
					"fix_prompt": {
						"type": "string",
						"description": "Prompt the maintainer can copy into a coding agent when the item is a legitimate actionable issue or PR. Include the original GitHub URL and enough investigation context for an agent to start fixing. Prefer readable multi-line Markdown with short sections. Empty string means no coding-agent handoff is recommended."
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
				"required": ["state_change", "rationale", "waiting_on", "draft_comment", "fix_prompt", "confidence"]
			}
		}
	},
	"required": ["options"]
}`)

func Schema() json.RawMessage {
	return append(json.RawMessage(nil), schema...)
}

// Role is re-exported here so callers don't have to import the
// sharedtypes package just to pick a prompt variant.
type Role = sharedtypes.Role

const (
	RoleMaintainer  = sharedtypes.RoleMaintainer
	RoleContributor = sharedtypes.RoleContributor
)

// Prompt returns the maintainer-mode prompt. Kept for backwards
// compatibility with callers that don't yet thread role through.
func Prompt(itemURL string, agentsInstructions string) string {
	return PromptWithRerunInstructions(itemURL, agentsInstructions, "")
}

// PromptWithRerunInstructions returns the maintainer-mode prompt with
// optional rerun instructions appended. Kept as the existing entry
// point so existing callers keep compiling.
func PromptWithRerunInstructions(itemURL string, agentsInstructions string, rerunInstructions string) string {
	return PromptForRole(RoleMaintainer, itemURL, agentsInstructions, rerunInstructions)
}

// PromptForRole returns the right prompt variant for the given role.
// The schema and parser are unchanged; only the in-bounds set of
// state_change values and the example combinations differ. Maintainer
// prompts are unchanged from before; contributor prompts narrow the
// in-bounds set to none/close/fix_required because we do not have
// merge or request_changes authority on repos we don't maintain.
func PromptForRole(role Role, itemURL string, agentsInstructions string, rerunInstructions string) string {
	if role == RoleContributor {
		return contributorPrompt(itemURL, agentsInstructions, rerunInstructions)
	}
	return maintainerPrompt(itemURL, agentsInstructions, rerunInstructions)
}

func maintainerPrompt(itemURL string, agentsInstructions string, rerunInstructions string) string {
	var b strings.Builder
	b.WriteString("Triage this GitHub issue or pull request and return structured JSON matching the provided schema.\n\n")
	b.WriteString("Item URL:\n")
	b.WriteString(itemURL)
	b.WriteString("\n\n")
	b.WriteString("Inspect the managed repository checkout provided in the execution context, plus any issue comments, pull request diff, linked issues, or CI context you need before deciding. Do not create ad hoc clones unless the provided checkout is unavailable.\n\n")
	b.WriteString("You are acting as the MAINTAINER of this repo. You can close, merge, request changes, comment, and hand work to a coding agent.\n\n")
	b.WriteString("Return one or more options. Each option is a self-contained proposed resolution with these fields:\n")
	b.WriteString("- draft_comment: the comment to post on the item, or empty string if no comment.\n")
	b.WriteString("- fix_prompt: a prompt the maintainer can copy into a coding agent when the item is a legitimate actionable issue or PR, or empty string if no coding-agent handoff is useful. Include the original issue/PR URL and enough context from your investigation for the coding agent to work on it. Prefer readable multi-line Markdown with short sections over a single long paragraph.\n")
	b.WriteString("- state_change: the state transition to apply: 'none', 'close', 'merge', 'request_changes', or 'fix_required'. Use 'fix_required' when the item is legitimate and should be handed to a coding agent before it can be closed.\n")
	b.WriteString("- waiting_on: who the item is waiting on after this action.\n")
	b.WriteString("- confidence: how sure you are this is the right resolution.\n\n")
	b.WriteString("How many options to return:\n")
	b.WriteString("- Prefer returning multiple options (2-3) whenever there are multiple reasonable next steps. Surfacing the alternatives helps the maintainer pick instead of forcing them to think of the alternatives themselves. Examples: close-as-stale vs. one-more-nudge, merge-as-is vs. request small changes, ask for repro vs. close as 'works for me', approve label-only vs. ask a clarifying question.\n")
	b.WriteString("- Return one option only when there's truly one obvious resolution and no other next step is reasonable (e.g. unambiguous duplicate, fully approved PR ready to merge, spam).\n")
	b.WriteString("- Always include at least one option primarily about accepting the incoming item when it is a legitimate good-faith issue or pull request, even if your top pick is to ask for changes, ask a question, or close it: for an issue, acknowledge the contribution and include a useful fix_prompt if appropriate; for a pull request, acknowledge the contribution and set state_change 'merge'.\n")
	b.WriteString("- Order options with your top pick first.\n")
	b.WriteString("- Don't pad with weak alternatives just to fill the list. Each option must be a genuinely reasonable next step on its own.\n\n")
	b.WriteString("Common combinations within an option:\n")
	b.WriteString("- ask the contributor a question: draft_comment set, state_change 'none'.\n")
	b.WriteString("- explain why we are closing and close: draft_comment set, state_change 'close'.\n")
	b.WriteString("- merge an approved PR: state_change 'merge' (draft_comment optional).\n")
	b.WriteString("- request changes on a PR: draft_comment set with the review feedback, state_change 'request_changes'.\n")
	b.WriteString("- hand a legitimate bug or feature request to a coding agent: fix_prompt set, state_change 'fix_required'.\n")
	b.WriteString("- mark triaged with no further action: draft_comment empty, state_change 'none'.\n\n")
	b.WriteString("For legitimate actionable issues, prefer state_change 'fix_required' and set fix_prompt to the handoff for a coding agent. The prompt should include the original URL, summary, reproduction or evidence, suspected files/components if found, acceptance criteria, and verification steps. Format it as multi-line Markdown so it is readable in a terminal and directly runnable by ezoss fix.\n\n")
	b.WriteString("If the item is a pull request, first check whether it is linked to an issue where the approach was already discussed and agreed upon. If there is no prior agreement, set state_change 'none' and use draft_comment to ask whether the approach is wanted - do not request_changes or merge until the approach is confirmed. If there is prior agreement, proceed with code review and choose request_changes or merge based on the review.\n")
	appendInstructions(&b, agentsInstructions, rerunInstructions)
	return b.String()
}

func contributorPrompt(itemURL string, agentsInstructions string, rerunInstructions string) string {
	var b strings.Builder
	b.WriteString("Triage this GitHub issue or pull request and return structured JSON matching the provided schema.\n\n")
	b.WriteString("Item URL:\n")
	b.WriteString(itemURL)
	b.WriteString("\n\n")
	b.WriteString("You are acting as a CONTRIBUTOR on a repo you do not maintain. The item is something you authored: an issue you filed or a pull request you opened against an upstream repo. The maintainer is someone else; you have no authority to merge, request changes, or apply labels there.\n\n")
	b.WriteString("Inspect the issue/PR thread, the PR diff if applicable, and any linked discussion. If a managed checkout of the upstream repo is provided, use it for context, but do not assume write access to it.\n\n")
	b.WriteString("Allowed state_change values for contributor mode (the schema enum is unchanged, but only this subset is in-bounds for you here):\n")
	b.WriteString("- 'none': leave the item open. Use this for replies, pings, and acknowledgments.\n")
	b.WriteString("- 'close': close my own issue or abandon my own PR.\n")
	b.WriteString("- 'fix_required': hand the item to a coding agent. For a contributor PR this means push more commits to the EXISTING PR branch (do not propose creating a new PR). For an authored issue this is rarely useful; prefer 'none'.\n\n")
	b.WriteString("Do NOT use 'merge' or 'request_changes' - those are maintainer actions on a repo you don't own.\n\n")
	b.WriteString("Each option fields:\n")
	b.WriteString("- draft_comment: a reply you would post on the thread, or empty string if no comment. Common contributor uses: reply to a reviewer, ping a silent reviewer, ask a clarifying question, withdraw the proposal.\n")
	b.WriteString("- fix_prompt: when state_change is 'fix_required', a prompt the contributor can copy into a coding agent to push more commits to the existing PR branch. Include the upstream PR URL, the head branch (the one to push to), the reviewer's feedback, suspected files, and verification steps. Examples: 'address review feedback in <files>', 'rebase against base, resolve conflicts'. Empty string if no coding-agent handoff is useful.\n")
	b.WriteString("- state_change: 'none', 'close', or 'fix_required' as described above.\n")
	b.WriteString("- waiting_on: who the item is waiting on after this action. After replying or pushing fixes, this is usually the maintainer.\n")
	b.WriteString("- confidence: how sure you are this is the right next step.\n\n")
	b.WriteString("How many options to return:\n")
	b.WriteString("- Prefer 2-3 options when there are multiple reasonable next steps (e.g. address feedback now vs. ping for clarification, push a rebase vs. close-and-replace).\n")
	b.WriteString("- Return one option when the next step is unambiguous (e.g. reviewer asked a direct question - reply, nothing else).\n")
	b.WriteString("- Order options with your top pick first.\n\n")
	b.WriteString("Common combinations within an option:\n")
	b.WriteString("- reply to a reviewer: draft_comment set, state_change 'none'.\n")
	b.WriteString("- ping a silent reviewer: draft_comment set (a short polite nudge), state_change 'none'.\n")
	b.WriteString("- push more commits to the PR: state_change 'fix_required', fix_prompt set with the work to do.\n")
	b.WriteString("- rebase against base: state_change 'fix_required', fix_prompt 'rebase against base, resolve conflicts'.\n")
	b.WriteString("- abandon my own PR: state_change 'close', draft_comment optional.\n")
	b.WriteString("- close my own issue (resolved or no longer relevant): state_change 'close'.\n\n")
	b.WriteString("Nothing in this output is posted to GitHub automatically; the maintainer (you) reviews each option in the inbox before any action is taken.\n")
	appendInstructions(&b, agentsInstructions, rerunInstructions)
	return b.String()
}

func appendInstructions(b *strings.Builder, agentsInstructions, rerunInstructions string) {
	if strings.TrimSpace(agentsInstructions) != "" {
		b.WriteString("\nUser instructions from ~/.ezoss/AGENTS.md:\n")
		b.WriteString(strings.TrimSpace(agentsInstructions))
		b.WriteString("\n")
	}
	if strings.TrimSpace(rerunInstructions) != "" {
		b.WriteString("\nMaintainer-provided rerun instructions:\n")
		b.WriteString(strings.TrimSpace(rerunInstructions))
		b.WriteString("\n\n")
		b.WriteString("Use these instructions as additional context for this rerun. Do not treat them as GitHub-visible text. If they conflict with repository evidence or project policy, explain the conflict in the recommendation.\n")
	}
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
		sharedtypes.StateChangeRequestChanges,
		sharedtypes.StateChangeFixRequired:
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
