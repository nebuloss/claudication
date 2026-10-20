//go:build unix

package handover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// FileLock returns a lock per name, held across processes: a file under dir
// for each name, flock'd. For the work two workers overlapping in a handover
// must not both do — refreshing one account's token, whose refresh token the
// provider rotates and the slower of the two would find already spent.
//
// One file per name rather than one for everything, so a refresh that takes
// its full minute does not hold up every other account's. Names are hashed
// into file names, so any string is safe.
//
// The returned unlock is never nil. Waiting is polled, because flock cannot
// be interrupted by a context; running out of ctx while another holds the
// lock is the one error.
//
// A lock that cannot be taken at all — the directory unwritable, flock not
// supported — is logged and skipped rather than returned. What it guards is a
// race that needs a handover to happen during a refresh; failing every
// refresh would take every account offline for certain, to avoid a chance
// of taking one.
func FileLock(dir string, log *slog.Logger) func(ctx context.Context, name string) (unlock func(), err error) {
	unusable := func(name string, err error) (func(), error) {
		log.Error("cannot lock token refreshes across processes; refreshing with only this process's own lock "+
			"(a handover overlapping this refresh could spend the refresh token twice)",
			"dir", dir, "name", name, "err", err)
		return func() {}, nil
	}
	return func(ctx context.Context, name string) (func(), error) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return unusable(name, err)
		}
		sum := sha256.Sum256([]byte(name))
		path := filepath.Join(dir, hex.EncodeToString(sum[:8])+".lock")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return unusable(name, err)
		}
		for {
			err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				// Closing the file releases the lock.
				return func() { f.Close() }, nil
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
				f.Close()
				return unusable(name, err)
			}
			select {
			case <-ctx.Done():
				f.Close()
				return func() {}, fmt.Errorf("lock %s: %w", name, ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
}
