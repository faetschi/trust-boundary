//go:build !linux

package main

import (
	"errors"
	"io"
)

func runProject(args []string, stdout io.Writer, stderr io.Writer) error {
	return errors.New("project is available only on Linux")
}
