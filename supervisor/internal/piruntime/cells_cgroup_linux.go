//go:build linux

package piruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// d09CellsCgroupMode is the mode cgroupfs gives a new cgroup directory; an
// existing `cells` must carry exactly this mode.
const d09CellsCgroupMode = 0o755

// cellsCgroupMkdirat is a seam for the fake-cgroup tests only.
var cellsCgroupMkdirat = syscall.Mkdirat

// EnsureCellsCgroup creates (or re-admits) the closed `cells` cgroup below the
// verified, process-free delegated service parent. The service cgroup exists
// only while the unit runs, so this runner-side step is the only place `cells`
// can be created. Any mismatch is an error; there is no fallback. It must run
// before the first cell launch (the launcher only creates pi-<token> children).
func (v *VerifiedD09ManagedLiveRoot) EnsureCellsCgroup(ctx context.Context) error {
	if v == nil || ctx == nil {
		return ErrD09ManagedLiveRootRefused
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || v.serviceCgroup.inode == 0 {
		return ErrD09ManagedLiveRootRefused
	}
	if err := verifyD09TemplateCgroupPath(ctx, v.registration, v.serviceCgroup); err != nil {
		return err
	}
	if err := requireCgroup2Path(v.serviceCgroup.hostPath); err != nil {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	parent, err := openAbsoluteDirectoryNoSymlinks(v.serviceCgroup.hostPath)
	if err != nil {
		return errors.Join(ErrD09ManagedLiveRootRefused, fmt.Errorf("open delegated service cgroup: %w", err))
	}
	defer parent.Close()
	info, err := parent.Stat()
	stat, ok := (*syscall.Stat_t)(nil), false
	if info != nil {
		stat, ok = info.Sys().(*syscall.Stat_t)
	}
	if err != nil || !ok || uint64(stat.Dev) != v.serviceCgroup.device || stat.Ino != v.serviceCgroup.inode {
		return errors.Join(ErrD09ManagedLiveRootRefused,
			errors.New("delegated service cgroup identity changed before cells creation"), err)
	}
	if err := requireCgroup2Descriptor(parent); err != nil {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return ensureCellsCgroup(parent, v.registration.ServiceUID)
}

// ensureCellsCgroup does the descriptor-relative work on an already-opened
// service cgroup. Nothing here follows a symlink or uses a path.
func ensureCellsCgroup(parent *os.File, serviceUID int) error {
	if parent == nil || serviceUID <= 0 {
		return ErrCgroupScopeRefused
	}
	info, err := parent.Stat()
	if err != nil || !info.IsDir() {
		return errors.Join(ErrCgroupScopeRefused, errors.New("service cgroup handle is not a directory"), err)
	}
	parentStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(parentStat.Uid) != serviceUID {
		return errors.Join(ErrCgroupScopeRefused, errors.New("service cgroup is not owned by the service uid"))
	}
	if err := requireNoCgroupProcesses(parent); err != nil {
		return fmt.Errorf("service cgroup parent must hold no processes: %w", err)
	}
	if err := enableD09SubtreeControllers(parent); err != nil {
		return fmt.Errorf("enable cpu/memory/pids controllers on the service cgroup: %w", err)
	}
	mkdirErr := cellsCgroupMkdirat(int(parent.Fd()), D09ManagedPiCellCgroupLeaf, d09CellsCgroupMode)
	if mkdirErr != nil && !errors.Is(mkdirErr, syscall.EEXIST) {
		return fmt.Errorf("create %s cgroup: %w", D09ManagedPiCellCgroupLeaf, mkdirErr)
	}
	created := mkdirErr == nil
	fd, err := syscall.Openat(int(parent.Fd()), D09ManagedPiCellCgroupLeaf,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.Join(ErrCgroupScopeRefused, fmt.Errorf("open %s cgroup as a real directory: %w", D09ManagedPiCellCgroupLeaf, err))
	}
	cells := os.NewFile(uintptr(fd), "tbound-cells-cgroup")
	defer cells.Close()
	if created {
		// The service unit runs with UMask=0077, which masks the mkdirat mode.
		// Set the exact mode on the directory this call created, through the
		// descriptor; an existing cells directory is validated as found.
		if err := syscall.Fchmod(int(cells.Fd()), d09CellsCgroupMode); err != nil {
			return errors.Join(ErrCgroupScopeRefused, fmt.Errorf("set %s cgroup mode: %w", D09ManagedPiCellCgroupLeaf, err))
		}
	}
	cellsInfo, err := cells.Stat()
	if err != nil {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	cellsStat, ok := cellsInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrCgroupScopeRefused
	}
	if err := validateCellsDirectoryStat(cellsStat, serviceUID, uint64(parentStat.Dev)); err != nil {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	if err := requireNoCgroupProcesses(cells); err != nil {
		return fmt.Errorf("%s cgroup must hold no processes: %w", D09ManagedPiCellCgroupLeaf, err)
	}
	if err := enableD09SubtreeControllers(cells); err != nil {
		return fmt.Errorf("enable cpu/memory/pids controllers on the %s cgroup: %w", D09ManagedPiCellCgroupLeaf, err)
	}
	return nil
}

func validateCellsDirectoryStat(stat *syscall.Stat_t, serviceUID int, parentDev uint64) error {
	switch {
	case stat == nil || stat.Mode&syscall.S_IFMT != syscall.S_IFDIR:
		return errors.New("cells cgroup is not a directory")
	case int(stat.Uid) != serviceUID:
		return errors.New("cells cgroup is not owned by the service uid")
	case stat.Mode&0o7777 != d09CellsCgroupMode:
		return fmt.Errorf("cells cgroup mode %04o differs from %04o", stat.Mode&0o7777, d09CellsCgroupMode)
	case uint64(stat.Dev) != parentDev:
		return errors.New("cells cgroup is on a different filesystem than the service cgroup")
	}
	return nil
}

func requireNoCgroupProcesses(directory *os.File) error {
	procs, err := readAt(directory, "cgroup.procs", 1<<16)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(procs))) != 0 {
		return errors.New("cgroup.procs is not empty")
	}
	return nil
}
