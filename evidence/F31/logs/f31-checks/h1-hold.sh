#!/bin/sh
# H1 guest hold (F31, 2026-11-20), run as root right after the pre-F30 restore: keep kernel 7.0.0-38, no unattended upgrades, no needrestart auto-restart.
set -eu
echo "== before $(date -u +%FT%TZ)"
uname -r
[ "$(uname -r)" = 7.0.0-38-generic ] || { echo "FATAL: running kernel is not 7.0.0-38-generic"; exit 3; }
if pgrep -a 'apt|dpkg|unattended-upgr' | grep -v 'unattended-upgrade-shutdown --wait-for-signal'; then echo "FATAL: apt, dpkg or unattended-upgrade is running; nothing changed"; exit 4; fi
[ -z "$(dpkg --audit)" ] || { echo "FATAL: dpkg --audit reports a problem; nothing changed"; dpkg --audit; exit 4; }
apt-mark showhold | tr '\n' ' '; echo
systemctl is-enabled apt-daily.timer apt-daily-upgrade.timer unattended-upgrades.service || true
grep -n '^ *set default=' /boot/grub/grub.cfg || true
SUB=$(grep -o 'gnulinux-advanced-[0-9a-f-]*' /boot/grub/grub.cfg | head -1)
ENT=$(grep -o 'gnulinux-7.0.0-38-generic-advanced-[0-9a-f-]*' /boot/grub/grub.cfg | head -1)
[ -n "$SUB" ] && [ -n "$ENT" ] || { echo "FATAL: no grub entry for 7.0.0-38-generic; nothing changed"; exit 5; }
echo "== hold"
apt-mark hold linux-generic-hwe-24.04 linux-headers-generic-hwe-24.04 linux-image-generic-hwe-24.04 linux-image-7.0.0-38-generic
printf 'GRUB_DEFAULT="%s>%s"\n' "$SUB" "$ENT" > /etc/default/grub.d/99-tbound-h1-kernel.cfg
update-grub 2>&1 | tail -3
grep -n '^ *set default=' /boot/grub/grub.cfg
grep -q "^ *set default=\"$SUB>$ENT\"" /boot/grub/grub.cfg || { echo "FATAL: grub default is not the 7.0.0-38 entry"; exit 6; }
systemctl disable --now apt-daily.timer apt-daily-upgrade.timer
systemctl disable unattended-upgrades.service
printf 'APT::Periodic::Update-Package-Lists "0";\nAPT::Periodic::Unattended-Upgrade "0";\n' > /etc/apt/apt.conf.d/99-tbound-h1-hold
printf '$nrconf{restart} = '"'"'l'"'"';\n' > /etc/needrestart/conf.d/99-tbound-h1-hold.conf
echo "== after $(date -u +%FT%TZ)"
apt-mark showhold | tr '\n' ' '; echo
systemctl is-enabled apt-daily.timer apt-daily-upgrade.timer unattended-upgrades.service || true
systemctl is-active apt-daily.timer apt-daily-upgrade.timer || true
apt-config dump | grep -E 'APT::Periodic::(Update-Package-Lists|Unattended-Upgrade) '
cat /etc/default/grub.d/99-tbound-h1-kernel.cfg /etc/needrestart/conf.d/99-tbound-h1-hold.conf
echo H1_HOLD_OK
