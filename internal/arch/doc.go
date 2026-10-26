// Package arch is the gateway's layering, written down where a test can hold
// the code to it.
//
// Every package sits on a layer, and imports only from layers below its own.
// Lowest first:
//
//	0  leaves         version logging memlimit secret oauth request anthropicwire
//	                  geminiwire api mcp service/limits service/settings
//	                  service/install service/clients configs
//	                  testenv handover
//	1  records        store config provider relay/passes api/anthropic
//	                  api/openai api/gemini
//	2  accounts       pool provider/anthropic provider/openai provider/google
//	                  service/accounts service/availability service/usage
//	                  service/uptime
//	                  service/clientconfig
//	3  relay          relay
//	4  services       service/titles service/models
//	5  HTTP plumbing  httpapi/httpx
//	6  HTTP surfaces  httpapi/gateway httpapi/admin httpapi/web httpapi/agent
//	7  composition    httpapi
//	8  binary         cmd/claudication
//
// Read upward, that is the path of a request: the binary starts the
// composition root, which hands each HTTP surface the services it uses; the
// gateway passes a request to the relay, which leases an account from the pool
// and speaks to the upstream through a provider.Wire.
//
// configs is the one package outside internal/ and cmd/: the committed client
// files, embedded from where they sit (go:embed reaches only below the file
// naming it). mcp is the Model Context Protocol's wire, which knows nothing of
// the gateway; httpapi/agent is the gateway's MCP server built on it, and
// service/clientconfig writes a client's files from the committed ones, for
// the admin page and the agent alike.
//
// Layering alone does not say three things worth saying, so they are rules of
// their own:
//
//   - The four HTTP surfaces do not import one another. What one needs from
//     another — the model list, the sign-in link at the root, the gateway's
//     key check in front of the MCP server, a client's share of the
//     subscriptions — is handed across by the composition root, as a
//     function.
//   - An upstream is known only to the composition root, which builds it.
//     The relay speaks to one through provider.Wire; the pool, the usage
//     poller, the recorder and the admin API through provider.Accounts, the
//     registry the root hands them. The admin API used to import Anthropic's
//     package directly, because connecting, testing and revoking an account
//     is a provider's own flow and there was one provider; the second brought
//     the interface, as this rule said it would.
//   - The client dialects are known only to the gateway, which serves them,
//     and the composition root, which registers them.
//
// A package that is not on the map fails the test. Placing a new package is
// the moment to decide what it may depend on; finding out later is how the
// layering wears away.
package arch
