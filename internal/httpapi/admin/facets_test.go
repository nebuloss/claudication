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
