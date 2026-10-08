#!/usr/bin/env bash
# Reverses install.sh. Removes ONLY TBound-managed files. Never removes the
# managed runtime bundle (/opt/tbound) unless --purge is given, and never
# touches a user's Pi/npm installation.
set -euo pipefail

prefix="${PREFIX:-/usr/local}"
bindir="$prefix/bin"
etc_dir="${ETC_DIR:-/etc/tbound}"
root="${TBOUND_ROOT:-/opt/tbound}"
dry_run=0
purge=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry_run=1 ;;
    --purge) purge=1 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

run() {
  if [ "$dry_run" = "1" ]; then echo "would: $*"; else "$@"; fi
}

unit_dir="$HOME/.config/systemd/user/tbound.service"
if [ -f "$unit_dir" ]; then
  echo "stopping/disabling user unit"
  run systemctl --user disable --now tbound.service || true
  run rm -f "$unit_dir"
fi

if [ -f "$bindir/tbound" ]; then
  run sudo rm -f "$bindir/tbound"
fi

# Config dir: only remove when it carries the TBound ownership marker.
if [ -f "$etc_dir/.tbound-managed" ]; then
  run sudo rm -rf "$etc_dir"
elif [ -d "$etc_dir" ]; then
  echo "leaving $etc_dir in place (no TBound ownership marker)"
fi

if [ "$purge" = "1" ]; then
  if [ -f "$root/.tbound-managed" ]; then
    run sudo rm -rf "$root"
  else
    echo "refusing to purge $root: no TBound ownership marker" >&2
  fi
else
  echo "leaving runtime bundle $root (use --purge to remove it)"
fi

echo "uninstall complete"
