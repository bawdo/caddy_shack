//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/state"
)

func TestRegisterIsSerialisedByFileLock(t *testing.T) {
	// httptest Caddy stub. Slow handler so the race is visible.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	statePath := filepath.Join(t.TempDir(), "state.json")
	mkApp := func() *app.App {
		return app.NewWithPaths(
			app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
			caddyapi.NewClient(srv.URL, time.Second),
			fakeRunningDocker{},
		)
	}

	port := freePort(t)
	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := mkApp().Register(context.Background(), app.RegisterInput{
				Name: fmt.Sprintf("svc%d", i),
				Port: port,
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Register: %v", err)
		}
	}

	s, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Backends) != n {
		t.Errorf("backends: got %d want %d (lock dropped writes)", len(s.Backends), n)
	}
}
