//go:build linux

package piruntime

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// CellCgroupFacts is what ObserveCellCgroup reads from one held cell cgroup.
type CellCgroupFacts struct {
	// HierarchyPath is the cgroup2 hierarchy path of the held descriptor.
	HierarchyPath string
	// Populated is the cgroup.events populated value (recursive).
	Populated bool
	// PIDs are the members of the cell and of every descendant cgroup, sorted
	// and unique, as seen from the reader's PID namespace.
	PIDs []int
	// Removed is true when the held descriptor is a cgroup2 directory that has
	// been removed. Nothing else is read then: Populated is false and PIDs is nil.
	Removed bool
}

const (
	observeCellMaxDepth = 16
	observeCellMaxDirs  = 4096
)

// ObserveCellCgroup reads a held cell cgroup descriptor fd-relative: it verifies
// the descriptor is cgroup2 (observeCgroup2Descriptor), reads cgroup.events
// (readCgroupPopulated) and cgroup.procs of the cell and its descendants. A
// process id of 0 (a member outside the reader's PID namespace) is an error.
func ObserveCellCgroup(dir *os.File) (CellCgroupFacts, error) {
	if dir == nil {
		return CellCgroupFacts{}, ErrCgroupScopeRefused
	}
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return CellCgroupFacts{}, fmt.Errorf("inspect cgroup2 mount: %w", err)
	}
	hierarchy, err := observeCgroup2Descriptor(dir, string(mountinfo))
	if err != nil {
		if removedCgroup2Descriptor(dir) {
			return CellCgroupFacts{Removed: true}, nil
		}
		return CellCgroupFacts{}, err
	}
	populated, err := readCgroupPopulated(dir)
	if err != nil {
		return CellCgroupFacts{}, err
	}
	seen := map[int]struct{}{}
	dirs := 0
	if err := collectCgroupPIDs(dir, 0, &dirs, seen); err != nil {
		return CellCgroupFacts{}, err
	}
	pids := make([]int, 0, len(seen))
	for pid := range seen {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	return CellCgroupFacts{HierarchyPath: hierarchy, Populated: populated, PIDs: pids}, nil
}

func removedCgroup2Descriptor(f *os.File) bool {
	var filesystem syscall.Statfs_t
	if syscall.Fstatfs(int(f.Fd()), &filesystem) != nil || int64(filesystem.Type) != cgroup2SuperMagic {
		return false
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
	return err == nil && strings.HasSuffix(path, " (deleted)")
}

func collectCgroupPIDs(dir *os.File, depth int, dirs *int, seen map[int]struct{}) error {
	if depth > observeCellMaxDepth || *dirs >= observeCellMaxDirs {
		return errors.New("cell cgroup tree exceeds the observation bounds")
	}
	*dirs++
	procs, err := readAt(dir, "cgroup.procs", 1<<22)
	if err != nil {
		return fmt.Errorf("read cgroup.procs: %w", err)
	}
	for _, field := range strings.Fields(string(procs)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			return errors.New("cgroup.procs holds a process outside this PID namespace or a malformed id")
		}
		seen[pid] = struct{}{}
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("list cell cgroup: %w", err)
	}
	// Seeking back lets the same descriptor be listed again by a later call.
	if _, err := dir.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind cell cgroup: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		fd, err := syscall.Openat(int(dir.Fd()), entry.Name(),
			syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("open descendant cgroup: %w", err)
		}
		child := os.NewFile(uintptr(fd), entry.Name())
		err = collectCgroupPIDs(child, depth+1, dirs, seen)
		child.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
