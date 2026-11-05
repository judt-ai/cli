#!/bin/sh
# Installs the Judt CLI: curl -fsSL https://judt.ai/install | sh
#
#   JUDT_VERSION=1.2.3   install that version instead of the latest
#   JUDT_INSTALL=/path   install to /path/bin instead of ~/.judt/bin
set -eu

repo="judt-ai/cli"
dir="${JUDT_INSTALL:-$HOME/.judt}/bin"

fail() { echo "judt install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "$(uname -s) isn't supported here. On Windows, run: npm install -g judt" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "$(uname -m) isn't supported" ;;
esac

if [ -n "${JUDT_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/v${JUDT_VERSION#v}"
else
  base="https://github.com/$repo/releases/latest/download"
fi
archive="judt_${os}_${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
echo "Downloading $archive..."
curl -fsSL "$base/$archive" -o "$tmp/$archive" || fail "couldn't download $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "couldn't download checksums.txt"

expected="$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
[ -n "$expected" ] || fail "checksums.txt has no entry for $archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$archive" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)"
fi
[ "$expected" = "$actual" ] || fail "the download doesn't match its checksum"

tar -xzf "$tmp/$archive" -C "$tmp" judt
mkdir -p "$dir"
mv "$tmp/judt" "$dir/judt"
chmod 755 "$dir/judt"

echo "Installed $("$dir/judt" version) to $dir/judt"
case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    echo
    echo "Add it to your PATH, for example in ~/.zshrc or ~/.bashrc:"
    echo "  export PATH=\"$dir:\$PATH\""
    ;;
esac
echo
echo "Next: judt auth login"
