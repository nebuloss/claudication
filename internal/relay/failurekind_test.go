package relay

import (
	"testing"

	"claudication/internal/pool"
	"claudication/internal/provider"
)

// A provider's verdict renames a failure — the retry path and the last
// attempt read it the same way — and never makes a failure of a success.
func TestFailureKindIsOneRule(t *testing.T) {
	forbidden := provider.Verdict{Kind: string(pool.FailureForbidden)}
	for _, c := range []struct {
		status pool.FailureKind
		v      provider.Verdict
		want   pool.FailureKind
	}{
		{pool.FailureRateLimit, forbidden, pool.FailureForbidden},
		{pool.FailureRateLimit, provider.Verdict{}, pool.FailureRateLimit},
		{"", forbidden, ""},
		{"", provider.Verdict{}, ""},
	} {
		if got := failureKind(c.status, c.v); got != c.want {
			t.Errorf("failureKind(%q, %q) = %q, want %q", c.status, c.v.Kind, got, c.want)
		}
	}
}
