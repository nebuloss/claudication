package usage

import (
	"testing"
	"time"
)

// A request is running from Start to its done, the longest-running first,
// and one client's are kept when asked; done twice ends it once.
func TestRunningRequests(t *testing.T) {
	var r Running
	base := time.Date(2026, 10, 29, 16, 0, 0, 0, time.UTC)
	late := r.Start(RunningRequest{StartedAt: base.Add(time.Minute), KeyID: "a", Model: "claude-opus-5-5"})
	early := r.Start(RunningRequest{StartedAt: base, KeyID: "b", Model: "gemini-4-argon-high"})

	all := r.List("")
	if len(all) != 2 || all[0].KeyID != "b" || all[1].KeyID != "a" {
		t.Fatalf("running = %+v, want b then a", all)
	}
	if one := r.List("a"); len(one) != 1 || one[0].Model != "claude-opus-5-5" {
		t.Errorf("client a's = %+v", one)
	}
	early()
	early()
	if all := r.List(""); len(all) != 1 || all[0].KeyID != "a" {
		t.Errorf("after one ended, running = %+v", all)
	}
	late()
	if all := r.List(""); len(all) != 0 {
		t.Errorf("after both ended, running = %+v", all)
	}
}
