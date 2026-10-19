package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"claudication/internal/store"
)

// One reading serves the minute; a failed one is not kept, and an old one is
// read again.
func TestFacetCacheServesOneReadingAMinute(t *testing.T) {
	var c facetCache
	reads := 0
	read := func(context.Context) (store.RequestFacets, error) {
		reads++
		return store.RequestFacets{Models: []store.FacetValue{{Value: "m", Count: int64(reads)}}}, nil
	}
	fail := func(context.Context) (store.RequestFacets, error) {
		return store.RequestFacets{}, errors.New("boom")
	}
	ctx := context.Background()

	if _, err := c.get(ctx, fail); err == nil {
		t.Fatal("a failed read was not reported")
	}
	first, _ := c.get(ctx, read)
	second, _ := c.get(ctx, read)
	if reads != 1 || second.Models[0].Count != first.Models[0].Count {
		t.Errorf("%d reads inside the minute, want 1", reads)
	}

	c.at = time.Now().Add(-facetsFor - time.Second)
	if third, _ := c.get(ctx, read); reads != 2 || third.Models[0].Count != 2 {
		t.Errorf("an expired reading was served again: %d reads", reads)
	}
}

// A screen that leaves while the menus are being read does not cancel the
// reading for the screens waiting on it, and they share the one scan.
func TestFacetCacheOutlivesTheScreenThatAsked(t *testing.T) {
	var c facetCache
	reads := 0
	release := make(chan struct{})
	read := func(ctx context.Context) (store.RequestFacets, error) {
		reads++
		<-release
		if err := ctx.Err(); err != nil {
			return store.RequestFacets{}, err
		}
		return store.RequestFacets{Models: []store.FacetValue{{Value: "m", Count: 1}}}, nil
	}

	leaving, leave := context.WithCancel(context.Background())
	gone := make(chan error, 1)
	go func() {
		_, err := c.get(leaving, read)
		gone <- err
	}()
	for {
		c.mu.Lock()
		started := c.reading != nil
		c.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	leave()
	if err := <-gone; !errors.Is(err, context.Canceled) {
		t.Fatalf("the screen that left got %v, want context.Canceled", err)
	}

	waited := make(chan store.RequestFacets, 1)
	go func() {
		f, _ := c.get(context.Background(), read)
		waited <- f
	}()
	close(release)
	if f := <-waited; len(f.Models) != 1 || reads != 1 {
		t.Errorf("waiter got %v after %d reads, want one reading from one scan", f.Models, reads)
	}
}
