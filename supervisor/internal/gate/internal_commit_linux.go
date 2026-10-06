//go:build linux

package gate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/publication"
)

var (
	ErrInternalCommitDenied = errors.New("internal workspace commit was not authorized")
	ErrInternalCommitReplay = errors.New("internal workspace commit authority was already consumed")
)

// InternalCommitGate is a supervisor-only authorization path for workspace
// publication. It is intentionally separate from Policy and the four
// model-visible tools. The caller supplies an independently trusted verifier
// over the evidence that produced the binding; this gate supplies durable
// binding validation and replay rejection, not external policy authenticity.
type InternalCommitGate struct {
	journal *audit.Journal
	mu      *sync.Mutex
}

var internalCommitJournalLocks sync.Map // map[*audit.Journal]*sync.Mutex

type InternalCommitVerifier func(context.Context, publication.CommitBinding) error

type internalCommitRecord struct {
	Binding    publication.CommitBinding `json:"binding"`
	DecisionID string                    `json:"decision_id"`
	ReplayKey  string                    `json:"replay_key"`
}

func NewInternalCommitGate(journal *audit.Journal) (*InternalCommitGate, error) {
	if journal == nil {
		return nil, errors.New("internal workspace-commit gate requires a durable audit journal")
	}
	lock, _ := internalCommitJournalLocks.LoadOrStore(journal, &sync.Mutex{})
	return &InternalCommitGate{journal: journal, mu: lock.(*sync.Mutex)}, nil
}

// Authorize binds one trusted workspace_commit decision to the complete
// publication binding and syncs it before returning. The publication package
// records its separate opaque one-use token before any live-tree mutation.
func (g *InternalCommitGate) Authorize(ctx context.Context, binding publication.CommitBinding, verify InternalCommitVerifier) (publication.Authorization, error) {
	if g == nil || g.journal == nil || verify == nil || ctx == nil || ctx.Err() != nil || !validInternalBinding(binding) {
		return publication.Authorization{}, ErrInternalCommitDenied
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := verify(ctx, binding); err != nil {
		return publication.Authorization{}, fmt.Errorf("%w: trusted binding verification: %v", ErrInternalCommitDenied, err)
	}
	replayKey := internalCommitReplayKey(binding)
	decisionID := "workspace-commit-" + strings.TrimPrefix(binding.BindingDigest, "sha256:")
	trace, err := g.journal.Trace()
	if err != nil {
		return publication.Authorization{}, fmt.Errorf("verify internal commit journal: %w", err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind != "workspace_commit_authorized" {
			continue
		}
		var prior internalCommitRecord
		if err := decodeCommitRecord(record.Event.Data, &prior); err != nil {
			return publication.Authorization{}, fmt.Errorf("decode internal commit record: %w", err)
		}
		if record.Event.ID != prior.DecisionID {
			return publication.Authorization{}, errors.New("internal commit audit event ID differs from its decision")
		}
		if prior.ReplayKey == replayKey || prior.Binding.PublicationID == binding.PublicationID || prior.DecisionID == decisionID {
			return publication.Authorization{}, ErrInternalCommitReplay
		}
	}
	encoded, err := json.Marshal(internalCommitRecord{Binding: binding, DecisionID: decisionID, ReplayKey: replayKey})
	if err != nil {
		return publication.Authorization{}, err
	}
	if _, err := g.journal.Append(audit.Event{Kind: "workspace_commit_authorized", ID: decisionID, Data: encoded}); err != nil {
		return publication.Authorization{}, fmt.Errorf("durably record internal workspace commit: %w", err)
	}
	return publication.Authorization{DecisionID: decisionID, BindingDigest: binding.BindingDigest}, nil
}

func validInternalBinding(binding publication.CommitBinding) bool {
	if !internalIdentity(binding.WorkflowID) || binding.OwnerEpoch == 0 || !internalIdentity(binding.PublicationID) ||
		!internalIdentity(binding.OriginProposalID) || !internalDigest(binding.ChainDigest) || !internalDigest(binding.DeltaDigest) ||
		!internalTreeDigest(binding.BaselineTreeDigest) || !internalTreeDigest(binding.SealedTreeDigest) ||
		!internalDigest(binding.PolicyDigest) || !internalDigest(binding.MetadataPolicyDigest) || !internalDigest(binding.BindingDigest) {
		return false
	}
	copy := binding
	copy.BindingDigest = ""
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
	}{copy.WorkflowID, copy.OwnerEpoch, copy.PublicationID, copy.OriginProposalID, copy.ChainDigest,
		copy.DeltaDigest, copy.BaselineTreeDigest, copy.SealedTreeDigest, copy.PolicyDigest, copy.MetadataPolicyDigest})
	if err != nil {
		return false
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(canonical)
	return binding.BindingDigest == "sha256:"+hex.EncodeToString(sum[:])
}

func internalTreeDigest(value string) bool {
	const treePrefix = "tbound-tree-jcs-rfc8785/v1:sha256:"
	if strings.HasPrefix(value, treePrefix) {
		return internalDigest("sha256:" + strings.TrimPrefix(value, treePrefix))
	}
	return internalDigest(value)
}

func internalCommitReplayKey(binding publication.CommitBinding) string {
	encoded, _ := json.Marshal([]string{binding.WorkflowID, binding.PublicationID, binding.OriginProposalID,
		binding.ChainDigest, binding.BaselineTreeDigest, binding.SealedTreeDigest})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func internalIdentity(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func internalDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	encoded := strings.TrimPrefix(value, "sha256:")
	if strings.ToLower(encoded) != encoded {
		return false
	}
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == sha256.Size
}

func decodeCommitRecord(data []byte, target *internalCommitRecord) error {
	if len(data) == 0 || len(data) > 1<<20 {
		return errors.New("internal commit record is out of bounds")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("internal commit record has trailing data")
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(canonical, data) {
		return errors.New("internal commit record is not canonical JSON")
	}
	if !validInternalBinding(target.Binding) || target.DecisionID != "workspace-commit-"+strings.TrimPrefix(target.Binding.BindingDigest, "sha256:") ||
		target.ReplayKey != internalCommitReplayKey(target.Binding) {
		return errors.New("internal commit record does not bind a valid single-use decision")
	}
	return nil
}
