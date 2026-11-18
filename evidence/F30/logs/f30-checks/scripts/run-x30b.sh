#!/bin/bash
# F30 extra checks, release.yml chain again with the workflow's own non-tag version 0.0.0 (x30 used 0.0.0-f30check, which build-rpm rejects by design).
set -uo pipefail
T=/home/tbound/tbound-work; R=$T/x30; L=$T/x30-logs
source /home/tbound/env.sh
exec 9>/home/tbound/.linux-line.lock; flock -w 7200 9 || exit 1
[ "$(git -C $R rev-parse HEAD^{tree})" = a458f0268eaeaa3df2ec8fe4c3eb789f96616596 ] && [ -z "$(git -C $R status --porcelain)" ] || { echo FATAL checkout; exit 1; }
[ ! -e $T/x30tmp/dist0 ] || { echo FATAL dist0 exists; exit 1; }
export TMPDIR=$T/x30tmp; cd $R; fails=0
chk() { local name=$1; shift; local rc=0; ( umask 077; nice -n 19 "$@" ) > "$L/$name.log" 2>&1 || rc=$?; echo "CHECK $name rc=$rc $(tail -n 1 "$L/$name.log" | cut -c1-150)"; [ $rc -eq 0 ] || fails=$((fails+1)); }
V=0.0.0; D=$T/x30tmp/dist0
chk v0-release-build bash scripts/build-release.sh "$V" "$D" linux/amd64 darwin/amd64 darwin/arm64 windows/amd64
chk v0-release-npm-packages bash scripts/make-npm-packages.sh "$V" "$D"
chk v0-release-verify-install env TBOUND_VERIFY_DIST="$D" bash scripts/verify-install.sh "$V"
chk v0-release-strict-mode bash tests/packaging/verify-release-strict-mode.sh
chk v0-release-layout env TBOUND_REQUIRE_LINUX_RELEASE=1 TBOUND_RELEASE_DIST="$D" bash tests/packaging/verify-release-layout.sh "$V" --require-linux
chk v0-release-npm-verify env TBOUND_NPM_DIST="$D" bash tests/packaging/verify-npm-packages.sh "$V"
[ -z "$(git -C $R status --porcelain)" ] && echo "checkout still clean"
echo "FAILS=$fails"
