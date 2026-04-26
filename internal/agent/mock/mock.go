package mock

import (
	"strings"

	"github.com/kunchenguid/ezoss/internal/ghclient"
	"github.com/kunchenguid/ezoss/internal/triage"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

type Result struct {
	Agent          sharedtypes.AgentName
	Model          string
	Recommendation *triage.Recommendation
	TokensIn       int
	TokensOut      int
}

func Recommend(item ghclient.Item) Result {
	if item.Kind == sharedtypes.ItemKindPR {
		return Result{
			Agent: sharedtypes.AgentAuto,
			Model: "mock-pr-reviewer",
			Recommendation: &triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:  sharedtypes.StateChangeNone,
					Rationale:    "This PR changes behavior, but there is no prior agreement captured in the mock issue history.",
					WaitingOn:    sharedtypes.WaitingOnMaintainer,
					DraftComment: "Thanks for the PR. Before doing a full review, can you confirm this approach is something we want to ship?",
					FixPrompt:    "",
					Confidence:   sharedtypes.ConfidenceMedium,
					Followups:    []string{"Check whether there is a linked issue with prior maintainer approval."},
				}},
			},
			TokensIn:  6400,
			TokensOut: 520,
		}
	}

	if strings.Contains(strings.ToLower(item.Title), "follow-up") {
		return Result{
			Agent: sharedtypes.AgentAuto,
			Model: "mock-stale-triager",
			Recommendation: &triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:  sharedtypes.StateChangeClose,
					Rationale:    "The contributor has been inactive past the stale threshold, so it is reasonable to close this until they can provide more detail.",
					WaitingOn:    sharedtypes.WaitingOnContributor,
					DraftComment: "Closing this for now since we have not heard back on the requested follow-up. Happy to reopen once you can provide the missing details.",
					FixPrompt:    "",
					Confidence:   sharedtypes.ConfidenceHigh,
					Followups:    []string{"Reopen if the contributor replies with the missing logs."},
				}, {
					StateChange:  sharedtypes.StateChangeNone,
					Rationale:    "Alternatively, post one more friendly nudge before closing - some contributors come back when prompted directly.",
					WaitingOn:    sharedtypes.WaitingOnContributor,
					DraftComment: "Friendly ping - any update on this? Happy to keep it open if you're still working on it.",
					FixPrompt:    "",
					Confidence:   sharedtypes.ConfidenceMedium,
				}},
			},
			TokensIn:  3100,
			TokensOut: 280,
		}
	}

	return Result{
		Agent: sharedtypes.AgentAuto,
		Model: "mock-issue-triager",
		Recommendation: &triage.Recommendation{
			Options: []triage.RecommendationOption{{
				StateChange:  sharedtypes.StateChangeNone,
				Rationale:    "The report sounds actionable but still needs a reproduction or logs to narrow down the root cause.",
				WaitingOn:    sharedtypes.WaitingOnContributor,
				DraftComment: "Thanks for the report. Can you share the exact command you ran and any stack trace from the failing poll loop?",
				FixPrompt:    "Fix " + item.URL + " by reproducing the reported behavior, identifying the smallest correct code change, adding or updating regression tests, and verifying the relevant test suite passes.",
				Confidence:   sharedtypes.ConfidenceLow,
				Followups:    []string{"Check whether this reproduces against the latest main branch."},
			}},
		},
		TokensIn:  4200,
		TokensOut: 340,
	}
}
