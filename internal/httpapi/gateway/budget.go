package gateway

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/limits"
	"claudication/internal/store"
)

// withinBudget answers whether the key may spend, and writes the refusal when
// it may not. Returns true to carry on. The counting is limits.Budgets'; this
// is the part that speaks HTTP.
//
// A key can overshoot its budget by one request, necessarily: what a request
// will cost is only known once it has been served. The budget is a ceiling on
// what has already been spent, not a reservation against what is about to be.
func (s *Gateway) withinBudget(w http.ResponseWriter, r *http.Request, key store.APIKey) bool {
	if key.TokenBudget <= 0 || !s.cfg.Usage.Enabled() {
		// No budget, or no usage history to measure one against — enforcing a
		// budget with retention off would refuse everything the moment the
		// figure could not be read.
		return true
	}

	spent, err := s.budgets.SpentBy(r.Context(), key.ID)
	if err != nil {
		// Fail open, loudly. A database that cannot be read is a reason to
		// stop counting, not a reason to stop serving: turning a transient
		// storage fault into a total outage is the worse failure, and the
		// budget exists to bound spending rather than to guard anything.
		s.log.Error("could not read the token budget; allowing the request",
			"err", err, "api_key", key.Display(), "request_id", httpx.RequestID(r.Context()))
		return true
	}
	if spent < key.TokenBudget {
		return true
	}

	// Say when it frees up rather than leaving the client to guess. The window
	// rolls, so the first relief comes when the oldest counted request ages out.
	if at, ok := s.store.OldestSpendAt(r.Context(), key.ID, s.budgets.Now().Add(-limits.BudgetWindow)); ok {
		if wait := time.Until(at.Add(limits.BudgetWindow)); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
	}
	s.log.Warn("api key is over its token budget",
		"api_key", key.Display(), "api_key_name", key.Name,
		"spent", spent, "budget", key.TokenBudget,
		"request_id", httpx.RequestID(r.Context()))

	// rate_limit_error because that is the type a client knows how to read, and
	// this is the same shape of problem: too much, too soon, try later.
	httpx.WriteError(w, http.StatusTooManyRequests, "rate_limit_error",
		fmt.Sprintf("this API key has spent %d of its %d token budget for the last %s",
			spent, key.TokenBudget, limits.BudgetWindow))
	return false
}
