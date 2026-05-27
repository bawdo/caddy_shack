# MVP vertical slice design

Date: 2026-05-27
Status: Draft, awaiting user review

## Goal

Ship a usable end-to-end slice of CaddyShack: one developer can install a
locally-running Caddy in Docker, register a backend, hit it over HTTPS at a
stable `.test` URL, and deregister it on shutdown. Each backend project is
expected to call `caddy_shack register` on startup and `caddy_shack deregister`
on teardown.

## In scope (MVP)

- `caddy_shack install` - pulls the Caddy image, creates named volumes, starts
  the container with an empty config.
- `caddy_shack start` / `stop` / `restart` - manages the container.
- `caddy_shack status` - container state, admin API reachability, registered
  backend count.
- `caddy_shack register <name> --upstream host:port [--domain x.test]` -
  idempotent registration of a backend.
- `caddy_shack deregister <name>` - idempotent removal.
- `caddy_shack list` - human and `--json` listings of registered backends.

## Non-goals (explicitly deferred)

- `update`, `uninstall --purge`, `logs`, `doctor`, `setup`, `open`, `wait`,
  shell completions.
- Per-route options (path rewrites, header injection, websocket toggles).
- Automated DNS or trust-store setup. Both are user prerequisites for MVP;
  `doctor` and `setup` will automate later.
- Multi-machine or remote Caddy management. Local Docker only.
- Backends serving their own TLS. Upstreams are plain HTTP.

## Audience and assumptions

- Solo developer use for now. Possible OSS release later, so avoid hard-coding
  personal paths or assumptions but do not pre-build multi-user features.
- macOS only for MVP. Use XDG-style state paths so future cross-platform work
  is unblocked.

## Approach

Vertical slice: build the smallest set of commands that lets one project work
end-to-end. Use real usage to drive the next slice. Plumbing decisions (state
store, locking, atomic API calls) are easier to make once one slice works.

## Architecture

Three internal collaborators sit behind the CLI:

1. **Docker layer** (`internal/docker`) - shells out to the `docker` CLI to
   manage a single container named `caddy_shack`. Named volumes:
   `caddy_shack_data` (certs, ACME state) and `caddy_shack_config` (Caddy
   autosave). Published ports: `127.0.0.1:80`, `127.0.0.1:443`,
   `127.0.0.1:2019` (admin, loopback-only).
2. **Caddy admin client** (`internal/caddyapi`) - HTTP client against
   `http://127.0.0.1:2019`. Uses `POST /load` for register/deregister so
   changes are atomic full-config swaps.
3. **State store** (`internal/state`) - single JSON file under
   `~/Library/Application Support/caddy_shack/state.json`. The state file is
   the source of truth; Caddy config is derived from it on every change.

Command flow for register/deregister:

```
take file lock
  read state.json
  mutate in memory
  render full Caddy JSON config from state
  POST /load to Caddy admin API
  on 2xx, atomically write state.json
release lock
```

### Package layout

```
internal/
  docker/    docker CLI wrapper (interface + concrete impl)
  caddyapi/  admin API client and JSON config builder
  state/     JSON store and file lock
  app/       orchestrates the three above; one func per command
cmd/
  install.go register.go deregister.go list.go status.go
  start.go stop.go restart.go
```

## State file

Path: `~/Library/Application Support/caddy_shack/state.json` (created on first
`install`).

Schema (v1):

```json
{
  "schema_version": 1,
  "backends": {
    "myapp": {
      "domain": "myapp.test",
      "upstream": "127.0.0.1:3000",
      "registered_at": "2026-05-27T11:58:00+10:00"
    }
  }
}
```

Rules:

- Keyed by backend `name`. Name must match `^[a-z0-9][a-z0-9-]{0,30}$`.
- Default `domain` is `<name>.test` when `--domain` is omitted.
- Read-modify-write goes through a `flock(2)` on a sibling `state.json.lock`,
  with a 5 second timeout (exit 8 on contention).
- Writes are atomic: `state.json.tmp` then `os.Rename`.
- A corrupt state file is never silently rebuilt. Exit 7 with the path and
  require manual remediation.

## Caddy config shape

Rendered fresh from state on every change. Single HTTP server `srv0`
listening on `:443` (with `:80` for automatic redirects). One route per
backend:

```json
{
  "match": [{"host": ["myapp.test"]}],
  "handle": [{
    "handler": "reverse_proxy",
    "upstreams": [{"dial": "127.0.0.1:3000"}]
  }]
}
```

TLS automation policy uses the `internal` issuer for any `*.test` host. Caddy
generates and serves certs from its built-in local CA.

The whole config is pushed via `POST /load` so the swap is atomic from
Caddy's perspective.

## HTTPS only

The public-facing side of Caddy serves HTTPS only. The port 80 listener exists
solely for Caddy's automatic 308 redirect to HTTPS. There is no `--scheme`
flag on `register`. Upstreams are assumed to be plain HTTP (typical local dev
servers). Backends serving their own TLS are not supported in MVP.

## DNS and TLS

### DNS for `.test`

MVP treats DNS as a user prerequisite documented in `README.md`. CaddyShack
does not modify dnsmasq config, `/etc/hosts`, or `/etc/resolver` during
`install`. The Phase 2 `doctor` command will detect missing DNS configuration,
write the dnsmasq fragment (if dnsmasq is on PATH), and prompt the user to
apply the sudo-required steps.

### TLS

- Caddy uses its built-in `internal` issuer for `.test`, so cert issuance is
  automatic with no external dependencies.
- The root CA cert lives in the `caddy_shack_data` volume. MVP documents the
  manual `security add-trusted-cert ...` step; Phase 2 `setup` will automate
  trust-store integration.

## Command surface

All commands are non-interactive (no prompts). Output is human-readable on
stdout by default; `--json` switches structured commands to JSON. Errors go to
stderr with a short cause. `--quiet` suppresses informational stdout but not
errors.

| Command | Behaviour | Exit codes |
|---|---|---|
| `install` | Creates volumes, pulls the Caddy image (tag pinned in source, e.g. `caddy:2`), starts container with empty config. No-op if installed. | 0, 4 |
| `start` | Starts the existing container. Errors if not installed. | 0, 4, 6 |
| `stop` | Stops the container. No-op if already stopped. | 0, 4 |
| `restart` | Stop then start. | 0, 4, 6 |
| `status` | Prints container state, admin API reachability, backend count. `--json` for scripts. Exit 0 if container running AND admin API reachable, exit 1 otherwise. | 0, 1 |
| `register <name> --upstream host:port [--domain d]` | Idempotent register. Re-registering same fields is a no-op. Updates on change. | 0, 1, 2, 3, 5, 7, 8 |
| `deregister <name>` | Idempotent. Unknown name is exit 0 with stderr note. | 0, 1, 5, 7, 8 |
| `list` | Table by default; `--json` for scripts. | 0, 7 |

### Flag conventions

- Long flags only for clarity in scripts (`--upstream`, not `-u`), except the
  standard `--help`.
- No hidden interactivity. Scripts must be able to invoke any command without
  a TTY.

## Idempotency rules

- `register foo --upstream X` twice: no-op, exit 0.
- `register foo --upstream Y` when foo exists with X: update, exit 0, stderr
  notes "updated".
- `register bar --domain foo.test` when foo already owns foo.test: exit 3, no
  state change.
- `deregister unknown`: exit 0, stderr "no such backend".
- `install` when already installed: no-op, exit 0.

## Error taxonomy

Exit codes are part of the public CLI contract and are documented in
`caddy_shack --help` long text so startup scripts can branch on them.

| Code | Meaning |
|---|---|
| 0 | OK |
| 1 | Generic command failure (e.g. Caddy admin API rejected the config; `status` reports unhealthy) |
| 2 | Invalid arguments |
| 3 | Domain conflict |
| 4 | Docker daemon unreachable |
| 5 | Caddy container not running |
| 6 | Caddy not installed (no container exists) |
| 7 | State file corrupt, manual remediation required |
| 8 | Lock held by another caddy_shack process (5s timeout) |

Code 1 is the catch-all primary failure for a command; specific codes are
reserved for cross-cutting concerns scripts may want to branch on. The state
file is never rewritten on a failed Caddy API call.

## Testing strategy

### Unit tests (fast, no docker)

- `internal/state`: lock acquisition, atomic write, schema-version handling,
  corrupt-file detection.
- `internal/caddyapi`: state to Caddy JSON config rendering using golden-file
  tests.
- `internal/docker`: tested via an interface that the app layer consumes.
- `cmd/*`: flag parsing, exit code mapping, output format (table vs JSON).

### Integration tests (require docker, gated by `-tags integration`)

- Spin up a real Caddy container against a temp data dir.
- Run `register`, hit `GET /config/` on Caddy's admin API, assert the
  rendered config matches.
- Run `register` then curl an upstream through Caddy and assert HTTPS works
  (using Caddy's local CA).
- Concurrent `register` race test: two processes invoking simultaneously,
  file lock must serialise them.

### Smoke test (manual, documented in README)

- `python -m http.server 3000`, then
  `caddy_shack register hello --upstream 127.0.0.1:3000`, then
  `curl https://hello.test`.

### CI

- Existing workflow continues to run unit tests fast.
- Add a job that runs `go test -tags integration ./...` on `ubuntu-latest`
  (has Docker).
- Coverage threshold unchanged for now.

## Open questions

None at design time. Phase 2 work (doctor, setup, update, logs) will be
designed in separate specs once MVP usage surfaces real pain points.
