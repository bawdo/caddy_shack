package app

// ensureSharedNetwork makes the shared Docker network exist and attaches the
// caddy container to it, so container-mode (--container) backends can be
// reached by their network alias. Idempotent; safe to call repeatedly. The
// caller is responsible for ensuring the caddy container is running first.
func (a *App) ensureSharedNetwork() error {
	if err := a.docker.EnsureNetwork(a.inst.NetworkName); err != nil {
		return err
	}
	return a.docker.ConnectNetwork(a.inst.NetworkName, a.inst.ContainerName)
}
