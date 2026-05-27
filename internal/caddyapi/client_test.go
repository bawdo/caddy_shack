package caddyapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientLoadPostsConfig(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	if err := c.Load(context.Background(), []byte(`{"apps":{}}`)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotPath != "/load" {
		t.Errorf("path: got %q want /load", gotPath)
	}
	if gotMethod != "POST" {
		t.Errorf("method: got %q want POST", gotMethod)
	}
	if !strings.Contains(string(gotBody), `"apps":{}`) {
		t.Errorf("body not forwarded: %s", gotBody)
	}
}

func TestClientLoadReturnsErrRejectedOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad config"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	err := c.Load(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rej *ErrRejected
	if !errors.As(err, &rej) {
		t.Errorf("not ErrRejected: %v", err)
	}
	if rej.Status != 400 {
		t.Errorf("Status: got %d want 400", rej.Status)
	}
	if !strings.Contains(rej.Body, "bad config") {
		t.Errorf("Body: %q", rej.Body)
	}
}

func TestClientLoadReturnsErrUnreachableOnConnFail(t *testing.T) {
	// Pick a port that is almost certainly unused.
	c := NewClient("http://127.0.0.1:1", 200*time.Millisecond)
	err := c.Load(context.Background(), []byte(`{}`))
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable, got %v", err)
	}
}

func TestClientLoadHonoursContextCancellation(t *testing.T) {
	// Raw TCP listener that accepts then sits on the connection without
	// responding. We avoid httptest here because httptest.Server.Close
	// waits for in-flight handlers to return, deadlocking against a
	// handler that's blocked. Closing the listener directly cleans up.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			// Hold the connection; the client's cancel will tear it down.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	c := NewClient("http://"+l.Addr().String(), time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err = c.Load(ctx, []byte(`{}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Load did not respect cancel: took %s", elapsed)
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable wrap, got %v", err)
	}
}

func TestClientReachableTrueOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/" {
			t.Errorf("path: %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	if !c.Reachable(context.Background()) {
		t.Error("expected Reachable=true")
	}
}

func TestClientReachableFalseOnNoServer(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", 200*time.Millisecond)
	if c.Reachable(context.Background()) {
		t.Error("expected Reachable=false")
	}
}

func TestClientGetConfigReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/" {
			t.Errorf("path: got %q want /config/", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method: got %q want GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"apps":{"http":{}}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	body, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if string(body) != `{"apps":{"http":{}}}` {
		t.Errorf("body: %s", body)
	}
}

func TestClientGetConfigErrUnreachable(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", 200*time.Millisecond)
	_, err := c.GetConfig(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable, got %v", err)
	}
}

func TestClientGetConfigErrRejectedOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"oops"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	_, err := c.GetConfig(context.Background())
	var rej *ErrRejected
	if !errors.As(err, &rej) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if rej.Status != 500 {
		t.Errorf("status: %d", rej.Status)
	}
}
