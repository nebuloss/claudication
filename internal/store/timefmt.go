package store

import (
	"database/sql"
	"time"
)

// The store writes moments in two text forms, and reads both back here.
//
//   - stamp: fixed-width UTC with nine fraction digits, for every column that
//     is compared or ordered as text — usage_events.at, login links, chat
//     titles. See stampLayout.
//   - RFC3339 to the second, for the columns that are only ever displayed or
//     compared in Go — when a key or an account was made, last used, last
//     refreshed. Older than stamp, and kept: rewriting them buys nothing, and
//     every one of them parses with the same layout.
//
// Reading is lenient on purpose. None of these columns is required for a key
// or an account to work, so a value that does not parse reads as unknown
// rather than failing the whole list it is part of.

// stampLayout is how a moment is written into a column that is compared or
// ordered as text: always UTC, always nine fraction digits.
//
// Fixed width is the point. SQLite compares these columns as strings, and
// RFC3339Nano drops trailing zeros — 10:00:00.5Z is shorter than 10:00:00Z
// and sorts before it, so a window starting on a whole second miscounted the
// events inside that second. With every value the same width, text order is
// time order. Go's RFC3339Nano parser reads it back unchanged.
const stampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// stamp formats t for such a column.
func stamp(t time.Time) string { return t.UTC().Format(stampLayout) }

// nullTime renders an optional timestamp for SQLite: NULL when unknown, which
// is meaningfully different from "expired at the zero time".
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// formatOrEmpty is nullTime for a column that is NOT NULL: empty when unknown.
func formatOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// parseRFC3339 reads a second-precision column: an unset or unreadable value
// is the zero time. RFC3339 parsing accepts a fraction, so a stamp reads too.
func parseRFC3339(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseOptional is parseRFC3339 for a nullable column whose absence the
// caller keeps as nil: never used, never refreshed, not disabled.
func parseOptional(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v.String)
	if err != nil {
		return nil
	}
	return &t
}

// scanner is a *sql.Row or *sql.Rows, so one scan function serves a single
// read and a list alike.
type scanner interface{ Scan(...any) error }
