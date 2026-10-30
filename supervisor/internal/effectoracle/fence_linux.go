//go:build linux

package effectoracle

import (
	"os"

	"golang.org/x/sys/unix"
)

// FenceRoot makes one fence on the root whose descriptor is root: FenceBurst times it opens FenceDir read-only, reads
// the directory once, and closes it. It writes nothing and creates nothing. An observer of the root that watches FenceDir
// (Report.Fences) records the burst as an unbroken alternation of opens and reads, which no scan of the tree makes, and
// which the kernel does not merge: it merges only identical events that are next to each other in the queue. It fails
// when the directory cannot be opened or read (it is absent, or the caller may not open it), which leaves a partial
// burst at most.
func FenceRoot(root *os.File) error {
	var buf [4096]byte
	for i := 0; i < FenceBurst; i++ {
		fd, err := unix.Openat(int(root.Fd()), FenceDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		_, err = unix.Getdents(fd, buf[:])
		_ = unix.Close(fd)
		if err != nil {
			return err
		}
	}
	return nil
}
