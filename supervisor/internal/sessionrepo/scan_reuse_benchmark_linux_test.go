//go:build linux

package sessionrepo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime/pprof"
	"sort"
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

// scanReuseProjection keeps only the fields that a fixed fixture determines. Effect, view, and
// lease IDs are random per Store and the journals are hash-chained over them, so two Stores never
// serialize alike. Cross-process comparison uses this projection; each run compares full reports.
type scanReuseProjection struct {
	Chain       delta.Result
	Generations []GenerationEvidence
	Transitions []scanReuseTransitionProjection
}

type scanReuseTransitionProjection struct {
	Sequence         uint64
	Tool             string
	InputGeneration  string
	OutputGeneration string
	InputTreeDigest  string
	OutputTreeDigest string
	Changes          []delta.ObjectChange
}

// TestScanReuseBenchmark times only the grouped evidence call, one group per timed window.
// Warm-up and every equality check run outside the windows. The version-specific adapter is the
// only changed input.
func TestScanReuseBenchmark(t *testing.T) {
	if os.Getenv("SCAN_REUSE_BENCHMARK") != "1" {
		t.Skip("opt-in performance harness: set SCAN_REUSE_BENCHMARK=1")
	}
	iterations := 20
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
			projectionDigest := scanReuseProjectionDigest(t, want)
			// Untimed warm-up group, checked like every timed group.
			warm, err := scanReuseGrouped(store)
			if err != nil {
				t.Fatalf("warm-up group: %v", err)
			}
			if !reflect.DeepEqual(warm, want) {
				t.Fatal("warm-up group differs from full reference")
			}
			// A separate profile run covers only the timed loop: it stops before the equality checks.
			stopCPUProfile := func() {}
			if prefix := os.Getenv("SCAN_REUSE_CPU_PROFILE"); prefix != "" {
				file, err := os.Create(prefix + "-" + fixture.name + ".pprof")
				if err != nil {
					t.Fatal(err)
				}
				if err := pprof.StartCPUProfile(file); err != nil {
					file.Close()
					t.Fatal(err)
				}
				stopCPUProfile = func() { pprof.StopCPUProfile(); file.Close() }
				defer stopCPUProfile()
			}
			groups := make([]scanReuseResult, iterations)
			groupNanos := make([]int64, iterations)
			for i := 0; i < iterations; i++ {
				started := time.Now()
				got, err := scanReuseGrouped(store)
				groupNanos[i] = time.Since(started).Nanoseconds()
				if err != nil {
					t.Fatalf("group %d: %v", i, err)
				}
				groups[i] = got
			}
			stopCPUProfile()
			// Equality is checked only after every timed window has closed.
			mismatches := 0
			for i := range groups {
				if !reflect.DeepEqual(groups[i], want) {
					mismatches++
				}
			}
			if mismatches != 0 {
				t.Fatalf("%d of %d timed groups differ from full reference", mismatches, iterations)
			}
			sorted := append([]int64(nil), groupNanos...)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
			median := sorted[len(sorted)/2]
			if len(sorted)%2 == 0 {
				median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
			}
			var total int64
			for _, nanos := range groupNanos {
				total += nanos
			}
			result := struct {
				Fixture          string `json:"fixture"`
				Files            int    `json:"files_per_generation"`
				Bytes            int    `json:"file_bytes"`
				Generations      int    `json:"retained_generations"`
				Iterations       int    `json:"successful_groups"`
				TimedNS          int64  `json:"timed_ns_total"`
				NSPerGroupMedian int64  `json:"ns_per_group_median"`
				NSPerGroupMin    int64  `json:"ns_per_group_min"`
				NSPerGroupMax    int64  `json:"ns_per_group_max"`
				Mismatches       int    `json:"mismatched_groups"`
				Projection       string `json:"projection_sha256"`
				Tip              string `json:"tip_tree_digest"`
			}{fixture.name, fixture.files, fixture.bytes, fixture.generations, iterations, total, median, sorted[0], sorted[len(sorted)-1], mismatches, projectionDigest, want.Chain.SealedTreeDigest}
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

// scanReuseProjectionDigest hashes the fixture-determined part of a reference result, so baseline
// and candidate processes can be compared with each other.
func scanReuseProjectionDigest(t *testing.T, want scanReuseResult) string {
	t.Helper()
	projection := scanReuseProjection{Chain: want.Chain, Generations: want.Bundle.Generations}
	for _, transition := range want.Artifact.ApprovedDeltaLedger {
		projection.Transitions = append(projection.Transitions, scanReuseTransitionProjection{
			Sequence:         transition.Sequence,
			Tool:             transition.Tool,
			InputGeneration:  transition.InputGeneration,
			OutputGeneration: transition.OutputGeneration,
			InputTreeDigest:  transition.InputTreeDigest,
			OutputTreeDigest: transition.OutputTreeDigest,
			Changes:          transition.Changes,
		})
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
