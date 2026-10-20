//go:build linux

package piruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
)

// cgroupRemoveDir is the rmdir of one cgroup directory below a held parent; a seam for fake trees.
var cgroupRemoveDir = unlinkatDir

// cgroupRemoveBusyWait bounds how long removing a settled scope retries EBUSY.
var cgroupRemoveBusyWait = 5 * time.Second

const cgroupRemoveMaxDepth = 8

// removeSettledCgroup removes the settled cgroup `name` below the held parent descriptor.
//
// Podman's `--rm` cleanup (conmon, then `podman container cleanup`) removes the container's own
// libpod-<id> child cgroup asynchronously after the cell exited, and may never run if conmon died,
// so a settled scope can still hold empty descendants when the host removes it (EBUSY). Before the
// scope itself is removed, every descendant cgroup is removed bottom-up through descriptors, but
// only if it holds no process and reports populated 0; anything populated is refused, nothing is
// followed through a symlink, and only directories are touched. The final rmdir tolerates a
// bounded EBUSY while the kernel finishes releasing a just-emptied cgroup.
func removeSettledCgroup(ctx context.Context, parentFD int, name string) error {
	if ctx == nil || name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return ErrCgroupScopeRefused
	}
	fd, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	scope := os.NewFile(uintptr(fd), name)
	err = removeEmptyDescendants(scope, 0)
	_ = scope.Close()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(cgroupRemoveBusyWait)
	for {
		err := cgroupRemoveDir(parentFD, name)
		if err == nil || !errors.Is(err, syscall.EBUSY) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func removeEmptyDescendants(dir *os.File, depth int) error {
	if depth >= cgroupRemoveMaxDepth {
		return fmt.Errorf("%w: cgroup tree is deeper than %d levels", ErrCgroupScopeRefused, cgroupRemoveMaxDepth)
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue // control files, and never a link
		}
		name := entry.Name()
		fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if errors.Is(err, syscall.ENOENT) {
			continue // removed concurrently by podman's own cleanup
		}
		if err != nil {
			return err
		}
		child := os.NewFile(uintptr(fd), name)
		err = requireEmptyCgroup(child)
		if err == nil {
			err = removeEmptyDescendants(child, depth+1)
		}
		_ = child.Close()
		if err != nil {
			return fmt.Errorf("descendant cgroup %s: %w", name, err)
		}
		if err := cgroupRemoveDir(int(dir.Fd()), name); err != nil && !errors.Is(err, syscall.ENOENT) {
			return fmt.Errorf("remove empty descendant cgroup %s: %w", name, err)
		}
	}
	return nil
}

// requireEmptyCgroup refuses a cgroup that holds a process or reports populated.
func requireEmptyCgroup(dir *os.File) error {
	procs, err := readAt(dir, "cgroup.procs", 1<<16)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(procs)) != "" {
		return fmt.Errorf("%w: cgroup still holds processes", ErrCgroupScopeRefused)
	}
	events, err := readAt(dir, "cgroup.events", 4096)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(events), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "populated" {
			if fields[1] == "0" {
				return nil
			}
			return fmt.Errorf("%w: cgroup is populated", ErrCgroupScopeRefused)
		}
	}
	return fmt.Errorf("%w: cgroup reports no populated state", ErrCgroupScopeRefused)
}
