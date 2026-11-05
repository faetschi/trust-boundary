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
# linux_safe: an absolute Linux path, written plainly (no //, /./, /../), not under /mnt. Lexical; natively host_paths_ok also resolves symlinks (not_mnt).
# env_paths_ok: recheck the paths a sourced env.sh or session-env.sh set (a stale file must not point into /mnt).
env_paths_ok() { local v; for v in W B H REPO HF CF LOG; do [ -z "${!v:-}" ] || linux_safe "${!v}" || { echo "FATAL: $v=${!v} is not a plain Linux path outside /mnt" >&2; exit 2; }; done; }
linux_safe() { [[ $1 == /[!/]* && $1 != /mnt && $1 != /mnt/* && $1 != *//* && $1 != */./* && $1 != */. && $1 != */../* && $1 != */.. ]]; }
# host_paths_ok: the host paths are plain Linux paths outside /mnt; natively also HOME, TMPDIR and the kit itself, resolved through symlinks.
# Called here and again after the values file is sourced (lib.sh, guest/glib.sh), since that file could set them too.
not_mnt() { local r; r=$(realpath -m -- "$1") && [[ $r != /mnt && $r != /mnt/* ]]; }
host_paths_ok() {
  local v vs="TBOUND_WORK OLDH GO PASSPHRASE_FILE" native=1
  case $(uname -s) in MINGW* | MSYS*) native=0 ;; *) vs="$vs HOME TMPDIR" ;; esac
  for v in $vs; do
    linux_safe "${!v:-}" && { [ $native = 0 ] || not_mnt "${!v}"; } || { echo "FATAL: $v=${!v:-} must be a plain absolute Linux path outside /mnt" >&2; exit 2; }
  done
  [ $native = 0 ] || not_mnt "$KIT_DIR" || { echo "FATAL: the kit is under /mnt: run it from a Linux checkout" >&2; exit 2; }
  [[ ${PREV_FREEZE:-none} =~ ^(none|F[0-9]+)$ ]] || { echo "FATAL: PREV_FREEZE=$PREV_FREEZE is not none or F<n>" >&2; exit 2; }
}
case $(uname -s) in MINGW* | MSYS*) ;; *) export TMPDIR=${TMPDIR:-/tmp}; unset GOTMPDIR XDG_CACHE_HOME XDG_CONFIG_HOME XDG_DATA_HOME ;; esac
host_paths_ok
# TBOUND_WORK is on a Linux filesystem: drive mounts are read-only from WSL and lose the modes the tests check.
# Select by uname, not wsl.exe: WSL with interop has wsl.exe too, and there TBOUND_WORK is already a Linux path.
case $(uname -s) in
  MINGW* | MSYS*)
    TBOUND_WSL_DISTRO=${TBOUND_WSL_DISTRO:-tbound-dev}
    TBOUND_WORK_VIEW=${TBOUND_WORK_VIEW:-//wsl.localhost/$TBOUND_WSL_DISTRO$TBOUND_WORK}
    on_linux() { MSYS_NO_PATHCONV=1 wsl.exe -d "$TBOUND_WSL_DISTRO" --cd / -e bash -c "$1"; }   # --cd /: never start in the /mnt view of the Windows cwd
    ;;
  *)
    TBOUND_WORK_VIEW=$TBOUND_WORK   # no override natively: the view exists only for Git Bash
    on_linux() { bash -c "$1"; }
    ;;
esac
# Git Bash cannot mkdir -p through //wsl.localhost, so the Linux side creates it.
make_receipts_dir() { on_linux "mkdir -p '$TBOUND_WORK/receipts/$FREEZE/logs'" </dev/null; }
