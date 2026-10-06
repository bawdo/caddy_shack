package app

import (
	"errors"
	"fmt"
	"sort"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/certstore"
	"github.com/bawdo/caddy_shack/internal/state"
)

// loadedCerts turns the certificate metadata in state into the material
// caddyapi.BuildWith needs, reading the PEM from the on-host store.
//
// buildConfig calls this, and every config push must go through buildConfig.
// A config pushed without it would drop the loaded certificates out of the
// running Caddy, and the next request for a covered domain would fall back to
// the internal CA or fail outright.
//
// A state entry whose material has gone missing from disk (certstore.ErrNotFound)
// is skipped rather than treated as an error: a hand-deleted file should
// degrade to "no certificate for that domain", not wedge every subsequent
// register. Any other error reading the store (a permissions fault, an I/O
// error) is a real misconfiguration and is returned to the caller rather
// than silently dropping the certificate from the pushed config.
func (a *App) loadedCerts(s *state.State) ([]caddyapi.LoadedCert, error) {
	if s == nil || len(s.Certs) == 0 {
		return nil, nil
	}
	domains := make([]string, 0, len(s.Certs))
	for d := range s.Certs {
		domains = append(domains, d)
	}
	// Sorted so the generated config is byte-stable across calls; the doctor
	// drift check compares generated output.
	sort.Strings(domains)

	out := make([]caddyapi.LoadedCert, 0, len(domains))
	for _, d := range domains {
		m, err := a.certs.Load(d)
		if errors.Is(err, certstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load certificate material for %s: %w", d, err)
		}
		c := s.Certs[d]
		out = append(out, caddyapi.LoadedCert{
			Subjects: c.Subjects,
			CertPEM:  string(m.CertPEM),
			KeyPEM:   string(m.KeyPEM),
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
