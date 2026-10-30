//go:build linux

package productsetup

import (
	"io/fs"
	"syscall"
)

// ownerOf returns the numeric owner of a file.
func ownerOf(info fs.FileInfo) (uid, gid int, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
