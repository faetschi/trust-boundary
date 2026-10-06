//go:build !windows

package main

import (
	"errors"
	"os"
)

func writeOwnerOnlyTokenFile(path, token string) error {
	if path == "" || token == "" {
		return errors.New("token metadata path and token are required")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("pairing metadata path is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Unix mode bits are the complete owner-private primitive for this fixture
	// path. The production launcher must retain an equivalent private channel.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	_, err = file.WriteString(token + "\n")
	return err
}
