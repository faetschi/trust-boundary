//go:build linux

package install

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

const (
	cgroup2SuperMagic        = 0x63677270
	landlockCreateRulesetVer = 1
	sysLandlockCreateRule    = 444
	sysPidfdOpen             = 434
)

// DefaultProbes returns real Linux host probes.
func DefaultProbes() Probes {
	return Probes{
		GOOS:          runtime.GOOS,
		ReadFile:      os.ReadFile,
		Stat:          os.Stat,
		LookPath:      exec.LookPath,
		Run:           runCommand,
		KernelRelease: kernelRelease,
		LandlockABI:   landlockABI,
		PidfdSupported: func() (bool, error) {
			return pidfdSupported()
		},
		UserName: func() (string, error) { return currentUser(), nil },
		Cgroup2:  cgroup2Info,
		OwnerUID: ownerUID,
	}
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func kernelRelease() (string, error) {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		return "", err
	}
	return charsToString(uts.Release[:]), nil
}

func charsToString(ca []int8) string {
	b := make([]byte, 0, len(ca))
	for _, c := range ca {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

func landlockABI() (int, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return 0, os.ErrInvalid
	}
	r1, _, errno := syscall.Syscall(sysLandlockCreateRule, 0, 0, uintptr(landlockCreateRulesetVer))
	if errno != 0 {
		return 0, errno
	}
	return int(r1), nil
}

func pidfdSupported() (bool, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return false, nil
	}
	_, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(os.Getpid()), 0, 0)
	if errno == 0 {
		return true, nil
	}
	if errno == syscall.ENOSYS {
		return false, nil
	}
	// EINVAL/EPERM still indicate the syscall exists.
	return errno != syscall.ENOSYS, nil
}

func cgroup2Info() (CgroupInfo, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/sys/fs/cgroup", &st); err != nil {
		return CgroupInfo{}, err
	}
	info := CgroupInfo{Unified: uint32(st.Type) == cgroup2SuperMagic}
	if data, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		info.Controllers = strings.TrimSpace(string(data))
	}
	info.SubtreeControlWritable = syscall.Access("/sys/fs/cgroup/cgroup.subtree_control", 0x2) == nil
	info.CanKill = fileExists("/sys/fs/cgroup/cgroup.kill")
	return info, nil
}

func ownerUID(path string) (int, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, false
	}
	return int(st.Uid), true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
