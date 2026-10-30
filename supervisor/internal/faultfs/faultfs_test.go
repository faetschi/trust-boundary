package faultfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCheckIsInertWithoutHookAndRestores(t *testing.T) {
	if err := Check(SiteAuditJournal, OpFsync); err != nil {
		t.Fatalf("Check without a hook = %v", err)
	}
	restore := Install(func(site Site, op Op) error {
		if site == SiteAuditJournal && op == OpFsync {
			return syscall.EIO
		}
		return nil
	})
	if err := Check(SiteAuditJournal, OpFsync); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Check with a hook = %v, want EIO", err)
	}
	if err := Check(SiteAuditJournal, OpWrite); err != nil {
		t.Fatalf("hook failed an unselected operation: %v", err)
	}
	restore()
	if err := Check(SiteAuditJournal, OpFsync); err != nil {
		t.Fatalf("Check after restore = %v", err)
	}
}

// TestInstallIsOnlyCalledFromTests keeps the seam out of production code: a
// hook can be installed only by a *_test.go file of this module.
func TestInstallIsOnlyCalledFromTests(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found: %v", err)
	}
	checked := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			filepath.Base(filepath.Dir(path)) == "faultfs" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		if strings.Contains(string(data), "faultfs.Install(") {
			t.Errorf("%s installs a fault hook outside a test file", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("scanned only %d production files; the scan is not covering the module", checked)
	}
}
