#!/usr/bin/env bash
# TBound reproducible evaluation host bootstrap: native Linux path (Ubuntu 24.04 amd64).
#
# Prepares one dedicated non-root trial account environment for the tbound
# evaluation profile so that infra/guest/probe-capabilities.sh passes:
#   - verifies or creates the trial user and its 65536-ID subordinate ranges
#   - installs pinned Go/Node toolchains and rootless Podman/crun by delegating
#     to infra/guest/setup-ubuntu-24.04.sh when they are missing
#   - installs the pinned Cosign static binary (Cosign is not in Ubuntu 24.04)
#   - applies the Ubuntu 24.04 unprivileged-userns sysctl fix after explicit
#     operator confirmation
#   - runs infra/guest/probe-capabilities.sh as the trial account at the end
#
# This is reproducibility infrastructure for the thesis evaluation profile.
# It is not production hardening and establishes no containment claim.
#
# Run as root:      sudo bash bootstrap-linux.sh [trial-user]
# Unattended runs:  TBOUND_ASSUME_YES=1 sudo -E bash bootstrap-linux.sh [trial-user]
#
# Exit codes:
#   0  bootstrap complete; probe passed with at most the two by-design UNVERIFIED items
#   1  the capability probe reported at least one FAIL (fix footer printed)
#   2  precondition or environment error before or during bootstrap (mirrors
#      setup-ubuntu-24.04.sh)
#   3  the only FAIL is a measured mismatch with the exact D09 ABI 8 profile
#   4  the probe reported UNVERIFIED items beyond the two by-design ones
set -Eeuo pipefail
umask 077

# ---------------------------------------------------------------------------
# Shared helpers
# ---------------------------------------------------------------------------

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s ==\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 2; }

LOG_FILE=/var/log/tbound-repro-bootstrap.log
PROBE_OUTPUT=
COSIGN_TMP_CLEANUP=
cleanup_tracked() {
  if [[ -n "$COSIGN_TMP_CLEANUP" ]]; then
    local work_real
    work_real=$(readlink -f -- "$COSIGN_TMP_CLEANUP" 2>/dev/null || true)
    if [[ -n "$work_real" && "$work_real" == /var/tmp/tbound-cosign.* ]]; then
      rm -rf -- "$work_real"
    fi
  fi
  if [[ -n "$PROBE_OUTPUT" ]]; then
    rm -f -- "$PROBE_OUTPUT"
  fi
}
trap 'rc=$?; cleanup_tracked; printf "\nBootstrap stopped with exit %s at %s\n" "$rc" "$(date -Is)" >&2' ERR

confirm() {
  # confirm <description>: returns 0 only on an explicit yes.
  if [[ "${TBOUND_ASSUME_YES:-0}" == "1" ]]; then
    say "Auto-confirmed (TBOUND_ASSUME_YES=1): $1"
    return 0
  fi
  local answer
  printf '%s\nProceed? [y/N] ' "$1"
  # Read from the terminal so piped or redirected stdin cannot answer silently.
  if read -r answer < /dev/tty 2>/dev/null; then
    [[ "$answer" == "y" || "$answer" == "Y" || "$answer" == "yes" || "$answer" == "Yes" ]]
  else
    printf 'ERROR: no terminal available to confirm; re-run with TBOUND_ASSUME_YES=1 to accept this exact action.\n' >&2
    return 1
  fi
}

# Suppress raw APT diagnostics: they can contain configured source URLs or
# proxy credentials (same policy as infra/guest/setup-ubuntu-24.04.sh).
run_apt() {
  local action=$1
  shift
  if "$@" >/dev/null 2>&1; then
    say "APT $action succeeded."
  else
    local rc=$?
    printf 'APT %s failed with exit %s; raw diagnostics were suppressed to protect configured credentials.\n' "$action" "$rc" >&2
    return "$rc"
  fi
}

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------

if (( EUID != 0 )); then
  printf 'Run as root: sudo %s [trial-user]\n' "$0" >&2
  exit 2
fi

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
PROBE_SCRIPT="$SCRIPT_DIR/../guest/probe-capabilities.sh"
SETUP_SCRIPT="$SCRIPT_DIR/../guest/setup-ubuntu-24.04.sh"
[[ -r "$PROBE_SCRIPT" ]] || die "probe script not readable at $PROBE_SCRIPT; copy the whole infra/ directory to this host"
PROFILE_GATE="$SCRIPT_DIR/landlock-profile-gate.sh"
[[ -r "$PROFILE_GATE" ]] || die "Landlock profile gate not readable at $PROFILE_GATE"
# shellcheck source=landlock-profile-gate.sh
source "$PROFILE_GATE"
[[ -r "$SETUP_SCRIPT" ]] || die "setup script not readable at $SETUP_SCRIPT; copy the whole infra/ directory to this host"

. /etc/os-release
if [[ "${ID:-}" != ubuntu || "${VERSION_CODENAME:-}" != noble || "${VERSION_ID:-}" != 24.04 ]]; then
  printf 'Expected Ubuntu 24.04 Noble; found ID=%s VERSION_ID=%s CODENAME=%s\n' "${ID:-?}" "${VERSION_ID:-?}" "${VERSION_CODENAME:-?}" >&2
  exit 2
fi
if [[ "$(uname -m)" != x86_64 ]]; then
  printf 'The pinned toolchains and Cosign binary are Linux amd64; found architecture %s.\n' "$(uname -m)" >&2
  exit 2
fi
if [[ -v APT_CONFIG ]]; then
  die 'APT_CONFIG is set; clear the custom APT configuration and retry. No changes made.'
fi

TARGET_USER="${1:-${TBOUND_USER:-${SUDO_USER:-}}}"
if [[ -z "$TARGET_USER" ]]; then
  printf 'No trial user given. Usage: sudo %s <non-root-trial-user>\n' "$0" >&2
  exit 2
fi

install -d -m 0755 /var/log
if [[ -L "$LOG_FILE" || ( -e "$LOG_FILE" && ! -f "$LOG_FILE" ) ]]; then
  die "bootstrap log path is not a regular file; refusing to write it: $LOG_FILE"
fi
if [[ ! -e "$LOG_FILE" ]]; then
  (set -o noclobber; : > "$LOG_FILE") || die 'could not create the bootstrap log securely'
fi
chmod 0600 "$LOG_FILE"
exec > >(tee -a "$LOG_FILE") 2>&1

printf '\n=== tbound repro bootstrap (native Linux) start: %s ===\n' "$(date -Is)"
printf 'OS: %s\nKernel: %s\n' "$(grep '^PRETTY_NAME=' /etc/os-release | cut -d= -f2- | tr -d '"')" "$(uname -r)"
printf 'Bootstrap log: %s\n' "$LOG_FILE"

# ---------------------------------------------------------------------------
# Step 1: trial user and subordinate ID ranges
# ---------------------------------------------------------------------------

step 'Step 1/6: trial account and subordinate UID/GID ranges'

if ! id "$TARGET_USER" >/dev/null 2>&1; then
  say "Trial user '$TARGET_USER' does not exist."
  if confirm "Create dedicated non-root trial account '$TARGET_USER' (adduser --disabled-password, no login password set)?"; then
    adduser --disabled-password --gecos 'TBound evaluation trial account' "$TARGET_USER" >/dev/null
    say "Created trial user '$TARGET_USER'."
  else
    die "refused to create trial user '$TARGET_USER'; create it manually and re-run"
  fi
fi
TARGET_UID=$(id -u "$TARGET_USER")
if [[ "$TARGET_UID" == 0 ]]; then
  die 'the trial account must be a non-root user'
fi
say "Trial account: $TARGET_USER (uid=$TARGET_UID)"

# Rootless Podman needs subordinate UID and GID ranges. Reuse a sufficiently
# large existing range for the target account; otherwise allocate a fresh
# range beyond all configured ranges in both files. Idempotent: only missing
# ranges are added.
has_subid_range() {
  local file=$1
  [[ -r "$file" ]] && awk -F: -v name="$TARGET_USER" -v uid="$TARGET_UID" '($1 == name || $1 == uid) && $3 ~ /^[0-9]+$/ && $3 >= 65536 { ok=1 } END { exit !ok }' "$file"
}
first_free_subid() {
  local files=()
  [[ -r /etc/subuid ]] && files+=(/etc/subuid)
  [[ -r /etc/subgid ]] && files+=(/etc/subgid)
  if ((${#files[@]} == 0)); then
    printf '100000\n'
  else
    awk -F: 'NF >= 3 && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ { end = $2 + $3; if (end > max) max = end } END { start = max + 1; if (start < 100000) start = 100000; print start }' "${files[@]}"
  fi
}
SUBID_ADDED=0
if ! has_subid_range /etc/subuid || ! has_subid_range /etc/subgid; then
  if confirm "Add a 65536-ID subordinate UID/GID range for '$TARGET_USER' in /etc/subuid and /etc/subgid?"; then
    SUBID_START=$(first_free_subid)
    SUBID_END=$((SUBID_START + 65535))
    if (( SUBID_END > 4294967295 )); then
      die 'no safe 65536-ID subordinate range remains; configure /etc/subuid and /etc/subgid manually'
    fi
    if ! has_subid_range /etc/subuid; then
      usermod --add-subuids "${SUBID_START}-${SUBID_END}" "$TARGET_USER"
      say "Added subuid range $SUBID_START-$SUBID_END for $TARGET_USER"
      SUBID_ADDED=1
    fi
    if ! has_subid_range /etc/subgid; then
      usermod --add-subgids "${SUBID_START}-${SUBID_END}" "$TARGET_USER"
      say "Added subgid range $SUBID_START-$SUBID_END for $TARGET_USER"
      SUBID_ADDED=1
    fi
  else
    die "refused to add subordinate ID ranges; rootless Podman will fail without them"
  fi
else
  say "Subordinate UID/GID ranges of at least 65536 IDs already present for '$TARGET_USER'."
fi

# ---------------------------------------------------------------------------
# Step 2: rootless Podman, crun, pinned Go/Node toolchains
# ---------------------------------------------------------------------------

step 'Step 2/6: rootless Podman, crun, and the pinned Go/Node toolchains'

NODE_VERSION=24.21.0
GO_VERSION=1.27.1
NODE_DEST="/opt/tbound/toolchains/node-v${NODE_VERSION}-linux-x64"
GO_DEST="/opt/tbound/toolchains/go${GO_VERSION}"

if [[ -d "$NODE_DEST" && -d "$GO_DEST" ]]; then
  # Pinned toolchains are already provisioned (for example on a copy of the
  # verified evaluation VM). Verify they run; install only what is missing.
  NODE_BIN="$NODE_DEST/bin/node"
  GO_BIN="$GO_DEST/bin/go"
  "$NODE_BIN" --version | grep -qx "v${NODE_VERSION}" || die "$NODE_BIN did not report v${NODE_VERSION}; inspect the toolchain directory"
  GOTOOLCHAIN=local "$GO_BIN" version | grep -q "go${GO_VERSION} linux/amd64" || die "$GO_BIN did not report go${GO_VERSION} linux/amd64; inspect the toolchain directory"
  say "Pinned toolchains verified: Node v${NODE_VERSION} at $NODE_DEST, Go ${GO_VERSION} at $GO_DEST"

  MISSING_PKGS=()
  for pkg_cmd in 'podman:podman' 'crun:crun' 'uidmap:newuidmap' 'slirp4netns:slirp4netns' 'fuse-overlayfs:fuse-overlayfs' 'libseccomp2:' 'ca-certificates:' 'curl:curl' 'xz-utils:' 'gnupg:' 'python3:python3'; do
    pkg=${pkg_cmd%%:*}
    cmd=${pkg_cmd#*:}
    if [[ -n "$cmd" ]]; then
      command -v "$cmd" >/dev/null 2>&1 || MISSING_PKGS+=("$pkg")
    else
      dpkg-query -W -f='${db:Status-Abbrev}' "$pkg" 2>/dev/null | grep -q '^ii' || MISSING_PKGS+=("$pkg")
    fi
  done
  if ((${#MISSING_PKGS[@]} > 0)); then
    say "Runtime packages are missing: ${MISSING_PKGS[*]}"
    say 'This bootstrap does not run APT while the pinned toolchain directories already exist,'
    say 'because infra/guest/setup-ubuntu-24.04.sh performs the guarded APT validation and would'
    say 'refuse those existing directories. Install the missing packages with a reviewed command, for example:'
    say "  sudo apt-get install -y --no-install-recommends ${MISSING_PKGS[*]}"
    die 'install the missing runtime packages manually, then re-run this bootstrap'
  fi
  say 'Podman, crun, uidmap and helper packages are present.'
else
  if [[ -d "$NODE_DEST" || -d "$GO_DEST" ]]; then
    die "only one pinned toolchain directory exists ($NODE_DEST, $GO_DEST); inspect and resolve manually before bootstrapping"
  fi
  say "Pinned toolchains are absent; delegating to the guarded provisioner infra/guest/setup-ubuntu-24.04.sh."
  say "It installs Podman/crun/uidmap from signature-checked APT, downloads pinned Node v${NODE_VERSION}"
  say "and Go ${GO_VERSION} with signature/hash verification, and creates the subordinate ID ranges."
  if ! confirm "Run infra/guest/setup-ubuntu-24.04.sh for trial user '$TARGET_USER' (network access required)?"; then
    die 'refused to run the guarded provisioner; install the pinned toolchains manually and re-run'
  fi
  bash "$SETUP_SCRIPT" "$TARGET_USER"
  say 'Guarded provisioner completed.'
  say 'It installs held package versions and pinned toolchains; its log is /var/log/tbound-guest-provision.log.'
fi

# ---------------------------------------------------------------------------
# Step 3: Cosign (static binary; Cosign is not packaged for Ubuntu 24.04)
# ---------------------------------------------------------------------------

step 'Step 3/6: Cosign'

COSIGN_VERSION=3.1.3
COSIGN_DEFAULT_URL="https://github.com/sigstore/cosign/releases/download/v${COSIGN_VERSION}/cosign-linux-amd64"
# SHA-256 of the official release artifact, captured from
# https://github.com/sigstore/cosign/releases/download/v3.1.3/cosign-linux-amd64
# at authoring time (2026-10-11). Pre-install keyless verification is not
# possible before Cosign itself exists, so the digest is pinned here.
COSIGN_DEFAULT_SHA256=4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71
COSIGN_URL="${TBOUND_COSIGN_URL:-$COSIGN_DEFAULT_URL}"
COSIGN_SHA256="${TBOUND_COSIGN_SHA256:-$COSIGN_DEFAULT_SHA256}"
COSIGN_DEST="/opt/tbound/toolchains/cosign-v${COSIGN_VERSION}/cosign"

if command -v cosign >/dev/null 2>&1; then
  COSIGN_FOUND_VERSION=$(cosign version 2>/dev/null | sed -n 's/^GitVersion:[[:space:]]*v\{0,1\}//p' | head -n 1)
  if [[ -z "$COSIGN_FOUND_VERSION" ]]; then
    COSIGN_FOUND_VERSION=$(cosign version 2>/dev/null | head -n 1)
  fi
  if [[ "$COSIGN_FOUND_VERSION" == "$COSIGN_VERSION" ]]; then
    printf '%s  %s\n' "$COSIGN_SHA256" "$(command -v cosign)" | sha256sum --check - >/dev/null || \
      die "installed Cosign v${COSIGN_VERSION} does not match the pinned SHA-256"
    say "Cosign v${COSIGN_VERSION} already installed at $(command -v cosign)."
  else
    printf 'Cosign found at %s but its version (%s) does not match the pinned v%s.\n' \
      "$(command -v cosign)" "${COSIGN_FOUND_VERSION:-unknown}" "$COSIGN_VERSION" >&2
    die 'the evaluation profile pins Cosign; replace it or override TBOUND_COSIGN_URL and TBOUND_COSIGN_SHA256 deliberately'
  fi
else
  [[ -d "/opt/tbound/toolchains/cosign-v${COSIGN_VERSION}" ]] && die "pinned Cosign directory exists but 'cosign' is not on PATH: /opt/tbound/toolchains/cosign-v${COSIGN_VERSION}"
  if ! command -v curl >/dev/null 2>&1; then
    die 'curl is required to download Cosign; install it and re-run'
  fi
  if ! confirm "Download Cosign v${COSIGN_VERSION} (~135 MiB) and install it under /opt/tbound/toolchains with a /usr/local/bin/cosign symlink?"; then
    die 'refused to install Cosign; the offline verifier requires it'
  fi
  WORKDIR=$(mktemp -d /var/tmp/tbound-cosign.XXXXXXXX)
  COSIGN_TMP_CLEANUP=$WORKDIR
  curl --fail --location --proto '=https' --tlsv1.2 --retry 2 -o "$WORKDIR/cosign" "$COSIGN_URL"
  printf '%s  %s\n' "$COSIGN_SHA256" "$WORKDIR/cosign" | sha256sum --check -
  install -d -m 0755 "/opt/tbound/toolchains/cosign-v${COSIGN_VERSION}"
  install -m 0755 "$WORKDIR/cosign" "$COSIGN_DEST"
  rm -rf -- "$WORKDIR"
  COSIGN_TMP_CLEANUP=
  ln -sfn "$COSIGN_DEST" /usr/local/bin/cosign
  "$COSIGN_DEST" version | head -n 1
  say "Cosign v${COSIGN_VERSION} installed at $COSIGN_DEST (symlinked from /usr/local/bin/cosign)."
fi

# ---------------------------------------------------------------------------
# Step 4: unprivileged user namespace sysctl fix (explicit confirmation)
# ---------------------------------------------------------------------------

step 'Step 4/6: unprivileged user namespace policy'

SYSCTL_KEY='kernel.apparmor_restrict_unprivileged_userns'
SYSCTL_FILE='/etc/sysctl.d/99-tbound-userns.conf'
if ! sysctl -n "$SYSCTL_KEY" >/dev/null 2>&1; then
  say "$SYSCTL_KEY does not exist on this kernel; no user namespace restriction to fix."
else
  CURRENT=$(sysctl -n "$SYSCTL_KEY")
  if [[ "$CURRENT" == "0" ]]; then
    say "$SYSCTL_KEY is already 0; unprivileged user namespaces are allowed."
  else
    say "Ubuntu 24.04 restricts unprivileged user namespaces ($SYSCTL_KEY=$CURRENT)."
    say 'This blocks rootless Podman and the probe user-namespace check with'
    say '  "write /proc/self/uid_map: Operation not permitted".'
    say "Proposed change: write $SYSCTL_FILE containing"
    say "  $SYSCTL_KEY = 0"
    say 'and apply it now with sysctl -w. This is the documented Ubuntu fix for'
    say 'dedicated evaluation environments; it restores the upstream default under'
    say 'which any local user may create user namespaces. Review that trade-off for this host.'
    if confirm "Write $SYSCTL_FILE and set $SYSCTL_KEY=0 now?"; then
      if [[ -e "$SYSCTL_FILE" ]]; then
        grep -Eq '^[[:space:]]*kernel\.apparmor_restrict_unprivileged_userns[[:space:]]*=[[:space:]]*0' "$SYSCTL_FILE" || \
          die "$SYSCTL_FILE already exists with different content; inspect it manually"
      else
        (set -o noclobber; printf '# TBound evaluation environment: allow unprivileged user namespaces (rootless Podman).\n%s = 0\n' "$SYSCTL_KEY" > "$SYSCTL_FILE") \
          || die "could not create $SYSCTL_FILE (does it already exist?)"
        chmod 0644 "$SYSCTL_FILE"
      fi
      sysctl -w "$SYSCTL_KEY=0" >/dev/null
      APPLIED=$(sysctl -n "$SYSCTL_KEY")
      [[ "$APPLIED" == "0" ]] || die "sysctl write did not take effect ($SYSCTL_KEY=$APPLIED)"
      say "$SYSCTL_KEY=0 applied (runtime) and persisted in $SYSCTL_FILE."
    else
      die 'refused to change the user namespace policy; the probe user-namespace check will keep failing until it is applied'
    fi
  fi
fi

# ---------------------------------------------------------------------------
# Step 5: cgroup v2 and Landlock ABI prechecks (read-only)
# ---------------------------------------------------------------------------

step 'Step 5/6: cgroup v2 and Landlock ABI prechecks'

if grep -q ' - cgroup2 ' /proc/self/mountinfo; then
  say 'cgroup v2 hierarchy is mounted.'
else
  die 'no cgroup2 mount is visible; the tbound profile requires cgroup v2 (check hypervisor, WSL1, or container limitations)'
fi

LANDLOCK_ABI=$(python3 - <<'PY'
import ctypes
libc = ctypes.CDLL(None, use_errno=True)
libc.syscall.restype = ctypes.c_long
value = libc.syscall(444, None, ctypes.c_size_t(0), ctypes.c_uint(1))
if value > 0:
    print(value)
else:
    print(f'ERR {ctypes.get_errno()}')
PY
) || die 'the Landlock ABI precheck could not run (python3 required)'
if [[ "$LANDLOCK_ABI" == ERR* ]]; then
  say "Landlock ABI query failed: $LANDLOCK_ABI"
else
  say "Landlock ABI $LANDLOCK_ABI detected (registered D09 profile requires exactly ABI 8 = LANDLOCK_RESTRICT_SELF_TSYNC)."
fi

# ---------------------------------------------------------------------------
# Step 6: capability probe as the trial account
# ---------------------------------------------------------------------------

step 'Step 6/6: capability probe (infra/guest/probe-capabilities.sh)'

say "Running the read-only probe as '$TARGET_USER' (no container is started, no image is pulled)."
if [[ "$SUBID_ADDED" == 1 ]]; then
  say 'NOTE: subordinate ID ranges were just added. For checks that run inside a live'
  say 'login session of the trial user, log that session out and in again first.'
fi

PROBE_OUTPUT=$(mktemp /var/tmp/tbound-probe-out.XXXXXXXX)
PROBE_RC=0
sudo -H -u "$TARGET_USER" bash "$PROBE_SCRIPT" | tee "$PROBE_OUTPUT" || PROBE_RC=$?

# ---------------------------------------------------------------------------
# Interpretation and fix footer
# ---------------------------------------------------------------------------

explain_fail() {
  local name=$1
  case "$name" in
    user-namespace)
      say '  - user-namespace: apply the sysctl fix from Step 4 (kernel.apparmor_restrict_unprivileged_userns=0), then re-run.'
      ;;
    landlock-abi)
      say '  - landlock-abi: the Landlock ABI query failed or the kernel reports Landlock unavailable; inspect the detail above.'
      ;;
    d09-landlock-profile)
      say '  - d09-landlock-profile: the registered D09 profile requires exactly Landlock ABI 8; inspect the measured ABI and profile.'
      ;;
    podman-rootless-crun|podman-subid-maps|subuid|subgid|uidmap-helpers)
      say "  - $name: install podman/crun/uidmap (Step 2 path) and the subordinate ID ranges (Step 1), then log the trial user out and in again."
      ;;
    node-runtime)
      say '  - node-runtime: pinned Node v24.21.0 missing under /opt/tbound/toolchains; run the guarded provisioner (Step 2).'
      ;;
    go-runtime)
      say '  - go-runtime: pinned Go 1.27.1 missing under /opt/tbound/toolchains; run the guarded provisioner (Step 2).'
      ;;
    cgroup-v2|cgroup-domain|cgroup.kill-interface)
      say "  - $name: cgroup v2 delegation for the user session is required; this needs a real systemd host (not WSL1 or a plain container)."
      ;;
    seccomp-filter|pidfd)
      say "  - $name: kernel lacks a required mechanism; upgrade the kernel (HWE) on this host."
      ;;
    probe-runtime)
      say '  - probe-runtime: python3 is required on the host.'
      ;;
    *)
      say "  - $name: inspect the probe detail line above, fix the underlying condition, and re-run."
      ;;
  esac
}

PROBE_DECISION=$(tbound_classify_probe_report "$PROBE_OUTPUT" "$PROBE_RC")
REPORTED_LANDLOCK_ABI=$(tbound_report_landlock_abi "$PROBE_OUTPUT" 2>/dev/null || true)

if [[ "$PROBE_DECISION" == 3 ]]; then
  say ''
  say 'RESULT: measured Landlock ABI does not match the exact D09 profile.'
  say "  Observed: ${REPORTED_LANDLOCK_ABI:-unknown}; required: exactly ABI 8 (LANDLOCK_RESTRICT_SELF_TSYNC)."
  if [[ "$REPORTED_LANDLOCK_ABI" =~ ^[1-7]$ ]]; then
    say '  Ubuntu 24.04 HWE may provide ABI 8 on this installation. After reviewing the host, install the HWE stack and reboot:'
    say '    sudo apt-get install --install-recommends linux-generic-hwe-24.04'
    say '    sudo reboot'
    say '  Re-run the probe and record the running kernel, ABI, and package versions.'
  else
    say '  A later ABI is not accepted by this registered profile. Reconcile the protocol and profile explicitly.'
    say '  Record the measured ABI; do not infer it from the kernel version or installed packages.'
  fi
  rm -f -- "$PROBE_OUTPUT"; PROBE_OUTPUT=
  printf 'Bootstrap finished with exit 3 (exact D09 Landlock ABI mismatch) at %s\n' "$(date -Is)"
  exit 3
elif [[ "$PROBE_DECISION" == 1 ]]; then
  say ''
  say 'RESULT: the probe reported FAIL items. Plain-language fixes:'
  while IFS= read -r line; do
    name=${line#*  }
    name=${name#"${name%%[! ]*}"}
    name=${name%%:*}
    explain_fail "$name"
  done < <(grep '^FAIL  ' "$PROBE_OUTPUT" || true)
  rm -f -- "$PROBE_OUTPUT"; PROBE_OUTPUT=
  printf 'Bootstrap finished with exit 1 (probe FAIL items remain) at %s\n' "$(date -Is)"
  exit 1
elif [[ "$PROBE_DECISION" == 4 ]]; then
  say ''
  say 'RESULT: the probe reported UNVERIFIED items. UNVERIFIED is not success.'
  say 'By design of the read-only probe, two items are always UNVERIFIED here:'
  say '  - cgroup.kill-write: never written (writing it SIGKILLs the cgroup subtree)'
  say '  - uidmap-helpers: presence is checked, mapping behavior is not inferred'
  say 'Additional UNVERIFIED items beyond those two were found; inspect the output above.'
  rm -f -- "$PROBE_OUTPUT"; PROBE_OUTPUT=
  printf 'Bootstrap finished with exit 4 (unexpected UNVERIFIED items) at %s\n' "$(date -Is)"
  exit 4
elif [[ "$PROBE_DECISION" != 0 ]]; then
  printf 'The probe output could not be classified (probe exit %s)\n' "$PROBE_RC" >&2
  exit 2
else
  say ''
  say 'RESULT: all required capability checks PASS; only by-design UNVERIFIED rows remain, if present.'
fi
rm -f -- "$PROBE_OUTPUT"; PROBE_OUTPUT=

say ''
say '=== Bootstrap complete ==='
say "Trial account: $TARGET_USER (uid=$(id -u "$TARGET_USER"))"
say "Probe: $PROBE_SCRIPT"
say "Bootstrap log: $LOG_FILE"
if [[ "$SUBID_ADDED" == 1 ]]; then
  say "Log existing '$TARGET_USER' sessions out and in again (or reboot) before rootless trials so the new subordinate IDs take effect."
fi
printf 'Bootstrap complete: %s\n' "$(date -Is)"
exit 0
