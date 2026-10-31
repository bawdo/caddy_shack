# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.1] - 2026-11-01

### Security

- Updated go-jose/go-jose/v4 from 4.1.4 to 4.1.6 (GHSA-mgcc-cww5-fp2q,
  GHSA-vvm8-gf78-28g4, GHSA-4w3g-jfj2-cw2q). caddy_shack only reaches it
  through lego to sign ACME requests, so none of the three are reachable,
  but release binaries no longer carry the vulnerable version.

## [0.2.0] - 2026-10-31

### Added

- Releases attach their build provenance as a Sigstore bundle,
  caddy_shack_<version>_provenance.intoto.jsonl, so an archive can be verified
  without GitHub's attestation API.

- Releases are immutable once published, and only admins can create `v*` tags.
  `gh release verify` checks GitHub's own release attestation.

### Changed

- The verification commands in the release notes and SECURITY.md pin the
  release tag and refuse self-hosted runners, with a variant that checks the
  attached bundle offline.
- The verification commands also pin the tag's commit with `--source-digest`,
  and SECURITY.md shows how to check the tag is signed and on `main`.

## [0.1.0] - 2026-10-27

### Added

- Install, start, stop, restart and upgrade of the local Caddy container,
  keeping its volumes, local CA, networks and registered backends.
- `register` and `deregister` for host and container-mode upstreams, with
  subdomain routing, so each project gets a stable `.test` HTTPS URL.
- `list`, `status` and `sync` to show registered backends, report container
  health and drift, and push the registry back to Caddy when they differ.
- `doctor` to diagnose a setup, with fixes for the problems it can repair.
- A relay container on native Linux, so host-mode backends are reachable
  from the Caddy container.
- LAN exposure, giving a backend a second, LAN-facing name for other devices
  on the local network.
- Let's Encrypt certificates for a public domain via manual DNS-01
  (`caddy_shack cert`).
- `version`, which prints the build identity.

[Unreleased]: https://github.com/bawdo/caddy_shack/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/bawdo/caddy_shack/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/bawdo/caddy_shack/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/bawdo/caddy_shack/releases/tag/v0.1.0
