//go:build linux

package sessionrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/delta"
)

// Contract (audit #12, owner decision: document and pin current behaviour).
// The research P-half no-reap/v1 delta may be admitted LIVE by RunBash when the caller explicitly
// authorizes tolerated survivors (VerifySettlement). Replay, Reconstruct and Open refuse such a
// bundle on purpose: a run that used no-reap is deliberately unreconstructable. The serialized
// SurvivorsTolerated flag is never authority. No survivor process is launched; the settlement is
// synthetic and the verifier is an explicit test authorization.

// runToleratedBash runs one synthetic Bash effect whose settlement claims tolerated survivors.
func runToleratedBash(t *testing.T, settlement func(op delta.OperationIdentity, viewID string) CommandSettlement) (*Store, MutationResult, error) {
	t.Helper()
	return runToleratedBashVerified(t, settlement, authorizeTolerated)
}

// authorizeTolerated is the explicit authorization of tolerated survivors; the default fixture
// verifier refuses them.
func authorizeTolerated(s CommandSettlement, view, leaseID string) error {
	if !s.Admitted() || !s.SurvivorsTolerated || s.ViewID != view || s.LeaseID != leaseID {
		return fmt.Errorf("settlement is not an authorized tolerated-survivor settlement")
	}
	return nil
}

func runToleratedBashVerified(t *testing.T, settlement func(op delta.OperationIdentity, viewID string) CommandSettlement,
	verify func(CommandSettlement, string, string) error) (*Store, MutationResult, error) {
	t.Helper()
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureDir(t, filepath.Join(sourcePath, "build"), 0o755)
	writeFixtureFile(t, filepath.Join(sourcePath, "README.md"), "baseline text\n", 0o644)
	writeFixtureFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}
	op, decision := lease("no-reap"), allow("no-reap")
	spec := CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "synthetic"}, DeclaredOutputPaths: []string{"build/result.txt"}}
	viewID := reserveTestViewID(t, store)
	args, err := commandArgumentsDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := commandExecutionContextDigest(viewID, g0.TreeDigest(), op.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	request := OperationRequest{Tool: "bash", Operation: op, Decision: decision, InputGeneration: g0.ID(),
		InputTreeDigest: g0.TreeDigest(), ArgumentDigest: args, ViewID: viewID,
		OutputScopePaths: spec.DeclaredOutputPaths, ExecutionContextDigest: execution}
	authority.grant(request)
	expected := fixtureSnapshot(t, "g1", "baseline text\n", "changed result\n")
	authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expected, request, 1))
	store.options.VerifySettlement = verify
	result, err := store.RunBash(context.Background(), g0, op, decision, spec, CommandRunnerFunc(
		func(_ context.Context, v *CommandView, _ CommandSpec) (CommandResult, CommandSettlement, error) {
			if err := os.WriteFile(filepath.Join(v.testPath(), "build", "result.txt"), []byte("changed result\n"), 0o644); err != nil {
				return CommandResult{}, CommandSettlement{}, err
			}
			return CommandResult{ExitObserved: true}, settlement(op, v.ID()), nil
		}))
	return store, result, err
}

func toleratedSettlement(op delta.OperationIdentity, viewID string) CommandSettlement {
	return CommandSettlement{LeaseID: op.LeaseID, ViewID: viewID, ExitObserved: true,
		SurvivorsTolerated: true, EvidenceClass: "synthetic-no-reap"}
}

func TestNoReapToleratedSettlementIsAdmittedLive(t *testing.T) {
	_, result, err := runToleratedBash(t, toleratedSettlement)
	if err != nil || result.Generation == nil {
		t.Fatalf("live RunBash refused an authorized tolerated-survivor settlement: result=%+v err=%v", result, err)
	}
}

func TestNoReapToleratedFlagIsNotAuthorityLive(t *testing.T) {
	// SurvivorsTolerated with any "closed" boolean set is not the admitted no-reap shape.
	_, result, err := runToleratedBash(t, func(op delta.OperationIdentity, viewID string) CommandSettlement {
		s := toleratedSettlement(op, viewID)
		s.ProcessScopeEmpty = true
		return s
	})
	if !errors.Is(err, ErrInvalidSettlement) || result.Generation != nil {
		t.Fatalf("live RunBash trusted SurvivorsTolerated on a half-closed settlement: result=%+v err=%v", result, err)
	}
}

// A well-formed tolerated-survivor settlement is still not authority: the caller's verifier decides.
func TestNoReapToleratedSettlementIsRefusedWhenTheVerifierRejects(t *testing.T) {
	errRejected := errors.New("verifier rejects the tolerated-survivor settlement")
	store, result, err := runToleratedBashVerified(t, toleratedSettlement,
		func(CommandSettlement, string, string) error { return errRejected })
	if !errors.Is(err, errRejected) || result.Generation != nil || len(store.generations) != 1 {
		t.Fatalf("rejected tolerated settlement: generations=%d result=%+v err=%v", len(store.generations), result, err)
	}
}

func TestNoReapEvidenceBundleIsNotReconstructable(t *testing.T) {
	store, _, err := runToleratedBash(t, toleratedSettlement)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatalf("a tolerated-survivor run must still emit a bundle: %v", err)
	}
	if _, err := Reconstruct(bundle); !errors.Is(err, ErrInvalidEvidenceBundle) {
		t.Fatalf("Reconstruct = %v, want %v", err, ErrInvalidEvidenceBundle)
	}
}

func TestNoReapRepositoryDoesNotReopen(t *testing.T) {
	for name, open := range map[string]func(*os.File, Options) (*Store, error){
		"Open":                Open,
		"OpenRecoveryPreview": OpenRecoveryPreview,
	} {
		t.Run(name, func(t *testing.T) {
			store, _, err := runToleratedBash(t, toleratedSettlement)
			if err != nil {
				t.Fatal(err)
			}
			rootPath, options := store.rootPath, store.options
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := open(openFixtureRoot(t, rootPath), options)
			if err == nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, ErrQuarantined) {
				t.Fatalf("%s of a committed no-reap effect = %v, want %v", name, err, ErrQuarantined)
			}
		})
	}
}
