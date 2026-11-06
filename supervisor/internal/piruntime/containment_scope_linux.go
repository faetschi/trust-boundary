//go:build linux

// containment_scope_linux.go: D09 podman/crun launcher state, managed live root binding and owned command cgroup scopes.

package piruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

type d09LaunchRecord struct {
	proof D09TCBProof
	// hostNamespaces are the host namespace inodes captured when this cell was launched. The
	// proof's namespaces are checked against them, not against a re-capture at validation time.
	hostNamespaces D09NamespaceInodes
	pidfd          *os.File
	stdout         io.ReadCloser
	proofRead      bool
	settled        bool
	waitDone       chan struct{}
	waitErr        error
	ownerMu        sync.Mutex
	stdoutClosed   bool
	stdoutCloseErr error
	pidfdCloseErr  error
}

// NewD09PodmanCrunLauncher builds a launcher bound to the real host exec runner.
// A launched cell additionally requires BindCgroupScope; without a verified
// scope the launcher refuses admission and cannot launch.
func NewD09PodmanCrunLauncher(config D09Config) *D09PodmanCrunLauncher {
	return &D09PodmanCrunLauncher{runner: newExecD09CommandRunner(), config: config.normalized()}
}

// BindCgroupScope binds the supervisor-owned, verified cgroup-v2 scope that
// settlement and cleanup act on. It is supplied by the trusted composition root
// from a held delegated-root descriptor; a nil scope is refused.
func (l *D09PodmanCrunLauncher) BindCgroupScope(scope *LinuxCgroupScope) error {
	if l == nil || scope == nil || scope.file == nil || scope.root == nil {
		return ErrCgroupScopeRefused
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed || l.scope != nil {
		return ErrCgroupScopeRefused
	}
	if err := validateCgroupDirectory(scope.file); err != nil {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	if err := enableD09SubtreeControllers(scope.file); err != nil {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	l.scope = scope
	return nil
}

// BindManagedLiveRoot binds the exact registration-verification object C's
// ManagedLiveRootAuthority also consumes. The binding establishes root/service
// policy identity only; it does not grant exclusivity or a quiescence permit.
func (l *D09PodmanCrunLauncher) BindManagedLiveRoot(verified *VerifiedD09ManagedLiveRoot) error {
	if l == nil || verified == nil {
		return ErrD09ManagedLiveRootRefused
	}
	l.stateMu.Lock()
	if l.closed || l.activeOps != 0 {
		l.stateMu.Unlock()
		return ErrD09ManagedLiveRootRefused
	}
	l.managedRootMu.Lock()
	alreadyBound := l.managedRoot != nil
	l.managedRootMu.Unlock()
	if alreadyBound {
		l.stateMu.Unlock()
		return ErrD09ManagedLiveRootRefused
	}
	l.stateMu.Unlock()
	registration := verified.Registration()
	if registration.Digest != l.config.ManagedLiveRootRegistrationDigest {
		return ErrD09ManagedLiveRootRefused
	}
	owned, err := verified.DuplicateCapability(context.Background())
	if err != nil {
		return err
	}
	if err := owned.Revalidate(context.Background(), owned.root); err != nil {
		_ = owned.Close()
		return err
	}
	l.stateMu.Lock()
	if l.closed || l.activeOps != 0 {
		l.stateMu.Unlock()
		_ = owned.Close()
		return ErrD09ManagedLiveRootRefused
	}
	l.managedRootMu.Lock()
	if l.managedRoot != nil {
		l.managedRootMu.Unlock()
		l.stateMu.Unlock()
		_ = owned.Close()
		return ErrD09ManagedLiveRootRefused
	}
	l.managedRoot = owned
	l.config.UserNamespaceHostUID = registration.ServiceUID
	l.config.UserNamespaceHostGID = registration.ServiceGID
	l.managedRootMu.Unlock()
	l.stateMu.Unlock()
	return nil
}

// ManagedLiveRootRegistration returns the currently bound trusted registration
// snapshot. It is policy metadata only, not the CLI's exclusive writer permit.
func (l *D09PodmanCrunLauncher) ManagedLiveRootRegistration() (D09ManagedLiveRootRegistration, bool) {
	if l == nil {
		return D09ManagedLiveRootRegistration{}, false
	}
	l.managedRootMu.Lock()
	defer l.managedRootMu.Unlock()
	if l.managedRoot == nil {
		return D09ManagedLiveRootRegistration{}, false
	}
	return l.managedRoot.Registration(), true
}

func (l *D09PodmanCrunLauncher) ensureManagedLiveRoot(ctx context.Context) (D09ManagedLiveRootRegistration, error) {
	if l == nil || ctx == nil || !l.productionRunner() {
		return D09ManagedLiveRootRegistration{}, ErrD09ManagedLiveRootRefused
	}
	l.managedRootMu.Lock()
	defer l.managedRootMu.Unlock()
	if l.managedRoot != nil {
		if l.managedRoot.registration.Digest != l.config.ManagedLiveRootRegistrationDigest {
			return D09ManagedLiveRootRegistration{}, ErrD09ManagedLiveRootRefused
		}
		if err := l.managedRoot.Revalidate(ctx, l.managedRoot.root); err != nil {
			return D09ManagedLiveRootRegistration{}, err
		}
		return l.managedRoot.Registration(), nil
	}
	registration, err := LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	if registration.Digest != l.config.ManagedLiveRootRegistrationDigest {
		return D09ManagedLiveRootRegistration{}, ErrD09ManagedLiveRootRefused
	}
	root, err := openManagedLiveRoot(registration.RootPath)
	if err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	verified, err := VerifyD09ManagedLiveRoot(ctx, root)
	_ = root.Close()
	if err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	defer verified.Close()
	return registration, nil
}

func (l *D09PodmanCrunLauncher) beginOperation() error {
	if l == nil {
		return ErrProductionLauncherUnavailable
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed {
		return ErrProductionLauncherUnavailable
	}
	l.activeOps++
	return nil
}

func (l *D09PodmanCrunLauncher) endOperation() {
	if l == nil {
		return
	}
	l.stateMu.Lock()
	if l.activeOps > 0 {
		l.activeOps--
	}
	l.stateMu.Unlock()
}

func (l *D09PodmanCrunLauncher) retainCommandReceipt() error {
	if l == nil {
		return ErrProductionLauncherUnavailable
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed {
		return ErrProductionLauncherUnavailable
	}
	l.pendingCommandReceipts++
	return nil
}

func (l *D09PodmanCrunLauncher) releaseCommandReceipt() {
	if l == nil {
		return
	}
	l.stateMu.Lock()
	if l.pendingCommandReceipts > 0 {
		l.pendingCommandReceipts--
	}
	l.stateMu.Unlock()
}

// Close deterministically unbinds and closes only the launcher's own duplicate
// of the managed-root registration. It never closes the caller's verified
// authority object or cgroup scope. Close refuses while a launch, proof, or
// command cgroup is active, so the caller must settle/remove every exact cell
// first. The C publication authority can retain its independent permit handle.
func (l *D09PodmanCrunLauncher) Close(ctx context.Context) error {
	if l == nil || ctx == nil {
		return ErrProductionLauncherUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.stateMu.Lock()
	if l.closed {
		l.stateMu.Unlock()
		return nil
	}
	if l.activeOps != 0 || l.pendingCommandReceipts != 0 {
		l.stateMu.Unlock()
		return ErrUnknownSettlement
	}
	l.proofMu.Lock()
	activeProof := l.launchInProg || len(l.proofs) != 0
	l.proofMu.Unlock()
	l.commandMu.Lock()
	activeCommands := len(l.commandScopes) != 0
	l.commandMu.Unlock()
	workerScopeOpen := false
	if l.scope != nil {
		l.scope.mu.Lock()
		workerScopeOpen = !l.scope.closed
		l.scope.mu.Unlock()
	}
	if activeProof || activeCommands || workerScopeOpen {
		l.stateMu.Unlock()
		return ErrUnknownSettlement
	}
	l.closed = true
	l.stateMu.Unlock()
	l.managedRootMu.Lock()
	managedRoot := l.managedRoot
	if managedRoot != nil {
		if err := managedRoot.Close(); err != nil {
			l.managedRootMu.Unlock()
			l.stateMu.Lock()
			l.closed = false
			l.stateMu.Unlock()
			return err
		}
		l.managedRoot = nil
	}
	l.managedRootMu.Unlock()
	l.admissionMu.Lock()
	l.admittedProfile = HostProfile{}
	l.profileAdmitted = false
	l.admissionMu.Unlock()
	return nil
}

// NewCommandScope creates a unique direct-child cgroup of the held, verified
// D09 worker scope. It consumes the authenticated lease/view identity only as
// input to freshness/identity validation; no caller-supplied path is accepted.
func (l *D09PodmanCrunLauncher) NewCommandScope(ctx context.Context, leaseID, viewID string) (*d09OwnedCommandScope, error) {
	return l.newCommandScope(ctx, leaseID, viewID, false, false)
}

// NewDetachedCommandScope is NewCommandScope for the no-reap/v1 command cell: the unique cgroup is a
// direct child of the delegated cells root, a sibling of the worker scope and not below it, so the
// worker scope's recursive cgroup.kill at settlement cannot reach a surviving descendant. The scope
// is never removed by the lease; abandon() hands it over to the operator reset.
func (l *D09PodmanCrunLauncher) NewDetachedCommandScope(ctx context.Context, leaseID, viewID string) (*d09OwnedCommandScope, error) {
	return l.newCommandScope(ctx, leaseID, viewID, true, false)
}

// NewNetSinkCommandScope is NewCommandScope for the net-sink/v1 command cell: the unique cgroup is a
// child of the fixed parent D09NetSinkCgroupName of the cells cgroup, which the guest nftables rule
// matches. The parent must exist and is only opened here, never created or removed. The scope is
// settled and removed by the lease like any other command scope.
func (l *D09PodmanCrunLauncher) NewNetSinkCommandScope(ctx context.Context, leaseID, viewID string) (*d09OwnedCommandScope, error) {
	return l.newCommandScope(ctx, leaseID, viewID, false, true)
}

func (l *D09PodmanCrunLauncher) newCommandScope(ctx context.Context, leaseID, viewID string, detached, netSink bool) (*d09OwnedCommandScope, error) {
	if l == nil || l.scope == nil || ctx == nil || !validIdentity(leaseID) || !validIdentity(viewID) {
		return nil, ErrCgroupScopeRefused
	}
	if err := l.beginOperation(); err != nil {
		return nil, err
	}
	defer l.endOperation()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parentPath, err := l.scope.Path()
	if err != nil {
		return nil, err
	}
	if detached {
		if parentPath, err = cgroupDescriptorPath(l.scope.root); err != nil {
			return nil, err
		}
	}
	name := "tbound-cmd-" + d09NewToken()
	parentDir := l.scope.file
	if detached {
		name = d09DetachedScopePrefix + d09NewToken()
		parentDir = l.scope.root
		if parentDir == nil {
			return nil, ErrCgroupScopeRefused
		}
	}
	if netSink {
		dir, err := l.openNetSinkParent(netSinkLoadAttested)
		if err != nil {
			return nil, err
		}
		defer dir.Close()
		parentDir = dir
		if parentPath, err = cgroupDescriptorPath(dir); err != nil {
			return nil, err
		}
	}
	parentFD, err := syscall.Openat(int(parentDir.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate D09 delegated cgroup root: %w", err)
	}
	parent := os.NewFile(uintptr(parentFD), "d09-command-parent")
	if err := syscall.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("create unique D09 command cgroup: %w", err)
	}
	childFD, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = unlinkatDir(int(parent.Fd()), name)
		_ = parent.Close()
		return nil, err
	}
	child := os.NewFile(uintptr(childFD), "d09-command-scope")
	scope, err := NewLinuxCgroupScope(parent, child, D09ContainmentProfile, D09SettlementProfile)
	if err != nil {
		_ = child.Close()
		_ = parent.Close()
		return nil, errors.Join(err, unlinkatDir(int(parentDir.Fd()), name))
	}
	if err := validateCgroupDirectory(scope.file); err != nil {
		_ = scope.Close()
		_ = unlinkatDir(int(parentDir.Fd()), name)
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	if err := enableD09SubtreeControllers(scope.file); err != nil {
		_ = scope.Close()
		_ = unlinkatDir(int(parentDir.Fd()), name)
		return nil, fmt.Errorf("enable D09 command-scope cpu/memory/pids controllers: %w", err)
	}
	owned := &d09OwnedCommandScope{launcher: l, name: name, scope: scope, leaseID: leaseID, viewID: viewID, parentPath: parentPath, detached: detached}
	l.commandMu.Lock()
	if l.commandScopes == nil {
		l.commandScopes = make(map[string]*d09OwnedCommandScope)
	}
	if _, collision := l.commandScopes[name]; collision {
		l.commandMu.Unlock()
		_ = scope.Close()
		_ = unlinkatDir(int(parentDir.Fd()), name)
		return nil, ErrCgroupScopeRefused
	}
	l.commandScopes[name] = owned
	l.commandMu.Unlock()
	return owned, nil
}

func unlinkatDir(parentFD int, name string) error {
	namePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(parentFD), uintptr(unsafe.Pointer(namePtr)), uintptr(0x200)) // AT_REMOVEDIR
	if errno != 0 {
		return errno
	}
	return nil
}

type d09OwnedCommandScope struct {
	launcher   *D09PodmanCrunLauncher
	name       string
	scope      *LinuxCgroupScope
	leaseID    string
	viewID     string
	parentPath string
	detached   bool

	mu         sync.Mutex
	settled    bool
	reconciled bool
	removed    bool
	removing   bool
	closed     bool
}

func (s *d09OwnedCommandScope) Path() (string, error) {
	if s == nil || s.scope == nil {
		return "", ErrCgroupScopeRefused
	}
	return s.scope.Path()
}

func (s *d09OwnedCommandScope) ContainsPID(pid int) (bool, error) {
	if s == nil || s.scope == nil {
		return false, ErrCgroupScopeRefused
	}
	return s.scope.ContainsPID(pid)
}

func (s *d09OwnedCommandScope) Settle(ctx context.Context) (bool, error) {
	if s == nil || s.scope == nil || ctx == nil {
		return false, ErrCgroupScopeRefused
	}
	settled, err := s.scope.Settle(ctx)
	if err == nil && settled {
		s.mu.Lock()
		s.settled = true
		s.mu.Unlock()
	}
	return settled, err
}

func (s *d09OwnedCommandScope) IsSettled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settled
}

// MarkReconciled records that the caller independently observed container
// removal, leader pidfd exit, and detached command-view mount references in
// addition to this scope's populated=0. Without this exact cleanup proof Close
// refuses and the scope remains retained for reconciliation.
func (s *d09OwnedCommandScope) MarkReconciled() error {
	if s == nil {
		return ErrCgroupScopeRefused
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.settled || s.closed {
		return ErrUnknownSettlement
	}
	s.reconciled = true
	return nil
}

// Close removes the scope only after its held cgroup.kill/populated observer
// established empty. If cleanup is UNKNOWN, descriptors and directory ownership
// remain retained by the launcher for reconciliation.
func (s *d09OwnedCommandScope) Close() error {
	if s == nil {
		return ErrCgroupScopeRefused
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if !s.settled || !s.reconciled || !s.removed || s.removing {
		s.mu.Unlock()
		return ErrUnknownSettlement
	}
	s.mu.Unlock()
	closeErr := s.scope.Close()
	if closeErr != nil {
		return closeErr
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if s.launcher == nil || s.launcher.scope == nil || s.launcher.scope.file == nil {
		return ErrCgroupScopeRefused
	}
	if s.launcher != nil {
		s.launcher.commandMu.Lock()
		delete(s.launcher.commandScopes, s.name)
		s.launcher.commandMu.Unlock()
	}
	return nil
}

// Remove unlinks the already-settled command cgroup by name relative to the
// held delegated scope before closing descriptors. A failure leaves the handles
// retained and the name registered for reconciliation.
func (s *d09OwnedCommandScope) Remove() error {
	if s == nil || s.launcher == nil || s.launcher.scope == nil || s.launcher.scope.file == nil || s.scope == nil || s.scope.root == nil {
		return ErrCgroupScopeRefused
	}
	s.mu.Lock()
	if s.closed || !s.settled || !s.reconciled || s.removing {
		s.mu.Unlock()
		return ErrUnknownSettlement
	}
	if s.removed {
		s.mu.Unlock()
		return s.Close()
	}
	s.removing = true
	s.mu.Unlock()
	removeContext, cancelRemove := context.WithTimeout(context.Background(), d09CleanupTimeout)
	err := removeSettledCgroup(removeContext, int(s.scope.root.Fd()), s.name)
	cancelRemove()
	s.mu.Lock()
	s.removing = false
	if err == nil {
		s.removed = true
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("remove reconciled command cgroup: %w", err)
	}
	return s.Close()
}

// d09DetachedScopePrefix names the command cgroups of the no-reap/v1 cell. The operator reset
// recognises them by this prefix.
const d09DetachedScopePrefix = "tbound-nr-"

// abandon releases a detached command scope from the lease without killing or removing it: the
// no-reap/v1 cell is left running by design. It closes the held descriptors and forgets the scope
// in the launcher, so a later launcher Close does not wait for it. The cgroup directory and its
// processes stay for the operator reset.
func (s *d09OwnedCommandScope) abandon() error {
	if s == nil || s.launcher == nil || !s.detached {
		return ErrCgroupScopeRefused
	}
	s.mu.Lock()
	if s.closed || s.removing {
		s.mu.Unlock()
		return ErrUnknownSettlement
	}
	s.closed, s.settled = true, false
	s.mu.Unlock()
	closeErr := s.scope.Close()
	s.launcher.commandMu.Lock()
	delete(s.launcher.commandScopes, s.name)
	s.launcher.commandMu.Unlock()
	return closeErr
}

// populated reads cgroup.events of the scope without any kill.
func (s *d09OwnedCommandScope) populated() (bool, error) {
	if s == nil || s.scope == nil || s.scope.file == nil {
		return false, ErrCgroupScopeRefused
	}
	return readCgroupPopulated(s.scope.file)
}
