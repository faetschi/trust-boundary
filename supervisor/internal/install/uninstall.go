package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// managedMarker is written into every directory TBound creates so uninstall can
// confirm ownership before removing anything.
const managedMarker = ".tbound-managed"

// FS abstracts the filesystem operations uninstall needs, for testing.
type FS interface {
	Stat(name string) (os.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
	Remove(name string) error
	RemoveAll(path string) error
	MkdirAll(path string, perm os.FileMode) error
}

type osFS struct{}

func (osFS) Stat(name string) (os.FileInfo, error)             { return os.Stat(name) }
func (osFS) ReadFile(name string) ([]byte, error)              { return os.ReadFile(name) }
func (osFS) WriteFile(n string, d []byte, p os.FileMode) error { return os.WriteFile(n, d, p) }
func (osFS) Remove(name string) error                          { return os.Remove(name) }
func (osFS) RemoveAll(path string) error                       { return os.RemoveAll(path) }
func (osFS) MkdirAll(path string, perm os.FileMode) error      { return os.MkdirAll(path, perm) }

// OSFS returns the real filesystem.
func OSFS() FS { return osFS{} }

// MarkManaged records ownership of a TBound-created directory.
func MarkManaged(fs FS, dir string) error {
	if err := fs.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return fs.WriteFile(filepath.Join(dir, managedMarker), []byte("tbound\n"), 0o640)
}

func isManaged(fs FS, dir string) bool {
	_, err := fs.Stat(filepath.Join(dir, managedMarker))
	return err == nil
}

// UninstallPlan lists exactly what uninstall will remove.
type UninstallPlan struct {
	Dirs  []string `json:"dirs"`
	Files []string `json:"files"`
}

// PlanUninstall determines the removable TBound-managed paths. Directories are
// only included when they carry the TBound ownership marker; unexpected
// contents cause a refusal rather than a broad delete.
func PlanUninstall(fs FS, bundleRoot, etcDir, unitPath string) (UninstallPlan, error) {
	if fs == nil {
		return UninstallPlan{}, errors.New("uninstall requires a filesystem")
	}
	var plan UninstallPlan
	for _, dir := range []string{bundleRoot, etcDir} {
		if dir == "" {
			continue
		}
		if _, err := fs.Stat(dir); err != nil {
			continue // nothing to remove
		}
		if !isManaged(fs, dir) {
			return UninstallPlan{}, fmt.Errorf("refuse to remove unmanaged directory %s (missing %s)", dir, managedMarker)
		}
		plan.Dirs = append(plan.Dirs, dir)
	}
	if unitPath != "" {
		if _, err := fs.Stat(unitPath); err == nil {
			plan.Files = append(plan.Files, unitPath)
		}
	}
	return plan, nil
}

// ApplyUninstall removes exactly the planned paths.
func ApplyUninstall(fs FS, plan UninstallPlan) error {
	if fs == nil {
		return errors.New("uninstall requires a filesystem")
	}
	for _, f := range plan.Files {
		if err := fs.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, d := range plan.Dirs {
		if !isManaged(fs, d) {
			return fmt.Errorf("refuse to remove %s: ownership marker disappeared", d)
		}
		if err := fs.RemoveAll(d); err != nil {
			return err
		}
	}
	return nil
}
