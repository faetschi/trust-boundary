//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

const (
	siocgifFlags = 0x8913
	siocsifFlags = 0x8914
	iffUp        = 0x1
)

// makeMountsPrivate detaches every mount in the calling mount namespace from
// propagation to the host, so cell mounts never leak outward.
func makeMountsPrivate() error {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	return nil
}

// mountTmpfs mounts a fresh tmpfs at target.
func mountTmpfs(target, options string) error {
	if err := syscall.Mount("tmpfs", target, "tmpfs", 0, options); err != nil {
		return fmt.Errorf("mount tmpfs at %s: %w", target, err)
	}
	return nil
}

// bindMount binds source onto target. The target must already exist.
func bindMount(source, target string, recursive bool) error {
	flags := uintptr(syscall.MS_BIND)
	if recursive {
		flags |= syscall.MS_REC
	}
	if err := syscall.Mount(source, target, "", flags, ""); err != nil {
		return fmt.Errorf("bind %s -> %s: %w", source, target, err)
	}
	return nil
}

// remountReadOnly makes an existing bind mount read-only.
func remountReadOnly(target string) error {
	if err := syscall.Mount("", target, "", syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("remount %s read-only: %w", target, err)
	}
	return nil
}

// mountProc mounts a fresh procfs for the calling PID namespace.
func mountProc(target string) error {
	if err := syscall.Mount("proc", target, "proc", 0, ""); err != nil {
		return fmt.Errorf("mount proc at %s: %w", target, err)
	}
	return nil
}

// bringUpLoopback enables the loopback interface in the calling network
// namespace. There is no route to any other interface because the namespace
// contains no other interface.
func bringUpLoopback() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open loopback control socket: %w", err)
	}
	defer syscall.Close(fd)
	var ifreq [40]byte
	copy(ifreq[:16], "lo")
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocgifFlags,
		uintptr(unsafe.Pointer(&ifreq[0]))); errno != 0 {
		return fmt.Errorf("read loopback flags: %w", errno)
	}
	flags := *(*uint16)(unsafe.Pointer(&ifreq[16]))
	flags |= iffUp
	*(*uint16)(unsafe.Pointer(&ifreq[16])) = flags
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocsifFlags,
		uintptr(unsafe.Pointer(&ifreq[0]))); errno != 0 {
		return fmt.Errorf("set loopback up: %w", errno)
	}
	return nil
}

// existingDirectory reports whether path is a real directory (not a symlink).
func existingDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

// mountPointsUnder returns every mount point equal to root or nested beneath
// it, deepest first, from the calling mount namespace's mountinfo.
func mountPointsUnder(root string) []string {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	var points []string
	prefix := root + "/"
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		point := unescapeMountPath(fields[4])
		if point == root || strings.HasPrefix(point, prefix) {
			points = append(points, point)
		}
	}
	sort.Slice(points, func(i, j int) bool { return len(points[i]) > len(points[j]) })
	return points
}

func unescapeMountPath(value string) string {
	value = strings.ReplaceAll(value, `\040`, " ")
	value = strings.ReplaceAll(value, `\011`, "\t")
	value = strings.ReplaceAll(value, `\012`, "\n")
	value = strings.ReplaceAll(value, `\134`, `\`)
	return value
}

// detachMountTree lazily detaches root and every nested mount. Lazy detach is
// a real detachment from this namespace and never blocks on a busy subtree.
// The returned strings are failures.
func detachMountTree(root string) []string {
	var failures []string
	points := mountPointsUnder(root)
	if len(points) == 0 {
		points = []string{root}
	}
	for _, point := range points {
		if err := syscall.Unmount(point, 0); err == nil {
			continue
		}
		if err := syscall.Unmount(point, syscall.MNT_DETACH); err != nil &&
			err != syscall.EINVAL && err != syscall.ENOENT {
			failures = append(failures, fmt.Sprintf("unmount %s: %v", point, err))
		}
	}
	return failures
}
