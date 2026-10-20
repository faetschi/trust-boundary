#!/usr/bin/env bash
# Mock test for the explicit cgroup-reuse qualification wiring. It does not
# create cgroups or run the real probe; --no-scope is test-only.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HELPER="$ROOT/run-cgroup-reuse-check.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-cq-mock.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
mkdir -m 700 "$WORK/bin"

cat > "$WORK/bin/tbound-profile" <<'SH'
#!/usr/bin/env bash
case "${1:-}" in
  cgroup-reuse-check)
    case "${2:-}" in
      --help) exit 0 ;;
      --confirm-disposable-cgroup)
        printf '%s\n' '{"result":"incompatible","advisory":true,"error":"mock kernel mismatch","observations":[],"reuse_populated":false}'
        exit 2
        ;;
    esac
    ;;
esac
exit 2
SH
chmod 700 "$WORK/bin/tbound-profile"

set +e
out="$("$HELPER" --tbound-profile "$WORK/bin/tbound-profile" --out "$WORK/out" --no-scope 2>"$WORK/stderr")"
rc=$?
set -e
[[ "$rc" -eq 2 ]] || { echo "FAIL: probe exit $rc, want 2" >&2; exit 1; }
[[ -f "$WORK/out/result.json" ]] || { echo "FAIL: result.json missing" >&2; exit 1; }
[[ "$(cat "$WORK/out/rc")" == "2" ]] || { echo "FAIL: rc file mismatch" >&2; exit 1; }
[[ "$(cat "$WORK/out/probe-rc.txt")" == "2" ]] || { echo "FAIL: probe-rc mismatch" >&2; exit 1; }
[[ "$(cat "$WORK/out/scope-rc.txt")" == "0" ]] || { echo "FAIL: scope-rc mismatch" >&2; exit 1; }
[[ -s "$WORK/out/identity.txt" ]] || { echo "FAIL: identity.txt missing" >&2; exit 1; }
[[ -s "$WORK/out/argv.txt" ]] || { echo "FAIL: argv.txt missing" >&2; exit 1; }
grep -q '"result":"incompatible"' <<<"$out" || { echo "FAIL: stdout JSON missing" >&2; exit 1; }
[[ -s "$WORK/out/setup-error.txt" ]] && { echo "FAIL: unexpected setup error" >&2; exit 1; }
printf 'cgroup-reuse wiring mock tests: PASS\n'
