# Security policy

caddy_shack is a local development tool that runs Caddy in Docker and trusts
the containers next to it. The README's
[Security notes](README.md#security-notes) describe that trust boundary in
full. Bugs that weaken it in ways the notes do not already describe are
security bugs: the Caddy admin API and the local root CA key reachable
through it, how Let's Encrypt private keys are held, and the LAN listener.
Behaviour the notes already document as a known limit is not a
vulnerability, though a better fix is always welcome.

The release gate (the check that a tag is on `main` and signed) relies on
nobody but the maintainer having write access to the repository, since a
collaborator with write access could alter the workflow. The `v*` tag
ruleset blocks moving or deleting release tags.

## Supported versions

Only the newest release on the
[Releases page](https://github.com/bawdo/caddy_shack/releases) receives
fixes. There are no maintenance branches.

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Use GitHub's private reporting form:
<https://github.com/bawdo/caddy_shack/security/advisories/new>. It goes
straight to the maintainer and nobody else can see it.

Include what you can of: the output of `caddy_shack version` and
`caddy_shack doctor`, the Docker engine you run (Docker Desktop, OrbStack or
native Linux) and its version, the steps involved, what you expected, and
what happened. A proof of concept helps but is not required.

You will get an acknowledgement within seven days. A fix, or an explanation
of why the report is not being treated as a vulnerability, follows as a
published advisory and a release. Credit is given in the advisory unless
you ask otherwise.

## Verifying a release

Every release archive carries a build provenance attestation from the
release workflow, and a `checksums.txt` lists the archive digests. To
confirm a download came from this repository's CI and has not been
altered, run both against the archive you downloaded:

```bash
gh attestation verify caddy_shack_<version>_<os>_<arch>.tar.gz --repo bawdo/caddy_shack \
  --signer-workflow bawdo/caddy_shack/.github/workflows/release.yml \
  --source-ref refs/tags/v<version> \
  --deny-self-hosted-runners
sha256sum -c checksums.txt --ignore-missing
```

On macOS, use `shasum -a 256 -c checksums.txt --ignore-missing` for the
second command.

`--repo` on its own only proves that some workflow in this repository, run
from any ref, signed the archive. The other three flags pin it to the
release workflow running from that release's tag on a GitHub-hosted runner,
which rules out an attestation made from a throwaway branch, by any other
workflow file or on a self-hosted runner.

Releases after v0.1.0 also carry the attestation as a Sigstore bundle,
`caddy_shack_<version>_provenance.intoto.jsonl` (v0.1.0 predates it).
Download it next to the archive to verify without querying GitHub's
attestation API:

```bash
gh attestation verify caddy_shack_<version>_<os>_<arch>.tar.gz --repo bawdo/caddy_shack \
  --signer-workflow bawdo/caddy_shack/.github/workflows/release.yml \
  --source-ref refs/tags/v<version> \
  --deny-self-hosted-runners \
  --bundle caddy_shack_<version>_provenance.intoto.jsonl
```

On a machine with no network access at all, `gh` cannot fetch the Sigstore
trusted root itself. Save it on a connected machine, copy it across and
pass it in:

```bash
gh attestation trusted-root > trusted_root.jsonl
gh attestation verify caddy_shack_<version>_<os>_<arch>.tar.gz --repo bawdo/caddy_shack \
  --signer-workflow bawdo/caddy_shack/.github/workflows/release.yml \
  --source-ref refs/tags/v<version> \
  --deny-self-hosted-runners \
  --bundle caddy_shack_<version>_provenance.intoto.jsonl \
  --custom-trusted-root trusted_root.jsonl
```
