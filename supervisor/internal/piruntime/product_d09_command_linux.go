//go:build linux

package piruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// D09ProductCommandOwnership identifies a product command before launch and, when started, its authenticated namespace.
type D09ProductCommandOwnership struct {
	ContainerName string `json:"container_name"`
	LeaseID       string `json:"lease_id"`
	ViewID        string `json:"view_id"`
	ViewDevice    uint64 `json:"view_device"`
	ViewInode     uint64 `json:"view_inode"`
	MountNSInode  uint64 `json:"mount_ns_inode,omitempty"`
	cgroup        *os.File
}

// D09ProductCommandLimits holds supervisor observations of the live container's cgroup limits.
type D09ProductCommandLimits struct {
	PidsMax   string `json:"pids.max"`
	MemoryMax string `json:"memory.max"`
}

// ReadLimits reads only the held live cgroup during the product started callback.
func (o D09ProductCommandOwnership) ReadLimits() (D09ProductCommandLimits, error) {
	if o.cgroup == nil {
		return D09ProductCommandLimits{}, ErrCgroupScopeRefused
	}
	pids, err := readAt(o.cgroup, "pids.max", 128)
	if err != nil {
		return D09ProductCommandLimits{}, err
	}
	memory, err := readAt(o.cgroup, "memory.max", 128)
	return D09ProductCommandLimits{strings.TrimSpace(string(pids)), strings.TrimSpace(string(memory))}, err
}

func (l *D09PodmanCrunLauncher) reportProductCommand(o D09ProductCommandOwnership, scope *LinuxCgroupScope, pid int) error {
	if l.productHost == nil || l.productCommandOwner == nil {
		return nil
	}
	if !l.productHost.verified() {
		return ErrProductD09HostRefused
	}
	if pid != 0 {
		file, err := openProductCommandCgroup(scope, pid)
		if err != nil {
			return err
		}
		defer file.Close()
		o.cgroup = file
	}
	return l.productCommandOwner(o)
}

func openProductCommandCgroup(scope *LinuxCgroupScope, pid int) (*os.File, error) {
	parent, err := scope.Path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return nil, err
	}
	member := strings.TrimSpace(strings.TrimPrefix(string(raw), "0::"))
	path, err := cgroup2HostPath(member)
	if err != nil || !strings.HasPrefix(path, parent+"/") {
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	fd, err := syscall.Dup(int(scope.file.Fd()))
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "product-command-cgroup-parent")
	for _, name := range strings.Split(strings.TrimPrefix(path, parent+"/"), "/") {
		child, err := syscall.Openat(int(file.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		file = os.NewFile(uintptr(child), "product-command-live-cgroup")
	}
	again, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil || string(raw) != string(again) {
		_ = file.Close()
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	if err := requireCgroup2Descriptor(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	procs, err := readAt(file, "cgroup.procs", 1<<20)
	if err != nil || !strings.Contains("\n"+string(procs), "\n"+strconv.Itoa(pid)+"\n") {
		_ = file.Close()
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	return file, nil
}

// ReconcileProductD09CommandResidue proves container removal and namespace release using the revalidated product environment.
func ReconcileProductD09CommandResidue(ctx context.Context, host *D09ProductCommandHost, owner D09ProductCommandOwnership) error {
	if err := host.Revalidate(ctx); err != nil {
		return err
	}
	config, err := productD09LauncherConfig(host)
	if err != nil {
		return err
	}
	return reconcileProductD09CommandResidue(ctx, config.PodmanPath, productD09CommandEnvironment(), owner)
}

func reconcileProductD09CommandResidue(ctx context.Context, podman string, environment []string, owner D09ProductCommandOwnership) error {
	if ctx == nil || !strings.HasPrefix(owner.ContainerName, d09CellNamePrefix+"cmd-") || !validIdentity(owner.ContainerName) ||
		!validIdentity(owner.LeaseID) || !validIdentity(owner.ViewID) || owner.ViewInode == 0 {
		return ErrUnknownSettlement
	}
	exists, err := containerExists(ctx, podman, owner.ContainerName, environment)
	if err != nil {
		return errors.Join(ErrUnknownSettlement, err)
	}
	if exists {
		state, err := inspectContainer(ctx, podman, owner.ContainerName, environment)
		if err != nil || strings.TrimPrefix(state.Name, "/") != owner.ContainerName {
			return errors.Join(ErrUnknownSettlement, err)
		}
		if owner.MountNSInode == 0 && state.State.PID > 0 {
			owner.MountNSInode, err = processMountNamespaceInode(state.State.PID)
			if err != nil {
				return errors.Join(ErrUnknownSettlement, err)
			}
		}
		if err := removeContainer(ctx, podman, owner.ContainerName, environment); err != nil {
			return errors.Join(ErrUnknownSettlement, err)
		}
	}
	if exists, err := containerExists(ctx, podman, owner.ContainerName, environment); err != nil || exists {
		return errors.Join(ErrUnknownSettlement, err)
	}
	// A missing namespace record cannot prove detach, even when Podman and systemd have forgotten the container.
	if owner.MountNSInode == 0 {
		return ErrUnknownSettlement
	}
	if held, err := processMountNamespaceReferences(ctx, owner.MountNSInode); err != nil || held {
		return errors.Join(ErrUnknownSettlement, err)
	}
	return nil
}
