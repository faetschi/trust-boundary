//go:build linux

package taskaccept

import (
	"io/fs"
	"syscall"

	"golang.org/x/sys/unix"
)

// fileOwner returns the owner of a file the walk reported, when the file system reports it. A
// view that does not (an in-memory fs in a test) has no canonical fence: the fence is then part
// of the tree, which fails an unchanged_except check rather than hiding a write into it.
func fileOwner(info fs.FileInfo) (uid, gid uint32, ok bool) {
	switch st := info.Sys().(type) {
	case *syscall.Stat_t: // os.Lstat
		if st != nil {
			return st.Uid, st.Gid, true
		}
	case *unix.Stat_t: // RootFS, the production view of the live root
		if st != nil {
			return st.Uid, st.Gid, true
		}
	}
	return 0, 0, false
}
