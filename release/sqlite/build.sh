#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 3 || ( "$2" != amd64 && "$2" != arm64 ) ]]; then
    printf 'Usage: %s <commit> <amd64|arm64> <output-dir>\n' "$0" >&2
    exit 2
fi

repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
revision="$(git -C "$repo" rev-parse --verify --end-of-options "$1^{commit}")"
arch="$2"
builder_lock="$(git -C "$repo" show "$revision:release/sqlite/builder.lock")"
read -r go_version image_digest amd64_digest arm64_digest <<< "$builder_lock"
version_pattern='^go[0-9]+\.[0-9]+\.[0-9]+$'
digest_pattern='^sha256:[0-9a-f]{64}$'
if [[ ! "$go_version" =~ $version_pattern || ! "$image_digest" =~ $digest_pattern ||
      ! "$amd64_digest" =~ $digest_pattern || ! "$arm64_digest" =~ $digest_pattern ]]; then
    printf 'Invalid Go builder lock for %s\n' "$revision" >&2
    exit 2
fi

builder_arch="$(docker version --format '{{.Server.Arch}}')"
case "$builder_arch" in
    amd64) builder_digest="$amd64_digest" ;;
    arm64) builder_digest="$arm64_digest" ;;
    *) printf 'Unsupported Docker server architecture: %s\n' "$builder_arch" >&2; exit 2 ;;
esac
image="docker.io/library/golang:${go_version#go}-bookworm"

umask 022
mkdir -p -- "$3"
output="$(cd -- "$3" && pwd -P)"
staging="$(mktemp -d "$output/.seafile-sqlite-build.XXXXXXXX")"
trap 'rm -rf -- "$staging"' EXIT

archive="seafile-linux-sqlite-$revision-$arch.tar.gz"
git -C "$repo" archive "$revision" |
    docker run --rm -i --platform "linux/$builder_arch" "$image@$builder_digest" bash -euc '
        mkdir /tmp/seafile-release-source
        tar -xf - -C /tmp/seafile-release-source
        cd /tmp/seafile-release-source
        exec bash release/sqlite/build-archive.sh "$@"
    ' archive "$revision" "$arch" "$go_version" "$image@$image_digest" > "$staging/$archive"

(
    cd -- "$staging"
    sha256sum "$archive" > "$archive.sha256"
)

for file in "$archive" "$archive.sha256"; do
    # Publishing without replacing an existing path also handles simultaneous
    # reproducible builds. An identical archive can complete a partial publish.
    destination="$output/$file"
    if ln -- "$staging/$file" "$output" 2>/dev/null; then
        continue
    fi
    if [[ ! -f "$destination" || -L "$destination" ]] ||
        ! cmp -s "$staging/$file" "$destination"; then
        printf 'Cannot publish archive output %s\n' "$destination" >&2
        exit 1
    fi
done

printf '%s\n' "$output/$archive"
