//go:build !linux

// tbound-cell-entrypoint is the Linux in-image entrypoint for the governed D09
// cell. This stub keeps `go build ./...` working on non-Linux hosts; it is never
// installed into a cell.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "tbound-cell-entrypoint is Linux-only")
	os.Exit(97)
}
