//go:build linux

package piruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// cgroup2SuperMagic is CGROUP2_SUPER_MAGIC (linux/magic.h).
const cgroup2SuperMagic = 0x63677270

// mountinfoEntry is the part of one /proc/self/mountinfo line the cgroup
// checks use: the mount id, the root inside the filesystem, the mountpoint
// and the filesystem type.
type mountinfoEntry struct {
	id                       uint64
	root, mountpoint, fstype string
}

// coveringMount returns the mount that covers hostPath: the one with the
// longest mountpoint that equals hostPath or is a proper ancestor of it,
// chosen among ALL mounts of every filesystem type. Mounts on the same
// mountpoint are stacked, so the later mountinfo line is the visible one. A
// non-cgroup2 mount over (part of) the cells path therefore wins and is then
// refused by the callers; it is never skipped.
func coveringMount(mountinfo, hostPath string) (mountinfoEntry, bool) {
	var best mountinfoEntry
	found := false
	for _, line := range strings.Split(mountinfo, "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		tail := strings.Fields(parts[1])
		if len(fields) < 5 || len(tail) < 1 {
			continue
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		mountpoint := filepath.Clean(unescapeMountField(fields[4]))
		prefix := mountpoint
		if prefix != "/" {
			prefix += "/"
		}
		if hostPath != mountpoint && !strings.HasPrefix(hostPath, prefix) {
			continue
		}
		if found && len(mountpoint) < len(best.mountpoint) {
			continue
		}
		best = mountinfoEntry{id: id, root: filepath.Clean(unescapeMountField(fields[3])), mountpoint: mountpoint, fstype: tail[0]}
		found = true
	}
	return best, found
}

// cgroup2HierarchyPathFromMountinfo maps a host path below a cgroup2 mount to
// its path inside the cgroup hierarchy (the form used by cgroups_path and by
// /proc/<pid>/cgroup). The covering mount is selected among all mounts and must
// itself be cgroup2; a different filesystem mounted over the path is refused,
// not skipped. The mountpoint itself (the hierarchy root) never qualifies as a
// delegated cell root.
func cgroup2HierarchyPathFromMountinfo(mountinfo, hostPath string) (string, bool) {
	mount, ok := coveringMount(mountinfo, hostPath)
	if !ok || mount.fstype != "cgroup2" || hostPath == mount.mountpoint {
		return "", false
	}
	prefix := mount.mountpoint
	if prefix != "/" {
		prefix += "/"
	}
	best := filepath.Join(mount.root, strings.TrimPrefix(hostPath, prefix))
	if !strings.HasPrefix(best, "/") {
		best = "/" + best
	}
	return best, true
}

// requireCgroup2PathIn refuses path unless the covering mount among all mounts
// is a cgroup2 mount that path lies strictly below.
func requireCgroup2PathIn(mountinfo, path string) error {
	mount, ok := coveringMount(mountinfo, path)
	if !ok || mount.fstype != "cgroup2" || path == mount.mountpoint {
		return errors.New("Pi worker cgroup is not below a mounted cgroup-v2 filesystem")
	}
	return nil
}

// parseFdinfoMountID extracts the single mnt_id line of /proc/self/fdinfo/<fd>.
func parseFdinfoMountID(fdinfo string) (uint64, error) {
	var id uint64
	seen := 0
	for _, line := range strings.Split(fdinfo, "\n") {
		value, ok := strings.CutPrefix(line, "mnt_id:")
		if !ok {
			continue
		}
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed == 0 {
			return 0, errors.New("descriptor mount id is malformed")
		}
		id, seen = parsed, seen+1
	}
	if seen != 1 {
		return 0, errors.New("descriptor mount id is unavailable")
	}
	return id, nil
}

// requireDescriptorOnMount refuses a descriptor that does not live on mount.
func requireDescriptorOnMount(descriptorMountID uint64, mount mountinfoEntry) error {
	if descriptorMountID == 0 || descriptorMountID != mount.id {
		return errors.New("held cgroup descriptor is not on the mount that covers its path")
	}
	return nil
}

// observeCgroup2Descriptor verifies, from the HELD descriptor, that it is a
// cgroup2 directory, and returns its hierarchy path. The descriptor's own
// filesystem type (fstatfs == CGROUP2_SUPER_MAGIC), the covering mount of its
// path among all mounts (which must be cgroup2), and the correlation of the
// descriptor's mount id with that mount must all agree. A path-derived
// mountinfo match alone is never enough: a filesystem mounted over the path
// after the descriptor was opened changes the covering mount but not the
// descriptor, and is refused here.
func observeCgroup2Descriptor(f *os.File, mountinfo string) (string, error) {
	if f == nil {
		return "", ErrCgroupScopeRefused
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Fstatfs(int(f.Fd()), &filesystem); err != nil {
		return "", fmt.Errorf("fstatfs cgroup descriptor: %w", err)
	}
	if int64(filesystem.Type) != cgroup2SuperMagic {
		return "", errors.New("held cgroup descriptor is not on a cgroup2 filesystem")
	}
	path, err := cgroupDescriptorPath(f)
	if err != nil {
		return "", err
	}
	mount, ok := coveringMount(mountinfo, path)
	if !ok || mount.fstype != "cgroup2" || path == mount.mountpoint {
		return "", errors.New("cgroup descriptor path is not strictly below a covering cgroup2 mount")
	}
	id, err := descriptorMountID(f.Fd())
	if err != nil {
		return "", err
	}
	if err := requireDescriptorOnMount(id, mount); err != nil {
		return "", err
	}
	hierarchyPath, ok := cgroup2HierarchyPathFromMountinfo(mountinfo, path)
	if !ok {
		return "", errors.New("cgroup descriptor has no cgroup2 hierarchy path")
	}
	return hierarchyPath, nil
}

// requireCgroup2Descriptor is observeCgroup2Descriptor against the live mount table.
func requireCgroup2Descriptor(f *os.File) error {
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("inspect cgroup2 mount: %w", err)
	}
	_, err = observeCgroup2Descriptor(f, string(mountinfo))
	return err
}
