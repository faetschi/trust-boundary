//go:build linux

package gate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gowebpki/jcs"
	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/publication"
)

func TestInternalCommitGateBindsDurableSingleUseAuthority(t *testing.T) {
	journal := internalCommitJournal(t)
	commitGate, err := NewInternalCommitGate(journal)
	if err != nil {
		t.Fatal(err)
	}
	binding := internalCommitBinding(t)
	verified := 0
	authorization, err := commitGate.Authorize(context.Background(), binding, func(_ context.Context, got publication.CommitBinding) error {
		verified++
		if got != binding {
			return errors.New("binding changed before trusted verifier")
		}
		return nil
	})
	if err != nil || verified != 1 || authorization.BindingDigest != binding.BindingDigest || authorization.DecisionID == "" {
		t.Fatalf("authorization=%+v verified=%d err=%v", authorization, verified, err)
	}
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) != 1 || trace.Records[0].Event.Kind != "workspace_commit_authorized" {
		t.Fatalf("authorization was not durably audited: records=%+v err=%v", trace.Records, err)
	}
	if _, err := commitGate.Authorize(context.Background(), binding, func(context.Context, publication.CommitBinding) error { return nil }); !errors.Is(err, ErrInternalCommitReplay) {
		t.Fatalf("same commit binding was reusable: %v", err)
	}
	otherPublication := binding
	otherPublication.PublicationID = "different-publication"
	otherPublication.BindingDigest = ""
	otherPublication.BindingDigest = internalCommitDigest(t, otherPublication)
	if _, err := commitGate.Authorize(context.Background(), otherPublication, func(context.Context, publication.CommitBinding) error { return nil }); err != nil {
		t.Fatalf("distinct publication binding was incorrectly treated as replay: %v", err)
	}
}

func TestInternalCommitGateRejectsInvalidOrUnverifiedBindingBeforeAudit(t *testing.T) {
	journal := internalCommitJournal(t)
	commitGate, err := NewInternalCommitGate(journal)
	if err != nil {
		t.Fatal(err)
	}
	binding := internalCommitBinding(t)
	binding.SealedTreeDigest = "sha256:" + string(make([]byte, 64))
	if _, err := commitGate.Authorize(context.Background(), binding, func(context.Context, publication.CommitBinding) error { return nil }); !errors.Is(err, ErrInternalCommitDenied) {
		t.Fatalf("malformed binding accepted: %v", err)
	}
	binding = internalCommitBinding(t)
	if _, err := commitGate.Authorize(context.Background(), binding, func(context.Context, publication.CommitBinding) error { return errors.New("origin receipt denied") }); !errors.Is(err, ErrInternalCommitDenied) {
		t.Fatalf("unverified origin accepted: %v", err)
	}
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) != 0 {
		t.Fatalf("denied or malformed decision left durable authorization: records=%d err=%v", len(trace.Records), err)
	}
}

func internalCommitJournal(t *testing.T) *audit.Journal {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "journal")
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	return journal
}

func internalCommitBinding(t *testing.T) publication.CommitBinding {
	t.Helper()
	binding := publication.CommitBinding{
		WorkflowID: "workflow-test", OwnerEpoch: 1, PublicationID: "publication-test", OriginProposalID: "proposal-test",
		ChainDigest: internalTestDigest("chain"), DeltaDigest: internalTestDigest("delta"),
		BaselineTreeDigest: internalTestDigest("baseline"), SealedTreeDigest: internalTestDigest("sealed"),
		PolicyDigest: internalTestDigest("policy"), MetadataPolicyDigest: internalTestDigest("metadata"),
	}
	binding.BindingDigest = internalCommitDigest(t, binding)
	return binding
}

func internalCommitDigest(t *testing.T, binding publication.CommitBinding) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		WorkflowID           string `json:"workflow_id"`
		OwnerEpoch           uint64 `json:"owner_epoch"`
		PublicationID        string `json:"publication_id"`
		OriginProposalID     string `json:"origin_proposal_id"`
		ChainDigest          string `json:"chain_digest"`
		DeltaDigest          string `json:"delta_digest"`
		BaselineTreeDigest   string `json:"baseline_tree_digest"`
		SealedTreeDigest     string `json:"sealed_tree_digest"`
		PolicyDigest         string `json:"policy_digest"`
		MetadataPolicyDigest string `json:"metadata_policy_digest"`
	}{binding.WorkflowID, binding.OwnerEpoch, binding.PublicationID, binding.OriginProposalID, binding.ChainDigest,
		binding.DeltaDigest, binding.BaselineTreeDigest, binding.SealedTreeDigest, binding.PolicyDigest, binding.MetadataPolicyDigest})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func internalTestDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
