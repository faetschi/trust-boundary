//go:build linux

package taskaccept

import (
	"io/fs"
	"syscall"
)

// fileOwner returns the owner of a file the walk reported, when the file system reports it. A
// view that does not (an in-memory fs in a test) has no canonical fence: the fence is then part
// of the tree, which fails an unchanged_except check rather than hiding a write into it.
func fileOwner(info fs.FileInfo) (uid, gid uint32, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat || st == nil {
		return 0, 0, false
	}
	return st.Uid, st.Gid, true
}
