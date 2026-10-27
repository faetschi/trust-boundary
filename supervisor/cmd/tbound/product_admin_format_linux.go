//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"tbound/supervisor/internal/productsetup"
)

// productAdminBinaryFormat asks a candidate binary for its installation format with `product
// installation-format`. The administrator chose the candidate, and the update then runs it as root anyway
// (doctor --self-test), so asking first adds no trust. A binary that runs but does not know the command
// predates the format and is the legacy format. One that cannot run is an error, not a legacy format.
func productAdminBinaryFormat(path string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "product", "installation-format")
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return 0, fmt.Errorf("the binary did not answer `product installation-format` in time: %w", ctx.Err())
	case errors.As(err, &exit):
		return productsetup.LegacyInstallationFormat, nil
	case err != nil:
		return 0, err
	}
	format, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || format <= 0 {
		return 0, fmt.Errorf("the binary answered %q to `product installation-format`", strings.TrimSpace(truncate(string(out), 64)))
	}
	return format, nil
}
