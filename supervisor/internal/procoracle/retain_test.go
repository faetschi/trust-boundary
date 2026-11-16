package procoracle

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/runner"
)

// The collector retains every input of its Evaluate call, and a reader recomputes from them the bound reference the
// runner journals, including the closure scan (Extra) and the polls after it (Tail).
func TestCollectorRetainsTheInputsThatRecomputeItsReference(t *testing.T) {
	survivor := Process{PID: 9, StartTicks: 1, PPID: 1, State: "S", Comm: "sleep", CgroupPath: "/user.slice", CellIdentity: true, UserNS: []uint64{21, 20}}
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), processes: map[Point][]Process{PointPostTeardown: {survivor}, PointFinalCleanup: {survivor}}}
	c, _ := newEarlyCollector(g)
	c.RetainDir = t.TempDir()
	c.Policy.AuthorizedSockets = []string{"udp 127.0.0.1:53"}
	begin(t, c)
	ev, err := runProc(t, c, closureRef)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Findings[runner.OracleProc01] != runner.FindingObserved {
		t.Fatalf("findings = %v, want an EFF-PROC-01 survivor", ev.Findings)
	}
	read := func(name string) []byte {
		raw, err := os.ReadFile(filepath.Join(c.RetainDir, name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	in, err := ParseInputs(read(InputsName(closureRef.Interval)))
	if err != nil {
		t.Fatal(err)
	}
	term, err := ParseRetained(read(RetainedName(closureRef.Interval)))
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Policy.Extra) != 1 || len(in.Policy.Tail) == 0 {
		t.Fatalf("retained policy holds %d closure scans and %d tail polls", len(in.Policy.Extra), len(in.Policy.Tail))
	}
	rep, bound, err := VerifyRetained(in, term, Expected{CellUID: c.CellUID, CellGID: c.CellGID, TeardownGrace: c.TeardownGrace, AuthorizedSockets: c.Policy.AuthorizedSockets})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(Findings(rep), ev.Findings) {
		t.Fatalf("recomputed findings %v, journaled %v", Findings(rep), ev.Findings)
	}
	for k, ref := range ev.Refs {
		if ref.Report != bound {
			t.Fatalf("%s: recomputed reference %s, journaled %s", k, bound, ref.Report)
		}
	}
}
