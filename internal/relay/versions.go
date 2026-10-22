package relay

import "context"

// Rebind makes a request's body name the version the leased account serves,
// for a request any of several versions can (pool.WithVersions): the gateway
// puts the model in, and fits the output budget to that model's cap, since
// both depend on which account took it and so are known only per attempt.
//
// A function handed in by the gateway rather than done here, because what a
// model means for a body — which field names it, which limit is fitted to
// it — is the dialect's and the model list's, and the relay knows neither.
// It is given the body as the passes left it, never a previous attempt's, so
// a retry on an account with another version is rebuilt from the same bytes.
type Rebind func(body []byte, model string) []byte

type rebindKey struct{}

// WithRebind hands the relay a request's Rebind.
func WithRebind(ctx context.Context, rb Rebind) context.Context {
	return context.WithValue(ctx, rebindKey{}, rb)
}

// forAttempt is the body one attempt sends: the passes' body, rebound to the
// version the lease serves when there is one to bind.
func forAttempt(ctx context.Context, body []byte, model string) []byte {
	if model == "" {
		return body
	}
	if rb, ok := ctx.Value(rebindKey{}).(Rebind); ok && rb != nil {
		return rb(body, model)
	}
	return body
}
