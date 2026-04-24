package db

import (
	"database/sql"
	"fmt"
)

func (d *DB) UpsertRepo(repo Repo) error {
	createdAt := repo.CreatedAt
	if createdAt == 0 {
		createdAt = nowUnix()
	}

	_, err := d.sql.Exec(
		`INSERT INTO repos (id, default_branch, last_poll_at, last_triaged_refresh_at, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		 default_branch = CASE
		 	WHEN excluded.default_branch <> '' THEN excluded.default_branch
		 	ELSE repos.default_branch
		 END,
		 last_poll_at = COALESCE(excluded.last_poll_at, repos.last_poll_at),
		 last_triaged_refresh_at = COALESCE(excluded.last_triaged_refresh_at, repos.last_triaged_refresh_at)`,
		repo.ID,
		repo.DefaultBranch,
		timeToUnixPtr(repo.LastPollAt),
		timeToUnixPtr(repo.LastTriagedRefreshAt),
		createdAt,
	)
	if err != nil {
		return fmt.Errorf("upsert repo: %w", err)
	}
	return nil
}

func (d *DB) GetRepo(id string) (*Repo, error) {
	var repo Repo
	var lastPollAt sql.NullInt64
	var lastTriagedRefreshAt sql.NullInt64

	err := d.sql.QueryRow(
		`SELECT id, default_branch, last_poll_at, last_triaged_refresh_at, created_at FROM repos WHERE id = ?`,
		id,
	).Scan(&repo.ID, &repo.DefaultBranch, &lastPollAt, &lastTriagedRefreshAt, &repo.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}

	repo.LastPollAt = unixToTimePtr(lastPollAt)
	repo.LastTriagedRefreshAt = unixToTimePtr(lastTriagedRefreshAt)
	return &repo, nil
}
