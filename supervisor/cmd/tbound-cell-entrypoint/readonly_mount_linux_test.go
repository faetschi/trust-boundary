//go:build linux

package main

import "testing"

// Lines taken from a real rootless command cell on the guest (podman --read-only, crun).
const cellMountinfo = `709 655 0:54 / / ro,relatime - overlay overlay rw,lowerdir=/a:/b
711 709 0:58 / /run rw,nosuid,nodev,relatime - tmpfs tmpfs rw,mode=755
714 709 0:61 / /tmp rw,nosuid,nodev,noexec,relatime - tmpfs tmpfs rw,size=8192k
719 713 0:7 /null /dev/null rw,nosuid,noexec,relatime - devtmpfs udev rw
`

func TestReadOnlyMountCoversPathsOnTheRootMount(t *testing.T) {
	for _, path := range []string{"/bin/bash", "/usr/bin/bash", "/"} {
		if !mountinfoReadOnly(cellMountinfo, path) {
			t.Fatalf("%s lies on the read-only root mount but was not accepted", path)
		}
	}
}

func TestReadOnlyMountRefusesWritableMounts(t *testing.T) {
	for _, path := range []string{"/tmp/bash", "/run/x", "/dev/null"} {
		if mountinfoReadOnly(cellMountinfo, path) {
			t.Fatalf("%s lies on a writable mount but was accepted", path)
		}
	}
	rw := "1 0 0:1 / / rw,relatime - overlay overlay rw\n"
	if mountinfoReadOnly(rw, "/bin/bash") {
		t.Fatal("a writable root mount was accepted")
	}
	if mountinfoReadOnly("", "/bin/bash") {
		t.Fatal("an empty mount table was accepted")
	}
}

func TestReadOnlyMountDoesNotMatchSiblingPrefixes(t *testing.T) {
	m := "1 0 0:1 / / rw,relatime - x x rw\n2 1 0:2 / /usr ro,relatime - x x ro\n"
	if mountinfoReadOnly(m, "/usrx/bash") {
		t.Fatal("/usrx matched the /usr mount")
	}
	if !mountinfoReadOnly(m, "/usr/bin/bash") {
		t.Fatal("/usr/bin/bash on the read-only /usr mount was refused")
	}
}
