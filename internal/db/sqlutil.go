package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func timeToUnixPtr(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.Unix()
}

func unixToTimePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	tm := time.Unix(v.Int64, 0)
	return &tm
}

func marshalStringSlice(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshal string slice: %w", err)
	}
	return string(data), nil
}

func unmarshalStringSlice(raw sql.NullString) ([]string, error) {
	if !raw.Valid || raw.String == "" {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw.String), &values); err != nil {
		return nil, fmt.Errorf("unmarshal string slice: %w", err)
	}
	return values, nil
}
