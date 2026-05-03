package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

// ErrFixJobInFlight is returned by CreateFixJob when an existing fix job for
// the same item is mid-agent (preparing the worktree, running the agent,
// committing, or pushing). The caller is expected to surface this so the user
// can retry once the in-flight job either reaches waiting_for_pr or finishes.
var ErrFixJobInFlight = errors.New("fix job already in flight for this item")

func (d *DB) CreateFixJob(input NewFixJob) (*FixJob, error) {
	if strings.TrimSpace(input.ItemID) == "" {
		return nil, fmt.Errorf("create fix job: item_id required")
	}
	if strings.TrimSpace(input.RecommendationID) == "" {
		return nil, fmt.Errorf("create fix job: recommendation_id required")
	}
	if strings.TrimSpace(input.RepoID) == "" {
		return nil, fmt.Errorf("create fix job: repo_id required")
	}
	if strings.TrimSpace(input.FixPrompt) == "" {
		return nil, fmt.Errorf("create fix job: fix_prompt required")
	}
	if input.PRCreate == "" {
		input.PRCreate = "auto"
	}

	d.fixJobMu.Lock()
	defer d.fixJobMu.Unlock()

	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("create fix job: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(
		`SELECT id, item_id, recommendation_id, option_id, repo_id, item_number, item_kind, title, fix_prompt, agent, pr_create,
		        branch, worktree_path, pr_url, status, phase, message, error, created_at, started_at, updated_at, completed_at
		 FROM fix_jobs
		 WHERE item_id = ? AND status IN (?, ?)
		 ORDER BY created_at ASC LIMIT 1`,
		input.ItemID, FixJobStatusQueued, FixJobStatusRunning,
	)
	if err != nil {
		return nil, fmt.Errorf("active fix job: %w", err)
	}
	existing, err := scanOptionalFixJob(rows)
	if closeErr := rows.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// Reject when the existing job is mid-agent: the agent subprocess is
		// in flight inside runFixStage and we cannot externally cancel it.
		// Once it reaches waiting_for_pr (or terminates) the new fix can be
		// queued.
		if !canSupersedeFixJob(existing) {
			return nil, fmt.Errorf("%w: %s/%s", ErrFixJobInFlight, existing.Status, existing.Phase)
		}
		now := nowUnix()
		superseded, err := supersedeFixJobIfCancellable(tx, existing.ID, now)
		if err != nil {
			return nil, fmt.Errorf("supersede fix job: %w", err)
		}
		if !superseded {
			return nil, fmt.Errorf("%w: %s/%s", ErrFixJobInFlight, existing.Status, existing.Phase)
		}
	}

	now := nowUnix()
	job := &FixJob{
		ID:               newID(),
		ItemID:           input.ItemID,
		RecommendationID: input.RecommendationID,
		OptionID:         input.OptionID,
		RepoID:           input.RepoID,
		ItemNumber:       input.ItemNumber,
		ItemKind:         input.ItemKind,
		Title:            input.Title,
		FixPrompt:        strings.TrimSpace(input.FixPrompt),
		Agent:            input.Agent,
		PRCreate:         input.PRCreate,
		Status:           FixJobStatusQueued,
		Phase:            FixJobPhaseQueued,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	_, err = tx.Exec(
		`INSERT INTO fix_jobs (
		 id, item_id, recommendation_id, option_id, repo_id, item_number, item_kind, title, fix_prompt, agent, pr_create,
		 branch, worktree_path, pr_url, status, phase, message, error, created_at, started_at, updated_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, '', '', ?, NULL, ?, NULL)`,
		job.ID, job.ItemID, job.RecommendationID, nullableString(job.OptionID), job.RepoID, job.ItemNumber, job.ItemKind,
		job.Title, job.FixPrompt, nullableString(string(job.Agent)), job.PRCreate, job.Status, job.Phase, job.CreatedAt, job.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create fix job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("create fix job: commit: %w", err)
	}
	return job, nil
}

func (d *DB) ActiveFixJobForItem(itemID string) (*FixJob, error) {
	rows, err := d.sql.Query(
		`SELECT id, item_id, recommendation_id, option_id, repo_id, item_number, item_kind, title, fix_prompt, agent, pr_create,
		        branch, worktree_path, pr_url, status, phase, message, error, created_at, started_at, updated_at, completed_at
		 FROM fix_jobs
		 WHERE item_id = ? AND status IN (?, ?)
		 ORDER BY created_at ASC LIMIT 1`,
		itemID, FixJobStatusQueued, FixJobStatusRunning,
	)
	if err != nil {
		return nil, fmt.Errorf("active fix job: %w", err)
	}
	defer rows.Close()
	return scanOptionalFixJob(rows)
}

func (d *DB) LatestFixJobForItem(itemID string) (*FixJob, error) {
	rows, err := d.sql.Query(
		`SELECT id, item_id, recommendation_id, option_id, repo_id, item_number, item_kind, title, fix_prompt, agent, pr_create,
		        branch, worktree_path, pr_url, status, phase, message, error, created_at, started_at, updated_at, completed_at
		 FROM fix_jobs WHERE item_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, itemID,
	)
	if err != nil {
		return nil, fmt.Errorf("latest fix job: %w", err)
	}
	defer rows.Close()
	return scanOptionalFixJob(rows)
}

func (d *DB) GetFixJob(id string) (*FixJob, error) {
	rows, err := d.sql.Query(
		`SELECT id, item_id, recommendation_id, option_id, repo_id, item_number, item_kind, title, fix_prompt, agent, pr_create,
		        branch, worktree_path, pr_url, status, phase, message, error, created_at, started_at, updated_at, completed_at
		 FROM fix_jobs WHERE id = ?`, id,
	)
	if err != nil {
		return nil, fmt.Errorf("get fix job: %w", err)
	}
	defer rows.Close()
	return scanOptionalFixJob(rows)
}

func (d *DB) ClaimNextQueuedFixJob() (*FixJob, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("claim fix job: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id string
	err = tx.QueryRow(`SELECT id FROM fix_jobs WHERE status = ? ORDER BY created_at ASC LIMIT 1`, FixJobStatusQueued).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim fix job: select: %w", err)
	}
	now := nowUnix()
	if _, err := tx.Exec(`UPDATE fix_jobs SET status = ?, phase = ?, started_at = COALESCE(started_at, ?), updated_at = ? WHERE id = ? AND status = ?`, FixJobStatusRunning, FixJobPhasePreparingWorktree, now, now, id, FixJobStatusQueued); err != nil {
		return nil, fmt.Errorf("claim fix job: update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim fix job: commit: %w", err)
	}
	return d.GetFixJob(id)
}

func (d *DB) ListFixJobsByStatus(statuses ...FixJobStatus) ([]FixJob, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, status := range statuses {
		placeholders[i] = "?"
		args[i] = status
	}
	rows, err := d.sql.Query(
		`SELECT id, item_id, recommendation_id, option_id, repo_id, item_number, item_kind, title, fix_prompt, agent, pr_create,
		        branch, worktree_path, pr_url, status, phase, message, error, created_at, started_at, updated_at, completed_at
		 FROM fix_jobs WHERE status IN (`+strings.Join(placeholders, ",")+`) ORDER BY created_at ASC`, args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list fix jobs: %w", err)
	}
	defer rows.Close()
	var jobs []FixJob
	for rows.Next() {
		job, err := scanFixJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list fix jobs: %w", err)
	}
	return jobs, nil
}

func (d *DB) ListFixJobs() ([]FixJob, error) {
	rows, err := d.sql.Query(
		`SELECT id, item_id, recommendation_id, option_id, repo_id, item_number, item_kind, title, fix_prompt, agent, pr_create,
		        branch, worktree_path, pr_url, status, phase, message, error, created_at, started_at, updated_at, completed_at
		 FROM fix_jobs ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list fix jobs: %w", err)
	}
	defer rows.Close()
	var jobs []FixJob
	for rows.Next() {
		job, err := scanFixJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list fix jobs: %w", err)
	}
	return jobs, nil
}

func (d *DB) ReclaimStaleRunningFixJobs(cutoff time.Time) (int, error) {
	now := nowUnix()
	result, err := d.sql.Exec(
		`UPDATE fix_jobs SET
		 status = ?,
		 phase = ?,
		 message = ?,
		 error = ?,
		 updated_at = ?,
		 completed_at = COALESCE(completed_at, ?)
		 WHERE status = ?
		   AND COALESCE(phase, '') <> ?
		   AND updated_at <= ?`,
		FixJobStatusFailed,
		FixJobPhaseFailed,
		"fix job interrupted",
		"fix job interrupted before completion",
		now,
		now,
		FixJobStatusRunning,
		FixJobPhaseWaitingForPR,
		cutoff.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("reclaim stale fix jobs: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reclaim stale fix jobs: rows affected: %w", err)
	}
	return int(rows), nil
}

func (d *DB) UpdateFixJob(id string, update FixJobUpdate) error {
	now := nowUnix()
	completedAt := any(nil)
	if update.Status == FixJobStatusSucceeded || update.Status == FixJobStatusFailed || update.Status == FixJobStatusCancelled {
		completedAt = now
	}
	_, err := d.sql.Exec(
		`UPDATE fix_jobs SET
		 status = COALESCE(NULLIF(?, ''), status),
		 phase = COALESCE(NULLIF(?, ''), phase),
		 message = COALESCE(NULLIF(?, ''), message),
		 error = COALESCE(NULLIF(?, ''), error),
		 agent = COALESCE(NULLIF(?, ''), agent),
		 branch = COALESCE(NULLIF(?, ''), branch),
		 worktree_path = COALESCE(NULLIF(?, ''), worktree_path),
		 pr_url = COALESCE(NULLIF(?, ''), pr_url),
		 updated_at = ?,
		 completed_at = COALESCE(?, completed_at)
		 WHERE id = ?`,
		update.Status, update.Phase, update.Message, update.Error, update.Agent, update.Branch, update.WorktreePath, update.PRURL, now, completedAt, id,
	)
	if err != nil {
		return fmt.Errorf("update fix job: %w", err)
	}
	return nil
}

func (d *DB) SupersedeFixJobIfCancellable(id string) (bool, error) {
	superseded, err := supersedeFixJobIfCancellable(d.sql, id, nowUnix())
	if err != nil {
		return false, fmt.Errorf("supersede fix job: %w", err)
	}
	return superseded, nil
}

func (d *DB) CompleteWaitingFixJobWithPR(id, url string) (bool, error) {
	now := nowUnix()
	result, err := d.sql.Exec(
		`UPDATE fix_jobs SET
		 status = ?,
		 phase = ?,
		 pr_url = ?,
		 message = ?,
		 updated_at = ?,
		 completed_at = COALESCE(completed_at, ?)
		 WHERE id = ? AND status = ? AND phase = ?`,
		FixJobStatusSucceeded,
		FixJobPhasePROpened,
		url,
		"PR opened",
		now,
		now,
		id,
		FixJobStatusRunning,
		FixJobPhaseWaitingForPR,
	)
	if err != nil {
		return false, fmt.Errorf("complete waiting fix job with PR: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("complete waiting fix job with PR: rows affected: %w", err)
	}
	return rows > 0, nil
}

type fixJobExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func supersedeFixJobIfCancellable(exec fixJobExecer, id string, now int64) (bool, error) {
	result, err := exec.Exec(
		`UPDATE fix_jobs SET
		 status = ?,
		 message = ?,
		 updated_at = ?,
		 completed_at = COALESCE(completed_at, ?)
		 WHERE id = ?
		   AND (status = ? OR (status = ? AND phase = ?))`,
		FixJobStatusCancelled,
		"superseded by newer fix request",
		now,
		now,
		id,
		FixJobStatusQueued,
		FixJobStatusRunning,
		FixJobPhaseWaitingForPR,
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return rows > 0, nil
}

// canSupersedeFixJob reports whether an existing active fix job can be safely
// cancelled to make room for a new one. Queued jobs haven't been claimed yet,
// and waiting_for_pr jobs are past the agent run; both can be cancelled
// without abandoning a live agent subprocess.
func canSupersedeFixJob(job *FixJob) bool {
	if job == nil {
		return true
	}
	if job.Status == FixJobStatusQueued {
		return true
	}
	if job.Status == FixJobStatusRunning && job.Phase == FixJobPhaseWaitingForPR {
		return true
	}
	return false
}

func scanOptionalFixJob(rows *sql.Rows) (*FixJob, error) {
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("scan fix job: %w", err)
		}
		return nil, nil
	}
	return scanFixJob(rows)
}

type fixJobScanner interface {
	Scan(dest ...any) error
}

func scanFixJob(row fixJobScanner) (*FixJob, error) {
	var job FixJob
	var optionID, title, agentName, branch, worktreePath, prURL, phase, message, errText sql.NullString
	var startedAt, completedAt sql.NullInt64
	if err := row.Scan(&job.ID, &job.ItemID, &job.RecommendationID, &optionID, &job.RepoID, &job.ItemNumber, &job.ItemKind, &title, &job.FixPrompt, &agentName, &job.PRCreate, &branch, &worktreePath, &prURL, &job.Status, &phase, &message, &errText, &job.CreatedAt, &startedAt, &job.UpdatedAt, &completedAt); err != nil {
		return nil, fmt.Errorf("scan fix job: %w", err)
	}
	if optionID.Valid {
		job.OptionID = optionID.String
	}
	if title.Valid {
		job.Title = title.String
	}
	if agentName.Valid {
		job.Agent = sharedtypes.AgentName(agentName.String)
	}
	if branch.Valid {
		job.Branch = branch.String
	}
	if worktreePath.Valid {
		job.WorktreePath = worktreePath.String
	}
	if prURL.Valid {
		job.PRURL = prURL.String
	}
	if phase.Valid {
		job.Phase = FixJobPhase(phase.String)
	}
	if message.Valid {
		job.Message = message.String
	}
	if errText.Valid {
		job.Error = errText.String
	}
	job.StartedAt = unixToTimePtr(startedAt)
	job.CompletedAt = unixToTimePtr(completedAt)
	return &job, nil
}
