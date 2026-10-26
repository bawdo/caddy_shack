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

func TestClientLocalCAReturnsRootAndIntermediate(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"local","root_certificate":"-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----\n","intermediate_certificate":"-----BEGIN CERTIFICATE-----\nINT\n-----END CERTIFICATE-----\n"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	info, err := c.LocalCA(context.Background())
	if err != nil {
		t.Fatalf("LocalCA: %v", err)
	}
	if gotPath != "/pki/ca/local" {
		t.Errorf("path: got %q want /pki/ca/local", gotPath)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method: got %q want GET", gotMethod)
	}
	if !strings.Contains(info.RootCertificate, "ROOT") {
		t.Errorf("RootCertificate: %q", info.RootCertificate)
	}
	if !strings.Contains(info.IntermediateCertificate, "INT") {
		t.Errorf("IntermediateCertificate: %q", info.IntermediateCertificate)
	}
}

func TestClientLocalCAErrUnreachable(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", 200*time.Millisecond)
	_, err := c.LocalCA(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable, got %v", err)
	}
}

func TestClientLocalCAErrRejectedOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"unknown CA"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	_, err := c.LocalCA(context.Background())
	var rej *ErrRejected
	if !errors.As(err, &rej) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if rej.Status != 404 {
		t.Errorf("status: %d", rej.Status)
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

// roundTripFunc is an http.RoundTripper made from a function, so a test can
// see every request the client sends without binding a port.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestClientSendsAdminOrigin: Caddy's admin API runs with enforce_origin,
// so every request the client makes must carry an Origin in AdminOrigins.
// The header is the same whatever host port the admin API is published on
// (here a non-default 47123): Caddy compares it with its configured origins,
// which name the container-side 127.0.0.1:2019, not with the port the
// request arrived on.
func TestClientSendsAdminOrigin(t *testing.T) {
	var got []string
	c := NewClient("http://127.0.0.1:47123", time.Second)
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.Method+" "+r.URL.Host+r.URL.Path+" "+r.Header.Get("Origin"))
		body := ""
		if r.URL.Path == "/pki/ca/local" {
			body = `{}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})

	ctx := context.Background()
	if err := c.Load(ctx, []byte(`{}`)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := c.GetConfig(ctx); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if _, err := c.LocalCA(ctx); err != nil {
		t.Fatalf("LocalCA: %v", err)
	}
	if !c.Reachable(ctx) {
		t.Fatal("Reachable: false")
	}

	const o = "http://127.0.0.1:2019"
	const h = "127.0.0.1:47123"
	want := []string{"POST " + h + "/load " + o, "GET " + h + "/config/ " + o, "GET " + h + "/pki/ca/local " + o, "GET " + h + "/config/ " + o}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests (method host+path Origin):\n got  %q\n want %q", got, want)
	}
}
