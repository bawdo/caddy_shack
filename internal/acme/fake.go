package acme

import "context"

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

// Issue records the request and returns the canned response. When a prompter
// is supplied it is invoked once with a representative record, so tests can
// assert the operator would have been asked.
func (f *FakeIssuer) Issue(ctx context.Context, req Request, prompt Prompter) (Result, error) {
	f.Calls = append(f.Calls, req)
	if prompt != nil {
		f.PromptCalls++
		if err := prompt.Confirm([]Record{{
			FQDN:  "_acme-challenge." + req.Domains[0] + ".",
			Value: "fake-challenge-value",
		}}); err != nil {
			return Result{}, err
		}
	}
	if f.err != nil {
		return Result{}, f.err
	}
	return f.result, nil
}
