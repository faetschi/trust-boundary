#!/bin/bash
# Clean-room full gate: gate.sh <archive.tar> <label>. Results in ~/gates/<label>/ (persistent, not /tmp).
umask 077
case $(uname -s) in MINGW* | MSYS*) echo "FATAL: run gate.sh inside WSL or on Linux, not from Git Bash" >&2; exit 2 ;; esac
. "$(dirname -- "${BASH_SOURCE[0]}")/../freeze-guard.sh" || exit 2
. "$(dirname -- "${BASH_SOURCE[0]}")/../paths.sh"
A=${1:?usage: gate.sh <archive.tar> <label>}; L=${2:?usage: gate.sh <archive.tar> <label>}
[[ $L =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || { echo "FATAL: bad gate label: $L" >&2; exit 2; }
G=$HOME/gates/$L
not_mnt "$G" || { echo "FATAL: $G resolves into /mnt" >&2; exit 2; }
rm -rf "${G:?}"; mkdir -p "$G/t"; export TMPDIR=$G/t
tar -C "$G" -xf "$A"
cd "$G/supervisor" || exit 2
unset GOCACHE GOPATH GOMODCACHE GOENV GOROOT   # defaults sit under the validated HOME
export PATH=$(dirname -- "$GO"):$PATH GOFLAGS=-mod=readonly
echo "boot $(cat /proc/sys/kernel/random/boot_id) $(date -u +%FT%TZ)"
go version
fail() { echo "GATE_FAILED: $1"; exit 1; }
go build ./... && echo BUILD_OK || fail build
go vet ./... && echo VET_OK || fail vet
GOOS=windows go build ./... && GOOS=windows go vet ./... && echo WINDOWS_OK || fail windows
[ -z "$(gofmt -l .)" ] && echo FMT_OK || fail gofmt
(cd "$G" && sh scripts/check-extension-boundary.sh >/dev/null) && echo BOUNDARY_OK || fail boundary
go test -race -count=1 -timeout 40m -json ./... > "$G/test.json" 2> "$G/test.err"; TEST_RC=$?; echo "TEST_RC=$TEST_RC"
echo "boot $(cat /proc/sys/kernel/random/boot_id) $(date -u +%FT%TZ)"
python3 -I - "$G/test.json" <<'PY'
import json, sys, collections
c = collections.Counter(); pk = collections.Counter()
for l in open(sys.argv[1]):
    try: e = json.loads(l)
    except ValueError: continue
    a = e.get("Action")
    if a not in ("pass", "fail", "skip"): continue
    if e.get("Test"): c[a] += 1
    else: pk[a] += 1
    if a == "fail" and e.get("Test") and "/" not in e["Test"]: print("FAIL", e["Package"], e["Test"])
print("tests", dict(c), "packages", dict(pk))
PY
exit "$TEST_RC"
