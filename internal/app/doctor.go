package app

import (
	"context"
	"encoding/json"
	"fmt"
)

// Severity is the verdict of one check.
type Severity int

const (
	SeverityPass Severity = iota
	SeverityWarn
	SeverityFail
	SeveritySkip
)

func (s Severity) String() string {
	switch s {
	case SeverityPass:
		return "pass"
	case SeverityWarn:
		return "warn"
	case SeverityFail:
		return "fail"
	case SeveritySkip:
		return "skip"
	default:
		return "unknown"
	}
}

// MarshalJSON encodes Severity as its lowercase string form.
func (s Severity) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// FixAction is an optional remediation a check can attach to its result.
type FixAction struct {
	// Label is the human-readable description shown in the prompt and in
	// JSON output. Should describe what the fix will run, not its result.
	Label string
	// RequiresSudo signals that Apply will invoke `sudo`. Surfaced in the
	// prompt label and in JSON so callers and scripts can pre-flight.
	RequiresSudo bool
	// Apply performs the remediation. Returning a non-nil error marks the
	// fix [FIX-FAILED]; nil marks it [FIXED]. May invoke sudo itself.
	Apply func(ctx context.Context) error
}

// CheckResult is the verdict of one check plus optional fix.
type CheckResult struct {
	Name    string
	Status  Severity
	Summary string
	Detail  []string
	Fix     *FixAction
}

// MarshalJSON encodes CheckResult with the documented schema. The Apply
// closure is intentionally never serialised.
func (r CheckResult) MarshalJSON() ([]byte, error) {
	type out struct {
		Name            string   `json:"name"`
		Status          Severity `json:"status"`
		Summary         string   `json:"summary"`
		Detail          []string `json:"detail,omitempty"`
		FixAvailable    bool     `json:"fix_available"`
		FixRequiresSudo bool     `json:"fix_requires_sudo"`
	}
	o := out{
		Name:    r.Name,
		Status:  r.Status,
		Summary: r.Summary,
		Detail:  r.Detail,
	}
	if r.Fix != nil {
		o.FixAvailable = true
		o.FixRequiresSudo = r.Fix.RequiresSudo
	}
	return json.Marshal(o)
}

// DoctorOptions controls Doctor's behaviour.
type DoctorOptions struct {
	SkipBackends bool
}

// DoctorReport is the full output of one Doctor run.
type DoctorReport struct {
	SchemaVersion int                 `json:"schema_version"`
	Overall       Severity            `json:"overall"`
	Checks        []CheckResult       `json:"checks"`
	Summary       DoctorReportSummary `json:"summary"`
}

// DoctorReportSummary counts results by status.
type DoctorReportSummary struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// FixOutcome is the result of ApplyFix.
type FixOutcome int

const (
	FixApplied FixOutcome = iota
	FixSkipped
	FixFailed
)

// PromptFunc is called by ApplyFix to ask the user whether to apply a fix.
// Returns true to apply, false to skip. The cmd layer wires this to stdin
// for --fix and to `func(string) bool { return true }` for --fix --yes.
type PromptFunc func(label string) bool

// Doctor runs every check in order, honouring ctx cancellation between
// checks. Returns a populated DoctorReport. Returns ctx.Err() if cancelled
// mid-run; the report still contains every result completed so far.
func (a *App) Doctor(ctx context.Context, opts DoctorOptions) (DoctorReport, error) {
	rep := DoctorReport{SchemaVersion: 1}
	add := func(r CheckResult) {
		rep.Checks = append(rep.Checks, r)
	}
	checks := a.buildCheckList(opts)
	for _, c := range checks {
		if err := ctx.Err(); err != nil {
			finaliseDoctorReport(&rep)
			return rep, err
		}
		add(c(ctx))
	}
	finaliseDoctorReport(&rep)
	return rep, nil
}

// buildCheckList returns the ordered checks Doctor will run. Each entry is
// a closure capturing the App so the check signature stays uniform.
func (a *App) buildCheckList(opts DoctorOptions) []func(context.Context) CheckResult {
	sh := defaultShell
	tty := defaultTTYShell
	checks := []func(context.Context) CheckResult{
		func(ctx context.Context) CheckResult { return checkDockerDaemon(ctx, a) },
		func(ctx context.Context) CheckResult { return checkPortAvailability(ctx, a) },
		func(ctx context.Context) CheckResult { return checkContainerHealth(ctx, a) },
		func(ctx context.Context) CheckResult { return checkSharedNetwork(ctx, a) },
		func(ctx context.Context) CheckResult { return checkDNS(ctx, a, sh, tty) },
		func(ctx context.Context) CheckResult { return checkTrustStore(ctx, a, sh, tty) },
		func(ctx context.Context) CheckResult { return checkStateIntegrity(ctx, a) },
		func(ctx context.Context) CheckResult { return checkConfigDrift(ctx, a) },
	}
	if !opts.SkipBackends {
		checks = append(checks, func(ctx context.Context) CheckResult { return checkBackends(ctx, a) })
	} else {
		checks = append(checks, func(_ context.Context) CheckResult {
			return CheckResult{
				Name:    "backends",
				Status:  SeveritySkip,
				Summary: "skipped (--no-backends)",
			}
		})
	}
	return checks
}

func finaliseDoctorReport(rep *DoctorReport) {
	for _, c := range rep.Checks {
		switch c.Status {
		case SeverityPass:
			rep.Summary.Pass++
		case SeverityWarn:
			rep.Summary.Warn++
		case SeverityFail:
			rep.Summary.Fail++
		case SeveritySkip:
			rep.Summary.Skip++
		}
	}
	switch {
	case rep.Summary.Fail > 0:
		rep.Overall = SeverityFail
	case rep.Summary.Warn > 0:
		rep.Overall = SeverityWarn
	default:
		rep.Overall = SeverityPass
	}
}

// ApplyFix calls prompt(r.Fix.Label) and, if it returns true, invokes
// r.Fix.Apply(ctx). Returns FixSkipped when there is no fix or the user
// declined; FixFailed (and the error) when Apply errored; FixApplied
// otherwise. Safe to call on results with no Fix.
func (a *App) ApplyFix(ctx context.Context, r CheckResult, prompt PromptFunc) (FixOutcome, error) {
	if r.Fix == nil {
		return FixSkipped, nil
	}
	if !prompt(r.Fix.Label) {
		return FixSkipped, nil
	}
	if err := r.Fix.Apply(ctx); err != nil {
		return FixFailed, fmt.Errorf("apply fix %q: %w", r.Name, err)
	}
	return FixApplied, nil
}
