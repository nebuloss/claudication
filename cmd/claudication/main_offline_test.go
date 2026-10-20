package main

import (
	"fmt"
	"os"
	"testing"

	"claudication/internal/handover"
	"claudication/internal/testenv"
)

// The suite needs no network; see testenv.
//
// It is also the worker: a test that runs `serve` runs it as a supervisor,
// which starts this binary again with the handover environment, and that
// process has to be the gateway rather than the suite.
func TestMain(m *testing.M) {
	if _, ok := os.LookupEnv(handover.EnvMode); ok {
		if err := run(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "claudication: "+err.Error())
			os.Exit(exitCode(err))
		}
		os.Exit(0)
	}
	testenv.Offline(m)
}
