//go:build linux

package piruntime

import "syscall"

// OSRouteProbeFS is the real read-only surface of the route probe. Every call is a read: an open with
// O_RDONLY (and one byte read), a stat, an open of a directory. The route probe test keeps this file free
// of any call that writes.
type OSRouteProbeFS struct{}

const probeOpenFlags = syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NONBLOCK | syscall.O_NOCTTY

// OpenRead opens path O_RDONLY, reads at most one byte and discards it.
func (OSRouteProbeFS) OpenRead(path string) error {
	fd, err := syscall.Open(path, probeOpenFlags, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var one [1]byte
	_, err = syscall.Read(fd, one[:])
	return err
}

// Stat reads the metadata of path.
func (OSRouteProbeFS) Stat(path string) error {
	var st syscall.Stat_t
	return syscall.Stat(path, &st)
}

// List opens path as a directory and reads one batch of entries.
func (OSRouteProbeFS) List(path string) error {
	fd, err := syscall.Open(path, probeOpenFlags|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var batch [4096]byte
	_, err = syscall.ReadDirent(fd, batch[:])
	return err
}
