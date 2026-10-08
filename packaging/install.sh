#!/usr/bin/env bash
# Bootstrap installer for the TBound tarball. Installs the CLI, creates the
# managed directories, installs the systemd user unit, then delegates runtime
# bundle / host-profile / cell provisioning to `tbound install` / `tbound doctor`.
# Re-runnable and fail-closed. Does not require network beyond `tbound install`.
set -euo pipefail

prefix="${PREFIX:-/usr/local}"
bindir="$prefix/bin"
etc_dir="${ETC_DIR:-/etc/tbound}"
root="${TBOUND_ROOT:-/opt/tbound}"
user="${TBOUND_USER:-${SUDO_USER:-$(id -un)}}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ "$(id -u)" = "0" ]; then
  echo "run install.sh as the trial user, not root (it uses sudo only where needed)" >&2
fi
if [ ! -f "$here/bin/tbound" ]; then
  echo "bin/tbound not found next to install.sh" >&2
  exit 1
fi

echo "installing tbound to $bindir/tbound"
sudo install -d -m 0755 "$bindir"
sudo install -m 0755 "$here/bin/tbound" "$bindir/tbound"

echo "creating $etc_dir (0750) and $root (0750)"
sudo install -d -m 0750 "$etc_dir"
sudo install -d -m 0750 "$root"

if [ -f "$here/systemd/tbound-user.service" ]; then
  unit_dir="$HOME/.config/systemd/user"
  mkdir -p "$unit_dir"
  install -m 0644 "$here/systemd/tbound-user.service" "$unit_dir/tbound.service"
  echo "installed user unit at $unit_dir/tbound.service"
  echo "enable later with: systemctl --user daemon-reload && systemctl --user enable --now tbound.service"
fi

echo "running host preflight (tbound doctor)"
"$bindir/tbound" doctor || {
  echo "preflight did not pass; fix the reported gaps before installing the runtime" >&2
  exit 1
}

echo
echo "Next (operator-gated):"
echo "  1. provision $etc_dir/pi-host-profile.json (+ signature + public key)"
echo "  2. build/sign the cell image and publish the cosign public key"
echo "  3. run: tbound install --manifest <runtime-bundle.json>"
echo
echo "User: $user  Prefix: $prefix  Config: $etc_dir  Runtime root: $root"
