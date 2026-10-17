//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/drainproof"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workflowrecovery"
)

const workflowCatalogSchema = "tbound-pi-workflow-catalog/v2"

var ErrPiWorkflowCatalog = errors.New("host-owned Pi workflow catalog is missing, corrupt, or mismatched")

type productionWorkflowAdmission struct {
	Mode             string // fresh or resume; populated only by the host catalog.
	Entry            piServiceWorkflowEntry
	Catalog          *piServiceWorkflowCatalog
	Owner            workflowrecovery.Owner
	Root             workflowrecovery.TrustedRoot
	RootCloser       interface{ Close() error }
	OwnerCloser      interface{ Close() error }
	CandidateEpoch   uint64
	OwnerIncarnation workflowrecovery.OwnerIncarnation
	Checkpoint       workflowrecovery.Checkpoint
	CheckpointHead   string
	Ledger           *workflowrecovery.Ledger
}

type piServiceWorkflowOwnerScope struct {
	AdmissionHash       string
	WorkflowID          string
	OwnerEpoch          uint64
	Incarnation         workflowrecovery.OwnerIncarnation
	ScopeName           string
	ScopePath           string
	ScopeIdentityDigest string
	Drained             bool
	Unexposed           bool
}

type piServiceWorkflowEntry struct {
	SchemaVersion           string                              `json:"schema_version"`
	HostAdmissionID         string                              `json:"host_admission_id"`
	AdmissionRecordHash     string                              `json:"admission_record_hash"`
	AdmissionSequence       uint64                              `json:"admission_sequence"`
	PeerUID                 int                                 `json:"peer_uid"`
	PeerGID                 int                                 `json:"peer_gid"`
	PeerPID                 int                                 `json:"peer_pid"`
	RequestID               string                              `json:"request_id"`
	TaskID                  string                              `json:"task_id"`
	PayloadDigest           string                              `json:"payload_digest"`
	ServiceConfigDigest     string                              `json:"service_config_digest"`
	ServiceRegistrationID   string                              `json:"service_registration_id"`
	WorkflowID              string                              `json:"workflow_id"`
	RunID                   string                              `json:"run_id"`
	SessionID               string                              `json:"session_id"`
	ConversationID          string                              `json:"conversation_id"`
	OwnerDBPath             string                              `json:"owner_db_path"`
	StoreRootPath           string                              `json:"store_root_path"`
	AdminRootPath           string                              `json:"admin_root_path"`
	StoreJournalPath        string                              `json:"store_journal_path"`
	ProviderJournalPath     string                              `json:"provider_journal_path"`
	CommandJournalPath      string                              `json:"command_journal_path"`
	TaskPrompt              string                              `json:"task_prompt"`
	TaskDigest              string                              `json:"task_digest"`
	PromptDigest            string                              `json:"prompt_digest"`
	BashOutputPaths         []string                            `json:"bash_output_paths"`
	Limits                  workflowrecovery.Limits             `json:"limits"`
	ReservedAt              time.Time                           `json:"reserved_at"`
	FreshAuthorizationUsed  bool                                `json:"fresh_authorization_used"`
	RootIdentityDigest      string                              `json:"root_identity_digest,omitempty"`
	OwnerStoreIdentity      workflowrecovery.OwnerStoreIdentity `json:"owner_store_identity,omitempty"`
	OwnerIncarnation        workflowrecovery.OwnerIncarnation   `json:"owner_incarnation,omitempty"`
	InitialCheckpointDigest string                              `json:"initial_checkpoint_digest,omitempty"`
	InitialOwnerEpoch       uint64                              `json:"initial_owner_epoch,omitempty"`
	CheckpointBound         bool                                `json:"checkpoint_bound"`
	LatestCheckpointDigest  string                              `json:"latest_checkpoint_digest,omitempty"`
	Terminal                bool                                `json:"terminal"`
	TerminalRunID           string                              `json:"-"`
	TerminalProofDigest     string                              `json:"-"`
	TerminalProofSequence   uint64                              `json:"-"`
	TerminalTransactionID   string                              `json:"-"`
}

type piServiceWorkflowEvent struct {
	SchemaVersion            string                              `json:"schema_version"`
	AdmissionHash            string                              `json:"admission_hash"`
	Entry                    piServiceWorkflowEntry              `json:"entry,omitempty"`
	OwnerEpoch               uint64                              `json:"owner_epoch,omitempty"`
	Digest                   string                              `json:"digest,omitempty"`
	RootIdentity             string                              `json:"root_identity,omitempty"`
	StoreIdentity            workflowrecovery.OwnerStoreIdentity `json:"store_identity,omitempty"`
	Incarnation              workflowrecovery.OwnerIncarnation   `json:"incarnation,omitempty"`
	RunID                    string                              `json:"run_id,omitempty"`
	TCBProofDigest           string                              `json:"tcb_proof_digest,omitempty"`
	TCBProofSequence         uint64                              `json:"tcb_proof_sequence,omitempty"`
	PublicationTransactionID string                              `json:"publication_transaction_id,omitempty"`
	WorkflowID               string                              `json:"workflow_id,omitempty"`
	ScopeName                string                              `json:"scope_name,omitempty"`
	ScopePath                string                              `json:"scope_path,omitempty"`
	ScopeIdentityDigest      string                              `json:"scope_identity_digest,omitempty"`
	DrainProofDigest         string                              `json:"drain_proof_digest,omitempty"`
	Unexposed                bool                                `json:"unexposed,omitempty"`
}

type piServiceWorkflowCatalog struct {
	mu                sync.Mutex
	journal           *audit.Journal
	entries           map[string]piServiceWorkflowEntry
	byHostAdmissionID map[string]string
	ownerScopes       map[string]piServiceWorkflowOwnerScope
	// ownerStoreRoot is the directory the owner DBs must sit in. The loader sets
	// it to piServiceOwnerStoreRoot. It is an unexported test dependency, not
	// configuration: only the loader and tests set it.
	ownerStoreRoot string
}

// ownerRoot is the owner-DB directory this catalog verifies against. A catalog
// built as a struct literal, as older fixtures do, keeps the production value.
func (c *piServiceWorkflowCatalog) ownerRoot() string {
	if c == nil || c.ownerStoreRoot == "" {
		return piServiceOwnerStoreRoot
	}
	return c.ownerStoreRoot
}

func openPiServiceWorkflowCatalog(path string) (*piServiceWorkflowCatalog, error) {
	if path != piServiceWorkflowCatalogPath {
		return nil, ErrPiWorkflowCatalog
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm() != 0o700 {
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != uint32(os.Geteuid()) || parentStat.Gid != uint32(os.Getegid()) {
		return nil, ErrPiWorkflowCatalog
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, ErrPiWorkflowCatalog
	}
	journal, err := audit.Open(path)
	if err != nil {
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	return loadPiServiceWorkflowCatalog(journal)
}

// loadPiServiceWorkflowCatalog replays a catalog journal into memory and takes
// ownership of it: on refusal it closes the journal. The production opener calls
// it only after the fixed-path and ownership gates. Tests call it on a temporary
// journal to reopen the durable catalog; the replay rules are the same either way.
func loadPiServiceWorkflowCatalog(journal *audit.Journal) (*piServiceWorkflowCatalog, error) {
	catalog := &piServiceWorkflowCatalog{journal: journal, entries: make(map[string]piServiceWorkflowEntry),
		byHostAdmissionID: make(map[string]string), ownerScopes: make(map[string]piServiceWorkflowOwnerScope),
		ownerStoreRoot: piServiceOwnerStoreRoot}
	trace, err := journal.Trace()
	if err != nil {
		_ = journal.Close()
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	for _, record := range trace.Records {
		var event piServiceWorkflowEvent
		if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil || event.SchemaVersion != workflowCatalogSchema ||
			!validBareWorkflowDigest(event.AdmissionHash) {
			_ = journal.Close()
			return nil, errors.Join(ErrPiWorkflowCatalog, err)
		}
		entry, exists := catalog.entries[event.AdmissionHash]
		switch record.Event.Kind {
		case "pi_workflow_admission_reserved":
			if record.Event.ID != workflowCatalogEventID("reserved", event.AdmissionHash) || exists ||
				event.Entry.AdmissionRecordHash != event.AdmissionHash || !validWorkflowCatalogEntry(event.Entry) {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			if _, duplicate := catalog.byHostAdmissionID[event.Entry.HostAdmissionID]; duplicate {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			catalog.entries[event.AdmissionHash] = event.Entry
			catalog.byHostAdmissionID[event.Entry.HostAdmissionID] = event.AdmissionHash
		case "pi_workflow_fresh_authorized":
			if record.Event.ID != workflowCatalogEventID("fresh", event.AdmissionHash) || !exists || entry.FreshAuthorizationUsed ||
				event.OwnerEpoch != 1 || !validBareWorkflowDigest(event.Digest) || !validBareWorkflowDigest(event.RootIdentity) ||
				!validBareWorkflowDigest(event.StoreIdentity.Digest) || event.StoreIdentity.Path != entry.OwnerDBPath ||
				!validWorkflowOwnerIncarnation(event.Incarnation) {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			entry.FreshAuthorizationUsed = true
			entry.InitialOwnerEpoch = event.OwnerEpoch
			entry.InitialCheckpointDigest = event.Digest
			entry.RootIdentityDigest = event.RootIdentity
			entry.OwnerStoreIdentity = event.StoreIdentity
			entry.OwnerIncarnation = event.Incarnation
			catalog.entries[event.AdmissionHash] = entry
		case "pi_workflow_checkpoint_bound":
			if record.Event.ID != workflowCatalogEventID("checkpoint", event.AdmissionHash) || !exists ||
				!entry.FreshAuthorizationUsed || entry.CheckpointBound || event.Digest != entry.InitialCheckpointDigest {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			entry.CheckpointBound, entry.LatestCheckpointDigest = true, event.Digest
			catalog.entries[event.AdmissionHash] = entry
		case "pi_workflow_checkpoint_advanced":
			if record.Event.ID != workflowCatalogEventID("advance", event.AdmissionHash) || !exists ||
				!entry.CheckpointBound || !validBareWorkflowDigest(event.Digest) || event.Digest == entry.LatestCheckpointDigest {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			entry.LatestCheckpointDigest = event.Digest
			catalog.entries[event.AdmissionHash] = entry
		case "pi_workflow_terminal":
			if record.Event.ID != workflowCatalogEventID("terminal", event.AdmissionHash) || !exists ||
				!entry.CheckpointBound || entry.Terminal {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			hasTerminalLink := event.RunID != "" || event.TCBProofDigest != "" || event.TCBProofSequence != 0 ||
				event.PublicationTransactionID != ""
			if hasTerminalLink && (!validIdentityText(event.RunID) || !validDigestText(event.TCBProofDigest) ||
				event.TCBProofSequence == 0 || !boundedProductionIdentity(event.PublicationTransactionID, productionRequestMaxBytes) ||
				!validBareWorkflowDigest(event.Digest)) || !hasTerminalLink && event.Digest != entry.LatestCheckpointDigest {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			entry.Terminal = true
			entry.TerminalRunID, entry.TerminalProofDigest, entry.TerminalProofSequence =
				event.RunID, event.TCBProofDigest, event.TCBProofSequence
			entry.TerminalTransactionID = event.PublicationTransactionID
			if hasTerminalLink {
				entry.LatestCheckpointDigest = event.Digest
			}
			catalog.entries[event.AdmissionHash] = entry
		case "pi_workflow_owner_scope_bound":
			key := workflowOwnerEpochKey(event.WorkflowID, event.OwnerEpoch)
			if record.Event.ID != workflowOwnerScopeEventID("bound", event.WorkflowID, event.OwnerEpoch) || !exists ||
				entry.WorkflowID != event.WorkflowID || !entry.CheckpointBound || event.OwnerEpoch == 0 ||
				!validWorkflowOwnerIncarnation(event.Incarnation) || event.ScopeName != workflowOwnerScopeName(event.WorkflowID, event.OwnerEpoch) ||
				filepath.Clean(event.ScopePath) != event.ScopePath || filepath.Base(event.ScopePath) != event.ScopeName ||
				!validDigestText(event.ScopeIdentityDigest) {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			if _, duplicate := catalog.ownerScopes[key]; duplicate {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			catalog.ownerScopes[key] = piServiceWorkflowOwnerScope{AdmissionHash: event.AdmissionHash,
				WorkflowID: event.WorkflowID, OwnerEpoch: event.OwnerEpoch, Incarnation: event.Incarnation,
				ScopeName: event.ScopeName, ScopePath: event.ScopePath, ScopeIdentityDigest: event.ScopeIdentityDigest}
		case "pi_workflow_owner_scope_drained":
			key := workflowOwnerEpochKey(event.WorkflowID, event.OwnerEpoch)
			state, bound := catalog.ownerScopes[key]
			if record.Event.ID != workflowOwnerScopeEventID("drained", event.WorkflowID, event.OwnerEpoch) || !exists ||
				entry.WorkflowID != event.WorkflowID || event.OwnerEpoch == 0 || !validWorkflowOwnerIncarnation(event.Incarnation) ||
				!validDigestText(event.DrainProofDigest) || event.Unexposed && bound || (!event.Unexposed && !bound) ||
				bound && (state.AdmissionHash != event.AdmissionHash || state.Drained || state.Incarnation != event.Incarnation ||
					state.ScopeIdentityDigest != event.ScopeIdentityDigest || state.ScopePath != event.ScopePath || state.ScopeName != event.ScopeName) {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			if event.Unexposed {
				if event.ScopeName != workflowOwnerScopeName(event.WorkflowID, event.OwnerEpoch) || event.ScopePath != "" || event.ScopeIdentityDigest != "" {
					_ = journal.Close()
					return nil, ErrPiWorkflowCatalog
				}
				state = piServiceWorkflowOwnerScope{AdmissionHash: event.AdmissionHash, WorkflowID: event.WorkflowID,
					OwnerEpoch: event.OwnerEpoch, Incarnation: event.Incarnation, ScopeName: event.ScopeName, Unexposed: true}
			} else {
				state.Drained = true
			}
			state.Drained = true
			catalog.ownerScopes[key] = state
		default:
			_ = journal.Close()
			return nil, ErrPiWorkflowCatalog
		}
	}
	return catalog, nil
}

func (c *piServiceWorkflowCatalog) ResolveAdmission(admission piServiceAdmissionContext, config piServiceConfig,
	configDigest, registrationID string) (*productionWorkflowAdmission, error) {
	if c == nil || c.journal == nil || admission.AcceptedRecord.Sequence == 0 ||
		admission.AcceptedRecord.Event.Kind != "pi_service_request_accepted" ||
		admission.Accepted.SchemaVersion != "tbound-pi-service-request-accepted/v1" ||
		admission.AcceptedRecord.Event.ID != piServiceRequestKey(admission.Accepted.ClientUID, admission.Accepted.RequestID) ||
		admission.Accepted.ClientUID != admission.Peer.UID || admission.Accepted.ClientGID != admission.Peer.GID ||
		admission.Accepted.RequestID != admission.Request.RequestID || admission.Accepted.TaskID != admission.Request.TaskID ||
		admission.Accepted.PayloadDigest != admission.PayloadDigest || !validDigestText(configDigest) || !validDigestText(registrationID) {
		return nil, ErrPiWorkflowCatalog
	}
	if admission.ConfigDigest != "" && admission.ConfigDigest != configDigest ||
		admission.RegistrationID != "" && admission.RegistrationID != registrationID {
		return nil, ErrPiWorkflowCatalog
	}
	if !workflowEnvelopeMatchesRequest(config, admission.Request) {
		return nil, ErrPiWorkflowCatalog
	}
	key := admission.AcceptedRecord.Hash
	if !validBareWorkflowDigest(key) {
		return nil, ErrPiWorkflowCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, exists := c.entries[key]; exists {
		if !catalogEntryMatchesAdmission(existing, admission, configDigest, registrationID) || !admission.IsReplay ||
			!existing.CheckpointBound || existing.Terminal {
			return nil, ErrPiWorkflowCatalog
		}
		return &productionWorkflowAdmission{Mode: "resume", Entry: clonePiServiceWorkflowEntry(existing), Catalog: c}, nil
	}
	if _, alreadyReserved := c.byHostAdmissionID[config.WorkflowAdmissionID]; alreadyReserved {
		// An accepted host task has already consumed this one-use admission
		// envelope. A changed client request ID cannot mint a second workflow or
		// reset its cumulative counters.
		return nil, ErrPiWorkflowCatalog
	}
	if admission.IsReplay || config.WorkflowAdmissionMode != "fresh" {
		return nil, ErrPiWorkflowCatalog
	}
	token, err := productionToken()
	if err != nil {
		return nil, err
	}
	workflowID := "tbound-workflow-" + token
	runID := "production-run-" + token
	sessionID := runID
	conversationID := "tbound-conversation-" + token
	taskBytes, err := json.Marshal(struct {
		AdmissionID string   `json:"admission_id"`
		TaskID      string   `json:"task_id"`
		Prompt      string   `json:"prompt"`
		BashPaths   []string `json:"bash_output_paths"`
	}{config.WorkflowAdmissionID, admission.Request.TaskID, admission.Request.Prompt,
		admissionRequestBashPaths(admission.Request)})
	if err != nil {
		return nil, err
	}
	var bashPaths []string
	if admission.Request.BashOutputPaths != nil {
		bashPaths = cloneDurableOutputScopePaths(*admission.Request.BashOutputPaths)
	}
	reservedAt := time.Now().UTC()
	entry := piServiceWorkflowEntry{
		SchemaVersion: workflowCatalogSchema, HostAdmissionID: config.WorkflowAdmissionID, AdmissionRecordHash: key,
		AdmissionSequence: admission.AcceptedRecord.Sequence,
		PeerUID:           admission.Peer.UID, PeerGID: admission.Peer.GID, PeerPID: admission.Peer.PID,
		RequestID: admission.Request.RequestID, TaskID: admission.Request.TaskID,
		PayloadDigest: admission.PayloadDigest, ServiceConfigDigest: configDigest,
		ServiceRegistrationID: registrationID, WorkflowID: workflowID, RunID: runID,
		SessionID: sessionID, ConversationID: conversationID,
		OwnerDBPath:         filepath.Join(config.WorkflowOwnerStoreRoot, workflowID+".sqlite"),
		StoreRootPath:       filepath.Join(productionStoreRootDefault, "session-"+token),
		AdminRootPath:       filepath.Join(productionAdminRootDefault, "admin-"+token),
		StoreJournalPath:    filepath.Join(productionAuditRootDefault, runID+"-session-audit.jsonl"),
		ProviderJournalPath: filepath.Join(productionAuditRootDefault, runID+"-provider-audit.jsonl"),
		CommandJournalPath:  filepath.Join(productionAuditRootDefault, runID+"-command-audit.jsonl"),
		TaskPrompt:          admission.Request.Prompt, TaskDigest: digestBytes(taskBytes),
		PromptDigest: digestBytes([]byte(admission.Request.Prompt)), BashOutputPaths: bashPaths,
		Limits: workflowrecovery.Limits{MaxPreEffectRetries: config.WorkflowMaxPreEffectRetries,
			MaxCleanRestarts: config.WorkflowMaxCleanRestarts, MaxHostCalls: config.WorkflowMaxHostCalls,
			MaxTokens: config.WorkflowMaxTokens,
			Deadline:  reservedAt.Add(time.Duration(config.WorkflowDurationSeconds) * time.Second)},
		ReservedAt: reservedAt,
	}
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: key, Entry: entry}
	if err := c.appendLocked("pi_workflow_admission_reserved", workflowCatalogEventID("reserved", key), event); err != nil {
		return nil, err
	}
	c.entries[key] = entry
	c.byHostAdmissionID[entry.HostAdmissionID] = key
	return &productionWorkflowAdmission{Mode: "fresh", Entry: clonePiServiceWorkflowEntry(entry), Catalog: c}, nil
}

func openProductionWorkflowOwner(ctx context.Context, admission *productionWorkflowAdmission,
	config piServiceConfig, template piruntime.D09CellTemplate, profile piruntime.HostProfile) error {
	if ctx == nil || admission == nil || admission.Catalog == nil || admission.Entry.WorkflowID == "" ||
		config.WorkflowRootDBPath != piServiceWorkflowRootDBPath || config.WorkflowOwnerStoreRoot != admission.Catalog.ownerRoot() {
		return ErrPiWorkflowCatalog
	}
	boundary, err := workflowrecovery.NewLinuxHostBoundary(config.WorkflowOwnerStoreRoot, uint32(os.Geteuid()),
		catalogOwnerDrainVerifier{catalog: admission.Catalog, cgroupRoot: template.CgroupsPath,
			cgroupRootDigest: profile.CgroupRootDigest, containment: profile.ContainmentProfile,
			settlement: profile.SettlementProfile}, admission.Catalog)
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	root, err := workflowrecovery.OpenExistingProtectedRoot(config.WorkflowRootDBPath, config.WorkflowOwnerStoreRoot, boundary)
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	var owner *workflowrecovery.SQLiteOwner
	if admission.Mode == "fresh" {
		owner, err = workflowrecovery.CreateFreshSQLiteOwner(admission.Entry.OwnerDBPath, root)
	} else if admission.Mode == "resume" {
		owner, err = workflowrecovery.OpenExistingSQLiteOwner(admission.Entry.OwnerDBPath, root)
	}
	if err != nil {
		_ = root.Close()
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	closeOwner := true
	defer func() {
		if closeOwner {
			_ = owner.Close()
			_ = root.Close()
		}
	}()
	identity, err := owner.OwnerStoreIdentity(ctx, admission.Entry.WorkflowID)
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	if admission.Mode == "fresh" {
		if identity.Path != admission.Entry.OwnerDBPath {
			return ErrPiWorkflowCatalog
		}
		candidate, err := owner.NextOwnerEpoch(ctx, admission.Entry.WorkflowID)
		if err != nil || candidate != 1 {
			return errors.Join(workflowrecovery.ErrOwnerFence, err)
		}
		admission.CandidateEpoch = candidate
		admission.OwnerIncarnation = owner.OwnerIncarnation()
	} else {
		checkpoint, checkpointHead, exists, loadErr := owner.LoadCheckpoint(ctx, admission.Entry.WorkflowID)
		if loadErr != nil || !exists || !admission.Entry.CheckpointBound || !admission.Entry.FreshAuthorizationUsed ||
			identity != admission.Entry.OwnerStoreIdentity || checkpoint.Pins.WorkflowID != admission.Entry.WorkflowID ||
			checkpoint.Limits != admission.Entry.Limits || checkpoint.Pins.TaskDigest != admission.Entry.TaskDigest ||
			checkpoint.Pins.PromptDigest != admission.Entry.PromptDigest ||
			checkpoint.Pins.RootIdentityDigest != root.TrustedRootIdentityDigest() ||
			checkpoint.Pins.StoreIdentityDigest == "" || checkpoint.Pins.AdminIdentityDigest == "" {
			return errors.Join(workflowrecovery.ErrMissing, ErrPiWorkflowCatalog, loadErr)
		}
		if err := owner.VerifyWorkflowDeadline(ctx, admission.Entry.WorkflowID, checkpoint.Limits.Deadline); err != nil {
			return errors.Join(workflowrecovery.ErrDeadline, err)
		}
		candidate, err := owner.NextOwnerEpoch(ctx, admission.Entry.WorkflowID)
		if err != nil || candidate <= checkpoint.Pins.OwnerEpoch {
			return errors.Join(workflowrecovery.ErrOwnerFence, err)
		}
		admission.CandidateEpoch = candidate
		admission.OwnerIncarnation = owner.OwnerIncarnation()
		admission.Checkpoint = checkpoint
		admission.CheckpointHead = checkpointHead
		ledger, ledgerErr := workflowrecovery.Open(ctx, owner, workflowrecovery.OpenOptions{
			WorkflowID: admission.Entry.WorkflowID, OwnerEpoch: candidate,
			RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
		})
		if ledgerErr != nil {
			return errors.Join(workflowrecovery.ErrOwnerFence, ledgerErr)
		}
		opened, openedHead := ledger.Snapshot()
		if !reflect.DeepEqual(opened, checkpoint) || openedHead != checkpointHead {
			return workflowrecovery.ErrRollback
		}
		admission.Ledger = ledger
	}
	admission.Owner, admission.Root, admission.RootCloser, admission.OwnerCloser = owner, root, root, owner
	closeOwner = false
	return nil
}

func (a *productionWorkflowAdmission) CloseOwner() error {
	if a == nil {
		return nil
	}
	var errs []error
	if a.OwnerCloser != nil {
		errs = append(errs, a.OwnerCloser.Close())
		a.OwnerCloser = nil
	}
	if a.RootCloser != nil {
		errs = append(errs, a.RootCloser.Close())
		a.RootCloser = nil
	}
	return errors.Join(errs...)
}

func (c *piServiceWorkflowCatalog) AuthorizeFreshWorkflow(ctx context.Context, workflow string, ownerEpoch uint64,
	initialDigest string, incarnation workflowrecovery.OwnerIncarnation, deadline time.Time, rootIdentity string,
	store workflowrecovery.OwnerStoreIdentity) error {
	if c == nil || ctx == nil || ctx.Err() != nil || ownerEpoch != 1 || !validBareWorkflowDigest(initialDigest) ||
		deadline.IsZero() || deadline.Location() != time.UTC || !validBareWorkflowDigest(rootIdentity) || !validWorkflowOwnerIncarnation(incarnation) ||
		!validBareWorkflowDigest(store.Digest) || store.Path == "" {
		return errors.Join(workflowrecovery.ErrFreshAuthorization, ErrPiWorkflowCatalog)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var match *piServiceWorkflowEntry
	var matchHash string
	for hash, entry := range c.entries {
		if entry.WorkflowID == workflow {
			copyEntry := entry
			match, matchHash = &copyEntry, hash
			break
		}
	}
	if match == nil || matchHash != match.AdmissionRecordHash || match.FreshAuthorizationUsed ||
		!deadline.Equal(match.Limits.Deadline) ||
		match.OwnerDBPath != store.Path || match.Terminal {
		return errors.Join(workflowrecovery.ErrFreshAuthorization, ErrPiWorkflowCatalog)
	}
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: matchHash,
		OwnerEpoch: ownerEpoch, Digest: initialDigest, RootIdentity: rootIdentity,
		StoreIdentity: store, Incarnation: incarnation}
	if err := c.appendLocked("pi_workflow_fresh_authorized", workflowCatalogEventID("fresh", matchHash), event); err != nil {
		return errors.Join(workflowrecovery.ErrFreshAuthorization, err)
	}
	match.FreshAuthorizationUsed, match.InitialOwnerEpoch = true, ownerEpoch
	match.InitialCheckpointDigest, match.RootIdentityDigest = initialDigest, rootIdentity
	match.OwnerStoreIdentity, match.OwnerIncarnation = store, incarnation
	c.entries[matchHash] = *match
	return nil
}

func (c *piServiceWorkflowCatalog) BindInitialCheckpoint(admission *productionWorkflowAdmission,
	receipt workflowrecovery.Receipt) error {
	if c == nil || admission == nil || admission.Catalog != c || admission.Mode != "fresh" ||
		receipt.WorkflowID() != admission.Entry.WorkflowID ||
		receipt.SessionID() != admission.Entry.SessionID || !validBareWorkflowDigest(receipt.CheckpointDigest()) {
		return ErrPiWorkflowCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[admission.Entry.AdmissionRecordHash]
	if !ok || !entry.FreshAuthorizationUsed || entry.CheckpointBound || entry.Terminal ||
		entry.InitialCheckpointDigest != receipt.CheckpointDigest() {
		return ErrPiWorkflowCatalog
	}
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema,
		AdmissionHash: entry.AdmissionRecordHash, Digest: receipt.CheckpointDigest()}
	if err := c.appendLocked("pi_workflow_checkpoint_bound", workflowCatalogEventID("checkpoint", entry.AdmissionRecordHash), event); err != nil {
		return err
	}
	entry.CheckpointBound, entry.LatestCheckpointDigest = true, receipt.CheckpointDigest()
	c.entries[entry.AdmissionRecordHash] = entry
	return nil
}

func (c *piServiceWorkflowCatalog) MarkCheckpoint(admissionHash, previousDigest, nextDigest string) error {
	if c == nil || !validBareWorkflowDigest(admissionHash) || !validBareWorkflowDigest(previousDigest) ||
		!validBareWorkflowDigest(nextDigest) || previousDigest == nextDigest {
		return ErrPiWorkflowCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[admissionHash]
	if !ok || !entry.CheckpointBound || entry.Terminal || entry.LatestCheckpointDigest != previousDigest {
		return ErrPiWorkflowCatalog
	}
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash, Digest: nextDigest}
	if err := c.appendLocked("pi_workflow_checkpoint_advanced", workflowCatalogEventID("advance", admissionHash), event); err != nil {
		return err
	}
	entry.LatestCheckpointDigest = nextDigest
	c.entries[admissionHash] = entry
	return nil
}

func (c *piServiceWorkflowCatalog) MarkTerminal(admissionHash, checkpointDigest, runID, proofDigest string,
	proofSequence uint64, transactionID string) error {
	if c == nil || !validBareWorkflowDigest(admissionHash) || !validBareWorkflowDigest(checkpointDigest) ||
		!validIdentityText(runID) || !validDigestText(proofDigest) || proofSequence == 0 ||
		!boundedProductionIdentity(transactionID, productionRequestMaxBytes) {
		return ErrPiWorkflowCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[admissionHash]
	if !ok || !entry.CheckpointBound || entry.LatestCheckpointDigest != checkpointDigest {
		return ErrPiWorkflowCatalog
	}
	if entry.Terminal {
		if entry.LatestCheckpointDigest == checkpointDigest && entry.TerminalRunID == runID &&
			entry.TerminalProofDigest == proofDigest && entry.TerminalProofSequence == proofSequence &&
			entry.TerminalTransactionID == transactionID {
			return nil
		}
		return ErrPiWorkflowCatalog
	}
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash, Digest: checkpointDigest,
		RunID: runID, TCBProofDigest: proofDigest, TCBProofSequence: proofSequence,
		PublicationTransactionID: transactionID}
	if err := c.appendLocked("pi_workflow_terminal", workflowCatalogEventID("terminal", admissionHash), event); err != nil {
		return err
	}
	entry.Terminal, entry.LatestCheckpointDigest = true, checkpointDigest
	entry.TerminalRunID, entry.TerminalProofDigest, entry.TerminalProofSequence, entry.TerminalTransactionID =
		runID, proofDigest, proofSequence, transactionID
	c.entries[admissionHash] = entry
	return nil
}

func (c *piServiceWorkflowCatalog) admissionEntry(admissionHash string) (piServiceWorkflowEntry, bool) {
	if c == nil || !validBareWorkflowDigest(admissionHash) {
		return piServiceWorkflowEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[admissionHash]
	if !ok {
		return piServiceWorkflowEntry{}, false
	}
	return clonePiServiceWorkflowEntry(entry), true
}

func (c *piServiceWorkflowCatalog) verifyCompletedServiceRequest(ctx context.Context, state piServiceRequestState,
	configDigest, registrationID string, verified *piruntime.VerifiedD09ManagedLiveRoot,
	template piruntime.D09CellTemplate, profile piruntime.HostProfile) error {
	if ctx == nil || ctx.Err() != nil || c == nil || verified == nil || state.terminal == nil ||
		state.accepted.SchemaVersion != "tbound-pi-service-request-accepted/v1" ||
		state.acceptedRecord.Sequence == 0 || state.acceptedRecord.Event.Kind != "pi_service_request_accepted" ||
		state.acceptedRecord.Event.ID != piServiceRequestKey(state.accepted.ClientUID, state.accepted.RequestID) ||
		!validBareWorkflowDigest(state.acceptedRecord.Hash) || !validDigestText(state.accepted.PayloadDigest) ||
		state.terminal.SchemaVersion != piServiceResponseSchema || state.terminal.RequestID != state.accepted.RequestID ||
		state.terminal.Status != "completed" || len(state.terminal.Receipt) == 0 ||
		!validDigestText(configDigest) || !validDigestText(registrationID) {
		return ErrPiServiceUnresolvedPrior
	}
	var persistedAccepted piServiceRequestAcceptedEvent
	if err := unmarshalGovernedJSON(state.acceptedRecord.Event.Data, &persistedAccepted); err != nil || persistedAccepted != state.accepted {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	entry, ok := c.admissionEntry(state.acceptedRecord.Hash)
	if !ok || entry.AdmissionRecordHash != state.acceptedRecord.Hash || entry.AdmissionSequence != state.acceptedRecord.Sequence ||
		entry.PeerUID != state.accepted.ClientUID || entry.PeerGID != state.accepted.ClientGID ||
		entry.PeerPID != state.accepted.ClientPID || entry.RequestID != state.accepted.RequestID ||
		entry.TaskID != state.accepted.TaskID || entry.PayloadDigest != state.accepted.PayloadDigest ||
		entry.ServiceConfigDigest != configDigest || entry.ServiceRegistrationID != registrationID ||
		!entry.CheckpointBound || !entry.FreshAuthorizationUsed ||
		requireWorkflowCatalogPath(entry, piServiceConfig{WorkflowOwnerStoreRoot: c.ownerRoot()}) != nil {
		return ErrPiWorkflowCatalog
	}
	var receipt productionPiReceipt
	if err := unmarshalGovernedJSON(state.terminal.Receipt, &receipt); err != nil ||
		receipt.SchemaVersion != "tbound-production-pi-receipt/v1" || receipt.Mode != "production" || !receipt.Success ||
		!receipt.WorkerReady || !receipt.TaskAdmitted || !receipt.TurnCompleted || !receipt.ExitObserved ||
		!receipt.DescendantsSettled || receipt.Settlement != string(piruntime.SettlementStopped) ||
		!productionReceiptSettlementClaimed(receipt) ||
		!boundedProductionIdentity(receipt.RunID, productionTaskIDMaxBytes) ||
		!validDigestText(receipt.TCBProofDigest) || receipt.TCBProofJournalSequence == 0 {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	if err := validateSettledServiceProfile(profile, template); err != nil {
		return err
	}
	registration := verified.Registration()
	if registration.Digest != template.ManagedLiveRootRegistrationDigest {
		return ErrPiWorkflowCatalog
	}
	root, err := verified.DuplicateRoot()
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	defer root.Close()
	if err := verified.Revalidate(ctx, root); err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	rootDescriptor, err := measureManagedLiveRootDescriptor(root)
	if err != nil || !managedRegistrationMatchesDescriptor(registration, rootDescriptor) {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	rootID := workflow.RootIdentity{Device: rootDescriptor.Device, Inode: rootDescriptor.Inode}
	if rootID == (workflow.RootIdentity{}) {
		return ErrManagedLiveRootUnavailable
	}
	drainVerifier := catalogOwnerDrainVerifier{catalog: c, cgroupRoot: template.CgroupsPath,
		cgroupRootDigest: profile.CgroupRootDigest, containment: profile.ContainmentProfile,
		settlement: profile.SettlementProfile}
	boundary, err := workflowrecovery.NewLinuxHostBoundary(c.ownerRoot(), uint32(os.Geteuid()), drainVerifier, c)
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	protectedRoot, err := workflowrecovery.OpenExistingProtectedRoot(piServiceWorkflowRootDBPath, c.ownerRoot(), boundary)
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	defer protectedRoot.Close()
	view, err := workflowrecovery.LoadSettledCheckpoint(ctx, entry.OwnerDBPath, protectedRoot, entry.WorkflowID)
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	checkpoint := view.Checkpoint
	pins := checkpoint.Pins
	if view.Digest != checkpoint.ComputedCheckpointDigest || view.OwnerStoreIdentity != entry.OwnerStoreIdentity ||
		view.OwnerStoreIdentity.Path != entry.OwnerDBPath || !validBareWorkflowDigest(entry.RootIdentityDigest) ||
		pins.WorkflowID != entry.WorkflowID || checkpoint.Limits != entry.Limits {
		return ErrPiWorkflowCatalog
	}
	return c.verifyCompletedServiceRequestPins(ctx, state, entry, receipt, view, registration, rootDescriptor, rootID,
		drainVerifier, template, profile)
}

func validateSettledServiceProfile(profile piruntime.HostProfile, template piruntime.D09CellTemplate) error {
	if profile.Mode != piruntime.ModeProduction || profile.ContainmentProfile != piruntime.D09ContainmentProfile ||
		profile.SettlementProfile != piruntime.D09SettlementProfile || !validDigestText(profile.Digest) ||
		!validDigestText(profile.CgroupRootDigest) || template.LandlockABI != 8 {
		return ErrPiWorkflowCatalog
	}
	templateDigest, err := template.Digest()
	if err != nil || templateDigest != profile.ContainmentProfileDigest {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	return nil
}

func validateSettledServiceDisposition(pins workflowrecovery.Pins, expectedConversationID string) error {
	if pins.PendingCallDisposition != workflowrecovery.PendingCallNone &&
		pins.PendingCallDisposition != workflowrecovery.PendingCallTerminal &&
		pins.PendingCallDisposition != workflowrecovery.PendingCallRetired {
		return errors.Join(ErrPiWorkflowCatalog, workflowrecovery.ErrPendingUnknown)
	}
	if pins.ProviderDisposition != workflowrecovery.DispositionNone &&
		pins.ProviderDisposition != workflowrecovery.DispositionAwaitingHostPrompt &&
		pins.ProviderDisposition != workflowrecovery.DispositionTerminal &&
		pins.ProviderDisposition != workflowrecovery.DispositionRetired {
		return errors.Join(ErrPiWorkflowCatalog, workflowrecovery.ErrPendingUnknown)
	}
	if len(pins.ProviderTranscripts) == 0 {
		if pins.ProviderDisposition != workflowrecovery.DispositionNone {
			return errors.Join(ErrPiWorkflowCatalog, workflowrecovery.ErrPendingUnknown)
		}
	} else {
		latest := pins.ProviderTranscripts[len(pins.ProviderTranscripts)-1]
		if pins.ProviderDisposition != latest.Disposition || latest.ConversationID != expectedConversationID {
			return errors.Join(ErrPiWorkflowCatalog, workflowrecovery.ErrPendingUnknown)
		}
	}
	if effect := pins.EffectReservation; effect != nil &&
		effect.Disposition != workflowrecovery.EffectReservationTerminal &&
		effect.Disposition != workflowrecovery.EffectReservationRetired {
		return errors.Join(ErrPiWorkflowCatalog, workflowrecovery.ErrPendingUnknown)
	}
	if call := pins.HostCallReservation; call != nil &&
		call.Disposition != workflowrecovery.HostCallTerminal &&
		call.Disposition != workflowrecovery.HostCallRetired {
		return errors.Join(ErrPiWorkflowCatalog, workflowrecovery.ErrPendingUnknown)
	}
	for index, transcript := range pins.ProviderTranscripts {
		quiescentLatestConversation := index == len(pins.ProviderTranscripts)-1 &&
			transcript.Disposition == workflowrecovery.DispositionAwaitingHostPrompt
		if transcript.Disposition != workflowrecovery.DispositionTerminal &&
			transcript.Disposition != workflowrecovery.DispositionRetired && !quiescentLatestConversation {
			return fmt.Errorf("%w: %w: transcript %d lacks terminal lineage disposition",
				ErrPiWorkflowCatalog, workflowrecovery.ErrPendingUnknown, index)
		}
	}
	return nil
}

func (c *piServiceWorkflowCatalog) verifyCompletedServiceRequestPins(ctx context.Context, state piServiceRequestState,
	entry piServiceWorkflowEntry, receipt productionPiReceipt, view workflowrecovery.SettledCheckpointView,
	registration piruntime.D09ManagedLiveRootRegistration, rootDescriptor managedLiveRootDescriptorIdentity, rootID workflow.RootIdentity,
	drainVerifier catalogOwnerDrainVerifier, template piruntime.D09CellTemplate, profile piruntime.HostProfile) error {
	pins := view.Checkpoint.Pins
	if pins.RootIdentityDigest != entry.RootIdentityDigest ||
		pins.TaskDigest != bareWorkflowDigest(entry.TaskDigest) || pins.PromptDigest != bareWorkflowDigest(entry.PromptDigest) ||
		pins.ProfileDigest != bareWorkflowDigest(profile.Digest) || pins.ImageDigest != bareWorkflowDigest(template.ExpectedImageDigest) ||
		pins.OwnerEpoch == 0 || pins.RecoveryGeneration == 0 || pins.StoreJournalCount == 0 ||
		!validBareWorkflowDigest(pins.StoreJournalHead) {
		return ErrPiWorkflowCatalog
	}
	expectedRunID, expectedSessionID, expectedConversationID := entry.RunID, entry.SessionID, entry.ConversationID
	if pins.RecoveryGeneration > 1 {
		expectedSessionID = fmt.Sprintf("%s-session-g%d", entry.WorkflowID, pins.RecoveryGeneration)
		expectedRunID = expectedSessionID
		expectedConversationID = fmt.Sprintf("%s-conversation-g%d", entry.WorkflowID, pins.RecoveryGeneration)
	}
	if err := validateSettledServiceDisposition(pins, expectedConversationID); err != nil {
		return err
	}
	if receipt.RunID != expectedRunID || pins.SessionID != expectedSessionID {
		return ErrPiWorkflowCatalog
	}
	if err := c.verifySettledOwnerScopes(entry, view.OwnerEpochs, template.CgroupsPath); err != nil {
		return err
	}
	if _, err := drainVerifier.VerifyOwnerCellsDrainedWithProof(ctx, entry.WorkflowID, view.OwnerEpochs); err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	storeJournal, err := openPiServiceSettlementJournal(entry.StoreJournalPath)
	if err != nil {
		return err
	}
	storeTrace, traceErr := storeJournal.Trace()
	if traceErr != nil {
		_ = storeJournal.Close()
		return errors.Join(ErrPiWorkflowCatalog, traceErr)
	}
	if pins.StoreJournalCount > uint64(len(storeTrace.Records)) ||
		auditHeadHash(storeTrace.Records[:pins.StoreJournalCount]) != pins.StoreJournalHead ||
		receipt.TCBProofJournalSequence > pins.StoreJournalCount {
		_ = storeJournal.Close()
		return ErrPiWorkflowCatalog
	}
	proofRecord, proofEvent, err := verifyServiceTCBProof(storeJournal, storeTrace, receipt, expectedSessionID,
		entry.WorkflowID, expectedConversationID, profile, template)
	storeCloseErr := storeJournal.Close()
	if storeCloseErr != nil {
		return errors.Join(ErrPiWorkflowCatalog, storeCloseErr)
	}
	if err != nil {
		return err
	}
	publicationJournal, err := openPiServiceSettlementJournal(filepath.Join(productionAuditRootDefault, "publication-audit.jsonl"))
	if err != nil {
		return err
	}
	publicationTrace, traceErr := publicationJournal.Trace()
	if traceErr != nil {
		_ = publicationJournal.Close()
		return errors.Join(ErrPiWorkflowCatalog, traceErr)
	}
	settlementID := receipt.PublicationTransactionID
	if receipt.Disposition == "NO_DIFF" {
		settlementID, err = verifyManagedPiNoPublicationSettlement(publicationJournal, publicationTrace, rootDescriptor, rootID,
			registration, entry.WorkflowID, receipt.RunID, pins.OwnerEpoch)
	} else {
		err = verifyManagedPiPublicationSettlement(publicationJournal, publicationTrace, rootDescriptor, rootID, registration, entry.WorkflowID,
			receipt.RunID, pins.OwnerEpoch, receipt.PublicationTransactionID)
	}
	if err != nil {
		_ = publicationJournal.Close()
		return err
	}
	publicationCloseErr := publicationJournal.Close()
	if publicationCloseErr != nil {
		return errors.Join(ErrPiWorkflowCatalog, publicationCloseErr)
	}
	if entry.Terminal {
		if entry.LatestCheckpointDigest != view.Digest || entry.TerminalRunID != receipt.RunID ||
			entry.TerminalProofDigest != receipt.TCBProofDigest ||
			entry.TerminalProofSequence != receipt.TCBProofJournalSequence ||
			entry.TerminalTransactionID != settlementID {
			return ErrPiWorkflowCatalog
		}
		return c.MarkTerminal(entry.AdmissionRecordHash, view.Digest, receipt.RunID, receipt.TCBProofDigest,
			receipt.TCBProofJournalSequence, settlementID)
	}
	if proofRecord.Sequence != receipt.TCBProofJournalSequence || proofEvent.ProofDigest != receipt.TCBProofDigest {
		return ErrPiWorkflowCatalog
	}
	return c.MarkTerminal(entry.AdmissionRecordHash, view.Digest, receipt.RunID, receipt.TCBProofDigest,
		receipt.TCBProofJournalSequence, settlementID)
}

func (c *piServiceWorkflowCatalog) verifySettledOwnerScopes(entry piServiceWorkflowEntry,
	owners []workflowrecovery.OwnerEpochIdentity, cgroupRoot string) error {
	if len(owners) == 0 || filepath.Clean(cgroupRoot) != cgroupRoot || !filepath.IsAbs(cgroupRoot) {
		return ErrPiWorkflowCatalog
	}
	scopes := c.ownerScopesForAdmission(entry.AdmissionRecordHash, entry.WorkflowID)
	if len(scopes) != len(owners) {
		return ErrPiWorkflowCatalog
	}
	for index, owner := range owners {
		scope := scopes[index]
		name := workflowOwnerScopeName(entry.WorkflowID, owner.Epoch)
		path := filepath.Join(cgroupRoot, name)
		if scope.OwnerEpoch != owner.Epoch || scope.Incarnation != owner.Incarnation || !scope.Drained ||
			scope.AdmissionHash != entry.AdmissionRecordHash || scope.WorkflowID != entry.WorkflowID ||
			scope.ScopeName != name {
			return ErrPiWorkflowCatalog
		}
		if scope.Unexposed {
			if scope.ScopePath != "" || scope.ScopeIdentityDigest != "" {
				return ErrPiWorkflowCatalog
			}
		} else if scope.ScopePath != path || !validDigestText(scope.ScopeIdentityDigest) {
			return ErrPiWorkflowCatalog
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.Join(ErrPiWorkflowCatalog, err)
		}
	}
	return nil
}

func openPiServiceSettlementJournal(path string) (*audit.Journal, error) {
	if filepath.Clean(path) != path || filepath.Dir(path) != productionAuditRootDefault {
		return nil, ErrPiWorkflowCatalog
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
		!ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	journal, err := audit.Open(path)
	if err != nil {
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	return journal, nil
}

func verifyServiceTCBProof(journal *audit.Journal, trace audit.Trace, receipt productionPiReceipt, sessionID, workflowID, conversationID string,
	profile piruntime.HostProfile, template piruntime.D09CellTemplate) (audit.Record, productionLaunchTCBProofEvent, error) {
	var matched *audit.Record
	for index := range trace.Records {
		record := &trace.Records[index]
		if record.Sequence == receipt.TCBProofJournalSequence && record.Event.Kind == "production_tcb_proof" &&
			record.Event.ID == "production-tcb-proof:"+receipt.RunID {
			if matched != nil {
				return audit.Record{}, productionLaunchTCBProofEvent{}, ErrPiWorkflowCatalog
			}
			matched = record
		}
	}
	if matched == nil {
		return audit.Record{}, productionLaunchTCBProofEvent{}, ErrPiWorkflowCatalog
	}
	var event productionLaunchTCBProofEvent
	if err := unmarshalGovernedJSON(matched.Event.Data, &event); err != nil ||
		event.SchemaVersion != "tbound-production-launch-tcb-proof/v1" || event.RunID != receipt.RunID ||
		event.SessionID != sessionID || event.WorkflowID != workflowID || event.ConversationID != conversationID ||
		event.ProfileID != profile.ID || event.ProfileDigest != profile.Digest || event.ProofDigest != receipt.TCBProofDigest ||
		!validDigestText(event.SourceBindingDigest) || !validIdentityText(event.SourceGenerationID) ||
		!validDigestText(event.Proof.SourceBindingDigest) || event.Proof.SourceBindingDigest != event.SourceBindingDigest ||
		event.Proof.SourceGenerationID != event.SourceGenerationID || event.Proof.SourceStoreAuditCount == 0 ||
		!validBareWorkflowDigest(event.Proof.SourceStoreAuditHead) {
		return audit.Record{}, productionLaunchTCBProofEvent{}, errors.Join(ErrPiWorkflowCatalog, err)
	}
	snapshot := productionLifecycleSnapshot{proofRunID: receipt.RunID, proofDigest: receipt.TCBProofDigest,
		proofSeq: matched.Sequence, proofRecordHash: matched.Hash, proofEventID: matched.Event.ID,
		workerPID: event.Proof.LauncherProcessPID}
	if err := verifyProductionLaunchProofJournal(journal, snapshot, profile, template,
		receipt.RunID, sessionID, workflowID, conversationID, event.SourceGenerationID, event.SourceBindingDigest); err != nil {
		return audit.Record{}, productionLaunchTCBProofEvent{}, errors.Join(ErrPiWorkflowCatalog, err)
	}
	if event.Proof.SourceStoreAuditCount >= receipt.TCBProofJournalSequence ||
		event.Proof.SourceStoreAuditCount > uint64(len(trace.Records)) ||
		auditHeadHash(trace.Records[:event.Proof.SourceStoreAuditCount]) != event.Proof.SourceStoreAuditHead {
		return audit.Record{}, productionLaunchTCBProofEvent{}, ErrPiWorkflowCatalog
	}
	return *matched, event, nil
}

func verifyManagedPiPublicationSettlement(journal *audit.Journal, trace audit.Trace,
	rootDescriptor managedLiveRootDescriptorIdentity, root workflow.RootIdentity,
	registration piruntime.D09ManagedLiveRootRegistration, workflowID, runID string, ownerEpoch uint64,
	transactionID string) error {
	if journal == nil || root == (workflow.RootIdentity{}) || !validDigestText(registration.Digest) || !validIdentityText(workflowID) ||
		!validIdentityText(runID) || ownerEpoch == 0 || !boundedProductionIdentity(transactionID, productionRequestMaxBytes) {
		return ErrManagedLiveRootUnavailable
	}
	key := managedWorkflowEventID(root, workflowID, "production-publication-"+workflowID)
	admissionID := managedLiveRootRunEventID(root, registration.Digest, runID, ownerEpoch)
	var admitted *audit.Record
	var released *audit.Record
	var pendingSeq, transactionSeq, resolvedSeq uint64
	var resolvedHash string
	var publicationResolved *audit.Record
	for index := range trace.Records {
		record := &trace.Records[index]
		switch record.Event.Kind {
		case "managed_live_root_run_admitted":
			if record.Event.ID == admissionID {
				var event managedLiveRootRunAdmittedEvent
				if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil || admitted != nil ||
					event.SchemaVersion != "tbound-managed-live-root-run-admitted/v1" || event.Root != root ||
					event.RegistrationDigest != registration.Digest || event.RunID != runID ||
					event.WorkflowID != workflowID || event.PublicationID != "production-publication-"+workflowID ||
					event.OwnerEpoch != ownerEpoch || event.ServiceUID != registration.ServiceUID ||
					event.ServiceGID != registration.ServiceGID || event.Descriptor != rootDescriptor {
					return errors.Join(ErrManagedLiveRootUnavailable, err)
				}
				admitted = record
			}
		case "managed_live_root_run_released":
			if record.Event.ID == admissionID+"/released" {
				var event managedLiveRootRunResolvedEvent
				if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil || released != nil ||
					event.SchemaVersion != "tbound-managed-live-root-run-resolved/v1" || event.Root != root ||
					event.RegistrationDigest != registration.Digest || event.RunID != runID ||
					event.WorkflowID != workflowID || event.PublicationID != "production-publication-"+workflowID ||
					event.OwnerEpoch != ownerEpoch || event.Outcome != "publication-succeeded" ||
					event.ResolvedByRunID != "" || event.ResolvedByOwnerEpoch != 0 {
					return errors.Join(ErrManagedLiveRootUnavailable, err)
				}
				released, resolvedSeq, resolvedHash = record, event.LifecycleJournalSequence, event.LifecycleJournalRecordHash
			}
		case "workflow_publication_pending":
			var event managedRootPendingWire
			if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil {
				return errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if event.Root == root && event.WorkflowID == workflowID && event.PublicationID == "production-publication-"+workflowID {
				if record.Event.ID != key || event.OwnerEpoch != ownerEpoch || pendingSeq != 0 {
					return ErrManagedLiveRootUnavailable
				}
				pendingSeq = record.Sequence
			}
		case "workflow_publication_transaction":
			var event managedRootTransactionWire
			if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil {
				return errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if event.Root == root && event.WorkflowID == workflowID && event.PublicationID == "production-publication-"+workflowID {
				if record.Event.ID != key+"/transaction" || event.TransactionID != transactionID || transactionSeq != 0 {
					return ErrManagedLiveRootUnavailable
				}
				transactionSeq = record.Sequence
			}
		case "workflow_publication_resolved":
			var event managedRootResolvedWire
			if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil {
				return errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if event.Root == root && event.WorkflowID == workflowID && event.PublicationID == "production-publication-"+workflowID {
				if record.Event.ID != key+"/resolved" || event.TransactionID != transactionID ||
					event.Status != publication.StatusSucceeded || publicationResolved != nil {
					return ErrManagedLiveRootUnavailable
				}
				publicationResolved = record
			}
		}
	}
	if admitted == nil || released == nil || publicationResolved == nil || pendingSeq == 0 || transactionSeq == 0 ||
		resolvedSeq == 0 || publicationResolved.Sequence != resolvedSeq || publicationResolved.Hash != resolvedHash {
		return ErrManagedLiveRootUnavailable
	}
	if err := validateManagedPublicationOrder(pendingSeq, transactionSeq, publicationResolved.Sequence); err != nil {
		return err
	}
	var releasedEvent managedLiveRootRunResolvedEvent
	if err := unmarshalGovernedJSON(released.Event.Data, &releasedEvent); err != nil ||
		releasedEvent.AdmissionSequence != admitted.Sequence || releasedEvent.AdmissionRecordHash != admitted.Hash {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	if _, pending, err := managedRootLedgerState(journal, root); err != nil || len(pending) != 0 {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	return nil
}

func validateManagedPublicationOrder(pending, transaction, resolved uint64) error {
	if pending == 0 || transaction <= pending || resolved <= transaction {
		return ErrManagedLiveRootUnavailable
	}
	return nil
}

// A published receipt names the publication transaction it settled. A known
// no-change receipt names none: its settlement is proven only by the durable
// no-publication release, which verifyManagedPiNoPublicationSettlement checks.
func productionReceiptSettlementClaimed(receipt productionPiReceipt) bool {
	switch receipt.Disposition {
	case "PUBLISHED":
		return receipt.PublicationStatus == string(publication.StatusSucceeded) &&
			boundedProductionIdentity(receipt.PublicationTransactionID, productionRequestMaxBytes)
	case "NO_DIFF":
		return receipt.PublicationStatus == "NO_DIFF" && receipt.PublicationTransactionID == ""
	}
	return false
}

// verifyManagedPiNoPublicationSettlement accepts a known no-change run only when
// the publication journal holds this run's admission record and an exact
// no-publication release anchored to a ledger record that existed before it.
// No publication pending, transaction, or resolution record may name the
// workflow, the admitted root identity must still match, and the released tree
// digest must equal this run's durable seed. It returns the
// release identity that terminal settlement binds in place of a transaction ID.
func verifyManagedPiNoPublicationSettlement(journal *audit.Journal, trace audit.Trace,
	rootDescriptor managedLiveRootDescriptorIdentity, root workflow.RootIdentity,
	registration piruntime.D09ManagedLiveRootRegistration, workflowID, runID string, ownerEpoch uint64) (string, error) {
	if journal == nil || root == (workflow.RootIdentity{}) || !validDigestText(registration.Digest) || !validIdentityText(workflowID) ||
		!validIdentityText(runID) || ownerEpoch == 0 {
		return "", ErrManagedLiveRootUnavailable
	}
	publicationID := "production-publication-" + workflowID
	key := managedWorkflowEventID(root, workflowID, publicationID)
	admissionID := managedLiveRootRunEventID(root, registration.Digest, runID, ownerEpoch)
	var admitted, released *audit.Record
	for index := range trace.Records {
		record := &trace.Records[index]
		if record.Event.ID == key || strings.HasPrefix(record.Event.ID, key+"/") {
			switch record.Event.Kind {
			case "workflow_publication_pending", "workflow_publication_transaction", "workflow_publication_resolved":
				return "", ErrManagedLiveRootUnavailable
			}
		}
		switch record.Event.Kind {
		case "managed_live_root_run_admitted":
			if record.Event.ID == admissionID {
				var event managedLiveRootRunAdmittedEvent
				if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil || admitted != nil ||
					event.SchemaVersion != "tbound-managed-live-root-run-admitted/v1" || event.Root != root ||
					event.RegistrationDigest != registration.Digest || event.RunID != runID ||
					event.WorkflowID != workflowID || event.PublicationID != publicationID ||
					event.OwnerEpoch != ownerEpoch || event.ServiceUID != registration.ServiceUID ||
					event.ServiceGID != registration.ServiceGID || event.Descriptor != rootDescriptor {
					return "", errors.Join(ErrManagedLiveRootUnavailable, err)
				}
				admitted = record
			}
		case "managed_live_root_run_released":
			if record.Event.ID == admissionID+"/released" {
				if released != nil {
					return "", ErrManagedLiveRootUnavailable
				}
				released = record
			}
		}
	}
	if admitted == nil || released == nil {
		return "", ErrManagedLiveRootUnavailable
	}
	var event managedLiveRootRunResolvedEvent
	if err := unmarshalGovernedJSON(released.Event.Data, &event); err != nil ||
		event.SchemaVersion != "tbound-managed-live-root-run-resolved/v1" || event.Root != root ||
		event.RegistrationDigest != registration.Digest || event.RunID != runID ||
		event.WorkflowID != workflowID || event.PublicationID != publicationID ||
		event.OwnerEpoch != ownerEpoch || event.Outcome != "no-publication" ||
		event.ResolvedByRunID != "" || event.ResolvedByOwnerEpoch != 0 ||
		event.AdmissionSequence != admitted.Sequence || event.AdmissionRecordHash != admitted.Hash ||
		event.LifecycleJournalSequence < admitted.Sequence || event.LifecycleJournalSequence >= released.Sequence {
		return "", errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	anchored := false
	for _, record := range trace.Records {
		if record.Sequence == event.LifecycleJournalSequence {
			anchored = record.Hash == event.LifecycleJournalRecordHash
			break
		}
	}
	if !anchored {
		return "", ErrManagedLiveRootUnavailable
	}
	if err := verifyManagedNoChangeSeed(trace, runID, admitted.Sequence, released.Sequence, event.ReleasedTreeDigest); err != nil {
		return "", err
	}
	if _, pending, err := managedRootLedgerState(journal, root); err != nil || len(pending) != 0 {
		return "", errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	return "no-publication:" + released.Hash, nil
}

// verifyManagedNoChangeSeed binds a no-change release to the admitted tree. This
// run's durable seed of the managed root must sit between admission and release,
// and its tree digest must equal the digest that release rescanned from the root.
func verifyManagedNoChangeSeed(trace audit.Trace, runID string, admissionSeq, releaseSeq uint64, releasedTree string) error {
	id := "managed-root-source-capture-seeded:" + runID
	var seeded *audit.Record
	for index := range trace.Records {
		record := &trace.Records[index]
		if record.Event.Kind != "managed_root_source_capture_seeded" || record.Event.ID != id {
			continue
		}
		if seeded != nil {
			return ErrManagedLiveRootUnavailable
		}
		seeded = record
	}
	if seeded == nil || seeded.Sequence <= admissionSeq || seeded.Sequence >= releaseSeq || !validManagedTreeDigest(releasedTree) {
		return ErrManagedLiveRootUnavailable
	}
	var evidence struct {
		SchemaVersion          string `json:"schema_version"`
		RunID                  string `json:"run_id"`
		QualificationDigest    string `json:"qualification_digest"`
		SnapshotEvidenceDigest string `json:"snapshot_evidence_digest"`
		XattrEvidenceDigest    string `json:"xattr_evidence_digest"`
		GenerationID           string `json:"generation_id"`
		TreeDigest             string `json:"tree_digest"`
		ManifestDigest         string `json:"manifest_digest"`
	}
	if err := unmarshalGovernedJSON(seeded.Event.Data, &evidence); err != nil ||
		evidence.SchemaVersion != "tbound-managed-root-source-capture-seeded/v1" || evidence.RunID != runID ||
		evidence.GenerationID != "g0" || evidence.TreeDigest != releasedTree {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	return nil
}

func (c *piServiceWorkflowCatalog) BindOwnerScope(admission *productionWorkflowAdmission, ownerEpoch uint64,
	incarnation workflowrecovery.OwnerIncarnation, scopePath, scopeIdentity string) error {
	if c == nil || admission == nil || admission.Catalog != c || ownerEpoch == 0 ||
		ownerEpoch != admission.CandidateEpoch || incarnation != admission.OwnerIncarnation ||
		!validWorkflowOwnerIncarnation(incarnation) || !validDigestText(scopeIdentity) ||
		filepath.Clean(scopePath) != scopePath || filepath.Base(scopePath) != workflowOwnerScopeName(admission.Entry.WorkflowID, ownerEpoch) {
		return ErrPiWorkflowCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, exists := c.entries[admission.Entry.AdmissionRecordHash]
	if !exists || entry.WorkflowID != admission.Entry.WorkflowID || !entry.CheckpointBound || entry.Terminal {
		return ErrPiWorkflowCatalog
	}
	key := workflowOwnerEpochKey(entry.WorkflowID, ownerEpoch)
	if _, exists := c.ownerScopes[key]; exists {
		return ErrPiWorkflowCatalog
	}
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: entry.AdmissionRecordHash,
		WorkflowID: entry.WorkflowID, OwnerEpoch: ownerEpoch, Incarnation: incarnation,
		ScopeName: filepath.Base(scopePath), ScopePath: scopePath, ScopeIdentityDigest: scopeIdentity}
	if err := c.appendLocked("pi_workflow_owner_scope_bound", workflowOwnerScopeEventID("bound", entry.WorkflowID, ownerEpoch), event); err != nil {
		return err
	}
	c.ownerScopes[key] = piServiceWorkflowOwnerScope{AdmissionHash: entry.AdmissionRecordHash,
		WorkflowID: entry.WorkflowID, OwnerEpoch: ownerEpoch, Incarnation: incarnation,
		ScopeName: filepath.Base(scopePath), ScopePath: scopePath, ScopeIdentityDigest: scopeIdentity}
	return nil
}

// MarkOwnerScopeDrained is called only after the held D09 scope was removed or
// the host drain verifier killed it and observed populated=0. The path absence
// check catches an unlink race before the append.
func (c *piServiceWorkflowCatalog) MarkOwnerScopeDrained(admissionHash, workflow string, ownerEpoch uint64,
	incarnation workflowrecovery.OwnerIncarnation, cgroupRoot string, proofKind string) error {
	if c == nil || !validBareWorkflowDigest(admissionHash) || !validIdentityText(workflow) || ownerEpoch == 0 ||
		!validWorkflowOwnerIncarnation(incarnation) || filepath.Clean(cgroupRoot) != cgroupRoot ||
		proofKind != "session_cleanup_complete" && proofKind != "host_process_exit_and_cgroup_kill" {
		return ErrPiWorkflowCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, exists := c.entries[admissionHash]
	key := workflowOwnerEpochKey(workflow, ownerEpoch)
	state, bound := c.ownerScopes[key]
	if !exists || entry.WorkflowID != workflow || !bound || state.AdmissionHash != admissionHash || state.Drained ||
		state.Incarnation != incarnation {
		return ErrPiWorkflowCatalog
	}
	if _, err := os.Lstat(state.ScopePath); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	proofDigest := workflowDrainProofDigest(workflow, ownerEpoch, incarnation, state.ScopePath,
		state.ScopeIdentityDigest, proofKind)
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash,
		WorkflowID: workflow, OwnerEpoch: ownerEpoch, Incarnation: incarnation,
		ScopeName: state.ScopeName, ScopePath: state.ScopePath, ScopeIdentityDigest: state.ScopeIdentityDigest,
		DrainProofDigest: proofDigest}
	if err := c.appendLocked("pi_workflow_owner_scope_drained", workflowOwnerScopeEventID("drained", workflow, ownerEpoch), event); err != nil {
		return err
	}
	state.Drained = true
	c.ownerScopes[key] = state
	return nil
}

// MarkOwnerUnexposedDrained records the complete binder cleanup path that
// returned before it created any worker scope or exposed worker authority.
func (c *piServiceWorkflowCatalog) MarkOwnerUnexposedDrained(admissionHash, workflow string, ownerEpoch uint64,
	incarnation workflowrecovery.OwnerIncarnation, cgroupRoot string) error {
	if c == nil || !validBareWorkflowDigest(admissionHash) || !validIdentityText(workflow) || ownerEpoch == 0 ||
		!validWorkflowOwnerIncarnation(incarnation) || filepath.Clean(cgroupRoot) != cgroupRoot {
		return ErrPiWorkflowCatalog
	}
	name := workflowOwnerScopeName(workflow, ownerEpoch)
	path := filepath.Join(cgroupRoot, name)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, exists := c.entries[admissionHash]
	key := workflowOwnerEpochKey(workflow, ownerEpoch)
	if !exists || entry.WorkflowID != workflow {
		return ErrPiWorkflowCatalog
	}
	if _, duplicate := c.ownerScopes[key]; duplicate {
		return ErrPiWorkflowCatalog
	}
	proofDigest := workflowDrainProofDigest(workflow, ownerEpoch, incarnation, "", "", "binder_cleanup_without_worker_scope")
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash,
		WorkflowID: workflow, OwnerEpoch: ownerEpoch, Incarnation: incarnation,
		ScopeName: name, DrainProofDigest: proofDigest, Unexposed: true}
	if err := c.appendLocked("pi_workflow_owner_scope_drained", workflowOwnerScopeEventID("drained", workflow, ownerEpoch), event); err != nil {
		return err
	}
	c.ownerScopes[key] = piServiceWorkflowOwnerScope{AdmissionHash: admissionHash,
		WorkflowID: workflow, OwnerEpoch: ownerEpoch, Incarnation: incarnation,
		ScopeName: name, Drained: true, Unexposed: true}
	return nil
}

// catalogOwnerDrainVerifier verifies superseded owner epochs for the Pi workflow
// catalog. Each owner is observed and then proved with drainproof. The verifier
// returns the drain-proof digest per owner, and the protected root persists it as
// drain evidence.
type catalogOwnerDrainVerifier struct {
	catalog          *piServiceWorkflowCatalog
	cgroupRoot       string
	cgroupRootDigest string
	containment      string
	settlement       string
	// openScope opens one exact owner scope. It is nil in production, which uses
	// piruntime.OpenLinuxCgroupScopeForDrain. Tests supply a fake with the same
	// open contract, because a delegated cgroup v2 root is not available to them.
	openScope func(root *os.File, name, containment, settlement, expectedIdentity string) (ownerScopeDrainer, error)
}

// ownerScopeDrainer is the part of a retained cgroup scope that the drain verifier uses.
type ownerScopeDrainer interface {
	RootDigest() string
	IdentityDigest() string
	Remove(ctx context.Context) error
	Close() error
}

func (v catalogOwnerDrainVerifier) openOwnerScope(root *os.File, name, expectedIdentity string) (ownerScopeDrainer, error) {
	if v.openScope != nil {
		return v.openScope(root, name, v.containment, v.settlement, expectedIdentity)
	}
	scope, err := piruntime.OpenLinuxCgroupScopeForDrain(root, name, v.containment, v.settlement, expectedIdentity)
	if err != nil {
		return nil, err
	}
	return scope, nil
}

func (v catalogOwnerDrainVerifier) VerifyOwnerCellsDrainedWithProof(ctx context.Context, workflow string,
	owners []workflowrecovery.OwnerEpochIdentity) ([]string, error) {
	if ctx == nil || v.catalog == nil || !validIdentityText(workflow) || len(owners) == 0 ||
		filepath.Clean(v.cgroupRoot) != v.cgroupRoot || !filepath.IsAbs(v.cgroupRoot) ||
		!validDigestText(v.cgroupRootDigest) || v.containment != piruntime.D09ContainmentProfile ||
		v.settlement != piruntime.D09SettlementProfile {
		return nil, workflowrecovery.ErrOwnerFence
	}
	entry, ok := v.catalog.workflowEntry(workflow)
	settledTerminal := entry.Terminal && validIdentityText(entry.TerminalRunID) &&
		validDigestText(entry.TerminalProofDigest) && entry.TerminalProofSequence > 0 &&
		boundedProductionIdentity(entry.TerminalTransactionID, productionRequestMaxBytes)
	if !ok || !entry.CheckpointBound || entry.Terminal && !settledTerminal {
		return nil, errors.Join(workflowrecovery.ErrOwnerFence, ErrPiWorkflowCatalog)
	}
	digests := make([]string, 0, len(owners))
	priorEpoch := uint64(0)
	for _, owner := range owners {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if owner.Epoch <= priorEpoch || !validWorkflowOwnerIncarnation(owner.Incarnation) {
			return nil, workflowrecovery.ErrOwnerFence
		}
		priorEpoch = owner.Epoch
		digest, err := v.drainOwnerEpoch(ctx, entry, owner)
		if err != nil {
			return nil, fmt.Errorf("owner epoch %d drain proof is incomplete: %w", owner.Epoch, err)
		}
		digests = append(digests, digest)
	}
	return digests, nil
}

// drainOwnerEpoch proves that one superseded owner epoch is drained and returns
// the drain-proof digest of the observed facts. A live owner process is refused
// first, whatever the scope state. A drained catalog record is the cleanup
// receipt for an absent scope. An undrained epoch is torn down only when its
// scope is bound to this epoch, and it is receipted before the proof is built.
func (v catalogOwnerDrainVerifier) drainOwnerEpoch(ctx context.Context, entry piServiceWorkflowEntry,
	owner workflowrecovery.OwnerEpochIdentity) (string, error) {
	state, exists := v.catalog.ownerScope(entry.WorkflowID, owner.Epoch)
	if exists && (state.AdmissionHash != entry.AdmissionRecordHash || state.Incarnation != owner.Incarnation) {
		return "", ErrPiWorkflowCatalog
	}
	process, err := workflowOwnerProcessObservation(owner.Incarnation)
	if err != nil {
		return "", err
	}
	if process.Live {
		return "", errors.New("superseded owner process is still live and has no completed cleanup proof")
	}
	name := workflowOwnerScopeName(entry.WorkflowID, owner.Epoch)
	if exists && state.Drained {
		if _, err := os.Lstat(filepath.Join(v.cgroupRoot, name)); !errors.Is(err, os.ErrNotExist) {
			return "", errors.Join(ErrPiWorkflowCatalog, err)
		}
		if state.Unexposed {
			// The binder cleaned this owner up before any worker scope was created, so
			// the receipt has no scope identity. It is a distinct never-bound basis, and
			// a receipt that also carries a binding is inconsistent and refused.
			// The proof digest binds the workflow and scope name instead, so two workflows
			// at one epoch under one owner never share it.
			if state.ScopeIdentityDigest != "" || state.ScopePath != "" || state.ScopeName != name {
				return "", ErrPiWorkflowCatalog
			}
			return proveOwnerDrain(owner, process,
				drainproof.ScopeObservation{Absent: true, NeverBound: true,
					WorkflowID: entry.WorkflowID, ScopeName: name}, "", "")
		}
		// The durable drained record is the cleanup receipt. It names the scope it
		// bound, so the receipt identity and the expected identity are that binding.
		return proveOwnerDrain(owner, process,
			drainproof.ScopeObservation{Absent: true, IdentityDigest: state.ScopeIdentityDigest},
			state.ScopeIdentityDigest, state.ScopeIdentityDigest)
	}
	root, err := openServiceDirectoryNoSymlinks(v.cgroupRoot)
	if err != nil {
		return "", err
	}
	defer root.Close()
	// An unbound scope is opened with no expectation, exactly as the recovery path
	// always did. Its identity is captured below, before teardown.
	scope, openErr := v.openOwnerScope(root, name, state.ScopeIdentityDigest)
	if openErr != nil {
		if errors.Is(openErr, syscall.ENOENT) {
			return "", errors.New("superseded owner scope is absent without a durable cleanup receipt")
		}
		return "", openErr
	}
	if scope.RootDigest() != v.cgroupRootDigest {
		_ = scope.Close()
		return "", errors.New("retained owner scope is under a different delegated cgroup root")
	}
	scopeIdentity := scope.IdentityDigest()
	if !validDigestText(scopeIdentity) {
		_ = scope.Close()
		return "", errors.New("retained owner scope identity could not be captured before teardown")
	}
	if err := scope.Remove(ctx); err != nil {
		return "", err
	}
	// Remove returned nil, so the scope settled and was unlinked. The absence is
	// observed again rather than assumed from that return value.
	if _, err := os.Lstat(filepath.Join(v.cgroupRoot, name)); !errors.Is(err, os.ErrNotExist) {
		return "", errors.Join(ErrPiWorkflowCatalog, err)
	}
	if !exists {
		// A dead owner's unbound scope is the scope a crash left between creation and
		// binding. It is now removed, so bind it to this epoch, then record the receipt.
		if err := v.catalog.bindRecoveredOwnerScope(entry, owner, name, filepath.Join(v.cgroupRoot, name), scopeIdentity); err != nil {
			return "", err
		}
	}
	if err := v.catalog.MarkOwnerScopeDrained(entry.AdmissionRecordHash, entry.WorkflowID, owner.Epoch,
		owner.Incarnation, v.cgroupRoot, "host_process_exit_and_cgroup_kill"); err != nil {
		return "", err
	}
	// The receipt is durable and names the identity captured before teardown. For a
	// bound scope that identity equals the recorded binding, which the open checked.
	// For an unbound scope the binding was just recorded from that same identity, so
	// the expectation is that identity.
	expectedIdentity := state.ScopeIdentityDigest
	if !exists {
		expectedIdentity = scopeIdentity
	}
	return proveOwnerDrain(owner, process,
		drainproof.ScopeObservation{Absent: true, IdentityDigest: scopeIdentity},
		scopeIdentity, expectedIdentity)
}

// proveOwnerDrain builds drainproof's observation of one superseded owner epoch
// and returns its proof digest. Every drainproof refusal is an owner-fence refusal.
func proveOwnerDrain(owner workflowrecovery.OwnerEpochIdentity, process workflowOwnerProcess,
	scope drainproof.ScopeObservation, receiptIdentity, expectedIdentity string) (string, error) {
	proof, err := drainproof.Prove(drainproof.OwnerObservation{
		Owner:                  owner,
		BootID:                 owner.Incarnation.BootID,
		ObservedBootID:         process.BootID,
		StartTicks:             owner.Incarnation.StartTimeTicks,
		ObservedStartTicks:     process.StartTicks,
		ProcessLive:            process.Live,
		Scope:                  scope,
		CleanupReceiptIdentity: receiptIdentity,
		ExpectedScopeIdentity:  expectedIdentity,
		// Pending effects are not observed here. The catalog keeps no pending-effect
		// count per owner epoch, so 0 is recorded. drainproof's pending-effect refusal
		// (OPEN question Q2) therefore does not apply at this boundary.
		PendingEffects: 0,
	})
	if err != nil {
		return "", fmt.Errorf("%w: owner epoch %d: %w", workflowrecovery.ErrOwnerFence, owner.Epoch, err)
	}
	return proof.Digest, nil
}

func (c *piServiceWorkflowCatalog) bindRecoveredOwnerScope(entry piServiceWorkflowEntry,
	owner workflowrecovery.OwnerEpochIdentity, name, path, identity string) error {
	if c == nil || !validDigestText(identity) || name != workflowOwnerScopeName(entry.WorkflowID, owner.Epoch) ||
		filepath.Base(path) != name || filepath.Clean(path) != path {
		return ErrPiWorkflowCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := workflowOwnerEpochKey(entry.WorkflowID, owner.Epoch)
	if _, exists := c.ownerScopes[key]; exists {
		return ErrPiWorkflowCatalog
	}
	current, ok := c.entries[entry.AdmissionRecordHash]
	if !ok || current.WorkflowID != entry.WorkflowID {
		return ErrPiWorkflowCatalog
	}
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: entry.AdmissionRecordHash,
		WorkflowID: entry.WorkflowID, OwnerEpoch: owner.Epoch, Incarnation: owner.Incarnation,
		ScopeName: name, ScopePath: path, ScopeIdentityDigest: identity}
	if err := c.appendLocked("pi_workflow_owner_scope_bound", workflowOwnerScopeEventID("bound", entry.WorkflowID, owner.Epoch), event); err != nil {
		return err
	}
	c.ownerScopes[key] = piServiceWorkflowOwnerScope{AdmissionHash: entry.AdmissionRecordHash,
		WorkflowID: entry.WorkflowID, OwnerEpoch: owner.Epoch, Incarnation: owner.Incarnation,
		ScopeName: name, ScopePath: path, ScopeIdentityDigest: identity}
	return nil
}

func (c *piServiceWorkflowCatalog) workflowEntry(workflow string) (piServiceWorkflowEntry, bool) {
	if c == nil {
		return piServiceWorkflowEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		if entry.WorkflowID == workflow {
			return clonePiServiceWorkflowEntry(entry), true
		}
	}
	return piServiceWorkflowEntry{}, false
}

func (c *piServiceWorkflowCatalog) ownerScope(workflow string, epoch uint64) (piServiceWorkflowOwnerScope, bool) {
	if c == nil {
		return piServiceWorkflowOwnerScope{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.ownerScopes[workflowOwnerEpochKey(workflow, epoch)]
	return state, ok
}

func (c *piServiceWorkflowCatalog) ownerScopesForAdmission(admissionHash, workflow string) []piServiceWorkflowOwnerScope {
	if c == nil || !validBareWorkflowDigest(admissionHash) || !validIdentityText(workflow) {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var scopes []piServiceWorkflowOwnerScope
	for _, scope := range c.ownerScopes {
		if scope.AdmissionHash == admissionHash && scope.WorkflowID == workflow {
			scopes = append(scopes, scope)
		}
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].OwnerEpoch < scopes[j].OwnerEpoch })
	return scopes
}

func workflowOwnerScopeName(workflow string, epoch uint64) string {
	if workflow == "" || epoch == 0 {
		return ""
	}
	return "pi-wf-" + strings.TrimPrefix(digestBytes([]byte(workflow)), "sha256:")[:24] + "-e" + strconv.FormatUint(epoch, 10)
}

func workflowOwnerEpochKey(workflow string, epoch uint64) string {
	return workflow + "\x00" + strconv.FormatUint(epoch, 10)
}

func workflowOwnerScopeEventID(kind, workflow string, epoch uint64) string {
	return "pi-workflow-owner-scope-" + kind + ":" + strings.TrimPrefix(digestBytes([]byte(workflow)), "sha256:")[:24] + ":" + strconv.FormatUint(epoch, 10)
}

func workflowDrainProofDigest(workflow string, epoch uint64, incarnation workflowrecovery.OwnerIncarnation,
	path, identity, proofKind string) string {
	encoded, _ := json.Marshal(struct {
		Workflow string                            `json:"workflow"`
		Epoch    uint64                            `json:"epoch"`
		Inc      workflowrecovery.OwnerIncarnation `json:"incarnation"`
		Path     string                            `json:"path"`
		Identity string                            `json:"scope_identity"`
		Proof    string                            `json:"proof_kind"`
	}{workflow, epoch, incarnation, path, identity, proofKind})
	return digestBytes(encoded)
}

// workflowOwnerProcess is the host's observation of a superseded owner process.
// Live is true only when the process exists on the recorded boot with the recorded
// start ticks. That is the identity drainproof checks.
type workflowOwnerProcess struct {
	BootID     string
	StartTicks uint64
	Live       bool
}

func workflowOwnerProcessObservation(incarnation workflowrecovery.OwnerIncarnation) (workflowOwnerProcess, error) {
	if incarnation.PID <= 0 || incarnation.StartTimeTicks == 0 {
		return workflowOwnerProcess{}, workflowrecovery.ErrOwnerFence
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return workflowOwnerProcess{}, err
	}
	observed := workflowOwnerProcess{BootID: strings.TrimSpace(string(bootID))}
	if observed.BootID != incarnation.BootID {
		// A different boot means the recorded process is gone.
		return observed, nil
	}
	path := filepath.Join("/proc", strconv.Itoa(incarnation.PID), "stat")
	readStart := func() (uint64, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		closeParen := strings.LastIndexByte(string(data), ')')
		if closeParen < 0 || closeParen+1 >= len(data) {
			return 0, errors.New("owner process stat is malformed")
		}
		fields := strings.Fields(string(data[closeParen+1:]))
		if len(fields) <= 19 {
			return 0, errors.New("owner process stat lacks its start time")
		}
		start, err := strconv.ParseUint(fields[19], 10, 64)
		if err != nil || start == 0 {
			return 0, errors.New("owner process stat has an invalid start time")
		}
		return start, nil
	}
	first, err := readStart()
	if errors.Is(err, os.ErrNotExist) {
		return observed, nil
	}
	if err != nil {
		return workflowOwnerProcess{}, err
	}
	second, err := readStart()
	if errors.Is(err, os.ErrNotExist) {
		return observed, nil
	}
	if err != nil {
		return workflowOwnerProcess{}, err
	}
	if first != second {
		return workflowOwnerProcess{}, errors.New("owner PID identity changed while verifying retirement")
	}
	observed.StartTicks = first
	observed.Live = first == incarnation.StartTimeTicks
	return observed, nil
}

func (c *piServiceWorkflowCatalog) appendLocked(kind, id string, event piServiceWorkflowEvent) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := c.journal.Append(audit.Event{Kind: kind, ID: id, Data: encoded}); err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	return nil
}

func (c *piServiceWorkflowCatalog) Close() error {
	if c == nil || c.journal == nil {
		return nil
	}
	return c.journal.Close()
}

func piServiceRegistrationIdentity(registration piruntime.D09ManagedLiveRootRegistration,
	socket piServiceSocketRegistration, contract piServiceRegistrationContract) string {
	encoded, err := json.Marshal(struct {
		Registration piruntime.D09ManagedLiveRootRegistration `json:"registration"`
		Socket       piServiceSocketRegistration              `json:"socket"`
		Contract     piServiceRegistrationContract            `json:"contract"`
	}{registration, socket, contract})
	if err != nil {
		return ""
	}
	return digestBytes(encoded)
}

func catalogEntryMatchesAdmission(entry piServiceWorkflowEntry, admission piServiceAdmissionContext,
	configDigest, registrationID string) bool {
	return entry.SchemaVersion == workflowCatalogSchema &&
		entry.AdmissionRecordHash == admission.AcceptedRecord.Hash && entry.AdmissionSequence == admission.AcceptedRecord.Sequence &&
		entry.PeerUID == admission.Accepted.ClientUID && entry.PeerGID == admission.Accepted.ClientGID &&
		entry.PeerPID == admission.Accepted.ClientPID && entry.RequestID == admission.Accepted.RequestID &&
		entry.TaskID == admission.Accepted.TaskID && entry.PayloadDigest == admission.Accepted.PayloadDigest &&
		entry.ServiceConfigDigest == configDigest && entry.ServiceRegistrationID == registrationID &&
		entry.TaskPrompt == admission.Request.Prompt && reflect.DeepEqual(entry.BashOutputPaths, admissionRequestBashPaths(admission.Request))
}

func admissionRequestBashPaths(request piServiceRequest) []string {
	if request.BashOutputPaths == nil {
		return nil
	}
	return cloneDurableOutputScopePaths(*request.BashOutputPaths)
}

func validWorkflowCatalogEntry(entry piServiceWorkflowEntry) bool {
	return entry.SchemaVersion == workflowCatalogSchema && boundedProductionIdentity(entry.HostAdmissionID, productionRequestMaxBytes) &&
		validBareWorkflowDigest(entry.AdmissionRecordHash) && entry.AdmissionSequence > 0 &&
		entry.PeerUID > 0 && entry.PeerGID > 0 && entry.PeerPID > 0 &&
		boundedProductionIdentity(entry.RequestID, productionRequestMaxBytes) && boundedProductionIdentity(entry.TaskID, productionTaskIDMaxBytes) &&
		validDigestText(entry.PayloadDigest) && validDigestText(entry.ServiceConfigDigest) && validDigestText(entry.ServiceRegistrationID) &&
		validIdentityText(entry.WorkflowID) && validIdentityText(entry.RunID) && validIdentityText(entry.SessionID) &&
		validIdentityText(entry.ConversationID) && filepath.Clean(entry.OwnerDBPath) == entry.OwnerDBPath && filepath.IsAbs(entry.OwnerDBPath) &&
		filepath.Clean(entry.StoreRootPath) == entry.StoreRootPath && filepath.IsAbs(entry.StoreRootPath) &&
		filepath.Clean(entry.AdminRootPath) == entry.AdminRootPath && filepath.IsAbs(entry.AdminRootPath) &&
		filepath.Clean(entry.StoreJournalPath) == entry.StoreJournalPath && filepath.IsAbs(entry.StoreJournalPath) &&
		filepath.Clean(entry.ProviderJournalPath) == entry.ProviderJournalPath && filepath.IsAbs(entry.ProviderJournalPath) &&
		filepath.Clean(entry.CommandJournalPath) == entry.CommandJournalPath && filepath.IsAbs(entry.CommandJournalPath) &&
		boundedProductionPrompt(entry.TaskPrompt) && validDigestText(entry.TaskDigest) && validDigestText(entry.PromptDigest) &&
		!entry.Limits.Deadline.IsZero() && entry.Limits.Deadline.Location() == time.UTC &&
		entry.Limits.MaxPreEffectRetries <= 2 && entry.Limits.MaxCleanRestarts <= 2 &&
		entry.Limits.MaxHostCalls > 0 && entry.Limits.MaxTokens > 0 && !entry.ReservedAt.IsZero()
}

func workflowEnvelopeMatchesRequest(config piServiceConfig, request piServiceRequest) bool {
	return request.TaskID == config.WorkflowTaskID && request.Prompt == config.WorkflowPrompt &&
		reflect.DeepEqual(admissionRequestBashPaths(request), config.WorkflowBashOutputPaths)
}

func validWorkflowOwnerIncarnation(value workflowrecovery.OwnerIncarnation) bool {
	return validBareWorkflowDigest(value.Digest) && value.BootID != "" && value.PID > 0 && value.StartTimeTicks > 0 &&
		value.PIDNamespaceInode > 0 && value.EffectiveUID == uint32(os.Geteuid()) &&
		validBareWorkflowDigest(value.CgroupIdentityDigest)
}

func workflowCatalogEventID(kind, admissionHash string) string {
	return "pi-workflow-" + kind + ":" + admissionHash
}

func clonePiServiceWorkflowEntry(entry piServiceWorkflowEntry) piServiceWorkflowEntry {
	entry.BashOutputPaths = append([]string(nil), entry.BashOutputPaths...)
	return entry
}

func requireWorkflowCatalogPath(entry piServiceWorkflowEntry, config piServiceConfig) error {
	if entry.OwnerDBPath != filepath.Join(config.WorkflowOwnerStoreRoot, entry.WorkflowID+".sqlite") ||
		filepath.Dir(entry.StoreRootPath) != productionStoreRootDefault || filepath.Dir(entry.AdminRootPath) != productionAdminRootDefault ||
		filepath.Dir(entry.StoreJournalPath) != productionAuditRootDefault || filepath.Dir(entry.ProviderJournalPath) != productionAuditRootDefault ||
		filepath.Dir(entry.CommandJournalPath) != productionAuditRootDefault || strings.Contains(entry.WorkflowID, string(os.PathSeparator)) {
		return fmt.Errorf("%w: retained host layout does not match the fixed private service roots", ErrPiWorkflowCatalog)
	}
	return nil
}
