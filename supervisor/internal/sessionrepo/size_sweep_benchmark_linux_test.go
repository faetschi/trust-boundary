//go:build linux

package sessionrepo

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

// TestSizeSweepBenchmark times Seed, read, write and bash on real source trees, so the cost of
// the whole-tree copy and scan can be reported against project size. The runner writes one file
// into the view and starts no process, so the times are the store's work, not a cell's.
//
// Opt-in: SIZE_SWEEP_SOURCES=name=/abs/dir,... (scripts/size-sweep-fetch.sh prints it),
// SIZE_SWEEP_CALLS (default 5), SIZE_SWEEP_CPU_PROFILE=<prefix> for one profile per source.
func TestSizeSweepBenchmark(t *testing.T) {
	sources := os.Getenv("SIZE_SWEEP_SOURCES")
	if sources == "" {
		t.Skip("opt-in performance harness: set SIZE_SWEEP_SOURCES")
	}
	calls := 5
	if value := os.Getenv("SIZE_SWEEP_CALLS"); value != "" {
		var err error
		if calls, err = strconv.Atoi(value); err != nil || calls < 1 {
			t.Fatal("SIZE_SWEEP_CALLS must be a positive integer")
		}
	}
	for _, entry := range strings.Split(sources, ",") {
		name, dir, ok := strings.Cut(entry, "=")
		if !ok || name == "" || !filepath.IsAbs(dir) {
			t.Fatalf("bad SIZE_SWEEP_SOURCES entry %q, want name=/abs/dir", entry)
		}
		t.Run(name, func(t *testing.T) { sweepSource(t, name, dir, calls) })
	}
}

type sweepResult struct {
	Source        string    `json:"source"`
	Files         int       `json:"files"`
	Bytes         int64     `json:"bytes"`
	Calls         int       `json:"calls"`
	SeedMS        float64   `json:"seed_ms"`
	SeedError     string    `json:"seed_error,omitempty"`
	ReadMS        []float64 `json:"read_ms,omitempty"`
	WriteMS       []float64 `json:"write_ms,omitempty"`
	BashMS        []float64 `json:"bash_ms,omitempty"`
	ReadMedianMS  float64   `json:"read_median_ms"`
	WriteMedianMS float64   `json:"write_median_ms"`
	BashMedianMS  float64   `json:"bash_median_ms"`
	Generations   int       `json:"generations"`
	StoreBytes    int64     `json:"store_allocated_bytes"`
}

func sweepSource(t *testing.T, name, dir string, calls int) {
	files, bytes := sourceSize(t, dir)
	result := sweepResult{Source: name, Files: files, Bytes: bytes, Calls: calls}
	if prefix := os.Getenv("SIZE_SWEEP_CPU_PROFILE"); prefix != "" {
		file, err := os.Create(prefix + "-" + name + ".pprof")
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(file); err != nil {
			file.Close()
			t.Fatal(err)
		}
		defer func() { pprof.StopCPUProfile(); file.Close() }()
	}
	store := newSweepStore(t)
	started := time.Now()
	tip, err := store.Seed(openFixtureRoot(t, dir), RootAttestation{Quiescent: true})
	result.SeedMS = sinceMS(started)
	if err != nil {
		// An over-limit tree is a result, not a harness failure.
		result.SeedError = err.Error()
		logSweep(t, result)
		return
	}
	readPath := firstRegularFile(t, tip)
	ctx := context.Background()
	for i := 0; i < calls; i++ {
		id := fmt.Sprintf("sweep-%d", i)

		started = time.Now()
		if _, err := tip.Read(ctx, proposal("read-"+id), allow("read-"+id), readPath, 64<<10); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		result.ReadMS = append(result.ReadMS, sinceMS(started))

		started = time.Now()
		written, err := store.Write(ctx, tip, proposal("write-"+id), allow("write-"+id), "tbound-sweep-write.txt", []byte(id+"\n"))
		if err != nil || written.Generation == nil {
			t.Fatalf("write %d: %+v, %v", i, written, err)
		}
		result.WriteMS = append(result.WriteMS, sinceMS(started))
		tip = written.Generation

		started = time.Now()
		ran, err := store.RunBash(ctx, tip, lease("bash-"+id), allow("bash-"+id), sweepCommandSpec, sweepRunner(id))
		if err != nil || ran.Generation == nil {
			t.Fatalf("bash %d: %+v, %v", i, ran, err)
		}
		result.BashMS = append(result.BashMS, sinceMS(started))
		tip = ran.Generation
	}
	result.ReadMedianMS, result.WriteMedianMS, result.BashMedianMS = median(result.ReadMS), median(result.WriteMS), median(result.BashMS)
	result.Generations = len(store.generations)
	result.StoreBytes = allocatedBytes(t, store.rootPath)
	logSweep(t, result)
}

var sweepCommandSpec = CommandSpec{Executable: "/bin/sh", Args: []string{"-c", "echo sweep > tbound-sweep-bash.txt"},
	DeclaredOutputPaths: []string{"tbound-sweep-bash.txt"}}

// sweepRunner does the command's one write directly, so no process start is timed.
func sweepRunner(id string) CommandRunner {
	return CommandRunnerFunc(func(_ context.Context, view *CommandView, _ CommandSpec) (CommandResult, CommandSettlement, error) {
		if err := os.WriteFile(filepath.Join(view.testPath(), "tbound-sweep-bash.txt"), []byte(id+"\n"), 0o644); err != nil {
			return CommandResult{}, CommandSettlement{}, err
		}
		settled := CommandSettlement{LeaseID: "lease-bash-" + id, ViewID: view.ID(), ProcessScopeEmpty: true,
			WritersStopped: true, MountDetached: true, ExitObserved: true, EvidenceClass: "synthetic-test-fixture"}
		return CommandResult{ExitObserved: true}, settled, nil
	})
}

// newSweepStore is newE2EStore with an authority that allows every request: the sweep times
// the store's file work, and exact receipts for real trees would only re-derive the manifests.
func newSweepStore(t *testing.T) *Store {
	t.Helper()
	base := privateTestBase(t)
	storePath, auditPath := filepath.Join(base, "repository"), filepath.Join(base, "audit")
	for _, path := range []string{storePath, auditPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := audit.Open(filepath.Join(auditPath, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := Create(openFixtureRoot(t, storePath), Options{
		PolicyDigest:         allow("x").PolicyDigest,
		MetadataPolicyDigest: allow("x").MetadataPolicyDigest,
		Limits:               workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{
			ProfileDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			Authority:     workspace.XattrAuthorityBoundedNoHiddenTrusted,
		},
		Journal:            journal,
		AuthorizeOperation: func(OperationRequest) error { return nil },
		VerifyDecision:     func(delta.PolicyDecision, delta.TransitionBinding) error { return nil },
		VerifySettlement: func(settlement CommandSettlement, viewID, leaseID string) error {
			if settlement.ViewID != viewID || settlement.LeaseID != leaseID {
				return fmt.Errorf("settlement for another view or lease")
			}
			return nil
		},
	})
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(); _ = journal.Close() })
	return store
}

func firstRegularFile(t *testing.T, generation *Generation) string {
	t.Helper()
	for _, object := range generation.Manifest().Objects {
		if object.State.Exists && object.State.Type == delta.ObjectRegular {
			return object.Path
		}
	}
	t.Fatal("source has no regular file to read")
	return ""
}

func sourceSize(t *testing.T, dir string) (files int, bytes int64) {
	t.Helper()
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files++
		bytes += info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files, bytes
}

// allocatedBytes sums allocated blocks, so retained generations count at their real disk cost.
func allocatedBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var info syscall.Stat_t
		if err := syscall.Lstat(path, &info); err != nil {
			return err
		}
		total += info.Blocks * 512
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func sinceMS(started time.Time) float64 {
	return float64(time.Since(started).Microseconds()) / 1000
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[middle-1] + sorted[middle]) / 2
	}
	return sorted[middle]
}

func logSweep(t *testing.T, result sweepResult) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SIZE_SWEEP_RESULT %s", encoded)
}
