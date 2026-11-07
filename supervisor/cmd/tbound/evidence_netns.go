package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// hostNetnsHeld reports whether any of the network namespaces (nsfs inodes) is still held. A /proc that cannot be
// inspected completely is an error, never "not held".
func hostNetnsHeld(inodes []uint64) (bool, error) { return netnsHeldIn("/proc", inodes) }

// hostNetnsInode is the inode of the host's own network namespace (pid 1's), which a net-sink/v1 command shares.
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

// vanished says the process behind dir exited while it was being read: the error is a missing entry and so is the
// process directory now. A missing entry of a process that is still there (a kernel without the file, a hidden /proc)
// is not "vanished".
func vanished(err error, pidDir string) bool {
	if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ESRCH) {
		return false
	}
	_, serr := os.Stat(pidDir)
	return errors.Is(serr, os.ErrNotExist)
}

// netnsHeldIn looks for the namespaces everywhere one can be held: as the network namespace of any thread, as an open
// descriptor of any process (/proc/PID/fd), and as a bind mount in any mount namespace (the mountinfo of one process
// of every distinct mount namespace). Every inspection that cannot be completed is an error and refuses the proof.
func netnsHeldIn(procRoot string, inodes []uint64) (bool, error) {
	if len(inodes) == 0 {
		return false, nil
	}
	want := map[string]bool{}
	for _, ino := range inodes {
		if ino == 0 {
			return false, errors.New("a command names no network namespace")
		}
		want["net:["+strconv.FormatUint(ino, 10)+"]"] = true
	}
	pids, err := os.ReadDir(procRoot)
	if err != nil {
		return false, err
	}
	mountNS := map[string]bool{}
	inspected := 0
	for _, p := range pids {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		dir := filepath.Join(procRoot, p.Name())
		// Mount namespaces: one mountinfo per distinct namespace.
		mnt, err := os.Readlink(filepath.Join(dir, "ns", "mnt"))
		if vanished(err, dir) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("pid %s: mount namespace: %w", p.Name(), err)
		}
		if !mountNS[mnt] {
			info, err := os.ReadFile(filepath.Join(dir, "mountinfo"))
			if vanished(err, dir) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("pid %s: mountinfo: %w", p.Name(), err)
			}
			mountNS[mnt] = true
			for token := range want {
				if strings.Contains(string(info), token) {
					return true, nil
				}
			}
		}
		// Open descriptors.
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if vanished(err, dir) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("pid %s: fd: %w", p.Name(), err)
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if errors.Is(err, os.ErrNotExist) {
				continue // the descriptor was closed while the directory was read
			}
			if err != nil {
				return false, fmt.Errorf("pid %s: fd %s: %w", p.Name(), fd.Name(), err)
			}
			if want[link] {
				return true, nil
			}
		}
		// Threads' network namespaces.
		tasks, err := os.ReadDir(filepath.Join(dir, "task"))
		if vanished(err, dir) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("pid %s: task: %w", p.Name(), err)
		}
		for _, t := range tasks {
			link, err := os.Readlink(filepath.Join(dir, "task", t.Name(), "ns", "net"))
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				if _, serr := os.Stat(filepath.Join(dir, "task", t.Name())); errors.Is(serr, os.ErrNotExist) {
					continue // the thread exited
				}
			}
			if err != nil {
				return false, fmt.Errorf("pid %s: thread %s: %w", p.Name(), t.Name(), err)
			}
			if want[link] {
				return true, nil
			}
		}
		inspected++
	}
	if inspected == 0 {
		return false, errors.New("no process was inspected")
	}
	return false, nil
}
