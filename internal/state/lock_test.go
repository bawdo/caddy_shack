package state

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLockAcquireAndRelease(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.json.lock")

	l, err := AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Re-acquire after release should succeed.
	l2, err := AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	_ = l2.Release()
}

func TestLockContentionTimesOut(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.json.lock")

	held, err := AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer held.Release()

	start := time.Now()
	_, err = AcquireLock(lockPath, 200*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("returned too fast: %v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("returned too slow: %v", elapsed)
	}
}

func TestLockSerialisesConcurrentAcquirers(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.json.lock")

	var wg sync.WaitGroup
	var mu sync.Mutex
	insideCount := 0
	maxInside := 0

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := AcquireLock(lockPath, 2*time.Second)
			if err != nil {
				t.Errorf("AcquireLock: %v", err)
				return
			}
			mu.Lock()
			insideCount++
			if insideCount > maxInside {
				maxInside = insideCount
			}
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			insideCount--
			mu.Unlock()
			_ = l.Release()
		}()
	}
	wg.Wait()

	if maxInside != 1 {
		t.Errorf("lock allowed concurrency: maxInside=%d", maxInside)
	}
}
