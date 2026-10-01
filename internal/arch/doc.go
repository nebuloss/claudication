// Package arch is the gateway's layering, written down where a test can hold
// the code to it.
//
// Every package sits on a layer, and imports only from layers below its own.
// Lowest first:
//
//	0  leaves         version logging memlimit secret oauth request
//	                  provider api service/limits service/settings testenv
//	1  records        store config relay/passes api/anthropic api/openai
//	2  accounts       pool provider/anthropic service/accounts service/usage
//	                  service/surfaces
//	3  relay          relay
//	4  services       service/titles
//	5  HTTP plumbing  httpapi/httpx
//	6  HTTP surfaces  httpapi/gateway httpapi/admin httpapi/web
//	7  composition    httpapi
//	8  binary         cmd/claudication
//
// Read upward, that is the path of a request: the binary starts the
// composition root, which hands each HTTP surface the services it uses; the
// gateway passes a request to the relay, which leases an account from the pool
// and speaks to the upstream through a provider.Wire.
//
// Layering alone does not say three things worth saying, so they are rules of
// their own:
//
//   - The three HTTP surfaces do not import one another. What one needs from
//     another — the model list, the sign-in link at the root — is handed
//     across by the composition root, as a function.
//   - Anthropic, as an upstream, is known only where it has to be. The relay,
//     the pool and the services speak to it through provider.Wire and are
//     handed its functions (token refresh, error classification) by the
//     composition root. The admin API is the one exception, because
//     connecting, testing and revoking an account is a provider's own OAuth
//     flow, and there is one provider; a second would bring the interface that
//     flow does not need yet.
//   - The client dialects are known only to the gateway, which serves them,
//     and the composition root, which registers them.
//
// A package that is not on the map fails the test. Placing a new package is
// the moment to decide what it may depend on; finding out later is how the
// layering wears away.
package arch
