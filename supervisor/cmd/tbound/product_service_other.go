//go:build !linux

package main

import (
	"errors"
	"io"
)

func runProductServe(args []string, stdout io.Writer, stderr io.Writer) error {
	return errors.New("product-serve is available only on Linux")
}
