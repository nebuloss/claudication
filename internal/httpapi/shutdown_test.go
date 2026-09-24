package httpapi

import (
	"log/slog"
	"net"
	"testing"
	"time"

	"claudication/internal/config"
	"claudication/internal/secret"
)

// A drain on the relay must not hold the admin port. With the servers shut
// down one after another it did, for as long as the slowest stream took, and a
// restart in that window could bind the relay port but never the admin one —
// the supervisor gave up and the gateway stayed down.
func TestDrainReleasesAdminPort(t *testing.T) {
	_, st, cfg := newTestServer(t)
	cfg.AdminListen = "127.0.0.1:0"
	cfg.Shutdown.Grace = config.Duration(5 * time.Second)
	sealer, err := secret.Load(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, slog.New(slog.DiscardHandler), st, sealer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	base, cancel, done := startServer(t, srv)
	adminAddr := srv.AdminAddr()

	// A request that has started and not finished, which Shutdown waits for:
	// half a request line is enough to mark the connection active.
	conn, err := net.Dial("tcp", base[len("http://"):])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /health HTTP/1.1\r\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for {
		ln, err := net.Listen("tcp", adminAddr)
		if err == nil {
			ln.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("admin port %s still bound while the relay drains: %v", adminAddr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	conn.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down")
	}
}
