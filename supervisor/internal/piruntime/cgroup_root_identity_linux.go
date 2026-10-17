//go:build linux

package piruntime

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// D09CgroupRootIdentityLabel versions the signed HostProfile.CgroupRootDigest.
// v1 (unlabelled) hashed the cells cgroup's device and inode, which systemd and
// the runner recreate at every service start, so a signed value could never
// match a live scope. v2 binds only facts that survive a restart.
const D09CgroupRootIdentityLabel = "tbound-d09-cgroup-root/v2"

// d09RequiredCgroupControllers is the exact controller set a delegated cell
// root must expose and delegate; it is part of the signed identity.
const d09RequiredCgroupControllers = "cpu,memory,pids"

// DigestD09CgroupRoot derives the signed CgroupRootDigest from stable facts
// only: the canonical cgroup-v2 hierarchy path of the delegated cell root (for
// example /system.slice/tbound-pi.service/cells), the cgroup2 filesystem type,
// the delegated owner uid, and the required controller set. It deliberately
// binds no inode, device, mount id or cgroup id: those are runtime
// measurements that change on every service start. The live identity is still
// verified fd-relatively at launch (see cgroupRootFacts.stableDigest) and must
// derive to this exact value.
func DigestD09CgroupRoot(cgroupsPath string, ownerUID int) (string, error) {
	if !canonicalAbsolutePath(cgroupsPath) || cgroupsPath == "/" || ownerUID <= 0 {
		return "", errors.New("D09 cgroup root identity needs a canonical absolute hierarchy path and a non-root owner uid")
	}
	identity := fmt.Sprintf("%s\x00%s\x00cgroup2\x00%d\x00%s", D09CgroupRootIdentityLabel, cgroupsPath, ownerUID,
		d09RequiredCgroupControllers)
	return DigestBytes([]byte(identity)), nil
}

// D09ManagedCellsCgroupRootDigest is the value the offline tooling prints for
// the managed service: the closed <service cgroup>/cells root delegated to the
// registered service uid. It is derived, not measured.
func D09ManagedCellsCgroupRootDigest(serviceUID int) (string, error) {
	return DigestD09CgroupRoot(D09ManagedPiServiceCgroupPath+"/"+D09ManagedPiCellCgroupLeaf, serviceUID)
}

// cgroupRootFacts is what the launcher observes through the held delegated-root
// descriptor. device, inode and mountID are recorded for diagnostics only and
// are never part of the identity.
type cgroupRootFacts struct {
	hierarchyPath string
	cgroup2       bool
	ownerUID      uint32
	controllers   []byte
	subtree       []byte
	device        uint64
	inode         uint64
	mountID       uint64
}

// stableDigest refuses a root that is not cgroup2, lacks any required
// controller (available or delegated to children), or has no canonical path,
// and otherwise returns the same value DigestD09CgroupRoot derives offline.
func (f cgroupRootFacts) stableDigest() (string, error) {
	if !f.cgroup2 {
		return "", errors.New("delegated cgroup root is not on a cgroup2 filesystem")
	}
	for _, name := range strings.Split(d09RequiredCgroupControllers, ",") {
		if !hasCgroupController(f.controllers, name) {
			return "", fmt.Errorf("delegated cgroup root lacks the %s controller", name)
		}
		if !hasCgroupController(f.subtree, name) {
			return "", fmt.Errorf("delegated cgroup root has not enabled the %s controller for children", name)
		}
	}
	return DigestD09CgroupRoot(f.hierarchyPath, int(f.ownerUID))
}

// cgroup2HierarchyPathFromMountinfo maps a host path below a cgroup2 mount to
// its path inside the cgroup hierarchy (the form used by cgroups_path and by
// /proc/<pid>/cgroup). It ignores non-cgroup2 mounts and prefers the longest
// matching mountpoint. The mountpoint itself (the hierarchy root) never
// qualifies as a delegated cell root.
func cgroup2HierarchyPathFromMountinfo(mountinfo, hostPath string) (string, bool) {
	best, bestLen := "", -1
	for _, line := range strings.Split(mountinfo, "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "cgroup2 ") {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 5 {
			continue
		}
		root := filepath.Clean(unescapeMountField(fields[3]))
		mountpoint := filepath.Clean(unescapeMountField(fields[4]))
		if hostPath == mountpoint || !strings.HasPrefix(hostPath, mountpoint+"/") || len(mountpoint) <= bestLen {
			continue
		}
		remainder := strings.TrimPrefix(hostPath, mountpoint+"/")
		best, bestLen = filepath.Join(root, remainder), len(mountpoint)
		if !strings.HasPrefix(best, "/") {
			best = "/" + best
		}
	}
	return best, bestLen >= 0
}
