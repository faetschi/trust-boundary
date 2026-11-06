//go:build linux

package piruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testCgroupMountpoint = "/sys/fs/cgroup"
	testCellsHostPath    = testCgroupMountpoint + testCellsHierarchyPath
)

func testCoveredCellsMountinfo(order ...string) string {
	lines := map[string]string{
		"sys":    "22 1 0:21 / /sys rw,nosuid - sysfs sysfs rw",
		"cgroup": "25 22 0:22 / " + testCgroupMountpoint + " rw,nosuid,nodev,noexec - cgroup2 cgroup2 rw",
		"tmpfs":  "41 25 0:41 / " + testCellsHostPath + " rw,nosuid,nodev - tmpfs tmpfs rw,size=1m",
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		out = append(out, lines[name])
	}
	return strings.Join(out, "\n")
}

// Finding: a filesystem mounted over the cells path was ignored because only
// cgroup2 mounts were considered before choosing the longest match. The
// covering (longest) mount among ALL mounts decides, and it must be cgroup2.
func TestCgroup2PathObservationRefusesAMountCoveringTheCellsRoot(t *testing.T) {
	covered := testCoveredCellsMountinfo("sys", "cgroup", "tmpfs")
	for _, path := range []string{testCellsHostPath, testCellsHostPath + "/pi-0123456789ab"} {
		if got, ok := cgroup2HierarchyPathFromMountinfo(covered, path); ok {
			t.Fatalf("hierarchy path %q resolved through a covering tmpfs for %q", got, path)
		}
		if err := requireCgroup2PathIn(covered, path); err == nil {
			t.Fatalf("requireCgroup2Path accepted %q below a covering tmpfs", path)
		}
	}
	// Control: the same path without the covering mount is the real cgroup2.
	clean := testCoveredCellsMountinfo("sys", "cgroup")
	if got, ok := cgroup2HierarchyPathFromMountinfo(clean, testCellsHostPath); !ok || got != testCellsHierarchyPath {
		t.Fatalf("control hierarchy path = %q ok=%v", got, ok)
	}
	if err := requireCgroup2PathIn(clean, testCellsHostPath+"/pi-0123456789ab"); err != nil {
		t.Fatalf("control path refused: %v", err)
	}
	// A longer cgroup2 mount is the covering one even when listed before a
	// shorter non-cgroup mount.
	reordered := testCoveredCellsMountinfo("sys", "tmpfs", "cgroup")
	if err := requireCgroup2PathIn(reordered, testCellsHostPath+"/pi-0123456789ab"); err == nil {
		t.Fatal("tmpfs on the cells path was ignored because it was listed first")
	}
	// Stacked mounts on one mountpoint: the later one covers the earlier one.
	stackedTmpfs := strings.Join([]string{
		"25 22 0:22 / " + testCgroupMountpoint + " rw - cgroup2 cgroup2 rw",
		"26 22 0:50 / " + testCgroupMountpoint + " rw - tmpfs tmpfs rw",
	}, "\n")
	if err := requireCgroup2PathIn(stackedTmpfs, testCellsHostPath); err == nil {
		t.Fatal("a tmpfs stacked over the cgroup2 mountpoint was accepted")
	}
	// The mountpoint itself is never a delegated root.
	if err := requireCgroup2PathIn(clean, testCgroupMountpoint); err == nil {
		t.Fatal("the cgroup2 mountpoint itself was accepted")
	}
	// A non-cgroup mount covering an ancestor of the cells root decides too.
	nested := strings.Join([]string{
		"25 22 0:22 / " + testCgroupMountpoint + " rw - cgroup2 cgroup2 rw",
		"30 25 0:30 / " + testCgroupMountpoint + "/system.slice rw - overlay overlay rw",
	}, "\n")
	if err := requireCgroup2PathIn(nested, testCellsHostPath); err == nil {
		t.Fatal("an overlay covering an ancestor of the cells root was accepted")
	}
}

// The held descriptors themselves must be cgroup2: fstatfs on a descriptor that
// is not on a cgroup2 filesystem is refused regardless of what mountinfo says.
func TestCgroup2DescriptorObservationRefusesANonCgroupDescriptor(t *testing.T) {
	directory := t.TempDir()
	file, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := requireCgroup2Descriptor(file); err == nil {
		t.Fatal("a temp directory descriptor was accepted as a cgroup2 root")
	}
	// Even a forged mountinfo claiming the temp directory's parent is a cgroup2
	// mount cannot override the descriptor's own filesystem type.
	forged := "25 22 0:22 / " + filepath.Dir(directory) + " rw - cgroup2 cgroup2 rw"
	if _, err := observeCgroup2Descriptor(file, forged); err == nil {
		t.Fatal("forged mountinfo made a non-cgroup2 descriptor acceptable")
	}
	if _, err := observeCgroup2Descriptor(nil, forged); err == nil {
		t.Fatal("nil descriptor accepted")
	}
}

func TestCgroup2DescriptorMountIDIsCorrelatedWithMountinfo(t *testing.T) {
	entry, ok := coveringMount(testCoveredCellsMountinfo("sys", "cgroup", "tmpfs"), testCellsHostPath+"/pi-x")
	if !ok || entry.id != 41 || entry.fstype != "tmpfs" {
		t.Fatalf("covering mount = %+v ok=%v", entry, ok)
	}
	entry, ok = coveringMount(testCoveredCellsMountinfo("sys", "cgroup"), testCellsHostPath+"/pi-x")
	if !ok || entry.id != 25 || entry.fstype != "cgroup2" {
		t.Fatalf("covering mount = %+v ok=%v", entry, ok)
	}
	// A descriptor on a different mount than the covering cgroup2 mount (a mount
	// created over the path after the descriptor was opened) is refused.
	if err := requireDescriptorOnMount(41, entry); err == nil {
		t.Fatal("descriptor on a different mount than the covering cgroup2 mount was accepted")
	}
	if err := requireDescriptorOnMount(25, entry); err != nil {
		t.Fatalf("matching mount id refused: %v", err)
	}
	// Live sanity check: for a real directory the descriptor's mount id from
	// /proc matches the longest-prefix mount in /proc/self/mountinfo.
	directory := t.TempDir()
	file, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	live, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Skipf("mountinfo unavailable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	id, err := descriptorMountID(file.Fd())
	if err != nil || id == 0 {
		t.Fatalf("descriptor mount id = %d, %v", id, err)
	}
	if mount, ok := coveringMount(string(live), resolved); !ok || mount.id != id {
		t.Fatalf("live covering mount %+v ok=%v does not match descriptor mount id %d", mount, ok, id)
	}
}
