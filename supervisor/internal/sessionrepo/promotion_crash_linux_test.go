//go:build linux

package sessionrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

const promotionCrashPhase = "TBOUND_PROMOTION_CRASH_PHASE"
const promotionCrashRoot = "TBOUND_PROMOTION_CRASH_ROOT"
const promotionCrashExit = 73
const promotionCrashMarker = "promoted:g1;primary:unresolved\n"

func TestStoreProcessCrashAfterPromotionBeforeOutcome(t *testing.T) {
	if os.Getenv(promotionCrashPhase) != "" {
		promotionCrashChild(t)
		return
	}
	store, journal, primaryPath, _ := newE2EStore(t)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(source, "task.txt"), "before\n", 0o644)
	if _, err := store.Seed(openFixtureRoot(t, source), RootAttestation{Quiescent: true}); err != nil {
		t.Fatal(err)
	}
	seed, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	root := store.rootPath
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	promotionCrashProcess(t, "write", root, promotionCrashExit)
	markerPath := filepath.Join(filepath.Dir(root), "promotion.marker")
	marker, err := os.ReadFile(markerPath)
	if err != nil || string(marker) != promotionCrashMarker {
		t.Fatalf("crash marker=%q err=%v", marker, err)
	}
	primary, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatal(err)
	}
	admissionPath := filepath.Join(root, admissionJournalName)
	admissions, err := os.ReadFile(admissionPath)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := audit.Verify(bytes.NewReader(primary))
	if err != nil || len(trace.Effects) != 2 {
		t.Fatalf("primary trace=%+v err=%v", trace, err)
	}
	effect := trace.Effects[1]
	if trace.Effects[0].Outcome != "success" || trace.Effects[0].Unresolved || !effect.Unresolved || effect.Outcome != "" || effect.OutcomeSequence != 0 {
		t.Fatalf("unexpected crash effects: %+v", trace.Effects)
	}
	request, _, err := decodeOperationIntent(effect.Intent)
	if err != nil || request.Tool != "write" || request.InputGeneration != "g0" {
		t.Fatalf("crash intent=%+v err=%v", request, err)
	}
	admissionTrace, err := audit.Verify(bytes.NewReader(admissions))
	if err != nil || len(admissionTrace.Records) != 1 {
		t.Fatalf("admission trace=%+v err=%v", admissionTrace, err)
	}
	candidate, err := decodeAdmissionRecord(admissionTrace.Records[0].Event.Data)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := validateAdmissionRecord(candidate, seed.PolicyDigest, seed.MetadataPolicyDigest)
	if err != nil || outcome != "allow" || candidate.Request.EffectID != effect.ID || candidate.Output.Generation != "g1" || !sameManifest(candidate.Input, seed.Baseline.Manifest) {
		t.Fatalf("candidate admission=%+v outcome=%q err=%v", candidate, outcome, err)
	}
	if err := verifyAdmissionIntent(candidate, effect.Intent); err != nil {
		t.Fatal(err)
	}
	digest, err := delta.ComputeTreeDigest(candidate.Output)
	if err != nil || digest != candidate.Candidate.OutputTreeDigest {
		t.Fatalf("candidate digest=%q err=%v", digest, err)
	}
	g1 := openFixtureRoot(t, filepath.Join(root, "generations", "g1"))
	actual, err := workspace.Scan(g1, store.options.workspaceOptions("g1", true))
	if err != nil || actual.TreeDigest != digest || !sameManifest(actual.Manifest, candidate.Output) {
		t.Fatalf("actual promoted candidate=%+v err=%v", actual, err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "generations", "g1", "task.txt")); err != nil || string(got) != "after\n" {
		t.Fatalf("promoted bytes=%q err=%v", got, err)
	}
	// Keep only accepted seed generations; the candidate remains in admission
	// evidence, never invented as an approved ledger transition.
	seed.AuditJournal, seed.AdmissionJournal = primary, admissions
	seed.AuditRecordCount, seed.AdmissionRecordCount = uint64(len(trace.Records)), uint64(len(admissionTrace.Records))
	seed.AuditHeadHash, seed.AdmissionHeadHash = trace.Records[len(trace.Records)-1].Hash, admissionTrace.Records[0].Hash
	disposition, err := Reconstruct(seed)
	if err != nil || !disposition.Quarantined || len(disposition.Ledger) != 0 || len(disposition.Operations) != 0 {
		t.Fatalf("crash disposition=%+v err=%v", disposition, err)
	}
	promotionCrashProcess(t, "open", root, 0)
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "forbidden-recovery.marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh recovery dispatched a forbidden callback, or marker check failed: %v", err)
	}
	for path, before := range map[string][]byte{primaryPath: primary, admissionPath: admissions, markerPath: marker} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("fresh recovery changed %s: %v", filepath.Base(path), err)
		}
	}
}

func promotionCrashProcess(t *testing.T, phase, root string, wantExit int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestStoreProcessCrashAfterPromotionBeforeOutcome$")
	cmd.Env = []string{promotionCrashPhase + "=" + phase, promotionCrashRoot + "=" + root, "TMPDIR=" + os.TempDir()}
	output, err := cmd.CombinedOutput()
	if wantExit == 0 {
		if err != nil {
			t.Fatalf("recovery child: %v\n%s", err, output)
		}
		return
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != wantExit {
		t.Fatalf("crash child: %v want exit %d\n%s", err, wantExit, output)
	}
}

func promotionCrashChild(t *testing.T) {
	phase, root := os.Getenv(promotionCrashPhase), os.Getenv(promotionCrashRoot)
	if root == "" || (phase != "write" && phase != "open") {
		t.Fatal("invalid crash helper inputs")
	}
	base := filepath.Dir(root)
	path := filepath.Join(base, "audit", "journal.jsonl")
	var hook audit.LifecycleHook
	if phase == "write" {
		hook = func(boundary audit.EffectBoundary) error {
			if boundary != audit.EffectBoundaryAfterCallbackReturn {
				return nil
			}
			got, err := os.ReadFile(filepath.Join(root, "generations", "g1", "task.txt"))
			if err != nil || string(got) != "after\n" {
				return fmt.Errorf("promoted candidate missing: %v", err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			trace, err := audit.Verify(bytes.NewReader(raw))
			if err != nil || len(trace.Effects) != 2 || !trace.Effects[1].Unresolved {
				return fmt.Errorf("primary not unresolved: %v", err)
			}
			marker, err := os.OpenFile(filepath.Join(base, "promotion.marker"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			if _, err = marker.Write([]byte(promotionCrashMarker)); err != nil {
				marker.Close()
				return err
			}
			if err = marker.Sync(); err != nil {
				marker.Close()
				return err
			}
			if err = marker.Close(); err != nil {
				return err
			}
			directory, err := os.Open(base)
			if err != nil {
				return err
			}
			if err = directory.Sync(); err != nil {
				directory.Close()
				return err
			}
			if err = directory.Close(); err != nil {
				return err
			}
			os.Exit(promotionCrashExit)
			return nil
		}
	}
	journal, err := audit.OpenWithLifecycleHook(path, hook)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	authority := &e2eReceiptAuthority{requests: make(map[string]OperationRequest), bindings: make(map[string]delta.TransitionBinding)}
	options := Options{PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Limits: workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Complete: true},
		Journal:         journal, AuthorizeOperation: authority.authorize, VerifyDecision: authority.verifyDecision,
		VerifySettlement: func(CommandSettlement, string, string) error {
			return errors.New("write crash fixture must not run a command")
		}}
	if phase == "open" {
		forbidden := func() error {
			marker, err := os.OpenFile(filepath.Join(base, "forbidden-recovery.marker"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
			if err != nil {
				return err
			}
			_, writeErr := marker.WriteString("recovery callback dispatched\n")
			return errors.Join(errors.New("unresolved recovery must not dispatch callbacks"), writeErr, marker.Sync(), marker.Close())
		}
		options.AuthorizeOperation = func(OperationRequest) error { return forbidden() }
		options.VerifyDecision = func(delta.PolicyDecision, delta.TransitionBinding) error { return forbidden() }
	}
	store, err := Open(openFixtureRoot(t, root), options)
	if phase == "open" {
		if store != nil {
			store.Close()
		}
		if store != nil || !errors.Is(err, ErrQuarantined) {
			t.Fatalf("fresh Open=(%v,%v), want no usable Store and quarantine", store, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if len(store.generations) != 1 || store.generations[0].ID() != "g0" {
		t.Fatal("unexpected initial generation")
	}
	expectedPath := filepath.Join(base, "expected-g1")
	if err := os.Mkdir(expectedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(expectedPath, "task.txt"), "after\n", 0o644)
	expected, err := workspace.Scan(openFixtureRoot(t, expectedPath), options.workspaceOptions("g1", true))
	if err != nil {
		t.Fatal(err)
	}
	input := store.generations[0]
	operation, decision := proposal("promotion-crash"), allow("promotion-crash")
	request := OperationRequest{Tool: "write", Operation: operation, Decision: decision, InputGeneration: input.ID(), InputTreeDigest: input.TreeDigest(),
		ArgumentDigest: writeArgumentsDigestForTest("task.txt", []byte("after\n")), OutputScopePaths: []string{"task.txt"}}
	authority.grant(request)
	authority.grantBinding(decision.ID, exactBinding(input.snapshot, expected, request, 1))
	_, err = store.Write(context.Background(), input, operation, decision, "task.txt", []byte("after\n"))
	t.Fatalf("crash hook did not terminate write: %v", err)
}
