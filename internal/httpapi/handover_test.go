package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"claudication/internal/secret"
	"claudication/internal/store"
)

// A handover is not an outage: the socket never closed, so nothing was
// refused, and the status page must not say otherwise. A stop still is one.
func TestAHandoverRecordsNoOutage(t *testing.T) {
	ctx := context.Background()
	oldSrv, st, cfg := newTestServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// The same socket for the new process, as the supervisor hands it down.
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	newLn, err := net.FileListener(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()

	release := make(chan struct{})
	oldReady, oldDone := make(chan struct{}), make(chan error, 1)
	go func() {
		oldDone <- oldSrv.Serve(ctx, Handoff{
			Listeners: map[string]net.Listener{ListenRelay: ln},
			Ready:     func() { close(oldReady) },
			Release:   release,
		})
	}()
	<-oldReady
	if !health(t, base)["reloadable"].(bool) {
		t.Error("/health does not say a supervised worker is reloadable")
	}

	// The new worker, on the same database as the old, opened beside it.
	sealer, err := secret.Load(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := store.OpenAlongside(ctx, filepath.Join(cfg.StateDir, "test.db"), sealer)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	newSrv, err := New(cfg, slog.New(slog.DiscardHandler), st2)
	if err != nil {
		t.Fatal(err)
	}
	stopNew, cancel := context.WithCancel(ctx)
	defer cancel()
	newReady, newDone := make(chan struct{}), make(chan error, 1)
	go func() {
		newDone <- newSrv.Serve(stopNew, Handoff{
			Listeners: map[string]net.Listener{ListenRelay: newLn},
			Ready:     func() { close(newReady) },
			Release:   make(chan struct{}),
		})
	}()
	<-newReady

	close(release)
	select {
	case err := <-oldDone:
		if err != nil {
			t.Fatalf("the released server returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the released server did not drain")
	}
	if health(t, base)["status"] != "ok" {
		t.Error("nothing answers after the handover")
	}
	if outages, err := st.OutagesSince(ctx, time.Time{}); err != nil || len(outages) != 0 {
		t.Errorf("after a handover: outages %+v (%v), want none", outages, err)
	}

	cancel()
	if err := <-newDone; err != nil {
		t.Fatal(err)
	}
	if outages, err := st.OutagesSince(ctx, time.Time{}); err != nil || len(outages) != 1 {
		t.Errorf("after a stop: outages %+v (%v), want the one", outages, err)
	}
}

// A worker given sockets serves only those: binding one of its own would
// hold a port the supervisor does not, and the next handover would find it
// taken.
func TestAnInheritedServerBindsNothingOfItsOwn(t *testing.T) {
	srv, _, _ := newTestServer(t)
	err := srv.Serve(context.Background(), Handoff{Listeners: map[string]net.Listener{}})
	if err == nil {
		t.Fatal("served without the socket it was meant to inherit")
	}
}

// Released, a worker stops its background writers at once — the sampler,
// the pruners, the sweepers, the poller — and not only after its last stream
// ends: they belong to the process taking requests, which is the other one.
func TestAReleasedServerStopsItsBackgroundWorkAtOnce(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- srv.Serve(context.Background(), Handoff{
			Listeners: map[string]net.Listener{ListenRelay: ln},
			Ready:     func() { close(ready) },
			Release:   release,
		})
	}()
	<-ready
	// A request in flight, its body still coming, so the drain cannot end.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "POST /admin/session HTTP/1.1\r\nHost: t\r\nContent-Length: 100\r\n\r\n{")
	time.Sleep(100 * time.Millisecond)

	close(release)
	select {
	case <-srv.stopSweeper:
	case <-time.After(2 * time.Second):
		t.Fatal("the background work still runs after the release")
	}
	select {
	case err := <-done:
		t.Fatalf("returned with a request in flight: %v", err)
	default:
	}
	conn.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("did not finish draining")
	}
}

func health(t *testing.T, base string) map[string]any {
	t.Helper()
	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
