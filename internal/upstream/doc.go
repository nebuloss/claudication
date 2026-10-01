// Package upstream relays a request to Anthropic on behalf of a pooled
// subscription account: choosing the account, retrying on another when the
// failure is the account's, streaming the answer back byte for byte, and
// reading usage and mid-stream errors out of it on the way past.
//
// What it does not do is change the request. Every change the gateway makes to
// a body is a named pass in internal/relay/passes, which is also where the
// rule they are exceptions to — the caller's bytes go upstream unchanged — is
// written down with the measurements behind each one. The relay runs whatever
// pipeline it is given and reports which passes acted ([Result].Rewrites).
//
// # Timing, not bytes
//
// One thing here changes when bytes are sent rather than what they are: a
// streaming answer's opening events are held until the first one carrying
// content, so an attempt the upstream accepted and then never worked on can be
// abandoned and sent again before the client has seen any of it. Every byte
// still arrives, in order and unchanged. stall.go has the measurements and the
// cost; passthrough.stall-timeout turns it off.
//
// # Where the rest is written down
//
// docs/refused-requests.md is every refusal with the measurements behind it,
// and the upstream constraints deliberately left alone.
// docs/upstream-request-pipeline.md is what the official client does between a
// prompt and its POST. docs/reversing.md is how any of it can be checked
// again. They exist so nobody reverses the client a second time.
package upstream
