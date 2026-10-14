//go:build !windows && !linux

package webchat

import (
	"errors"
	"os"
)

func openRegularNoFollow(path string) (*os.File, error) {
	return nil, errors.New("safe no-follow observation document opening is unavailable on this platform")
}
