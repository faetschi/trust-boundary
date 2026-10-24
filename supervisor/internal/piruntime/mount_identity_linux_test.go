//go:build linux

package piruntime

import (
	"os"
	"strings"
	"testing"
)

const (
	guestHostLiveRoot    = "161 34 7:0 / /var/lib/tbound/live rw,nosuid,nodev,relatime shared:294 - ext4 /dev/loop0 rw"
	guestServiceLiveRoot = "627 604 7:0 / /var/lib/tbound/live rw,nosuid,nodev,relatime shared:454 master:294 - ext4 /dev/loop0 rw"
	guestHostParent      = "30 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw"
	guestServiceParent   = "604 580 8:2 / / ro,nosuid,relatime master:1 - ext4 /dev/sda2 rw"
)

func mustMountRecord(t *testing.T, line string, id uint64) mountRecord {
	t.Helper()
	record, err := parseMountRecord(line, id)
	if err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	return record
}

func TestRegisteredMountMatchesAcrossMountNamespacesWithDifferentMountIDs(t *testing.T) {
	host := mustMountRecord(t, guestHostLiveRoot, 161)
	service := mustMountRecord(t, guestServiceLiveRoot, 627)
	digest := DigestBytes([]byte(host.options()))
	if service.id == host.id || !service.matchesRegisteredOptions(digest, false) {
		t.Fatal("same device/root/mountpoint/options with another namespace-local mount ID was refused")
	}
	if !service.onDevice(7<<8 | 0) {
		t.Fatal("mountinfo device 7:0 did not match st_dev")
	}
	for name, line := range map[string]string{
		"different root within the filesystem": "627 604 7:0 /sub /var/lib/tbound/live rw,nosuid,nodev,relatime master:294 - ext4 /dev/loop0 rw",
		"different mount point":                "627 604 7:0 / /var/lib/tbound/other rw,nosuid,nodev,relatime master:294 - ext4 /dev/loop0 rw",
		"different filesystem type":            "627 604 7:0 / /var/lib/tbound/live rw,nosuid,nodev,relatime master:294 - xfs /dev/loop0 rw",
		"different source":                     "627 604 7:0 / /var/lib/tbound/live rw,nosuid,nodev,relatime master:294 - ext4 /dev/loop1 rw",
		"read-only":                            "627 604 7:0 / /var/lib/tbound/live ro,nosuid,nodev,relatime master:294 - ext4 /dev/loop0 rw",
		"suid allowed":                         "627 604 7:0 / /var/lib/tbound/live rw,nodev,relatime master:294 - ext4 /dev/loop0 rw",
		"device files allowed":                 "627 604 7:0 / /var/lib/tbound/live rw,nosuid,relatime master:294 - ext4 /dev/loop0 rw",
		"extra flag":                           "627 604 7:0 / /var/lib/tbound/live rw,nosuid,nodev,noatime,relatime master:294 - ext4 /dev/loop0 rw",
	} {
		// The live root is a ReadWritePaths mount and is always matched exactly.
		if mustMountRecord(t, line, 627).matchesRegisteredOptions(digest, false) {
			t.Fatalf("%s: live root mount was accepted", name)
		}
	}
	if service.onDevice(7<<8 | 1) {
		t.Fatal("a different device was accepted")
	}
}

func TestParentMountOptionsAcceptOnlyNarrowing(t *testing.T) {
	host := mustMountRecord(t, guestHostParent, 30)
	registered := DigestBytes([]byte(host.options()))
	if want := DigestBytes([]byte("/\x00/\x00rw,relatime\x00ext4\x00/dev/sda2\x00rw")); registered != want {
		t.Fatalf("host parent options string drifted: %q", host.options())
	}
	service := mustMountRecord(t, guestServiceParent, 604)
	if service.options() != "/\x00/\x00ro,nosuid,relatime\x00ext4\x00/dev/sda2\x00rw" {
		t.Fatalf("service parent options string = %q", service.options())
	}
	if !service.matchesRegisteredOptions(registered, true) {
		t.Fatal("ProtectSystem=strict narrowing (rw to ro, nosuid added) was refused")
	}
	if !host.matchesRegisteredOptions(registered, true) {
		t.Fatal("the unchanged host parent options were refused")
	}
	if service.matchesRegisteredOptions(registered, false) {
		t.Fatal("narrowing was accepted where an exact match is required")
	}
	for name, test := range map[string]struct{ registered, observed string }{
		"ro to rw":              {"30 1 8:2 / / ro,relatime - ext4 /dev/sda2 rw", guestHostParent},
		"nosuid removed":        {"30 1 8:2 / / rw,nosuid,relatime - ext4 /dev/sda2 rw", guestHostParent},
		"nodev removed":         {"30 1 8:2 / / rw,nodev,relatime - ext4 /dev/sda2 rw", guestHostParent},
		"noexec removed":        {"30 1 8:2 / / rw,noexec,relatime - ext4 /dev/sda2 rw", guestHostParent},
		"extra flag added":      {guestHostParent, "604 580 8:2 / / ro,nosuid,noatime,relatime - ext4 /dev/sda2 rw"},
		"flag removed":          {guestHostParent, "604 580 8:2 / / ro,nosuid - ext4 /dev/sda2 rw"},
		"flag swapped":          {guestHostParent, "604 580 8:2 / / ro,nosuid,strictatime - ext4 /dev/sda2 rw"},
		"different source":      {guestHostParent, "604 580 8:2 / / ro,nosuid,relatime - ext4 /dev/sda3 rw"},
		"different root":        {guestHostParent, "604 580 8:2 /sub / ro,nosuid,relatime - ext4 /dev/sda2 rw"},
		"different super flags": {guestHostParent, "604 580 8:2 / / ro,nosuid,relatime - ext4 /dev/sda2 rw,errors=remount-ro"},
		"different mount point": {guestHostParent, "604 580 8:2 / /other ro,nosuid,relatime - ext4 /dev/sda2 rw"},
	} {
		reg := DigestBytes([]byte(mustMountRecord(t, test.registered, 30).options()))
		if mustMountRecord(t, test.observed, firstFieldID(test.observed)).matchesRegisteredOptions(reg, true) {
			t.Fatalf("%s: was accepted", name)
		}
	}
}

func firstFieldID(line string) uint64 {
	if strings.HasPrefix(line, "604 ") {
		return 604
	}
	return 30
}

func TestMountRecordForDescriptorMatchesTheDescriptorDevice(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	record, err := mountRecordForDescriptor(dir)
	if err != nil {
		t.Fatalf("real directory descriptor has no matching mountinfo record: %v", err)
	}
	if record.mountpoint == "" || record.fstype == "" {
		t.Fatalf("incomplete mount record: %+v", record)
	}
}
