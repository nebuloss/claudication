package httpx

import (
	"net/url"
	"testing"
)

func TestQueryInt(t *testing.T) {
	for _, c := range []struct {
		query string
		want  int
	}{
		{"", 50},
		{"limit=", 50},
		{"limit=abc", 50},
		{"limit=0", 50},
		{"limit=-3", 50},
		{"limit=7", 7},
		{"limit=1000", 1000},
		{"limit=600000", 1000},
	} {
		q, _ := url.ParseQuery(c.query)
		if got := QueryInt(q, "limit", 50, 1000); got != c.want {
			t.Errorf("QueryInt(%q) = %d, want %d", c.query, got, c.want)
		}
	}
}
