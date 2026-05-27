package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrLockTimeout is returned when AcquireLock cannot obtain the lock within
// the given timeout.
var ErrLockTimeout = errors.New("state: lock already held by another process")

// Lock represents a held file lock. Release MUST be called.
type Lock struct {
	file *os.File
}

// AcquireLock blocks until it can flock(2)-lock the file at path, or the
// timeout elapses. The file (and its parent directory) are created if
// missing.
func AcquireLock(path string, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{file: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrLockTimeout
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Release unlocks and closes the underlying file.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

// LockHeld reports whether someone else is currently holding the lock at
// path. Returns false if the lock file is missing (no holder is possible).
// flock(2) auto-releases when the holding process exits, so a true result
// always means a live concurrent register/deregister.
func LockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open lock file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, fmt.Errorf("flock: %w", err)
	}
	// We just acquired it for an instant; release before returning so we
	// don't accidentally serialise ourselves out of subsequent operations.
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}
