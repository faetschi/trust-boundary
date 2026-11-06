//go:build linux

package piruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	PiRuntimeBundleMaxFiles = 100_000
	PiRuntimeBundleMaxBytes = int64(4 << 30)
	piWorkerStartupTimeout  = 90 * time.Second
	piWorkerStopTimeout     = 5 * time.Second
)

var (
	ErrLinuxLauncherRefused = errors.New("Linux governed Pi launcher refused the child plan")
	ErrRuntimeBundleRefused = errors.New("Pi runtime bundle is not immutable and profile-bound")
	ErrCgroupScopeRefused   = errors.New("delegated Linux cgroup scope is not admitted")
)

// LinuxCgroupScope wraps a pre-created, supervisor-owned cgroup-v2 directory
// supplied by the trusted host/profile loader. It does not create, delegate,
// or broaden cgroups. For D09 deployments the descriptor must belong to the
// frozen rootless Podman/crun cell profile. A missing/unusable descriptor is a
// hard refusal, never a process-group fallback.
type LinuxCgroupScope struct {
	root        *os.File
	file        *os.File
	rootDigest  string
	containment string
	settlement  string
	mu          sync.Mutex
	closed      bool
}

func NewLinuxCgroupScope(delegatedRoot, directory *os.File, containmentProfile, settlementProfile string) (*LinuxCgroupScope, error) {
	return newLinuxCgroupScope(delegatedRoot, directory, containmentProfile, settlementProfile, true)
}

func newLinuxCgroupScope(delegatedRoot, directory *os.File, containmentProfile, settlementProfile string,
	requireEmpty bool) (*LinuxCgroupScope, error) {
	if delegatedRoot == nil || directory == nil || !validIdentity(containmentProfile) || !validIdentity(settlementProfile) {
		return nil, ErrCgroupScopeRefused
	}
	if delegatedRoot == directory || sameBinding(delegatedRoot, directory) {
		return nil, errors.New("delegated cgroup root and per-worker scope must be distinct handles")
	}
	rootInfo, err := delegatedRoot.Stat()
	if err != nil || !rootInfo.IsDir() {
		return nil, ErrCgroupScopeRefused
	}
	rootPath, err := cgroupDescriptorPath(delegatedRoot)
	if err != nil {
		return nil, err
	}
	directoryPath, err := cgroupDescriptorPath(directory)
	if err != nil || filepath.Dir(directoryPath) != rootPath {
		return nil, errors.New("Pi worker cgroup must be a unique direct child of the delegated root descriptor")
	}
	rootStat, rootOK := rootInfo.Sys().(*syscall.Stat_t)
	if !rootOK || rootStat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("delegated cgroup root must be owned by the launching UID")
	}
	rootControllers, err := readAt(delegatedRoot, "cgroup.controllers", 1024)
	if err != nil || !hasCgroupController(rootControllers, "cpu") || !hasCgroupController(rootControllers, "memory") || !hasCgroupController(rootControllers, "pids") {
		return nil, errors.New("delegated cgroup root lacks cpu/memory/pids controllers")
	}
	rootSubtree, err := readAt(delegatedRoot, "cgroup.subtree_control", 1024)
	if err != nil || !hasCgroupController(rootSubtree, "cpu") || !hasCgroupController(rootSubtree, "memory") || !hasCgroupController(rootSubtree, "pids") {
		return nil, errors.New("delegated cgroup root has not enabled cpu/memory/pids controllers")
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return nil, ErrCgroupScopeRefused
	}
	if err := validateCgroupDirectoryState(directory, requireEmpty); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCgroupScopeRefused, err)
	}
	directoryStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || directoryStat.Uid != uint32(os.Geteuid()) || directoryStat.Dev != rootStat.Dev {
		return nil, errors.New("Pi worker cgroup identity differs from delegated root")
	}
	if err := requireCgroup2Descriptor(directory); err != nil {
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	// The root identity binds only stable facts (hierarchy path, cgroup2, owner
	// uid, required controllers). The root's device/inode/mount id change at
	// every service start and are deliberately not part of it.
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("inspect cgroup2 mount: %w", err)
	}
	// The held delegated-root descriptor itself must be cgroup2 (fstatfs) and on the
	// mount that covers its path among ALL mounts; a mount over the path is refused.
	hierarchyPath, observeErr := observeCgroup2Descriptor(delegatedRoot, string(mountinfo))
	rootDigest, err := cgroupRootFacts{hierarchyPath: hierarchyPath, cgroup2: observeErr == nil, ownerUID: rootStat.Uid,
		controllers: rootControllers, subtree: rootSubtree, device: uint64(rootStat.Dev), inode: rootStat.Ino,
	}.stableDigest()
	if err != nil {
		return nil, errors.Join(ErrCgroupScopeRefused, err, observeErr)
	}
	return &LinuxCgroupScope{root: delegatedRoot, file: directory, rootDigest: rootDigest, containment: containmentProfile, settlement: settlementProfile}, nil
}

// OpenLinuxCgroupScopeForDrain reopens one exact, direct-child D09 scope using
// the held delegated-root descriptor. It permits populated state only so the
// caller can immediately revoke it through Remove; it never enables controllers
// or launches a process. expectedIdentity binds the exact retained child inode.
func OpenLinuxCgroupScopeForDrain(delegatedRoot *os.File, childName, containmentProfile,
	settlementProfile, expectedIdentity string) (*LinuxCgroupScope, error) {
	if delegatedRoot == nil || !validIdentity(childName) || strings.ContainsAny(childName, "/\\\x00") {
		return nil, ErrCgroupScopeRefused
	}
	rootFD, err := syscall.Openat(int(delegatedRoot.Fd()), ".",
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	root := os.NewFile(uintptr(rootFD), "tbound-cgroup-drain-root")
	childFD, err := syscall.Openat(int(root.Fd()), childName,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = root.Close()
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	child := os.NewFile(uintptr(childFD), "tbound-cgroup-drain-child")
	scope, err := newLinuxCgroupScope(root, child, containmentProfile, settlementProfile, false)
	if err != nil {
		_ = child.Close()
		_ = root.Close()
		return nil, err
	}
	identity := scope.IdentityDigest()
	if identity == "" || expectedIdentity != "" && identity != expectedIdentity {
		_ = scope.Close()
		return nil, ErrCgroupScopeRefused
	}
	return scope, nil
}

// CreateLinuxCgroupScope creates a unique direct-child cgroup using the held
// delegated-root descriptor (never a caller path), then returns a verified
// scope which owns both descriptors. The child name is one clean path component.
// The caller removes it with scope.Remove(ctx); on UNKNOWN it must retain the
// returned scope handle for reconciliation.
func CreateLinuxCgroupScope(delegatedRoot *os.File, childName, containmentProfile, settlementProfile string) (*LinuxCgroupScope, error) {
	if delegatedRoot == nil || !validIdentity(childName) || strings.ContainsAny(childName, "/\\") ||
		!validIdentity(containmentProfile) || !validIdentity(settlementProfile) {
		return nil, ErrCgroupScopeRefused
	}
	rootInfo, err := delegatedRoot.Stat()
	if err != nil || !rootInfo.IsDir() {
		return nil, ErrCgroupScopeRefused
	}
	if err := requireCgroup2Descriptor(delegatedRoot); err != nil {
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	rootType, err := readAt(delegatedRoot, "cgroup.type", 128)
	if err != nil || strings.TrimSpace(string(rootType)) != "domain" {
		return nil, errors.New("delegated cgroup root is not a non-threaded domain")
	}
	rootOwner, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || rootOwner.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("delegated cgroup root is not owned by the launching uid")
	}
	parentFD, err := syscall.Openat(int(delegatedRoot.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate delegated cgroup root: %w", err)
	}
	parent := os.NewFile(uintptr(parentFD), "tbound-cgroup-root")
	if err := syscall.Mkdirat(int(delegatedRoot.Fd()), childName, 0o700); err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("create unique cgroup child via held root fd: %w", err)
	}
	childFD, err := syscall.Openat(int(parent.Fd()), childName, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = unlinkatDir(int(parent.Fd()), childName)
		_ = parent.Close()
		return nil, fmt.Errorf("open unique cgroup child: %w", err)
	}
	child := os.NewFile(uintptr(childFD), "tbound-cgroup-child")
	scope, err := NewLinuxCgroupScope(parent, child, containmentProfile, settlementProfile)
	if err != nil {
		_ = child.Close()
		_ = unlinkatDir(int(parent.Fd()), childName)
		_ = parent.Close()
		return nil, err
	}
	if err := validateCgroupDirectory(scope.file); err != nil {
		_ = unlinkatDir(int(scope.root.Fd()), childName)
		_ = scope.Close()
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	if err := enableD09SubtreeControllers(scope.file); err != nil {
		_ = unlinkatDir(int(scope.root.Fd()), childName)
		_ = scope.Close()
		return nil, fmt.Errorf("enable D09 session-scope cpu/memory/pids controllers: %w", err)
	}
	return scope, nil
}

// enableD09SubtreeControllers enables exactly the resource controllers required
// by the frozen D09 profile on a newly-created, empty scope. It never acts on a
// caller path and refuses missing controller delegation.
func enableD09SubtreeControllers(scope *os.File) error {
	if scope == nil {
		return ErrCgroupScopeRefused
	}
	const required = "cpu,memory,pids"
	available, err := readAt(scope, "cgroup.controllers", 1024)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(required, ",") {
		if !hasCgroupController(available, name) {
			return fmt.Errorf("D09 cgroup lacks delegated %s controller", name)
		}
	}
	current, err := readAt(scope, "cgroup.subtree_control", 1024)
	if err != nil {
		return err
	}
	var missing []string
	for _, name := range strings.Split(required, ",") {
		if !hasCgroupController(current, name) {
			missing = append(missing, "+"+name)
		}
	}
	if len(missing) != 0 {
		fd, err := syscall.Openat(int(scope.Fd()), "cgroup.subtree_control", syscall.O_WRONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), "d09-cgroup-subtree-control")
		_, writeErr := file.WriteString(strings.Join(missing, " "))
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return err
		}
	}
	verified, err := readAt(scope, "cgroup.subtree_control", 1024)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(required, ",") {
		if !hasCgroupController(verified, name) {
			return fmt.Errorf("D09 cgroup subtree failed to enable %s", name)
		}
	}
	return nil
}

func (s *LinuxCgroupScope) BoundSettlement() DescendantSettler { return s }

func (s *LinuxCgroupScope) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.file.Close(), s.root.Close())
}

// Remove settles and removes this exact unique direct-child cgroup using the
// held parent descriptor. It is the production teardown API for a session-owned
// worker scope. On any error the descriptors remain open and the scope remains
// available for UNKNOWN/reconciliation; it never unlinks by caller path or
// removes the delegated parent.
func (s *LinuxCgroupScope) Remove(ctx context.Context) error {
	if s == nil || ctx == nil || s.root == nil || s.file == nil {
		return ErrCgroupScopeRefused
	}
	rootPath, err := cgroupDescriptorPath(s.root)
	if err != nil {
		return err
	}
	childPath, err := cgroupDescriptorPath(s.file)
	if err != nil || filepath.Dir(childPath) != rootPath {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	settled, err := s.Settle(ctx)
	if err != nil || !settled {
		return errors.Join(ErrUnknownSettlement, err)
	}
	if err := removeSettledCgroup(ctx, int(s.root.Fd()), filepath.Base(childPath)); err != nil {
		return fmt.Errorf("remove settled unique cgroup scope: %w", err)
	}
	return s.Close()
}

// RootDigest is the stable identity of the delegated cell root as observed
// through the held descriptor. It equals DigestD09CgroupRoot of the live
// hierarchy path/owner uid/controllers and is what the signed profile's
// CgroupRootDigest is compared against; it carries no inode or mount id.
func (s *LinuxCgroupScope) RootDigest() string {
	if s == nil {
		return ""
	}
	return s.rootDigest
}

// IdentityDigest commits to the held delegated-root identity and the exact
// direct-child cgroup path/device/inode tuple.
func (s *LinuxCgroupScope) IdentityDigest() string {
	if s == nil || s.root == nil || s.file == nil {
		return ""
	}
	path, err := cgroupDescriptorPath(s.file)
	if err != nil {
		return ""
	}
	info, err := s.file.Stat()
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return ""
	}
	return DigestBytes([]byte(fmt.Sprintf("tbound-d09-cgroup-scope/v1\x00%s\x00%s\x00%d\x00%d",
		s.rootDigest, path, stat.Dev, stat.Ino)))
}

// Path returns the identity path of this held, verified cgroup-v2 scope.
// Callers must use this path only to place a child under this descriptor-bound
// scope; it is not a substitute for retaining the scope handle.
func (s *LinuxCgroupScope) Path() (string, error) {
	if s == nil || s.file == nil {
		return "", ErrCgroupScopeRefused
	}
	return cgroupDescriptorPath(s.file)
}

// ContainsPID verifies that pid is currently a member of this scope or one of
// its descendants in the unified cgroup-v2 hierarchy.
func (s *LinuxCgroupScope) ContainsPID(pid int) (bool, error) {
	if pid <= 0 {
		return false, ErrCgroupScopeRefused
	}
	path, err := s.Path()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		relative := filepath.Clean(strings.TrimPrefix(line, "0::"))
		member, err := cgroup2HostPath(relative)
		if err != nil {
			return false, err
		}
		scope := filepath.Clean(path)
		return member == scope || strings.HasPrefix(member, scope+string(os.PathSeparator)), nil
	}
	return false, errors.New("process cgroup omitted unified hierarchy")
}

func cgroup2HostPath(relative string) (string, error) {
	if !filepath.IsAbs(relative) {
		return "", ErrCgroupScopeRefused
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
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
		if root == "/" {
			return filepath.Join(mountpoint, strings.TrimPrefix(relative, string(os.PathSeparator))), nil
		}
		if relative == root || strings.HasPrefix(relative, root+string(os.PathSeparator)) {
			remainder := strings.TrimPrefix(strings.TrimPrefix(relative, root), string(os.PathSeparator))
			return filepath.Join(mountpoint, remainder), nil
		}
	}
	return "", errors.New("unified cgroup membership is not below a cgroup2 mount")
}

func cgroupDescriptorPath(directory *os.File) (string, error) {
	if directory == nil {
		return "", ErrCgroupScopeRefused
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", directory.Fd()))
	if err != nil || !filepath.IsAbs(path) || strings.HasSuffix(path, " (deleted)") {
		return "", ErrCgroupScopeRefused
	}
	return filepath.Clean(path), nil
}

func hasCgroupController(value []byte, name string) bool {
	for _, item := range strings.Fields(string(value)) {
		if strings.TrimLeft(item, "+-") == name {
			return true
		}
	}
	return false
}

func requireCgroup2Path(path string) error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("inspect cgroup2 mount: %w", err)
	}
	return requireCgroup2PathIn(string(data), path)
}

// Settle uses only the supplied unique cgroup directory's cgroup.kill and
// cgroup.events. A successful direct-child Wait alone can never return true.
func (s *LinuxCgroupScope) Settle(ctx context.Context) (bool, error) {
	if s == nil || ctx == nil {
		return false, ErrCgroupScopeRefused
	}
	s.mu.Lock()
	closed, file := s.closed, s.file
	s.mu.Unlock()
	if closed || file == nil {
		return false, ErrCgroupScopeRefused
	}
	if err := writeCgroupControl(file, "cgroup.kill", "1"); err != nil {
		return false, fmt.Errorf("revoke Pi worker cgroup: %w", err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		populated, err := readCgroupPopulated(file)
		if err != nil {
			return false, err
		}
		if !populated {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func validateCgroupDirectory(directory *os.File) error {
	return validateCgroupDirectoryState(directory, true)
}

func validateCgroupDirectoryState(directory *os.File, requireEmpty bool) error {
	stat, err := directory.Stat()
	if err != nil || !stat.IsDir() {
		return errors.New("cgroup handle is not a directory")
	}
	owner, ok := stat.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return errors.New("cgroup scope is not owned by the launching UID")
	}
	if err := requireCgroup2Descriptor(directory); err != nil {
		return err
	}
	typeValue, err := readAt(directory, "cgroup.type", 128)
	if err != nil || strings.TrimSpace(string(typeValue)) != "domain" {
		return errors.New("cgroup scope must be a non-threaded domain")
	}
	controllers, err := readAt(directory, "cgroup.controllers", 1024)
	if err != nil {
		return errors.New("cgroup controller inventory is unavailable")
	}
	controllerSet := make(map[string]bool)
	for _, controller := range strings.Fields(string(controllers)) {
		controllerSet[controller] = true
	}
	if !controllerSet["cpu"] || !controllerSet["memory"] || !controllerSet["pids"] {
		return errors.New("cgroup cpu, memory and pids controllers are not delegated")
	}
	populated, err := readCgroupPopulated(directory)
	if err != nil || requireEmpty && populated {
		return errors.New("new Pi worker cgroup must be observable and empty before launch")
	}
	killFD, err := syscall.Openat(int(directory.Fd()), "cgroup.kill", syscall.O_WRONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("cgroup.kill is not writable")
	}
	_ = syscall.Close(killFD)
	return nil
}

func readAt(directory *os.File, name string, maximum int64) ([]byte, error) {
	fd, err := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maximum+1))
}

func writeCgroupControl(directory *os.File, name, value string) error {
	fd, err := syscall.Openat(int(directory.Fd()), name, syscall.O_WRONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if _, err := file.WriteString(value); err != nil {
		return err
	}
	return nil
}

func readCgroupPopulated(directory *os.File) (bool, error) {
	data, err := readAt(directory, "cgroup.events", 4096)
	if err != nil {
		return false, fmt.Errorf("read cgroup.events: %w", err)
	}
	found := false
	populated := true
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "populated" {
			if fields[1] != "0" && fields[1] != "1" {
				return false, errors.New("cgroup.events populated field is invalid")
			}
			found = true
			populated = fields[1] == "1"
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	if !found {
		return false, errors.New("cgroup.events omitted populated")
	}
	return populated, nil
}

// LinuxExecutableLauncher is the descriptor-bound host-exec primitive. It uses
// clone3's CLONE_INTO_CGROUP but does not create a D09 rootless Podman/crun
// cell, namespaces, or seccomp profile. Therefore it is deliberately not an
// admitted production launcher until a separate frozen cell launcher exists.
type LinuxExecutableLauncher struct{ scope *LinuxCgroupScope }

func (l *LinuxExecutableLauncher) BoundSettlement() DescendantSettler {
	if l == nil {
		return nil
	}
	return l.scope
}

func (l *LinuxExecutableLauncher) admitsFrozenLinuxProfile(profile HostProfile) error {
	if l == nil || l.scope == nil || profile.Mode != ModeProduction {
		return ErrProductionLauncherUnavailable
	}
	// Cgroup-scoped host exec is not equivalent to D09 Podman/crun containment.
	// Keep the concrete primitive usable for reviewed development probes while
	// refusing to turn it into a signed production admission claim.
	return ErrProductionLauncherUnavailable
}

func (l *LinuxExecutableLauncher) StartVerified(ctx context.Context, request ExecutableLaunchRequest) (*exec.Cmd, error) {
	if ctx == nil || l == nil || l.scope == nil || request.ProfileDigest == "" ||
		!validDigest(request.Digest) || !validDigest(request.ArgumentsDigest) ||
		!validDigest(request.EnvironmentDigest) || !validDigest(request.WorkingDirectoryDigest) ||
		!validDigest(request.StdinDigest) || !validDigest(request.DescriptorProfileDigest) ||
		request.DescriptorProfile != PiWorkerDescriptorProfile ||
		request.DescriptorProfileDigest != DigestBytes([]byte(PiWorkerDescriptorProfile)) ||
		request.ContainmentProfile != l.scope.containment || request.SettlementProfile != l.scope.settlement ||
		request.PiRuntimeBundleHandle == nil || request.PiRuntimeBundleDigest == "" ||
		request.admission == nil || !request.admission.valid() || request.admission.profile.Mode != ModeProduction ||
		!sameBinding(request.admission.runtime.Launcher, l) || !sameBinding(request.Stdout, io.Discard) ||
		!sameBinding(request.Stderr, io.Discard) {
		return nil, ErrLinuxLauncherRefused
	}
	profile := request.admission.profile
	if err := l.admitsFrozenLinuxProfile(profile); err != nil {
		// This cgroup-scoped host-exec primitive is not a namespace-backed D09
		// launcher. Do not fall through to a direct-host execution path.
		return nil, err
	}
	if request.ProfileDigest != profile.Digest || request.Path != profile.ChildExecutable || request.Digest != profile.ExecutableDigest ||
		request.NodeVersion != profile.NodeVersion || request.ArgumentsDigest != profile.ChildArgumentsDigest ||
		request.EnvironmentDigest != profile.ChildEnvironmentDigest || request.WorkingDirectoryDigest != profile.WorkingDirectoryDigest ||
		request.Dir != profile.WorkingDirectory || request.StdinDigest != profile.StdinDigest ||
		request.StdinProfile != profile.StdinProfile || request.SourceDigest != profile.SourceProvenanceDigest ||
		request.DescriptorProfile != profile.DescriptorProfile ||
		request.DescriptorProfileDigest != profile.DescriptorProfileDigest || request.PiRuntimeBundleRoot != profile.PiRuntimeBundleRoot ||
		request.PiRuntimeBundleDigest != profile.PiRuntimeBundleDigest || request.ContainmentProfile != profile.ContainmentProfile ||
		request.SettlementProfile != profile.SettlementProfile || l.scope.RootDigest() != profile.CgroupRootDigest ||
		!sameBinding(request.WorkerExposureHandle, request.admission.runtime.WorkerExposureHandle) ||
		!sameBinding(request.PiRuntimeBundleHandle, request.admission.runtime.PiRuntimeBundleHandle) ||
		!sameBinding(request.Stdin, request.admission.runtime.Stdin) ||
		!sameInheritedDescriptors(request.Descriptors, request.admission.runtime.Descriptors) ||
		!sameBinding(l.scope, request.admission.runtime.Descendant) {
		return nil, ErrHostProfileMismatch
	}
	if DigestArguments(request.Args) != request.ArgumentsDigest || DigestEnvironment(request.Env) != request.EnvironmentDigest ||
		DigestBytes([]byte(request.Dir)) != request.WorkingDirectoryDigest || !minimalPiWorkerEnvironment(request.Env) {
		return nil, ErrHostProfileMismatch
	}
	if !equalStrings(request.Args, PiWorkerCellArguments()) || request.Path == "" || request.NodeVersion == "" {
		return nil, ErrHostProfileMismatch
	}
	if err := validatePrivateDirectory(request.Env); err != nil {
		return nil, err
	}
	if err := validateWorkerExposureDescriptor(request.WorkerExposureHandle, request.admission.runtime.WorkerExposureEvidence); err != nil {
		return nil, err
	}
	if err := verifyImmutableLinuxExecutable(request.Path); err != nil {
		return nil, err
	}
	runtimeDigest, err := VerifyLinuxPiRuntimeBundle(request.PiRuntimeBundleHandle)
	if err != nil || runtimeDigest != request.PiRuntimeBundleDigest {
		return nil, fmt.Errorf("%w: runtime bundle digest differs from admitted profile", ErrRuntimeBundleRefused)
	}
	if err := validatePiWorkerDescriptors(request.DescriptorProfile, request.Descriptors, request.WorkerExposureHandle, request.PiRuntimeBundleHandle); err != nil {
		return nil, err
	}
	executable, err := openVerifiedExecutable(request.Path, request.Digest)
	if err != nil {
		return nil, err
	}
	defer executable.Close()
	ordered := cloneInheritedDescriptors(request.Descriptors)
	extraFiles := make([]*os.File, 1, len(ordered)+1)
	extraFiles[0] = executable // child fd 3 is the descriptor-bound Node executable.
	for _, descriptor := range ordered {
		if descriptor.File == nil {
			return nil, ErrHostBindingsMissing
		}
		extraFiles = append(extraFiles, descriptor.File)
	}
	command := exec.CommandContext(ctx, "/proc/self/fd/3", request.Args...)
	// os/exec applies Dir before installing ExtraFiles, so Linux cannot use fd 4
	// for fchdir here. Bind the private path in the signed profile and require
	// the worker to compare its actual cwd identity with inherited fd 4 before
	// emitting ready or accepting a prompt.
	command.Dir = request.Dir
	command.Env = append([]string{}, request.Env...)
	command.Stdin = request.Stdin
	command.Stdout = request.Stdout
	command.Stderr = request.Stderr
	command.ExtraFiles = extraFiles
	command.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(l.scope.file.Fd())}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start descriptor-bound Node in admitted cgroup: %w", err)
	}
	return command, nil
}

func openVerifiedExecutable(path, expectedDigest string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrLinuxLauncherRefused
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open Node executable without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		_ = file.Close()
		return nil, ErrLinuxLauncherRefused
	}
	if info.Size() < 4 {
		_ = file.Close()
		return nil, ErrLinuxLauncherRefused
	}
	var magic [4]byte
	if _, err := file.ReadAt(magic[:], 0); err != nil || !bytes.Equal(magic[:], []byte{0x7f, 'E', 'L', 'F'}) {
		_ = file.Close()
		return nil, errors.New("Node executable is not a native ELF binary")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		_ = file.Close()
		return nil, errors.New("held Node executable digest differs from the admitted profile")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// verifyImmutableLinuxExecutable is production-only. Hashing a held descriptor
// is not by itself an immutability guarantee if another writable handle can
// mutate the inode before execve; production therefore requires a root-owned
// executable on a read-only mount.
func verifyImmutableLinuxExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("production Pi Node executable must be a regular non-writable, non-symlink file")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		return errors.New("production Pi Node executable must be root-owned")
	}
	if err := requireReadOnlyMount(path); err != nil {
		return fmt.Errorf("production Pi Node executable mount is mutable: %w", err)
	}
	return nil
}

func minimalPiWorkerEnvironment(environment []string) bool {
	allowed := map[string]bool{"HOME": true, "TMPDIR": true, "LANG": true, "PI_OFFLINE": true}
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		name, value, ok := strings.Cut(item, "=")
		if !ok || !allowed[name] {
			return false
		}
		if _, duplicate := values[name]; duplicate {
			return false
		}
		values[name] = value
	}
	return len(values) == len(allowed) && values["PI_OFFLINE"] == "1" && values["LANG"] == "C.UTF-8" &&
		filepath.IsAbs(values["HOME"]) && filepath.IsAbs(values["TMPDIR"])
}

func validatePrivateDirectory(environment []string) error {
	for _, item := range environment {
		name, value, _ := strings.Cut(item, "=")
		if name != "HOME" && name != "TMPDIR" {
			continue
		}
		info, err := os.Lstat(value)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("Pi worker %s must be a non-symlink mode-0700 directory", name)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("Pi worker %s must be owned by the launching user", name)
		}
	}
	return nil
}

// VerifyLinuxPiRuntimeBundle hashes a complete immutable Linux runtime bundle
// from its held root descriptor. Symlinks, special files, nested mounts,
// writable mounts, and unbounded trees are refused. This computes an identity;
// the trusted HostProfile loader must independently bind the expected digest.
func VerifyLinuxPiRuntimeBundle(root *os.File) (string, error) {
	if root == nil {
		return "", ErrRuntimeBundleRefused
	}
	info, err := root.Stat()
	if err != nil || !info.IsDir() {
		return "", ErrRuntimeBundleRefused
	}
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd()))
	if err != nil || !filepath.IsAbs(rootPath) || strings.HasSuffix(rootPath, " (deleted)") {
		return "", ErrRuntimeBundleRefused
	}
	pathInfo, err := os.Lstat(rootPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) {
		return "", ErrRuntimeBundleRefused
	}
	if err := requireReadOnlyMount(rootPath); err != nil {
		return "", err
	}
	device, ok := statDevice(info)
	if !ok {
		return "", ErrRuntimeBundleRefused
	}
	hash := sha256.New()
	count := 0
	total := int64(0)
	err = filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > PiRuntimeBundleMaxFiles {
			return ErrRuntimeBundleRefused
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrRuntimeBundleRefused
		}
		if _, ok := statDevice(info); !ok {
			return ErrRuntimeBundleRefused
		} else if current, _ := statDevice(info); current != device {
			return errors.New("Pi runtime bundle contains a nested filesystem mount")
		}
		relative, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			_, _ = fmt.Fprintf(hash, "d\x00%s\x00%o\n", filepath.ToSlash(relative), info.Mode().Perm())
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 512<<20 || total > PiRuntimeBundleMaxBytes-info.Size() {
			return ErrRuntimeBundleRefused
		}
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		before, err := file.Stat()
		if err != nil || !os.SameFile(info, before) {
			_ = file.Close()
			return ErrRuntimeBundleRefused
		}
		contentHash := sha256.New()
		n, copyErr := io.Copy(contentHash, io.LimitReader(file, info.Size()+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n != info.Size() {
			return errors.Join(copyErr, closeErr, ErrRuntimeBundleRefused)
		}
		total += n
		_, _ = fmt.Fprintf(hash, "f\x00%s\x00%o\x00%d\x00%s\n", filepath.ToSlash(relative), info.Mode().Perm(), info.Size(), hex.EncodeToString(contentHash.Sum(nil)))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRuntimeBundleRefused, err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// DigestDeveloperPiCLIBundle fingerprints the source-only extension snapshot.
func DigestDeveloperPiCLIBundle(root *os.File) (string, error) {
	if root == nil {
		return "", errors.New("Pi CLI extension bundle handle is required")
	}
	info, err := root.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return "", ErrRuntimeBundleRefused
	}
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd()))
	if err != nil || !filepath.IsAbs(rootPath) || strings.HasSuffix(rootPath, " (deleted)") {
		return "", ErrRuntimeBundleRefused
	}
	pathInfo, err := os.Lstat(rootPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) {
		return "", ErrRuntimeBundleRefused
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return "", errors.New("Pi CLI extension bundle is not owned by the current UID")
	}
	hash := sha256.New()
	for _, relative := range DeveloperPiCLIAdapterSourceFiles {
		data, err := readRegularBounded(filepath.Join(rootPath, filepath.FromSlash(relative)), 16<<20)
		if err != nil {
			return "", fmt.Errorf("read Pi CLI extension source %s: %w", relative, err)
		}
		if err := writeDigestField(hash, relative, data); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func readRegularBounded(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maximum {
		return nil, errors.New("file is not a bounded regular non-symlink")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errors.Join(err, errors.New("file changed or exceeded byte bound"))
	}
	return data, nil
}

func writeDigestField(writer hash.Hash, name string, data []byte) error {
	if len(name) == 0 || len(name) > 4096 || int64(len(data)) > PiRuntimeBundleMaxBytes {
		return errors.New("developer digest field exceeds bounds")
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(name)))
	if _, err := writer.Write(size[:]); err != nil {
		return err
	}
	if _, err := writer.Write([]byte(name)); err != nil {
		return err
	}
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	if _, err := writer.Write(size[:]); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}

func statDevice(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Dev), true
}

func requireReadOnlyMount(root string) error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("read Linux mount table: %w", err)
	}
	root = filepath.Clean(root)
	bestLength := -1
	readOnly := false
	for _, line := range strings.Split(string(data), "\n") {
		leftRight := strings.SplitN(line, " - ", 2)
		if len(leftRight) != 2 {
			continue
		}
		fields := strings.Fields(leftRight[0])
		if len(fields) < 6 {
			continue
		}
		mountPoint := filepath.Clean(unescapeMountField(fields[4]))
		if mountPoint != root && strings.HasPrefix(mountPoint, root+string(os.PathSeparator)) {
			return fmt.Errorf("%w: Pi runtime bundle contains a nested mountpoint", ErrRuntimeBundleRefused)
		}
		if root != mountPoint && !strings.HasPrefix(root, mountPoint+string(os.PathSeparator)) {
			continue
		}
		if len(mountPoint) > bestLength {
			bestLength = len(mountPoint)
			readOnly = strings.Contains(","+fields[5]+",", ",ro,")
		}
	}
	if bestLength < 0 || !readOnly {
		return fmt.Errorf("%w: Pi runtime bundle is not on a read-only mount", ErrRuntimeBundleRefused)
	}
	return nil
}

func unescapeMountField(value string) string {
	return strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\").Replace(value)
}

func verifyExposureDescriptorIdentity(file *os.File, device, inode, mountID uint64) error {
	if file == nil || device == 0 || inode == 0 || mountID == 0 {
		return errors.New("sealed view identity is incomplete")
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return errors.New("sealed view descriptor is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != device || stat.Ino != inode {
		return errors.New("sealed view descriptor device/inode differs from registered evidence")
	}
	observedMountID, err := descriptorMountID(file.Fd())
	if err != nil || observedMountID != mountID {
		return errors.New("sealed view descriptor mount ID differs from registered evidence")
	}
	return nil
}

func descriptorMountID(fd uintptr) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "mnt_id:" {
			id, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || id == 0 {
				return 0, errors.New("descriptor mount ID is invalid")
			}
			return id, nil
		}
	}
	return 0, errors.New("descriptor fdinfo omitted mount ID")
}

func validateWorkerExposureDescriptor(file *os.File, evidence WorkerExposureEvidence) error {
	if err := evidence.validateShape(); err != nil {
		return err
	}
	return verifyExposureDescriptorIdentity(file, evidence.Device, evidence.Inode, evidence.MountID)
}

func validateRuntimeAssetDescriptor(file *os.File, evidence WorkerRuntimeAssetEvidence) error {
	if err := evidence.validateShape(); err != nil {
		return err
	}
	return verifyExposureDescriptorIdentity(file, evidence.Device, evidence.Inode, evidence.MountID)
}
