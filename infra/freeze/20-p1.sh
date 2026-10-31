#!/bin/bash
# P1 [READ-ONLY] build the binaries from $FREEZE twice, byte-compare, compute the source digest, stage the cosign tool.
set -euo pipefail
umask 077
. "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
load_env
logto 20-p1

[ ! -e "$W/src" ] || die "P1 already ran in $W (src exists); start a new freeze"
GOV=$($GO version); case "$GOV" in *" go1.27.1 "*) ;; *) die "need Go 1.27.1, got: $GOV";; esac
export GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=file://$HOME/go/pkg/mod/cache/download GOSUMDB=off CGO_ENABLED=0
export GOMODCACHE=$W/gomod GOPATH=$W/gopath TMPDIR=$W/tmp; mkdir -p "$TMPDIR"
$GO version

for pass in 1 2; do
  if [ $pass = 1 ]; then S=$W/src; BIN=$W/bin; else S=$W/src-pass2; BIN=$W/bin-pass2; fi
  export GOCACHE=$W/gocache$pass
  mkdir -p "$S"
  git -C "$REPO" -c core.autocrlf=false -c core.eol=lf archive --format=tar "$F" supervisor scripts adapter/src adapter/package.json adapter/package-lock.json adapter/pi-runtime-package.json packaging infra corpus | tar -x -C "$S"
  mkdir -p "$BIN"
  cd "$S/supervisor"
  for c in tbound tbound-profile tbound-cell-entrypoint tbound-run; do $GO build -trimpath -o "$BIN/$c" "./cmd/$c"; done
  GOOS=windows GOARCH=amd64 $GO build -trimpath -o "$BIN/tbound-run.exe" ./cmd/tbound-run
  echo "== pass $pass"; for f in "$BIN"/*; do echo "$(D "$f")  $f"; done
done
cd "$W"

echo "== compare pass1 vs pass2"
for f in tbound tbound-profile tbound-cell-entrypoint tbound-run tbound-run.exe; do
  cmp "$W/bin/$f" "$W/bin-pass2/$f" || die "NOT IDENTICAL: $f"
  echo "IDENTICAL $f $(D "$W/bin/$f")"
done
echo "== source trees pass1 vs pass2"
diff -r "$W/src" "$W/src-pass2" || die "source trees differ"
echo "SRC TREES IDENTICAL"

# Public-value records for the receipt (file names are the receipt names).
printf '%s\n' "$(D "$W/bin/tbound")"                 > "$HF/TBOUND_DIGEST"
printf '%s\n' "$(D "$W/bin/tbound-profile")"         > "$HF/TBP_DIGEST"
printf '%s\n' "$(D "$W/bin/tbound-run")"             > "$HF/TBOUND_RUN_DIGEST"
printf '%s\n' "$(D "$W/bin/tbound-cell-entrypoint")" > "$HF/ENTRYPOINT_P1_DIGEST"
printf '%s\n' "$(D "$W/bin/tbound-run.exe")"         > "$HF/TBOUND_RUN_EXE_DIGEST"

export PATH=$W/bin:$PATH
SRC_DIGEST=$(tbound-run source-digest --repo "$REPO" --commit "$F" | tee "$W/source-digest.out" | sed -n 's/^source_digest //p')
cat "$W/source-digest.out"; echo "SRC_DIGEST=$SRC_DIGEST"
[[ $SRC_DIGEST =~ ^sha256:[0-9a-f]{64}$ ]] || die "BAD SRC_DIGEST"
echo "$SRC_DIGEST" > "$HF/SRC_DIGEST"

# cosign tool, as mkenv.sh did (used by 40-sign.sh).
cp "$COSIGN_TOOL" "$W/bin/cosign"; chmod 700 "$W/bin/cosign"
COSIGN_TOOL_DIGEST=$(D "$W/bin/cosign")
echo "$COSIGN_TOOL_DIGEST" > "$HF/COSIGN_TOOL_DIGEST"
echo "cosign tool: $COSIGN_TOOL_DIGEST  (F0 receipt P10 shows guest cosign hash starting 4629c757)"
case "$COSIGN_TOOL_DIGEST" in sha256:4629c757*) ;; *) echo "WARNING: cosign tool digest does not start with 4629c757 (the guest cosign of F0). Check before signing." ;; esac

find "$HF" -maxdepth 1 -type f -exec chmod 600 {} +
finish_step "$W/bin" "$W/bin-pass2" "$W/source-digest.out"
echo "== DONE 20-p1"
for f in TBOUND_DIGEST TBP_DIGEST TBOUND_RUN_DIGEST ENTRYPOINT_P1_DIGEST TBOUND_RUN_EXE_DIGEST SRC_DIGEST; do echo "$f=$(cat "$HF/$f")"; done
echo "== comparison with $PREV_FREEZE (informational; the guest scripts install whatever differs)"
note_prev TBOUND_DIGEST "$(cat "$HF/TBOUND_DIGEST")" "$PREV_TBOUND_DIGEST"
note_prev TBP_DIGEST "$(cat "$HF/TBP_DIGEST")" "$PREV_TBP_DIGEST"
note_prev ENTRYPOINT_P1_DIGEST "$(cat "$HF/ENTRYPOINT_P1_DIGEST")" "$PREV_ENTRYPOINT_DIGEST"
note_prev SRC_DIGEST "$SRC_DIGEST" "$PREV_SRC_DIGEST"
