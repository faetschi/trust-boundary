//go:build linux

package publication

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/publicationevents"
	"tbound/supervisor/internal/wsoracle"
)

// Temporary stress lever for issue #184 (not committed): unit 7's ledger shape, a delete of protected/work-report.txt
// and a create of relocated/work-report.txt, published under a real observer, many times. Each run is classified by
// the inputs judgeTransient reads.
func TestStress184(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("STRESS184_N"))
	if n == 0 {
		t.Skip("STRESS184_N not set")
	}
	live := map[string]string{"docs/note.md": "note", "protected/work-report.txt": "work report contents 29 bytes"}
	final := map[string]string{"docs/note.md": "note", "relocated/work-report.txt": "work report contents 29 bytes"}
	dirs := []string{"docs", "protected", "relocated"}
	counts := map[string]int{}
	scansBefore, _ := strconv.Atoi(os.Getenv("STRESS184_SCANS"))
	for i := 0; i < n; i++ {
		f := newFixture(t, live, live, final, final, dirs, dirs, dirs)
		repo := f.acquire(1)
		observer, err := effectoracle.Start(filepath.Join(f.root, "live"))
		if err != nil {
			t.Fatal(err)
		}
		liveRoot := filepath.Join(f.root, "live")
		for k := 0; k < scansBefore; k++ {
			_ = wsoracle.Snapshot(liveRoot, "scan")
			time.Sleep(time.Millisecond)
		}
		result, err := repo.Publish(context.Background(), f.request, func(b CommitBinding) (Authorization, error) {
			return Authorization{DecisionID: "d", BindingDigest: b.BindingDigest}, nil
		}, nil)
		if err != nil || result.Status != StatusSucceeded {
			t.Fatalf("Publish: %v %+v", err, result)
		}
		for k := 0; k < 2; k++ {
			_ = wsoracle.Snapshot(liveRoot, "scan")
			time.Sleep(time.Millisecond)
		}
		report, err := observer.Stop()
		if err != nil {
			t.Fatal(err)
		}
		repo.Close()
		class := "ok"
		var got []publicationevents.Event
		for _, ev := range report.Events {
			got = append(got, publicationevents.Event{Path: ev.Path, Kind: ev.Kind, IsDir: ev.IsDir})
		}
		seq, _ := publicationevents.Sequence(result.Ledger)
		runs := fenceRuns184(report)
		switch {
		case !report.QualifiedWindow():
			class = fmt.Sprintf("unqualified gaps=%+v beats=%+v", report.Gaps, report.Heartbeats)
		case len(runs) != 2:
			class = fmt.Sprintf("fences=%d runs=%v fences=%+v events=%+v", len(runs), runs, report.Fences, report.Events)
		case !reflect.DeepEqual(got, seq):
			class = fmt.Sprintf("events %v want %v", got, seq)
		}
		if class != "ok" && counts[class] == 0 {
			t.Logf("run %d: %s", i, class)
		}
		if class != "ok" {
			class = class[:min(len(class), 12)]
		}
		counts[class]++
	}
	t.Logf("STRESS184 n=%d %v", n, counts)
}

func fenceRuns184(rep *effectoracle.Report) [][2]uint64 {
	var runs [][2]uint64
	var cur [2]uint64
	length := 0
	var prev effectoracle.Fence
	end := func() {
		if length >= 2*effectoracle.FenceBurst {
			runs = append(runs, cur)
		}
		length = 0
	}
	for _, f := range rep.Fences {
		if f.Path != effectoracle.FenceDir || !f.IsDir || (f.Op != effectoracle.FenceOpen && f.Op != effectoracle.FenceAccess) {
			end()
			continue
		}
		if length > 0 && (f.Seq != prev.Seq+1 || f.Op == prev.Op) {
			end()
		}
		if length == 0 {
			cur[0] = f.Seq
		}
		cur[1] = f.Seq
		prev = f
		length++
	}
	end()
	return runs
}
