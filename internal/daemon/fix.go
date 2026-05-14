package daemon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kunchenguid/ezoss/internal/db"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

const (
	succeededFixJobRefreshInterval = time.Hour
	succeededFixJobRefreshLimit    = 25
)

func runFixStage(ctx context.Context, poller Poller) (bool, error) {
	if poller.DB == nil || poller.Fix == nil {
		return false, nil
	}

	reclaimed, err := poller.DB.ReclaimStaleRunningFixJobs(time.Now().Add(-fixJobTimeout(poller)))
	if err != nil {
		return false, err
	}
	reclaimedWork := reclaimed > 0

	if didWork, err := detectWaitingFixPRs(ctx, poller); didWork || err != nil {
		return reclaimedWork || didWork, err
	}
	if err := refreshSucceededFixJobItems(ctx, poller); err != nil {
		poller.log().Warn("refresh succeeded fix job items failed", "err", err)
	}

	job, err := poller.DB.ClaimNextQueuedFixJob()
	if err != nil {
		return false, err
	}
	if job == nil {
		return reclaimedWork, nil
	}
	progress := func(update db.FixJobUpdate) error {
		if update.Status == "" {
			update.Status = db.FixJobStatusRunning
		}
		return poller.DB.UpdateFixJob(job.ID, update)
	}
	fixCtx, cancel := context.WithTimeout(ctx, fixJobTimeout(poller))
	defer cancel()
	result, err := poller.Fix.RunFix(fixCtx, *job, progress)
	if err != nil {
		_ = poller.DB.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusFailed, Phase: db.FixJobPhaseFailed, Error: err.Error()})
		return true, fmt.Errorf("fix job %s: %w", job.ID, err)
	}
	if result == nil {
		result = &FixResult{}
	}
	update := db.FixJobUpdate{
		Branch:       result.Branch,
		WorktreePath: result.WorktreePath,
	}
	if strings.TrimSpace(result.PRURL) != "" {
		update.Status = db.FixJobStatusSucceeded
		update.Phase = db.FixJobPhasePROpened
		update.PRURL = result.PRURL
		update.Message = "PR opened"
	} else if result.WaitingForPR {
		update.Status = db.FixJobStatusRunning
		update.Phase = db.FixJobPhaseWaitingForPR
		update.Message = "waiting for no-mistakes pipeline to finish"
	} else if result.WaitingForManualReview {
		update.Status = db.FixJobStatusRunning
		update.Phase = db.FixJobPhaseWaitingForPR
		update.Message = "waiting for manual review"
	} else {
		update.Status = db.FixJobStatusSucceeded
		update.Phase = db.FixJobPhasePROpened
		update.Message = "fix completed"
	}
	if err := poller.DB.UpdateFixJob(job.ID, update); err != nil {
		return true, err
	}
	return true, nil
}

func detectWaitingFixPRs(ctx context.Context, poller Poller) (bool, error) {
	waiting, err := poller.DB.ListFixJobsByStatus(db.FixJobStatusRunning)
	if err != nil {
		return false, err
	}
	didWork := false
	for _, job := range waiting {
		if job.Phase != db.FixJobPhaseWaitingForPR {
			continue
		}
		if job.Message == "waiting for manual review" {
			continue
		}
		detectCtx, cancel := context.WithTimeout(ctx, fixJobTimeout(poller))
		url, err := poller.Fix.DetectPR(detectCtx, job)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return didWork, ctx.Err()
			}
			poller.log().Warn("detect fix PR failed", "job", job.ID, "err", err)
			continue
		}
		if strings.TrimSpace(url) == "" {
			continue
		}
		completed, err := poller.DB.CompleteWaitingFixJobWithPR(job.ID, url)
		if err != nil {
			return true, err
		}
		if !completed {
			continue
		}
		didWork = true
	}
	return didWork, nil
}

func refreshSucceededFixJobItems(ctx context.Context, poller Poller) error {
	getter, ok := poller.GitHub.(itemGetter)
	if !ok {
		return nil
	}
	jobs, err := poller.DB.ListSucceededFixJobsDueForRefresh(time.Now().Add(-succeededFixJobRefreshInterval), succeededFixJobRefreshLimit)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		prRepo, prNumber, ok := githubPRURLParts(job.PRURL)
		if !ok {
			if err := poller.DB.TouchFixJobRefresh(job.ID); err != nil {
				return err
			}
			continue
		}
		if fixJobItemsLocallyClosed(poller.DB, job, prRepo, prNumber) {
			if err := poller.DB.TouchFixJobRefresh(job.ID); err != nil {
				return err
			}
			continue
		}
		if err := refreshFixJobItem(ctx, poller.DB, getter, job.RepoID, job.ItemKind, job.ItemNumber, false); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			poller.log().Warn("refresh succeeded fix job source item failed", "job", job.ID, "err", err)
			if err := poller.DB.TouchFixJobRefresh(job.ID); err != nil {
				return err
			}
			continue
		}
		if err := refreshFixJobItem(ctx, poller.DB, getter, prRepo, sharedtypes.ItemKindPR, prNumber, true); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			poller.log().Warn("refresh succeeded fix job PR item failed", "job", job.ID, "err", err)
			if err := poller.DB.TouchFixJobRefresh(job.ID); err != nil {
				return err
			}
			continue
		}
		if err := poller.DB.TouchFixJobRefresh(job.ID); err != nil {
			return err
		}
	}
	return nil
}

func fixJobItemsLocallyClosed(database *db.DB, job db.FixJob, prRepo string, prNumber int) bool {
	source, err := database.GetItem(job.ItemID)
	if err == nil && source != nil && source.State != sharedtypes.ItemStateOpen {
		return true
	}
	pr, err := database.GetItem(itemID(prRepo, prNumber))
	return err == nil && pr != nil && pr.State != sharedtypes.ItemStateOpen
}

func refreshFixJobItem(ctx context.Context, database *db.DB, getter itemGetter, repoID string, kind sharedtypes.ItemKind, number int, forceGHTriaged bool) error {
	current, err := getter.GetItem(ctx, repoID, kind, number)
	if err != nil {
		return fmt.Errorf("get fix item %s#%d: %w", repoID, number, err)
	}
	cached, err := database.GetItem(itemID(repoID, number))
	if err != nil {
		return err
	}
	if err := database.UpsertRepo(db.Repo{ID: repoID}); err != nil {
		return err
	}
	itemRecord := db.Item{
		ID:          itemID(repoID, current.Number),
		RepoID:      repoID,
		Kind:        current.Kind,
		Number:      current.Number,
		Title:       current.Title,
		Author:      current.Author,
		State:       current.State,
		IsDraft:     current.IsDraft,
		GHTriaged:   hasLabel(current.Labels, triagedLabel),
		WaitingOn:   sharedtypes.WaitingOnNone,
		LastEventAt: timePtr(current.UpdatedAt.UTC()),
	}
	if cached != nil {
		itemRecord.Role = cached.Role
		itemRecord.GHTriaged = cached.GHTriaged
		itemRecord.WaitingOn = cached.WaitingOn
		itemRecord.StaleSince = cached.StaleSince
		itemRecord.LastSelfActivityAt = cached.LastSelfActivityAt
		itemRecord.HeadRepo = cached.HeadRepo
		itemRecord.HeadRef = cached.HeadRef
		itemRecord.HeadCloneURL = cached.HeadCloneURL
	}
	if forceGHTriaged {
		itemRecord.GHTriaged = true
	}
	if current.State != sharedtypes.ItemStateOpen {
		itemRecord.StaleSince = nil
	}
	if err := database.UpsertItem(itemRecord); err != nil {
		return err
	}
	if current.State != sharedtypes.ItemStateOpen {
		if err := database.MarkActiveRecommendationsForItemSuperseded(itemRecord.ID, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func githubPRURLParts(rawURL string) (string, int, bool) {
	trimmed := strings.TrimSpace(rawURL)
	trimmed = strings.TrimPrefix(trimmed, "https://github.com/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return "", 0, false
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", 0, false
	}
	return parts[0] + "/" + parts[1], number, true
}

func fixJobTimeout(poller Poller) time.Duration {
	if poller.PerFixJobTimeout > 0 {
		return poller.PerFixJobTimeout
	}
	return defaultPerFixJobTimeout
}
