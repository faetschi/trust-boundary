// Package faultfs is the single filesystem fault-injection seam of the trusted
// supervisor. The session repository, the workspace importer and the audit
// journal call Check immediately before the filesystem operation they are
// about to perform; a conformance test installs a Hook that fails chosen
// operations with a typed errno (ENOSPC, EDQUOT, EIO, EROFS, EXDEV) and then
// asserts fail-closed behavior. The kernel cannot be asked for EDQUOT or a
// targeted EIO without privileges, so the injected error replaces the
// operation instead of simulating it: the operation is not performed.
//
// With no Hook installed, Check is one atomic load and returns nil. Install is
// reserved for tests; TestInstallIsOnlyCalledFromTests enforces that no
// production file in the module installs a hook.
package faultfs

import "sync/atomic"

// Op is the kind of filesystem operation about to be performed.
type Op string

const (
	OpMkdir  Op = "mkdir"
	OpCreate Op = "create"
	OpWrite  Op = "write"
	OpFsync  Op = "fsync"
	OpRename Op = "rename"
	OpUnlink Op = "unlink"
)

// Site names the code path that performs the operation. Sites are closed
// constants so a test names the exact boundary it fails.
type Site string

const (
	// SiteWorkspaceImport covers every create, write and sync of
	// workspace.Import. The destination is a command view during view setup
	// and a staging generation during copy-on-write and command import.
	SiteWorkspaceImport Site = "workspace.import"
	SiteViewSetup       Site = "sessionrepo.view-setup"
	SiteStageSetup      Site = "sessionrepo.stage-setup"
	SiteStageApply      Site = "sessionrepo.stage-apply"
	SitePromote         Site = "sessionrepo.promote"
	SiteViewTeardown    Site = "sessionrepo.view-teardown"
	SiteHeadState       Site = "sessionrepo.head-state"
	SiteAuditJournal    Site = "audit.journal"
)

// Hook decides whether the operation fails. Returning nil lets it proceed. It
// may be called from any goroutine and must not call back into the supervisor.
type Hook func(Site, Op) error

var current atomic.Pointer[Hook]

// Check returns the error a test injected for this operation, or nil.
func Check(site Site, op Op) error {
	hook := current.Load()
	if hook == nil {
		return nil
	}
	return (*hook)(site, op)
}

// Install replaces the process-wide hook and returns a function that restores
// the previous one. Tests that install a hook must not run in parallel.
func Install(hook Hook) (restore func()) {
	var next *Hook
	if hook != nil {
		next = &hook
	}
	previous := current.Swap(next)
	return func() { current.Store(previous) }
}
