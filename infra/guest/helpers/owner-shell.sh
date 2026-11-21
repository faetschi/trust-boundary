# Owner-session helpers for the TBound runbook (infra/guest/PROVISIONING-SIGNING.md, Placeholders).
# Source this file after setting the variables below. Sourcing only defines functions.
# GSSH and KSSH run ssh from Git Bash on Windows; WSL cannot reach the guest network.
# Set per session, never defaulted: GUEST_IP (measured this session), HOST_KEY_ALIAS (the name the
# pinned known_hosts entry is stored under), KNOWN_HOSTS (that pinned file), ADMIN_KEY_PATH (the
# admin identity file, readable by this shell), TBP (the frozen tbound-profile binary path).
# A refused call returns non-zero and names the variable or path; it never exits the owner's shell.

# SVC is a guest-side function. GSSH sends this definition ahead of each command, so the `SVC ...`
# words in runbook strings resolve on the guest (service uid 995, no capabilities, no-new-privs).
_OWNER_SVC_DEF='SVC() { sudo -n setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- "$@"; }; '

_owner_need() { # _owner_need VAR...: each named variable must be set and non-empty
  local n
  for n in "$@"; do
    [ -n "${!n:-}" ] || { echo "owner-shell: set $n first (see the runbook Placeholders)" >&2; return 1; }
  done
}

_owner_ssh() { # _owner_ssh KEY TARGET [REMOTE...]: the only place that holds the ssh options
  [ -r "$1" ] || { echo "owner-shell: identity file not readable: $1" >&2; return 1; }
  [ -r "$KNOWN_HOSTS" ] || { echo "owner-shell: known_hosts not readable: $KNOWN_HOSTS" >&2; return 1; }
  MSYS_NO_PATHCONV=1 ssh -i "$1" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes \
    -o UserKnownHostsFile="$KNOWN_HOSTS" -o HostKeyAlias="$HOST_KEY_ALIAS" "${@:2}"
}

GSSH() { # GSSH [COMMAND...]: tboundadmin on the guest with the pinned host key; no command opens a login shell
  _owner_need GUEST_IP HOST_KEY_ALIAS KNOWN_HOSTS ADMIN_KEY_PATH || return
  [ $# -gt 0 ] || { _owner_ssh "$ADMIN_KEY_PATH" "tboundadmin@$GUEST_IP"; return; }
  _owner_ssh "$ADMIN_KEY_PATH" "tboundadmin@$GUEST_IP" "$_OWNER_SVC_DEF$*"
}

KSSH() { # KSSH KEY [COMMAND...]: root on the guest with one forced-command key (SSH-3)
  local key=${1-}
  [ -n "$key" ] || { echo "owner-shell: KSSH needs a key path first: KSSH KEY [COMMAND...]" >&2; return 1; }
  _owner_need GUEST_IP HOST_KEY_ALIAS KNOWN_HOSTS || return
  shift
  _owner_ssh "$key" "root@$GUEST_IP" "$@"
}

D() { # D FILE: digest in the form every pin uses, sha256:<64 hex>; fails on a missing file
  [ -n "${1-}" ] || { echo "owner-shell: usage: D FILE" >&2; return 2; }
  local h
  h=$(sha256sum -- "$1") || return
  echo "sha256:${h%% *}"
}

TBP() { # TBP ARGS...: runs the frozen tbound-profile at $TBP (runbook line 133 sets it)
  _owner_need TBP || return
  "$TBP" "$@"
}
