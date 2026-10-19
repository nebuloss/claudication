package admin

import (
	"context"
	"net/http"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/provider"
)

// providerFor is an account's provider, as the registry has it. A provider the
// registry does not know — a row left by a release that served one this one
// does not — answers 400 with the name, rather than a nil to fall over on.
func (s *Admin) providerFor(w http.ResponseWriter, id string) (provider.Accounts, bool) {
	if p, ok := s.providers.Find(id); ok {
		return p, true
	}
	httpx.WriteError(w, http.StatusBadRequest, "unsupported_provider",
		"this gateway does not serve the "+id+" provider")
	return nil, false
}

// defaultProvider is the provider a request that names none means: the first
// the gateway lists, which is Anthropic, the only one there was when the
// field was optional.
func (s *Admin) defaultProvider() string {
	if len(s.providers) == 0 {
		return ""
	}
	return s.providers[0].ID()
}

// poolStatus is the pool's view across every provider: which account would
// serve the next request of each, which are sitting out, and how many are
// usable. An account serves its own provider's requests, so "serving" is one
// account per provider, not one for the gateway.
type poolStatus struct {
	Serving     map[string]bool
	Cooling     map[string]time.Time
	Usable      int
	NeedsReauth int
}

func (s *Admin) poolStatus(ctx context.Context) (poolStatus, error) {
	out := poolStatus{Serving: map[string]bool{}, Cooling: map[string]time.Time{}}
	for _, p := range s.providers {
		st, err := s.pool.Status(ctx, p.ID())
		if err != nil {
			return poolStatus{}, err
		}
		if st.Serving != "" {
			out.Serving[st.Serving] = true
		}
		for id, until := range st.Cooling {
			out.Cooling[id] = until
		}
		out.Usable += st.Usable
		out.NeedsReauth += st.NeedsReauth
	}
	return out, nil
}

// refusal is what a recorded error really means when that is not what it
// says, in the vocabulary of the provider that answered; providerID empty is
// the default provider, for a row recorded before rows said whose they were.
func (s *Admin) refusal(providerID, text string) string {
	if text == "" {
		return ""
	}
	if providerID == "" {
		providerID = s.defaultProvider()
	}
	if p, ok := s.providers.Find(providerID); ok {
		return p.Refusal(text)
	}
	return ""
}
