package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
)

// ExportCA returns the PEM-encoded local root CA that Caddy uses to sign
// certificates for the .test TLD, so external tools and containers can seed a
// trust store without reaching into Caddy's internal PKI paths. When
// includeIntermediate is true and an intermediate exists, it is appended after
// the root (root first). The returned string always ends in a newline.
//
// Returns, matching the documented exit codes:
//   - ErrDockerUnreachable (4) when the docker daemon is down
//   - ErrNotInstalled     (6) when no caddy container exists
//   - ErrCaddyNotRunning  (5) when the container is stopped or restarting, or
//     the admin API is unreachable
//   - a generic error     (1) when Caddy has not provisioned the CA yet
func (a *App) ExportCA(ctx context.Context, includeIntermediate bool) (string, error) {
	if err := a.docker.DaemonReachable(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}

	st, err := a.docker.ContainerState(a.inst.ContainerName)
	if err != nil {
		return "", fmt.Errorf("inspect container: %w", err)
	}
	switch st {
	case docker.StateAbsent:
		return "", fmt.Errorf("%w: run 'caddy_shack install' first", ErrNotInstalled)
	case docker.StateStopped, docker.StateRestarting:
		return "", fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	info, err := a.caddy.LocalCA(ctx)
	if err != nil {
		// A running container whose admin API won't answer is, for the
		// consumer's purposes, not serving: report it as not running.
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return "", fmt.Errorf("%w: admin API unreachable: %v", ErrCaddyNotRunning, err)
		}
		return "", fmt.Errorf("read local CA: %w", err)
	}

	root := strings.TrimSpace(info.RootCertificate)
	if root == "" {
		return "", fmt.Errorf("%w; register a backend to trigger certificate issuance", errLocalCANotProvisioned)
	}

	out := root + "\n"
	if includeIntermediate {
		if inter := strings.TrimSpace(info.IntermediateCertificate); inter != "" {
			out += inter + "\n"
		}
	}
	return out, nil
}

// errLocalCANotProvisioned marks the ExportCA failure where the container and
// admin API are up but Caddy has not issued its local root yet. It stays a
// generic (exit 1) error for the CLI; doctor uses it to tell "not yet" apart
// from "broken".
var errLocalCANotProvisioned = errors.New("local CA not provisioned yet")
