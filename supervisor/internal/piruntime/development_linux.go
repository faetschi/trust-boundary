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
const DevelopmentPiNodeVersion = "v24.15.0"

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

// StageDevelopmentPiWorkerBundle copies the pinned adapter source snapshot into a
// fresh private mode-0700 bundle under bundleParent and links the pre-existing
// pinned dependency tree. It is the shared staging step used by both the opt-in
// development process test and the explicit development CLI route; neither
// installs or mutates node_modules. The returned path is the runtime bundle root
// consumed by StartDevelopmentPiSDKWorker.
func StageDevelopmentPiWorkerBundle(bundleParent, adapterRoot, dependencyRoot string) (string, error) {
	if !filepath.IsAbs(bundleParent) || !filepath.IsAbs(adapterRoot) || !filepath.IsAbs(dependencyRoot) {
		return "", errors.New("development Pi bundle staging requires absolute parent, adapter, and dependency paths")
	}
	dependencyInfo, err := os.Lstat(dependencyRoot)
	if err != nil || !dependencyInfo.IsDir() || dependencyInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("development pinned dependency root must be an existing non-symlink directory")
	}
	bundleRoot := filepath.Join(bundleParent, "adapter")
	if err := os.Mkdir(bundleRoot, 0o700); err != nil {
		return "", fmt.Errorf("create development bundle root: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(bundleRoot)
		}
	}()
	if err := os.Chmod(bundleRoot, 0o700); err != nil {
		return "", err
	}
	workerDir := filepath.Join(bundleRoot, "src")
	if err := os.Mkdir(workerDir, 0o700); err != nil {
		return "", err
	}
	if err := os.Symlink(dependencyRoot, filepath.Join(bundleRoot, "node_modules")); err != nil {
		return "", fmt.Errorf("link existing pinned dependencies into private development bundle: %w", err)
	}
	for _, relative := range DeveloperPiSDKAdapterSourceFiles {
		data, err := readRegularBounded(filepath.Join(adapterRoot, filepath.FromSlash(relative)), 16<<20)
		if err != nil {
			return "", fmt.Errorf("read development adapter source %s: %w", relative, err)
		}
		if err := os.WriteFile(filepath.Join(bundleRoot, filepath.FromSlash(relative)), data, 0o600); err != nil {
			return "", fmt.Errorf("stage development adapter source %s: %w", relative, err)
		}
	}
	cleanup = false
	return bundleRoot, nil
}
