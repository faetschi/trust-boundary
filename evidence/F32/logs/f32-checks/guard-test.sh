#!/bin/bash
# F31 check: the #143 leftover-container guard in `tbound trial reset` on the real guest, with the pilot service running.
# 1 baseline check passes; 2 create tbound-v-guardtest (uid 995, the service's podman, an image already in the store, not started);
# 3 check refuses and names it; 4 remove it; 5 check passes again. Root steps use password sudo (TBA_PW on stdin to sudo -S only);
# the checks use the trial forced-command key by path. usage: guard-test.sh <out> <case_id>
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
OUT=$1; CASE=$2
GSSH="C:/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb/infra/freeze/tools/gssh.sh"
export TBOUND_LOCAL=F:/trust-boundary/.local
: "${TBA_PW:?}" "${GUEST_IP:?}" "${TBOUND_TRIAL_KEY:?}"
[ -f "$TBOUND_TRIAL_KEY" ] || { echo "no trial key file"; exit 1; }
PSVC='setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- env -C / -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/var/lib/tbound/podman-home USER=tbound-pi LOGNAME=tbound-pi XDG_RUNTIME_DIR=/run/tbound-pi XDG_CONFIG_HOME=/etc/tbound/podman-config XDG_DATA_HOME=/var/lib/tbound/podman-home/data XDG_CACHE_HOME=/run/tbound-pi/cache TMPDIR=/run/tbound-pi/tmp CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf LANG=C LC_ALL=C /usr/bin/podman'
root() { printf '%s\n' "$TBA_PW" | sh "$GSSH" "sudo -S -p '' bash -c $(printf '%q' "PSVC=\"$PSVC\"; $1")" 2>&1; }
check() { GSSH_IDENTITY=$TBOUND_TRIAL_KEY GSSH_USER=root sh "$GSSH" "/usr/local/bin/tbound trial reset $CASE --check" </dev/null 2>&1; echo "rc=$?"; }
{
echo "== guard test start $(date -u +%FT%TZ) guest=$GUEST_IP case=$CASE"
echo "== 0 state"; root 'systemctl is-active tbound-pi.service; $PSVC ps -a --format "{{.Names}} {{.Status}}" 2>/dev/null; echo "-- images:"; $PSVC images --format "{{.ID}} {{.Repository}}:{{.Tag}} {{.Size}}" 2>/dev/null'
echo "== 1 baseline: trial reset $CASE --check (expect a check document)"; check
echo "== 2 create tbound-v-guardtest (not started)"
root 'img=$($PSVC images -q 2>/dev/null | head -1); [ -n "$img" ] || { echo "no image in the store"; exit 1; }; echo "image=$img"; $PSVC create --name tbound-v-guardtest --network none --entrypoint /bin/true "$img" 2>&1 | grep -v "^time=" ; echo "create rc=${PIPESTATUS[0]}"; $PSVC ps -a --format "{{.Names}} {{.Status}}" 2>/dev/null'
echo "== 3 trial reset $CASE --check with the container present (expect a refusal naming tbound-v-guardtest)"; check
echo "== 4 remove tbound-v-guardtest"
root '$PSVC rm tbound-v-guardtest 2>&1 | grep -v "^time="; echo "rm rc=${PIPESTATUS[0]}"; echo "-- ps -a after:"; $PSVC ps -a --format "{{.Names}} {{.Status}}" 2>/dev/null; echo "-- end ps"'
echo "== 5 trial reset $CASE --check after removal (expect a check document)"; check
echo "== guard test end $(date -u +%FT%TZ)"
} >> "$OUT"
