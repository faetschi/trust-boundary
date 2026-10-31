#!/bin/bash
# 00 [PRIVILEGED WRITE: sudoers] open or close the $FREEZE session sudo drop-in on the guest (run in Git Bash).
#   open  : uses tboundadmin's PASSWORD sudo (owner-authorized password in the environment variable TBA_PW) to install
#           /etc/sudoers.d/91-tbound-$FREEZE_LC-session = "tboundadmin ALL=(ALL) NOPASSWD: ALL" (visudo -cf before it becomes active).
#           The scripts 10-upgrade.sh, 20-r1.sh and 50-i1-i2.sh need it; 27-archive-state.sh (option A; or 28-abandon-g1.sh if abandon is possible; both run BEFORE open, on the guest that still runs $PREV_FREEZE), 29-g1-recover.sh (password sudo, G1-5 step 2) and 30-reg-g1.sh must run with it CLOSED.
#   close : removes the drop-in and proves the narrowed state again:
#             - `sudo -n true` fails;
#             - `sudo -n -l` shows exactly ONE NOPASSWD entry (NOPASSWD: /opt/tbound/operator/tbound-op) and the password
#               rule (ALL : ALL) ALL; no other NOPASSWD, no NOPASSWD: ALL.
#   status: read-only report of the same facts.
# The password is read from the environment only: never written to a file, never in argv (it travels on the stdin of the ssh
# session into `sudo -S -p ''`), never printed. The script refuses to run under xtrace. Export it in the same Git Bash window:
#     read -rs TBA_PW; export TBA_PW
# Usage: KIT_FREEZE=$FREEZE bash 00-session-sudo.sh open|close|status        (logs: $GOUT\logs\00-session-sudo.log; each call appends)
set -euo pipefail
case "$-" in *x*) echo "FATAL: refusing to run under xtrace (it would print the password)" >&2; exit 1;; esac
case ":${SHELLOPTS:-}:" in *:xtrace:*) echo "FATAL: SHELLOPTS has xtrace" >&2; exit 1;; esac
unset BASH_XTRACEFD || true
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 00-session-sudo

MODE=${1:-}
case "$MODE" in open|close|status) ;; *) die "usage: bash 00-session-sudo.sh open|close|status";; esac

DROPIN=/etc/sudoers.d/91-tbound-$FREEZE_LC-session
LINE='tboundadmin ALL=(ALL) NOPASSWD: ALL'

# Password-sudo transport: the password goes on the stdin of the ssh session, the command reads it with -S and no prompt.
need_pw() { [ -n "${TBA_PW:-}" ] || die "TBA_PW is not set (read -rs TBA_PW; export TBA_PW)"; }
GPW() { printf '%s\n' "$TBA_PW" | sh "$GSSH_SH" "$@"; }

# sudo_any <command...>: run as root through the cheapest working path (NOPASSWD if the drop-in is open, else password sudo).
sudo_any() {
  if G 'sudo -n true' >/dev/null 2>&1; then G "sudo -n $*"; else need_pw; GPW "sudo -S -p '' $*"; fi
}

narrow_report() { # prints the facts; returns 0 if narrowed
  local rc=0 l nopw
  say "narrowed-sudo facts"
  if G 'sudo -n true' >/dev/null 2>&1; then echo "sudo -n true: WORKS (session drop-in or other NOPASSWD ALL is active)"; rc=1; else echo "sudo -n true: fails (good)"; fi
  l=$(G 'sudo -n -l 2>&1' | tr -d '\r' || true)
  if [[ $l == *"a password is required"* || $l == *"password is required"* ]]; then
    need_pw; l=$(GPW "sudo -S -p '' -l 2>&1" | tr -d '\r')
  fi
  printf '%s\n' "$l"
  nopw=$(printf '%s\n' "$l" | grep -c 'NOPASSWD' || true)
  if [ "$nopw" != 1 ] || ! printf '%s\n' "$l" | grep -q 'NOPASSWD: */opt/tbound/operator/tbound-op *$'; then echo "NOT NARROW: expected exactly one NOPASSWD entry, for /opt/tbound/operator/tbound-op (found $nopw)"; rc=1; fi
  if printf '%s\n' "$l" | grep -qE 'NOPASSWD: *ALL'; then echo "NOT NARROW: NOPASSWD: ALL listed"; rc=1; fi
  if ! printf '%s\n' "$l" | grep -qE '\(ALL : ALL\) ALL'; then echo "NOT NARROW: the password rule (ALL : ALL) ALL is missing from the listing"; rc=1; fi
  if [ -n "${TBA_PW:-}" ] || G 'sudo -n true' >/dev/null 2>&1; then echo "sudoers.d: $(sudo_any 'ls /etc/sudoers.d' | tr -d '\r' | tr '\n' ' ')"; else echo "sudoers.d: (not listed: needs root; set TBA_PW to list)"; fi
  return "$rc"
}

eq "ssh identity" "$(G 'id -un' | tr -d '\r')" tboundadmin
echo "guest: $(G 'hostname; uname -r; date -u +%FT%TZ' | tr -d '\r' | tr '\n' ' ')"
echo "operator: ${SUDO_USER:-$(id -un)} mode=$MODE"

case "$MODE" in
status)
  narrow_report && ok "narrowed" || note "not in the narrowed state (session drop-in open?)"
  ;;
open)
  need_pw
  say "password sudo works (one throwaway command; the password is not printed)"
  [ "$(GPW "sudo -S -p '' id -u" | tr -d '\r')" = 0 ] || die "password sudo did not give uid 0"
  ok "password sudo works"
  if G 'sudo -n true' >/dev/null 2>&1; then
    ok "sudo -n true already works: a NOPASSWD ALL rule is already active (the session drop-in is open); re-verifying only"
  else
    # No single quote inside INNER (it is wrapped in single quotes). The temp name starts with a dot: sudo ignores it until the mv.
    INNER="set -e; umask 077; D=/etc/sudoers.d; T=\$D/.91-tbound-$FREEZE_LC-session.new; printf \"%s\\n\" \"# TBound $FREEZE session drop-in (removed by 00-session-sudo.sh close)\" \"$LINE\" > \$T; chmod 0440 \$T; chown root:root \$T; visudo -cf \$T; mv -f \$T \$D/91-tbound-$FREEZE_LC-session"
    GPW "sudo -S -p '' sh -c '$INNER'"
  fi
  say "verify"
  G 'sudo -n true' || die "sudo -n true still fails after the install"
  ok "sudo -n true works (session open)"
  G "sudo -n stat -c '%U:%G %a %n' $DROPIN; sudo -n visudo -c" | tr -d '\r'
  [ "$(G "sudo -n cat $DROPIN" | grep -vc '^#' || true)" = 1 ] || die "unexpected drop-in content"
  note "SESSION OPEN: close it when the privileged steps are done: bash 00-session-sudo.sh close"
  ;;
close)
  say "remove the drop-in"
  sudo_any "rm -f $DROPIN"
  G 'sudo -n -k' >/dev/null 2>&1 || true            # forget any cached sudo credential (so the checks below are real)
  sudo_any "test ! -e $DROPIN" || { RC=$(sudo_any "ls -l $DROPIN 2>&1" || true); die "drop-in still present: $RC"; }
  ok "$DROPIN removed"
  if G 'sudo -n true' >/dev/null 2>&1; then die "sudo -n true STILL works after the removal: another NOPASSWD ALL rule is active (see sudo -l below)"; fi
  ok "sudo -n true fails again"
  narrow_report || die "the sudo configuration is not the narrowed one"
  ok "narrowed: only the tbound-op NOPASSWD rule plus password (ALL : ALL) ALL"
  ;;
esac
done_step
