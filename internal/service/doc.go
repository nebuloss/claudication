// Package service is the layer between the HTTP handlers and everything they
// stand on: what the gateway does, as opposed to how a request asks for it.
//
// Each sub-package is one service, handed what it uses rather than reaching
// for it — a store narrowed to the calls it makes, a function where one call
// is all it needs — so each can be tested on its own, with fakes, and without
// an HTTP server around it:
//
//	accounts  keeps each account's subscription usage current
//	limits    request rates per key and per address, token budgets per key
//	settings  the runtime switches an operator flips from the admin UI
//	surfaces  which client-facing APIs are served, one such switch each
//	titles    names chats, read off the wire or asked for
//	usage     records what each request cost, and prunes the history
//
// None of them speaks HTTP. Refusing a request, writing an error a client can
// read, deciding what an admin endpoint returns: that is internal/httpapi,
// which builds these and wires them together, and the surfaces below it that
// call them.
package service
