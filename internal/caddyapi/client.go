package caddyapi

import (
	"bytes"
	"context"
	"encoding/json"
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

// newRequest builds a request to path on the admin API, honouring ctx, with
// the Origin header Caddy's enforce_origin requires. The Origin is always
// http://AdminOrigins[0], not derived from the base URL: Caddy compares it
// with its configured origins (the container-side 127.0.0.1:2019), not with
// the host port the request arrived on, so a non-default admin port still
// matches.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Origin", "http://"+AdminOrigins[0])
	return req, nil
}

// Load POSTs config to /load. Treats 2xx as success. The request honours
// ctx cancellation so Ctrl+C during a long config push exits promptly.
func (c *Client) Load(ctx context.Context, config []byte) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/load", bytes.NewReader(config))
	if err != nil {
		return err
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

// GetConfig retrieves the live admin config (GET /config/). 2xx returns the
// raw body. Connection errors wrap ErrUnreachable. Non-2xx returns
// *ErrRejected with the status and body.
func (c *Client) GetConfig(ctx context.Context) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/config/", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}
	return nil, &ErrRejected{Status: resp.StatusCode, Body: string(body)}
}

// CAInfo is the subset of Caddy's /pki/ca/<id> response CaddyShack needs:
// the PEM-encoded root and intermediate certificates of a CA.
type CAInfo struct {
	RootCertificate         string `json:"root_certificate"`
	IntermediateCertificate string `json:"intermediate_certificate"`
}

// LocalCA retrieves Caddy's built-in "local" CA (GET /pki/ca/local), the CA
// that signs certificates for the .test TLD. 2xx returns the parsed CAInfo.
// Connection errors wrap ErrUnreachable. Non-2xx returns *ErrRejected. Honours
// ctx so Ctrl+C during a hanging probe exits promptly.
func (c *Client) LocalCA(ctx context.Context) (CAInfo, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/pki/ca/local", nil)
	if err != nil {
		return CAInfo{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return CAInfo{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CAInfo{}, &ErrRejected{Status: resp.StatusCode, Body: string(body)}
	}
	var info CAInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return CAInfo{}, fmt.Errorf("decode CA info: %w", err)
	}
	return info, nil
}

// Reachable returns true when GET /config/ returns 2xx within the client's
// timeout. Used by `status`. Honours ctx so Ctrl+C during a hanging probe
// exits promptly.
func (c *Client) Reachable(ctx context.Context) bool {
	req, err := c.newRequest(ctx, http.MethodGet, "/config/", nil)
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
