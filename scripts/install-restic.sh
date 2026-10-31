#!/bin/bash
# Installs restic VERSION into DIR (DIR/restic) from the GitHub release, after checking it
# against the release's SHA256SUMS and that file's signature by restic's release key
# (restic-release-key.asc next to this script, fingerprint
# CF8F 18F2 8445 7597 3F79  D4E1 91A6 868B D3F7 A907). A matching restic already in DIR is
# kept. Used by CI (integration jobs) and by `make integration-oldest`; needs gpg.
#
#   scripts/install-restic.sh 0.17.1 ~/.cache/restor/restic/0.17.1

set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 VERSION DIR" >&2
    exit 2
fi
version=$1
dir=$2

if [ -x "$dir/restic" ] && "$dir/restic" version | grep -q "^restic $version "; then
    exit 0
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    armv7l | armv6l) arch=arm ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

base="https://github.com/restic/restic/releases/download/v$version"
file="restic_${version}_${os}_${arch}.bz2"
key="$(dirname "$0")/restic-release-key.asc"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL -o "$tmp/$file" "$base/$file"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
curl -fsSL -o "$tmp/SHA256SUMS.asc" "$base/SHA256SUMS.asc"
# The checksums come from the same place as the binary; the signature ties them to
# restic's release key. A keyring of its own keeps the user's keys out of it.
GNUPGHOME="$tmp/gnupg" gpg --batch --quiet --dearmor --output "$tmp/restic.gpg" "$key"
gpgv --quiet --keyring "$tmp/restic.gpg" "$tmp/SHA256SUMS.asc" "$tmp/SHA256SUMS"
(cd "$tmp" && grep " $file\$" SHA256SUMS | sha256sum --check --strict --quiet)
bunzip2 "$tmp/$file"
mkdir -p "$dir"
install -m 0755 "$tmp/${file%.bz2}" "$dir/restic"
"$dir/restic" version
