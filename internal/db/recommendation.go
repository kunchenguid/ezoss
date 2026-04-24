package db

import (
	"database/sql"
	"fmt"
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

	rec := &Recommendation{
		ID:        newID(),
		ItemID:    input.ItemID,
		Agent:     input.Agent,
		Model:     input.Model,
		TokensIn:  input.TokensIn,
		TokensOut: input.TokensOut,
		CreatedAt: nowUnix(),
	}

	if _, err := tx.Exec(
		`INSERT INTO recommendations (
		 id, item_id, agent, model, rationale, draft_comment, followups, proposed_labels, state_change, confidence, tokens_in, tokens_out, created_at, superseded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID,
		rec.ItemID,
		rec.Agent,
		rec.Model,
		"", "", nil, nil, "", "",
		rec.TokensIn,
		rec.TokensOut,
		rec.CreatedAt,
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
		Followups:        append([]string(nil), input.Followups...),
		ProposedLabels:   append([]string(nil), input.ProposedLabels...),
		Confidence:       input.Confidence,
		WaitingOn:        input.WaitingOn,
		CreatedAt:        createdAt,
	}

	if _, err := tx.Exec(
		`INSERT INTO recommendation_options (
		 id, recommendation_id, position, state_change, rationale, draft_comment, proposed_labels, confidence, waiting_on, followups, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		opt.ID,
		opt.RecommendationID,
		opt.Position,
		opt.StateChange,
		opt.Rationale,
		opt.DraftComment,
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

	err := d.sql.QueryRow(
		`SELECT id, item_id, agent, model, tokens_in, tokens_out, created_at, superseded_at
		 FROM recommendations WHERE id = ?`,
		id,
	).Scan(
		&rec.ID,
		&rec.ItemID,
		&rec.Agent,
		&rec.Model,
		&rec.TokensIn,
		&rec.TokensOut,
		&rec.CreatedAt,
		&supersededAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get recommendation: %w", err)
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
		`SELECT id, recommendation_id, position, state_change, rationale, draft_comment, proposed_labels, confidence, waiting_on, followups, created_at
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
		`SELECT id, recommendation_id, position, state_change, rationale, draft_comment, proposed_labels, confidence, waiting_on, followups, created_at
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
	var labels sql.NullString
	var followups sql.NullString
	if err := rows.Scan(
		&opt.ID,
		&opt.RecommendationID,
		&opt.Position,
		&opt.StateChange,
		&opt.Rationale,
		&opt.DraftComment,
		&labels,
		&opt.Confidence,
		&opt.WaitingOn,
		&followups,
		&opt.CreatedAt,
	); err != nil {
		return RecommendationOption{}, fmt.Errorf("scan recommendation option: %w", err)
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
		`SELECT id, item_id, agent, model, tokens_in, tokens_out, created_at, superseded_at
		 FROM recommendations
		 WHERE superseded_at IS NULL
		 ORDER BY created_at DESC, id DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list active recommendations: %w", err)
	}
	defer rows.Close()

	var recommendations []Recommendation
	for rows.Next() {
		var rec Recommendation
		var supersededAt sql.NullInt64
		if err := rows.Scan(
			&rec.ID,
			&rec.ItemID,
			&rec.Agent,
			&rec.Model,
			&rec.TokensIn,
			&rec.TokensOut,
			&rec.CreatedAt,
			&supersededAt,
		); err != nil {
			return nil, fmt.Errorf("list active recommendations: %w", err)
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
