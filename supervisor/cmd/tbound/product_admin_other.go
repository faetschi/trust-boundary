//go:build !linux

package main

import (
	"errors"
	"io"
)

func runProductAdmin(_ []string, _, _ io.Writer) error {
	return errors.New("product administration (registration, setup, update, doctor) requires Linux; `tbound release verify` works on every platform")
}
