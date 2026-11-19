// Package service is the layer between the HTTP handlers and everything they
// stand on: what the gateway does, as opposed to how a request asks for it.
//
// Each sub-package is one service, handed what it uses rather than reaching
// for it — a store narrowed to the calls it makes, a function where one call
// is all it needs — so each can be tested on its own, with fakes, and without
// an HTTP server around it:
//
//	accounts      keeps each account's subscription usage current
//	availability  checks each account's models, and routes around failing ones
//	capture       records a client's content while an operator watches it
//	clientconfig  writes a typed client's files from the committed ones
//	clients       the kinds of client the gateway configures, their slots and names
//	install       the one-time links an install or update command is fetched by
//	limits        request rates per key and per address, token budgets per key
//	models        which model a request's name serves, and the model lists
//	settings      the runtime switches an operator flips from the admin UI
//	titles        names chats, read off the wire or asked for
//	uptime        samples whether the gateway serves, and keeps its outages
//	usage         records what each request cost, and prunes the history
//
// None of them speaks HTTP. Refusing a request, writing an error a client can
// read, deciding what an admin endpoint returns: that is internal/httpapi,
// which builds these and wires them together, and the surfaces below it that
// call them.
package service
