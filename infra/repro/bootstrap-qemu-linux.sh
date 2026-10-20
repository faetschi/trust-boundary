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
#   --vm-dir DIR      VM directory (default: $HOME/.local/share/tbound-vm)
#   --name NAME       QEMU/cloud-init name (default: tbound-vm)
#   --user USER       Guest trial account (default: tbound)
#   --cpus N          Guest vCPUs (default: 4)
#   --memory MB       Guest RAM (default: 3072; TCG is memory-hungry)
#   --disk GB         Guest disk (default: 40)
#   --port PORT       Host SSH port (default: 2222)
#   --image FILE      Use a local Ubuntu 24.04 cloud qcow2 instead of downloading
#   --no-hwe          Keep the cloud image GA kernel (Landlock ABI < 8)
#   --no-check        Do not run the adapter check in the guest
#   --check           Print host prerequisites and exit
#
# The script never deletes an existing VM directory. It refuses to overwrite a
# disk, SSH key, or seed. Stop an existing VM before creating a new one.
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
VM_DIR="${TBOUND_VM_DIR:-$HOME/.local/share/tbound-vm}"
VM_NAME="tbound-vm"
TRIAL_USER="tbound"
CPUS=4
MEMORY_MB=3072
DISK_GB=40
SSH_PORT=2222
IMAGE=""
INSTALL_HWE=1
RUN_ADAPTER_CHECK=1
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
    --no-hwe) INSTALL_HWE=0; shift ;;
    --no-check) RUN_ADAPTER_CHECK=0; shift ;;
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
  ssh -p "$SSH_PORT" -i "$KEY" -o UserKnownHostsFile="$KNOWN" \
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
    if ssh -p "$SSH_PORT" -i "$KEY" -o UserKnownHostsFile="$KNOWN" \
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
  if [[ ! -f "$image" || ! -f "$base/SHA256SUMS" ]]; then
    say "Downloading Ubuntu 24.04 cloud image and signed checksum manifest ..."
    curl --fail --location --proto '=https' --tlsv1.2 --retry 3 \
      -o "$base/SHA256SUMS" \
      https://cloud-images.ubuntu.com/noble/current/SHA256SUMS
    curl --fail --location --proto '=https' --tlsv1.2 --retry 3 \
      -o "$image" \
      https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
  fi
  ( cd "$base" && sha256sum -c <(grep 'noble-server-cloudimg-amd64.img' SHA256SUMS) )
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
mkdir -m 700 -p "$TMPDIR"
umask 077
cd "$HOME/trust-boundary"

step() { printf '\n===== %s =====\n' "$*"; }

step "official Pi CLI 0.87.1"
npm_config_prefix="$HOME/.local" npm install --global --ignore-scripts \
  @earendil-works/pi-coding-agent@0.87.1
export PATH="$HOME/.local/bin:$PATH"
hash -r
pi --version

step "adapter dependencies"
cd "$HOME/trust-boundary/adapter"
npm ci --ignore-scripts --no-audit --no-fund
if [[ "${RUN_ADAPTER_CHECK:-1}" == 1 ]]; then
  step "adapter check (slow-VM timeout override)"
  TBOUND_TEST_TIMEOUT_MS=120000 npm run check
fi

step "build and install tbound"
cd "$HOME/trust-boundary"
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
hash -r

step "doctor"
tbound-doctor --pi "$(command -v pi)" | tee "$HOME/tbound-doctor.txt"
tbound-doctor --json > "$HOME/tbound-doctor.json" 2>/dev/null || true

step "native fixture"
cd "$HOME/trust-boundary"
tbound serve --pi --native-fixture | tee "$HOME/tbound-native-fixture.json"

step "offline Pi CLI development fixture"
export TBOUND_DEV_FIXTURE_PI="$(realpath "$(command -v pi)")"
export TBOUND_DEV_FIXTURE_NODE="$(realpath "$(command -v node)")"
export TBOUND_DEV_FIXTURE_ADAPTER="$HOME/trust-boundary/adapter"
export TBOUND_DEV_FIXTURE_ROOT="$HOME/.local/state/tbound-dev-fixture"
mkdir -m 700 -p "$TBOUND_DEV_FIXTURE_ROOT"
tbound serve --pi --dev-fixture | tee "$HOME/tbound-dev-fixture.json"

step "install verification"
make verify 2>&1 | tee "$HOME/tbound-make-verify.log" | tail -30

step "governed-host advisory"
bash scripts/check-governed-host.sh > "$HOME/tbound-governed-host.txt" 2>&1 || true
tail -8 "$HOME/tbound-governed-host.txt"

step "done"
GUEST
  chmod +x "$VM_DIR/provision-guest.sh"
}

print_result() {
  say
  say "TBound VM is ready."
  say "  VM directory : $VM_DIR"
  say "  SSH          : ssh -p $SSH_PORT -i $KEY -o UserKnownHostsFile=$KNOWN $TRIAL_USER@127.0.0.1"
  say "  Start/stop   : $0 --vm-dir '$VM_DIR' --start | --stop"
  say "  Guest shell  : $0 --vm-dir '$VM_DIR' --ssh"
  say "  Console log  : $CONSOLE"
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
  exec ssh -p "$SSH_PORT" -i "$KEY" -o UserKnownHostsFile="$KNOWN" \
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
say "  install HWE kernel: $([[ "$INSTALL_HWE" == 1 ]] && echo yes || echo no)"
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
  --exclude='./.git' --exclude='./dist' --exclude='node_modules' \
  --exclude='*.qcow2' --exclude='*.img' --exclude='*.iso' \
  -czf - . | ssh_guest 'rm -rf "$HOME/trust-boundary" && mkdir -p "$HOME/trust-boundary" && tar -xzf - -C "$HOME/trust-boundary"'

step "bootstrap guest host"
set +e
ssh_guest "cd \"\$HOME/trust-boundary\" && sudo -n env TBOUND_ASSUME_YES=1 RUN_ADAPTER_CHECK=$RUN_ADAPTER_CHECK bash infra/repro/bootstrap-linux.sh '$TRIAL_USER'"
bootstrap_rc=$?
set -e
if [[ "$bootstrap_rc" -ne 0 ]]; then
  warn "bootstrap-linux.sh exited $bootstrap_rc; inspect the guest log before continuing."
  ssh_guest 'sudo -n tail -40 /var/log/tbound-repro-bootstrap.log || true'
  exit "$bootstrap_rc"
fi

step "install Pi and tbound"
write_guest_provisioner
scp -q -P "$SSH_PORT" -i "$KEY" -o UserKnownHostsFile="$KNOWN" -o StrictHostKeyChecking=yes \
  "$VM_DIR/provision-guest.sh" "$TRIAL_USER@127.0.0.1:/home/$TRIAL_USER/provision-guest.sh"
ssh_guest "RUN_ADAPTER_CHECK=$RUN_ADAPTER_CHECK bash \"\$HOME/provision-guest.sh\""

print_result
