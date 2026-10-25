#!/bin/bash
# Regenerates the netsink-nft-<version> listings: loads the pinned rule with `nft -f` into a private
# network and mount namespace and reads the table back. No root: the throwaway cgroup2 subtree lives
# in the user's systemd delegation, and a tmpfs over /sys/fs/cgroup in the private mount namespace
# makes it appear as system.slice, so the rule's path resolves as it does on the guest.
# usage: capture-netsink-listing.sh <nft-binary> <rule-file> <out-prefix>   (writes <out-prefix>.json and .txt)
set -eu
nft=$1 rule=$2 out=$3
uid=$(id -u)
cg=/sys/fs/cgroup/unified/user.slice/user-$uid.slice/user@$uid.service/tb-nft-shape
stage=$(mktemp -d)
trap 'rmdir "$cg/tbound-pi.service/cells/net-sink" "$cg/tbound-pi.service/cells" "$cg/tbound-pi.service" "$cg" "$stage" 2>/dev/null || true' EXIT
mkdir -p "$cg/tbound-pi.service/cells/net-sink"
unshare -rmn bash -c '
	set -eu
	mount --bind "$1" "$5"
	mount -t tmpfs tmpfs /sys/fs/cgroup
	mkdir /sys/fs/cgroup/system.slice
	mount --bind "$5" /sys/fs/cgroup/system.slice
	"$2" -f "$3"
	"$2" -j list table inet tbound_net_sink >"$4.json"
	"$2" list table inet tbound_net_sink >"$4.txt"
' _ "$cg" "$nft" "$rule" "$out" "$stage"
sync
