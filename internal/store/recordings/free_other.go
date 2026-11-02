//go:build !linux

package recordings

// Free is not read on this system: the budget alone bounds what is stored.
// The gateway's own service targets are Linux; the other release builds are
// for running it by hand.
func Free(string) (int64, bool) { return 0, false }
