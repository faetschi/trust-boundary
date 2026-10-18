//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// The maintenance lock keeps `tbound trial reset` (root) and the Pi service (uid tbound-pi) from
// working on the live root at the same time. Both sides flock the same file exclusively and never
// wait: the service takes it when it admits a workflow and keeps it until the workflow is settled
// (terminal and publication included); the reset takes it before its pending check and keeps it
// across the kill, the wipe and populate, and the final digest check. A holder that is gone (a
// crash) releases it, the kernel closes the descriptor; the catalog's pending check still stands
// on its own.
//
// Path: /run/tbound is root-owned (0755, made by the socket unit) and the service sees /run read
// only (ProtectSystem=strict), so the service could not create or replace a file there. The
// reset creates the file (root:root 0644) and the service only opens it read-only; flock does not
// need write access. A file that is not a regular, root-owned file nobody else can write, or
// that is absent, is "unavailable" and the service refuses the admission: it never runs without
// the lock. /run/tbound-pi is not used because systemd removes it with the service, and the
// reset must also work then. infra/guest/tmpfiles.d/tbound-maintenance.conf creates the file at
// boot, so a service that is started before any reset is not refused.
const maintenanceLockPath = "/run/tbound/maintenance.lock"

var (
	errMaintenanceBusy        = errors.New("the maintenance lock is held")
	errMaintenanceUnavailable = errors.New("the maintenance lock file is unavailable")
)

type maintenanceLock struct {
	path     string
	ownerUID uint32 // the owner the lock file must have: root in production
	create   bool   // the reset creates the file; the service never does
}

var (
	serviceMaintenanceLock = maintenanceLock{path: maintenanceLockPath}
	resetMaintenanceLock   = maintenanceLock{path: maintenanceLockPath, create: true}
)

// acquire takes the lock without waiting and returns the function that releases it (once).
func (l maintenanceLock) acquire() (func(), error) {
	flags, mode := os.O_RDONLY, os.FileMode(0)
	if l.create {
		flags, mode = os.O_RDWR|os.O_CREATE, 0o644
	}
	file, err := os.OpenFile(l.path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, mode)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMaintenanceUnavailable, err)
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != l.ownerUID {
		file.Close()
		return nil, fmt.Errorf("%w: %s is not a regular file owned by uid %d", errMaintenanceUnavailable, l.path, l.ownerUID)
	}
	if l.create && st.Mode&0o777 != 0o644 {
		// Created under the umask of the caller (0077 in the guest): the service must be able to open it.
		if err := file.Chmod(0o644); err != nil {
			file.Close()
			return nil, fmt.Errorf("%w: %v", errMaintenanceUnavailable, err)
		}
	} else if st.Mode&0o022 != 0 {
		file.Close()
		return nil, fmt.Errorf("%w: %s is writable by group or others", errMaintenanceUnavailable, l.path)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", errMaintenanceBusy, l.path)
		}
		return nil, fmt.Errorf("%w: %v", errMaintenanceUnavailable, err)
	}
	var once sync.Once
	return func() { once.Do(func() { file.Close() }) }, nil
}
