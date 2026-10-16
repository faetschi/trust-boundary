//go:build !linux

package effectoracle

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
