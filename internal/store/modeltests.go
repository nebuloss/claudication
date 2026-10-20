package store

import (
	"context"
	"fmt"
	"time"
)

// ModelTest is the last test of one model on one account: see
// account_model_tests in schema.sql.
type ModelTest struct {
	Model     string    `json:"model"`
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	Status    int       `json:"status"`
	LatencyMS int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
}

// SaveModelTest keeps a model's test result on an account, replacing the
// last one.
func (s *Store) SaveModelTest(ctx context.Context, accountID string, t ModelTest) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO account_model_tests (account_id, model, at, ok, status, latency_ms, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (account_id, model) DO UPDATE SET at = excluded.at, ok = excluded.ok,
		   status = excluded.status, latency_ms = excluded.latency_ms, error = excluded.error`,
		accountID, t.Model, ms(t.At), t.OK, t.Status, t.LatencyMS, t.Error); err != nil {
		return fmt.Errorf("save model test: %w", err)
	}
	return nil
}

// ModelTests are an account's last test of each model, by model.
func (s *Store) ModelTests(ctx context.Context, accountID string) (map[string]ModelTest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model, at, ok, status, latency_ms, error FROM account_model_tests WHERE account_id = ?`, accountID)
	if err != nil {
		return nil, fmt.Errorf("model tests: %w", err)
	}
	defer rows.Close()
	out := map[string]ModelTest{}
	for rows.Next() {
		var (
			t  ModelTest
			at int64
		)
		if err := rows.Scan(&t.Model, &at, &t.OK, &t.Status, &t.LatencyMS, &t.Error); err != nil {
			return nil, fmt.Errorf("model test: %w", err)
		}
		t.At = fromMS(at)
		out[t.Model] = t
	}
	return out, rows.Err()
}
