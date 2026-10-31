#!/bin/sh
export MSYS_NO_PATHCONV=1
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
LOCAL=${TBOUND_LOCAL:-$ROOT/.local}
KNOWN_HOSTS=${TBOUND_GUEST_KNOWN_HOSTS:-$ROOT/evidence/guest-known-hosts.txt}
HOST_ALIAS=${TBOUND_GUEST_HOST_ALIAS:-172.18.155.19}
exec ssh -T \
  -i "$LOCAL/secrets/abi8-qualification/id_ed25519" \
  -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes \
  -o PreferredAuthentications=publickey -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no \
  -o StrictHostKeyChecking=yes -o HostKeyAlgorithms=ssh-ed25519 \
  -o UserKnownHostsFile="$KNOWN_HOSTS" \
  -o GlobalKnownHostsFile=/dev/null -o ConnectTimeout=20 \
  -o ControlMaster=no -o ControlPath=none -o ForwardAgent=no -o ForwardX11=no \
  -o HostKeyAlias="$HOST_ALIAS" tboundadmin@"${GUEST_IP:?set GUEST_IP}" "$@"
