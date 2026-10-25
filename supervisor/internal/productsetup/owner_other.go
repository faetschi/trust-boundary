//go:build !linux

package productsetup

import "io/fs"

// ownerOf has no numeric owner off Linux; setup and doctor are Linux-host tools.
func ownerOf(fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
