#!/usr/bin/env bash
# Pack the npm package trees from make-npm-packages.sh into the tarballs that get
# published, then install those exact tarballs offline and run them. Run it on
# Linux: the Actions artifact upload and npm pack on Windows both drop the
# executable bit, and a published version can never be repacked.
#
# Usage: scripts/pack-npm-packages.sh <version> <npm-tree-dir> <out-dir>
set -euo pipefail

die() { echo "pack-npm-packages: $*" >&2; exit 1; }
[ "$#" -eq 3 ] || die "usage: pack-npm-packages.sh <version> <npm-tree-dir> <out-dir>"
VERSION="$1"
[ "$(uname -s)" = Linux ] || die "run on Linux; npm pack elsewhere drops the executable bit"
SRC="$(cd "$2" && pwd)"
mkdir -p "$3"
OUT="$(cd "$3" && pwd)"

# Publish order: the platform package first, then the root package that depends on it.
for pkg in cli-linux-x64 cli; do
  [ -f "$SRC/$pkg/package.json" ] || die "missing $SRC/$pkg/package.json"
  npm pack --ignore-scripts --silent --pack-destination "$OUT" "$SRC/$pkg" >/dev/null
done

platform="$OUT/tbound-cli-linux-x64-$VERSION.tgz"
root="$OUT/tbound-cli-$VERSION.tgz"
[ -f "$platform" ] && [ -f "$root" ] || die "npm pack did not write $(basename "$platform") and $(basename "$root")"

platform_list="$(tar -tvzf "$platform")"
root_list="$(tar -tzf "$root")"
for b in tbound tbound-doctor tbound-profile; do
  grep -E "^-rwxr-xr-x .* package/bin/$b\$" <<<"$platform_list" >/dev/null \
    || die "package/bin/$b is missing or not mode 0755 in $(basename "$platform")"
done
for f in package/bin/tbound.js package/scripts/postinstall.js package/README.md package/LICENSE; do
  grep -Fx "$f" <<<"$root_list" >/dev/null || die "$f missing from $(basename "$root")"
done
if grep -F tbound-cell-entrypoint <<<"$platform_list$root_list" >/dev/null; then
  die "the TCB entrypoint must never be packed for npm"
fi

tar -xzOf "$platform" package/package.json > "$OUT/.platform.json"
tar -xzOf "$root" package/package.json > "$OUT/.root.json"
node - "$VERSION" "$OUT/.platform.json" "$OUT/.root.json" <<'EOF' || die "packed manifests do not match version $VERSION"
const fs = require("node:fs");
const [version, platformPath, rootPath] = process.argv.slice(2);
const platform = JSON.parse(fs.readFileSync(platformPath, "utf8"));
const root = JSON.parse(fs.readFileSync(rootPath, "utf8"));
const checks = [
  [platform.name === "@tbound/cli-linux-x64", "platform name"],
  [platform.version === version, "platform version"],
  [root.name === "@tbound/cli", "root name"],
  [root.version === version, "root version"],
  [(root.optionalDependencies || {})["@tbound/cli-linux-x64"] === version, "root optionalDependency pins the platform version"],
  [!("private" in platform) && !("private" in root), "neither manifest is private"],
];
const failed = checks.filter(([ok]) => !ok).map(([, what]) => what);
if (failed.length) { console.error(`pack-npm-packages: failed: ${failed.join(", ")}`); process.exit(1); }
EOF
rm -f "$OUT/.platform.json" "$OUT/.root.json"

smoke="$(mktemp -d "${TMPDIR:-/tmp}/tbound-npm-smoke.XXXXXX")"
trap 'rm -rf "$smoke"' EXIT
npm install --prefix "$smoke" --offline --no-audit --no-fund --loglevel=error "$platform" "$root" >/dev/null \
  || die "the packed tarballs do not install offline"
got="$("$smoke/node_modules/.bin/tbound" --version 2>&1)" || die "installed tbound failed to run: $got"
[ "$got" = "tbound $VERSION" ] || die "installed tbound --version printed: $got"

(cd "$OUT" && sha256sum "$(basename "$platform")" "$(basename "$root")")
