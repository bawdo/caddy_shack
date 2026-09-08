// Package acme obtains certificates from an ACME server. It exists so the
// rest of caddy_shack can order a certificate without knowing about lego, and
// so the app layer can be tested against a fake issuer.
//
// Caddy's own ACME automation is all or nothing: it has no mode that pauses
// and asks a human for a DNS record. Since alpha.arrix.dev and domains like it
// resolve to 127.0.0.1, no inbound challenge can reach them and DNS-01 is the
// only option, so caddy_shack drives the order itself and hands Caddy the
// finished certificate.
package acme

import (
	"context"
	"time"
)

// Environment names an ACME directory.
const (
	EnvStaging    = "staging"
	EnvProduction = "production"
)

// Record is one DNS TXT record the operator must publish to satisfy a
// DNS-01 challenge.
type Record struct {
	// FQDN is the fully qualified record name, e.g.
	// "_acme-challenge.alpha.arrix.dev." with its trailing dot.
	FQDN string
	// Value is the TXT record's value.
	Value string
}

// Prompter shows the operator the records to publish and blocks until they
// confirm. Implementations live in the cmd layer so they can write to the
// command's own streams.
type Prompter interface {
	Confirm(records []Record) error
}

// Request is one certificate order.
type Request struct {
	// Domains are the identifiers to include, apex first.
	Domains []string
	// Email is the ACME account contact.
	Email string
	// Env is EnvStaging or EnvProduction.
	Env string
	// AccountKeyPath is where the account key is read from, or written to on
	// first use for this environment.
	AccountKeyPath string
}

// Result is an issued certificate.
type Result struct {
	// CertPEM is the full chain, PEM encoded.
	CertPEM []byte
	// KeyPEM is the private key, PEM encoded.
	KeyPEM []byte
	// NotAfter is the leaf certificate's expiry.
	NotAfter time.Time
}

// Issuer obtains certificates. The real implementation talks to Let's
// Encrypt; tests use the fake.
type Issuer interface {
	Issue(ctx context.Context, req Request, prompt Prompter) (Result, error)
}
