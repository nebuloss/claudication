package admin

import "testing"

// The main thread is the conversation a person started, not the thread with
// the largest prompt: a subagent reading a large repository outgrows a main
// thread that is still short, and a title asked first is one request.
func TestTheMainThreadIsTheOneTheChatBeganWith(t *testing.T) {
	title := turnCount{root: "title", system: "titler"}
	main := turnCount{root: "hello", system: "cc"}
	sub := turnCount{root: "task", system: "agent"}
	compacted := turnCount{root: "summary", system: "cc"}
	counts := []turnCount{title, main, main, sub, sub, sub, main, {unread: "not json"}, compacted, compacted}
	prompts := []int64{100, 2_000, 3_000, 90_000, 120_000, 150_000, 4_000, 0, 1_000, 2_000}

	threads, of := groupThreads(counts, prompts)
	if len(threads) != 4 {
		t.Fatalf("threads = %+v", threads)
	}
	want := []int{3, 1, 1, 4, 4, 4, 1, 0, 2, 2}
	for i := range want {
		if of[i] != want[i] {
			t.Errorf("request %d on thread %d, want %d (threads %+v)", i, of[i], want[i], threads)
		}
	}
	if !threads[0].Main || !threads[1].Main || threads[1].Label != "Main, continued" || threads[2].Main || threads[3].Main {
		t.Errorf("threads = %+v", threads)
	}

	// No thread of more than one request: the largest prompt is main.
	threads, of = groupThreads([]turnCount{title, sub}, []int64{100, 5_000})
	if !threads[0].Main || of[1] != 1 {
		t.Errorf("fallback: threads %+v, of %v", threads, of)
	}
}
