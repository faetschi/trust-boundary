//go:build !linux

// tbound-profile is Linux-only: the signed host profile, its signature domain,
// and its digest are defined by the Linux runtime. This stub keeps
// `go build ./...` working on non-Linux hosts and never produces a profile.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "tbound-profile is Linux-only: the signed host-profile format is Linux-specific")
	os.Exit(2)
}
