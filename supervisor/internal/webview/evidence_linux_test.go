//go:build linux

package webview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

func TestGenerationProjectionReconstructsAndRejectsTamperedSessionrepoBundle(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no private home directory available for Linux audit fixture")
	}
	base, err := os.MkdirTemp(home, ".tbound-webview-evidence-")
	if err != nil {
		t.Skipf("no private evidence fixture directory available: %v", err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		_ = os.RemoveAll(base)
		t.Skipf("cannot protect Linux fixture directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	repositoryPath := filepath.Join(base, "repository")
	auditPath := filepath.Join(base, "audit")
	sourcePath := filepath.Join(base, "source")
	for _, path := range []string{repositoryPath, auditPath, sourcePath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "fixture.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	journal, err := audit.Open(filepath.Join(auditPath, "journal.jsonl"))
	if err != nil {
		t.Skipf("Linux audit fixture unavailable on this filesystem: %v", err)
	}
	root, err := os.Open(repositoryPath)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	options := sessionrepo.Options{
		PolicyDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Limits:               workspace.DefaultLimits(),
		XattrVisibility:      workspace.XattrVisibilityAttestation{ProfileDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Complete: true},
		Journal:              journal,
		AuthorizeOperation:   func(sessionrepo.OperationRequest) error { return nil },
		VerifyDecision:       func(delta.PolicyDecision, delta.TransitionBinding) error { return nil },
		VerifySettlement:     func(sessionrepo.CommandSettlement, string, string) error { return nil },
	}
	store, err := sessionrepo.Create(root, options)
	if err != nil {
		_ = root.Close()
		_ = journal.Close()
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = store.Close()
			_ = journal.Close()
		}
		_ = root.Close()
	})
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true}); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true

	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := reconstructGenerationProjection(encoded)
	if err != nil {
		t.Fatalf("project reconstructed sessionrepo evidence: %v", err)
	}
	if projection.Status != "reconstructed" || projection.Baseline != "g0" || projection.Sealed != "g0" || projection.Quarantined || len(projection.Generations) != 1 || projection.Generations[0] != "g0" {
		t.Fatalf("unexpected bounded generation projection: %+v", projection)
	}
	if !strings.Contains(strings.Join(projection.Limitations, ";"), "not an authorization verdict") {
		t.Fatalf("projection omitted its scope limitation: %+v", projection.Limitations)
	}

	bundle.AuditHeadHash = "sha256:tampered"
	tampered, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	projection, err = reconstructGenerationProjection(tampered)
	if err == nil || projection.Status != "" {
		t.Fatalf("tampered bundle was projected as valid: projection=%+v err=%v", projection, err)
	}
}
