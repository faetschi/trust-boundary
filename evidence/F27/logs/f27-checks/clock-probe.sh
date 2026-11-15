#!/bin/bash
# Read-only guest clock probe. One ssh session prints the guest wall clock once per second; the host stamps each line on arrival.
# offset = guest - host_arrival (true offset is larger by the one-way delay, a few ms). Retries until the guest answers. No secrets.
# usage: clock-probe.sh <guest-ip> <total-seconds> <stream-seconds> <out>
IP=$1; N=$2; K=$3; OUT=$4
GSSH="F:/trust-boundary/.local/worktrees/f27-guest/infra/freeze/tools/gssh.sh"
end=$(( $(date +%s) + N ))
while [ "$(date +%s)" -lt "$end" ]; do
  echo "connect attempt host=$(date -u +%FT%T.%NZ)" >> "$OUT"
  GUEST_IP=$IP TBOUND_LOCAL=F:/trust-boundary/.local timeout $((K + 30)) sh "$GSSH" "i=0; while [ \$i -lt $K ]; do echo \$(date -u +%s.%N) \$(cut -d' ' -f1 /proc/uptime); i=\$((i+1)); sleep 1; done" </dev/null 2>>"$OUT.err" |
  while read -r g up; do
    h=$(date -u +%s.%N)
    awk -v h="$h" -v g="$g" -v up="$up" 'BEGIN{printf "host=%.3f guest=%.3f offset_guest_minus_host=%+.3f guest_uptime=%s\n", h, g, g-h, up}' >> "$OUT"
  done
  grep -q '^host=' "$OUT" 2>/dev/null && [ "$(grep -c '^host=' "$OUT")" -ge "$K" ] && break
  sleep 1
done
