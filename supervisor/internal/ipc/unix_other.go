//go:build !linux

package ipc

import "errors"

var ErrUnixUnavailable = errors.New("Unix-domain IPC transport is available only on Linux")

// UnixListener is an unavailable-platform placeholder so the package API
// remains portable; net.Pipe is the cross-platform test transport.
type UnixListener struct{}

func ListenUnix(string) (*UnixListener, error) { return nil, ErrUnixUnavailable }

func (*UnixListener) Accept(string) (*Server, error) { return nil, ErrUnixUnavailable }

func (*UnixListener) Close() error { return nil }

func DialUnix(string, string) (*Client, error) { return nil, ErrUnixUnavailable }
