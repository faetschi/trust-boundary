//go:build linux

package main

import (
	"context"

	"tbound/supervisor/internal/runend"
)

// runEndSource is one row of the run-end registry.
type runEndSource struct {
	err     error
	bound   string
	is      func(taskCtx context.Context, runErr error) bool
	class   runend.Class
	carrier bool
}

var runEndRegistry = []runEndSource{
	{is: func(taskCtx context.Context, _ error) bool { return false }, class: runend.AgentCeiling},
}
