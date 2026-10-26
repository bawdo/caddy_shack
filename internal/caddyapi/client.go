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
// the Origin header Caddy's enforce_origin requires when withOrigin is set.
// The Origin is always http://AdminOrigins[0], not derived from the base
// URL: Caddy compares it with its configured origins (the container-side
// 127.0.0.1:2019), not with the host port the request arrived on, so a
// non-default admin port still matches. body may be nil; otherwise each call
// reads it through a fresh reader, so a retry resends it whole.
func (c *Client) newRequest(ctx context.Context, method, path string, body []byte, withOrigin bool) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withOrigin {
		req.Header.Set("Origin", "http://"+AdminOrigins[0])
	}
	return req, nil
}

// originRefusal is the text in the body of the 403 Caddy's admin API returns
// to a request whose Origin header it does not allow.
const originRefusal = "not allowed to access from origin"

// do sends method path (with body, which may be nil) to the admin API and
// returns the status and the whole response body. A transport failure wraps
// ErrUnreachable; a non-2xx status is returned for the caller to judge.
//
// The request carries the Origin header enforce_origin requires. If Caddy
// answers 403 because it does not allow that origin, do rebuilds the same
// request without the header and sends it once more. This is a
// compatibility path for upgrades: an install made before caddy_shack
// turned on enforce_origin resumes an autosave whose admin block has no
// origins list, and such a Caddy refuses any request that carries Origin
// while serving one that does not. The next config push installs the
// origins admin block, after which the first attempt succeeds and the retry
// stops firing. It can be removed once no install predates the origins
// block. A 403 for any other reason is returned as is, with no retry.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	status, resp, err := c.send(ctx, method, path, body, true)
	if err == nil && status == http.StatusForbidden && bytes.Contains(resp, []byte(originRefusal)) {
		return c.send(ctx, method, path, body, false)
	}
	return status, resp, err
}

// send makes one attempt for do, with or without the Origin header.
func (c *Client) send(ctx context.Context, method, path string, body []byte, withOrigin bool) (int, []byte, error) {
	req, err := c.newRequest(ctx, method, path, body, withOrigin)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// ok reports whether status is 2xx.
func ok(status int) bool { return status >= 200 && status < 300 }

// Load POSTs config to /load. Treats 2xx as success. The request honours
// ctx cancellation so Ctrl+C during a long config push exits promptly.
func (c *Client) Load(ctx context.Context, config []byte) error {
	if config == nil {
		config = []byte{}
	}
	status, body, err := c.do(ctx, http.MethodPost, "/load", config)
	if err != nil {
		return err
	}
	if ok(status) {
		return nil
	}
	return &ErrRejected{Status: status, Body: string(body)}
}

// GetConfig retrieves the live admin config (GET /config/). 2xx returns the
// raw body. Connection errors wrap ErrUnreachable. Non-2xx returns
// *ErrRejected with the status and body.
func (c *Client) GetConfig(ctx context.Context) ([]byte, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/config/", nil)
	if err != nil {
		return nil, err
	}
	if ok(status) {
		return body, nil
	}
	return nil, &ErrRejected{Status: status, Body: string(body)}
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
	status, body, err := c.do(ctx, http.MethodGet, "/pki/ca/local", nil)
	if err != nil {
		return CAInfo{}, err
	}
	if !ok(status) {
		return CAInfo{}, &ErrRejected{Status: status, Body: string(body)}
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
	status, _, err := c.do(ctx, http.MethodGet, "/config/", nil)
	return err == nil && ok(status)
}
