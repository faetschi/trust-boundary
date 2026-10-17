//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/usdledger"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workflowrecovery"
)

// readReconcileBootID is a test seam for the boot id the owner drain proofs are observed on.
var readReconcileBootID = func() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(data)), err
}

// piServiceOwnerReconciliation is the owner side of one reconciliation.
type piServiceOwnerReconciliation struct {
	NeverBound bool
	Checkpoint string
	Proofs     []string
	BootID     string
	// ConversationIDs are every provider conversation the workflow can have created, which the
	// provider stage probes.
	ConversationIDs []string
}

// piServiceUnknownReconciler reconciles the workflow behind one unknown request from host
// state alone: the owner (settled checkpoint dispositions, drained owner scopes), the
// publication journal and the provider side (the USD ledger on the research route, the
// provider-bridge journals on the engineering route). Anything it cannot prove is reported as
// unreconciled, and all unreconciled kinds are listed together.
type piServiceUnknownReconciler struct {
	catalog   *piServiceWorkflowCatalog
	route     string // piruntime.D09ManagedPiProviderRoute{Engineering,Research}
	auditRoot string
	// owner proves the owner side of an entry whose checkpoint was bound.
	owner func(ctx context.Context, entry piServiceWorkflowEntry) (piServiceOwnerReconciliation, error)
	// readJournal reads one audit journal. exists is false for a journal that was never created.
	readJournal func(path string) (trace audit.Trace, exists bool, err error)
	// spendUnits lists the USD ledger units of a workflow (research route only).
	spendUnits func(workflowID string, conversationIDs []string, maxTurns uint64) ([]usdledger.UnitInfo, error)
	// publications is the publication-journal snapshot of one admission (see forAdmission). It is
	// nil on a reconciler that reads the journal afresh for every reconciliation.
	publications *publicationSnapshot
}

// publicationSnapshot is one read and verification of the publication journal, indexed by
// workflow. It is filled on first use and lives for one admission only.
type publicationSnapshot struct {
	loaded  bool
	records map[string][]publicationRecord
	err     error
}

// forAdmission returns a view of the reconciler that reads and verifies the publication journal
// once, at its first use, and shares that fresh snapshot among every reconciliation made through
// the view. Nothing is carried from one admission to the next: begin takes a new view each time.
func (r *piServiceUnknownReconciler) forAdmission() piServiceAbandonReconciler {
	if r == nil {
		return r
	}
	scoped := *r
	scoped.publications = &publicationSnapshot{}
	return &scoped
}

// newProductionUnknownReconciler is the reconciler of the running service and of the
// abandon-unknown command, over the same host stores.
func newProductionUnknownReconciler(catalog *piServiceWorkflowCatalog, config piServiceConfig,
	route productionProviderRoute) *piServiceUnknownReconciler {
	r := &piServiceUnknownReconciler{catalog: catalog, route: config.ProviderRoute, auditRoot: productionAuditRootDefault,
		owner: catalog.reconcileProductionOwner, readJournal: readProductionAuditJournal}
	if route.IsResearch() {
		r.spendUnits = route.research.WorkflowUnits
	}
	return r
}

// readProductionAuditJournal reads one journal under the fixed audit root through the same
// ownership gate the settlement verification uses.
func readProductionAuditJournal(path string) (audit.Trace, bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return audit.Trace{}, false, nil
	} else if err != nil {
		return audit.Trace{}, false, err
	}
	journal, err := openPiServiceSettlementJournal(path)
	if err != nil {
		return audit.Trace{}, true, err
	}
	trace, traceErr := journal.Trace()
	return trace, true, errors.Join(traceErr, journal.Close())
}

func unreconciledf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrPiServiceUnreconciled}, args...)...)
}

// problemText is the message of an unreconciled error without the repeated sentinel prefix.
func problemText(err error) string {
	return strings.TrimPrefix(err.Error(), ErrPiServiceUnreconciled.Error()+": ")
}

// Reconcile proves the workflow behind an unknown request quiescent, or returns an error that
// wraps ErrPiServiceUnreconciled and lists everything that is not.
func (r *piServiceUnknownReconciler) Reconcile(ctx context.Context, state piServiceRequestState) (piServiceReconciliation, error) {
	if ctx == nil || ctx.Err() != nil || r == nil || r.catalog == nil || state.terminal == nil || state.terminal.Status != "unknown" ||
		!validBareWorkflowDigest(state.acceptedRecord.Hash) ||
		r.route != piruntime.D09ManagedPiProviderRouteEngineering && r.route != piruntime.D09ManagedPiProviderRouteResearch {
		return piServiceReconciliation{}, unreconciledf("the request is not an unknown terminal of a configured service")
	}
	entry, reserved := r.catalog.admissionEntry(state.acceptedRecord.Hash)
	if !reserved {
		// The catalog never reserved a workflow for this admission, so no owner, publication
		// or provider request exists to reconcile. The proof is the absence, bound to the
		// admission, and it stops holding the moment a workflow appears.
		none := digestBytes([]byte("tbound-pi-service-no-workflow/v1\x00" + state.acceptedRecord.Hash))
		// Nothing was dispatched either, so no exchange lacks a terminal record.
		return piServiceReconciliation{NoWorkflow: true, OwnerCheckpoint: none, Publication: none,
			ProviderRoute: r.route, Provider: none, ProviderSettlement: providerSettlementTerminal}, nil
	}
	var problems []string
	note := func(err error) { problems = append(problems, problemText(err)) }

	var owner piServiceOwnerReconciliation
	switch {
	case !entry.CheckpointBound:
		// No checkpoint was ever bound, so no owner scope may exist, and none was drained.
		if scopes := r.catalog.ownerScopesForAdmission(entry.AdmissionRecordHash, entry.WorkflowID); len(scopes) != 0 {
			note(unreconciledf("owner scope: %d owner scope(s) are bound but no checkpoint was ever bound", len(scopes)))
		}
		owner = piServiceOwnerReconciliation{NeverBound: true, ConversationIDs: []string{entry.ConversationID},
			Checkpoint: digestBytes([]byte("tbound-pi-service-owner-never-bound/v1\x00" + entry.AdmissionRecordHash))}
	case r.owner == nil:
		note(unreconciledf("owner: no owner reconciliation is installed"))
	default:
		var err error
		if owner, err = r.owner(ctx, entry); err != nil {
			note(unreconciledf("owner: %s", problemText(err)))
		}
	}
	conversations := owner.ConversationIDs
	if len(conversations) == 0 {
		conversations = []string{entry.ConversationID}
	}
	publication, err := r.reconcilePublication(entry)
	if err != nil {
		note(err)
	}
	provider, settlement, err := r.reconcileProvider(ctx, entry, conversations)
	if err != nil {
		note(err)
	}
	if len(problems) != 0 {
		return piServiceReconciliation{}, unreconciledf("workflow %s: %s", entry.WorkflowID, strings.Join(problems, "; "))
	}
	return piServiceReconciliation{WorkflowID: entry.WorkflowID, OwnerNeverBound: owner.NeverBound,
		OwnerCheckpoint: owner.Checkpoint, OwnerDrainProofs: owner.Proofs, OwnerDrainBootID: owner.BootID,
		Publication: publication, ProviderRoute: r.route, Provider: provider, ProviderSettlement: settlement}, nil
}

func (r *piServiceUnknownReconciler) reconcilePublication(entry piServiceWorkflowEntry) (string, error) {
	if r.readJournal == nil {
		return "", unreconciledf("publication: no publication journal reader is installed")
	}
	if !validIdentityText(entry.WorkflowID) {
		return "", unreconciledf("publication: the workflow id is not valid")
	}
	if r.publications == nil {
		index, err := r.readPublicationIndex()
		if err != nil {
			return "", err
		}
		return publicationEvidence(index[entry.WorkflowID], entry.WorkflowID)
	}
	// The snapshot of this admission: read and verified once, shared by every reconciliation.
	if !r.publications.loaded {
		r.publications.records, r.publications.err = r.readPublicationIndex()
		r.publications.loaded = true
	}
	if r.publications.err != nil {
		return "", r.publications.err
	}
	return publicationEvidence(r.publications.records[entry.WorkflowID], entry.WorkflowID)
}

// readPublicationIndex reads and verifies the publication journal afresh and indexes its
// records by workflow.
func (r *piServiceUnknownReconciler) readPublicationIndex() (map[string][]publicationRecord, error) {
	// A journal that was never created holds no publication record of any workflow.
	trace, _, err := r.readJournal(filepath.Join(r.auditRoot, "publication-audit.jsonl"))
	if err != nil {
		return nil, unreconciledf("publication: the publication journal cannot be read: %v", err)
	}
	return indexPublicationRecords(trace)
}

// publicationRecord is one record of the publication journal that bears on a workflow.
type publicationRecord struct {
	kind, eventID, hash       string
	root                      workflow.RootIdentity
	registrationDigest, runID string
	ownerEpoch                uint64
}

// indexPublicationRecords parses the publication and managed-root run records of a publication
// journal and groups them by workflow, in journal order. An unreadable record of those kinds
// makes the whole journal unreadable.
func indexPublicationRecords(trace audit.Trace) (map[string][]publicationRecord, error) {
	type wire struct {
		Root               workflow.RootIdentity `json:"root"`
		WorkflowID         string                `json:"workflow_id"`
		OwnerEpoch         uint64                `json:"owner_epoch"`
		RegistrationDigest string                `json:"registration_digest"`
		RunID              string                `json:"run_id"`
	}
	index := make(map[string][]publicationRecord)
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "workflow_publication_pending", "workflow_publication_transaction", "workflow_publication_resolved",
			"managed_live_root_run_admitted", "managed_live_root_run_released", "managed_live_root_run_reconciled":
		default:
			continue
		}
		var event wire
		if err := json.Unmarshal(record.Event.Data, &event); err != nil {
			return nil, unreconciledf("publication: record %d of the publication journal is unreadable", record.Sequence)
		}
		index[event.WorkflowID] = append(index[event.WorkflowID], publicationRecord{kind: record.Event.Kind, eventID: record.Event.ID,
			hash: record.Hash, root: event.Root, registrationDigest: event.RegistrationDigest, runID: event.RunID, ownerEpoch: event.OwnerEpoch})
	}
	return index, nil
}

// workflowPublicationEvidence digests the workflow's records in the publication journal and
// requires every one of its publications to be resolved and every managed-root run it was
// admitted under to be released or reconciled. Records of other workflows, whatever their
// state, are ignored and do not move the digest, which is stable once the workflow is
// quiescent.
func workflowPublicationEvidence(trace audit.Trace, workflowID string) (string, error) {
	if !validIdentityText(workflowID) {
		return "", unreconciledf("publication: the workflow id is not valid")
	}
	index, err := indexPublicationRecords(trace)
	if err != nil {
		return "", err
	}
	return publicationEvidence(index[workflowID], workflowID)
}

// publicationEvidence is workflowPublicationEvidence over the workflow's indexed records.
func publicationEvidence(records []publicationRecord, workflowID string) (string, error) {
	if !validIdentityText(workflowID) {
		return "", unreconciledf("publication: the workflow id is not valid")
	}
	pending := make(map[string]struct{})
	resolved := make(map[string]struct{})
	admitted := make(map[string]struct{})
	ended := make(map[string]struct{})
	digest := sha256.New()
	digest.Write([]byte("tbound-pi-service-publication-evidence/v1\x00"))
	for _, record := range records {
		digest.Write([]byte(record.hash + "\x00"))
		switch record.kind {
		case "workflow_publication_pending":
			pending[record.eventID] = struct{}{}
		case "workflow_publication_resolved":
			resolved[strings.TrimSuffix(record.eventID, "/resolved")] = struct{}{}
		case "managed_live_root_run_admitted":
			admitted[record.eventID] = struct{}{}
		case "managed_live_root_run_released", "managed_live_root_run_reconciled":
			ended[managedLiveRootRunEventID(record.root, record.registrationDigest, record.runID, record.ownerEpoch)] = struct{}{}
		}
	}
	var problems []string
	for id := range pending {
		if _, ok := resolved[id]; !ok {
			problems = append(problems, "publication "+shortEventID(id)+" is pending without a resolution")
		}
	}
	for id := range admitted {
		if _, ok := ended[id]; !ok {
			problems = append(problems, "managed-root run "+shortEventID(id)+" was admitted and never released or reconciled")
		}
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return "", unreconciledf("publication: %s", strings.Join(problems, ", "))
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func shortEventID(id string) string {
	if len(id) > 20 {
		return id[:20]
	}
	return id
}

// reconcileProvider proves that no provider request of the workflow lacks a disposition. The
// research route books every request in the USD ledger: an OPEN reservation has none, an
// UNCERTAIN one is booked at its worst case and a settled one is closed. The engineering route
// has no ledger, so each dispatched request (a recorded host-call reservation) must be followed
// by an exchange or an unknown exchange in the provider-bridge journal of its conversation.
//
// Besides the evidence digest it returns the provider settlement: what that evidence proves.
// "terminal" when every exchange has a terminal record. "bounded-retained-liability" when any
// ledger unit is UNCERTAIN or any provider-bridge exchange is unknown. An UNCERTAIN unit is a
// reservation the ledger kept at its worst case, and provider_exchange_unknown is a local
// disposition, so neither proves that the provider stopped working on the request. Remote
// quiescence cannot be proven from host state and is never claimed.
func (r *piServiceUnknownReconciler) reconcileProvider(_ context.Context, entry piServiceWorkflowEntry,
	conversationIDs []string) (string, string, error) {
	settlement := providerSettlementTerminal
	digest := sha256.New()
	digest.Write([]byte("tbound-pi-service-provider-evidence/v1\x00" + r.route + "\x00"))
	switch r.route {
	case piruntime.D09ManagedPiProviderRouteResearch:
		if r.spendUnits == nil {
			return "", "", unreconciledf("provider: no USD ledger probe is installed for the research route")
		}
		units, err := r.spendUnits(entry.WorkflowID, conversationIDs, entry.Limits.MaxHostCalls)
		if err != nil {
			return "", "", unreconciledf("provider: the USD ledger cannot be probed: %v", err)
		}
		var open []string
		for _, unit := range units {
			switch unit.State {
			case usdledger.UnitStateOpen:
				open = append(open, unit.ID)
			case usdledger.UnitStateUncertain:
				settlement = providerSettlementBoundedRetainedLiability
			}
		}
		if len(open) != 0 {
			return "", "", unreconciledf("provider: %d open USD ledger reservation(s) have no recorded disposition (%s)",
				len(open), strings.Join(open, ", "))
		}
		encoded, err := json.Marshal(units)
		if err != nil {
			return "", "", unreconciledf("provider: the ledger units cannot be encoded: %v", err)
		}
		digest.Write(encoded)
	case piruntime.D09ManagedPiProviderRouteEngineering:
		if r.readJournal == nil {
			return "", "", unreconciledf("provider: no provider journal reader is installed")
		}
		var problems []string
		for _, conversationID := range conversationIDs {
			path := entry.ProviderJournalPath
			if conversationID != entry.ConversationID {
				path = productionRecoveryProviderJournalPath(r.auditRoot, entry.WorkflowID, conversationID)
			}
			if path == "" {
				problems = append(problems, "conversation "+conversationID+" has no valid provider journal path")
				continue
			}
			trace, exists, err := r.readJournal(path)
			if err != nil {
				problems = append(problems, fmt.Sprintf("provider journal %s cannot be read: %v", filepath.Base(path), err))
				continue
			}
			head := ""
			if len(trace.Records) != 0 {
				head = trace.Records[len(trace.Records)-1].Hash
			}
			fmt.Fprintf(digest, "%s\x00%t\x00%d\x00%s\x00", path, exists, len(trace.Records), head)
			if providerJournalHasUnknownExchange(trace) {
				settlement = providerSettlementBoundedRetainedLiability
			}
			if undisposed := providerJournalUndisposed(trace); undisposed != 0 {
				problems = append(problems, fmt.Sprintf("%d dispatched provider request(s) in %s have no recorded disposition",
					undisposed, filepath.Base(path)))
			}
		}
		if len(problems) != 0 {
			return "", "", unreconciledf("provider: %s", strings.Join(problems, "; "))
		}
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), settlement, nil
}

// providerJournalHasUnknownExchange reports whether a provider-bridge journal records an exchange
// whose outcome is unknown: only a local disposition, not proof about the provider.
func providerJournalHasUnknownExchange(trace audit.Trace) bool {
	for _, record := range trace.Records {
		if record.Event.Kind == "provider_exchange_unknown" {
			return true
		}
	}
	return false
}

// providerJournalUndisposed counts the host-call reservations of a provider-bridge journal that
// no later exchange (provider_exchange or provider_exchange_unknown) disposes of.
func providerJournalUndisposed(trace audit.Trace) int {
	undisposed, open := 0, false
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "provider_host_call_reserved":
			if open {
				undisposed++
			}
			open = true
		case "provider_exchange", "provider_exchange_unknown":
			open = false
		}
	}
	if open {
		undisposed++
	}
	return undisposed
}

// reconcileSettledOwner proves the owner side from the workflow's latest settled owner
// checkpoint: it must be the checkpoint the catalog entry authorized, its pins must carry no
// pending effect, host call, pending call or undisposed provider exchange, every owner epoch it
// names must be proven drained by the owner drain verifier (drainproof, with NeverBound receipts
// for owners that were never exposed), and the catalog's owner-scope records must be exactly
// those epochs, all drained and removed.
func (c *piServiceWorkflowCatalog) reconcileSettledOwner(ctx context.Context, entry piServiceWorkflowEntry,
	view workflowrecovery.SettledCheckpointView, drain catalogOwnerDrainVerifier, cgroupRoot string) (piServiceOwnerReconciliation, error) {
	pins := view.Checkpoint.Pins
	if !validBareWorkflowDigest(view.Digest) || view.Digest != view.Checkpoint.ComputedCheckpointDigest ||
		view.OwnerStoreIdentity != entry.OwnerStoreIdentity || view.OwnerStoreIdentity.Path != entry.OwnerDBPath ||
		pins.WorkflowID != entry.WorkflowID || !validBareWorkflowDigest(entry.RootIdentityDigest) ||
		pins.RootIdentityDigest != entry.RootIdentityDigest || view.Checkpoint.Limits != entry.Limits {
		return piServiceOwnerReconciliation{}, unreconciledf("owner checkpoint: the settled checkpoint is not the one the catalog entry authorized")
	}
	var problems []string
	expectedConversationID := entry.ConversationID
	if pins.RecoveryGeneration > 1 {
		expectedConversationID = fmt.Sprintf("%s-conversation-g%d", entry.WorkflowID, pins.RecoveryGeneration)
	}
	if err := validateSettledServiceDisposition(pins, expectedConversationID); err != nil {
		problems = append(problems, "owner checkpoint: pending effect, host call, pending call or provider exchange: "+err.Error())
	}
	proofs, err := drain.VerifyOwnerCellsDrainedWithProof(ctx, entry.WorkflowID, view.OwnerEpochs)
	if err != nil {
		problems = append(problems, "owner scopes not drained: "+err.Error())
	}
	for index, proof := range proofs {
		// drainproof digests are bare hex; the evidence records digest texts.
		if validBareWorkflowDigest(proof) {
			proofs[index] = "sha256:" + proof
		} else if !validDigestText(proof) {
			problems = append(problems, fmt.Sprintf("owner scopes not drained: the drain proof of owner epoch %d is not a digest", index+1))
		}
	}
	if err := c.verifySettledOwnerScopes(entry, view.OwnerEpochs, cgroupRoot); err != nil {
		problems = append(problems, "owner scope records do not match the checkpoint's owner epochs: "+err.Error())
	}
	bootID, err := readReconcileBootID()
	if err != nil || bootID == "" {
		problems = append(problems, "owner scopes: the boot id of the drain proofs cannot be read")
	}
	if len(problems) != 0 {
		return piServiceOwnerReconciliation{}, unreconciledf("%s", strings.Join(problems, "; "))
	}
	conversations := []string{entry.ConversationID}
	seen := map[string]struct{}{entry.ConversationID: {}}
	add := func(id string) {
		if _, duplicate := seen[id]; !duplicate && id != "" {
			seen[id] = struct{}{}
			conversations = append(conversations, id)
		}
	}
	for _, transcript := range pins.ProviderTranscripts {
		add(transcript.ConversationID)
	}
	// A recovery generation that started and died before its first checkpoint may have
	// created the next conversation, so the probe reaches one generation past the checkpoint.
	for generation := uint64(2); generation <= pins.RecoveryGeneration+1; generation++ {
		add(fmt.Sprintf("%s-conversation-g%d", entry.WorkflowID, generation))
	}
	return piServiceOwnerReconciliation{Checkpoint: "sha256:" + view.Digest, Proofs: proofs, BootID: bootID,
		ConversationIDs: conversations}, nil
}

// reconcileProductionOwner loads the entry's settled owner checkpoint through the protected
// root, read-only, and reconciles it against the signed host profile and cell template.
func (c *piServiceWorkflowCatalog) reconcileProductionOwner(ctx context.Context, entry piServiceWorkflowEntry) (piServiceOwnerReconciliation, error) {
	if requireWorkflowCatalogPath(entry, piServiceConfig{WorkflowOwnerStoreRoot: c.ownerRoot()}) != nil {
		return piServiceOwnerReconciliation{}, unreconciledf("owner checkpoint: the catalog entry's retained layout is not the fixed private service roots")
	}
	template, _, err := piruntime.LoadD09CellTemplate(ctx)
	if err != nil {
		return piServiceOwnerReconciliation{}, unreconciledf("owner: the cell template cannot be loaded: %v", err)
	}
	profile, err := piruntime.LoadLinuxSignedHostProfile("")
	if err != nil {
		return piServiceOwnerReconciliation{}, unreconciledf("owner: the host profile cannot be loaded: %v", err)
	}
	if err := validateSettledServiceProfile(profile, template); err != nil {
		return piServiceOwnerReconciliation{}, unreconciledf("owner: %v", err)
	}
	drain := catalogOwnerDrainVerifier{catalog: c, cgroupRoot: template.CgroupsPath,
		cgroupRootDigest: profile.CgroupRootDigest, containment: profile.ContainmentProfile, settlement: profile.SettlementProfile}
	boundary, err := workflowrecovery.NewLinuxHostBoundary(c.ownerRoot(), uint32(os.Geteuid()), drain, c)
	if err != nil {
		return piServiceOwnerReconciliation{}, unreconciledf("owner: the host boundary cannot be built: %v", err)
	}
	protectedRoot, err := workflowrecovery.OpenExistingProtectedRoot(piServiceWorkflowRootDBPath, c.ownerRoot(), boundary)
	if err != nil {
		return piServiceOwnerReconciliation{}, unreconciledf("owner: the protected root cannot be opened: %v", err)
	}
	defer protectedRoot.Close()
	view, err := workflowrecovery.LoadSettledCheckpoint(ctx, entry.OwnerDBPath, protectedRoot, entry.WorkflowID)
	if err != nil {
		return piServiceOwnerReconciliation{}, unreconciledf("owner checkpoint: the settled owner checkpoint cannot be loaded: %v", err)
	}
	return c.reconcileSettledOwner(ctx, entry, view, drain, template.CgroupsPath)
}

// settlementEvidenceFiles names the per-request host files a settlement verification reads:
// the owner database (and its write-ahead log) and the store journal. The shared publication
// journal and the catalog are appended by every request and are not part of a per-request
// fingerprint.
func (c *piServiceWorkflowCatalog) settlementEvidenceFiles(state piServiceRequestState) []string {
	entry, ok := c.admissionEntry(state.acceptedRecord.Hash)
	if !ok {
		return nil
	}
	return []string{entry.OwnerDBPath, entry.OwnerDBPath + "-wal", entry.StoreJournalPath}
}

// settlementEvidenceState is the cheap value of what a settlement verification reads from the
// protected-root database for this request's workflow: its checkpoint and fence head. The
// database and its write-ahead log are written by every workflow, so a file fingerprint of them
// would invalidate every cached prior on every request. The workflow's own rows move only when
// its own checkpoint, fence or owner epoch history does.
func (c *piServiceWorkflowCatalog) settlementEvidenceState(state piServiceRequestState) string {
	entry, ok := c.admissionEntry(state.acceptedRecord.Hash)
	if !ok {
		return ""
	}
	return protectedRootWorkflowState(piServiceWorkflowRootDBPath, entry.WorkflowID)
}

// protectedRootWorkflowState digests, in one read-only transaction, the workflow's row of the
// protected-root state table (owner store identity, checkpoint epoch and digest, fence epoch and
// incarnation, fresh authorization, revision) and its owner epoch history with their drain
// evidence. It is a change detector for a cache, not a proof: the settlement verification still
// authenticates the checkpoint against the root when it runs. A root that cannot be read is its
// own value, so the prior is verified again and refused if it no longer verifies.
func protectedRootWorkflowState(path, workflowID string) string {
	digest, err := readProtectedRootWorkflowState(path, workflowID)
	if err != nil {
		return "unreadable:" + err.Error()
	}
	return digest
}

func readProtectedRootWorkflowState(path, workflowID string) (string, error) {
	query := url.Values{}
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout=5000")
	query.Add("_pragma", "query_only=ON")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String())
	if err != nil {
		return "", err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	hash := sha256.New()
	fmt.Fprintf(hash, "tbound-pi-service-protected-root-state/v1\x00%s\x00", workflowID)
	var storePath, storeDigest, checkpointDigest, checkpointIncarnation, fenceIncarnation, freshDigest string
	var checkpointEpoch, fenceEpoch, revision int64
	err = tx.QueryRowContext(ctx, `SELECT owner_store_path,owner_store_identity_digest,checkpoint_epoch,checkpoint_digest,
		checkpoint_incarnation_digest,fence_epoch,fence_incarnation_digest,fresh_authorization_digest,revision
		FROM workflow_root_state WHERE workflow_id=?`, workflowID).Scan(&storePath, &storeDigest, &checkpointEpoch,
		&checkpointDigest, &checkpointIncarnation, &fenceEpoch, &fenceIncarnation, &freshDigest, &revision)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		hash.Write([]byte("absent\x00"))
	case err != nil:
		return "", err
	default:
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%s\x00%s\x00%d\x00%s\x00%s\x00%d\x00", storePath, storeDigest, checkpointEpoch,
			checkpointDigest, checkpointIncarnation, fenceEpoch, fenceIncarnation, freshDigest, revision)
	}
	rows, err := tx.QueryContext(ctx, `SELECT epoch,incarnation_digest,drained,drain_evidence_digest
		FROM workflow_root_owner_epochs WHERE workflow_id=? ORDER BY epoch`, workflowID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var epoch, drained int64
		var incarnation, drainEvidence string
		if err := rows.Scan(&epoch, &incarnation, &drained, &drainEvidence); err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "epoch\x00%d\x00%s\x00%d\x00%s\x00", epoch, incarnation, drained, drainEvidence)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
