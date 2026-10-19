package store

import (
	"database/sql"
	"time"
)

// Every moment the store keeps is an INTEGER of unix milliseconds, UTC: one
// form for every column, compared and ordered as a number. See schema.sql for
// what the text forms before it cost.
//
// Milliseconds rather than nanoseconds because nothing the gateway measures is
// finer, and truncated the way UnixMilli truncates, so a time read back is the
// one written to the millisecond and the conversion of old rows agrees with it.

// ms is t for a NOT NULL column.
func ms(t time.Time) int64 { return t.UnixMilli() }

// nullMS is t for a nullable column: NULL when unknown, which is meaningfully
// different from "expired at the zero time".
func nullMS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

// fromMS reads a NOT NULL column back, in UTC.
func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

// fromNullMS reads a nullable column back as the zero time when unset.
func fromNullMS(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return fromMS(v.Int64)
}

// optionalMS reads a nullable column whose absence the caller keeps as nil:
// never used, never refreshed, not disabled.
func optionalMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

// scanner is a *sql.Row or *sql.Rows, so one scan function serves a single
// read and a list alike.
type scanner interface{ Scan(...any) error }
