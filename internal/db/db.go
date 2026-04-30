package db

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(rand.Reader, 0)
)

type DB struct {
	sql               *sql.DB
	migrationsApplied []string
	fixJobMu          sync.Mutex
}

func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if _, err := sqlDB.Exec(schemaSQL); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate db: %w", err)
	}
	var applied []string
	migrations := []struct {
		name, table, column, ddl string
	}{
		{"repos.last_triaged_refresh_at", "repos", "last_triaged_refresh_at", `ALTER TABLE repos ADD COLUMN last_triaged_refresh_at INTEGER`},
		{"recommendations.followups", "recommendations", "followups", `ALTER TABLE recommendations ADD COLUMN followups TEXT`},
		{"approvals.option_id", "approvals", "option_id", `ALTER TABLE approvals ADD COLUMN option_id TEXT REFERENCES recommendation_options(id) ON DELETE SET NULL`},
		{"recommendation_options.fix_prompt", "recommendation_options", "fix_prompt", `ALTER TABLE recommendation_options ADD COLUMN fix_prompt TEXT`},
		{"recommendations.rerun_instructions", "recommendations", "rerun_instructions", `ALTER TABLE recommendations ADD COLUMN rerun_instructions TEXT`},
		{"recommendations.created_at_nanos", "recommendations", "created_at_nanos", `ALTER TABLE recommendations ADD COLUMN created_at_nanos INTEGER`},
	}
	for _, m := range migrations {
		ran, err := ensureColumnExists(sqlDB, m.table, m.column, m.ddl)
		if err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("migrate db: %w", err)
		}
		if ran {
			applied = append(applied, m.name)
		}
	}
	backfilled, err := backfillRecommendationOptions(sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate db: %w", err)
	}
	if backfilled > 0 {
		applied = append(applied, fmt.Sprintf("backfill_recommendation_options(rows=%d)", backfilled))
	}

	return &DB{sql: sqlDB, migrationsApplied: applied}, nil
}

// MigrationsApplied returns the names of any schema migrations that
// actually ran during Open. Empty on a fully up-to-date DB. Lets the
// daemon log a one-line "schema upgraded: ..." breadcrumb without
// every other CLI command needing a logger.
func (d *DB) MigrationsApplied() []string {
	if len(d.migrationsApplied) == 0 {
		return nil
	}
	out := make([]string, len(d.migrationsApplied))
	copy(out, d.migrationsApplied)
	return out
}

func (d *DB) Close() error {
	return d.sql.Close()
}

func (d *DB) assertTableExists(name string) error {
	var found string
	err := d.sql.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if err != nil {
		return err
	}
	return nil
}

func newID() string {
	entropyMu.Lock()
	defer entropyMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

func nowUnix() int64 {
	return time.Now().Unix()
}

// ensureColumnExists returns (true, nil) when it actually applied the
// DDL, (false, nil) when the column was already present, or
// (false, err) on failure.
func ensureColumnExists(sqlDB *sql.DB, table string, column string, ddl string) (bool, error) {
	rows, err := sqlDB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var fieldType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &fieldType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	if _, err := sqlDB.Exec(ddl); err != nil {
		return false, err
	}
	return true, nil
}

// backfillRecommendationOptions copies legacy single-row recommendation
// data into recommendation_options for any recommendations that don't
// yet have an option (position 0). It also backfills approvals.option_id
// to point at the newly-created option for the recommendation. Idempotent.
// Returns the number of legacy recommendations that were backfilled.
func backfillRecommendationOptions(sqlDB *sql.DB) (int, error) {
	rows, err := sqlDB.Query(
		`SELECT r.id, r.item_id, r.created_at,
		        r.state_change, r.rationale, r.draft_comment, r.proposed_labels, r.confidence, r.followups
		 FROM recommendations r
		 LEFT JOIN recommendation_options o
		   ON o.recommendation_id = r.id AND o.position = 0
		 WHERE o.id IS NULL`,
	)
	if err != nil {
		return 0, fmt.Errorf("backfill options: scan recommendations: %w", err)
	}
	defer rows.Close()

	type legacyRec struct {
		recID          string
		itemID         string
		createdAt      int64
		stateChange    sql.NullString
		rationale      sql.NullString
		draftComment   sql.NullString
		proposedLabels sql.NullString
		confidence     sql.NullString
		followups      sql.NullString
	}
	var legacy []legacyRec
	for rows.Next() {
		var r legacyRec
		if err := rows.Scan(&r.recID, &r.itemID, &r.createdAt,
			&r.stateChange, &r.rationale, &r.draftComment, &r.proposedLabels, &r.confidence, &r.followups,
		); err != nil {
			return 0, fmt.Errorf("backfill options: scan: %w", err)
		}
		legacy = append(legacy, r)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("backfill options: rows err: %w", err)
	}
	if len(legacy) == 0 {
		return 0, nil
	}

	for _, r := range legacy {
		var waitingOn sql.NullString
		if err := sqlDB.QueryRow(`SELECT waiting_on FROM items WHERE id = ?`, r.itemID).Scan(&waitingOn); err != nil && err != sql.ErrNoRows {
			return 0, fmt.Errorf("backfill options: read item waiting_on: %w", err)
		}

		optionID := newID()
		if _, err := sqlDB.Exec(
			`INSERT INTO recommendation_options (
			 id, recommendation_id, position, state_change, rationale, draft_comment, fix_prompt, proposed_labels, confidence, waiting_on, followups, created_at
			) VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			optionID,
			r.recID,
			nullStringValue(r.stateChange),
			nullStringValue(r.rationale),
			nullStringValue(r.draftComment),
			"",
			nullStringValue(r.proposedLabels),
			nullStringValue(r.confidence),
			nullStringValue(waitingOn),
			nullStringValue(r.followups),
			r.createdAt,
		); err != nil {
			return 0, fmt.Errorf("backfill options: insert option for %s: %w", r.recID, err)
		}

		if _, err := sqlDB.Exec(
			`UPDATE approvals SET option_id = ? WHERE recommendation_id = ? AND option_id IS NULL`,
			optionID, r.recID,
		); err != nil {
			return 0, fmt.Errorf("backfill options: update approvals for %s: %w", r.recID, err)
		}
	}
	return len(legacy), nil
}

func nullStringValue(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}
