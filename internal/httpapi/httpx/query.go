package httpx

import (
	"net/url"
	"strconv"
)

// QueryInt reads a positive whole number from the query string: def when it
// is absent, unreadable or not above zero, and max when it is larger.
//
// Lenient, because these are page sizes and windows in URLs people edit by
// hand: a hand-edited URL should show something sensible, never an error page.
// Clamped rather than reset, because a caller who asks for more than the
// ceiling wants as much as there is — answering 600 with the default 100, as
// one handler did when the store's bound and its own disagreed, looked like
// there was nothing more to show. The ceiling is the caller's, so one layer
// owns it.
func QueryInt(q url.Values, name string, def, max int) int {
	n, err := strconv.Atoi(q.Get(name))
	if err != nil || n <= 0 {
		return def
	}
	return min(n, max)
}
