package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Where a model's last result came from: see account_model_tests in
// schema.sql.
const (
	// TestByOperator is the account page's Test.
	TestByOperator = "test"
	// TestByScan is the gateway's own check (internal/service/availability).
	TestByScan = "scan"
	// TestByTraffic is a client request the model served or was refused for.
	TestByTraffic = "traffic"
)

// ModelTest is the last known state of one model on one account: see
// account_model_tests in schema.sql.
type ModelTest struct {
	Model     string    `json:"model"`
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	Status    int       `json:"status"`
	LatencyMS int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	// Source is TestByOperator, TestByScan or TestByTraffic.
	Source string `json:"source"`
	// OKAt is when the model last worked on the account; zero when it has
	// not since it was first tried. A failure keeps it.
	OKAt time.Time `json:"ok_at,omitzero"`
}

// SaveModelTest keeps a model's latest result on an account, replacing the
// last one but not when the model last worked: a failure keeps that.
func (s *Store) SaveModelTest(ctx context.Context, accountID string, t ModelTest) error {
	if t.Source == "" {
		t.Source = TestByOperator
	}
	var okAt sql.NullInt64
	if t.OK {
		okAt = sql.NullInt64{Int64: ms(t.At), Valid: true}
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO account_model_tests (account_id, model, at, ok, status, latency_ms, error, source, ok_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (account_id, model) DO UPDATE SET at = excluded.at, ok = excluded.ok,
		   status = excluded.status, latency_ms = excluded.latency_ms, error = excluded.error,
		   source = excluded.source, ok_at = COALESCE(excluded.ok_at, ok_at)`,
		accountID, t.Model, ms(t.At), t.OK, t.Status, t.LatencyMS, t.Error, t.Source, okAt); err != nil {
		return fmt.Errorf("save model test: %w", err)
	}
	return nil
}

// ModelTests are an account's last result for each model, by model.
func (s *Store) ModelTests(ctx context.Context, accountID string) (map[string]ModelTest, error) {
	out := map[string]ModelTest{}
	err := s.modelTests(ctx, `WHERE account_id = ?`, []any{accountID}, func(_ string, t ModelTest) {
		out[t.Model] = t
	})
	return out, err
}

// FailingModelTests are every account's models whose last result failed, by
// account: what the gateway routes around and tests again.
func (s *Store) FailingModelTests(ctx context.Context) (map[string][]ModelTest, error) {
	out := map[string][]ModelTest{}
	err := s.modelTests(ctx, `WHERE NOT ok`, nil, func(account string, t ModelTest) {
		out[account] = append(out[account], t)
	})
	return out, err
}

func (s *Store) modelTests(ctx context.Context, where string, args []any, each func(account string, t ModelTest)) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT account_id, model, at, ok, status, latency_ms, error, source, ok_at
		   FROM account_model_tests `+where, args...)
	if err != nil {
		return fmt.Errorf("model tests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			account string
			t       ModelTest
			at      int64
			okAt    sql.NullInt64
		)
		if err := rows.Scan(&account, &t.Model, &at, &t.OK, &t.Status, &t.LatencyMS, &t.Error, &t.Source, &okAt); err != nil {
			return fmt.Errorf("model test: %w", err)
		}
		t.At = fromMS(at)
		if okAt.Valid {
			t.OKAt = fromMS(okAt.Int64)
		}
		each(account, t)
	}
	return rows.Err()
}
