package acme

import (
	"context"
	"fmt"
)

// FakeIssuer is a test double for Issuer. It records every request and
// returns a canned result, so the app layer can be tested without a network.
type FakeIssuer struct {
	// Calls records every Issue request in order.
	Calls []Request
	// PromptCalls counts how many times the supplied prompter was invoked.
	PromptCalls int

	result Result
	err    error
}

// NewFakeIssuer returns a FakeIssuer that answers every Issue with result and
// err.
func NewFakeIssuer(result Result, err error) *FakeIssuer {
	return &FakeIssuer{result: result, err: err}
}

// Issue records the request and returns the canned response, after invoking
// the prompter once with a representative record so tests can assert the
// operator would have been asked. It mirrors LegoIssuer.Issue's upfront
// validation (an empty domain list, or a nil prompter) so an app-layer bug
// that would fail against the real issuer fails against the fake too.
func (f *FakeIssuer) Issue(ctx context.Context, req Request, prompt Prompter) (Result, error) {
	if len(req.Domains) == 0 {
		return Result{}, fmt.Errorf("acme: no domains in request")
	}
	if prompt == nil {
		return Result{}, fmt.Errorf("acme: no prompter supplied")
	}
	f.Calls = append(f.Calls, req)
	f.PromptCalls++
	if err := prompt.Confirm([]Record{{
		FQDN:  "_acme-challenge." + req.Domains[0] + ".",
		Value: "fake-challenge-value",
	}}); err != nil {
		return Result{}, err
	}
	if f.err != nil {
		return Result{}, f.err
	}
	return f.result, nil
}
