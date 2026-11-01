#!/usr/bin/env bash
# Create and provision a disposable Ubuntu 24.04 QEMU VM on a Linux host.
#
# This is the Linux-host counterpart to bootstrap-hyperv-windows.ps1. It is
# development/reproducibility tooling: a successful run installs and smoke-tests
# tbound, but does not create a signed governed host profile and does not
# establish containment or G1.
#
# Usage:
#   bash infra/repro/bootstrap-qemu-linux.sh [options]
#   bash infra/repro/bootstrap-qemu-linux.sh --vm-dir DIR --start|--stop|--ssh|--status
#
# Options:
#   --vm-dir DIR      VM directory (default: <repo>/.local/vm/tbound-vm)
#   --name NAME       QEMU/cloud-init name (default: tbound-vm)
#   --user USER       Guest trial account (default: tbound)
#   --cpus N          Guest vCPUs (default: 4)
#   --memory MB       Guest RAM (default: 3072; TCG is memory-hungry)
#   --disk GB         Guest disk (default: 40)
#   --port PORT       Host SSH port (default: 2222)
#   --image FILE      Use a local Ubuntu 24.04 cloud qcow2 instead of downloading
#   --local-dist DIR  Install a prebuilt local release instead of building in-guest
#   --version VER     Release/install version (default: dev)
#   --hwe             Opt in to the HWE kernel (ABI 8, not yet qualified)
#   --allow-userns-sysctl
#                     Opt in to changing the host-wide AppArmor userns sysctl
#   --developer       Run adapter checks, the Pi dev fixture, and make verify
#   --check           Print host prerequisites and exit
#
# The script never deletes an existing VM directory. It refuses to overwrite a
# disk, SSH key, or seed. Stop an existing VM before creating a new one.
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
VM_DIR="${TBOUND_VM_DIR:-$ROOT/.local/vm/tbound-vm}"
VM_NAME="tbound-vm"
TRIAL_USER="tbound"
CPUS=4
MEMORY_MB=3072
DISK_GB=40
SSH_PORT=2222
IMAGE=""
LOCAL_DIST=""
DIST_VERSION="dev"
INSTALL_HWE=0
ALLOW_USERNS_SYSCTL=0
RUN_DEVELOPER=0
QUALIFICATION_STATUS="not run (default doctor is read-only)"
MODE=create

say()  { printf '%s\n' "$*"; }
step() { printf '\n== %s ==\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 0
}

while (($# > 0)); do
  case "$1" in
    --vm-dir) VM_DIR="${2:?--vm-dir needs a value}"; shift 2 ;;
    --name) VM_NAME="${2:?--name needs a value}"; shift 2 ;;
    --user) TRIAL_USER="${2:?--user needs a value}"; shift 2 ;;
    --cpus) CPUS="${2:?--cpus needs a value}"; shift 2 ;;
    --memory) MEMORY_MB="${2:?--memory needs a value}"; shift 2 ;;
    --disk) DISK_GB="${2:?--disk needs a value}"; shift 2 ;;
    --port) SSH_PORT="${2:?--port needs a value}"; shift 2 ;;
    --image) IMAGE="${2:?--image needs a value}"; shift 2 ;;
    --local-dist) LOCAL_DIST="${2:?--local-dist needs a value}"; shift 2 ;;
    --version) DIST_VERSION="${2:?--version needs a value}"; shift 2 ;;
    --hwe) INSTALL_HWE=1; shift ;;
    --allow-userns-sysctl) ALLOW_USERNS_SYSCTL=1; shift ;;
    --developer) RUN_DEVELOPER=1; shift ;;
    --check) MODE=check; shift ;;
    --start) MODE=start; shift ;;
    --stop) MODE=stop; shift ;;
    --ssh) MODE=ssh; shift ;;
    --status) MODE=status; shift ;;
    -h|--help) usage ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
done

[[ "$TRIAL_USER" =~ ^[a-z_][a-z0-9_-]*$ ]] || die "invalid --user: $TRIAL_USER"
[[ "$CPUS" =~ ^[1-9][0-9]*$ ]] || die "--cpus must be a positive integer"
[[ "$MEMORY_MB" =~ ^[1-9][0-9]*$ ]] || die "--memory must be a positive integer"
[[ "$DISK_GB" =~ ^[1-9][0-9]*$ ]] || die "--disk must be a positive integer"
[[ "$SSH_PORT" =~ ^[1-9][0-9]*$ ]] || die "--port must be a positive integer"
[[ "$DIST_VERSION" =~ ^[A-Za-z0-9._-]+$ ]] || die "--version has invalid characters: $DIST_VERSION"
if [[ -n "$LOCAL_DIST" ]]; then
  [[ -d "$LOCAL_DIST" ]] || die "--local-dist is not a directory: $LOCAL_DIST"
  LOCAL_DIST="$(cd -- "$LOCAL_DIST" && pwd)"
fi

# Later steps change directory, so the VM paths must be absolute.
VM_DIR=$(realpath -m -- "$VM_DIR")
KEY="$VM_DIR/id_ed25519_${VM_NAME//[^A-Za-z0-9_.-]/_}"
KNOWN="$VM_DIR/known_hosts"
DISK="$VM_DIR/$VM_NAME.qcow2"
SEED="$VM_DIR/seed.iso"
PIDFILE="$VM_DIR/qemu.pid"
CONSOLE="$VM_DIR/console.log"
ENVFILE="$VM_DIR/vm.env"

need_host_tools() {
  local missing=()
  for tool in qemu-system-x86_64 qemu-img cloud-localds ssh ssh-keygen curl sha256sum tar; do
    command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
  done
  if ((${#missing[@]} > 0)); then
    say "Missing host tools: ${missing[*]}"
    say "On Debian/Ubuntu: sudo apt-get install -y qemu-system-x86 qemu-utils cloud-image-utils openssh-client curl"
    exit 2
  fi
}

kvm_usable() {
  [[ -e /dev/kvm ]] || return 1
  command -v python3 >/dev/null 2>&1 || return 1
  python3 - <<'PY' >/dev/null 2>&1
import os
fd = os.open('/dev/kvm', os.O_RDWR)
os.close(fd)
PY
}

load_env() {
  [[ -r "$ENVFILE" ]] || die "no VM environment at $ENVFILE; create one first without --start/--stop/--ssh/--status"
  # shellcheck disable=SC1090
  source "$ENVFILE"
  KEY="$VM_DIR/id_ed25519_${VM_NAME//[^A-Za-z0-9_.-]/_}"
  KNOWN="$VM_DIR/known_hosts"
  DISK="$VM_DIR/$VM_NAME.qcow2"
  SEED="$VM_DIR/seed.iso"
  PIDFILE="$VM_DIR/qemu.pid"
  CONSOLE="$VM_DIR/console.log"
}

ssh_guest() {
  ssh -p "$SSH_PORT" -i "$KEY" -o "UserKnownHostsFile=\"$KNOWN\"" \
    -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -o ServerAliveInterval=30 \
    "$TRIAL_USER@127.0.0.1" "$@"
}

vm_running() {
  [[ -f "$PIDFILE" ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null
}

wait_ssh() {
  local label=$1
  say "Waiting for SSH ($label) on 127.0.0.1:$SSH_PORT ..."
  for _ in $(seq 1 240); do
    if ssh -p "$SSH_PORT" -i "$KEY" -o "UserKnownHostsFile=\"$KNOWN\"" \
        -o StrictHostKeyChecking=accept-new -o ConnectTimeout=5 -o BatchMode=yes \
        "$TRIAL_USER@127.0.0.1" 'true' >/dev/null 2>&1; then
      ssh-keygen -lf "$KNOWN" | sed 's/^/host key: /'
      return 0
    fi
    sleep 5
  done
  tail -40 "$CONSOLE" 2>/dev/null || true
  die "SSH did not become ready; inspect $CONSOLE"
}

start_qemu() {
  local accel=()
  if kvm_usable; then
    accel=(-accel kvm -cpu host)
    say "KVM is available; using hardware acceleration."
  else
    accel=(-accel "tcg,thread=multi" -cpu max)
    warn "KVM is unavailable; using QEMU TCG. Provisioning will be much slower."
  fi
  qemu-system-x86_64 \
    -name "$VM_NAME" \
    -machine q35 "${accel[@]}" -smp "$CPUS" -m "$MEMORY_MB" \
    -drive file="$DISK",if=virtio,format=qcow2,cache=writeback \
    -drive file="$SEED",if=virtio,format=raw,readonly=on \
    -device virtio-rng-pci \
    -netdev user,id=net0,hostfwd=tcp:127.0.0.1:"$SSH_PORT"-:22 \
    -device virtio-net-pci,netdev=net0 \
    -display none -serial file:"$CONSOLE" -monitor none \
    -daemonize -pidfile "$PIDFILE" || {
      if [[ "${accel[1]}" == kvm ]]; then
        warn "KVM start failed; retrying with TCG."
        accel=(-accel "tcg,thread=multi" -cpu max)
        qemu-system-x86_64 -name "$VM_NAME" -machine q35 "${accel[@]}" \
          -smp "$CPUS" -m "$MEMORY_MB" \
          -drive file="$DISK",if=virtio,format=qcow2,cache=writeback \
          -drive file="$SEED",if=virtio,format=raw,readonly=on \
          -device virtio-rng-pci \
          -netdev user,id=net0,hostfwd=tcp:127.0.0.1:"$SSH_PORT"-:22 \
          -device virtio-net-pci,netdev=net0 \
          -display none -serial file:"$CONSOLE" -monitor none \
          -daemonize -pidfile "$PIDFILE"
      else
        die "QEMU failed to start; inspect $CONSOLE"
      fi
    }
  say "QEMU started (pid $(cat "$PIDFILE")); console: $CONSOLE"
}

stop_qemu() {
  if ! vm_running; then
    say "VM is not running."
    rm -f "$PIDFILE"
    return 0
  fi
  ssh_guest 'sudo -n poweroff' >/dev/null 2>&1 || true
  for _ in $(seq 1 60); do
    if ! vm_running; then
      say "VM stopped."
      rm -f "$PIDFILE"
      return 0
    fi
    sleep 2
  done
  die "VM did not stop; pid $(cat "$PIDFILE") is still alive"
}

download_image() {
  local base="$VM_DIR/base"
  mkdir -p "$base"
  local image="$base/noble-server-cloudimg-amd64.img"
  if [[ -n "$IMAGE" ]]; then
    [[ -r "$IMAGE" ]] || die "local image is not readable: $IMAGE"
    say "Using local base image: $IMAGE"
    cp --reflink=auto -- "$IMAGE" "$image" 2>/dev/null || cp -- "$IMAGE" "$image"
    sha256sum "$image" | sed 's/^/base image sha256: /'
    return 0
  fi
  # A release directory stays online and its hash never changes, unlike noble/current.
  local sha256=04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94
  if [[ ! -f "$image" ]] || [[ "$(sha256sum "$image" | cut -d' ' -f1)" != "$sha256" ]]; then
    say "Downloading the pinned Ubuntu 24.04 cloud image ..."
    curl --fail --location --proto '=https' --tlsv1.2 --retry 3 -o "$image.part" \
      https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.img
    [[ "$(sha256sum "$image.part" | cut -d' ' -f1)" == "$sha256" ]] || { rm -f -- "$image.part"; die "cloud image does not match its pin; not kept"; }
    mv -- "$image.part" "$image"
  fi
}

write_seed() {
  umask 077
  cat > "$VM_DIR/user-data" <<EOF
#cloud-config
hostname: $VM_NAME
manage_etc_hosts: true
users:
  - name: $TRIAL_USER
    gecos: TBound trial account
    groups: [adm, sudo]
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - $(cat "$KEY.pub")
ssh_pwauth: false
disable_root: true
growpart:
  mode: auto
  devices: ['/']
resize_rootfs: true
package_update: false
package_upgrade: false
EOF
  cat > "$VM_DIR/meta-data" <<EOF
instance-id: $VM_NAME-001
local-hostname: $VM_NAME
EOF
  ( cd "$VM_DIR" && cloud-localds "$SEED" user-data meta-data )
}

write_guest_provisioner() {
  cat > "$VM_DIR/provision-guest.sh" <<'GUEST'
#!/usr/bin/env bash
set -euo pipefail
export GOTOOLCHAIN=local
export PATH="/opt/tbound/toolchains/go1.27.1/bin:/opt/tbound/toolchains/node-v24.21.0-linux-x64/bin:$HOME/.local/bin:$PATH"
export TMPDIR="$HOME/.cache/tbound-tmp"
install -d -m 700 "$TMPDIR"
umask 077
cd "$HOME/trust-boundary"

step() { printf '\n===== %s =====\n' "$*"; }
say()  { printf '%s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }

step "official Pi CLI 0.87.1"
npm_config_prefix="$HOME/.local" npm install --global --ignore-scripts \
  @earendil-works/pi-coding-agent@0.87.1
export PATH="$HOME/.local/bin:$PATH"
hash -r
pi --version

if [[ "${RUN_DEVELOPER:-0}" == 1 ]]; then
  step "adapter dependencies and developer check"
  cd "$HOME/trust-boundary/adapter"
  npm ci --ignore-scripts --no-audit --no-fund
  TBOUND_TEST_TIMEOUT_MS=120000 npm run check
fi

step "install tbound"
cd "$HOME/trust-boundary"
if [[ -n "${TBOUND_LOCAL_DIST:-}" ]]; then
  step "install prebuilt local release"
  bash install/install.sh --version "$TBOUND_DIST_VERSION" --local-dist "$TBOUND_LOCAL_DIST" --no-path
else
  step "build and install from source"
  make build VERSION="$TBOUND_DIST_VERSION"
  bash install/install.sh --version "$TBOUND_DIST_VERSION" --local-dist dist --no-path
fi
export PATH="$HOME/.local/share/tbound/bin:$PATH"
hash -r

step "doctor (advisory)"
set +e
tbound-doctor --pi "$(command -v pi)" | tee "$HOME/tbound-doctor.txt"
DOCTOR_RC=${PIPESTATUS[0]}
tbound-doctor --json > "$HOME/tbound-doctor.json" 2>/dev/null || true
set -e
[[ "$DOCTOR_RC" -eq 0 ]] || warn "tbound-doctor exited $DOCTOR_RC; the user-prefix install is still complete."

step "native fixture (non-claim-bearing smoke)"
cd "$HOME/trust-boundary"
set +e
tbound serve --pi --native-fixture | tee "$HOME/tbound-native-fixture.json"
NATIVE_RC=${PIPESTATUS[0]}
set -e
[[ "$NATIVE_RC" -eq 0 ]] || warn "native fixture exited $NATIVE_RC; inspect the receipt and doctor output. The user-prefix install is still complete."

if [[ "${RUN_DEVELOPER:-0}" == 1 ]]; then
  step "offline Pi CLI development fixture"
  TBOUND_DEV_FIXTURE_PI="$(realpath "$(command -v pi)")"
  TBOUND_DEV_FIXTURE_NODE="$(realpath "$(command -v node)")"
  TBOUND_DEV_FIXTURE_ADAPTER="$HOME/trust-boundary/adapter"
  TBOUND_DEV_FIXTURE_ROOT="$HOME/.local/state/tbound-dev-fixture"
  export TBOUND_DEV_FIXTURE_PI TBOUND_DEV_FIXTURE_NODE TBOUND_DEV_FIXTURE_ADAPTER TBOUND_DEV_FIXTURE_ROOT
  install -d -m 700 "$TBOUND_DEV_FIXTURE_ROOT"
  tbound serve --pi --dev-fixture | tee "$HOME/tbound-dev-fixture.json"

  step "install verification"
  make verify 2>&1 | tee "$HOME/tbound-make-verify.log" | tail -30
fi

step "qualification"
say "User-prefix install is complete. Kernel/cgroup qualification is separate and does"
say "not gate this install; use tbound-doctor."

GUEST
  chmod +x "$VM_DIR/provision-guest.sh"
}

print_result() {
  say
  say "TBound VM is ready."
  say "  VM directory : $VM_DIR"
  say "  SSH          : ssh -p $SSH_PORT -i $KEY -o 'UserKnownHostsFile=\"$KNOWN\"' $TRIAL_USER@127.0.0.1"
  say "  Start/stop   : $0 --vm-dir '$VM_DIR' --start | --stop"
  say "  Guest shell  : $0 --vm-dir '$VM_DIR' --ssh"
  say "  Console log  : $CONSOLE"
  say "  Qualification: $QUALIFICATION_STATUS"
  say
  say "The guest install is development-only. Governed serve --pi still requires the"
  say "owner's signed host profile, D09 image, and service provisioning; see"
  say "infra/guest/PROVISIONING-SIGNING.md."
}

# ---------------------------------------------------------------------------

need_host_tools
mkdir -p "$VM_DIR"
chmod 700 "$VM_DIR"

if [[ "$MODE" == status ]]; then
  load_env
  if vm_running; then say "running (pid $(cat "$PIDFILE"))"; else say "stopped"; fi
  exit 0
fi
if [[ "$MODE" == stop ]]; then
  load_env
  stop_qemu
  exit 0
fi
if [[ "$MODE" == ssh ]]; then
  load_env
  exec ssh -p "$SSH_PORT" -i "$KEY" -o "UserKnownHostsFile=\"$KNOWN\"" \
    -o StrictHostKeyChecking=yes "$TRIAL_USER@127.0.0.1"
fi
if [[ "$MODE" == start ]]; then
  load_env
  if vm_running; then
    say "VM already running (pid $(cat "$PIDFILE"))"
  else
    start_qemu
  fi
  wait_ssh start
  print_result
  exit 0
fi
if [[ "$MODE" == check ]]; then
  if kvm_usable; then say "KVM: usable"; else say "KVM: unavailable (TCG fallback)"; fi
  say "VM directory: $VM_DIR"
  say "QEMU: $(qemu-system-x86_64 --version | head -n1)"
  say "cloud-localds: $(command -v cloud-localds)"
  exit 0
fi

# Create mode.
if [[ -e "$DISK" || -e "$SEED" || -e "$KEY" ]]; then
  die "VM files already exist under $VM_DIR; refusing to overwrite. Use --vm-dir for a new VM or stop/remove the old one explicitly."
fi

say "Plan:"
say "  create $VM_NAME in $VM_DIR"
say "  Ubuntu 24.04 cloud image, ${CPUS} vCPU, ${MEMORY_MB} MiB RAM, ${DISK_GB} GiB disk"
say "  trial user: $TRIAL_USER, SSH 127.0.0.1:$SSH_PORT"
say "  install from: $([[ -n "$LOCAL_DIST" ]] && echo "prebuilt $LOCAL_DIST" || echo "source (Go compiler required in guest)")"
say "  install HWE kernel (opt-in, unqualified): $([[ "$INSTALL_HWE" == 1 ]] && echo yes || echo no)"
say "  allow global AppArmor userns sysctl: $([[ "$ALLOW_USERNS_SYSCTL" == 1 ]] && echo yes || echo no)"
say "  developer checks: $([[ "$RUN_DEVELOPER" == 1 ]] && echo yes || echo no)"
if [[ "$INSTALL_HWE" == 1 ]]; then
  warn "The HWE 7.0 kernel provides Landlock ABI 8 but is not qualified; the functional cgroup/reuse probe may fail."
fi
if ! kvm_usable; then
  warn "KVM is not usable; this will use TCG and can take tens of minutes."
fi

download_image
ssh-keygen -q -t ed25519 -N '' -f "$KEY" -C "$VM_NAME"
write_seed
qemu-img create -f qcow2 -F qcow2 -b "$VM_DIR/base/noble-server-cloudimg-amd64.img" "$DISK" "${DISK_GB}G"
cat > "$ENVFILE" <<EOF
VM_NAME=$VM_NAME
TRIAL_USER=$TRIAL_USER
SSH_PORT=$SSH_PORT
CPUS=$CPUS
MEMORY_MB=$MEMORY_MB
DISK_GB=$DISK_GB
EOF
chmod 600 "$ENVFILE" "$KEY"

step "boot the VM"
start_qemu
wait_ssh install

step "prepare guest (swap, cloud-init, HWE)"
ssh_guest 'sudo -n cloud-init status --wait || true'
ssh_guest 'if ! swapon --show | grep -q /swapfile; then sudo -n fallocate -l 2G /swapfile && sudo -n chmod 600 /swapfile && sudo -n mkswap /swapfile >/dev/null && sudo -n swapon /swapfile && echo "/swapfile none swap sw 0 0" | sudo -n tee -a /etc/fstab >/dev/null; fi; free -h | head -3'
if [[ "$INSTALL_HWE" == 1 ]]; then
  set +e
  ssh_guest 'sudo -n apt-get update -qq && sudo -n env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq linux-generic-hwe-24.04'
  hwe_rc=$?
  set -e
  if [[ "$hwe_rc" -eq 0 ]]; then
    say "HWE kernel installed; rebooting."
    ssh_guest 'sudo -n reboot' || true
    sleep 10
    wait_ssh "after HWE reboot"
  else
    warn "HWE install failed (exit $hwe_rc); continuing with the GA kernel. Landlock ABI 8 is not expected."
  fi
fi
ssh_guest 'uname -r; cat /etc/os-release | grep PRETTY_NAME'

step "copy checkout into guest"
tar -C "$ROOT" \
  --exclude='./.git' --exclude='./.local' --exclude='./thesis' --exclude='./dist' --exclude='node_modules' \
  --exclude='*.qcow2' --exclude='*.img' --exclude='*.iso' \
  -czf - . | ssh_guest 'rm -rf "$HOME/trust-boundary" && mkdir -p "$HOME/trust-boundary" && tar -xzf - -C "$HOME/trust-boundary"'

if [[ -n "$LOCAL_DIST" ]]; then
  step "copy prebuilt release into guest"
  tar -C "$LOCAL_DIST" -czf - . | ssh_guest 'rm -rf "$HOME/tbound-dist" && mkdir -p "$HOME/tbound-dist" && tar -xzf - -C "$HOME/tbound-dist"'
fi

step "bootstrap guest host"
set +e
ssh_guest "cd \"\$HOME/trust-boundary\" && sudo -n env TBOUND_ASSUME_YES=1 TBOUND_APPLY_USERNS_SYSCTL=$ALLOW_USERNS_SYSCTL bash infra/repro/bootstrap-linux.sh '$TRIAL_USER'"
bootstrap_rc=$?
set -e
case "$bootstrap_rc" in
  0) QUALIFICATION_STATUS="capability probe passed (host still not G1-qualified)"; say "Guest host bootstrap and capability probe completed." ;;
  1|3|4)
    QUALIFICATION_STATUS="probe reported exit $bootstrap_rc; install continues, host NOT qualified"
    warn "Guest provisioning completed, but the capability/qualification probe reported exit $bootstrap_rc."
    warn "This does not undo the user-prefix install below; inspect the guest log for the exact failed checks."
    ssh_guest 'sudo -n tail -40 /var/log/tbound-repro-bootstrap.log || true'
    ;;
  *)
    warn "bootstrap-linux.sh failed with exit $bootstrap_rc; stopping before install."
    ssh_guest 'sudo -n tail -40 /var/log/tbound-repro-bootstrap.log || true'
    exit "$bootstrap_rc"
    ;;
esac

step "install Pi and tbound"
write_guest_provisioner
scp -q -P "$SSH_PORT" -i "$KEY" -o "UserKnownHostsFile=\"$KNOWN\"" -o StrictHostKeyChecking=yes \
  "$VM_DIR/provision-guest.sh" "$TRIAL_USER@127.0.0.1:/home/$TRIAL_USER/provision-guest.sh"
if [[ -n "$LOCAL_DIST" ]]; then
  guest_dist="\$HOME/tbound-dist"
else
  guest_dist=""
fi
ssh_guest "RUN_DEVELOPER=$RUN_DEVELOPER TBOUND_DIST_VERSION=$DIST_VERSION TBOUND_LOCAL_DIST=\"$guest_dist\" bash \"\$HOME/provision-guest.sh\""

print_result
