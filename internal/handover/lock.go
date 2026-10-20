//go:build unix

package handover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
// be interrupted by a context.
func FileLock(dir string) func(ctx context.Context, name string) (unlock func(), err error) {
	return func(ctx context.Context, name string) (func(), error) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return func() {}, fmt.Errorf("lock %s: %w", name, err)
		}
		sum := sha256.Sum256([]byte(name))
		path := filepath.Join(dir, hex.EncodeToString(sum[:8])+".lock")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return func() {}, fmt.Errorf("lock %s: %w", name, err)
		}
		for {
			err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				// Closing the file releases the lock.
				return func() { f.Close() }, nil
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
				f.Close()
				return func() {}, fmt.Errorf("lock %s: %w", name, err)
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
