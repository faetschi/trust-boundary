#!/bin/bash
# F31 checks (lsns/conmon spot check, leftover check): one password-sudo ssh session runs a read-only root loop on the guest (poll 0.5 s), started before G1.
#  check 3: the first time each cells scope holds a process, print lsns, each cell process's user-ns chain to the init ns with the
#           uid_map of every level, the uid-995 processes and conmon's cgroup.
#  check 4: the runbook leftover-container command ($PODMAN_SVC ps --filter name=tbound-accept- --filter name=tbound-v-) each time
#           tbound-pi.service turns active (svc start) and after each captured cell run ends with the service still active.
# Output streams back over ssh; nothing is written on the guest. TBA_PW goes on stdin to sudo -S only.
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
OUT=$1; SECS=$2; MAXC=$3
GSSH="C:/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb/infra/freeze/tools/gssh.sh"
export TBOUND_LOCAL=F:/trust-boundary/.local
: "${TBA_PW:?}" "${GUEST_IP:?}"
LOOP='C=/sys/fs/cgroup/system.slice/tbound-pi.service/cells; end=$(( $(date +%s) + $1 )); n=0; seen=" "; open=""; prev=inactive
init=$(readlink /proc/1/ns/user | tr -dc 0-9)
PODMAN_SVC="setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- env -C / -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/var/lib/tbound/podman-home USER=tbound-pi LOGNAME=tbound-pi XDG_RUNTIME_DIR=/run/tbound-pi XDG_CONFIG_HOME=/etc/tbound/podman-config XDG_DATA_HOME=/var/lib/tbound/podman-home/data XDG_CACHE_HOME=/run/tbound-pi/cache TMPDIR=/run/tbound-pi/tmp CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf LANG=C LC_ALL=C /usr/bin/podman"
leftover() {
  echo "== leftover-check $(date -u +%FT%T.%NZ) reason=$1 service=$(systemctl is-active tbound-pi.service)"
  o=$($PODMAN_SVC ps --filter name=tbound-accept- --filter name=tbound-v- --format "{{.Names}} {{.Status}}" 2>&1); rc=$?
  echo "podman ps rc=$rc lines=$(printf %s "$o" | grep -c .)"; [ -z "$o" ] || printf "%s\n" "$o"
  echo "== end leftover-check"
}
echo "guest loop start $(date -u +%FT%T.%NZ) init_userns=$init"
while [ $(date +%s) -lt $end ]; do
  st=$(systemctl is-active tbound-pi.service)
  if [ "$st" = active ] && [ "$prev" != active ]; then sleep 3; leftover svc-start; fi
  prev=$st
  if [ -n "$open" ] && [ -z "$(find $C/$open -name cgroup.procs -exec cat {} + 2>/dev/null)" ]; then
    [ "$(systemctl is-active tbound-pi.service)" = active ] && leftover "after-cell-run $open"; open=""
  fi
  if [ -d $C ] && [ $n -lt $2 ]; then for s in $(ls $C 2>/dev/null); do
    [ -d $C/$s ] || continue
    case "$seen" in *" $s "*) continue;; esac
    [ -n "$(find $C/$s -name cgroup.procs -exec cat {} + 2>/dev/null)" ] || continue
    sleep 2
    [ -n "$(find $C/$s -name cgroup.procs -exec cat {} + 2>/dev/null)" ] || continue
    seen="$seen$s "; n=$((n+1)); open=$s
    echo "== capture $(date -u +%FT%T.%NZ) scope=$s"
    echo "-- lsns -t user"; lsns -t user -o NS,PNS,ONS,NPROCS,PID,UID,USER,COMMAND
    echo "-- cgroup tree below cells"; find $C -name cgroup.procs | while read f; do echo "${f#/sys/fs/cgroup}: $(tr "\n" " " < $f)"; done
    echo "-- cell processes: user-ns chain to the init ns; [uid_map of a member of each level: inner outer count]"
    for p in $(find $C -name cgroup.procs -exec cat {} + 2>/dev/null); do
      x=$(readlink /proc/$p/ns/user 2>/dev/null | tr -dc 0-9); [ -n "$x" ] || continue; chain=""; d=0
      while [ "$x" != "$init" ] && [ $d -lt 8 ]; do
        m=$(lsns -n -t user -o NS,PNS,PID | awk -v n=$x "\$1==n{print \$2\" \"\$3}" | head -1); [ -n "$m" ] || break
        mp=${m#* }; chain="$chain $x[uid_map: $(tr -s " " < /proc/$mp/uid_map | tr "\n" ";")]"; x=${m%% *}; d=$((d+1))
      done
      echo "pid=$p comm=$(cat /proc/$p/comm 2>/dev/null) hostuid=$(awk "/^Uid:/{print \$2}" /proc/$p/status 2>/dev/null) cg=$(cut -d: -f3 /proc/$p/cgroup 2>/dev/null) levels_below_init=$d chain:$chain -> $init(init)"
    done
    echo "-- uid-995 processes"
    for p in $(pgrep -u 995); do echo "pid=$p ppid=$(awk "/^PPid:/{print \$2}" /proc/$p/status 2>/dev/null) comm=$(cat /proc/$p/comm 2>/dev/null) userns=$(readlink /proc/$p/ns/user 2>/dev/null) cg=$(cut -d: -f3 /proc/$p/cgroup 2>/dev/null)"; done
    echo "-- conmon"
    for p in $(pgrep -x conmon); do echo "pid=$p uid=$(awk "/^Uid:/{print \$2}" /proc/$p/status) userns=$(readlink /proc/$p/ns/user) cgroup=$(cut -d: -f3 /proc/$p/cgroup)"; done
    echo "== end capture"
    break
  done; fi
  sleep 0.5
done
echo "guest loop end $(date -u +%FT%T.%NZ) captures=$n"'
echo "watch start $(date -u +%FT%TZ)" >> "$OUT"
printf '%s\n' "$TBA_PW" | timeout $((SECS + 120)) sh "$GSSH" "sudo -S -p '' bash -c $(printf '%q' "$LOOP") _ $SECS $MAXC" >> "$OUT" 2>&1
echo "watch end $(date -u +%FT%TZ) rc=$?" >> "$OUT"
