//go:build !linux

package taskaccept

import "io/fs"

// fileOwner has no owner to report off Linux. A view without ownership has no canonical fence:
// the fence is part of the tree, which fails closed.
func fileOwner(fs.FileInfo) (uid, gid uint32, ok bool) { return 0, 0, false }
