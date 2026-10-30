//go:build linux

package main

import (
	"os"
	"strings"
	"testing"
)

func writeExecutable(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

// TestDigestCommandExecutableRequiresTheMeasuredUIDMap: the owner decision uses the map the cell
// measured and registered, passed in, never a second read of /proc/self/uid_map.
func TestDigestCommandExecutableRequiresTheMeasuredUIDMap(t *testing.T) {
	path := t.TempDir() + "/cmd"
	if err := writeExecutable(path, "#!/bin/sh\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := digestCommandExecutable(path, ""); err == nil || !strings.Contains(err.Error(), "uid_map") {
		t.Fatalf("digestCommandExecutable without a measured uid_map = %v", err)
	}
}
