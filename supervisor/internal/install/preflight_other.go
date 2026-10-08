//go:build !linux

package install

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// DefaultProbes returns non-Linux probes. Every host-specific probe reports an
// error so preflight marks the platform unsupported instead of guessing.
func DefaultProbes() Probes {
	unsupported := func() error { return errors.New("unsupported on " + runtime.GOOS) }
	return Probes{
		GOOS:           runtime.GOOS,
		ReadFile:       os.ReadFile,
		Stat:           os.Stat,
		LookPath:       exec.LookPath,
		Run:            func(context.Context, string, ...string) (string, error) { return "", unsupported() },
		KernelRelease:  func() (string, error) { return "", unsupported() },
		LandlockABI:    func() (int, error) { return 0, unsupported() },
		PidfdSupported: func() (bool, error) { return false, unsupported() },
		UserName:       func() (string, error) { return currentUser(), nil },
		Cgroup2:        func() (CgroupInfo, error) { return CgroupInfo{}, unsupported() },
		OwnerUID:       func(string) (int, bool) { return 0, false },
	}
}
