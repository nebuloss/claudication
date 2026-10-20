//go:build linux

package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// workers are this process's children: under `serve`, the supervisor's
// workers. Read from /proc, which is why the test is Linux's.
func workers(t *testing.T) []int {
	t.Helper()
	tasks, err := filepath.Glob("/proc/self/task/*/children")
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, f := range tasks {
		b, _ := os.ReadFile(f)
		for _, s := range strings.Fields(string(b)) {
			if p, err := strconv.Atoi(s); err == nil {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return out
}

// The real thing, end to end: `serve` comes up as a supervisor and a worker,
// SIGHUP replaces the worker with a new one from the binary on disk while a
// client loop runs, no connection fails, and the uptime record shows no
// outage — then the stop that follows is recorded as one.
func TestServeHandsOverOnSIGHUP(t *testing.T) {
	dir, cfg := stateDir(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("CLAUDICATION_LISTEN", addr)
	t.Setenv("CLAUDICATION_LOG_LEVEL", "error")

	done := make(chan error, 1)
	go func() { done <- cmdServe([]string{"-config", cfg}) }()

	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}
	up := func() error {
		resp, err := client.Get("http://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for up() != nil {
		if time.Now().After(deadline) {
			t.Fatal("serve never answered")
		}
		select {
		case err := <-done:
			t.Fatalf("serve exited before answering: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	first := workers(t)
	if len(first) != 1 {
		t.Fatalf("workers = %v, want one", first)
	}

	var ok, failed atomic.Int64
	stop := make(chan struct{})
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := up(); err != nil {
				failed.Add(1)
				t.Logf("request failed: %v", err)
			} else {
				ok.Add(1)
			}
		}
	}()

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	var now []int
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		now = workers(t)
		if len(now) == 1 && now[0] != first[0] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workers = %v after SIGHUP, want one new one in place of %v", now, first)
		}
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	<-loopDone
	if failed.Load() > 0 {
		t.Errorf("%d of %d requests failed across the handover", failed.Load(), failed.Load()+ok.Load())
	}
	t.Logf("%d requests across the handover from %d to %d, none failed", ok.Load(), first[0], now[0])

	st := openStore(t, dir)
	if outages, err := st.OutagesSince(context.Background(), time.Time{}); err != nil || len(outages) != 0 {
		t.Errorf("after the handover: outages %+v (%v), want none", outages, err)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v after SIGTERM", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
	if outages, err := st.OutagesSince(context.Background(), time.Time{}); err != nil || len(outages) != 1 {
		t.Errorf("after the stop: outages %+v (%v), want one", outages, err)
	}
	if w := workers(t); len(w) != 0 {
		t.Errorf("workers %v outlived the supervisor", w)
	}
}

// A SIGHUP that arrives while serve is still starting — before it has read
// its config — must not end it: Go's default for SIGHUP is to exit, and a
// reload sent just after a start used to kill the supervisor. Caught first
// thing, it is a reload once the supervisor runs.
func TestServeSurvivesASIGHUPWhileStarting(t *testing.T) {
	_, cfg := stateDir(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("CLAUDICATION_LISTEN", addr)
	t.Setenv("CLAUDICATION_LOG_LEVEL", "error")
	testHookStarting = func() { _ = syscall.Kill(os.Getpid(), syscall.SIGHUP) }
	defer func() { testHookStarting = nil }()

	done := make(chan error, 1)
	go func() { done <- cmdServe([]string{"-config", cfg}) }()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("serve never answered")
		}
		select {
		case err := <-done:
			t.Fatalf("serve exited: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}
