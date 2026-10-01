package relay

import (
	"testing"

	"claudication/internal/testenv"
)

// The suite needs no network; see testenv.
func TestMain(m *testing.M) { testenv.Offline(m) }
