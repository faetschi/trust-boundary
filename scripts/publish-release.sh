#!/usr/bin/env bash
# Publish a GitHub release with the built tbound artifacts.
#
#   scripts/build-release.sh <ver> dist
#   scripts/publish-release.sh <ver> [--dry-run]
#
# Requires the `gh` CLI authenticated for the repository. --dry-run prints the
# command without creating anything. This never publishes npm packages.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-}"; [ -n "$VERSION" ] && shift || true
DRY=0
[ "${1:-}" = "--dry-run" ] && DRY=1
DIST="${TBOUND_DIST:-$ROOT/dist}"
TAG="v$VERSION"

die() { echo "publish-release: $*" >&2; exit 1; }
case "$VERSION" in
  ""|*[!0-9A-Za-z._-]*) die "usage: publish-release.sh <version> [--dry-run] (version required, A-Za-z0-9._-)" ;;
esac
[ -d "$DIST" ] || die "dist dir $DIST not found (run scripts/build-release.sh first)"
ls "$DIST"/tbound-"$VERSION"-*.tar.gz >/dev/null 2>&1 || die "no artifacts for version $VERSION in $DIST"
[ -f "$DIST/SHA256SUMS" ] || die "missing $DIST/SHA256SUMS"

files=("$DIST"/tbound-"$VERSION"-*.tar.gz "$DIST/SHA256SUMS")

# Require the checksums to actually cover the artifacts before publishing.
if ( cd "$DIST" && sha256sum -c SHA256SUMS >/dev/null 2>&1 ); then :
elif ( cd "$DIST" && shasum -a 256 -c SHA256SUMS >/dev/null 2>&1 ); then :
else die "SHA256SUMS does not verify the artifacts in $DIST"; fi
# Every uploaded tarball must appear in SHA256SUMS (coverage, not just validity).
# Every uploaded tarball must appear in SHA256SUMS (coverage, not just validity).
# Use exact field comparison, not a regex, so metacharacters can't spoof a match.
for f in "$DIST"/tbound-"$VERSION"-*.tar.gz; do
  base="$(basename "$f")"
  awk -v a="$base" '$2==a {found=1} END {exit !found}' "$DIST/SHA256SUMS" || die "SHA256SUMS is missing an entry for $base"
done

TARGET="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo '')"
target_arg=()
[ -n "$TARGET" ] && target_arg=(--target "$TARGET")

if [ "$DRY" = 1 ]; then
  echo "publish-release: would run:"
  echo "  gh release create $TAG --title \"tbound $VERSION\" ${target_arg[*]} --notes-file <notes> ${files[*]}"
  exit 0
fi

command -v gh >/dev/null 2>&1 || die "gh CLI is required (install GitHub CLI)"

notes="$(mktemp "${TMPDIR:-/tmp}/tbound-notes.XXXXXX")"
trap 'rm -f "$notes"' EXIT
cat > "$notes" <<NOTES
tbound $VERSION

Tarballs contain bin/tbound, bin/tbound-doctor, runtime/, and shell completions.
The dev/install surface: \`tbound serve --pi --native-fixture\` runs the
non-claim-bearing smoke path; governed \`tbound serve --pi\` remains refused until
the runtime composition admits a signed host profile and the Podman/crun
containment stack.
NOTES

gh release create "$TAG" --title "tbound $VERSION" "${target_arg[@]}" --notes-file "$notes" "${files[@]}"
echo "publish-release: created $TAG"
