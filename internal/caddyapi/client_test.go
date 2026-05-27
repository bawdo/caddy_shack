package caddyapi

import (
	"errors"
	"io"
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
	if err := c.Load([]byte(`{"apps":{}}`)); err != nil {
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
	err := c.Load([]byte(`{}`))
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
	err := c.Load([]byte(`{}`))
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable, got %v", err)
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
	if !c.Reachable() {
		t.Error("expected Reachable=true")
	}
}

func TestClientReachableFalseOnNoServer(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", 200*time.Millisecond)
	if c.Reachable() {
		t.Error("expected Reachable=false")
	}
}
