package db

import (
	"database/sql"
	"fmt"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func (d *DB) UpsertRepo(repo Repo) error {
	createdAt := repo.CreatedAt
	if createdAt == 0 {
		createdAt = nowUnix()
	}
	source := repo.Source
	if source == "" {
		source = RepoSourceConfig
	}

	_, err := d.sql.Exec(
		`INSERT INTO repos (id, default_branch, source, last_poll_at, last_triaged_refresh_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		 default_branch = CASE
		 	WHEN excluded.default_branch <> '' THEN excluded.default_branch
		 	ELSE repos.default_branch
		 END,
		 source = CASE
		 	WHEN repos.source = 'config' THEN repos.source
		 	ELSE excluded.source
		 END,
		 last_poll_at = COALESCE(excluded.last_poll_at, repos.last_poll_at),
		 last_triaged_refresh_at = COALESCE(excluded.last_triaged_refresh_at, repos.last_triaged_refresh_at)`,
		repo.ID,
		repo.DefaultBranch,
		source,
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
	var source sql.NullString

	err := d.sql.QueryRow(
		`SELECT id, default_branch, source, last_poll_at, last_triaged_refresh_at, created_at FROM repos WHERE id = ?`,
		id,
	).Scan(&repo.ID, &repo.DefaultBranch, &source, &lastPollAt, &lastTriagedRefreshAt, &repo.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}

	repo.Source = RepoSource(source.String)
	if repo.Source == "" {
		repo.Source = RepoSourceConfig
	}
	repo.LastPollAt = unixToTimePtr(lastPollAt)
	repo.LastTriagedRefreshAt = unixToTimePtr(lastTriagedRefreshAt)
	return &repo, nil
}

func (d *DB) ListReposBySource(source RepoSource) ([]Repo, error) {
	rows, err := d.sql.Query(
		`SELECT id, default_branch, source, last_poll_at, last_triaged_refresh_at, created_at FROM repos WHERE source = ? ORDER BY id ASC`,
		source,
	)
	if err != nil {
		return nil, fmt.Errorf("list repos by source: %w", err)
	}
	defer rows.Close()

	var repos []Repo
	for rows.Next() {
		var repo Repo
		var lastPollAt sql.NullInt64
		var lastTriagedRefreshAt sql.NullInt64
		var source sql.NullString
		if err := rows.Scan(&repo.ID, &repo.DefaultBranch, &source, &lastPollAt, &lastTriagedRefreshAt, &repo.CreatedAt); err != nil {
			return nil, fmt.Errorf("list repos by source: %w", err)
		}
		repo.Source = RepoSource(source.String)
		if repo.Source == "" {
			repo.Source = RepoSourceConfig
		}
		repo.LastPollAt = unixToTimePtr(lastPollAt)
		repo.LastTriagedRefreshAt = unixToTimePtr(lastTriagedRefreshAt)
		repos = append(repos, repo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list repos by source: %w", err)
	}
	return repos, nil
}

func (d *DB) ListReposWithContributorItems() ([]Repo, error) {
	rows, err := d.sql.Query(
		`SELECT DISTINCT r.id, r.default_branch, r.source, r.last_poll_at, r.last_triaged_refresh_at, r.created_at
		 FROM repos r
		 JOIN items i ON i.repo_id = r.id
		 WHERE i.role = ?
		 ORDER BY r.id ASC`,
		sharedtypes.RoleContributor,
	)
	if err != nil {
		return nil, fmt.Errorf("list repos with contributor items: %w", err)
	}
	defer rows.Close()

	var repos []Repo
	for rows.Next() {
		var repo Repo
		var lastPollAt sql.NullInt64
		var lastTriagedRefreshAt sql.NullInt64
		var source sql.NullString
		if err := rows.Scan(&repo.ID, &repo.DefaultBranch, &source, &lastPollAt, &lastTriagedRefreshAt, &repo.CreatedAt); err != nil {
			return nil, fmt.Errorf("list repos with contributor items: %w", err)
		}
		repo.Source = RepoSource(source.String)
		if repo.Source == "" {
			repo.Source = RepoSourceConfig
		}
		repo.LastPollAt = unixToTimePtr(lastPollAt)
		repo.LastTriagedRefreshAt = unixToTimePtr(lastTriagedRefreshAt)
		repos = append(repos, repo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list repos with contributor items: %w", err)
	}
	return repos, nil
}

// DeleteRepoIfContrib removes a repo only when it has source='contrib'.
// Used by the contributor sweep to auto-prune repos that no longer have
// any open contributor items. Returns true when a row was removed.
func (d *DB) DeleteRepoIfContrib(id string) (bool, error) {
	res, err := d.sql.Exec(`DELETE FROM repos WHERE id = ? AND source = ?`, id, RepoSourceContrib)
	if err != nil {
		return false, fmt.Errorf("delete contrib repo: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete contrib repo: %w", err)
	}
	return n > 0, nil
}
