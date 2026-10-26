package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/certstore"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// renewWindow is how much remaining life makes a certificate due for renewal.
// Let's Encrypt issues for 90 days and recommends renewing at 30.
const renewWindow = 30 * 24 * time.Hour

// certDomainRE accepts a public FQDN of two or more dot-separated labels.
var certDomainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// CertRequestInput holds the fields the cert request command accepts.
type CertRequestInput struct {
	Domain string
	Email  string
	// Production selects the real Let's Encrypt directory. The default is
	// staging, because production allows only five duplicate certificates per
	// week for a given name set and this flow has a human in it.
	Production bool
}

// CertListing is one row in the output of CertList.
type CertListing struct {
	Domain        string   `json:"domain"`
	Env           string   `json:"env"`
	Subjects      []string `json:"subjects"`
	NotAfter      string   `json:"not_after"`
	DaysRemaining int      `json:"days_remaining"`
}

// SetIssuer substitutes the ACME issuer. Tests use it to avoid the network.
func (a *App) SetIssuer(iss acme.Issuer) { a.issuer = iss }

// validateCertDomain rejects anything the ACME path cannot or should not
// handle.
func validateCertDomain(domain string) error {
	if strings.HasPrefix(domain, "*.") {
		return fmt.Errorf("%w: pass the apex domain; the wildcard is requested automatically (got %q)", ErrInvalidArgs, domain)
	}
	if strings.HasSuffix(domain, ".test") {
		return fmt.Errorf("%w: .test domains are served by the internal CA, not Let's Encrypt (got %q)", ErrInvalidArgs, domain)
	}
	if !certDomainRE.MatchString(domain) {
		return fmt.Errorf("%w: not a valid public domain name (got %q)", ErrInvalidArgs, domain)
	}
	return nil
}

// CertRequest obtains a certificate covering the domain and its single-label
// subdomains, stores it, and pushes it into the running Caddy.
//
// Every cheap failure is checked before any ACME traffic: an issuance is rate
// limited, so spending one only to discover the container is down would cost a
// week.
func (a *App) CertRequest(ctx context.Context, in CertRequestInput, prompt acme.Prompter) error {
	// DNS names are case-insensitive; normalise before validating so
	// "Alpha.arrix.dev" is accepted the same as "alpha.arrix.dev" and
	// nothing downstream (state, the cert store path) ever sees mixed case.
	in.Domain = strings.ToLower(in.Domain)
	if err := validateCertDomain(in.Domain); err != nil {
		return err
	}
	if strings.TrimSpace(in.Email) == "" {
		return fmt.Errorf("%w: --email is required; Let's Encrypt needs an account contact", ErrInvalidArgs)
	}

	st, err := a.docker.ContainerState(a.inst.ContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if st == docker.StateAbsent {
		return fmt.Errorf("%w: run 'caddy_shack install' first", ErrNotInstalled)
	}
	if st != docker.StateRunning {
		return fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	env := acme.EnvStaging
	if in.Production {
		env = acme.EnvProduction
	}
	return a.issueAndInstall(ctx, in.Domain, in.Email, env, prompt)
}

// issueAndInstall runs the order and, only on success, writes material, updates
// state and pushes the config through commitWith. Shared by CertRequest and
// CertRenew.
//
// The material already on disk is captured before it is overwritten:
// certstore.Save replaces in place with no backup, so on renewal this is the
// only record of a certificate that was serving traffic until now. The undo
// hook restores it (or removes fresh material) on any failure, and commitWith
// runs that undo BEFORE building the rollback config after a failed state
// write, because buildConfig reads material back off disk; the other order
// would re-push the new key under the old registry.
func (a *App) issueAndInstall(ctx context.Context, domain, email, env string, prompt acme.Prompter) error {
	// Cheap gate before the order: prior material that exists but cannot be
	// read would fail the install below anyway, after an ACME issuance had
	// been spent against the rate limits. The read under the lock below
	// stays the one the undo restores from.
	if _, err := a.loadPriorMaterial(domain); err != nil {
		return err
	}
	subjects := []string{domain, "*." + domain}
	res, err := a.issuer.Issue(ctx, acme.Request{
		Domains:        subjects,
		Email:          email,
		Env:            env,
		AccountKeyPath: a.certs.AccountKeyPath(env),
	}, prompt)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCertIssuance, err)
	}
	lock, err := acquireStateLock(a.paths.LockPath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return err
	}

	prior, err := a.loadPriorMaterial(domain)
	if err != nil {
		return err
	}
	if err := a.certs.Save(domain, res.CertPEM, res.KeyPEM); err != nil {
		return err
	}

	updated := s.Clone()
	updated.Certs[domain] = state.Cert{
		Domain:   domain,
		Subjects: subjects,
		Env:      env,
		Email:    email,
		IssuedAt: time.Now().Format(time.RFC3339),
		NotAfter: res.NotAfter.Format(time.RFC3339),
	}
	return a.commitWith(ctx, s, updated, func() error { return a.restoreMaterialOnly(domain, prior) })
}

// loadPriorMaterial reads the material already stored for domain. Only
// certstore.ErrNotFound means there is none (nil, nil). Any other read error
// (a key with bad permissions, say) means material exists that cannot be
// backed up, and undoing over it would RemoveAll a certificate that was
// serving traffic, so it is returned, naming the file, and nothing is
// written.
func (a *App) loadPriorMaterial(domain string) (*certstore.Material, error) {
	prior, err := a.certs.Load(domain)
	if errors.Is(err, certstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read existing certificate material for %s (nothing was changed; fix or remove it, then retry): %w", domain, err)
	}
	return &prior, nil
}

// restoreMaterialOnly undoes the certstore write issueAndInstall made,
// restoring prior material exactly when there was any, or removing the new
// material when there was none. issueAndInstall hands it to commitWith as
// the undo hook, so it runs on every failure after the material is written.
// Its error reads as the clause commitWith appends after "additionally, ".
func (a *App) restoreMaterialOnly(domain string, prior *certstore.Material) error {
	var err error
	if prior != nil {
		err = a.certs.Save(domain, prior.CertPEM, prior.KeyPEM)
	} else {
		err = a.certs.Remove(domain)
	}
	if err != nil {
		return fmt.Errorf("restoring prior certificate material failed: %w", err)
	}
	return nil
}

// daysRemaining returns the whole days remaining until notAfter, an RFC3339
// timestamp, truncated toward zero the same way time.Duration division
// always does. That truncation means a certificate a few hours past expiry
// and one a few hours from expiry can both read as 0; callers that need to
// tell those apart should treat 0 as "expiring or just expired", not as a
// guarantee of either. ok is false when notAfter cannot be parsed, in which
// case days is 0 and must not be treated as a real reading.
func daysRemaining(notAfter string) (days int, ok bool) {
	t, err := time.Parse(time.RFC3339, notAfter)
	if err != nil {
		return 0, false
	}
	return int(time.Until(t).Hours() / 24), true
}

// CertList returns the stored certificates, sorted by domain.
func (a *App) CertList(ctx context.Context) ([]CertListing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return nil, err
	}
	domains := make([]string, 0, len(s.Certs))
	for d := range s.Certs {
		domains = append(domains, d)
	}
	sort.Strings(domains)

	out := make([]CertListing, 0, len(domains))
	for _, d := range domains {
		c := s.Certs[d]
		row := CertListing{
			Domain:   c.Domain,
			Env:      c.Env,
			Subjects: c.Subjects,
			NotAfter: c.NotAfter,
		}
		if d, ok := daysRemaining(c.NotAfter); ok {
			row.DaysRemaining = d
		}
		out = append(out, row)
	}
	return out, nil
}

// CertRemove deletes a certificate and pushes a config without it. The bool
// reports whether anything was removed; an unknown domain is not an error,
// matching Deregister.
func (a *App) CertRemove(ctx context.Context, domain string) (bool, error) {
	// Match the lower-casing CertRequest applies before storing, so a domain
	// typed in whatever case still finds the record it created. Removal
	// stays otherwise permissive: an unknown domain remains a no-op, not a
	// validation error, matching Deregister.
	domain = strings.ToLower(domain)

	lock, err := acquireStateLock(a.paths.LockPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = lock.Release() }()

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return false, err
	}
	if _, present := s.Certs[domain]; !present {
		return false, nil
	}

	// Drop the entry from the in-memory state and push a config built from
	// that state before touching anything on disk. loadedCerts derives its
	// output from the state it is given, so a state without this entry
	// produces a config without the certificate regardless of what is still
	// in the store; nothing is destroyed until that push has actually
	// succeeded, so a rejected push leaves both the material and the state
	// record intact rather than half-completing the removal.
	delete(s.Certs, domain)

	// Push only when Caddy is up. Removing while it is stopped still updates
	// state; the next start rebuilds the config from state anyway.
	if st, err := a.docker.ContainerState(a.inst.ContainerName); err == nil && st == docker.StateRunning {
		cfg, err := a.buildConfig(s)
		if err != nil {
			return false, err
		}
		if err := a.caddy.Load(ctx, cfg); err != nil && !errors.Is(err, caddyapi.ErrUnreachable) {
			return false, err
		}
	}

	if err := a.certs.Remove(domain); err != nil {
		return false, err
	}
	if err := state.Save(a.paths.StatePath, s); err != nil {
		return false, fmt.Errorf("save state: %w", err)
	}
	return true, nil
}

// CertRenew re-orders a certificate when it is within the renewal window, or
// unconditionally when force is set. The bool reports whether an order was
// placed.
func (a *App) CertRenew(ctx context.Context, domain string, force bool, prompt acme.Prompter) (bool, error) {
	// Match the lower-casing CertRequest applies before storing, so renewing
	// with the exact string the operator typed at request time (any case)
	// still finds the record.
	domain = strings.ToLower(domain)

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return false, err
	}
	c, present := s.Certs[domain]
	if !present {
		return false, fmt.Errorf("%w: no certificate for %q; run 'caddy_shack cert request %s' first", ErrInvalidArgs, domain, domain)
	}

	if !force {
		notAfter, err := time.Parse(time.RFC3339, c.NotAfter)
		if err != nil {
			return false, fmt.Errorf("%w: stored expiry for %q is unreadable: %v", ErrStateCorrupt, domain, err)
		}
		if time.Until(notAfter) > renewWindow {
			return false, nil
		}
	}

	st, err := a.docker.ContainerState(a.inst.ContainerName)
	if err != nil {
		return false, fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return false, fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	// Renew into the environment the certificate came from. Silently renewing
	// a production certificate against staging would replace a trusted
	// certificate with an untrusted one.
	env := c.Env
	if env == "" {
		env = acme.EnvStaging
	}

	// The contact was stored at issuance so renewal never has to ask again.
	// A record written before this field existed has none; the account key
	// is what identifies the account to the server, so fall back rather than
	// refusing to renew.
	email := c.Email
	if email == "" {
		email = "renewal@" + domain
	}
	if err := a.issueAndInstall(ctx, domain, email, env, prompt); err != nil {
		return false, err
	}
	return true, nil
}
