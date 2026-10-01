package testenv

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) { Offline(m) }

// The real API is refused at once, and a loopback fake still answers: the two
// halves of what every package's TestMain relies on.
func TestOfflineRefusesTheRealAPIAndKeepsLoopback(t *testing.T) {
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	}
	_, err := client.Get("https://api.anthropic.com/v1/models")
	if err == nil || !strings.Contains(err.Error(), "proxyconnect") {
		t.Errorf("request to the real API: err = %v, want refused at the dead proxy", err)
	}

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fake.Close()
	resp, err := client.Get(fake.URL)
	if err != nil {
		t.Fatalf("loopback fake: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("loopback fake answered %d", resp.StatusCode)
	}
}
