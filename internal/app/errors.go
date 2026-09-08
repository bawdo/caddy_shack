package app

import "errors"

// Sentinel errors. Each maps to a specific exit code in ExitCode below.
// These are part of the public CLI contract documented in caddy_shack --help.
var (
	ErrInvalidArgs       = errors.New("invalid arguments")
	ErrDomainConflict    = errors.New("domain already claimed by another backend")
	ErrDockerUnreachable = errors.New("docker daemon unreachable")
	ErrCaddyNotRunning   = errors.New("caddy container not running")
	ErrNotInstalled      = errors.New("caddy not installed")
	ErrStateCorrupt      = errors.New("state file corrupt")
	ErrLockContention    = errors.New("state lock held by another process")
	ErrUnhealthy         = errors.New("caddy_shack is unhealthy")
	// ErrCertIssuance covers any failure of an ACME order: the challenge, the
	// DNS validation, or a rate limit. Distinguished from a generic failure
	// because a script may want to back off rather than retry.
	ErrCertIssuance = errors.New("certificate issuance failed")
)

// ExitCode maps an error to the documented exit codes.
// 0 = nil, 1 = generic failure, 2-8 = specific sentinels.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrInvalidArgs):
		return 2
	case errors.Is(err, ErrDomainConflict):
		return 3
	case errors.Is(err, ErrDockerUnreachable):
		return 4
	case errors.Is(err, ErrCaddyNotRunning):
		return 5
	case errors.Is(err, ErrNotInstalled):
		return 6
	case errors.Is(err, ErrStateCorrupt):
		return 7
	case errors.Is(err, ErrLockContention):
		return 8
	case errors.Is(err, ErrCertIssuance):
		return 9
	default:
		return 1
	}
}
