//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
)

const (
	productD09ServiceHostPath     = "/sys/fs/cgroup/system.slice/tbound-product.service"
	productD09CellsHostPath       = productD09ServiceHostPath + "/cells"
	productD09CellsHierarchyPath  = "/system.slice/tbound-product.service/cells"
	productD09RunnerHierarchyPath = "/system.slice/tbound-product.service/runner"
)

// PrepareProductD09CgroupRoot initializes only the product service's empty cells
// sibling. The caller owns the returned descriptor; no caller path is accepted.
func PrepareProductD09CgroupRoot(ctx context.Context, host *D09ProductCommandHost) (*os.File, error) {
	if err := host.Revalidate(ctx); err != nil {
		return nil, err
	}
	if _, err := productD09LauncherConfig(host); err != nil {
		return nil, err
	}
	current, err := currentUnifiedCgroupPath()
	if err != nil {
		return nil, err
	}
	parent, err := openAbsoluteDirectoryNoSymlinks(productD09ServiceHostPath)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	runner, err := openManagedRunnerLeafBelow(parent, host.snapshot.serviceUID)
	if err != nil {
		return nil, err
	}
	defer runner.Close()
	parentPath, err := cgroupDescriptorPath(parent)
	if err != nil {
		return nil, err
	}
	runnerPath, err := cgroupDescriptorPath(runner)
	if err != nil {
		return nil, err
	}
	if err := validateProductD09CgroupPaths(current, parentPath, runnerPath); err != nil {
		return nil, err
	}
	for _, directory := range []*os.File{parent, runner} {
		if err := requireCgroup2Descriptor(directory); err != nil {
			return nil, err
		}
		info, err := directory.Stat()
		if err != nil {
			return nil, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != host.snapshot.serviceUID || stat.Mode&0o022 != 0 {
			return nil, ErrCgroupScopeRefused
		}
		kind, err := readAt(directory, "cgroup.type", 128)
		if err != nil || string(kind) != "domain\n" {
			return nil, errors.Join(ErrCgroupScopeRefused, err)
		}
	}
	if err := ensureCellsCgroup(parent, host.snapshot.serviceUID); err != nil {
		return nil, err
	}
	fd, err := syscall.Openat(int(parent.Fd()), "cells", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	cells := os.NewFile(uintptr(fd), "product-d09-cells")
	if err := validateProductD09CellsRoot(cells, host); err != nil {
		_ = cells.Close()
		return nil, err
	}
	return cells, nil
}

func validateProductD09CgroupPaths(current, parent, runner string) error {
	if current != productD09RunnerHierarchyPath || parent != productD09ServiceHostPath || runner != productD09ServiceHostPath+"/runner" {
		return ErrCgroupScopeRefused
	}
	return nil
}

func validateProductD09CellsRoot(root *os.File, host *D09ProductCommandHost) error {
	if root == nil || !host.verified() {
		return ErrCgroupScopeRefused
	}
	path, err := cgroupDescriptorPath(root)
	if err != nil || path != productD09CellsHostPath {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	if err := requireCgroup2Descriptor(root); err != nil {
		return err
	}
	if err := validateCgroupDirectoryState(root, false); err != nil {
		return err
	}
	info, err := root.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != host.snapshot.serviceUID || stat.Mode&0o7777 != d09CellsCgroupMode {
		return ErrCgroupScopeRefused
	}
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	hierarchy, err := observeCgroup2Descriptor(root, string(mountinfo))
	if err != nil || hierarchy != productD09CellsHierarchyPath {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	controllers, err := readAt(root, "cgroup.controllers", 1024)
	if err != nil {
		return err
	}
	subtree, err := readAt(root, "cgroup.subtree_control", 1024)
	if err != nil {
		return err
	}
	digest, err := (cgroupRootFacts{hierarchyPath: hierarchy, cgroup2: true, ownerUID: stat.Uid, controllers: controllers, subtree: subtree}).stableDigest()
	if err != nil || digest != host.Profile().CgroupRootDigest {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	return requireNoCgroupProcesses(root)
}

func productD09LauncherConfig(host *D09ProductCommandHost) (D09Config, error) {
	if !host.verified() {
		return D09Config{}, ErrProductD09HostRefused
	}
	template, profile := host.Template(), host.Profile()
	digest, err := template.Digest()
	rootDigest, rootErr := DigestD09CgroupRoot(productD09CellsHierarchyPath, host.snapshot.serviceUID)
	if err != nil || rootErr != nil || digest != profile.ContainmentProfileDigest || profile.CgroupRootDigest != rootDigest ||
		profile.Mode != ModeProduction || profile.ContainmentProfile != D09ContainmentProfile || profile.SettlementProfile != D09SettlementProfile ||
		template.CgroupsPath != productD09CellsHostPath || template.ManagedLiveRootRegistrationDigest != "" || !validDigest(template.ProductRegistrationDigest) {
		return D09Config{}, ErrProductD09HostRefused
	}
	config := template.Config().normalized()
	config.UserNamespaceHostUID, config.UserNamespaceHostGID = host.snapshot.serviceUID, host.snapshot.serviceGID
	return config, nil
}

// NewProductD09PodmanCrunLauncher admits the revalidated signed product host for
// command mode only. Failure leaves scope ownership with the caller.
func NewProductD09PodmanCrunLauncher(ctx context.Context, host *D09ProductCommandHost, scope *LinuxCgroupScope) (*D09PodmanCrunLauncher, error) {
	if err := host.Revalidate(ctx); err != nil {
		return nil, err
	}
	config, err := productD09LauncherConfig(host)
	if err != nil {
		return nil, err
	}
	launcher := &D09PodmanCrunLauncher{config: config, runner: execD09CommandRunner{environment: productD09CommandEnvironment()}}
	if err := validateProductD09Scope(scope, host); err != nil {
		return nil, err
	}
	if err := launcher.BindCgroupScope(scope); err != nil {
		return nil, err
	}
	launcher.config.CgroupsPath, err = scope.Path()
	if err != nil {
		return nil, err
	}
	launcher.productHost = host
	if _, err := launcher.Probe(ctx); err != nil {
		return nil, err
	}
	launcher.admittedProfile, launcher.profileAdmitted = host.Profile(), true
	return launcher, nil
}

func validateProductD09Scope(scope *LinuxCgroupScope, host *D09ProductCommandHost) error {
	if scope == nil || scope.file == nil || scope.root == nil || !host.verified() || scope.RootDigest() != host.Profile().CgroupRootDigest ||
		scope.containment != D09ContainmentProfile || scope.settlement != D09SettlementProfile {
		return ErrCgroupScopeRefused
	}
	scope.mu.Lock()
	closed := scope.closed
	scope.mu.Unlock()
	if closed {
		return ErrCgroupScopeRefused
	}
	if err := validateProductD09CellsRoot(scope.root, host); err != nil {
		return err
	}
	if err := validateCgroupDirectory(scope.file); err != nil {
		return err
	}
	path, err := scope.Path()
	if err != nil || filepath.Dir(path) != productD09CellsHostPath {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	return nil
}

func (l *D09PodmanCrunLauncher) productD09ProbeConfig(ctx context.Context) (D09Config, error) {
	if err := l.productHost.Revalidate(ctx); err != nil {
		return D09Config{}, err
	}
	config, err := productD09LauncherConfig(l.productHost)
	if err != nil {
		return D09Config{}, err
	}
	if err := validateProductD09Scope(l.scope, l.productHost); err != nil {
		return D09Config{}, err
	}
	config.CgroupsPath, err = l.scope.Path()
	if err != nil {
		return D09Config{}, err
	}
	if !reflect.DeepEqual(l.config, config) {
		return D09Config{}, ErrProductD09HostRefused
	}
	runner, ok := l.runner.(execD09CommandRunner)
	if !ok || !reflect.DeepEqual(runner.commandEnvironment(), l.commandEnvironment()) {
		return D09Config{}, ErrProductD09HostRefused
	}
	return config, nil
}
