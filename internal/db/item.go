package db

import (
	"database/sql"
	"fmt"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

const itemColumns = `id, repo_id, kind, role, number, title, author, state, is_draft, gh_triaged, waiting_on,
	last_event_at, stale_since,
	last_seen_updated_at, last_seen_comment_id, last_self_activity_at,
	head_repo, head_ref, head_clone_url,
	created_at, updated_at`

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
	role := item.Role
	if role == "" {
		role = sharedtypes.RoleMaintainer
	}

	_, err := d.sql.Exec(
		`INSERT INTO items (
		 id, repo_id, kind, role, number, title, author, state, is_draft, gh_triaged, waiting_on,
		 last_event_at, stale_since,
		 last_seen_updated_at, last_seen_comment_id, last_self_activity_at,
		 head_repo, head_ref, head_clone_url,
		 created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		 repo_id = excluded.repo_id,
		 kind = excluded.kind,
		 role = excluded.role,
		 number = excluded.number,
		 title = excluded.title,
		 author = excluded.author,
		 state = excluded.state,
		 is_draft = excluded.is_draft,
		 gh_triaged = excluded.gh_triaged,
		 waiting_on = excluded.waiting_on,
		 last_event_at = excluded.last_event_at,
		 stale_since = excluded.stale_since,
		 last_seen_updated_at = excluded.last_seen_updated_at,
		 last_seen_comment_id = excluded.last_seen_comment_id,
		 last_self_activity_at = excluded.last_self_activity_at,
		 head_repo = excluded.head_repo,
		 head_ref = excluded.head_ref,
		 head_clone_url = excluded.head_clone_url,
		 updated_at = excluded.updated_at`,
		item.ID,
		item.RepoID,
		item.Kind,
		role,
		item.Number,
		item.Title,
		item.Author,
		item.State,
		boolToInt(item.IsDraft),
		boolToInt(item.GHTriaged),
		item.WaitingOn,
		timeToUnixPtr(item.LastEventAt),
		timeToUnixPtr(item.StaleSince),
		timeToUnixPtr(item.LastSeenUpdatedAt),
		nullInt64IfZero(item.LastSeenCommentID),
		timeToUnixPtr(item.LastSelfActivityAt),
		nullStringIfEmpty(item.HeadRepo),
		nullStringIfEmpty(item.HeadRef),
		nullStringIfEmpty(item.HeadCloneURL),
		createdAt,
		updatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert item: %w", err)
	}
	return nil
}

func (d *DB) GetItem(id string) (*Item, error) {
	row := d.sql.QueryRow(
		`SELECT `+itemColumns+` FROM items WHERE id = ?`,
		id,
	)
	item, err := scanItemRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get item: %w", err)
	}
	return item, nil
}

// ListItemsNeedingTriage returns open, GitHub-untriaged items that have
// no active recommendation, or whose latest GitHub event is more recent
// than the active recommendation. This drives the agent stage of the
// poll cycle so we don't re-investigate items we already have a fresh
// take on.
func (d *DB) ListItemsNeedingTriage() ([]Item, error) {
	rows, err := d.sql.Query(
		`SELECT `+itemColumns+`
		 FROM items i
		 LEFT JOIN (
		   SELECT item_id, MAX(created_at) AS rec_created_at
		   FROM recommendations
		   WHERE superseded_at IS NULL
		   GROUP BY item_id
		 ) r ON r.item_id = i.id
		 WHERE i.state = ?
			   AND i.gh_triaged = 0
		   AND (r.item_id IS NULL OR (i.last_event_at IS NOT NULL AND r.rec_created_at < i.last_event_at))
		 ORDER BY i.repo_id ASC, i.number ASC`,
		sharedtypes.ItemStateOpen,
	)
	if err != nil {
		return nil, fmt.Errorf("list items needing triage: %w", err)
	}
	defer rows.Close()
	return scanItemRows(rows, "list items needing triage")
}

func (d *DB) ListTriagedItemsWaitingOnContributor(repoID string) ([]Item, error) {
	rows, err := d.sql.Query(
		`SELECT `+itemColumns+`
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
	return scanItemRows(rows, "list triaged contributor items")
}

// ListContributorItemsForRepo returns all contributor-role items for the
// given repo. Used by the contrib auto-prune step to decide whether the
// repo should remain in the local DB.
func (d *DB) ListContributorItemsForRepo(repoID string) ([]Item, error) {
	rows, err := d.sql.Query(
		`SELECT `+itemColumns+`
		 FROM items
		 WHERE repo_id = ? AND role = ?
		 ORDER BY number ASC`,
		repoID,
		sharedtypes.RoleContributor,
	)
	if err != nil {
		return nil, fmt.Errorf("list contributor items: %w", err)
	}
	defer rows.Close()
	return scanItemRows(rows, "list contributor items")
}

// DeleteItem removes an item and its recommendations (cascade).
func (d *DB) DeleteItem(id string) error {
	if _, err := d.sql.Exec(`DELETE FROM items WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete item: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItemRow(row rowScanner) (*Item, error) {
	var item Item
	var role sql.NullString
	var isDraft int
	var ghTriaged int
	var lastEventAt sql.NullInt64
	var staleSince sql.NullInt64
	var lastSeenUpdatedAt sql.NullInt64
	var lastSeenCommentID sql.NullInt64
	var lastSelfActivityAt sql.NullInt64
	var headRepo sql.NullString
	var headRef sql.NullString
	var headCloneURL sql.NullString

	if err := row.Scan(
		&item.ID,
		&item.RepoID,
		&item.Kind,
		&role,
		&item.Number,
		&item.Title,
		&item.Author,
		&item.State,
		&isDraft,
		&ghTriaged,
		&item.WaitingOn,
		&lastEventAt,
		&staleSince,
		&lastSeenUpdatedAt,
		&lastSeenCommentID,
		&lastSelfActivityAt,
		&headRepo,
		&headRef,
		&headCloneURL,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}

	item.Role = sharedtypes.Role(role.String)
	if !item.Role.IsValid() {
		item.Role = sharedtypes.RoleMaintainer
	}
	item.IsDraft = isDraft != 0
	item.GHTriaged = ghTriaged != 0
	item.LastEventAt = unixToTimePtr(lastEventAt)
	item.StaleSince = unixToTimePtr(staleSince)
	item.LastSeenUpdatedAt = unixToTimePtr(lastSeenUpdatedAt)
	if lastSeenCommentID.Valid {
		item.LastSeenCommentID = lastSeenCommentID.Int64
	}
	item.LastSelfActivityAt = unixToTimePtr(lastSelfActivityAt)
	item.HeadRepo = headRepo.String
	item.HeadRef = headRef.String
	item.HeadCloneURL = headCloneURL.String
	return &item, nil
}

func scanItemRows(rows *sql.Rows, errPrefix string) ([]Item, error) {
	var items []Item
	for rows.Next() {
		item, err := scanItemRow(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errPrefix, err)
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	return items, nil
}

func nullInt64IfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullStringIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
