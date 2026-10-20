#!/bin/sh
# P12 (owner): install the OpenCode Go key on the guest. Run in Git Bash from the repo root:  sh infra/guest/helpers/p12-provider-key.sh
# The key is read without echo and streamed over the pinned SSH channel into /etc/tbound/provider/opencode-go.key (995:986 0400).
# It never appears on a command line, in a log, or in the agent transcript.
set -eu
: "${GUEST_IP:=172.29.201.195}"; export GUEST_IP
G=${GSSH_HELPER:-/f/TBoundAssets/recovery-20261015/F0-c7f9e9c/gssh.sh}   # pinned-SSH helper; override with GSSH_HELPER
printf 'OpenCode Go API key (input hidden): '; stty -echo; IFS= read -r K; stty echo; printf '\n'
[ -n "$K" ] || { echo "empty key, nothing installed"; exit 1; }
printf '%s' "$K" | sh "$G" 'umask 077; sudo -n install -d -o root -g root -m 0755 /etc/tbound/provider && sudo -n install -o 995 -g 986 -m 0400 /dev/stdin /etc/tbound/provider/opencode-go.key'
unset K
sh "$G" 'sudo -n stat -c "%U:%G %a %s bytes %F" /etc/tbound/provider/opencode-go.key; namei -mo /etc/tbound/provider/opencode-go.key'
echo "P12 done"
