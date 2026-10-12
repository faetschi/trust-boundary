#!/usr/bin/env bash
# Offline consistency check between build-release.sh sidecar names/checksum
# members and both GitHub Actions upload lists. No Go build, network, publish, or
# filesystem mutation outside a private temp directory is performed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/release.yml"
BUILDER="$ROOT/scripts/build-release.sh"
VERSION="${1:-1.2.3}"
if [ -z "${TMPDIR:-}" ] || [ ! -d "$TMPDIR" ] || [ "$(stat -c '%a' "$TMPDIR" 2>/dev/null || true)" != 700 ]; then
  echo "verify-release-workflow-coverage: set TMPDIR to an existing private mode-0700 directory" >&2
  exit 2
fi
NORMALIZED_WORKFLOW="$(mktemp "$TMPDIR/tbound-release-workflow.XXXXXX")"
trap 'rm -f "$NORMALIZED_WORKFLOW"' EXIT
sed 's/\r$//' "$WORKFLOW" > "$NORMALIZED_WORKFLOW"

die() { echo "verify-release-workflow-coverage: FAIL: $*" >&2; exit 1; }
pass() { printf '  PASS  %s\n' "$*"; }

metadata_pattern='dist/tbound-cell-entrypoint-*-linux-amd64.metadata.json'
checksum_pattern='dist/tbound-cell-entrypoint-*-linux-amd64.sha256'
[ "$(grep -Fxc "            $metadata_pattern" "$NORMALIZED_WORKFLOW")" -eq 2 ] || die "metadata sidecar glob must appear in both upload lists"
[ "$(grep -Fxc "            $checksum_pattern" "$NORMALIZED_WORKFLOW")" -eq 2 ] || die "checksum sidecar glob must appear in both upload lists"
[ "$(grep -Fxc '            dist/SHA256SUMS' "$NORMALIZED_WORKFLOW")" -eq 2 ] || die "SHA256SUMS must appear in both upload lists"
pass "upload-artifact and GitHub release both include SHA256SUMS and the two exact sidecar globs"

grep -Fq 'ENTRY_META_BASE="tbound-cell-entrypoint-$VERSION-$ENTRY_TARGET"' "$BUILDER" || die "builder sidecar basename template changed"
grep -Fq '"$STAGE_OUT/$ENTRY_META_BASE.metadata.json"' "$BUILDER" || die "builder no longer stages metadata sidecar"
grep -Fq '"$STAGE_OUT/$ENTRY_META_BASE.sha256"' "$BUILDER" || die "builder no longer stages checksum sidecar"
grep -Fq 'CHECKSUM_INPUTS+=("$STAGE_OUT/$ENTRY_META_BASE.metadata.json" "$STAGE_OUT/$ENTRY_META_BASE.sha256")' "$BUILDER" || die "SHA256SUMS no longer covers both sidecars"
pass "builder generates and adds both sidecars to SHA256SUMS"

# Build a checksum-member fixture using the exact current filename template, then
# assert every member class has an upload path. The archive/layout test separately
# validates real generated SHA256SUMS bytes when the Linux build succeeds.
FIXTURE="$(mktemp "$TMPDIR/tbound-release-workflow-members.XXXXXX")"
trap 'rm -f "$FIXTURE" "$NORMALIZED_WORKFLOW"' EXIT
metadata="tbound-cell-entrypoint-$VERSION-linux-amd64.metadata.json"
checksum="tbound-cell-entrypoint-$VERSION-linux-amd64.sha256"
printf '%064d  %s\n%064d  %s\n%064d  %s\n' \
  0 "tbound-$VERSION-linux-amd64.tar.gz" \
  0 "$metadata" \
  0 "$checksum" > "$FIXTURE"
while read -r _digest member; do
  case "$member" in
    tbound-*.tar.gz) grep -Fqx '            dist/*.tar.gz' "$NORMALIZED_WORKFLOW" || die "tarball member lacks workflow upload path: $member" ;;
    tbound-cell-entrypoint-*-linux-amd64.metadata.json) grep -Fqx "            $metadata_pattern" "$NORMALIZED_WORKFLOW" || die "metadata member lacks workflow upload path: $member" ;;
    tbound-cell-entrypoint-*-linux-amd64.sha256) grep -Fqx "            $checksum_pattern" "$NORMALIZED_WORKFLOW" || die "checksum member lacks workflow upload path: $member" ;;
    *) die "uncovered checksum member: $member" ;;
  esac
done < "$FIXTURE"
pass "sample checksum members all match workflow release asset patterns"

echo "verify-release-workflow-coverage: 3 passed, 0 failed"
