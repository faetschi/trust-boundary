package sessionrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/faultfs"
)

// A command view changed after Import is refused by the preauthorization Scan, before
// authorization and the runner, and the view is removed. The hook changes the view at
// the existing view-setup sync seam. That sync still passes because it opens entries
// with O_PATH, while the Scan must read the mode-0 file and fails.
func TestRunBashScanRefusesViewChangedAfterImportAndRemovesIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 file, so the Scan cannot refuse it")
	}
	f := newFaultFixture(t)
	before := f.image()
	runnerCalls := 0
	changed := false
	restore := faultfs.Install(func(site faultfs.Site, op faultfs.Op) error {
		if site != faultfs.SiteViewSetup || op != faultfs.OpFsync || changed {
			return nil
		}
		changed = true
		views, err := os.ReadDir(filepath.Join(f.rootPath, "views"))
		if err != nil || len(views) != 1 {
			t.Fatalf("expected exactly one command view at the sync seam: %d entries, err=%v", len(views), err)
		}
		if err := os.Chmod(filepath.Join(f.rootPath, "views", views[0].Name(), "README.md"), 0); err != nil {
			t.Fatalf("change command view after import: %v", err)
		}
		return nil
	})
	defer restore()
	_, err := f.bash(countingRunner(&runnerCalls))
	restore()
	if !changed {
		t.Fatal("the command view was never changed; the regression is vacuous")
	}
	if err == nil || !strings.Contains(err.Error(), "verify command view before authorization") {
		t.Fatalf("RunBash err=%v, want the preauthorization view scan to refuse the changed view", err)
	}
	if runnerCalls != 0 {
		t.Fatalf("a changed command view launched the runner: calls=%d", runnerCalls)
	}
	after := f.image()
	sameImageExceptViews(t, before, after)
	if len(after.views) != 0 {
		t.Fatalf("changed command view survived the refusal: %v", after.views)
	}
	if f.store.quarantined || len(f.store.generations) != 1 || len(f.store.transitions) != 0 {
		t.Fatalf("refused command view changed the Store: quarantined=%t generations=%d", f.store.quarantined, len(f.store.generations))
	}
	f.requireSealedInput()
}
