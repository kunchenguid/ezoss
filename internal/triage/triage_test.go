package triage

import (
	"encoding/json"
	"strings"
	"testing"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func TestParseSingleOptionRecommendation(t *testing.T) {
	t.Parallel()

	parsed, err := Parse([]byte(`{
		"options": [{
			"state_change":"close",
			"rationale":"Issue is a duplicate of #100 - linking and closing.",
			"waiting_on":"maintainer",
			"draft_comment":"Closing as duplicate of #100. Please follow the discussion there.",
			"confidence":"high",
			"followups":["Verify no further reports come in"]
		}]
	}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(parsed.Options) != 1 {
		t.Fatalf("len(Options) = %d, want 1", len(parsed.Options))
	}
	opt := parsed.Options[0]
	if opt.StateChange != sharedtypes.StateChangeClose {
		t.Fatalf("StateChange = %q", opt.StateChange)
	}
	if opt.WaitingOn != sharedtypes.WaitingOnMaintainer {
		t.Fatalf("WaitingOn = %q", opt.WaitingOn)
	}
	if opt.Confidence != sharedtypes.ConfidenceHigh {
		t.Fatalf("Confidence = %q", opt.Confidence)
	}
	if len(opt.Followups) != 1 || opt.Followups[0] != "Verify no further reports come in" {
		t.Fatalf("Followups = %#v", opt.Followups)
	}
	if opt.DraftComment == "" {
		t.Fatalf("DraftComment empty, want non-empty")
	}
}

// TestParseDropsProposedLabelsFromAgentResponse asserts the agent's user-label
// proposals are no longer part of the contract: the parser ignores any
// proposed_labels field if the model still emits one. User labels in foreign
// repos are unreliable (the agent can't see what labels exist) and used to
// cause atomic failures when applied via gh edit --add-label.
func TestParseDropsProposedLabelsFromAgentResponse(t *testing.T) {
	t.Parallel()

	parsed, err := Parse([]byte(`{
		"options": [{
			"state_change":"close",
			"rationale":"dup",
			"waiting_on":"maintainer",
			"draft_comment":"closing as dup",
			"proposed_labels":["agent-integration","mystery-label"],
			"confidence":"high"
		}]
	}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	// Inspect via JSON round-trip to assert the field is absent from the
	// in-memory representation entirely (not just empty).
	encoded, err := json.Marshal(parsed.Options[0])
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "proposed_labels") {
		t.Fatalf("encoded option = %s, want it to omit proposed_labels", string(encoded))
	}
}

func TestParseMultiOptionRecommendation(t *testing.T) {
	t.Parallel()

	parsed, err := Parse([]byte(`{
		"options": [
			{
				"state_change":"close",
				"rationale":"Stale - close after long inactivity.",
				"waiting_on":"contributor",
				"draft_comment":"Closing as stale. Feel free to reopen.",
				"confidence":"high"
			},
			{
				"state_change":"none",
				"rationale":"Or: one more nudge before closing.",
				"waiting_on":"contributor",
				"draft_comment":"Friendly ping - any update?",
				"confidence":"medium"
			}
		]
	}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(parsed.Options) != 2 {
		t.Fatalf("len(Options) = %d, want 2", len(parsed.Options))
	}
	if parsed.Options[0].StateChange != sharedtypes.StateChangeClose {
		t.Fatalf("Options[0].StateChange = %q, want close", parsed.Options[0].StateChange)
	}
	if parsed.Options[1].StateChange != sharedtypes.StateChangeNone {
		t.Fatalf("Options[1].StateChange = %q, want none", parsed.Options[1].StateChange)
	}
}

func TestParseRejectsEmptyOptions(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`{"options": []}`))
	if err == nil {
		t.Fatal("expected error for empty options")
	}
	if !strings.Contains(err.Error(), "missing options") {
		t.Fatalf("unexpected error %q", err.Error())
	}
}

func TestParseAllowsCommentOnlyWithStateChangeNone(t *testing.T) {
	t.Parallel()

	parsed, err := Parse([]byte(`{
		"options": [{
			"state_change":"none",
			"rationale":"Asking the contributor to confirm the approach.",
			"waiting_on":"contributor",
			"draft_comment":"Hey, can you confirm the approach is wanted before I review?",
			"confidence":"medium"
		}]
	}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.Options[0].StateChange != sharedtypes.StateChangeNone {
		t.Fatalf("StateChange = %q, want none", parsed.Options[0].StateChange)
	}
	if parsed.Options[0].DraftComment == "" {
		t.Fatalf("DraftComment empty, want non-empty")
	}
}

func TestParseRejectsUnsupportedEnums(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`{
		"options": [{
			"state_change":"ship_it",
			"rationale":"nope",
			"waiting_on":"maintainer",
			"draft_comment":"",
			"confidence":"medium"
		}]
	}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "unsupported state_change") {
		t.Fatalf("unexpected error %q", got)
	}
}

func TestSchemaWrapsOptions(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(Schema(), &schema); err != nil {
		t.Fatalf("Schema() returned invalid JSON: %v", err)
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	options, ok := properties["options"].(map[string]any)
	if !ok {
		t.Fatalf("schema options = %#v", properties["options"])
	}
	if options["type"] != "array" {
		t.Fatalf("options type = %v, want array", options["type"])
	}
	itemSchema, ok := options["items"].(map[string]any)
	if !ok {
		t.Fatalf("options items = %#v", options["items"])
	}
	itemProperties, ok := itemSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("item properties = %#v", itemSchema["properties"])
	}
	for _, field := range []string{"state_change", "rationale", "waiting_on", "draft_comment", "confidence", "followups"} {
		if _, ok := itemProperties[field]; !ok {
			t.Fatalf("option schema missing property %q", field)
		}
	}
	if _, ok := itemProperties["proposed_labels"]; ok {
		t.Fatalf("option schema must not include proposed_labels - the agent has no view of repo-specific labels and the field caused half-finished approvals when proposed labels didn't exist")
	}

	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "options" {
		t.Fatalf("schema required = %#v, want [options]", required)
	}
}

func TestPromptIncludesURLAndAgentInstructions(t *testing.T) {
	t.Parallel()

	prompt := Prompt("https://github.com/acme/widgets/issues/42", "Always ask for a repro before calling something a bug.")

	if !strings.Contains(prompt, "https://github.com/acme/widgets/issues/42") {
		t.Fatalf("prompt missing item URL: %q", prompt)
	}
	if !strings.Contains(prompt, "Always ask for a repro before calling something a bug.") {
		t.Fatalf("prompt missing AGENTS.md instructions: %q", prompt)
	}
	if !strings.Contains(prompt, "structured JSON") {
		t.Fatalf("prompt missing structured-output instruction: %q", prompt)
	}
	if strings.Contains(prompt, "You can clone") {
		t.Fatalf("prompt should not encourage ad hoc clones now that ezoss provides a managed checkout: %q", prompt)
	}
}

func TestPromptDescribesDecomposedActionFields(t *testing.T) {
	t.Parallel()

	prompt := Prompt("https://github.com/acme/widgets/pull/42", "")

	for _, want := range []string{
		"draft_comment",
		"state_change",
		"explain why we are closing and close",
		"merge an approved PR",
		"request changes on a PR",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q in:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "proposed_labels") {
		t.Fatalf("prompt mentions proposed_labels but the agent should no longer propose user labels:\n%s", prompt)
	}
}

func TestPromptEncouragesMultipleOptionsWhenReasonable(t *testing.T) {
	t.Parallel()

	prompt := Prompt("https://github.com/acme/widgets/issues/1", "")

	for _, want := range []string{
		"Prefer returning multiple options",
		"multiple reasonable next steps",
		"Return one option only when",
		"Don't pad with weak alternatives",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func TestPromptIncludesPRApprovalBeforeReviewGuidance(t *testing.T) {
	t.Parallel()

	prompt := Prompt("https://github.com/acme/widgets/pull/42", "")

	for _, want := range []string{
		"If the item is a pull request, first check whether it is linked to an issue where the approach was already discussed and agreed upon.",
		"set state_change 'none' and use draft_comment to ask whether the approach is wanted",
		"choose request_changes or merge",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt %q does not contain %q", prompt, want)
		}
	}
}
