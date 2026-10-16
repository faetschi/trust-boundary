//go:build linux

package effectoracle

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"syscall"
)

// inotifyHeaderSize is sizeof(struct inotify_event) without the name.
const inotifyHeaderSize = 16

// errTruncatedRecord reports a record that claims more bytes than were read.
var errTruncatedRecord = errors.New("effectoracle: truncated inotify record")

// inotifyAPI is the kernel surface the observer uses. Tests substitute an
// implementation to inject watch-limit, overflow, and read faults. The observer
// assumes nothing else about it.
type inotifyAPI interface {
	AddWatch(path string, mask uint32) (int, error)
	RmWatch(wd int) error
	Read(p []byte) (int, error)
	Close() error
}

// rawEvent is one decoded inotify record. A watch descriptor of -1 with
// IN_Q_OVERFLOW marks records the kernel dropped.
type rawEvent struct {
	wd   int
	mask uint32
	name string
}

// parseEvents decodes every record in buf. The kernel writes host-endian fields,
// and the name is NUL-padded. A record whose length exceeds the bytes read is
// rejected, not partially decoded.
func parseEvents(buf []byte) ([]rawEvent, error) {
	var out []rawEvent
	for len(buf) > 0 {
		if len(buf) < inotifyHeaderSize {
			return nil, errTruncatedRecord
		}
		wd := int32(binary.NativeEndian.Uint32(buf[0:4]))
		mask := binary.NativeEndian.Uint32(buf[4:8])
		n := uint64(binary.NativeEndian.Uint32(buf[12:16]))
		if n > uint64(len(buf)-inotifyHeaderSize) {
			return nil, errTruncatedRecord
		}
		end := inotifyHeaderSize + int(n)
		name := buf[inotifyHeaderSize:end]
		if i := bytes.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		out = append(out, rawEvent{wd: int(wd), mask: mask, name: string(name)})
		buf = buf[end:]
	}
	return out, nil
}

// kernelInotify is the real inotify implementation. The descriptor is
// non-blocking and wrapped in an os.File, so Read parks on the runtime poller,
// and Close unblocks it. Watch calls go through SyscallConn, which keeps the
// descriptor valid for the duration of each call.
type kernelInotify struct {
	file *os.File
	raw  syscall.RawConn
}

// openKernelInotify creates a close-on-exec, non-blocking inotify instance.
func openKernelInotify() (*kernelInotify, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "inotify")
	raw, err := f.SyscallConn()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &kernelInotify{file: f, raw: raw}, nil
}

// AddWatch adds or updates a watch. The kernel returns the existing descriptor
// when the inode is already watched.
func (k *kernelInotify) AddWatch(path string, mask uint32) (int, error) {
	var wd int
	var serr error
	if err := k.raw.Control(func(fd uintptr) {
		wd, serr = syscall.InotifyAddWatch(int(fd), path, mask)
	}); err != nil {
		return -1, err
	}
	return wd, serr
}

// RmWatch removes a watch by descriptor.
func (k *kernelInotify) RmWatch(wd int) error {
	var serr error
	if err := k.raw.Control(func(fd uintptr) {
		_, serr = syscall.InotifyRmWatch(int(fd), uint32(wd))
	}); err != nil {
		return err
	}
	return serr
}

// Read returns one batch of records. It returns os.ErrClosed after Close.
func (k *kernelInotify) Read(p []byte) (int, error) {
	return k.file.Read(p)
}

// Close closes the descriptor and unblocks a pending Read.
func (k *kernelInotify) Close() error {
	return k.file.Close()
}
