//go:build !linux

package main

import (
	"errors"
	"io"
)

func runServe(args []string, transcript io.Writer, stderr io.Writer) error {
	return errors.New("native Pi host profile is missing; production --pi launch is refused")
}
