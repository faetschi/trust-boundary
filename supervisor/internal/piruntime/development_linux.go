//go:build linux

package piruntime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// DevelopmentPiNodeVersion is the pinned Linux Node release the adapter source
// snapshot under adapter/ was validated against. The development launcher does
// not discover a Node binary: the caller must provide this exact release.
const DevelopmentPiNodeVersion = "v24.21.0"

var DeveloperPiCLIAdapterSourceFiles = []string{
	"src/tbound-cli-extension.ts", "src/broker-provider.ts",
	"src/proxy-tools.ts", "src/ipc-transport.ts",
}

// StageDevelopmentPiCLIBundle stages only TBound's mediated extension source.
// Pi API packages are resolved from the installed official CLI, never from an
// adapter node_modules tree.
func StageDevelopmentPiCLIBundle(bundleParent, adapterRoot string) (string, error) {
	if !filepath.IsAbs(bundleParent) || !filepath.IsAbs(adapterRoot) {
		return "", errors.New("Pi CLI staging requires absolute paths")
	}
	root := filepath.Join(bundleParent, "pi-cli-extension")
	if err := os.Mkdir(root, 0o700); err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"private":true,"type":"module"}`), 0o600); err != nil {
		return "", err
	}
	for _, relative := range DeveloperPiCLIAdapterSourceFiles {
		if relative == "src/tbound-cli-extension.ts" {
			data, err := readRegularBounded(filepath.Join(adapterRoot, filepath.FromSlash(relative)), 16<<20)
			if err != nil {
				return "", fmt.Errorf("read Pi CLI extension source %s: %w", relative, err)
			}
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(relative)), data, 0o600); err != nil {
				return "", err
			}
			continue
		}
		data, err := readRegularBounded(filepath.Join(adapterRoot, filepath.FromSlash(relative)), 16<<20)
		if err != nil {
			return "", fmt.Errorf("read Pi CLI extension source %s: %w", relative, err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(relative)), data, 0o600); err != nil {
			return "", err
		}
	}
	cleanup = false
	return root, nil
}

// ValidateDevelopmentPrivateDirectory enforces the private development-root
// precondition the opt-in development tests and the explicit development CLI
// route require: a non-symlink mode-0700 directory owned by the launching UID.
// It deliberately does not verify or claim any underlying filesystem type (for
// example ext4). It is shared by the tests and the explicit development CLI
// route so the private-root contract cannot drift between them.
func ValidateDevelopmentPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("development private root must be a non-symlink mode-0700 directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("development private root must be owned by the current Linux UID")
	}
	return nil
}

// DevelopmentNodeVersion runs the explicit Node binary with the fixed minimal
// worker environment and returns its trimmed version. The caller must already
// have created the private HOME/TMPDIR directories.
func DevelopmentNodeVersion(nodePath, home, tempDir string) (string, error) {
	if !filepath.IsAbs(nodePath) {
		return "", errors.New("development Node path must be absolute")
	}
	environment := minimalWorkerEnvironment(home, tempDir)
	if err := validateMinimalWorkerEnvironment(environment); err != nil {
		return "", err
	}
	command := exec.Command(nodePath, "--version")
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("run explicit development Node --version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}
