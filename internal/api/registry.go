package api

// Registry holds the protocols the gateway serves, in the order they should be
// presented.
//
// A slice rather than a map: the admin UI lists surfaces and an order that
// changes between page loads reads as a bug. It is built once, at startup, by
// internal/httpapi — the composition root, and the only place that knows which
// dialects exist.
type Registry []Protocol

// Find returns the protocol with this ID.
func (r Registry) Find(id string) (Protocol, bool) {
	for _, p := range r {
		if p.ID() == id {
			return p, true
		}
	}
	return nil, false
}
