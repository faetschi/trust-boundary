//go:build linux && amd64

package sandbox

// Raw Linux/amd64 syscall numbers used by the cell. The standard syscall
// package does not expose Landlock or pidfd numbers on amd64, so they are
// pinned here for the frozen Linux/amd64 profile.
const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446
	sysPidfdOpen             = 434
	sysSeccomp               = 317
	sysPrctl                 = 157
	sysCapset                = 126
)
