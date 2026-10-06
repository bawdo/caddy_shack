package app

import (
	"os"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/state"
)

// TestMain points every App built with a nil caddy client at a closed port,
// so no unit test can reach the admin API of a real caddy_shack install on
// 127.0.0.1:2019. Tests that need Caddy pass their own httptest client. The
// admin wait is shortened so a test against that closed port fails fast.
func TestMain(m *testing.M) {
	defaultAdminURL = func(Instance) string { return "http://127.0.0.1:1" }
	adminReadyTimeout, adminReadyInterval = 200*time.Millisecond, 10*time.Millisecond
	os.Exit(m.Run())
}

// mustBuildConfig is a.buildConfig for tests, failing the test on error.
func mustBuildConfig(t *testing.T, a *App, s *state.State) []byte {
	t.Helper()
	cfg, err := a.buildConfig(s)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	return cfg
}
