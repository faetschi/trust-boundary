//go:build !linux

package effectoracle

import (
	"context"
	"os"
)

// Observer is unavailable off Linux. Every operation fails closed with
// ErrUnsupported, so no report can be mistaken for an observation.
type Observer struct{}

// Start returns ErrUnsupported on platforms other than Linux.
func Start(root string) (*Observer, error) {
	return nil, ErrUnsupported
}

// Mark returns ErrUnsupported on platforms other than Linux.
func (o *Observer) Mark(name string) error {
	return ErrUnsupported
}

// Stop returns ErrUnsupported on platforms other than Linux.
func (o *Observer) Stop() (*Report, error) {
	return nil, ErrUnsupported
}

// Abort does nothing on platforms other than Linux, where no observer exists.
func (o *Observer) Abort() {}

// StartContext returns ErrUnsupported on platforms other than Linux.
func StartContext(ctx context.Context, root string) (*Observer, error) {
	return nil, ErrUnsupported
}

// FenceRoot returns ErrUnsupported on platforms other than Linux, where no observer exists.
func FenceRoot(root *os.File) error { return ErrUnsupported }
