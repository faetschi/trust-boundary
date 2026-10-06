//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
)

func writeOwnerOnlyTokenFile(path, token string) error {
	if path == "" || token == "" {
		return errors.New("token metadata path and token are required")
	}
	// Reserve an empty new file atomically; never write a capability while its
	// DACL is still inherited, and never truncate an existing destination.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("reserve new pairing metadata: %w", err)
	}
	removeOnFailure := true
	defer func() {
		_ = file.Close()
		if removeOnFailure {
			_ = os.Remove(path)
		}
	}()
	current, err := user.Current()
	if err != nil || current.Username == "" {
		return errors.New("cannot determine the Windows owner for pairing metadata")
	}
	// A Windows mode bit is not an owner-only boundary. Remove inherited ACEs
	// and grant only the launching credential; failure is fail-closed.
	if output, err := exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", current.Username+":F").CombinedOutput(); err != nil {
		return fmt.Errorf("protect pairing metadata DACL: %w (%s)", err, string(output))
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		return fmt.Errorf("write protected pairing metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync protected pairing metadata: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close protected pairing metadata: %w", err)
	}
	removeOnFailure = false
	return nil
}
