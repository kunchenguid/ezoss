package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (d *DB) CountActiveRecommendations() (int, error) {
	var count int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE superseded_at IS NULL`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active recommendations: %w", err)
	}
	return count, nil
}

func (d *DB) RecommendationTokenTotalsForItem(itemID string) (RecommendationTokenTotals, error) {
	var totals RecommendationTokenTotals
	err := d.sql.QueryRow(
		`SELECT COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0)
		 FROM recommendations
		 WHERE item_id = ?`,
		itemID,
	).Scan(&totals.TokensIn, &totals.TokensOut)
	if err != nil {
		return RecommendationTokenTotals{}, fmt.Errorf("recommendation token totals: %w", err)
	}
	return totals, nil
}

// InsertRecommendation writes the parent recommendation and one row per
// Option in a single transaction. At least one option is required; the
// first option (Position 0 in storage) is the agent's top pick.
func (d *DB) InsertRecommendation(input NewRecommendation) (*Recommendation, error) {
	rec, err := d.insertRecommendationReplacingActiveBefore(input, time.Time{})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// InsertRecommendationReplacingActiveBefore inserts input unless an active
// recommendation for the same item was created after supersededAt. When it
// inserts, active recommendations created at or before supersededAt are
// superseded in the same transaction so older in-flight triage runs cannot
// overwrite newer reruns.
func (d *DB) InsertRecommendationReplacingActiveBefore(input NewRecommendation, supersededAt time.Time) (*Recommendation, bool, error) {
	rec, err := d.insertRecommendationReplacingActiveBefore(input, supersededAt)
	if err == errNewerActiveRecommendation {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return rec, true, nil
}

var errNewerActiveRecommendation = fmt.Errorf("newer active recommendation exists")

func (d *DB) insertRecommendationReplacingActiveBefore(input NewRecommendation, supersededAt time.Time) (*Recommendation, error) {
	if len(input.Options) == 0 {
		return nil, fmt.Errorf("insert recommendation: at least one option required")
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("insert recommendation: begin tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if !supersededAt.IsZero() {
		cutoff := supersededAt.UnixNano()
		var newerCount int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM recommendations WHERE item_id = ? AND superseded_at IS NULL AND COALESCE(created_at_nanos, created_at * 1000000000) > ?`,
			input.ItemID,
			cutoff,
		).Scan(&newerCount); err != nil {
			return nil, fmt.Errorf("insert recommendation: check newer active recommendation: %w", err)
		}
		if newerCount > 0 {
			return nil, errNewerActiveRecommendation
		}
		supersededAtSeconds := supersededAt.Unix()
		if _, err := tx.Exec(
			`UPDATE recommendations SET superseded_at = ? WHERE item_id = ? AND superseded_at IS NULL AND COALESCE(created_at_nanos, created_at * 1000000000) <= ?`,
			supersededAtSeconds,
			input.ItemID,
			cutoff,
		); err != nil {
			return nil, fmt.Errorf("insert recommendation: supersede active recommendations: %w", err)
		}
	}

	now := time.Now()
	rec := &Recommendation{
		ID:                newID(),
		ItemID:            input.ItemID,
		Agent:             input.Agent,
		Model:             input.Model,
		TokensIn:          input.TokensIn,
		TokensOut:         input.TokensOut,
		RerunInstructions: strings.TrimSpace(input.RerunInstructions),
		CreatedAt:         now.Unix(),
		CreatedAtNanos:    now.UnixNano(),
	}

	if _, err := tx.Exec(
		`INSERT INTO recommendations (
		 id, item_id, agent, model, rationale, draft_comment, followups, proposed_labels, state_change, confidence, tokens_in, tokens_out, rerun_instructions, created_at, created_at_nanos, superseded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID,
		rec.ItemID,
		rec.Agent,
		rec.Model,
		"", "", nil, nil, "", "",
		rec.TokensIn,
		rec.TokensOut,
		rec.RerunInstructions,
		rec.CreatedAt,
		rec.CreatedAtNanos,
		nil,
	); err != nil {
		return nil, fmt.Errorf("insert recommendation: %w", err)
	}

	rec.Options = make([]RecommendationOption, 0, len(input.Options))
	for i, opt := range input.Options {
		stored, err := insertOptionTx(tx, rec.ID, i, opt, rec.CreatedAt)
		if err != nil {
			return nil, err
		}
		rec.Options = append(rec.Options, stored)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("insert recommendation: commit: %w", err)
	}
	tx = nil

	return rec, nil
}

func insertOptionTx(tx *sql.Tx, recommendationID string, position int, input NewRecommendationOption, createdAt int64) (RecommendationOption, error) {
	followupsJSON, err := marshalStringSlice(input.Followups)
	if err != nil {
		return RecommendationOption{}, err
	}
	labelsJSON, err := marshalStringSlice(input.ProposedLabels)
	if err != nil {
		return RecommendationOption{}, err
	}

	opt := RecommendationOption{
		ID:               newID(),
		RecommendationID: recommendationID,
		Position:         position,
		StateChange:      input.StateChange,
		Rationale:        input.Rationale,
		DraftComment:     input.DraftComment,
		FixPrompt:        input.FixPrompt,
		Followups:        append([]string(nil), input.Followups...),
		ProposedLabels:   append([]string(nil), input.ProposedLabels...),
		Confidence:       input.Confidence,
		WaitingOn:        input.WaitingOn,
		CreatedAt:        createdAt,
	}

	if _, err := tx.Exec(
		`INSERT INTO recommendation_options (
		 id, recommendation_id, position, state_change, rationale, draft_comment, fix_prompt, proposed_labels, confidence, waiting_on, followups, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		opt.ID,
		opt.RecommendationID,
		opt.Position,
		opt.StateChange,
		opt.Rationale,
		opt.DraftComment,
		opt.FixPrompt,
		labelsJSON,
		opt.Confidence,
		opt.WaitingOn,
		followupsJSON,
		opt.CreatedAt,
	); err != nil {
		return RecommendationOption{}, fmt.Errorf("insert recommendation option: %w", err)
	}

	return opt, nil
}

func (d *DB) GetRecommendation(id string) (*Recommendation, error) {
	var rec Recommendation
	var supersededAt sql.NullInt64
	var rerunInstructions sql.NullString
	var createdAtNanos sql.NullInt64

	err := d.sql.QueryRow(
		`SELECT id, item_id, agent, model, tokens_in, tokens_out, rerun_instructions, created_at, created_at_nanos, superseded_at
		 FROM recommendations WHERE id = ?`,
		id,
	).Scan(
		&rec.ID,
		&rec.ItemID,
		&rec.Agent,
		&rec.Model,
		&rec.TokensIn,
		&rec.TokensOut,
		&rerunInstructions,
		&rec.CreatedAt,
		&createdAtNanos,
		&supersededAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get recommendation: %w", err)
	}

	if rerunInstructions.Valid {
		rec.RerunInstructions = rerunInstructions.String
	}
	if createdAtNanos.Valid {
		rec.CreatedAtNanos = createdAtNanos.Int64
	}
	rec.SupersededAt = unixToTimePtr(supersededAt)
	options, err := d.listOptionsForRecommendation(rec.ID)
	if err != nil {
		return nil, err
	}
	rec.Options = options
	return &rec, nil
}

func (d *DB) GetRecommendationOption(id string) (*RecommendationOption, error) {
	rows, err := d.sql.Query(
		`SELECT id, recommendation_id, position, state_change, rationale, draft_comment, fix_prompt, proposed_labels, confidence, waiting_on, followups, created_at
		 FROM recommendation_options WHERE id = ?`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("get recommendation option: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get recommendation option: %w", err)
		}
		return nil, nil
	}

	opt, err := scanOption(rows)
	if err != nil {
		return nil, err
	}
	return &opt, nil
}

func (d *DB) listOptionsForRecommendation(recommendationID string) ([]RecommendationOption, error) {
	rows, err := d.sql.Query(
		`SELECT id, recommendation_id, position, state_change, rationale, draft_comment, fix_prompt, proposed_labels, confidence, waiting_on, followups, created_at
		 FROM recommendation_options
		 WHERE recommendation_id = ?
		 ORDER BY position ASC`,
		recommendationID,
	)
	if err != nil {
		return nil, fmt.Errorf("list recommendation options: %w", err)
	}
	defer rows.Close()

	var options []RecommendationOption
	for rows.Next() {
		opt, err := scanOption(rows)
		if err != nil {
			return nil, err
		}
		options = append(options, opt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recommendation options: %w", err)
	}
	return options, nil
}

func scanOption(rows *sql.Rows) (RecommendationOption, error) {
	var opt RecommendationOption
	var fixPrompt sql.NullString
	var labels sql.NullString
	var followups sql.NullString
	if err := rows.Scan(
		&opt.ID,
		&opt.RecommendationID,
		&opt.Position,
		&opt.StateChange,
		&opt.Rationale,
		&opt.DraftComment,
		&fixPrompt,
		&labels,
		&opt.Confidence,
		&opt.WaitingOn,
		&followups,
		&opt.CreatedAt,
	); err != nil {
		return RecommendationOption{}, fmt.Errorf("scan recommendation option: %w", err)
	}
	if fixPrompt.Valid {
		opt.FixPrompt = fixPrompt.String
	}
	var err error
	opt.ProposedLabels, err = unmarshalStringSlice(labels)
	if err != nil {
		return RecommendationOption{}, fmt.Errorf("scan recommendation option: %w", err)
	}
	opt.Followups, err = unmarshalStringSlice(followups)
	if err != nil {
		return RecommendationOption{}, fmt.Errorf("scan recommendation option: %w", err)
	}
	return opt, nil
}

func (d *DB) ListActiveRecommendations() ([]Recommendation, error) {
	rows, err := d.sql.Query(
		`SELECT id, item_id, agent, model, tokens_in, tokens_out, rerun_instructions, created_at, created_at_nanos, superseded_at
		 FROM recommendations
		 WHERE superseded_at IS NULL
		 ORDER BY COALESCE(created_at_nanos, created_at * 1000000000) DESC, id DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list active recommendations: %w", err)
	}
	defer rows.Close()

	var recommendations []Recommendation
	for rows.Next() {
		var rec Recommendation
		var supersededAt sql.NullInt64
		var rerunInstructions sql.NullString
		var createdAtNanos sql.NullInt64
		if err := rows.Scan(
			&rec.ID,
			&rec.ItemID,
			&rec.Agent,
			&rec.Model,
			&rec.TokensIn,
			&rec.TokensOut,
			&rerunInstructions,
			&rec.CreatedAt,
			&createdAtNanos,
			&supersededAt,
		); err != nil {
			return nil, fmt.Errorf("list active recommendations: %w", err)
		}
		if rerunInstructions.Valid {
			rec.RerunInstructions = rerunInstructions.String
		}
		if createdAtNanos.Valid {
			rec.CreatedAtNanos = createdAtNanos.Int64
		}
		rec.SupersededAt = unixToTimePtr(supersededAt)
		recommendations = append(recommendations, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active recommendations: %w", err)
	}

	for i := range recommendations {
		options, err := d.listOptionsForRecommendation(recommendations[i].ID)
		if err != nil {
			return nil, err
		}
		recommendations[i].Options = options
	}

	return recommendations, nil
}

func (d *DB) MarkRecommendationSuperseded(id string, supersededAt time.Time) error {
	_, err := d.sql.Exec(`UPDATE recommendations SET superseded_at = ? WHERE id = ?`, supersededAt.Unix(), id)
	if err != nil {
		return fmt.Errorf("mark recommendation superseded: %w", err)
	}
	return nil
}
