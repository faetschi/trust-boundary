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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
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
}

type piServiceWorkflowEvent struct {
	SchemaVersion       string                              `json:"schema_version"`
	AdmissionHash       string                              `json:"admission_hash"`
	Entry               piServiceWorkflowEntry              `json:"entry,omitempty"`
	OwnerEpoch          uint64                              `json:"owner_epoch,omitempty"`
	Digest              string                              `json:"digest,omitempty"`
	RootIdentity        string                              `json:"root_identity,omitempty"`
	StoreIdentity       workflowrecovery.OwnerStoreIdentity `json:"store_identity,omitempty"`
	Incarnation         workflowrecovery.OwnerIncarnation   `json:"incarnation,omitempty"`
	WorkflowID          string                              `json:"workflow_id,omitempty"`
	ScopeName           string                              `json:"scope_name,omitempty"`
	ScopePath           string                              `json:"scope_path,omitempty"`
	ScopeIdentityDigest string                              `json:"scope_identity_digest,omitempty"`
	DrainProofDigest    string                              `json:"drain_proof_digest,omitempty"`
	Unexposed           bool                                `json:"unexposed,omitempty"`
}

type piServiceWorkflowCatalog struct {
	mu                sync.Mutex
	journal           *audit.Journal
	entries           map[string]piServiceWorkflowEntry
	byHostAdmissionID map[string]string
	ownerScopes       map[string]piServiceWorkflowOwnerScope
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
	catalog := &piServiceWorkflowCatalog{journal: journal, entries: make(map[string]piServiceWorkflowEntry),
		byHostAdmissionID: make(map[string]string), ownerScopes: make(map[string]piServiceWorkflowOwnerScope)}
	trace, err := journal.Trace()
	if err != nil {
		_ = journal.Close()
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	for _, record := range trace.Records {
		var event piServiceWorkflowEvent
		if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil || event.SchemaVersion != workflowCatalogSchema ||
			!validDigestText(event.AdmissionHash) {
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
				event.OwnerEpoch != 1 || !validDigestText(event.Digest) || !validDigestText(event.RootIdentity) ||
				!validDigestText(event.StoreIdentity.Digest) || event.StoreIdentity.Path != entry.OwnerDBPath ||
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
				!entry.CheckpointBound || !validDigestText(event.Digest) || event.Digest == entry.LatestCheckpointDigest {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			entry.LatestCheckpointDigest = event.Digest
			catalog.entries[event.AdmissionHash] = entry
		case "pi_workflow_terminal":
			if record.Event.ID != workflowCatalogEventID("terminal", event.AdmissionHash) || !exists ||
				!entry.CheckpointBound || entry.Terminal || event.Digest != entry.LatestCheckpointDigest {
				_ = journal.Close()
				return nil, ErrPiWorkflowCatalog
			}
			entry.Terminal = true
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
	if !validDigestText(key) {
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
		config.WorkflowRootDBPath != piServiceWorkflowRootDBPath || config.WorkflowOwnerStoreRoot != piServiceOwnerStoreRoot {
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
	if c == nil || ctx == nil || ctx.Err() != nil || ownerEpoch != 1 || !validDigestText(initialDigest) ||
		deadline.IsZero() || deadline.Location() != time.UTC || !validDigestText(rootIdentity) || !validWorkflowOwnerIncarnation(incarnation) ||
		!validDigestText(store.Digest) || store.Path == "" {
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
		receipt.SessionID() != admission.Entry.SessionID || !validDigestText(receipt.CheckpointDigest()) {
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
	if c == nil || !validDigestText(admissionHash) || !validDigestText(previousDigest) || !validDigestText(nextDigest) || previousDigest == nextDigest {
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

func (c *piServiceWorkflowCatalog) MarkTerminal(admissionHash, checkpointDigest string) error {
	if c == nil || !validDigestText(admissionHash) || !validDigestText(checkpointDigest) {
		return ErrPiWorkflowCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[admissionHash]
	if !ok || !entry.CheckpointBound || entry.Terminal || entry.LatestCheckpointDigest != checkpointDigest {
		return ErrPiWorkflowCatalog
	}
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash, Digest: checkpointDigest}
	if err := c.appendLocked("pi_workflow_terminal", workflowCatalogEventID("terminal", admissionHash), event); err != nil {
		return err
	}
	entry.Terminal = true
	c.entries[admissionHash] = entry
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
	if c == nil || !validDigestText(admissionHash) || !validIdentityText(workflow) || ownerEpoch == 0 ||
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
	if c == nil || !validDigestText(admissionHash) || !validIdentityText(workflow) || ownerEpoch == 0 ||
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

type catalogOwnerDrainVerifier struct {
	catalog          *piServiceWorkflowCatalog
	cgroupRoot       string
	cgroupRootDigest string
	containment      string
	settlement       string
}

func (v catalogOwnerDrainVerifier) VerifyOwnerCellsDrained(ctx context.Context, workflow string,
	owners []workflowrecovery.OwnerEpochIdentity) error {
	if ctx == nil || v.catalog == nil || !validIdentityText(workflow) || len(owners) == 0 ||
		filepath.Clean(v.cgroupRoot) != v.cgroupRoot || !filepath.IsAbs(v.cgroupRoot) ||
		!validDigestText(v.cgroupRootDigest) || v.containment != piruntime.D09ContainmentProfile ||
		v.settlement != piruntime.D09SettlementProfile {
		return workflowrecovery.ErrOwnerFence
	}
	entry, ok := v.catalog.workflowEntry(workflow)
	if !ok || !entry.CheckpointBound || entry.Terminal {
		return errors.Join(workflowrecovery.ErrOwnerFence, ErrPiWorkflowCatalog)
	}
	priorEpoch := uint64(0)
	for _, owner := range owners {
		if err := ctx.Err(); err != nil {
			return err
		}
		if owner.Epoch <= priorEpoch || !validWorkflowOwnerIncarnation(owner.Incarnation) {
			return workflowrecovery.ErrOwnerFence
		}
		priorEpoch = owner.Epoch
		if err := v.drainOwnerEpoch(ctx, entry, owner); err != nil {
			return fmt.Errorf("owner epoch %d drain proof is incomplete: %w", owner.Epoch, err)
		}
	}
	return nil
}

func (v catalogOwnerDrainVerifier) drainOwnerEpoch(ctx context.Context, entry piServiceWorkflowEntry,
	owner workflowrecovery.OwnerEpochIdentity) error {
	state, exists := v.catalog.ownerScope(entry.WorkflowID, owner.Epoch)
	if exists && (state.AdmissionHash != entry.AdmissionRecordHash || state.Incarnation != owner.Incarnation) {
		return ErrPiWorkflowCatalog
	}
	processLive, err := workflowOwnerProcessStillLive(owner.Incarnation)
	if err != nil {
		return err
	}
	if exists && state.Drained {
		path := filepath.Join(v.cgroupRoot, workflowOwnerScopeName(entry.WorkflowID, owner.Epoch))
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.Join(ErrPiWorkflowCatalog, err)
		}
		return nil
	}
	if processLive {
		return errors.New("superseded owner process is still live and has no completed cleanup proof")
	}
	root, err := openServiceDirectoryNoSymlinks(v.cgroupRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	name := workflowOwnerScopeName(entry.WorkflowID, owner.Epoch)
	expectedIdentity := ""
	if exists {
		expectedIdentity = state.ScopeIdentityDigest
	}
	scope, openErr := piruntime.OpenLinuxCgroupScopeForDrain(root, name, v.containment, v.settlement, expectedIdentity)
	if openErr != nil {
		if errors.Is(openErr, syscall.ENOENT) {
			return errors.New("superseded owner scope is absent without a durable cleanup receipt")
		}
		return openErr
	}
	if scope.RootDigest() != v.cgroupRootDigest {
		_ = scope.Close()
		return errors.New("retained owner scope is under a different delegated cgroup root")
	}
	scopeIdentity := scope.IdentityDigest()
	if !validDigestText(scopeIdentity) {
		_ = scope.Close()
		return errors.New("retained owner scope identity could not be captured before teardown")
	}
	if err := scope.Remove(ctx); err != nil {
		return err
	}
	if !exists {
		if err := v.catalog.bindRecoveredOwnerScope(entry, owner, name, filepath.Join(v.cgroupRoot, name), scopeIdentity); err != nil {
			return err
		}
	}
	return v.catalog.MarkOwnerScopeDrained(entry.AdmissionRecordHash, entry.WorkflowID, owner.Epoch,
		owner.Incarnation, v.cgroupRoot, "host_process_exit_and_cgroup_kill")
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

func workflowOwnerProcessStillLive(incarnation workflowrecovery.OwnerIncarnation) (bool, error) {
	if incarnation.PID <= 0 || incarnation.StartTimeTicks == 0 {
		return false, workflowrecovery.ErrOwnerFence
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(bootID)) != incarnation.BootID {
		return false, nil
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
		return false, nil
	}
	if err != nil {
		return false, err
	}
	second, err := readStart()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if first != second {
		return false, errors.New("owner PID identity changed while verifying retirement")
	}
	return first == incarnation.StartTimeTicks, nil
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
		validDigestText(entry.AdmissionRecordHash) && entry.AdmissionSequence > 0 &&
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
	return value.Digest != "" && value.BootID != "" && value.PID > 0 && value.StartTimeTicks > 0 &&
		value.PIDNamespaceInode > 0 && value.EffectiveUID == uint32(os.Geteuid()) && value.CgroupIdentityDigest != ""
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
