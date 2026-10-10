//go:build linux

package piruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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
	if err != nil || !hasCgroupController(rootControllers, "memory") || !hasCgroupController(rootControllers, "pids") {
		return nil, errors.New("delegated cgroup root lacks memory/pids controllers")
	}
	rootSubtree, err := readAt(delegatedRoot, "cgroup.subtree_control", 1024)
	if err != nil || !hasCgroupController(rootSubtree, "memory") || !hasCgroupController(rootSubtree, "pids") {
		return nil, errors.New("delegated cgroup root has not enabled memory/pids controllers")
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return nil, ErrCgroupScopeRefused
	}
	if err := validateCgroupDirectory(directory); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCgroupScopeRefused, err)
	}
	directoryStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || directoryStat.Uid != uint32(os.Geteuid()) || directoryStat.Dev != rootStat.Dev {
		return nil, errors.New("Pi worker cgroup identity differs from delegated root")
	}
	rootIdentity := fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s", rootPath, rootStat.Dev, rootStat.Ino, strings.TrimSpace(string(rootControllers)), strings.TrimSpace(string(rootSubtree)))
	rootDigest := DigestBytes([]byte(rootIdentity))
	if err := requireCgroup2Path(directoryPath); err != nil {
		return nil, err
	}
	return &LinuxCgroupScope{root: delegatedRoot, file: directory, rootDigest: rootDigest, containment: containmentProfile, settlement: settlementProfile}, nil
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

func (s *LinuxCgroupScope) RootDigest() string {
	if s == nil {
		return ""
	}
	return s.rootDigest
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
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "cgroup2 ") {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 5 {
			continue
		}
		mount := filepath.Clean(unescapeMountField(fields[4]))
		if path != mount && strings.HasPrefix(path, mount+string(os.PathSeparator)) {
			return nil
		}
	}
	return errors.New("Pi worker cgroup is not below a mounted cgroup-v2 filesystem")
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
	stat, err := directory.Stat()
	if err != nil || !stat.IsDir() {
		return errors.New("cgroup handle is not a directory")
	}
	path, err := cgroupDescriptorPath(directory)
	if err != nil {
		return err
	}
	owner, ok := stat.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return errors.New("cgroup scope is not owned by the launching UID")
	}
	if err := requireCgroup2Path(path); err != nil {
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
	if !controllerSet["memory"] || !controllerSet["pids"] {
		return errors.New("cgroup memory and pids controllers are not delegated")
	}
	populated, err := readCgroupPopulated(directory)
	if err != nil || populated {
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

func NewLinuxExecutableLauncher(scope *LinuxCgroupScope) (*LinuxExecutableLauncher, error) {
	if scope == nil || scope.file == nil {
		return nil, ErrCgroupScopeRefused
	}
	return &LinuxExecutableLauncher{scope: scope}, nil
}

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
	if len(request.Args) != 2 || request.Args[0] != "--experimental-strip-types" ||
		request.Args[1] != "/proc/self/fd/9/src/governed-pi-worker.ts" ||
		request.Path == "" || request.NodeVersion == "" {
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

func digestLinuxExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrLinuxLauncherRefused
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", ErrLinuxLauncherRefused
	}
	var magic [4]byte
	if _, err := file.ReadAt(magic[:], 0); err != nil || !bytes.Equal(magic[:], []byte{0x7f, 'E', 'L', 'F'}) {
		return "", errors.New("Linux Node executable is not ELF")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
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

func validatePrivateWorkingDirectory(path string, handle *os.File) error {
	if handle == nil || !filepath.IsAbs(path) {
		return ErrHostBindingsMissing
	}
	opened, err := handle.Stat()
	if err != nil || !opened.IsDir() {
		return ErrHostBindingsMissing
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, pathInfo) {
		return ErrHostBindingsMissing
	}
	if pathInfo.Mode().Perm() != 0o700 {
		return errors.New("Pi worker CWD must be a private mode-0700 directory")
	}
	stat, ok := pathInfo.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("Pi worker CWD must be owned by the launching user")
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

// DeveloperPiSDKAdapterSourceFiles is the fixed, bounded adapter source set the
// governed Pi SDK worker imports at runtime. It is the single source of truth
// shared by the development bundle staging step and the developer bundle digest
// so the two cannot drift.
var DeveloperPiSDKAdapterSourceFiles = []string{
	"package.json", "package-lock.json",
	"src/governed-pi-worker.ts", "src/broker-provider.ts", "src/proxy-tools.ts",
	"src/ipc-transport.ts", "src/locked-resource-loader.ts",
}

// DigestDeveloperPiSDKBundle binds the small adapter source snapshot and lock
// metadata used by the offline Linux worker test, plus the explicit existing
// dependency-link target and pinned package manifests. It is development-only:
// it does not hash every dependency byte or produce a production receipt.
func DigestDeveloperPiSDKBundle(root *os.File, pinnedNodeModules string) (string, error) {
	if root == nil || !filepath.IsAbs(pinnedNodeModules) {
		return "", errors.New("developer bundle handle and explicit pinned dependency root are required")
	}
	rootInfo, err := root.Stat()
	if err != nil || !rootInfo.IsDir() {
		return "", ErrRuntimeBundleRefused
	}
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd()))
	if err != nil || !filepath.IsAbs(rootPath) || strings.HasSuffix(rootPath, " (deleted)") {
		return "", ErrRuntimeBundleRefused
	}
	pathInfo, err := os.Lstat(rootPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(rootInfo, pathInfo) {
		return "", ErrRuntimeBundleRefused
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || rootStat.Uid != uint32(os.Geteuid()) || rootInfo.Mode().Perm() != 0o700 {
		return "", errors.New("developer source bundle root must be owned mode 0700")
	}
	linkPath := filepath.Join(rootPath, "node_modules")
	linkInfo, err := os.Lstat(linkPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("developer runtime requires an explicit symlink to existing pinned dependencies")
	}
	linkTarget, err := filepath.EvalSymlinks(linkPath)
	if err != nil || filepath.Clean(linkTarget) != filepath.Clean(pinnedNodeModules) {
		return "", errors.New("developer dependency symlink target differs from the explicit pinned path")
	}
	if info, err := os.Stat(linkTarget); err != nil || !info.IsDir() {
		return "", errors.New("developer pinned dependencies are unavailable")
	}
	hash := sha256.New()
	if err := writeDigestField(hash, "node_modules-link", []byte(filepath.Clean(linkTarget))); err != nil {
		return "", err
	}
	for _, relative := range DeveloperPiSDKAdapterSourceFiles {
		data, err := readRegularBounded(filepath.Join(rootPath, filepath.FromSlash(relative)), 16<<20)
		if err != nil {
			return "", fmt.Errorf("read developer Pi runtime file %s: %w", relative, err)
		}
		if err := writeDigestField(hash, relative, data); err != nil {
			return "", err
		}
	}
	for _, dependency := range []struct{ name, version string }{
		{"@earendil-works/pi-coding-agent", "0.87.1"},
		{"@earendil-works/pi-ai", "0.87.1"},
		{"@earendil-works/pi-tui", "0.87.1"},
		{"typebox", "1.3.27"},
	} {
		data, err := readRegularBounded(filepath.Join(linkTarget, filepath.FromSlash(dependency.name), "package.json"), 1<<20)
		if err != nil {
			return "", fmt.Errorf("read dependency manifest %s: %w", dependency.name, err)
		}
		var manifest struct{ Name, Version string }
		if err := json.Unmarshal(data, &manifest); err != nil || manifest.Name != dependency.name || manifest.Version != dependency.version {
			return "", fmt.Errorf("dependency manifest identity/version mismatch: %s", dependency.name)
		}
		if err := writeDigestField(hash, "node_modules/"+dependency.name+"/package.json", data); err != nil {
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
