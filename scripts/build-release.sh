#!/usr/bin/env bash
#
# Builds the release archives into dist/ and smoke tests the one for this
# machine.
#
#   scripts/build-release.sh X.Y.Z COMMIT
#
# Writes dist/caddy_shack_X.Y.Z_<os>_<arch>.tar.gz for linux and darwin on
# amd64 and arm64, each holding a caddy_shack_X.Y.Z_<os>_<arch>/ directory
# with the binary, LICENSE, README.md and CHANGELOG.md, plus
# dist/checksums.txt. Then unpacks the host archive and fails unless its
# binary reports caddy_shack vX.Y.Z. BUILD_RELEASE_EXPECT overrides the
# expected version (for testing the smoke check). Run from the repository
# root; dist/ is replaced.

set -euo pipefail

die() {
    echo "build-release: $*" >&2
    exit 1
}

version="${1:-}"
commit="${2:-}"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || ! [[ "$commit" =~ ^[0-9a-f]+$ ]]; then
    die "usage: $0 X.Y.Z COMMIT"
fi
tag="v$version"
pkg=github.com/bawdo/caddy_shack/internal/version
ldflags="-s -w -X $pkg.Version=$tag -X $pkg.Commit=$commit -X $pkg.Tag=$tag -X $pkg.Dirty=false"

rm -rf dist
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
    os="${target%/*}"
    arch="${target#*/}"
    name="caddy_shack_${version}_${os}_${arch}"
    mkdir -p "dist/$name"
    echo "build-release: building $name"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$ldflags" -o "dist/$name/caddy_shack" .
    cp LICENSE README.md CHANGELOG.md "dist/$name/"
    tar -C dist -czf "dist/$name.tar.gz" "$name"
    rm -r "dist/${name:?}"
done

if command -v sha256sum >/dev/null 2>&1; then
    (cd dist && sha256sum ./*.tar.gz | sed 's| \./| |' > checksums.txt)
else
    (cd dist && shasum -a 256 ./*.tar.gz | sed 's| \./| |' > checksums.txt)
fi
cat dist/checksums.txt

{ read -r host_os; read -r host_arch; } < <(go env GOOS GOARCH)
host="caddy_shack_${version}_${host_os}_${host_arch}"
expect="caddy_shack ${BUILD_RELEASE_EXPECT:-$tag}"
smoke="$(mktemp -d "${TMPDIR:-/tmp}/build-release.XXXXXX")"
trap 'rm -rf "$smoke"' EXIT
tar -C "$smoke" -xzf "dist/$host.tar.gz"
output=$("$smoke/$host/caddy_shack" version) || die "smoke test: caddy_shack version failed"
echo "$output"
first=$(head -n 1 <<<"$output")
[ "$first" = "$expect" ] || die "built binary reports $first, expected $expect"
echo "build-release: smoke test passed for $host"
