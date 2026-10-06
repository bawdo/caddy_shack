package acme

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain disables lego's CNAME-following before any test in this package
// runs. dns01.GetChallengeInfo computes EffectiveFQDN by issuing real
// dns.TypeCNAME queries unless LEGO_DISABLE_CNAME_SUPPORT is set, and
// Present calls GetChallengeInfo. Without this, "go test ./internal/acme/"
// would reach the network on every test that calls Present: flaky in CI,
// broken offline, and slow. CNAME following is a legitimate production
// feature (it is what lets an operator delegate _acme-challenge to a zone
// with a working API), so it stays enabled in manual.go; it is only
// disabled here, for the test binary.
func TestMain(m *testing.M) {
	if err := os.Setenv("LEGO_DISABLE_CNAME_SUPPORT", "true"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// recordingPrompter captures what the provider asked the operator to do.
type recordingPrompter struct {
	calls   int
	records []Record
	err     error
}

func (p *recordingPrompter) Confirm(records []Record) error {
	p.calls++
	p.records = append([]Record{}, records...)
	return p.err
}

// TestProviderCollectsOneRecordPerIdentifier models what lego actually does
// for a wildcard order. lego's Challenge.PreSolve calls
// c.provider.Present(authz.Identifier.Value, ...), and for a wildcard
// authorisation Identifier.Value is the BARE domain, not a "*."-prefixed
// form (that prefixed form comes from challenge.GetTargetedDomain, which
// lego uses only for log messages and error-map keys, never as the Present
// argument). So a real wildcard order calls Present with the same bare
// domain twice, once per identifier, each with its own token and key
// authorisation, and that is exactly why both TXT records must land on one
// _acme-challenge name.
func TestProviderCollectsOneRecordPerIdentifier(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)

	if err := prov.Present("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Fatalf("present first identifier: %v", err)
	}
	if err := prov.Present("alpha.arrix.dev", "tok2", "keyauth-two"); err != nil {
		t.Fatalf("present second identifier: %v", err)
	}

	recs := prov.Records()
	if len(recs) != 2 {
		t.Fatalf("expected two records, got %d: %+v", len(recs), recs)
	}
	// A wildcard order puts both challenges on the same name. If these ever
	// differ, the operator would be told to create the wrong record.
	if recs[0].FQDN != recs[1].FQDN {
		t.Errorf("both identifiers must share one _acme-challenge name, got %q and %q", recs[0].FQDN, recs[1].FQDN)
	}
	if recs[0].FQDN != "_acme-challenge.alpha.arrix.dev." {
		t.Errorf("FQDN = %q, want the _acme-challenge name for the apex", recs[0].FQDN)
	}
	if recs[0].Value == recs[1].Value {
		t.Error("the two identifiers must yield different TXT values")
	}
	if recs[0].Value == "" {
		t.Error("TXT value must not be empty")
	}
}

// TestProviderStripsWildcardPrefixIfEverPassed pins the strings.TrimPrefix
// guard in Present. lego does not currently take this path: as established
// in TestProviderCollectsOneRecordPerIdentifier's comment, lego always
// passes the bare identifier value, never a "*."-prefixed domain. This test
// covers a path lego does not currently take, in case a future lego version
// (or a caller other than lego) ever passes a wildcard-prefixed domain
// directly; without the guard that would silently produce the malformed
// record name "_acme-challenge.*.alpha.arrix.dev.".
func TestProviderStripsWildcardPrefixIfEverPassed(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)

	if err := prov.Present("*.alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Fatalf("present: %v", err)
	}

	recs := prov.Records()
	if len(recs) != 1 {
		t.Fatalf("expected one record, got %d: %+v", len(recs), recs)
	}
	if !strings.HasPrefix(recs[0].FQDN, "_acme-challenge.alpha.arrix.dev") {
		t.Errorf("FQDN = %q, want the wildcard prefix stripped before building the name", recs[0].FQDN)
	}
}

func TestProviderDoesNotPromptDuringPresent(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)

	if err := prov.Present("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Fatalf("present: %v", err)
	}
	// Prompting inside Present would ask the operator once per identifier.
	// The prompt belongs in the pre-check, after every Present has run.
	if p.calls != 0 {
		t.Errorf("Present must not prompt, prompter called %d times", p.calls)
	}
}

func TestPreCheckPromptsExactlyOnceWithEveryRecord(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)

	if err := prov.Present("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Fatalf("present first identifier: %v", err)
	}
	if err := prov.Present("alpha.arrix.dev", "tok2", "keyauth-two"); err != nil {
		t.Fatalf("present second identifier: %v", err)
	}

	var checked int
	check := func(fqdn, value string) (bool, error) {
		checked++
		return true, nil
	}

	for i := 0; i < 3; i++ {
		ok, err := prov.preCheck("alpha.arrix.dev", "_acme-challenge.alpha.arrix.dev.", "v", check)
		if err != nil {
			t.Fatalf("precheck: %v", err)
		}
		if !ok {
			t.Fatal("precheck must delegate its verdict to the wrapped check")
		}
	}

	if p.calls != 1 {
		t.Errorf("operator must be prompted exactly once, got %d prompts", p.calls)
	}
	if len(p.records) != 2 {
		t.Errorf("the single prompt must show both records, showed %d", len(p.records))
	}
	if checked != 3 {
		t.Errorf("every precheck must still reach the real propagation check, got %d", checked)
	}
}

func TestPreCheckPropagatesPrompterError(t *testing.T) {
	sentinel := errors.New("operator cancelled")
	p := &recordingPrompter{err: sentinel}
	prov := newManualProvider(p)

	if err := prov.Present("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Fatalf("present: %v", err)
	}

	check := func(fqdn, value string) (bool, error) { return true, nil }
	_, err := prov.preCheck("alpha.arrix.dev", "_acme-challenge.alpha.arrix.dev.", "v", check)
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the prompter's error", err)
	}
}

func TestCleanUpIsQuietAndSucceeds(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)
	if err := prov.CleanUp("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Errorf("CleanUp must not fail: %v", err)
	}
	if p.calls != 0 {
		t.Error("CleanUp must not prompt")
	}
}

// TestProviderDoesNotImplementSequential guards the core design constraint:
// lego's prober (challenge/resolver/prober.go) only routes a provider into
// sequentialSolve if it implements Sequential(), and that would make a
// wildcard order prompt once per identifier instead of once overall. lego's
// real interface on dns01.DNSProviderManual is "Sequential() time.Duration",
// but an accidental addition might not match that exact signature, so this
// checks both the real shape and the shape used by some other lego
// challenge providers.
func TestProviderDoesNotImplementSequential(t *testing.T) {
	var p any = newManualProvider(nil)
	if _, bad := p.(interface{ Sequential() (bool, time.Duration) }); bad {
		t.Error("manualProvider must not implement Sequential() (bool, time.Duration)")
	}
	if _, bad := p.(interface{ Sequential() time.Duration }); bad {
		t.Error("manualProvider must not implement Sequential() time.Duration")
	}
}

// TestPresentIsSafeUnderConcurrentCalls proves the record slice tolerates
// concurrent Present calls, with no data race and no lost update. Note that
// lego's own parallelSolve does not currently call Present concurrently: it
// is two plain sequential for loops (PreSolve every authorisation, then
// Solve every authorisation), not goroutines; "parallel" in lego's naming
// means presolve-all-then-solve-all, as opposed to "sequential" meaning
// presolve-one-solve-one. The mutex and this test are defence against a
// future lego change or an app-level caller that does call Present from
// multiple goroutines, not a description of this pinned lego version's
// actual behaviour.
func TestPresentIsSafeUnderConcurrentCalls(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			keyAuth := "keyauth-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
			if err := prov.Present("alpha.arrix.dev", "tok", keyAuth); err != nil {
				t.Errorf("present: %v", err)
			}
		}()
	}
	wg.Wait()

	recs := prov.Records()
	if len(recs) != n {
		t.Fatalf("expected %d records, got %d", n, len(recs))
	}

	seen := make(map[string]int)
	for _, r := range recs {
		seen[r.Value]++
	}
	for value, count := range seen {
		if count != 1 {
			t.Errorf("value %q recorded %d times, want exactly once", value, count)
		}
	}
}
