//go:build linux

package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// UnixListener owns a Linux Unix-domain listener created at a previously
// absent path. The parent directory must be supervisor-owned and private.
type UnixListener struct {
	mu       sync.Mutex
	close    sync.Once
	listener *net.UnixListener
	path     string
	identity os.FileInfo
	accepted bool
	closed   bool
	closeErr error
}

// ListenUnix creates a private-mode session socket. It refuses to replace any
// existing path; callers must provide a protected parent directory.
func ListenUnix(path string) (*UnixListener, error) {
	if path == "" {
		return nil, errors.New("Unix socket path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve Unix socket path: %w", err)
	}
	path = filepath.Clean(absolute)
	if len(path) >= 108 {
		return nil, errors.New("Unix socket path exceeds the Linux pathname limit")
	}
	if err := validateSocketParent(path); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("Unix socket path already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect Unix socket path: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on Unix socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("set Unix socket permissions: %w", err)
	}
	identity, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("inspect created Unix socket: %w", err)
	}
	return &UnixListener{listener: listener, path: path, identity: identity}, nil
}

func validateSocketParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect Unix socket parent: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("Unix socket parent must be a non-symlink mode-0700 directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("Unix socket parent must be owned by the supervisor user")
	}
	return nil
}

// Accept binds one accepted connection to the supplied session token.
func (l *UnixListener) Accept(bindingToken string) (*Server, error) {
	if l == nil {
		return nil, ErrClosed
	}
	l.mu.Lock()
	if l.closed || l.accepted || l.listener == nil {
		l.mu.Unlock()
		return nil, ErrClosed
	}
	l.accepted = true
	listener := l.listener
	l.mu.Unlock()
	conn, err := listener.AcceptUnix()
	if err != nil {
		return nil, fmt.Errorf("accept Unix IPC connection: %w", err)
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = conn.Close()
		return nil, ErrClosed
	}
	if l.listener == listener {
		l.listener = nil
	}
	l.mu.Unlock()
	_ = listener.Close()
	server, err := NewServer(conn, bindingToken)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return server, nil
}

// Close closes the listener and removes its socket path only if the path still
// names the socket created by ListenUnix.
func (l *UnixListener) Close() error {
	if l == nil {
		return nil
	}
	l.close.Do(func() {
		l.mu.Lock()
		l.closed = true
		listener := l.listener
		l.listener = nil
		l.mu.Unlock()
		if listener != nil {
			l.closeErr = listener.Close()
		}
		current, statErr := os.Lstat(l.path)
		if statErr == nil && os.SameFile(l.identity, current) {
			if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				l.closeErr = errors.Join(l.closeErr, err)
			}
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			l.closeErr = errors.Join(l.closeErr, statErr)
		}
	})
	return l.closeErr
}

// DialUnix opens the client side of a Linux session socket and binds it to the
// same session token expected by the supervisor.
func DialUnix(path, bindingToken string) (*Client, error) {
	if len(path) >= 108 {
		return nil, errors.New("Unix socket path exceeds the Linux pathname limit")
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("dial Unix IPC socket: %w", err)
	}
	client, err := NewClient(conn, bindingToken)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}
