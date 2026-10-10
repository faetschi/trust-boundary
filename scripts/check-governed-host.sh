#!/usr/bin/env bash
# Read-only governed-host readiness check for the declared Linux evaluation
# profile. It inspects the machine and prints exactly what is present and what an
# operator must provision before `tbound serve --pi` can be admitted. It changes
# nothing, needs no sudo, and never installs anything.
#
# Exit 0 if every governed requirement is present, 1 otherwise.
set -uo pipefail

pass=0; warn=0; fail=0
ok()   { printf '  [PASS] %-22s %s\n' "$1" "$2"; pass=$((pass+1)); }
warnf(){ printf '  [WARN] %-22s %s\n' "$1" "$2"; warn=$((warn+1)); }
bad()  { printf '  [FAIL] %-22s %s\n' "$1" "$2"; fail=$((fail+1)); }
have() { command -v "$1" >/dev/null 2>&1; }

echo "tbound governed-host readiness (read-only, ADVISORY)"
echo "This is a preflight inventory, not an admission result. Even a fully green run"
echo "does not enable governed serve --pi; the runtime verifier and a signed profile decide that."
echo "host: $(uname -srm)   date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

# Kernel + user namespaces.
if have uname; then ok kernel "$(uname -r)"; else bad kernel "uname unavailable"; fi
if [ -r /proc/sys/kernel/unprivileged_userns_clone ]; then
  v="$(cat /proc/sys/kernel/unprivileged_userns_clone 2>/dev/null || echo '?')"
  [ "$v" = "1" ] && ok userns "unprivileged_userns_clone=$v" || bad userns "unprivileged_userns_clone=$v (need 1)"
else
  warnf userns "no unprivileged_userns_clone sysctl (distro default or unavailable)"
fi
if [ -r /proc/sys/kernel/apparmor_restrict_unprivileged_userns ]; then
  v="$(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns 2>/dev/null || echo '?')"
  [ "$v" = "1" ] && warnf apparmor "restrict_unprivileged_userns=$v (may block raw userns)" || ok apparmor "restrict_unprivileged_userns=$v"
fi

# cgroup v2 delegation + kill.
if [ -r /sys/fs/cgroup/cgroup.controllers ]; then
  ctrl="$(cat /sys/fs/cgroup/cgroup.controllers 2>/dev/null | tr -s ' ')"
  [ -n "$ctrl" ] && ok cgroup-controllers "$ctrl" || bad cgroup-controllers "empty (no delegation)"
else
  bad cgroup-v2 "/sys/fs/cgroup/cgroup.controllers missing (not cgroup v2)"
fi
if [ -e /sys/fs/cgroup/cgroup.kill ]; then ok cgroup-kill present; else bad cgroup-kill "not present at root; a delegated subtree with cgroup.kill is required"; fi
if [ -r /sys/fs/cgroup/cgroup.events ]; then ok cgroup-events "populated=$(grep -m1 populated /sys/fs/cgroup/cgroup.events 2>/dev/null | awk '{print $2}')"; else bad cgroup-events "unavailable (recursive populated accounting is required)"; fi

# Containment + image tooling.
for bin in podman crun cosign; do
  if have "$bin"; then ok "$bin" "$("$bin" --version 2>/dev/null | head -n1 || echo present)"; else bad "$bin" "not found (required for the D09 profile)"; fi
done

# Landlock / seccomp signals (best-effort, read-only).
if [ -r /sys/kernel/security/lsm ]; then ok lsm "LSMs: $(cat /sys/kernel/security/lsm 2>/dev/null)"; else warnf lsm "/sys/kernel/security/lsm unreadable"; fi
if grep -qi seccomp /proc/self/status 2>/dev/null; then ok seccomp "kernel exposes seccomp (this shell Seccomp=$(awk '/Seccomp:/{print $2}' /proc/self/status); cells apply a static profile)"; else warnf seccomp "no seccomp field in /proc/self/status"; fi

# Signed host profile.
prof_missing=0
for f in /etc/tbound/pi-host-profile.json /etc/tbound/pi-host-profile.ed25519 /etc/tbound/pi-host-profile.pub; do
  [ -e "$f" ] || prof_missing=$((prof_missing+1))
done
if [ "$prof_missing" -eq 0 ]; then
  if [ "$(stat -c '%U' /etc/tbound/pi-host-profile.json 2>/dev/null)" = "root" ]; then ok signed-profile "present, root-owned"; else bad signed-profile "present but not root-owned"; fi
else
  bad signed-profile "missing $prof_missing of 3 files under /etc/tbound"
fi

# Workspace filesystem.
ws_fs="$(findmnt -no FSTYPE -T / 2>/dev/null || echo '?')"
case "$ws_fs" in
  ext4|ext2|ext3|xfs) ok workspace-fs "$ws_fs" ;;
  *9p*|*drvfs*|*fuseblk*) bad workspace-fs "$ws_fs (DrvFs/9p is not the evaluation profile)" ;;
  *) warnf workspace-fs "$ws_fs (confirm against the frozen profile)" ;;
esac

echo
echo "summary: $pass pass, $warn warn, $fail fail"
if [ "$fail" -gt 0 ]; then
  echo
  echo "operator action required:"
  echo "  - provision the declared evaluation host/profile (see docs/governed-setup.md)"
  echo "  - install rootless Podman + crun and offline Cosign verification material"
  echo "  - delegate a fixed non-threaded cgroup v2 subtree with cgroup.kill"
  echo "  - generate and sign /etc/tbound/pi-host-profile.* (requires the profile tooling; Phase 2)"
  exit 1
fi
exit 0
