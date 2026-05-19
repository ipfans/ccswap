package lock_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipfans/ccswap/pkg/lock"
)

func TestAcquireAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	unlock, err := lock.Acquire(path, lock.DefaultTimeout)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Release should not panic.
	unlock()
}

func TestAcquireBlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	unlock, err := lock.Acquire(path, lock.DefaultTimeout)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	defer unlock()

	// Second acquire on the same file should time out.
	_, err = lock.Acquire(path, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for blocked lock, got nil")
	}
	t.Logf("got expected error: %v", err)
}

func TestParentDirCreated(t *testing.T) {
	// Use a nested path that does not exist yet.
	path := filepath.Join(t.TempDir(), "a", "b", "test.lock")

	unlock, err := lock.Acquire(path, lock.DefaultTimeout)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	defer unlock()
}

// TestConcurrentAcquire verifies cross-process locking by spawning a
// subprocess that holds the lock while the parent process tries to acquire
// the same lock. This exercises real OS-level flock contention rather than
// in-process goroutine contention (which does not contend on most Unixes).
func TestConcurrentAcquire(t *testing.T) {
	// When run as a subprocess with LOCK_HELPER set, just hold the lock.
	if lockPath := os.Getenv("LOCK_HELPER"); lockPath != "" {
		unlock, err := lock.Acquire(lockPath, 5*time.Second)
		if err != nil {
			os.Exit(2)
		}
		// Signal readiness to parent by writing to stdout.
		os.Stdout.WriteString("LOCKED\n")
		// Hold the lock until stdin is closed (parent closes the pipe).
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
		unlock()
		os.Exit(0)
		return
	}

	path := filepath.Join(t.TempDir(), "test.lock")

	// Start a subprocess that holds the lock.
	cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentAcquire$")
	cmd.Env = append(os.Environ(), "LOCK_HELPER="+path)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start subprocess: %v", err)
	}

	// Wait for subprocess to report that it holds the lock.
	buf := make([]byte, 64)
	n, err := stdoutPipe.Read(buf)
	if err != nil {
		t.Fatalf("failed to read from subprocess: %v", err)
	}
	if got := string(buf[:n]); got != "LOCKED\n" {
		t.Fatalf("unexpected subprocess output: %q", got)
	}

	// Now try to acquire the same lock from this process — should fail.
	_, err = lock.Acquire(path, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected error acquiring lock held by subprocess, got nil")
	}
	t.Logf("correctly failed to acquire contested lock: %v", err)

	// Release the subprocess so it can clean up.
	stdinPipe.Close()
	if err := cmd.Wait(); err != nil {
		t.Logf("subprocess exited with: %v (expected)", err)
	}
}
