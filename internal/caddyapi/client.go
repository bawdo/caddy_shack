package caddyapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrUnreachable indicates the Caddy admin API is not responding (Caddy
// container not running, port not published, etc.).
var ErrUnreachable = errors.New("caddyapi: admin API unreachable")

// ErrRejected indicates Caddy returned a non-2xx response to a config push.
// The Body field carries Caddy's error JSON so callers can surface it.
type ErrRejected struct {
	Status int
	Body   string
}

func (e *ErrRejected) Error() string {
	return fmt.Sprintf("caddyapi: admin API rejected config: %d %s", e.Status, e.Body)
}

// Client talks to the Caddy admin API.
type Client struct {
	base string
	http *http.Client
}

// NewClient builds a Client targeting baseURL (e.g. "http://127.0.0.1:2019")
// with the given per-request timeout.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		base: baseURL,
		http: &http.Client{Timeout: timeout},
	}
}

// Load POSTs config to /load. Treats 2xx as success. The request honours
// ctx cancellation so Ctrl+C during a long config push exits promptly.
func (c *Client) Load(ctx context.Context, config []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/load", bytes.NewReader(config))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return &ErrRejected{Status: resp.StatusCode, Body: string(body)}
}

// Reachable returns true when GET /config/ returns 2xx within the client's
// timeout. Used by `status`. Honours ctx so Ctrl+C during a hanging probe
// exits promptly.
func (c *Client) Reachable(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/config/", nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
