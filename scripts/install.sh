#!/bin/sh
# Herdr runs this build hook in the installed checkout. End users need no Go.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$(tr -d '\r\n' < "$root/VERSION")
case "$version" in ''|*[!0-9.]*) echo 'Invalid release version' >&2; exit 1 ;; esac

# A developer may have built the matching version before linking the checkout.
if [ -x "$root/bin/herdr-todo" ] && [ "$("$root/bin/herdr-todo" --version)" = "$version" ]; then
    exit 0
fi

case $(uname -s) in
    Darwin) platform=darwin ;;
    Linux) platform=linux ;;
    *) echo 'Herdr Todo supports macOS and Linux.' >&2; exit 1 ;;
esac
case $(uname -m) in
    arm64|aarch64) architecture=arm64 ;;
    x86_64|amd64) architecture=amd64 ;;
    *) echo 'Herdr Todo needs an arm64 or amd64 machine.' >&2; exit 1 ;;
esac

command -v curl >/dev/null 2>&1 || { echo 'Install curl to download Herdr Todo.' >&2; exit 1; }
command -v tar >/dev/null 2>&1 || { echo 'Install tar to unpack Herdr Todo.' >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
    checksum=sha256sum
elif command -v shasum >/dev/null 2>&1; then
    checksum=shasum
else
    echo 'Install sha256sum or shasum to verify the download.' >&2; exit 1
fi

asset="herdr-todo_${version}_${platform}_${architecture}.tar.gz"
base="https://github.com/qselle/herdr-todo/releases/download/v${version}"
mkdir -p "$root/bin"
# Keep the temporary binary on the destination filesystem for an atomic rename.
tmp=$(mktemp -d "$root/bin/.install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' HUP INT TERM

curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 120 --retry 2 --output "$tmp/$asset" "$base/$asset"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 120 --retry 2 --output "$tmp/SHA256SUMS" "$base/SHA256SUMS"

expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$tmp/SHA256SUMS")
case "$expected" in ''|*[!a-fA-F0-9]*) echo 'Missing or invalid release checksum.' >&2; exit 1 ;; esac
[ "${#expected}" -eq 64 ] || { echo 'Invalid release checksum length.' >&2; exit 1; }
if [ "$checksum" = sha256sum ]; then
    actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
else
    actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || { echo 'Release checksum mismatch; installation stopped.' >&2; exit 1; }

tar -xzf "$tmp/$asset" -C "$tmp" herdr-todo
[ -f "$tmp/herdr-todo" ] && [ ! -L "$tmp/herdr-todo" ] || { echo 'Missing release binary.' >&2; exit 1; }
chmod 755 "$tmp/herdr-todo"
[ "$("$tmp/herdr-todo" --version)" = "$version" ] || { echo 'Release binary version mismatch.' >&2; exit 1; }
mv -f "$tmp/herdr-todo" "$root/bin/herdr-todo"
printf 'Installed Herdr Todo %s (%s/%s)\n' "$version" "$platform" "$architecture"
