#!/bin/bash
# Tests infra/guest/helpers/owner-shell.sh offline. Stub ssh, sudo and tbound-profile go first on
# PATH and only record their argv, so nothing connects. Run: bash infra/guest/helpers/test-owner-shell.sh
set -euo pipefail

here=$(cd -- "$(dirname -- "$0")" && pwd)
helper="$here/owner-shell.sh"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
mkdir -p "$work/bin"
printf 'dummy identity file\n' > "$work/admin.key"    # only its path reaches ssh; no real key is read
printf 'dummy pin file\n' > "$work/known_hosts"
printf 'abc' > "$work/abc"

cat > "$work/bin/ssh" <<'STUB'
#!/bin/bash
printf '%s\0' "$@" > "$OWNER_SHELL_LOG/ssh.argv"
printf '%s' "${MSYS_NO_PATHCONV-unset}" > "$OWNER_SHELL_LOG/ssh.env"
STUB
cat > "$work/bin/sudo" <<'STUB'
#!/bin/bash
printf '%s\0' "$@" > "$OWNER_SHELL_LOG/sudo.argv"
STUB
cat > "$work/fake-tbound-profile" <<'STUB'
#!/bin/bash
printf '%s\0' "$@" > "$OWNER_SHELL_LOG/tbp.argv"
STUB
chmod +x "$work/bin/ssh" "$work/bin/sudo" "$work/fake-tbound-profile"

export PATH="$work/bin:$PATH"
export OWNER_SHELL_LOG="$work/log"
export GUEST_IP=192.0.2.10 HOST_KEY_ALIAS=guest-alias.test
export KNOWN_HOSTS="$work/known_hosts" ADMIN_KEY_PATH="$work/admin.key" TBP="$work/fake-tbound-profile"
export PODMAN_SVC='SVC env -C / -i PATH=/usr/bin:/bin /usr/bin/podman'

failures=0
ok() { echo "ok - $1"; }
bad() { echo "not ok - $1" >&2; failures=$((failures + 1)); }
check() { local label=$1; shift; if "$@"; then ok "$label"; else bad "$label"; fi; }
reset() { rm -rf -- "$OWNER_SHELL_LOG"; mkdir -p "$OWNER_SHELL_LOG"; }

runner='set -u; . "$1"; shift; "$@"'
run() { bash -c "$runner" owner-shell "$helper" "$@"; }

same_argv() { # same_argv FILE WANT...: FILE holds exactly WANT as NUL-separated arguments
  local file=$1; shift
  [ -f "$file" ] || return 1
  local -a got=() want=("$@")
  mapfile -d '' -t got < "$file"
  [ "${#got[@]}" -eq "${#want[@]}" ] || return 1
  [ "$(printf '%s\037' "${got[@]}")" = "$(printf '%s\037' "${want[@]}")" ]
}

ssh_remote() { # the last argument the ssh stub received, which is the remote command
  [ -f "$OWNER_SHELL_LOG/ssh.argv" ] || return 0
  local -a got=()
  mapfile -d '' -t got < "$OWNER_SHELL_LOG/ssh.argv"
  [ "${#got[@]}" -gt 0 ] || return 0
  printf '%s' "${got[-1]}"
}

refuses() { # refuses LABEL MUST_SAY ENV WORDS...: the call fails and says MUST_SAY; no stub ran
  local label=$1 says=$2 envspec=$3; shift 3
  local -a envargs=()
  read -ra envargs <<< "$envspec"
  reset
  local err rc=0
  err=$(env "${envargs[@]}" bash -c "$runner" owner-shell "$helper" "$@" 2>&1 >/dev/null) || rc=$?
  if [ "$rc" -ne 0 ] && [[ $err == *"$says"* ]] && [ -z "$(ls -A "$OWNER_SHELL_LOG")" ]; then
    ok "$label"
  else
    bad "$label (exit $rc; stderr: $err)"
  fi
}

gssh_head=(-i "$work/admin.key" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$work/known_hosts" -o HostKeyAlias=guest-alias.test tboundadmin@192.0.2.10)
svc_prefix=(-n setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs --)

echo "# GSSH"
reset
run GSSH 'SVC tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime'
check "GSSH: ssh gets the pinned options, GUEST_IP host, ADMIN_KEY_PATH key, one remote command" \
  same_argv "$OWNER_SHELL_LOG/ssh.argv" "${gssh_head[@]}" "$(ssh_remote)"
check "GSSH: ssh runs with MSYS_NO_PATHCONV=1" [ "$(cat "$OWNER_SHELL_LOG/ssh.env")" = 1 ]
remote=$(ssh_remote)
rc=0; bash -c "$remote" >/dev/null 2>&1 || rc=$?
check "GSSH: the guest shell resolves SVC to the service prefix (exit $rc)" \
  same_argv "$OWNER_SHELL_LOG/sudo.argv" "${svc_prefix[@]}" tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime

reset
run GSSH "$PODMAN_SVC image inspect localhost/tbound/pi-cell:latest"
remote=$(ssh_remote)
rc=0; bash -c "$remote" >/dev/null 2>&1 || rc=$?
check "GSSH: a PODMAN_SVC string runs under the service prefix (exit $rc)" \
  same_argv "$OWNER_SHELL_LOG/sudo.argv" "${svc_prefix[@]}" env -C / -i PATH=/usr/bin:/bin /usr/bin/podman image inspect localhost/tbound/pi-cell:latest

reset
run GSSH
check "GSSH with no command: ssh gets only the target, so it opens a login shell" \
  same_argv "$OWNER_SHELL_LOG/ssh.argv" "${gssh_head[@]}"

echo "# KSSH"
reset
run KSSH "$work/admin.key" /usr/local/bin/tbound evidence e04-snapshot --root /etc --point baseline
check "KSSH: the explicit key argument, root@GUEST_IP, command words stay separate" \
  same_argv "$OWNER_SHELL_LOG/ssh.argv" -i "$work/admin.key" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$work/known_hosts" -o HostKeyAlias=guest-alias.test \
  root@192.0.2.10 /usr/local/bin/tbound evidence e04-snapshot --root /etc --point baseline
check "KSSH: ssh runs with MSYS_NO_PATHCONV=1" [ "$(cat "$OWNER_SHELL_LOG/ssh.env")" = 1 ]

echo "# TBP and D"
reset
run TBP init --out x
check "TBP: runs the binary named by TBP with the words unchanged" same_argv "$OWNER_SHELL_LOG/tbp.argv" init --out x

got=$(run D "$work/abc") || got=FAILED
check "D: prints sha256:<64 hex> of the file" [ "$got" = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" ]
rc=0; got=$(run D "$work/missing" 2>/dev/null) || rc=$?
if [ "$rc" -ne 0 ] && [ -z "$got" ]; then ok "D: a missing file fails and prints no digest"; else bad "D: a missing file must fail with no output (exit $rc, out '$got')"; fi

echo "# sourcing and refusals"
reset
rc=0; out=$(env -i PATH="$PATH" bash -c 'set -u; . "$1"' owner-shell "$helper" 2>&1) || rc=$?
if [ "$rc" -eq 0 ] && [ -z "$out" ] && [ ! -e "$OWNER_SHELL_LOG/ssh.argv" ]; then
  ok "sourcing needs no variables, prints nothing, calls nothing"
else
  bad "sourcing needs no variables (exit $rc, output '$out')"
fi
refuses "GSSH without GUEST_IP is refused" "owner-shell: set GUEST_IP first" "-u GUEST_IP" GSSH id
refuses "GSSH with an empty GUEST_IP is refused" "owner-shell: set GUEST_IP first" "GUEST_IP=" GSSH id
refuses "GSSH without HOST_KEY_ALIAS is refused" "owner-shell: set HOST_KEY_ALIAS first" "-u HOST_KEY_ALIAS" GSSH id
refuses "GSSH without KNOWN_HOSTS is refused" "owner-shell: set KNOWN_HOSTS first" "-u KNOWN_HOSTS" GSSH id
refuses "GSSH without ADMIN_KEY_PATH is refused" "owner-shell: set ADMIN_KEY_PATH first" "-u ADMIN_KEY_PATH" GSSH id
refuses "GSSH with a missing identity file is refused" "owner-shell: identity file not readable: $work/no-such.key" "ADMIN_KEY_PATH=$work/no-such.key" GSSH id
refuses "KSSH without a key path is refused" "owner-shell: KSSH needs a key path first" "" KSSH
refuses "KSSH without GUEST_IP is refused" "owner-shell: set GUEST_IP first" "-u GUEST_IP" KSSH "$work/admin.key" id
refuses "TBP without TBP is refused" "owner-shell: set TBP first" "-u TBP" TBP init
refuses "D without a file is refused" "owner-shell: usage: D FILE" "" D

echo "# files"
if grep -q $'\r' "$helper" "$0"; then bad "helper and test are LF-only"; else ok "helper and test are LF-only"; fi
check "helper parses as bash" bash -n "$helper"

if [ "$failures" -ne 0 ]; then echo "$failures check(s) failed" >&2; exit 1; fi
echo "all owner-shell checks passed"
