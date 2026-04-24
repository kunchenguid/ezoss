package db

import (
	"database/sql"
	"fmt"
	"time"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func (d *DB) InsertApproval(input NewApproval) (*Approval, error) {
	labelsJSON, err := marshalStringSlice(input.FinalLabels)
	if err != nil {
		return nil, err
	}

	approval := &Approval{
		ID:               newID(),
		RecommendationID: input.RecommendationID,
		OptionID:         input.OptionID,
		Decision:         input.Decision,
		FinalComment:     input.FinalComment,
		FinalLabels:      append([]string(nil), input.FinalLabels...),
		FinalStateChange: input.FinalStateChange,
		ActedAt:          input.ActedAt,
		ActedError:       input.ActedError,
		CreatedAt:        nowUnix(),
	}

	_, err = d.sql.Exec(
		`INSERT INTO approvals (
		 id, recommendation_id, option_id, decision, final_comment, final_labels, final_state_change, acted_at, acted_error, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		approval.ID,
		approval.RecommendationID,
		nullableString(approval.OptionID),
		approval.Decision,
		approval.FinalComment,
		labelsJSON,
		approval.FinalStateChange,
		timeToUnixPtr(approval.ActedAt),
		approval.ActedError,
		approval.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert approval: %w", err)
	}

	return approval, nil
}

func (d *DB) GetApproval(id string) (*Approval, error) {
	var approval Approval
	var optionID sql.NullString
	var finalLabels sql.NullString
	var actedAt sql.NullInt64

	err := d.sql.QueryRow(
		`SELECT id, recommendation_id, option_id, decision, final_comment, final_labels, final_state_change, acted_at, acted_error, created_at
		 FROM approvals WHERE id = ?`,
		id,
	).Scan(
		&approval.ID,
		&approval.RecommendationID,
		&optionID,
		&approval.Decision,
		&approval.FinalComment,
		&finalLabels,
		&approval.FinalStateChange,
		&actedAt,
		&approval.ActedError,
		&approval.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get approval: %w", err)
	}

	if optionID.Valid {
		approval.OptionID = optionID.String
	}
	approval.FinalLabels, err = unmarshalStringSlice(finalLabels)
	if err != nil {
		return nil, fmt.Errorf("get approval: %w", err)
	}
	approval.ActedAt = unixToTimePtr(actedAt)

	return &approval, nil
}

func (d *DB) ListApprovalsForRecommendation(recommendationID string) ([]Approval, error) {
	rows, err := d.sql.Query(
		`SELECT id, recommendation_id, option_id, decision, final_comment, final_labels, final_state_change, acted_at, acted_error, created_at
		 FROM approvals WHERE recommendation_id = ? ORDER BY created_at ASC`,
		recommendationID,
	)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	defer rows.Close()

	var approvals []Approval
	for rows.Next() {
		var approval Approval
		var optionID sql.NullString
		var finalLabels sql.NullString
		var actedAt sql.NullInt64

		if err := rows.Scan(
			&approval.ID,
			&approval.RecommendationID,
			&optionID,
			&approval.Decision,
			&approval.FinalComment,
			&finalLabels,
			&approval.FinalStateChange,
			&actedAt,
			&approval.ActedError,
			&approval.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("list approvals: %w", err)
		}

		if optionID.Valid {
			approval.OptionID = optionID.String
		}
		approval.FinalLabels, err = unmarshalStringSlice(finalLabels)
		if err != nil {
			return nil, fmt.Errorf("list approvals: %w", err)
		}
		approval.ActedAt = unixToTimePtr(actedAt)
		approvals = append(approvals, approval)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}

	return approvals, nil
}

func (d *DB) DismissOption(optionID string, finalLabels []string, actedAt time.Time) (*Approval, error) {
	return d.actOnOption("dismiss option", optionID, sharedtypes.ApprovalDecisionDismissed, "", finalLabels, sharedtypes.StateChangeNone, actedAt)
}

func (d *DB) ApproveOption(optionID string, finalComment string, finalLabels []string, finalStateChange sharedtypes.StateChange, actedAt time.Time) (*Approval, error) {
	return d.actOnOption("approve option", optionID, sharedtypes.ApprovalDecisionApproved, finalComment, finalLabels, finalStateChange, actedAt)
}

func (d *DB) EditOption(optionID string, finalComment string, finalLabels []string, finalStateChange sharedtypes.StateChange, actedAt time.Time) (*Approval, error) {
	return d.actOnOption("edit option", optionID, sharedtypes.ApprovalDecisionEdited, finalComment, finalLabels, finalStateChange, actedAt)
}

func (d *DB) actOnOption(actionName string, optionID string, decision sharedtypes.ApprovalDecision, finalComment string, finalLabels []string, finalStateChange sharedtypes.StateChange, actedAt time.Time) (*Approval, error) {
	labelsJSON, err := marshalStringSlice(finalLabels)
	if err != nil {
		return nil, err
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("%s: begin tx: %w", actionName, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	var recommendationID string
	if err := tx.QueryRow(`SELECT recommendation_id FROM recommendation_options WHERE id = ?`, optionID).Scan(&recommendationID); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%s: option %s not found", actionName, optionID)
		}
		return nil, fmt.Errorf("%s: load option: %w", actionName, err)
	}

	approval := &Approval{
		ID:               newID(),
		RecommendationID: recommendationID,
		OptionID:         optionID,
		Decision:         decision,
		FinalComment:     finalComment,
		FinalLabels:      append([]string(nil), finalLabels...),
		FinalStateChange: finalStateChange,
		ActedAt:          &actedAt,
		CreatedAt:        nowUnix(),
	}

	if _, err := tx.Exec(
		`INSERT INTO approvals (
		 id, recommendation_id, option_id, decision, final_comment, final_labels, final_state_change, acted_at, acted_error, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		approval.ID,
		approval.RecommendationID,
		approval.OptionID,
		approval.Decision,
		approval.FinalComment,
		labelsJSON,
		approval.FinalStateChange,
		timeToUnixPtr(approval.ActedAt),
		approval.ActedError,
		approval.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("%s: insert approval: %w", actionName, err)
	}

	if _, err := tx.Exec(`UPDATE recommendations SET superseded_at = ? WHERE id = ?`, actedAt.Unix(), recommendationID); err != nil {
		return nil, fmt.Errorf("%s: supersede recommendation: %w", actionName, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("%s: commit: %w", actionName, err)
	}
	tx = nil

	return approval, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
