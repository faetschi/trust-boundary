//go:build !linux

package main

import (
	"errors"
	"io"
)

func runProvider(_ []string, _, _ io.Writer) error {
	return errors.New("product provider credential administration requires Linux")
}
