package certstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readOnlyDir makes dir unwritable for the rest of the test.
func readOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// TestWriteFileAtomicFailuresKeepTheOriginal: a write that cannot complete
// must leave the file it was replacing byte for byte, and no temp file.
func TestWriteFileAtomicFailuresKeepTheOriginal(t *testing.T) {
	cases := map[string]struct {
		// setup prepares path and returns what must still be there after.
		setup   func(t *testing.T, path string)
		wantErr string
	}{
		"directory not writable": {
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
				readOnlyDir(t, filepath.Dir(path))
			},
			wantErr: "certstore: open tmp",
		},
		"target is a directory": {
			setup: func(t *testing.T, path string) {
				if err := os.MkdirAll(filepath.Join(path, "keep"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "certstore: rename tmp",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fullchain.pem")
			tc.setup(t, path)
			before := snapshot(t, path)
			err := writeFileAtomic(path, []byte("new"), 0o644)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if after := snapshot(t, path); after != before {
				t.Errorf("target changed: before %q after %q", before, after)
			}
			if _, err := os.Lstat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("temp file left behind: %v", err)
			}
		})
	}
}

// snapshot describes what is at path: a file's bytes, or a directory's
// entries.
func snapshot(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return "dir:" + strings.Join(names, ",")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return "file:" + string(b)
}

// A store root that is a file, not a directory, fails the save without
// writing anything.
func TestSaveFailsWhenStoreRootIsAFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	if err := os.WriteFile(root, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := New(root).Save("alpha.arrix.dev", []byte(testCertPEM), []byte(testKeyPEM))
	if err == nil || !strings.Contains(err.Error(), "certstore: mkdir") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(root); !bytes.Equal(b, []byte("not a dir")) {
		t.Errorf("root file changed: %q", b)
	}
}

// TestLoadDistinguishesMissingFromUnreadable: only a missing file is
// ErrNotFound, which callers treat as "no certificate" and may overwrite or
// skip. Anything else must surface, or a renewal's undo would delete
// material it could not back up.
func TestLoadDistinguishesMissingFromUnreadable(t *testing.T) {
	const domain = "alpha.arrix.dev"
	cases := map[string]struct {
		setup        func(t *testing.T, s *Store)
		wantNotFound bool
		wantErr      string
	}{
		"key missing": {
			setup: func(t *testing.T, s *Store) {
				if err := os.Remove(s.KeyPath(domain)); err != nil {
					t.Fatal(err)
				}
			},
			wantNotFound: true,
			wantErr:      "(key missing)",
		},
		"cert unreadable": {
			setup: func(t *testing.T, s *Store) {
				replaceWithDir(t, s.CertPath(domain))
			},
			wantErr: "certstore: read cert",
		},
		"key unreadable": {
			setup: func(t *testing.T, s *Store) {
				replaceWithDir(t, s.KeyPath(domain))
			},
			wantErr: "certstore: read key",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := New(t.TempDir())
			if err := s.Save(domain, []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, s)
			_, err := s.Load(domain)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if got := errors.Is(err, ErrNotFound); got != tc.wantNotFound {
				t.Errorf("ErrNotFound = %v, want %v", got, tc.wantNotFound)
			}
		})
	}
}

// replaceWithDir swaps the file at path for a directory, which reads fail
// on with something other than "not exist".
func replaceWithDir(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestHasAndRemoveRejectInvalidDomains(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if s.Has("../escape") {
		t.Error("Has accepted a path-escaping domain")
	}
	if err := s.Remove("../escape"); err == nil {
		t.Error("Remove accepted a path-escaping domain")
	}
}

// A removal the filesystem refuses is reported and leaves the material.
func TestRemoveFailureKeepsMaterial(t *testing.T) {
	const domain = "alpha.arrix.dev"
	s := New(t.TempDir())
	if err := s.Save(domain, []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
		t.Fatal(err)
	}
	readOnlyDir(t, filepath.Dir(s.CertPath(domain)))
	err := s.Remove(domain)
	if err == nil || !strings.Contains(err.Error(), "certstore: remove "+domain) {
		t.Fatalf("err = %v", err)
	}
	if m, err := s.Load(domain); err != nil || string(m.CertPEM) != testCertPEM {
		t.Errorf("material after a failed remove: %q %v", m.CertPEM, err)
	}
}
