package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kunchenguid/ezoss/internal/db"
)

func runFixStage(ctx context.Context, poller Poller) (bool, error) {
	if poller.DB == nil || poller.Fix == nil {
		return false, nil
	}

	if didWork, err := detectWaitingFixPRs(ctx, poller); didWork || err != nil {
		return didWork, err
	}

	job, err := poller.DB.ClaimNextQueuedFixJob()
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
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
		update.Message = "waiting for PR"
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
		detectCtx, cancel := context.WithTimeout(ctx, fixJobTimeout(poller))
		url, err := poller.Fix.DetectPR(detectCtx, job)
		cancel()
		if err != nil {
			return true, fmt.Errorf("detect fix PR %s: %w", job.ID, err)
		}
		if strings.TrimSpace(url) == "" {
			continue
		}
		if err := poller.DB.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusSucceeded, Phase: db.FixJobPhasePROpened, PRURL: url, Message: "PR opened"}); err != nil {
			return true, err
		}
		didWork = true
	}
	return didWork, nil
}

func fixJobTimeout(poller Poller) time.Duration {
	if poller.PerFixJobTimeout > 0 {
		return poller.PerFixJobTimeout
	}
	return defaultPerFixJobTimeout
}
