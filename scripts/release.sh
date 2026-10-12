#!/bin/sh
# Build all supported targets without a C compiler. Run from any directory.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
version=$(tr -d '\r\n' < VERSION)
out="$root/dist"
mkdir -p "$out"
stage=$(mktemp -d "$out/.release.XXXXXX")
trap 'rm -rf "$stage"' EXIT
trap 'exit 1' HUP INT TERM

# Copy dependency copyright/license files into every archive. The dependencies
# remain pinned by go.mod/go.sum, including modernc SQLite's own license bundle.
CGO_ENABLED=0 go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' . > "$stage/modules.raw"
sort -u "$stage/modules.raw" > "$stage/modules"
mkdir -p "$stage/licenses"
while read -r module directory; do
    [ -n "$module" ] || continue
    name=$(printf '%s' "$module" | tr '/' '_')
    mkdir -p "$stage/licenses/$name"
    for license in "$directory"/LICENSE* "$directory"/COPYING* "$directory"/NOTICE*; do
        [ -f "$license" ] || continue
        cp "$license" "$stage/licenses/$name/"
    done
done < "$stage/modules"
mkdir -p "$stage/licenses/Go"
go_root=$(go env GOROOT)
go_license="$go_root/LICENSE"
# Homebrew keeps Go's license next to libexec instead of inside GOROOT.
if [ ! -f "$go_license" ]; then go_license="$(dirname "$go_root")/LICENSE"; fi
cp "$go_license" "$stage/licenses/Go/LICENSE"
cp LICENSE NOTICE "$stage/"

for platform in darwin linux; do
    for architecture in amd64 arm64; do
        printf 'Building %s/%s\n' "$platform" "$architecture"
        CGO_ENABLED=0 GOOS="$platform" GOARCH="$architecture" go build \
            -trimpath -buildvcs=false -ldflags='-s -w' -o "$stage/herdr-todo" .
        COPYFILE_DISABLE=1 tar -czf "$out/herdr-todo_${version}_${platform}_${architecture}.tar.gz" \
            -C "$stage" herdr-todo LICENSE NOTICE licenses
    done
done

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "herdr-todo_${version}_"*.tar.gz > SHA256SUMS
else
    shasum -a 256 "herdr-todo_${version}_"*.tar.gz > SHA256SUMS
fi
printf 'Release archives and SHA256SUMS are in %s\n' "$out"
