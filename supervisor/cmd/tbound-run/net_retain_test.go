package main

import (
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/netoracle"
)

func TestNetIntervalEvidenceIsRetainedBesideTheJournal(t *testing.T) {
	retained := func(runDir string) string {
		p := plan{cfg: runConfig{RunDir: runDir, Proc: procConfig{CellUID: 1000, CellGID: 1000, MonitorSeconds: 30}}}
		return p.collectorsOver(obsEvidence{}).NetFrom.(*netoracle.Collector).RetainDir
	}
	dir := filepath.Join(t.TempDir(), "run")
	if got, want := retained(dir), filepath.Join(dir, "net-evidence"); got != want {
		t.Fatalf("RetainDir = %q, want %q", got, want)
	}
	if got := retained(""); got != "" {
		t.Fatalf("a plan without a run dir retains into %q", got)
	}
}
