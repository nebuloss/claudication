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

// facetCache holds the last reading. The lock is held while computing, so
// several screens opening at once wait for one scan rather than each running
// their own.
type facetCache struct {
	mu     sync.Mutex
	at     time.Time
	facets store.RequestFacets
}

func (c *facetCache) get(ctx context.Context, read func(context.Context) (store.RequestFacets, error)) (store.RequestFacets, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < facetsFor {
		return c.facets, nil
	}
	f, err := read(ctx)
	if err != nil {
		return store.RequestFacets{}, err
	}
	c.facets, c.at = f, time.Now()
	return f, nil
}
