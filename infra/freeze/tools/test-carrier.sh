#!/bin/bash
# test-carrier.sh: local test of the screening-round carrier input of guest/33-corpus-pilot.sh (phase 0) and guest/carrier-check.sh. No guest contact.
#   bash test-carrier.sh                 carrier-check.sh, the CARRIER_* parsing and phase_carrier of 33 (the real code, cut out of the script, with local
#                                        stand-ins for WX/WB and the $HF store), first_compliant_case with a ledger id that holds ':'
#   bash test-carrier.sh <W> <HF>        also the phase-2 dry run with the freeze's tools: a synthetic carrier (a copy of the T5 draft) + $HF/pilot/pins.json
#                                        must give $HF/pilot/manifest.json byte for byte, corpus-pins must accept it and corpus-digest must print
#                                        $HF/pilot/CORPUS_DIGEST; a carrier with ':' case ids must be admitted too. Reads <W>/bin/tbound-profile and <HF>, writes neither.
# Run it in WSL or on Linux from a Linux checkout (never under /mnt). Prints PASS/FAIL per case and exits non-zero on any FAIL.
set -euo pipefail
umask 077
case $(uname -s) in MINGW* | MSYS*) echo "FATAL: run test-carrier.sh inside WSL or on Linux, not from Git Bash" >&2; exit 2 ;; esac
K=$(cd -P "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)      # the kit (infra/freeze)
ROOT=$(cd -P "$K/../.." && pwd)
case $ROOT in /mnt | /mnt/*) echo "FATAL: the checkout is under /mnt" >&2; exit 2 ;; esac
T5=$ROOT/corpus/pilot/t5-2026-10-29
CC=$K/guest/carrier-check.sh
P33=$K/guest/33-corpus-pilot.sh
T=$(mktemp -d "${TMPDIR:-/tmp}/carrier-test.XXXXXX")
case $T in /tmp/carrier-test.* | "${TMPDIR:-/tmp}"/carrier-test.*) ;; *) echo "FATAL: unexpected temp dir '$T'" >&2; exit 2 ;; esac
trap 'rm -rf -- "$T"' EXIT
FAILS=0
pass() { printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1"; FAILS=$((FAILS + 1)); }
# expect_ok <label> <cmd...> / expect_refuse <label> <stderr substring> <cmd...>
expect_ok() { local l=$1; shift; if out=$("$@" 2>&1); then pass "$l"; else fail "$l: $out"; fi; }
expect_refuse() {
  local l=$1 want=$2; shift 2
  if out=$("$@" 2>&1); then fail "$l: accepted"; elif [[ $out == *"$want"* ]]; then pass "$l"; else fail "$l: refused for another reason: $out"; fi
}
sha() { printf 'sha256:%s' "$(sha256sum "$1" | cut -c1-64)"; }

# ---- a checkout with two carriers: synthetic-01 (the T5 draft and artifacts as they are) and colon-01 (two case ids in the ledger form with ':')
R=$T/repo
mkcarrier() { # <round>: the T5 draft + artifacts and the lane C side files
  local d=$R/corpus/sampling/carriers/$1
  mkdir -p "$d/licenses"
  cp "$T5/manifest.draft.json" "$d/"; cp -R "$T5/artifacts" "$d/"
  printf '{"sources": []}\n' >"$d/sources.json"; printf '# notices\n' >"$d/THIRD_PARTY_NOTICES.md"; printf 'license text\n' >"$d/licenses/LICENSE-test.txt"
}
mkdir -p "$R"
mkcarrier synthetic-01
mkcarrier colon-01
python3 -I - "$R/corpus/sampling/carriers/colon-01/manifest.draft.json" <<'PY'
import json, sys
p = sys.argv[1]
m = json.load(open(p, encoding="utf-8"))
done = set()
for c in m["cases"]:
    if c["kind"] in ("compliant", "adversarial") and c["kind"] not in done:
        c["case_id"] = "agentdojo:slack:UserTask0:" + c["case_id"]; done.add(c["kind"])
open(p, "w", encoding="utf-8").write(json.dumps(m, indent=2) + "\n")
PY
git -C "$R" init -q
git -C "$R" -c core.autocrlf=false add -A
git -C "$R" -c user.name=test -c user.email=test@invalid -c commit.gpgsign=false commit -q -m carriers
C=$(git -C "$R" rev-parse HEAD)
S=$R/corpus/sampling/carriers/synthetic-01
D=$(sha "$S/manifest.draft.json")
COLON=$R/corpus/sampling/carriers/colon-01
DCOLON=$(sha "$COLON/manifest.draft.json")
copy() { local c=$T/copy-$1; cp -R "$S" "$c"; printf '%s' "$c"; }

say() { printf '\n== %s\n' "$*"; }
say "carrier-check.sh"
expect_ok "checkout at its commit" bash "$CC" "$R" "$C" synthetic-01 "$S" "$D" --checkout
expect_ok "a copy outside the checkout" bash "$CC" "$R" "$C" synthetic-01 "$(copy plain)" "$D"
c=$(copy crlf); f=$(cd "$c/artifacts" && ls | head -1); sed -i 's/$/\r/' "$c/artifacts/$f"
expect_refuse "CRLF drift in one artifact" "file bytes differ" bash "$CC" "$R" "$C" synthetic-01 "$c" "$D"
c=$(copy extra); printf 'x\n' >"$c/artifacts/extra.json"
expect_refuse "an extra file" "the files differ" bash "$CC" "$R" "$C" synthetic-01 "$c" "$D"
c=$(copy missing); rm -f -- "$c/sources.json"
expect_refuse "a missing file" "the files differ" bash "$CC" "$R" "$C" synthetic-01 "$c" "$D"
c=$(copy link); rm -f -- "$c/sources.json"; ln -s "$S/sources.json" "$c/sources.json"
expect_refuse "a symlink in place of a file" "neither a directory nor a regular file" bash "$CC" "$R" "$C" synthetic-01 "$c" "$D"
c=$(copy empty); : >"$c/THIRD_PARTY_NOTICES.md"
expect_refuse "a 0-byte file" "0-byte files" bash "$CC" "$R" "$C" synthetic-01 "$c" "$D"
expect_refuse "another manifest digest" "sha256(manifest.draft.json)" bash "$CC" "$R" "$C" synthetic-01 "$S" "sha256:$(printf '0%.0s' {1..64})" --checkout
expect_refuse "a round the commit does not hold" "is not a tree" bash "$CC" "$R" "$C" rehearsal-dev-01 "$S" "$D"
ln -s "$R" "$T/repo-link"
expect_refuse "the checkout reached through a symlink" "symlink is on the way" bash "$CC" "$R" "$C" synthetic-01 "$T/repo-link/corpus/sampling/carriers/synthetic-01" "$D" --checkout
printf ' ' >>"$S/sources.json"
expect_refuse "an uncommitted edit in the checkout" "file bytes differ" bash "$CC" "$R" "$C" synthetic-01 "$S" "$D" --checkout
git -C "$R" checkout -q -- corpus
git -C "$R" -c user.name=test -c user.email=test@invalid -c commit.gpgsign=false commit -q --allow-empty -m later
expect_refuse "checkout HEAD moved past the commit" "checkout HEAD" bash "$CC" "$R" "$C" synthetic-01 "$S" "$D" --checkout
git -C "$R" checkout -q "$C"

# ---- phase 0 of 33, cut out of the script, with local stand-ins for the transports and the $HF store (glib.sh's hf_* bodies)
say "33-corpus-pilot.sh phase 0 (CARRIER_* parsing, phase_carrier) and first_compliant_case"
HARNESS=$T/harness.sh
{
  echo 'set -euo pipefail'
  grep -E '^(linux_safe|not_mnt)\(\)' "$K/paths.sh"
  echo 'say() { printf "\n== %s\n" "$*"; }; ok() { printf "OK   %s\n" "$*"; }; note() { printf "NOTE %s\n" "$*"; }; die() { printf "FATAL: %s\n" "$*" >&2; exit 1; }'
  echo 'WXP=(); WX() { "$@" </dev/null; }; WB() { bash -c "$1" </dev/null; }; WBI() { bash -c "$1"; }'
  sed -n '/^isdig()/p;/^need_dig()/p;/^hf_has()/p;/^hf_get()/p;/^hf_put()/,/^}/p' "$K/guest/glib.sh"
  echo 'HF=$1; HG=$HF/pilot; WGA=$2; T5_DIR=/nonexistent/t5; ART=$T5_DIR/artifacts; T5_ARTIFACT_COUNT=76'
  sed -n '/^CARRIER_ROUND="" CARRIER_REC=""$/,/^fi$/p' "$P33"
  sed -n '/^aside_cmd()/p;/^carrier_check()/p;/^phase_carrier()/,/^}/p;/^first_compliant_case()/,/^}/p' "$P33"
  echo 'phase_carrier; echo "T5_DIR=$T5_DIR T5_ARTIFACT_COUNT=$T5_ARTIFACT_COUNT"'
  echo '[ -z "${FIRST_CASE_OF:-}" ] || echo "first compliant case: $(first_compliant_case "$FIRST_CASE_OF")"'
} >"$HARNESS"
grep -q '^phase_carrier() {' "$HARNESS" && grep -q '^CARRIER_ROUND=""' "$HARNESS" && grep -q '^hf_put() {' "$HARNESS" || { fail "the harness could not cut phase 0 out of 33/glib"; exit 1; }
HF1=$T/hf1; mkdir -m 0700 "$HF1"
h() { env "$@" bash "$HARNESS" "$HF1" "$K/guest"; }
expect_ok "no carrier, empty \$HF: nothing happens" h
[ ! -e "$HF1/pilot" ] && pass "no carrier: \$HF/pilot is not created" || fail "no carrier: \$HF/pilot was created"
expect_refuse "only two of the three CARRIER_* values" "go together" h CARRIER_DIR="$S" CARRIER_COMMIT="$C"
expect_refuse "CARRIER_DIR under /mnt" "outside /mnt" h CARRIER_DIR=/mnt/c/x/corpus/sampling/carriers/synthetic-01 CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$D"
expect_refuse "CARRIER_DIR not a carriers/<round> path" "not a plain Linux path" h CARRIER_DIR="$R/corpus/pilot" CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$D"
expect_refuse "CARRIER_DIR through a symlink" "passes a symlink" h CARRIER_DIR="$T/repo-link/corpus/sampling/carriers/synthetic-01" CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$D"
expect_refuse "a wrong digest writes nothing" "failed verification" h CARRIER_DIR="$S" CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$DCOLON"
[ ! -e "$HF1/pilot" ] && pass "refused carrier: \$HF/pilot is not created" || fail "refused carrier: \$HF/pilot was created"
out=$(h CARRIER_DIR="$S" CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$D" 2>&1) && [[ $out == *"T5_DIR=$HF1/pilot/carrier T5_ARTIFACT_COUNT=76"* ]] &&
  [ "$(cat "$HF1/pilot/CARRIER")" = "synthetic-01 $C $D" ] && bash "$CC" "$R" "$C" synthetic-01 "$HF1/pilot/carrier" "$D" >/dev/null &&
  pass "synthetic-01: verified, copied to \$HF/pilot/carrier (76 artifacts), recorded" || fail "synthetic-01 first run: $out"
expect_ok "synthetic-01 again: resumes on the verified copy" h CARRIER_DIR="$S" CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$D"
expect_refuse "another round on the same \$HF/pilot" "holds the carrier round 'synthetic-01" h CARRIER_DIR="$COLON" CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$DCOLON"
expect_refuse "no carrier on a carrier \$HF/pilot (the T5 run)" "no carrier is set" h
printf 'x' >>"$HF1/pilot/carrier/sources.json"
expect_refuse "an edited copy in \$HF/pilot/carrier" "failed verification: owner looks" h CARRIER_DIR="$S" CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$D"
HF2=$T/hf2; mkdir -p -m 0700 "$HF2/pilot"; printf 'sha256:x\n' >"$HF2/pilot/CORPUS_DIGEST"
expect_refuse "a T5 \$HF/pilot (no CARRIER record) under a carrier" "without a carrier record" env CARRIER_DIR="$S" CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$D" bash "$HARNESS" "$HF2" "$K/guest"
HF3=$T/hf3; mkdir -m 0700 "$HF3"
out=$(env CARRIER_DIR="$COLON" CARRIER_COMMIT="$C" CARRIER_MANIFEST_DIGEST="$DCOLON" FIRST_CASE_OF="$COLON/manifest.draft.json" bash "$HARNESS" "$HF3" "$K/guest" 2>&1) &&
  [[ $out == *"first compliant case: agentdojo:slack:UserTask0:"* ]] && pass "colon-01: verified; first_compliant_case returns the ':' id" || fail "colon-01: $out"

# ---- phase-2 dry run with the freeze's own tools and records
if [ $# -eq 2 ]; then
  W=$1 HF=$2; TBP=$W/bin/tbound-profile
  say "phase-2 dry run: $TBP, records of $HF"
  for f in "$TBP" "$HF/pilot/pins.json" "$HF/pilot/manifest.json" "$HF/pilot/CORPUS_DIGEST" "$HF/d09-cell.base.json" "$HF/BUNDLE_DIGEST" "$HF/NODE_DIGEST"; do [ -e "$f" ] || { fail "missing $f"; exit 1; }; done
  NODE=${NODE_BIN:-$(command -v node || true)}
  [ -n "$NODE" ] && [ "$(sha "$NODE")" = "$(cat "$HF/NODE_DIGEST")" ] || { fail "node '$NODE' is not the pinned NODE_DIGEST (set NODE_BIN)"; exit 1; }
  pin() { # <draft> <out>: phase 2's pin fill (the four pins replaced, everything else kept)
    python3 -I -c 'import json,sys
m=json.load(open(sys.argv[1],encoding="utf-8"))
p=json.load(open(sys.argv[2],encoding="utf-8"))
for k in ("environment_digest","model_digest","profile_digest","policy_digest"):
    m["pins"][k]=p[k]
print(json.dumps(m,indent=2))' "$1" "$HF/pilot/pins.json" >"$2"
  }
  pins_args=(--template "$HF/d09-cell.base.json" --runtime-bundle-digest "$(cat "$HF/BUNDLE_DIGEST")" --node-digest "$(cat "$HF/NODE_DIGEST")")
  ceil=(--quota-profile pilot --node "$NODE" --max-host-calls 17 --max-tokens 69632 --duration-seconds 1200)
  pin "$HF1/pilot/carrier/manifest.draft.json" "$T/m-synthetic.json"
  cmp -s "$T/m-synthetic.json" "$HF/pilot/manifest.json" && pass "synthetic carrier + pins == \$HF/pilot/manifest.json byte for byte" || fail "synthetic carrier manifest differs from $HF/pilot/manifest.json"
  [ "$("$TBP" corpus-pins "${pins_args[@]}" --manifest "$T/m-synthetic.json")" = "$(cat "$HF/pilot/pins.json")" ] && pass "corpus-pins --manifest accepts it" || fail "corpus-pins --manifest"
  got=$("$TBP" corpus-digest --manifest "$T/m-synthetic.json" --artifacts "$HF1/pilot/carrier/artifacts" "${ceil[@]}" 2>&1) || true
  [ "$got" = "$(cat "$HF/pilot/CORPUS_DIGEST")" ] && pass "corpus-digest of the synthetic carrier == the freeze's CORPUS_DIGEST $got" || fail "corpus-digest: $got"
  pin "$COLON/manifest.draft.json" "$T/m-colon.json"
  got=$("$TBP" corpus-digest --manifest "$T/m-colon.json" --artifacts "$COLON/artifacts" "${ceil[@]}" 2>&1) || true
  [[ $got =~ ^sha256:[0-9a-f]{64}$ && $got != "$(cat "$HF/pilot/CORPUS_DIGEST")" ]] && pass "corpus-digest admits ':' case ids ($got)" || fail "corpus-digest with ':' case ids: $got"
fi

echo
[ "$FAILS" = 0 ] && echo "ALL PASS" || { echo "$FAILS FAIL"; exit 1; }
