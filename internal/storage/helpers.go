package storage

import "database/sql"

func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

// boolToInt stores a Go bool in SQLite's 0/1 INTEGER convention.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
