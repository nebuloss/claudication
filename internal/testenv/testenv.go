// Package testenv keeps the test suite off the network.
//
// The tests promise to need neither credentials nor a network, and every test
// that talks to Anthropic does so through an in-process fake. But the real
// address is the default, so a test that forgets to point a client at its
// fake sends a real request to api.anthropic.com — it happened once, with a
// fake token, and was answered 401. Nothing failed; the test simply asserted
// on the wrong thing.
//
// Offline closes that door without a hook in production code. Every client
// the gateway builds takes its proxy from the environment, and Go never
// proxies a loopback address, so pointing the proxy at a port nothing listens
// on leaves the httptest fakes reachable and makes any other destination fail
// at once with a connection refused.
package testenv

import (
	"os"
	"testing"
)

// deadProxy is a port reserved for nothing, on a host that answers refused
// immediately rather than timing out.
const deadProxy = "http://127.0.0.1:1"

// Offline points every outbound proxy at deadProxy, then runs the tests. Call
// it from a package's TestMain:
//
//	func TestMain(m *testing.M) { testenv.Offline(m) }
func Offline(m *testing.M) {
	for _, v := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		os.Setenv(v, deadProxy)
	}
	// An exemption inherited from the developer's shell would reopen it.
	for _, v := range []string{"NO_PROXY", "no_proxy"} {
		os.Unsetenv(v)
	}
	os.Exit(m.Run())
}
