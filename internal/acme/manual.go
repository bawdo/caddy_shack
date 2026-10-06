package acme

import (
	"strings"
	"sync"

	"github.com/go-acme/lego/v4/challenge/dns01"
)

// manualProvider implements lego's challenge.Provider for an operator who
// edits DNS records by hand.
//
// It deliberately does NOT implement Sequential(). lego's prober splits
// solvers into parallelSolve (PreSolve every authorisation, then Solve every
// authorisation) and sequentialSolve, and a provider only lands in the
// sequential bucket if it implements Sequential(). lego's own manual provider
// does, which makes a wildcard order prompt twice, once per identifier, with a
// validation in between. Staying out of that bucket means both Present calls
// happen before any validation, so every record can be shown in one prompt and
// the operator makes a single trip to their DNS provider.
type manualProvider struct {
	prompt Prompter

	// mu guards records. lego's parallelSolve (see the comment above) calls
	// Present in series for this pinned lego version, so mu is not needed to
	// survive lego's current behaviour; it is cheap defence against a future
	// lego change, or an app-level caller, that calls Present from more than
	// one goroutine.
	mu      sync.Mutex
	records []Record

	once      sync.Once
	promptErr error
}

// newManualProvider returns a manualProvider that shows the operator its
// collected records through p when the first pre-check fires.
func newManualProvider(p Prompter) *manualProvider {
	return &manualProvider{prompt: p}
}

// Present records what needs publishing. It does not prompt: prompting here
// would ask the operator once per identifier. The prompt happens in preCheck,
// by which time every Present has run.
func (m *manualProvider) Present(domain, token, keyAuth string) error {
	// lego's Challenge.PreSolve calls Present with authz.Identifier.Value,
	// which for a wildcard authorisation is the bare domain, never a
	// "*."-prefixed form: that prefixed form exists only in lego's logging
	// path (challenge.GetTargetedDomain), not in what reaches Present. This
	// strip guards against a domain arriving with the wildcard prefix
	// anyway, so a change in that behaviour (or a non-lego caller) cannot
	// produce the malformed record name "_acme-challenge.*.<domain>.".
	domain = strings.TrimPrefix(domain, "*.")
	info := dns01.GetChallengeInfo(domain, keyAuth)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, Record{FQDN: info.EffectiveFQDN, Value: info.Value})
	return nil
}

// CleanUp is a no-op. The operator removes the records themselves, and the
// command tells them so once the order is complete. Failing here would fail an
// otherwise successful issuance.
func (m *manualProvider) CleanUp(domain, token, keyAuth string) error {
	return nil
}

// Records returns the collected records in the order they were presented.
func (m *manualProvider) Records() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Record{}, m.records...)
}

// preCheck is installed via dns01.WrapPreCheck. It prompts the operator once,
// on the first propagation check, then delegates to lego's real check. Because
// the real check still runs, confirming before the records have actually
// propagated is harmless: lego keeps polling the authoritative nameservers
// until the values resolve.
func (m *manualProvider) preCheck(domain, fqdn, value string, check dns01.PreCheckFunc) (bool, error) {
	m.once.Do(func() {
		m.promptErr = m.prompt.Confirm(m.Records())
	})
	if m.promptErr != nil {
		return false, m.promptErr
	}
	return check(fqdn, value)
}
