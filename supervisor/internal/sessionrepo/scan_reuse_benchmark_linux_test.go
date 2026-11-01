//go:build linux

package sessionrepo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/delta"
)

type scanReuseResult struct {
	Chain    delta.Result
	Artifact EvidenceArtifact
	Bundle   EvidenceBundle
}

// TestScanReuseBenchmark measures warm, serial grouped evidence requests, not
// fixture creation. The version-specific adapter is the only changed input.
func TestScanReuseBenchmark(t *testing.T) {
	if os.Getenv("SCAN_REUSE_BENCHMARK") != "1" {
		t.Skip("opt-in performance harness: set SCAN_REUSE_BENCHMARK=1")
	}
	iterations := 3
	if value := os.Getenv("SCAN_REUSE_ITERATIONS"); value != "" {
		var err error
		iterations, err = strconv.Atoi(value)
		if err != nil || iterations < 1 {
			t.Fatal("SCAN_REUSE_ITERATIONS must be a positive integer")
		}
	}
	for _, fixture := range []struct {
		name                      string
		files, bytes, generations int
	}{{"small", 8, 4096, 5}, {"many_file", 128, 65536, 9}} {
		t.Run(fixture.name, func(t *testing.T) {
			// Fail rather than silently accepting the fixture helper's skip path.
			home, err := os.UserHomeDir()
			if err != nil || !ancestorsArePrivate(home) {
				t.Fatal("benchmark requires a private Linux home for the audit fixture")
			}
			store, _, _, authority := newE2EStore(t)
			files := make([]writeFixtureFileSpec, fixture.files)
			for i := range files {
				files[i] = writeFixtureFileSpec{path: fmt.Sprintf("file-%04d.txt", i), content: strings.Repeat(string(rune('a'+i%26)), fixture.bytes), mode: 0o644}
			}
			source := filepath.Join(t.TempDir(), "source")
			buildWriteFixtureTree(t, source, files)
			generation, err := store.Seed(openFixtureRoot(t, source), RootAttestation{Quiescent: true})
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i < fixture.generations; i++ {
				files[0].content = fmt.Sprintf("%08d", i) + files[0].content[8:]
				expected := expectedWriteSnapshot(t, fmt.Sprintf("g%d", i), files)
				generation = writeAndAssertMode(t, store, generation, authority, fmt.Sprintf("scan-%d", i), files[0].path, []byte(files[0].content), expected, uint64(i), 0o644)
			}
			want := scanReuseReference(t, store)
			if want.Chain.TransitionCount != uint64(fixture.generations-1) || want.Chain.SealedGeneration != generation.ID() || len(want.Bundle.Generations) != fixture.generations || len(want.Bundle.AuditJournal) == 0 {
				t.Fatal("fixture did not retain a successful nonempty transition chain")
			}
			if disposition, err := Reconstruct(want.Bundle); err != nil || disposition.Quarantined {
				t.Fatalf("fixture reconstruction: %+v, %v", disposition, err)
			}
			// A separate profile run includes only the measured loop in CPU samples.
			if prefix := os.Getenv("SCAN_REUSE_CPU_PROFILE"); prefix != "" {
				file, err := os.Create(prefix + "-" + fixture.name + ".pprof")
				if err != nil {
					t.Fatal(err)
				}
				if err := pprof.StartCPUProfile(file); err != nil {
					file.Close()
					t.Fatal(err)
				}
				defer func() { pprof.StopCPUProfile(); file.Close() }()
			}
			started := time.Now()
			for i := 0; i < iterations; i++ {
				got, err := scanReuseGrouped(store)
				if err != nil {
					t.Fatalf("group %d: %v", i, err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("group %d differs from full reference", i)
				}
			}
			elapsed := time.Since(started)
			result := struct {
				Fixture     string `json:"fixture"`
				Files       int    `json:"files_per_generation"`
				Bytes       int    `json:"file_bytes"`
				Generations int    `json:"retained_generations"`
				Iterations  int    `json:"successful_groups"`
				Elapsed     int64  `json:"elapsed_ns"`
				NSPerGroup  int64  `json:"ns_per_group"`
				Errors      int    `json:"errors"`
				Tip         string `json:"tip_tree_digest"`
			}{fixture.name, fixture.files, fixture.bytes, fixture.generations, iterations, elapsed.Nanoseconds(), elapsed.Nanoseconds() / int64(iterations), 0, want.Chain.SealedTreeDigest}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("SCAN_REUSE_RESULT %s", encoded)
		})
	}
}

func scanReuseReference(t *testing.T, store *Store) scanReuseResult {
	t.Helper()
	chain, err := store.Verify()
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Evidence()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chain, artifact.ValidatedChain) || !reflect.DeepEqual(chain, bundle.ValidatedChain) {
		t.Fatal("reference chain disagrees with evidence")
	}
	return scanReuseResult{chain, artifact, bundle}
}
