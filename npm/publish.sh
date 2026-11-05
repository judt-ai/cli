#!/bin/sh
# Publishes the npm packages from GoReleaser's dist/: one @judt/cli-<platform>-<arch>
# package per binary, then `judt`, which depends on all of them at the same version.
#
#   npm/publish.sh 1.2.3            publish (needs NPM_TOKEN in ~/.npmrc or the env)
#   npm/publish.sh 1.2.3 --dry-run  build the packages and show what would publish
set -eu

version="${1:?usage: npm/publish.sh <version> [--dry-run]}"
version="${version#v}"
shift
flags="--access public $*"
case " $* " in *" --dry-run "*) ;; *) [ -n "${GITHUB_ACTIONS:-}" ] && flags="$flags --provenance" ;; esac

root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/dist/npm"
rm -rf "$out"
mkdir -p "$out"

for target in darwin:arm64:darwin:arm64 darwin:amd64:darwin:x64 linux:arm64:linux:arm64 linux:amd64:linux:x64 windows:arm64:win32:arm64 windows:amd64:win32:x64; do
  IFS=: read -r goos goarch platform cpu <<EOF
$target
EOF
  exe=judt
  [ "$goos" = windows ] && exe=judt.exe
  bin="$(ls "$root"/dist/judt_"${goos}"_"${goarch}"*/"$exe" 2>/dev/null | head -n 1)"
  [ -n "$bin" ] || { echo "no $goos/$goarch build in dist/; run goreleaser first" >&2; exit 1; }
  name="cli-$platform-$cpu"
  mkdir -p "$out/$name/bin"
  cp "$bin" "$out/$name/bin/$exe"
  cp "$root/LICENSE" "$out/$name/LICENSE"
  cat >"$out/$name/package.json" <<JSON
{
  "name": "@judt/$name",
  "version": "$version",
  "description": "The judt binary for $platform-$cpu. Install judt instead.",
  "license": "MIT",
  "repository": { "type": "git", "url": "git+https://github.com/judt-ai/cli.git" },
  "os": ["$platform"],
  "cpu": ["$cpu"],
  "files": ["bin"]
}
JSON
  (cd "$out/$name" && npm publish $flags)
done

cp -R "$root/npm/judt" "$out/judt"
cp "$root/README.md" "$out/judt/README.md"
cp "$root/LICENSE" "$out/judt/LICENSE"
node -e '
  const fs = require("node:fs");
  const file = process.argv[1], version = process.argv[2];
  const pkg = JSON.parse(fs.readFileSync(file, "utf8"));
  pkg.version = version;
  for (const dep of Object.keys(pkg.optionalDependencies)) pkg.optionalDependencies[dep] = version;
  fs.writeFileSync(file, JSON.stringify(pkg, null, 2) + "\n");
' "$out/judt/package.json" "$version"
(cd "$out/judt" && npm publish $flags)
