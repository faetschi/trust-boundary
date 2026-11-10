#!/bin/bash
# EVQ capture of $FREEZE [READ-ONLY on the repo; writes only the NEW dir $CF]: the Linux test run of the frozen commit, like the $PREV_FREEZE run
# ($CF). Needs 10, 20 (tbound-run). Takes a while (go test -race ./...). Run on Linux (in WSL from Windows): bash 35-evq-capture.sh
# Results: $CF/capture.json (archive_tree_digest must equal SRC_DIGEST, exit_status 0), go-test.json, guest-tests/{procoracle,tbound-cell-entrypoint}.test,
# and the two test_binaries digests = TEST_PROCORACLE / TEST_ENTRYPOINT for the $FREEZE pins.env (guest/10-upgrade.sh reads them from capture.json).
set -euo pipefail
umask 077
. "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
load_env
logto 35-evq-capture

[ ! -e "$CF" ] || die "$CF exists (evq-capture refuses to overwrite; remove it deliberately to capture again)"
[ -x "$W/bin/tbound-run" ] || die "run 20-p1.sh first"
[ "$(D "$W/bin/tbound-run")" = "$(cat "$HF/TBOUND_RUN_DIGEST")" ] || die "tbound-run changed since 20-p1"
# Same environment shape as $PREV_FREEZE: nothing of the caller's Go configuration (the tool records GOPROXY=off, default module cache under HOME).
unset GOPROXY GOFLAGS GOMODCACHE GOPATH GOCACHE GOTOOLCHAIN GOSUMDB GOOS GOARCH CGO_ENABLED || true
git_env
"$W/bin/tbound-run" evq-capture --repo "$REPO" --commit "$F" --go "$GO" --out "$CF"
echo "exit=$?"
finish_step "$CF/capture.json" "$CF/go-test.json" "$CF/guest-tests/procoracle.test" "$CF/guest-tests/tbound-cell-entrypoint.test"

[ -f "$CF/capture.json" ] || die "no capture.json"
cap() { python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); v=d
for k in sys.argv[2].split("."): v=v[k]
print(v)' "$CF/capture.json" "$1"; }
expect_same "capture commit" "$(cap commit)" "$F"
expect_same "capture archive_tree_digest == SRC_DIGEST" "$(cap archive_tree_digest)" "$(cat "$HF/SRC_DIGEST")"
expect_same "capture exit_status" "$(cap exit_status)" 0
TP=$(cap test_binaries.tbound/supervisor/internal/procoracle) ; TE=$(cap test_binaries.tbound/supervisor/cmd/tbound-cell-entrypoint)
for v in TP TE; do [[ ${!v} =~ ^sha256:[0-9a-f]{64}$ ]] || die "bad $v: ${!v}"; done
expect_same "guest-tests/procoracle.test sha256 == test_binaries" "$(D "$CF/guest-tests/procoracle.test")" "$TP"
expect_same "guest-tests/tbound-cell-entrypoint.test sha256 == test_binaries" "$(D "$CF/guest-tests/tbound-cell-entrypoint.test")" "$TE"
[ ! -e "$HF/TEST_PROCORACLE" ] && [ ! -e "$HF/TEST_ENTRYPOINT" ] || die "TEST_* records already exist in $HF"
printf '%s\n' "$TP" > "$HF/TEST_PROCORACLE"
printf '%s\n' "$TE" > "$HF/TEST_ENTRYPOINT"
printf '%s\n' "$(D "$CF/capture.json")" > "$HF/EVQ_CAPTURE_FILE_DIGEST"
chmod 600 "$HF/TEST_PROCORACLE" "$HF/TEST_ENTRYPOINT" "$HF/EVQ_CAPTURE_FILE_DIGEST"
note_prev TEST_PROCORACLE "$TP" "$PREV_TEST_PROCORACLE"
note_prev TEST_ENTRYPOINT "$TE" "$PREV_TEST_ENTRYPOINT"
echo "capture_sha256 file digest: $(cat "$HF/EVQ_CAPTURE_FILE_DIGEST")"
finish_step "$CF/capture.json" "$CF/go-test.json" "$CF/guest-tests/procoracle.test" "$CF/guest-tests/tbound-cell-entrypoint.test"
echo "== DONE 35-evq-capture: $CF"
