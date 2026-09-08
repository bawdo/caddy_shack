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

	st, err := a.docker.ContainerState(CaddyContainerName)
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
// state and pushes the config. Shared by CertRequest and CertRenew.
func (a *App) issueAndInstall(ctx context.Context, domain, email, env string, prompt acme.Prompter) error {
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

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return err
	}
	defer func() { _ = lock.Release() }()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return err
	}

	// Capture what was already there, if anything, before overwriting it.
	// certstore.Save replaces material in place with no backup of its own,
	// so on renewal this is the only record of the certificate that was
	// still working right up until this call. If the push below fails, a
	// bare "roll back to nothing" would delete a working certificate and
	// turn a failed renewal into an outage.
	prior, priorErr := a.certs.Load(domain)
	hadPrior := priorErr == nil

	if err := a.certs.Save(domain, res.CertPEM, res.KeyPEM); err != nil {
		return err
	}

	// Capture the state's own pre-change picture of this domain (distinct
	// from prior/hadPrior above, which track the certstore material) so a
	// state.Save failure below can rebuild the exact config that was live
	// before this call and re-push it.
	priorCertEntry, hadPriorCertEntry := s.Certs[domain]

	s.Certs[domain] = state.Cert{
		Domain:   domain,
		Subjects: subjects,
		Env:      env,
		Email:    email,
		IssuedAt: time.Now().Format(time.RFC3339),
		NotAfter: res.NotAfter.Format(time.RFC3339),
	}

	certs, err := a.loadedCerts(s)
	if err != nil {
		return a.restoreMaterial(domain, hadPrior, prior, err)
	}
	cfg := caddyapi.Build(s, certs)
	if err := a.caddy.Load(ctx, cfg); err != nil {
		pushErr := err
		if errors.Is(err, caddyapi.ErrUnreachable) {
			pushErr = fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return a.restoreMaterial(domain, hadPrior, prior, pushErr)
	}

	if err := state.Save(a.paths.StatePath, s); err != nil {
		// The push already landed but the state write failed. Left alone,
		// Caddy would keep serving material that state.json has no record
		// of: the next register/deregister/doctor --fix rebuilds from
		// state, drops the load_pem, and hands the domain back to the
		// internal issuer. Roll the push back using the pre-change state so
		// Caddy and the store both return to what they were before this
		// call, then surface both the save failure and whether that
		// rollback itself succeeded.
		saveErr := fmt.Errorf("save state: %w", err)

		preChangeState := &state.State{
			SchemaVersion: s.SchemaVersion,
			Backends:      s.Backends,
			Certs:         make(map[string]state.Cert, len(s.Certs)),
		}
		for k, v := range s.Certs {
			preChangeState.Certs[k] = v
		}
		if hadPriorCertEntry {
			preChangeState.Certs[domain] = priorCertEntry
		} else {
			delete(preChangeState.Certs, domain)
		}

		// Restore the store material BEFORE rebuilding the rollback config:
		// loadedCerts reads material back off disk, and the disk still holds
		// the just-issued bytes at this point. Building the rollback config
		// first would silently re-push the new material under the old
		// routing, defeating the rollback.
		materialErr := a.restoreMaterialOnly(domain, hadPrior, prior)

		var rollbackErr error
		rollbackCerts, certsErr := a.loadedCerts(preChangeState)
		if certsErr != nil {
			rollbackErr = fmt.Errorf("rebuild pre-change certificates: %w", certsErr)
		} else {
			rollbackCfg := caddyapi.Build(preChangeState, rollbackCerts)
			if loadErr := a.caddy.Load(ctx, rollbackCfg); loadErr != nil {
				rollbackErr = fmt.Errorf("re-push pre-change config: %w", loadErr)
			}
		}

		switch {
		case rollbackErr != nil && materialErr != nil:
			return fmt.Errorf("%w (additionally, config rollback failed: %v; material rollback failed: %v)", saveErr, rollbackErr, materialErr)
		case rollbackErr != nil:
			return fmt.Errorf("%w (additionally, config rollback failed: %v)", saveErr, rollbackErr)
		case materialErr != nil:
			return fmt.Errorf("%w (additionally, material rollback failed: %v)", saveErr, materialErr)
		default:
			return fmt.Errorf("%w (config and material rolled back to their pre-request state)", saveErr)
		}
	}
	return nil
}

// restoreMaterial undoes the certstore write that issueAndInstall made,
// after a push failure. When there was material for the domain before the
// order started, it is restored exactly, so a failed renewal is a no-op
// rather than an outage; when there was none (a fresh request), the new
// material is removed, same as before. pushErr is the failure that triggered
// the rollback; if the rollback itself fails, both are surfaced together so
// the operator knows the store may be in a bad state.
func (a *App) restoreMaterial(domain string, hadPrior bool, prior certstore.Material, pushErr error) error {
	if restoreErr := a.restoreMaterialOnly(domain, hadPrior, prior); restoreErr != nil {
		return fmt.Errorf("%w (additionally, restoring prior certificate material failed: %v)", pushErr, restoreErr)
	}
	return pushErr
}

// restoreMaterialOnly undoes the certstore write issueAndInstall made,
// restoring prior material exactly when there was any, or removing the new
// material when there was none. It is the restore path shared by
// restoreMaterial (push failure) and the state.Save failure branch of
// issueAndInstall, which additionally rolls the pushed config back.
func (a *App) restoreMaterialOnly(domain string, hadPrior bool, prior certstore.Material) error {
	if hadPrior {
		return a.certs.Save(domain, prior.CertPEM, prior.KeyPEM)
	}
	return a.certs.Remove(domain)
}

// CertList returns the stored certificates, sorted by domain.
func (a *App) CertList(ctx context.Context) ([]CertListing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return nil, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
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
		if t, err := time.Parse(time.RFC3339, c.NotAfter); err == nil {
			row.DaysRemaining = int(time.Until(t).Hours() / 24)
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

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return false, fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return false, err
	}
	defer func() { _ = lock.Release() }()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return false, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
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
	if st, err := a.docker.ContainerState(CaddyContainerName); err == nil && st == docker.StateRunning {
		certs, err := a.loadedCerts(s)
		if err != nil {
			return false, err
		}
		cfg := caddyapi.Build(s, certs)
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

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return false, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
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

	st, err := a.docker.ContainerState(CaddyContainerName)
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
