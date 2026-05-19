package lock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// DefaultTimeout is the default duration to wait when acquiring a lock.
const DefaultTimeout = 10 * time.Second

// Acquire attempts to acquire an exclusive file lock at the given path,
// retrying until the timeout elapses. It creates the parent directory if
// it does not already exist.
//
// On success it returns an unlock function that the caller must invoke to
// release the lock. On failure it returns a descriptive error.
func Acquire(path string, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create lock directory: %w", err)
	}

	fl := flock.New(path)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ok, err := fl.TryLockContext(ctx, 200*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire lock: another ccswap instance may be running: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("failed to acquire lock: another ccswap instance may be running")
	}

	unlock := func() {
		_ = fl.Unlock()
	}
	return unlock, nil
}
