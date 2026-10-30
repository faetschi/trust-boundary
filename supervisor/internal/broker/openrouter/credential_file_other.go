//go:build !linux && !windows

package openrouter

import (
	"errors"
)

func readSecureCredentialFile(string) ([]byte, error) {
	return nil, errors.New("secure credential files are unsupported on this platform")
}
