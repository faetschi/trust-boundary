#!/usr/bin/env bash
# Checks what a development checkout needs: build tools, tests, and the dev fixture.
# It reads the system and changes nothing. Governed-host requirements are warnings
# only, because they are not needed to build or test. See docs/guides/setup.md.
set -u

GO_MIN=1.26.0      # supervisor/go.mod
GO_PIN=1.27.1      # reproducibility pin (docs/guides/setup.md)
NODE_MIN=22.19.0   # Pi and the installer
NODE_PIN=24.21.0   # reproducibility pin

fail=0
ok()   { printf '  ok       %s\n' "$1"; }
warn() { printf '  warn     %s\n           -> %s\n' "$1" "$2"; }
miss() { printf '  missing  %s\n           -> %s\n' "$1" "$2"; fail=1; }
have() { command -v "$1" >/dev/null 2>&1; }
# version_at_least HAVE MIN: succeeds when HAVE >= MIN (sort -V on the two versions)
version_at_least() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]; }

echo "TBound development preflight"

if [ "$(uname -s 2>/dev/null)" != Linux ]; then
  miss "Linux" "run this inside Linux: a Linux host, a Linux VM, or WSL. Native Windows and macOS are not supported."
  echo
  echo "Stopped: nothing else was checked."
  exit 1
fi

echo "Required to build and test:"

if have go; then
  have_go="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  if version_at_least "$have_go" "$GO_MIN"; then
    if [ "$have_go" = "$GO_PIN" ]; then ok "go $have_go"; else warn "go $have_go (the pin is $GO_PIN)" "install Go $GO_PIN from https://go.dev/dl/ for reproducible results"; fi
  else
    miss "go $have_go (need $GO_MIN or newer)" "install Go $GO_PIN from https://go.dev/dl/"
  fi
else
  miss "go" "install Go $GO_PIN from https://go.dev/dl/"
fi

if have node; then
  have_node="$(node --version 2>/dev/null | sed 's/^v//')"
  if version_at_least "$have_node" "$NODE_MIN"; then
    if [ "$have_node" = "$NODE_PIN" ]; then ok "node $have_node"; else warn "node $have_node (the pin is $NODE_PIN)" "tests need Node $NODE_MIN or newer; $NODE_PIN is the reproducibility pin"; fi
  else
    miss "node $have_node (need $NODE_MIN or newer)" "install Node $NODE_PIN, for example with nvm or a distribution package"
  fi
else
  miss "node" "install Node $NODE_PIN; 'make test' runs Node-based supervisor tests"
fi

for tool in make git bash python3 tar sha256sum; do
  if have "$tool"; then ok "$tool"; else miss "$tool" "install it with your package manager (for Ubuntu: sudo apt install make git python3 coreutils tar)"; fi
done

if have gcc; then ok "gcc (cgo, race tests)"; else warn "gcc missing" "needed only for 'go test -race' (install build-essential)"; fi

echo
echo "Needed for the offline Pi fixture (docs/guides/setup.md):"

if have pi; then
  have_pi="$(pi --version 2>/dev/null | head -n1)"
  case "$have_pi" in
    *0.87.1*) ok "pi $have_pi" ;;
    *) warn "pi ${have_pi:-unknown} (the fixture needs 0.87.1)" "npm install --global @earendil-works/pi-coding-agent@0.87.1" ;;
  esac
else
  warn "pi not on PATH" "npm install --global @earendil-works/pi-coding-agent@0.87.1 (the fixture needs version 0.87.1)"
fi

echo
echo "Needed only for a governed host (not for development):"

if [ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null)" = cgroup2fs ]; then
  ok "cgroup v2 (unified hierarchy)"
else
  warn "cgroup v2 not detected" "governed runs need the unified cgroup v2 hierarchy"
fi

if grep -qw landlock /sys/kernel/security/lsm 2>/dev/null; then
  ok "Landlock listed in /sys/kernel/security/lsm"
  warn "Landlock ABI not verified here" "run infra/repro/probe-landlock-abi8.sh; governed runs need exactly ABI 8"
else
  warn "Landlock not listed by the kernel" "governed runs need Landlock ABI 8"
fi

for tool in podman crun cosign systemctl; do
  if have "$tool"; then ok "$tool"; else warn "$tool missing" "needed for a governed host only; see docs/guides/governed-setup.md"; fi
done

echo
if [ "$fail" -eq 0 ]; then
  echo "Development preflight passed. Next: docs/guides/quickstart.md"
else
  echo "Development preflight failed: fix the 'missing' lines above and run it again."
fi
exit "$fail"
