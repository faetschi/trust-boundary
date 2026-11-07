package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// hostNetnsInode is the inode of the host's own network namespace (pid 1's), which a net-sink/v1 command shares.
// It only classifies that one full-horizon case; the drain proof is structural (procoracle.EvaluateClosure).
func hostNetnsInode() (uint64, error) { return netnsInodeIn("/proc") }

func netnsInodeIn(procRoot string) (uint64, error) {
	link, err := os.Readlink(filepath.Join(procRoot, "1", "ns", "net"))
	if err != nil {
		return 0, err
	}
	n, ok := strings.CutPrefix(link, "net:[")
	n, ok2 := strings.CutSuffix(n, "]")
	ino, err := strconv.ParseUint(n, 10, 64)
	if !ok || !ok2 || err != nil || ino == 0 {
		return 0, fmt.Errorf("the host network namespace link %q is not net:[inode]", link)
	}
	return ino, nil
}
