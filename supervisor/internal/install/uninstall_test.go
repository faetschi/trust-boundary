package install

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type memFS struct {
	files map[string][]byte
	dirs  map[string]bool
}

func newMemFS() *memFS { return &memFS{files: map[string][]byte{}, dirs: map[string]bool{}} }

func (m *memFS) Stat(name string) (os.FileInfo, error) {
	if _, ok := m.files[name]; ok {
		return statFile{}, nil
	}
	if m.dirs[name] {
		return statDir{}, nil
	}
	return nil, os.ErrNotExist
}
func (m *memFS) ReadFile(name string) ([]byte, error) {
	if b, ok := m.files[name]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}
func (m *memFS) WriteFile(name string, data []byte, _ os.FileMode) error {
	m.files[name] = data
	return nil
}
func (m *memFS) Remove(name string) error {
	if _, ok := m.files[name]; !ok {
		return os.ErrNotExist
	}
	delete(m.files, name)
	return nil
}
func (m *memFS) RemoveAll(path string) error {
	for f := range m.files {
		if f == path || (len(f) > len(path) && f[:len(path)] == path) {
			delete(m.files, f)
		}
	}
	delete(m.dirs, path)
	return nil
}
func (m *memFS) MkdirAll(path string, _ os.FileMode) error {
	m.dirs[path] = true
	return nil
}

type statFile struct{}

func (statFile) Name() string       { return "file" }
func (statFile) Size() int64        { return 0 }
func (statFile) Mode() os.FileMode  { return 0o600 }
func (statFile) ModTime() time.Time { return time.Time{} }
func (statFile) IsDir() bool        { return false }
func (statFile) Sys() any           { return nil }

type statDir struct{}

func (statDir) Name() string       { return "dir" }
func (statDir) Size() int64        { return 0 }
func (statDir) Mode() os.FileMode  { return os.ModeDir | 0o750 }
func (statDir) ModTime() time.Time { return time.Time{} }
func (statDir) IsDir() bool        { return true }
func (statDir) Sys() any           { return nil }

var _ = os.ErrNotExist

func TestPlanUninstallRefusesUnmanagedDir(t *testing.T) {
	fs := newMemFS()
	fs.dirs["/opt/tbound"] = true
	if _, err := PlanUninstall(fs, "/opt/tbound", "/etc/tbound", ""); err == nil {
		t.Fatal("unmanaged directory must be refused")
	}
}

func TestPlanAndApplyUninstallManagedPaths(t *testing.T) {
	fs := newMemFS()
	if err := MarkManaged(fs, "/opt/tbound"); err != nil {
		t.Fatal(err)
	}
	if err := MarkManaged(fs, "/etc/tbound"); err != nil {
		t.Fatal(err)
	}
	fs.files["/etc/systemd/user/tbound.service"] = []byte("unit")
	plan, err := PlanUninstall(fs, "/opt/tbound", "/etc/tbound", "/etc/systemd/user/tbound.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Dirs) != 2 || len(plan.Files) != 1 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if err := ApplyUninstall(fs, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat("/opt/tbound"); err == nil {
		t.Fatal("managed dir should be removed")
	}
}

func TestWriteBrokerConfigRefusesSecret(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "broker.env")
	if err := WriteBrokerConfig(path, []byte("TBOUND_OPENROUTER_API_KEY=sk-abc123realsecret\n")); err == nil {
		t.Fatal("must refuse embedding a real secret")
	}
	if err := WriteBrokerConfig(path, []byte(BrokerConfigTemplate())); err != nil {
		t.Fatalf("template must be accepted: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if posixModes() && info.Mode().Perm() != 0o600 {
		t.Fatalf("broker config must be 0600, got %o", info.Mode().Perm())
	}
}
