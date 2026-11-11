#!/bin/sh
# Installs the Judt CLI: curl -fsSL https://judt.ai/install | sh
#
#   JUDT_VERSION=1.2.3   install that version instead of the latest
#   JUDT_INSTALL=/path   install to /path/bin instead of ~/.judt/bin
#   JUDT_NO_MODIFY_PATH=1   don't add that folder to PATH in your shell's startup file
set -eu

repo="judt-ai/cli"
dir="${JUDT_INSTALL:-$HOME/.judt}/bin"

fail() { echo "judt install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "$(uname -s) isn't supported here. On Windows, run: npm install -g @judt/cli" ;;
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

# The folder as it's written in a startup file, so the line still works if the file is shared.
case "$dir" in
  "$HOME"/*) shown="\$HOME/${dir#"$HOME"/}" ;;
  *) shown="$dir" ;;
esac
line="export PATH=\"$shown:\$PATH\""
case "${SHELL:-}" in
  */zsh) rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
  */bash)
    # Terminal on macOS opens login shells, which read .bash_profile and not .bashrc.
    if [ "$os" = darwin ]; then rc="$HOME/.bash_profile"; else rc="$HOME/.bashrc"; fi
    ;;
  */fish)
    rc="${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/judt.fish"
    line="fish_add_path \"$shown\""
    ;;
  *) rc="$HOME/.profile" ;;
esac

# Adds the line unless the file already has this folder. Fails if the file can't be written.
add_to_path() {
  if [ -f "$rc" ] && grep -qF -e "$shown" -e "$dir" "$rc"; then return 0; fi
  { mkdir -p "$(dirname "$rc")" && printf '\n# Judt CLI\n%s\n' "$line" >>"$rc"; } 2>/dev/null
}

echo
case ":$PATH:" in
  *":$dir:"*)
    echo "Next: judt auth login"
    ;;
  *)
    if [ -z "${JUDT_NO_MODIFY_PATH:-}" ] && add_to_path; then
      echo "Added judt to your PATH in $rc. Open a new terminal, or run this in this one:"
    else
      echo "Add judt to your PATH by adding this line to $rc, and run it in this terminal:"
    fi
    echo "  $line"
    echo
    echo "Then: judt auth login"
    ;;
esac
