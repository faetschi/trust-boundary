#!/bin/bash
# F30 extra checks the local gate lacks (CI commands from .github/workflows on the integration), inside the Linux-line lock.
set -uo pipefail
S=/mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/5885a9d4-6837-49d0-8bb9-191b48098c96/scratchpad/f30w
T=/home/tbound/tbound-work
R=$T/x30
L=$T/x30-logs
WANT_HEAD=f9cbf14afa70a5eef4e4543ea3a68ab378df9210
WANT_TREE=a458f0268eaeaa3df2ec8fe4c3eb789f96616596
for d in "$R" "$L" "$T/x30tmp"; do [ ! -e "$d" ] || { echo "FATAL: $d exists"; exit 1; }; done
source /home/tbound/env.sh
exec 9>/home/tbound/.linux-line.lock
flock -w 7200 9 || { echo "FATAL: lock"; exit 1; }
echo "lock taken $(date -u +%FT%TZ)"
cp -- "$S/f30-integ.bundle" "$T/f30-integ.bundle"
git -c core.autocrlf=false init -q "$R"
git -C "$R" -c core.autocrlf=false fetch -q "$T/f30-integ.bundle" refs/heads/integ/f30-20261118:refs/heads/integ/f30-20261118
git -C "$R" -c core.autocrlf=false checkout -q integ/f30-20261118
git -C "$R" config core.autocrlf false
[ "$(git -C "$R" rev-parse HEAD)" = $WANT_HEAD ] && [ "$(git -C "$R" rev-parse HEAD^{tree})" = $WANT_TREE ] || { echo "FATAL: head/tree"; exit 1; }
[ -z "$(git -C "$R" status --porcelain)" ] || { echo "FATAL: dirty checkout"; exit 1; }
echo "checkout $WANT_HEAD tree $WANT_TREE clean"
mkdir -m 700 "$L" "$T/x30tmp"
export TMPDIR=$T/x30tmp
cd "$R"
fails=0
chk() { local name=$1; shift; local rc=0
  ( umask 077; nice -n 19 "$@" ) > "$L/$name.log" 2>&1 || rc=$?
  echo "CHECK $name rc=$rc $(tail -n 1 "$L/$name.log" | cut -c1-150)"
  [ $rc -eq 0 ] || fails=$((fails+1)); }
chk bootstrap-package-status bash infra/repro/test_bootstrap_package_status.sh
chk bootstrap-userns-optin bash infra/repro/test_bootstrap_userns_optin.sh
chk hyperv-netplan python3 -I infra/repro/test_hyperv_netplan_injection.py -v
chk infra-guest-py python3 -I -m unittest discover -s infra/guest -p 'test_*.py' -v
chk cgroup-witness-py python3 -I -m unittest discover -s infra/repro -p 'test_cgroup_teardown_witness.py' -v
chk pi-runtime-closure-py python3 -I -m unittest discover -s scripts -p 'test_prepare_official_pi_runtime.py' -v
chk go-timing-py python3 -I -m unittest discover -s scripts -p 'test_ci_go_test_timing.py' -v
chk corpus-sampling-py python3 -I -m unittest discover -s corpus/sampling -p 'test_*.py' -v
chk pkg-manager-layout bash tests/packaging/verify-package-manager-layout.sh
chk publish-release-assets bash tests/packaging/verify-publish-release-assets.sh
chk release-workflow-coverage bash tests/packaging/verify-release-workflow-coverage.sh 1.2.3
chk runtime-paths bash tests/ci/test-runtime-paths.sh
chk runtime-status bash tests/ci/test-runtime-status.sh
chk doc-links python3 -I scripts/check-doc-links.py
chk containerfile-pins bash tests/packaging/verify-governed-containerfile-pins.sh
chk extension-boundary sh scripts/check-extension-boundary.sh
chk extension-boundary-selftest sh scripts/check-extension-boundary.sh --self-test
chk self-contained-selftest bash scripts/check-self-contained.sh --self-test
chk actionlint go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 -no-color -shellcheck= -pyflakes=
chk adapter-npm-ci bash -c 'cd adapter && npm ci --ignore-scripts --no-audit --no-fund'
chk adapter-npm-check bash -c 'cd adapter && npm run check'
# release.yml verification (build into a private dist, then the release steps in order)
V=0.0.0-f30check
D=$T/x30tmp/dist
chk release-build bash scripts/build-release.sh "$V" "$D" linux/amd64 darwin/amd64 darwin/arm64 windows/amd64
chk release-npm-packages bash scripts/make-npm-packages.sh "$V" "$D"
chk release-verify-install env TBOUND_VERIFY_DIST="$D" bash scripts/verify-install.sh "$V"
chk release-strict-mode bash tests/packaging/verify-release-strict-mode.sh
chk release-layout env TBOUND_REQUIRE_LINUX_RELEASE=1 TBOUND_RELEASE_DIST="$D" bash tests/packaging/verify-release-layout.sh "$V" --require-linux
chk release-npm-verify env TBOUND_NPM_DIST="$D" bash tests/packaging/verify-npm-packages.sh "$V"
[ -z "$(git -C "$R" status --porcelain)" ] && echo "checkout still clean" || { echo "checkout changed:"; git -C "$R" status --porcelain | head; }
echo "FAILS=$fails end $(date -u +%FT%TZ)"
