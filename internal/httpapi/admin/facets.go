package admin

import (
	"context"
	"sync"
	"time"

	"claudication/internal/store"
)

// facetsFor is how long one reading of the filter menus is served.
//
// RequestFacets is nine GROUP BYs over the whole of usage_events, most of them
// a full scan, and the request log asks for it each time it opens.
// The counts are totals over all of history, so a minute's lag in them is
// invisible, while recomputing them for every visit is the most expensive
// read the admin UI makes.
const facetsFor = 60 * time.Second

// facetsTimeout bounds one reading, which runs on past the request that
// started it.
const facetsTimeout = 30 * time.Second

// facetCache holds the last reading, and the one being taken.
//
// Several screens opening at once wait for one scan rather than each running
// their own. The scan does not run under the context of whoever started it:
// an operator who opens the log and leaves at once would otherwise cancel it,
// and every screen waiting on it would then scan again in turn. Each waiter
// can still give up on its own context.
type facetCache struct {
	mu      sync.Mutex
	at      time.Time
	facets  store.RequestFacets
	reading chan struct{} // closed when the reading under way is done; nil if none
	lastErr error         // how the last reading ended
}

func (c *facetCache) get(ctx context.Context, read func(context.Context) (store.RequestFacets, error)) (store.RequestFacets, error) {
	c.mu.Lock()
	if c.fresh() {
		f := c.facets
		c.mu.Unlock()
		return f, nil
	}
	if c.reading == nil {
		done := make(chan struct{})
		c.reading = done
		go c.take(context.WithoutCancel(ctx), read, done)
	}
	done := c.reading
	c.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return store.RequestFacets{}, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fresh() {
		return c.facets, nil
	}
	return store.RequestFacets{}, c.lastErr
}

func (c *facetCache) take(ctx context.Context, read func(context.Context) (store.RequestFacets, error), done chan struct{}) {
	ctx, cancel := context.WithTimeout(ctx, facetsTimeout)
	defer cancel()
	f, err := read(ctx)
	c.mu.Lock()
	if err == nil {
		c.facets, c.at = f, time.Now()
	}
	c.lastErr = err
	c.reading = nil
	c.mu.Unlock()
	close(done)
}

func (c *facetCache) fresh() bool {
	return !c.at.IsZero() && time.Since(c.at) < facetsFor
}
