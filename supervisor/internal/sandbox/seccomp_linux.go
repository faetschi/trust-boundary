//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// Classic-BPF seccomp constants.
const (
	seccompSetModeFilter = 1 // SECCOMP_SET_MODE_FILTER

	seccompRetAllow       = 0x7fff0000
	seccompRetErrno       = 0x00050000
	seccompRetKillProcess = 0x80000000

	auditArchX86_64 = 0xC000003E

	bpfLoad  = 0x00
	bpfWord  = 0x00
	bpfAbs   = 0x20
	bpfJump  = 0x05
	bpfEqual = 0x10
	bpfConst = 0x00
	bpfRet   = 0x06

	seccompDataNr   = 0
	seccompDataArch = 4
)

type sockFilter struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// sockFprog mirrors struct sock_fprog; the pointer is 8-byte aligned.
type sockFprog struct {
	Len    uint16
	_      [6]byte
	Filter *sockFilter
}

// buildSeccompFilter compiles the pinned default-deny allowlist. A syscall
// absent from allowedSyscalls returns EPERM; a non-amd64 audit architecture
// kills the process.
func buildSeccompFilter() []sockFilter {
	program := make([]sockFilter, 0, len(allowedSyscalls)*2+5)
	program = append(program, sockFilter{Code: bpfLoad | bpfWord | bpfAbs, K: seccompDataArch})
	program = append(program, sockFilter{Code: bpfJump | bpfEqual | bpfConst, Jt: 1, Jf: 0, K: auditArchX86_64})
	program = append(program, sockFilter{Code: bpfRet, K: seccompRetKillProcess})
	program = append(program, sockFilter{Code: bpfLoad | bpfWord | bpfAbs, K: seccompDataNr})
	for _, number := range allowedSyscalls {
		program = append(program,
			sockFilter{Code: bpfJump | bpfEqual | bpfConst, Jt: 0, Jf: 1, K: number},
			sockFilter{Code: bpfRet, K: seccompRetAllow},
		)
	}
	program = append(program, sockFilter{Code: bpfRet, K: seccompRetErrno | uint32(syscall.EPERM)})
	return program
}

// installSeccomp installs the pinned filter on the calling thread. The caller
// must hold its OS thread so the filter survives until execve.
func installSeccomp() error {
	filter := buildSeccompFilter()
	program := sockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if _, _, errno := syscall.Syscall(uintptr(sysSeccomp), seccompSetModeFilter, 0,
		uintptr(unsafe.Pointer(&program))); errno != 0 {
		// Fall back to prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER) on kernels
		// that do not implement the seccomp(2) entry point.
		const (
			prSetSeccomp      = 22
			seccompModeFilter = 2
		)
		if _, _, fallback := syscall.Syscall6(sysPrctl, prSetSeccomp, seccompModeFilter,
			uintptr(unsafe.Pointer(&program)), 0, 0, 0); fallback != 0 {
			return fmt.Errorf("install seccomp filter: %w (fallback: %v)", errno, fallback)
		}
	}
	return nil
}

// seccompAvailable reports whether the running kernel exposes the errno and
// allow seccomp actions needed by the pinned profile.
func seccompAvailable() bool {
	data, err := os.ReadFile("/proc/sys/kernel/seccomp/actions_avail")
	if err != nil {
		return false
	}
	actions := string(data)
	return strings.Contains(actions, "errno") && strings.Contains(actions, "allow")
}
