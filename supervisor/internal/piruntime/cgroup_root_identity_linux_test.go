//go:build linux

package piruntime

import (
	"strings"
	"testing"
)

const testCellsHierarchyPath = "/system.slice/tbound-pi.service/cells"

func testCgroupRootFacts(inode, mountID uint64) cgroupRootFacts {
	return cgroupRootFacts{
		hierarchyPath: testCellsHierarchyPath, cgroup2: true, ownerUID: 995,
		controllers: []byte("cpu memory pids\n"), subtree: []byte("cpu memory pids\n"),
		device: 41, inode: inode, mountID: mountID,
	}
}

// The signed CgroupRootDigest must survive a service restart, which recreates
// the cells cgroup with a new inode (and possibly a new mount id).
func TestD09CgroupRootDigestIsStableAcrossCellsInodes(t *testing.T) {
	signed, err := D09ManagedCellsCgroupRootDigest(995)
	if err != nil || !validDigest(signed) {
		t.Fatalf("derived digest = %q, %v", signed, err)
	}
	for _, facts := range []cgroupRootFacts{testCgroupRootFacts(1001, 17), testCgroupRootFacts(987654321, 99)} {
		live, err := facts.stableDigest()
		if err != nil {
			t.Fatalf("live cells facts refused: %v", err)
		}
		if live != signed {
			t.Fatalf("live digest for inode %d differs from the signed value: %s != %s", facts.inode, live, signed)
		}
	}
	other := testCgroupRootFacts(5, 5)
	other.device = 7
	if live, err := other.stableDigest(); err != nil || live != signed {
		t.Fatalf("device id leaked into the digest: %s %v", live, err)
	}
}

func TestD09CgroupRootDigestRefusesWrongPathOwnerOrControllers(t *testing.T) {
	signed, err := D09ManagedCellsCgroupRootDigest(995)
	if err != nil {
		t.Fatal(err)
	}
	wrongPath := testCgroupRootFacts(1, 1)
	wrongPath.hierarchyPath = "/system.slice/tbound-pi.service/other"
	wrongOwner := testCgroupRootFacts(1, 1)
	wrongOwner.ownerUID = 996
	for name, facts := range map[string]cgroupRootFacts{"wrong path": wrongPath, "wrong owner": wrongOwner} {
		live, err := facts.stableDigest()
		if err != nil {
			continue // refusing outright is also acceptable
		}
		if live == signed {
			t.Fatalf("%s matched the signed cgroup root digest", name)
		}
	}
	missingController := testCgroupRootFacts(1, 1)
	missingController.controllers = []byte("cpu memory\n")
	notDelegated := testCgroupRootFacts(1, 1)
	notDelegated.subtree = []byte("cpu pids\n")
	notCgroup2 := testCgroupRootFacts(1, 1)
	notCgroup2.cgroup2 = false
	emptyPath := testCgroupRootFacts(1, 1)
	emptyPath.hierarchyPath = ""
	for name, facts := range map[string]cgroupRootFacts{
		"missing controller": missingController, "controller not delegated": notDelegated,
		"not cgroup2": notCgroup2, "empty path": emptyPath,
	} {
		if live, err := facts.stableDigest(); err == nil {
			t.Fatalf("%s accepted with digest %s", name, live)
		}
	}
}

func TestD09CgroupRootDigestDerivationIsCanonicalAndLabelled(t *testing.T) {
	derived, err := DigestD09CgroupRoot(testCellsHierarchyPath, 995)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := D09ManagedCellsCgroupRootDigest(995)
	if err != nil || managed != derived {
		t.Fatalf("managed digest %s differs from explicit derivation %s (%v)", managed, derived, err)
	}
	legacy := DigestBytes([]byte(testCellsHierarchyPath + "\x00cgroup2\x00995\x00cpu,memory,pids"))
	if derived == legacy {
		t.Fatal("derived digest is not domain separated by its versioned label")
	}
	if !strings.HasSuffix(D09CgroupRootIdentityLabel, "/v2") {
		t.Fatalf("label %q must be bumped from the unlabelled inode-bound shape", D09CgroupRootIdentityLabel)
	}
	if other, _ := DigestD09CgroupRoot(testCellsHierarchyPath, 996); other == derived {
		t.Fatal("owner uid is not bound")
	}
	for _, bad := range []string{"", "relative/cells", "/system.slice/../cells", "/system.slice//cells", "/system.slice/cells/"} {
		if digest, err := DigestD09CgroupRoot(bad, 995); err == nil {
			t.Fatalf("non-canonical path %q derived %s", bad, digest)
		}
	}
	if digest, err := DigestD09CgroupRoot(testCellsHierarchyPath, 0); err == nil {
		t.Fatalf("uid 0 derived %s", digest)
	}
}

func TestCgroup2HierarchyPathStripsMountpointAndIgnoresOtherFilesystems(t *testing.T) {
	mountinfo := strings.Join([]string{
		"22 1 0:21 / /sys rw,nosuid - sysfs sysfs rw",
		"25 22 0:22 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:9 - cgroup2 cgroup2 rw",
		"30 25 0:30 / /sys/fs/cgroup/legacy rw - cgroup cgroup rw,cpu",
	}, "\n")
	got, ok := cgroup2HierarchyPathFromMountinfo(mountinfo, "/sys/fs/cgroup"+testCellsHierarchyPath)
	if !ok || got != testCellsHierarchyPath {
		t.Fatalf("hierarchy path = %q ok=%v", got, ok)
	}
	if _, ok := cgroup2HierarchyPathFromMountinfo(mountinfo, "/tmp/fake/cells"); ok {
		t.Fatal("path outside any cgroup2 mount resolved")
	}
	if _, ok := cgroup2HierarchyPathFromMountinfo(mountinfo, "/sys/fs/cgroup"); ok {
		t.Fatal("the cgroup2 mountpoint itself is not a delegated cell root")
	}
	namespaced := "40 1 0:40 /system.slice/tbound-pi.service /sys/fs/cgroup rw - cgroup2 cgroup2 rw"
	got, ok = cgroup2HierarchyPathFromMountinfo(namespaced, "/sys/fs/cgroup/cells")
	if !ok || got != testCellsHierarchyPath {
		t.Fatalf("mount root was not restored into the hierarchy path: %q ok=%v", got, ok)
	}
}
