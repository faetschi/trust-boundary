//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

type providerTerminal struct{}

func (providerTerminal) ReadSecret() ([]byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("a terminal is required")
	}
	defer tty.Close()
	fd := int(tty.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, errors.New("terminal state unavailable")
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO | unix.ECHONL
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(interrupts)
	if unix.IoctlSetTermios(fd, unix.TCSETSF, &quiet) != nil {
		return nil, errors.New("cannot disable terminal echo")
	}
	defer unix.IoctlSetTermios(fd, unix.TCSETSF, old)
	if _, err := fmt.Fprint(tty, "Provider API key: "); err != nil {
		return nil, errors.New("terminal prompt failed")
	}
	defer fmt.Fprintln(tty)
	type input struct {
		key []byte
		err error
	}
	done := make(chan input, 1)
	go func() {
		key, err := bufio.NewReader(io.LimitReader(tty, 4097)).ReadBytes('\n')
		if err != nil || len(key) > 4096 {
			clear(key)
			done <- input{err: errors.New("credential input refused")}
			return
		}
		done <- input{key: key}
	}()
	select {
	case <-interrupts:
		return nil, errors.New("credential input interrupted")
	case in := <-done:
		return in.key, in.err
	}
}
