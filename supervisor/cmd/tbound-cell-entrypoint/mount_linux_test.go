//go:build linux

package main

import (
	"os"
	"strings"
	"testing"
)

func writeExecutable(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

const testMountInfo = `36 35 98:0 / / rw,noatime master:1 - ext3 /dev/root rw,errors=continue
42 35 0:41 / /usr ro,relatime - ext4 /dev/vda1 ro
43 35 0:42 / /work rw,nosuid,nodev - ext4 /dev/vdb1 rw
`

func testFdinfo(mntID string) string {
	return "pos:\t0\nflags:\t0100000\nmnt_id:\t" + mntID + "\nino:\t1234\n"
}

// The opened descriptor's mount decides, not the path's lexical spelling: the mount ID of the
// fdinfo entry must be a mount that mountinfo reports read-only.
func TestRequireReadOnlyMountID(t *testing.T) {
	for name, test := range map[string]struct {
		fdinfo, mountinfo string
		ok                bool
	}{
		"the descriptor is on the read-only mount":    {fdinfo: testFdinfo("42"), mountinfo: testMountInfo, ok: true},
		"the descriptor is on a writable mount":       {fdinfo: testFdinfo("36"), mountinfo: testMountInfo},
		"the descriptor is on another writable mount": {fdinfo: testFdinfo("43"), mountinfo: testMountInfo},
		"the mount is not in mountinfo":               {fdinfo: testFdinfo("99"), mountinfo: testMountInfo},
		"fdinfo has no mnt_id":                        {fdinfo: "pos:\t0\nflags:\t0100000\n", mountinfo: testMountInfo},
		"fdinfo has two mnt_ids":                      {fdinfo: "mnt_id:\t42\nmnt_id:\t36\n", mountinfo: testMountInfo},
		"fdinfo mnt_id is zero":                       {fdinfo: testFdinfo("0"), mountinfo: testMountInfo},
		"fdinfo mnt_id is not decimal":                {fdinfo: testFdinfo("x"), mountinfo: testMountInfo},
		"mountinfo is empty":                          {fdinfo: testFdinfo("42")},
		"mountinfo line is malformed":                 {fdinfo: testFdinfo("42"), mountinfo: "42 35 0:41 / /usr ro,relatime\n"},
		"mountinfo id is not decimal":                 {fdinfo: testFdinfo("42"), mountinfo: "x 35 0:41 / /usr ro - ext4 /dev/vda1 ro\n"},
	} {
		err := requireReadOnlyMountID([]byte(test.fdinfo), []byte(test.mountinfo))
		if test.ok && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !test.ok && err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestDigestCommandExecutableRequiresTheMeasuredUIDMap: the owner decision uses the map the cell
// measured and registered, passed in, never a second read of /proc/self/uid_map.
func TestDigestCommandExecutableRequiresTheMeasuredUIDMap(t *testing.T) {
	path := t.TempDir() + "/cmd"
	if err := writeExecutable(path, "#!/bin/sh\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := digestCommandExecutable(path, ""); err == nil || !strings.Contains(err.Error(), "uid_map") {
		t.Fatalf("digestCommandExecutable without a measured uid_map = %v", err)
	}
}
