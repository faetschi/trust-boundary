#!/bin/bash
# 20 Row 1 at $FREEZE: xattr calibrations S1 (read-only source) and S2 (managed live root). Needs the session sudo drop-in and 10-upgrade.sh.
#
# DEFAULT = verify and reuse (no guest write, nothing signed). The calibrations measure the kernel/filesystem behaviour of the runtime mount
# and the live root; they bind no frozen-commit value (no runner, source or image digest), and $FREEZE changes no xattr code. The pair
# installed in /etc/tbound (signed at F1, reused unchanged at F2, F4, F5, F6, F7, F9 and $PREV_FREEZE) is therefore still the right pair. This mode: runtime and live-root measures in the service context (read-only),
# the four installed files equal the $PREV_FREEZE files, they verify under the $FREEZE tbound-profile (owner side and guest side) and the guest anchor,
# their digests equal CAL_RO_DIGEST / CAL_RW_DIGEST of $PREV_FREEZE; the $PREV_FREEZE files are COPIED (read-only on $PREV_FREEZE) into $HF so $HF is a complete record
# (R1-MODE = reused-from-$PREV_LC).
#
# R1_REMEASURE=1 = the full F4 procedure, new probes and new S1/S2 signatures into $HF (R1-1 .. R1-8 of 20-r1.sh at F4), with the service and
# socket stopped (the socket is started again at exit). At $FREEZE the live root is /var/lib/tbound/live/root (the mount root is only the same-mount
# staging parent; lost+found stays there). This mode needs that directory to exist (tbound-op seed-live creates it; 10, 20 and 50 do not) and
# accepts only the smoke fixture (SMOKE-README.txt) or nothing; after 30 has seeded the g1 fixture (src/, out/) it stops. WARNING: R1-5 creates and removes a probe directory in the live root, which changes the live root directory mtime and
# gives CAL_RW_DIGEST a new probe corpus: the installed registration (binds the RW calibration) is void afterwards, and the workflow state
# of the earlier smoke (completed, to be settled by the $FREEZE service) was recorded against the live root. Do this only on purpose.
#
# Usage: bash 20-r1.sh                      (verify and reuse)
#        R1_REMEASURE=1 bash 20-r1.sh       (full re-measure and re-sign)
set -euo pipefail
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 20-r1
load_env
check_key_meta
preflight_guest

CALS="d09-xattr-enumeration-calibration-readonly:CAL_RO_DIGEST d09-xattr-enumeration-calibration:CAL_RW_DIGEST"
if [ "${R1_REMEASURE:-0}" != 1 ]; then
  say "R1 verify-and-reuse (default; R1_REMEASURE=1 for the full procedure)"
  eq "guest tbound-profile is the $FREEZE build" "$(gsha "$TBPG")" "$TBP_DIGEST"
  eq "guest trust anchor == ADMIN_PUB_DIGEST" "$(gsha /etc/tbound/pi-host-profile.pub)" "$ADMIN_PUB_DIGEST"
  say "read-only measures in the service context (records xattr-measure-runtime.json, XATTR_MOPT, XATTR_MAGIC)"
  r1_measure_runtime
  eq "live root fixture is canonical (no non-canonical entries)" "$(G 'sudo -n find /var/lib/tbound/live/root -xdev -mindepth 1 \( \( -type d ! -perm 0755 \) -o \( -type f ! -perm 0644 ! -perm 0755 \) -o \( ! -type d ! -type f \) -o ! -user 995 -o ! -group 986 \) 2>/dev/null | wc -l')" 0
  for n in $CALS; do
    f=${n%%:*}; rec=${n##*:}
    for ext in json ed25519; do
      want=$(wd "$PREV_HF/$f.$ext")
      eq "guest /etc/tbound/$f.$ext == $PREV_FREEZE file" "$(gsha "/etc/tbound/$f.$ext")" "$want"
      if hf_has "$f.$ext"; then eq "$HF/$f.$ext == $PREV_FREEZE file" "$(wd "$HF/$f.$ext")" "$want"; else
        WX cp -n "$PREV_HF/$f.$ext" "$HF/$f.$ext"; WX chmod 600 "$HF/$f.$ext"; eq "copied $f.$ext into HF" "$(wd "$HF/$f.$ext")" "$want"
      fi
    done
    o=$(WX "$TBP" xattr-calibration-verify --calibration "$HF/$f.json" --sig "$HF/$f.ed25519" --pub "$ADMIN_PUB" 2>&1) || die "owner verify of $f failed: $o"
    echo "$o"
    od=$(printf '%s\n' "$o" | grep -o 'sha256:[0-9a-f]\{64\}' | head -1)
    g=$(G "$TBPG xattr-calibration-verify --calibration /etc/tbound/$f.json --sig /etc/tbound/$f.ed25519 --pub /etc/tbound/pi-host-profile.pub 2>&1") || die "guest verify of $f failed: $g"
    echo "$g"
    gd=$(printf '%s\n' "$g" | grep -o 'sha256:[0-9a-f]\{64\}' | head -1)
    eq "$rec: owner ($FREEZE tbound-profile) digest == $PREV_FREEZE record" "$od" "$(prev_get $rec)"
    cv=PREV_$rec; eq "$rec: owner digest == PREV_$rec constant" "$od" "${!cv}"
    eq "$rec: guest digest == owner digest" "$gd" "$od"
    hf_put "$rec" "$od"
  done
  for u in cal-ro.unsigned.json cal-rw.unsigned.json; do
    hf_has "$u" || { WX cp -n "$PREV_HF/$u" "$HF/$u"; WX chmod 600 "$HF/$u"; }
  done
  hf_put R1-MODE "reused-from-$PREV_LC: calibrations carry no frozen-commit binding; $PREV_FREEZE files copied, installed copies verified under the $FREEZE tbound-profile"
  note "CAL_RW_DIGEST is what the G1 registration binds (reg-measure reads /etc/tbound/d09-xattr-enumeration-calibration.json)"
  done_step
  exit 0
fi

say "R1_REMEASURE=1: full procedure (service and socket stopped; probes in the live root)"
svc_quiesce
G 'sudo -n test -d /var/lib/tbound/live/root' || die "live root /var/lib/tbound/live/root does not exist: tbound-op seed-live creates it (service inactive); run seed-live smoke, or keep the default reuse mode"

PROBE_RO=0; PROBE_RW=0
probe_cleanup() {
  if [ "$PROBE_RO" = 1 ]; then
    echo "cleanup: removing the probe xattr from runtime-src"
    printf '%s\n' "import os" "p,n='/var/lib/tbound/runtime-src','user.tbound.calibration-probe'" "n in os.listxattr(p) and os.removexattr(p,n)" | GI 'sudo -n python3 -I -'
  fi
  if [ "$PROBE_RW" = 1 ]; then
    echo "cleanup: removing the probe dir from the live root"
    G 'sudo -n rm -rf /var/lib/tbound/live/root/tbound-xattr-probe-20261017'
  fi
}
cleanup_add probe_cleanup

census()      { WX cat "$CENSUS_SRC" | GI "sudo -n python3 -I - $1"; }
census_hash() { census "$1" | sha256sum | cut -d' ' -f1; }
baseline() { # name dir: record the census once; afterwards it must not change
  if hf_has "$1"; then
    eq "census of $2 == recorded $1" "$(census_hash "$2")" "$(hf_sha "$1")"
  else
    census "$2" | hf_save "$1"
    hf_commit "$1"
  fi
}

say "preconditions"
eq "guest tbound-profile is the $FREEZE build" "$(gsha "$TBPG")" "$TBP_DIGEST"
CH=$(wd "$CENSUS_SRC"); has_edges "census helper xattr-census-r9.py" "$CH" "sha256:2daf06f6" "3e4b"
eq "guest /opt/tbound/helpers/xattr-census-r9.py == owner copy" "$(gsha /opt/tbound/helpers/xattr-census-r9.py)" "$CH"
eq "lost+found (P9)" "$(G 'sudo -n stat -c "%U:%G %a" /var/lib/tbound/live/lost+found' | tr -d '\r')" "tbound-pi:tbound-pi 755"
eq "scratch $SCR" "$(G "sudo -n stat -c '%U:%G %a' $SCR" | tr -d '\r')" "tbound-pi:tbound-pi 700"
live=$(G 'sudo -n ls -A /var/lib/tbound/live/root 2>/dev/null' | tr -d '\r' | LC_ALL=C sort | tr '\n' '|')
case "$live" in
  "") ok "live root /var/lib/tbound/live/root is empty or not created yet (tbound-op seed-live creates it in 30)";;
  "SMOKE-README.txt|") note "live root holds the smoke fixture (SMOKE-README.txt): the calibration probe corpus includes it";;
  *) die "live root holds unexpected entries: $live";;
esac

say "R1-1 [READ-ONLY] baselines"
baseline census-runtime-base.jsonl /var/lib/tbound/runtime
if G 'sudo -n test -d /var/lib/tbound/live/root'; then
  baseline census-live-base.jsonl /var/lib/tbound/live/root
else
  note "live root /var/lib/tbound/live/root does not exist yet (tbound-op seed-live creates it in 30): no live census baseline in this mode"
fi
r1_measure_runtime
MOPT=$(hf_get XATTR_MOPT)

# ---------------------------------------------------------------------------------------------------------------
if hf_has cal-ro.unsigned.json; then
  ok "R1-2..R1-4/R1-6 (read-only calibration): cal-ro.unsigned.json already in HF, skipped"
else
  say "R1-2 [PRIVILEGED WRITE] probe xattr on runtime-src (negative control first)"
  WX rm -f "$HF/census-runtime-probe.jsonl"
  G "sudo -n test ! -e $SCR/cal-ro.unsigned.json" || { note "stale $SCR/cal-ro.unsigned.json from an interrupted run: removing"; G "sudo -n rm -f $SCR/cal-ro.unsigned.json"; }
  if out=$(G "sudo -n python3 -I -c \"import os;os.setxattr('/var/lib/tbound/runtime/.','user.tbound.calibration-probe',b'x')\" 2>&1"); then
    die "negative control: setxattr on the ro mount unexpectedly succeeded"
  fi
  printf '%s\n' "$out" | tail -2
  [[ $out == *'Errno 30'* ]] || die "negative control did not fail with EROFS (Errno 30)"
  ok "negative control: setxattr on /var/lib/tbound/runtime/. fails with EROFS"
  PROBE_RO=1
  G "sudo -n python3 -I -c \"import os;os.setxattr('/var/lib/tbound/runtime-src','user.tbound.calibration-probe',b'abi8-ro-calibration-20261017')\""

  say "R1-3 [READ-ONLY] read-only calibration measure (scratch write)"
  census /var/lib/tbound/runtime | hf_save census-runtime-probe.jsonl
  hf_commit census-runtime-probe.jsonl
  PCD_RO=sha256:$(hf_sha census-runtime-probe.jsonl)
  echo "probe census summary: $(WX tail -n 1 "$HF/census-runtime-probe.jsonl")"
  last=$(WX tail -n 1 "$HF/census-runtime-probe.jsonl")
  [[ $last == *'"xattr_names_by_namespace": {"user": 1}'* ]] || die "probe census summary is not {\"user\": 1} only (owner review point of R1-3)"
  ok "probe census: xattr names {\"user\": 1} only; PCD_RO=$PCD_RO"
  svc "$TBPG xattr-calibration-measure --source /var/lib/tbound/runtime --access-profile readonly-sealed-source/v1 --probe-corpus-digest $PCD_RO --out $SCR/cal-ro.unsigned.json"

  say "R1-4 [PRIVILEGED WRITE] remove the probe, prove zero"
  G "sudo -n python3 -I -c \"import os;os.removexattr('/var/lib/tbound/runtime-src','user.tbound.calibration-probe')\""
  PROBE_RO=0
  eq "census(runtime) == baseline after removal" "$(census_hash /var/lib/tbound/runtime)" "$(hf_sha census-runtime-base.jsonl)"
  if svc "$TBPG xattr-calibration-measure --source /var/lib/tbound/runtime --access-profile readonly-sealed-source/v1 --probe-corpus-digest $PCD_RO" >/dev/null 2>&1; then
    die "xattr-calibration-measure did NOT refuse on the zero-xattr runtime (runbook R1-4)"
  fi
  ok "xattr-calibration-measure refuses again on the clean runtime"

  say "R1-6 [READ-ONLY] fetch the unsigned read-only calibration"
  G "sudo -n cat $SCR/cal-ro.unsigned.json" | hf_save cal-ro.unsigned.json
  hf_commit cal-ro.unsigned.json
fi
eq "cal-ro mount_options_digest == runtime MOPT" "$(jgetw "$HF/cal-ro.unsigned.json" mount_options_digest)" "$MOPT"
eq "cal-ro source_access_profile" "$(jgetw "$HF/cal-ro.unsigned.json" source_access_profile)" readonly-sealed-source/v1
echo "cal-ro: $(jgetw "$HF/cal-ro.unsigned.json" authority) probe_entry_count=$(jgetw "$HF/cal-ro.unsigned.json" probe_entry_count) probe_xattr_name_count=$(jgetw "$HF/cal-ro.unsigned.json" probe_xattr_name_count)"

# ---------------------------------------------------------------------------------------------------------------
if hf_has cal-rw.unsigned.json; then
  ok "R1-5/R1-6 (read-write calibration): cal-rw.unsigned.json already in HF, skipped"
else
  say "R1-5 [PRIVILEGED WRITE] read-write calibration on the live root (after P9)"
  WX rm -f "$HF/census-live-probe.jsonl"
  G "sudo -n test ! -e $SCR/cal-rw.unsigned.json" || { note "stale $SCR/cal-rw.unsigned.json from an interrupted run: removing"; G "sudo -n rm -f $SCR/cal-rw.unsigned.json"; }
  G 'sudo -n test ! -e /var/lib/tbound/live/root/tbound-xattr-probe-20261017' || die "probe dir from an earlier run is still in the live root: remove it (rm -rf) and rerun"
  baseline census-live-base2.jsonl /var/lib/tbound/live/root
  eq "live census base2 == base" "$(hf_sha census-live-base2.jsonl)" "$(hf_sha census-live-base.jsonl)"
  PROBE_RW=1
  G "sudo -n sh -c 'set -e; D=/var/lib/tbound/live/root/tbound-xattr-probe-20261017; install -d -o 995 -g 986 -m 0700 \$D; install -o 995 -g 986 -m 0600 /dev/null \$D/plain.txt; python3 -I -c \"import os;os.setxattr(\\\"\$D/plain.txt\\\",\\\"user.tbound.calibration-probe\\\",b\\\"x\\\")\"'"
  census /var/lib/tbound/live/root | hf_save census-live-probe.jsonl
  hf_commit census-live-probe.jsonl
  PCD_RW=sha256:$(hf_sha census-live-probe.jsonl)
  echo "live probe census summary: $(WX tail -n 1 "$HF/census-live-probe.jsonl")"
  svc "$TBPG xattr-calibration-measure --source /var/lib/tbound/live/root --access-profile managed-live-root-rw-under-recovered-lease/v1 --probe-corpus-digest $PCD_RW --out $SCR/cal-rw.unsigned.json"
  G "sudo -n rm -rf /var/lib/tbound/live/root/tbound-xattr-probe-20261017"
  PROBE_RW=0
  eq "census(live) == baseline after removal" "$(census_hash /var/lib/tbound/live/root)" "$(hf_sha census-live-base2.jsonl)"

  say "R1-6 [READ-ONLY] fetch the unsigned read-write calibration"
  G "sudo -n cat $SCR/cal-rw.unsigned.json" | hf_save cal-rw.unsigned.json
  hf_commit cal-rw.unsigned.json
fi
eq "cal-rw source_access_profile" "$(jgetw "$HF/cal-rw.unsigned.json" source_access_profile)" managed-live-root-rw-under-recovered-lease/v1
WX sha256sum "$HF/cal-ro.unsigned.json" "$HF/cal-rw.unsigned.json"

# ---------------------------------------------------------------------------------------------------------------
say "R1-7 [OWNER SIGN] S1 (read-only source) and S2 (managed live root): admin key by path only"
sign_cal() { # outbase srcunsigned recordname
  local out=$1 src=$2 rec=$3 o sd vd
  if hf_has "$out.json"; then
    ok "$out.json already signed in HF (verify only)"
  else
    hf_has "$out.ed25519" && die "$HF/$out.ed25519 exists without $out.json: partial signing, inspect (never --force)"
    o=$(WX "$TBP" xattr-calibration-sign --calibration "$HF/$src" --key "$ADMIN_KEY" --out-sig "$HF/$out.ed25519" --out-calibration "$HF/$out.json" 2>&1) || die "sign failed: $o"
    echo "$o"
    sd=$(printf '%s\n' "$o" | digest_after Digest); need_dig "$out sign Digest" "$sd"
  fi
  o=$(WX "$TBP" xattr-calibration-verify --calibration "$HF/$out.json" --sig "$HF/$out.ed25519" --pub "$ADMIN_PUB" 2>&1) || die "verify failed: $o"
  echo "$o"
  vd=$(printf '%s\n' "$o" | grep -o 'sha256:[0-9a-f]\{64\}' | head -1); need_dig "$out verify digest" "$vd"
  [ -z "${sd:-}" ] || eq "$out: sign digest == verify digest" "$sd" "$vd"
  hf_put "$rec" "$vd"
}
sign_cal d09-xattr-enumeration-calibration-readonly cal-ro.unsigned.json CAL_RO_DIGEST
sign_cal d09-xattr-enumeration-calibration cal-rw.unsigned.json CAL_RW_DIGEST
note "CAL_RW_DIGEST is what the registration must bind (30-r2.sh checks managed_root_xattr_calibration_digest against it)"

say "R1-8 [PRIVILEGED WRITE] install the four calibration files (root:root 0644), verify on the guest"
for f in d09-xattr-enumeration-calibration.json d09-xattr-enumeration-calibration.ed25519 d09-xattr-enumeration-calibration-readonly.json d09-xattr-enumeration-calibration-readonly.ed25519; do
  want=$(wd "$HF/$f")
  if [ "$(gsha "/etc/tbound/$f")" = "$want" ]; then ok "/etc/tbound/$f already installed"; else
    WX cat "$HF/$f" | GI "sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/$f"
  fi
  eq "guest /etc/tbound/$f == owner" "$(gsha "/etc/tbound/$f")" "$want"
done
for n in d09-xattr-enumeration-calibration-readonly:CAL_RO_DIGEST d09-xattr-enumeration-calibration:CAL_RW_DIGEST; do
  f=${n%%:*}; rec=${n##*:}
  o=$(G "$TBPG xattr-calibration-verify --calibration /etc/tbound/$f.json --sig /etc/tbound/$f.ed25519 --pub /etc/tbound/pi-host-profile.pub 2>&1") || die "guest verify of $f failed: $o"
  echo "$o"
  eq "guest verify digest of $f" "$(printf '%s\n' "$o" | grep -o 'sha256:[0-9a-f]\{64\}' | head -1)" "$(hf_get "$rec")"
done
done_step
