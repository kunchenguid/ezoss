package db

import (
	"database/sql"
	"fmt"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func (d *DB) UpsertItem(item Item) error {
	now := nowUnix()
	createdAt := item.CreatedAt
	if createdAt == 0 {
		createdAt = now
	}
	updatedAt := item.UpdatedAt
	if updatedAt == 0 {
		updatedAt = now
	}

	_, err := d.sql.Exec(
		`INSERT INTO items (
		 id, repo_id, kind, number, title, author, state, is_draft, gh_triaged, waiting_on, last_event_at, stale_since, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		 repo_id = excluded.repo_id,
		 kind = excluded.kind,
		 number = excluded.number,
		 title = excluded.title,
		 author = excluded.author,
		 state = excluded.state,
		 is_draft = excluded.is_draft,
		 gh_triaged = excluded.gh_triaged,
		 waiting_on = excluded.waiting_on,
		 last_event_at = excluded.last_event_at,
		 stale_since = excluded.stale_since,
		 updated_at = excluded.updated_at`,
		item.ID,
		item.RepoID,
		item.Kind,
		item.Number,
		item.Title,
		item.Author,
		item.State,
		boolToInt(item.IsDraft),
		boolToInt(item.GHTriaged),
		item.WaitingOn,
		timeToUnixPtr(item.LastEventAt),
		timeToUnixPtr(item.StaleSince),
		createdAt,
		updatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert item: %w", err)
	}
	return nil
}

func (d *DB) GetItem(id string) (*Item, error) {
	var item Item
	var isDraft int
	var ghTriaged int
	var lastEventAt sql.NullInt64
	var staleSince sql.NullInt64

	err := d.sql.QueryRow(
		`SELECT id, repo_id, kind, number, title, author, state, is_draft, gh_triaged, waiting_on, last_event_at, stale_since, created_at, updated_at
		 FROM items WHERE id = ?`,
		id,
	).Scan(
		&item.ID,
		&item.RepoID,
		&item.Kind,
		&item.Number,
		&item.Title,
		&item.Author,
		&item.State,
		&isDraft,
		&ghTriaged,
		&item.WaitingOn,
		&lastEventAt,
		&staleSince,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get item: %w", err)
	}

	item.IsDraft = isDraft != 0
	item.GHTriaged = ghTriaged != 0
	item.LastEventAt = unixToTimePtr(lastEventAt)
	item.StaleSince = unixToTimePtr(staleSince)

	return &item, nil
}

// ListItemsNeedingTriage returns open, GitHub-untriaged items that have
// no active recommendation, or whose latest GitHub event is more recent
// than the active recommendation. This drives the agent stage of the
// poll cycle so we don't re-investigate items we already have a fresh
// take on.
func (d *DB) ListItemsNeedingTriage() ([]Item, error) {
	rows, err := d.sql.Query(
		`SELECT i.id, i.repo_id, i.kind, i.number, i.title, i.author, i.state, i.is_draft, i.gh_triaged, i.waiting_on, i.last_event_at, i.stale_since, i.created_at, i.updated_at
		 FROM items i
		 LEFT JOIN (
		   SELECT item_id, MAX(created_at) AS rec_created_at
		   FROM recommendations
		   WHERE superseded_at IS NULL
		   GROUP BY item_id
		 ) r ON r.item_id = i.id
		 WHERE i.gh_triaged = 0
		   AND i.state = ?
		   AND (r.item_id IS NULL OR (i.last_event_at IS NOT NULL AND r.rec_created_at < i.last_event_at))
		 ORDER BY i.repo_id ASC, i.number ASC`,
		sharedtypes.ItemStateOpen,
	)
	if err != nil {
		return nil, fmt.Errorf("list items needing triage: %w", err)
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var item Item
		var isDraft int
		var ghTriaged int
		var lastEventAt sql.NullInt64
		var staleSince sql.NullInt64

		if err := rows.Scan(
			&item.ID,
			&item.RepoID,
			&item.Kind,
			&item.Number,
			&item.Title,
			&item.Author,
			&item.State,
			&isDraft,
			&ghTriaged,
			&item.WaitingOn,
			&lastEventAt,
			&staleSince,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("list items needing triage: %w", err)
		}

		item.IsDraft = isDraft != 0
		item.GHTriaged = ghTriaged != 0
		item.LastEventAt = unixToTimePtr(lastEventAt)
		item.StaleSince = unixToTimePtr(staleSince)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list items needing triage: %w", err)
	}

	return items, nil
}

func (d *DB) ListTriagedItemsWaitingOnContributor(repoID string) ([]Item, error) {
	rows, err := d.sql.Query(
		`SELECT id, repo_id, kind, number, title, author, state, is_draft, gh_triaged, waiting_on, last_event_at, stale_since, created_at, updated_at
		 FROM items
		 WHERE repo_id = ? AND gh_triaged = 1 AND waiting_on = ? AND state = ?
		 ORDER BY number ASC`,
		repoID,
		sharedtypes.WaitingOnContributor,
		sharedtypes.ItemStateOpen,
	)
	if err != nil {
		return nil, fmt.Errorf("list triaged contributor items: %w", err)
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var item Item
		var isDraft int
		var ghTriaged int
		var lastEventAt sql.NullInt64
		var staleSince sql.NullInt64

		if err := rows.Scan(
			&item.ID,
			&item.RepoID,
			&item.Kind,
			&item.Number,
			&item.Title,
			&item.Author,
			&item.State,
			&isDraft,
			&ghTriaged,
			&item.WaitingOn,
			&lastEventAt,
			&staleSince,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("list triaged contributor items: %w", err)
		}

		item.IsDraft = isDraft != 0
		item.GHTriaged = ghTriaged != 0
		item.LastEventAt = unixToTimePtr(lastEventAt)
		item.StaleSince = unixToTimePtr(staleSince)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list triaged contributor items: %w", err)
	}

	return items, nil
}
