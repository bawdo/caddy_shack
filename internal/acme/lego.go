package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// DirectoryURL returns the ACME directory for an environment. Anything other
// than EnvProduction resolves to staging: defaulting to production on an
// unrecognised value would spend one of the five weekly issuances Let's
// Encrypt allows for a given name set.
func DirectoryURL(env string) string {
	if env == EnvProduction {
		return lego.LEDirectoryProduction
	}
	return lego.LEDirectoryStaging
}

// legoUser adapts an account key and email to lego's registration.User.
type legoUser struct {
	email string
	reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *legoUser) GetEmail() string                        { return u.email }
func (u *legoUser) GetRegistration() *registration.Resource { return u.reg }
func (u *legoUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// LegoIssuer obtains certificates from a real ACME server.
type LegoIssuer struct {
	// DirectoryOverride, when set, replaces the environment-derived
	// directory URL. Integration tests point this at Pebble.
	DirectoryOverride string
	// InsecureSkipTLSVerify lets an integration test talk to a test ACME
	// server such as Pebble, which serves its directory with a self-signed
	// certificate. Never set this against a real ACME server.
	InsecureSkipTLSVerify bool
	// DNSResolvers, when set, makes propagation checks query these
	// resolvers (host:port) instead of the public recursive ones, and drops
	// the requirement that the zone's authoritative nameservers agree: a
	// test DNS server such as pebble-challtestsrv answers the TXT lookup but
	// has no real zone to answer the SOA/NS lookups authoritative-nameserver
	// discovery needs. Integration tests point this at a challenge test
	// server. Leave it empty in production, where checking the real
	// authoritative nameservers is the behaviour we want.
	DNSResolvers []string
}

// NewLegoIssuer returns an Issuer backed by Let's Encrypt.
func NewLegoIssuer() *LegoIssuer { return &LegoIssuer{} }

// Issue runs a full ACME order using manual DNS-01, prompting once through
// prompt for every record across every identifier.
func (l *LegoIssuer) Issue(ctx context.Context, req Request, prompt Prompter) (Result, error) {
	if len(req.Domains) == 0 {
		return Result{}, fmt.Errorf("acme: no domains in request")
	}
	if prompt == nil {
		return Result{}, fmt.Errorf("acme: no prompter supplied")
	}

	key, err := loadOrCreateAccountKey(req.AccountKeyPath)
	if err != nil {
		return Result{}, err
	}

	user := &legoUser{email: req.Email, key: key}
	cfg := lego.NewConfig(user)
	cfg.CADirURL = l.DirectoryOverride
	if cfg.CADirURL == "" {
		cfg.CADirURL = DirectoryURL(req.Env)
	}
	cfg.Certificate.KeyType = certcrypto.EC256

	if l.InsecureSkipTLSVerify {
		if tr, ok := cfg.HTTPClient.Transport.(*http.Transport); ok {
			if tr.TLSClientConfig == nil {
				tr.TLSClientConfig = &tls.Config{}
			}
			// #nosec G402 -- test-only escape hatch for a test ACME server
			// (Pebble) that serves its directory with a self-signed
			// certificate; InsecureSkipTLSVerify is never set for a real
			// ACME server.
			tr.TLSClientConfig.InsecureSkipVerify = true
		}
	}

	client, err := lego.NewClient(cfg)
	if err != nil {
		return Result{}, fmt.Errorf("acme: new client: %w", err)
	}

	provider := newManualProvider(prompt)
	dnsOpts := []dns01.ChallengeOption{dns01.WrapPreCheck(provider.preCheck)}
	if len(l.DNSResolvers) > 0 {
		dnsOpts = append(dnsOpts,
			dns01.AddRecursiveNameservers(l.DNSResolvers),
			dns01.DisableAuthoritativeNssPropagationRequirement(),
		)
	}
	if err := client.Challenge.SetDNS01Provider(provider, dnsOpts...); err != nil {
		return Result{}, fmt.Errorf("acme: set dns-01 provider: %w", err)
	}

	// Registration is idempotent for an existing account key: lego returns
	// the existing account rather than creating a second one.
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return Result{}, fmt.Errorf("acme: register account: %w", err)
	}
	user.reg = reg

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: req.Domains,
		Bundle:  true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("acme: obtain certificate: %w", err)
	}

	notAfter, err := leafNotAfter(res.Certificate)
	if err != nil {
		return Result{}, err
	}

	return Result{
		CertPEM:  res.Certificate,
		KeyPEM:   res.PrivateKey,
		NotAfter: notAfter,
	}, nil
}

// loadOrCreateAccountKey reads the ACME account key, generating one on first
// use. The key is reused across orders: regenerating it would orphan the
// account and lose its registration and rate-limit history.
func loadOrCreateAccountKey(path string) (crypto.PrivateKey, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from Paths, rooted at os.UserConfigDir
	switch {
	case err == nil:
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("acme: account key at %s is not valid PEM", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("acme: parse account key at %s: %w", path, err)
		}
		return key, nil
	case os.IsNotExist(err):
	default:
		return nil, fmt.Errorf("acme: read account key: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: generate account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("acme: marshal account key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("acme: mkdir account dir: %w", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("acme: write account key: %w", err)
	}
	return key, nil
}

// leafNotAfter reads the expiry from the first certificate in a PEM chain.
func leafNotAfter(chainPEM []byte) (time.Time, error) {
	block, _ := pem.Decode(chainPEM)
	if block == nil {
		return time.Time{}, fmt.Errorf("acme: certificate chain is not valid PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("acme: parse leaf certificate: %w", err)
	}
	return leaf.NotAfter, nil
}
