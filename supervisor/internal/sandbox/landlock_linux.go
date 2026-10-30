//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Landlock ABI 1 access rights (Linux 5.13). LANDLOCK_ACCESS_FS_REFER and
// later rights are deliberately excluded: the dyn profile pins ABI 1.
const (
	landlockCreateRulesetVersion = 1 // LANDLOCK_CREATE_RULESET_VERSION
	landlockRulePathBeneath      = 1

	landlockFSExecute    = 1 << 0
	landlockFSWriteFile  = 1 << 1
	landlockFSReadFile   = 1 << 2
	landlockFSReadDir    = 1 << 3
	landlockFSRemoveDir  = 1 << 4
	landlockFSRemoveFile = 1 << 5
	landlockFSMakeChar   = 1 << 6
	landlockFSMakeDir    = 1 << 7
	landlockFSMakeReg    = 1 << 8
	landlockFSMakeSock   = 1 << 9
	landlockFSMakeFifo   = 1 << 10
	landlockFSMakeBlock  = 1 << 11
	landlockFSMakeSym    = 1 << 12

	landlockAccessAllABI1 = landlockFSExecute | landlockFSWriteFile | landlockFSReadFile |
		landlockFSReadDir | landlockFSRemoveDir | landlockFSRemoveFile | landlockFSMakeChar |
		landlockFSMakeDir | landlockFSMakeReg | landlockFSMakeSock | landlockFSMakeFifo |
		landlockFSMakeBlock | landlockFSMakeSym
)

// ErrLandlockUnsupported is returned when the running kernel does not provide
// the pinned Landlock ABI.
var ErrLandlockUnsupported = errors.New("landlock ABI 1 is not available")

// landlockABI returns the Landlock ABI version supported by the kernel.
func landlockABI() (int, error) {
	r, _, errno := syscall.Syscall(uintptr(sysLandlockCreateRuleset), 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return 0, fmt.Errorf("probe landlock ABI: %w", errno)
	}
	return int(r), nil
}

// landlockRule grants allowedAccess on path and everything beneath it. The
// path must already exist in the calling mount namespace.
type landlockRule struct {
	path          string
	allowedAccess uint64
}

// applyLandlock installs the pinned ABI-1 ruleset on the calling thread.
func applyLandlock(rules []landlockRule) error {
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	if abi < 1 {
		return fmt.Errorf("%w: kernel reports ABI %d", ErrLandlockUnsupported, abi)
	}
	ruleset := struct {
		HandledAccessFS uint64
	}{landlockAccessAllABI1}
	fd, _, errno := syscall.Syscall(uintptr(sysLandlockCreateRuleset),
		uintptr(unsafe.Pointer(&ruleset)), unsafe.Sizeof(ruleset), 0)
	if errno != 0 {
		return fmt.Errorf("create landlock ruleset: %w", errno)
	}
	defer syscall.Close(int(fd))
	for _, rule := range rules {
		parent, err := os.Open(rule.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("open landlock rule path %q: %w", rule.path, err)
		}
		attr := struct {
			AllowedAccess uint64
			ParentFd      int32
		}{rule.allowedAccess, int32(parent.Fd())}
		_, _, errno := syscall.Syscall6(uintptr(sysLandlockAddRule), fd, landlockRulePathBeneath,
			uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
		closeErr := parent.Close()
		if errno != 0 {
			return fmt.Errorf("add landlock rule %q: %w", rule.path, errno)
		}
		if closeErr != nil {
			return fmt.Errorf("close landlock rule path %q: %w", rule.path, closeErr)
		}
	}
	if _, _, errno := syscall.Syscall(uintptr(sysLandlockRestrictSelf), fd, 0, 0); errno != 0 {
		return fmt.Errorf("restrict self with landlock: %w", errno)
	}
	return nil
}
