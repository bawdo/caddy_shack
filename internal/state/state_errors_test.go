package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSaveFailuresKeepThePreviousState: a save that cannot complete leaves
// the registry already on disk exactly as it was, and no temp file, so the
// next load still reads the last good state.
func TestSaveFailuresKeepThePreviousState(t *testing.T) {
	cases := map[string]struct {
		// setup returns the state path to save to.
		setup   func(t *testing.T, dir string) string
		wantErr string
	}{
		"parent is a file": {
			setup: func(t *testing.T, dir string) string {
				parent := filepath.Join(dir, "notadir")
				if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(parent, "state.json")
			},
			wantErr: "mkdir state dir",
		},
		"directory not writable": {
			setup: func(t *testing.T, dir string) string {
				if os.Geteuid() == 0 {
					t.Skip("root ignores directory permissions")
				}
				path := filepath.Join(dir, "state.json")
				if err := Save(path, &State{Backends: map[string]Backend{"keep": {Domain: "keep.test"}}}); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				return path
			},
			wantErr: "open tmp",
		},
		"target is a directory": {
			setup: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "state.json")
				if err := os.MkdirAll(filepath.Join(path, "keep"), 0o700); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantErr: "rename tmp",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := tc.setup(t, t.TempDir())
			before, beforeErr := os.ReadFile(path)
			err := Save(path, &State{Backends: map[string]Backend{"new": {Domain: "new.test"}}})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			after, afterErr := os.ReadFile(path)
			if !bytes.Equal(before, after) || (beforeErr == nil) != (afterErr == nil) {
				t.Errorf("state changed:\n before %q (%v)\n after  %q (%v)", before, beforeErr, after, afterErr)
			}
			if _, err := os.Lstat(path + ".tmp"); err == nil {
				t.Error("temp file left behind")
			}
		})
	}
}

// A state file that exists but cannot be read is a plain error, not
// corruption: the user has a permissions or I/O problem to fix, not a file
// to repair or delete.
func TestLoadUnreadableIsNotCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "read state") {
		t.Fatalf("err = %v", err)
	}
	if IsCorrupt(err) {
		t.Errorf("unreadable file classified as corrupt: %v", err)
	}
}

// ErrCorrupt keeps its cause reachable, so a caller can tell a JSON syntax
// error from an unknown schema version.
func TestErrCorruptUnwrapsToItsCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("err = %v, want a *json.SyntaxError underneath", err)
	}
	cause := errors.New("cause")
	if got := (&ErrCorrupt{Path: path, Reason: cause}).Unwrap(); got != cause {
		t.Errorf("Unwrap = %v, want the reason", got)
	}
}

func TestAcquireLockFailures(t *testing.T) {
	cases := map[string]struct {
		setup   func(t *testing.T, dir string) string
		wantErr string
	}{
		"parent is a file": {
			setup: func(t *testing.T, dir string) string {
				parent := filepath.Join(dir, "notadir")
				if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(parent, "state.json.lock")
			},
			wantErr: "create lock dir",
		},
		"lock path is a directory": {
			setup: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "state.json.lock")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantErr: "open lock file",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := tc.setup(t, t.TempDir())
			l, err := AcquireLock(path, 50*time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if errors.Is(err, ErrLockTimeout) {
				t.Error("a lock that could not be opened read as contention")
			}
			if l != nil {
				t.Error("a lock was returned with the error")
			}
		})
	}
}

// LockHeld reports a lock file it cannot open as an error, not as free:
// callers use false to mean "safe to go ahead".
func TestLockHeldUnopenableIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json.lock")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := LockHeld(path)
	if err == nil || !strings.Contains(err.Error(), "open lock file") || held {
		t.Errorf("held=%v err=%v, want an open error", held, err)
	}
}

// Release is safe on a nil or zero Lock, so a deferred release after a
// failed acquire cannot panic.
func TestReleaseOnNilLockIsSafe(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Errorf("nil Release: %v", err)
	}
	if err := (&Lock{}).Release(); err != nil {
		t.Errorf("zero Release: %v", err)
	}
}
