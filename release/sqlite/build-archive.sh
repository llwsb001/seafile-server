#!/usr/bin/env bash
# Runs inside the image selected by build.sh on a git archive of the revision.
set -euo pipefail

revision="$1"
arch="$2"
go_version="$3"
builder_image="$4"

export LC_ALL=C GOENV=off GOWORK=off GOTOOLCHAIN=local
export CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOAMD64=v1 GOARM64=v8.0
unset GOFLAGS GOEXPERIMENT
actual_go_version="$(go env GOVERSION)"
if [[ "$actual_go_version" != "$go_version" ]]; then
    printf 'Expected Go %s, found %s\n' "$go_version" "$actual_go_version" >&2
    exit 1
fi

umask 022
staging="$(mktemp -d)"
trap 'rm -rf -- "$staging"' EXIT
bin="$staging/seafile/bin"
share="$staging/seafile/share"
install -d -m 0755 "$bin" "$share/config" "$share/sqlite"
install -m 0755 release/sqlite/service.py "$bin/seafile-sqlite"
install -m 0644 release/sqlite/SERVICE.md "$share"

build_flags=(-mod=readonly -trimpath -buildvcs=false)
go -C fileserver build "${build_flags[@]}" -o "$bin/seaf-server-go" ./cmd/seafserver
go -C fileserver build "${build_flags[@]}" -o "$bin/fileserver" .
go -C fileserver build "${build_flags[@]}" -o "$bin/seafile-controller" ./cmd/seafilecontroller
go -C fileserver build "${build_flags[@]}" -o "$bin/seaf-fsck" ./cmd/seaffsck
go -C fileserver build "${build_flags[@]}" -o "$bin/seafserv-gc" ./cmd/seafservgc
go -C notification-server build "${build_flags[@]}" -o "$bin/notification-server" .
chmod 0755 "$bin/"*

install -m 0644 scripts/sql/sqlite/*.sql "$share/sqlite"
install -m 0644 release/sqlite/config/*.conf "$share/config"
install -m 0755 release/sqlite/snapshot.py "$bin/seafile-sqlite-snapshot"
install -m 0644 release/sqlite/SNAPSHOT.md "$share/SNAPSHOT.md"
install -m 0644 LICENSE.txt "$staging"
install -m 0755 release/sqlite/init.py "$staging"
install -m 0644 release/sqlite/INIT.md "$staging"
printf '%s\n' \
    "revision=$revision" \
    "builder_image=$builder_image" \
    "go_version=$go_version" \
    "goos=$GOOS" \
    "goarch=$GOARCH" \
    "goamd64=$GOAMD64" \
    "goarm64=$GOARM64" \
    "cgo_enabled=$CGO_ENABLED" \
    "build_flags=${build_flags[*]}" > "$staging/BUILD.txt"

cd -- "$staging"
find BUILD.txt INIT.md LICENSE.txt init.py seafile -type f -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
tar --format=gnu --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
    -cf - BUILD.txt INIT.md LICENSE.txt SHA256SUMS init.py seafile | gzip -n
