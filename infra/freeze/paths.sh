#!/bin/bash
# SOURCED. Every host path of the kit, resolved from the repo itself. Works from WSL, Git Bash and native Linux. No secrets here.
KIT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd) || exit 2
REPO_ROOT=$(cd -- "$KIT_DIR/../.." && pwd) || exit 2
TBOUND_LOCAL=${TBOUND_LOCAL:-$REPO_ROOT/.local}
EVIDENCE=${TBOUND_EVIDENCE:-$REPO_ROOT/evidence}
HOST_ENV=$TBOUND_LOCAL/freeze-host.env
[ -f "$HOST_ENV" ] || { echo "FATAL: no $HOST_ENV (copy infra/freeze/host.env.example there)" >&2; exit 2; }
# shellcheck disable=SC1090
. "$HOST_ENV"
for v in TBOUND_WORK OLDH GO PASSPHRASE_FILE; do
  [[ ${!v:-} == /* ]] || { echo "FATAL: $HOST_ENV must set $v to an absolute Linux path" >&2; exit 2; }
done
# TBOUND_WORK is on a Linux filesystem: drive mounts are read-only from WSL and lose the modes the tests check.
# Select by uname, not wsl.exe: WSL with interop has wsl.exe too, and there TBOUND_WORK is already a Linux path.
case $(uname -s) in
  MINGW* | MSYS*)
    TBOUND_WSL_DISTRO=${TBOUND_WSL_DISTRO:-Ubuntu}
    TBOUND_WORK_VIEW=${TBOUND_WORK_VIEW:-//wsl.localhost/$TBOUND_WSL_DISTRO$TBOUND_WORK}
    on_linux() { MSYS_NO_PATHCONV=1 wsl.exe -d "$TBOUND_WSL_DISTRO" -e bash -c "$1"; }
    linux_path() { case $1 in /[A-Za-z] | /[A-Za-z]/*) local d=${1:1:1}; printf '/mnt/%s%s\n' "${d,,}" "${1:2}" ;; *) printf '%s\n' "$1" ;; esac; }
    ;;
  *)
    TBOUND_WORK_VIEW=${TBOUND_WORK_VIEW:-$TBOUND_WORK}
    on_linux() { bash -c "$1"; }
    linux_path() { printf '%s\n' "$1"; }
    ;;
esac
# Git Bash cannot mkdir -p through //wsl.localhost, so the Linux side creates it.
make_receipts_dir() { on_linux "mkdir -p '$TBOUND_WORK/receipts/$FREEZE/logs'" </dev/null; }
