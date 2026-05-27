# caddy_shack

CaddyShack is a CLI tool that helps to deploy and manage a locally running
caddy instance.

The purpose of this is so an engineer can easily spin up a local project and
register that project with caddy and have https and stable/distinct local URLs
for these projects.

This is a helpful wrapper around the Caddy API that helps do things like:

- De/Register a new backend web service
- ...
- ...

CaddyShack also simplifies installing caddy (via docker) keeping it running,
checking on it's status/health etc, and to update the docker image without
losing configuration.

CaddyShack only manages the locally running caddy server via the API.

[Caddy](https://caddyserver.com/)
[Caddy API](https://caddyserver.com/docs/api)

## Install

```
go install github.com/bawdo/caddy_shack@latest
```

## Usage

```
caddy_shack version
```

## Development

Run the local pre-CI gauntlet before pushing:

```
make pre-ci
```

Auto-fix formatting along the way:

```
make pre-ci-fix
```

Install a local build into `$GOBIN`:

```
make install
```
