//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// verifyRetainedUsage is the help text of the verify-retained command.
const verifyRetainedUsage = `usage: tbound verify-retained --run-id ID

Verifies, offline and read-only, that the retained Store and D09 command journal of one
completed service run still account for every Bash command the Store ran, promoted or denied
after the effect. Root-only; stop tbound-pi.service first.

It re-derives each settlement from the retained command journal under the installed host
profile and cell template, and checks it against the Store's own audit journal and delta
ledger. It appends, repairs and deletes nothing, starts no cell and contacts no provider. It
does not replay the provider transcript. The profile and template must be those the run
ran under, so verify before the next re-registration cycle.

On success it prints one JSON line and exits 0. On any refusal it prints the cause on stderr
and exits 2.

`

// retainedRun is everything the verification of one run's retained evidence needs. The
// service's own sources fill it (openProductionRetainedRun); a test fills it from a fixture.
type retainedRun struct {
	StoreRootPath      string
	StoreJournalPath   string
	CommandJournalPath string
	// StoreOptions are the Store's pinned policies, limits, xattr attestation and admission scope. Its
	// Journal is opened by verifyRetainedRun.
	StoreOptions sessionrepo.Options
	Profile      piruntime.HostProfile
	Template     piruntime.D09CellTemplate
	WorkflowID   string
	Session      productionRecoveryCommandSession
}

// retainedRunReport is what a successful verification printed: counts are of the Bash settlements the
// command journal and the Store's audit journal and delta ledger agree on, one to one.
type retainedRunReport struct {
	RunID             string `json:"run_id"`
	Verified          bool   `json:"verified"`
	Operations        int    `json:"operations"`
	PromotedBash      int    `json:"promoted_bash"`
	DeniedBash        int    `json:"denied_bash"`
	StoreAuditRecords uint64 `json:"store_audit_records"`
	StoreAuditHead    string `json:"store_audit_head"`
}

// verifyRetainedRun is the offline half of recovery's settlement check, on its own. It opens the
// retained Store through the recovery preview and the Store and command journals as recovery does
// (existing files only), and checks each Bash settlement of the Store against the command journal. It
// never appends: the audit journal has no read-only open, so it takes the same exclusive lock as the
// service and fails while the service runs. The provider transcript is not replayed.
func verifyRetainedRun(run retainedRun) (retainedRunReport, error) {
	storeJournal, err := openExistingRecoveryJournal(run.StoreJournalPath)
	if err != nil {
		return retainedRunReport{}, fmt.Errorf("open the retained Store journal (is tbound-pi.service stopped?): %w", err)
	}
	defer storeJournal.Close()
	root, err := openExistingPrivateRecoveryDirectory(run.StoreRootPath)
	if err != nil {
		return retainedRunReport{}, fmt.Errorf("open the retained Store root: %w", err)
	}
	defer root.Close()
	options := run.StoreOptions
	options.Journal = storeJournal
	managed, err := sessionlaunch.OpenRecoveryPreview(root, options)
	if err != nil {
		return retainedRunReport{}, fmt.Errorf("preview the retained Store: %w", err)
	}
	defer managed.Close()
	bundle, err := managed.Store().EvidenceBundle()
	if err != nil {
		return retainedRunReport{}, fmt.Errorf("read the retained Store evidence: %w", err)
	}
	commandJournal, err := openExistingRecoveryJournal(run.CommandJournalPath)
	if err != nil {
		return retainedRunReport{}, fmt.Errorf("open the retained command journal (is tbound-pi.service stopped?): %w", err)
	}
	defer commandJournal.Close()
	identity, err := recoveryFileIdentityDigest(run.CommandJournalPath, false)
	if err != nil {
		return retainedRunReport{}, err
	}
	commandTrace, err := commandJournal.Trace()
	if err != nil {
		return retainedRunReport{}, err
	}
	commandPin := productionHistoricalCommandPin{IdentityDigest: identity,
		Head: auditHeadHash(commandTrace.Records), RecordCount: uint64(len(commandTrace.Records))}
	history, err := newProductionHistoricalBashHistory([]productionRecoveryCommandJournal{{Journal: commandJournal,
		Path: run.CommandJournalPath, Pin: commandPin, Sessions: []productionRecoveryCommandSession{run.Session}}},
		run.Profile, run.Template, run.WorkflowID, map[string]struct{}{run.Session.ConversationID: {}})
	if err != nil {
		return retainedRunReport{}, err
	}
	historical, err := newGovernedHistoricalStoreVerifier(storeJournal, options.PolicyDigest, options.MetadataPolicyDigest,
		history.VerifySettlement)
	if err != nil {
		return retainedRunReport{}, err
	}
	if err := historical.VerifyBundle(bundle); err != nil {
		return retainedRunReport{}, err
	}
	if err := history.VerifyStoreBundle(bundle); err != nil {
		return retainedRunReport{}, err
	}
	if err := historical.VerifyUnchanged(); err != nil {
		return retainedRunReport{}, err
	}
	if err := history.VerifyUnchanged(); err != nil {
		return retainedRunReport{}, err
	}
	report := retainedRunReport{RunID: run.Session.RunID, Verified: true, Operations: len(bundle.Operations),
		StoreAuditRecords: bundle.AuditRecordCount, StoreAuditHead: bundle.AuditHeadHash}
	for _, operation := range bundle.Operations {
		switch {
		case !settledBashOperation(operation):
		case deniedAfterEffect(operation):
			report.DeniedBash++
		default:
			report.PromotedBash++
		}
	}
	return report, nil
}

// openRetainedRun and verifyRetained are test seams for the host sources and for the verification. The
// release func that openRetainedRun returns drops the exclusion those sources hold (the catalog lock),
// so the command calls it only after the verification.
var (
	openRetainedRun = openProductionRetainedRun
	verifyRetained  = verifyRetainedRun
)

// runVerifyRetained is `tbound verify-retained --run-id ID`, the root-only operator check that a
// completed run's retained evidence still verifies in a fresh process. Stop tbound-pi.service first:
// the journals are flock-held while it runs, and the command is then refused. Nothing is written.
func runVerifyRetained(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound verify-retained", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, verifyRetainedUsage)
		flags.PrintDefaults()
	}
	runID := flags.String("run-id", "", "run id (production-run-...) of the completed service run to verify")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !boundedProductionIdentity(*runID, productionTaskIDMaxBytes) {
		return errors.New("verify-retained needs --run-id and takes no other arguments")
	}
	run, release, err := openRetainedRun(*runID)
	if err != nil {
		return err
	}
	report, err := verifyRetained(run)
	if err := errors.Join(err, release()); err != nil {
		return err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", encoded)
	return err
}

// openProductionRetainedRun resolves the run from the same sources the service uses: the signed
// registration, host profile and cell template, the signed xattr calibration, the signed service
// config and the workflow catalog. The signed inputs are read as root; then the command takes the
// service identity that owns the journals, as abandon-unknown does. The catalog stays open, and so
// locked, until the returned release func runs: a service started meanwhile cannot take it and
// admit another workflow while the retained evidence is read.
func openProductionRetainedRun(runID string) (retainedRun, func() error, error) {
	if operatorEffectiveUID() != 0 {
		return retainedRun{}, nil, errors.New("verify-retained is root-only: run it as root with tbound-pi.service stopped")
	}
	ctx := context.Background()
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return retainedRun{}, nil, errors.Join(ErrPiServiceUnavailable, err)
	}
	profile, err := piruntime.LoadLinuxSignedHostProfile("")
	if err != nil {
		return retainedRun{}, nil, fmt.Errorf("load the signed host profile: %w", err)
	}
	template, _, err := piruntime.LoadD09CellTemplate(ctx)
	if err != nil {
		return retainedRun{}, nil, fmt.Errorf("load the D09 cell template: %w", err)
	}
	calibration, err := piruntime.LoadVerifiedD09ManagedRWXattrCalibrationForRegistration(ctx, registration)
	if err != nil {
		return retainedRun{}, nil, errors.Join(ErrProductionXattrVisibilityUnavailable, err)
	}
	xattrVisibility, err := piruntime.WorkspaceXattrAttestation(calibration.Digest, calibration.Authority)
	if err != nil {
		return retainedRun{}, nil, errors.Join(ErrProductionXattrVisibilityUnavailable, err)
	}
	if err := dropToServiceIdentity(registration); err != nil {
		return retainedRun{}, nil, err
	}
	_, contract, err := serviceSocketRegistrationFromSignedRegistration(registration)
	if err != nil {
		return retainedRun{}, nil, err
	}
	config, err := readProductionPiServiceConfig(contract.ServiceConfigPath, contract.ServiceConfigDigest)
	if err != nil {
		return retainedRun{}, nil, err
	}
	catalog, err := openPiServiceWorkflowCatalog(config.WorkflowCatalogPath)
	if err != nil {
		return retainedRun{}, nil, fmt.Errorf("open the workflow catalog (is tbound-pi.service stopped?): %w", err)
	}
	if config.WorkflowAdmissionMode == piServiceAdmissionCorpus {
		if catalog.corpus, err = loadProductionCorpus(config); err != nil {
			return retainedRun{}, nil, errors.Join(err, catalog.Close())
		}
		// The binding reads the C-DH manifest digest; the provider clients prepareCDH builds are not needed for the Store scope.
		catalog.corpus.cdh = config.CDH
	}
	run, err := retainedRunFromCatalog(catalog, config, runID)
	if err != nil {
		return retainedRun{}, nil, errors.Join(err, catalog.Close())
	}
	run.Profile, run.Template, run.StoreOptions.XattrVisibility = profile, template, xattrVisibility
	return run, catalog.Close, nil
}

// retainedRunFromCatalog is what the catalog entry of the run says: the Store and journal paths, the
// session identity, and the Store's policies and admission scope, the last from the corpus binding of
// the entry. The host profile, template and xattr attestation come from the signed files.
func retainedRunFromCatalog(catalog *piServiceWorkflowCatalog, config piServiceConfig, runID string) (retainedRun, error) {
	entry, ok := catalog.workflowEntryByRunID(runID)
	if !ok {
		return retainedRun{}, fmt.Errorf("%w: no workflow admission has run id %q", ErrPiWorkflowCatalog, runID)
	}
	if err := requireWorkflowCatalogPath(entry, config); err != nil {
		return retainedRun{}, err
	}
	options := sessionrepo.Options{PolicyDigest: digestBytes([]byte(gate.ProductionSessionPolicyLabel)),
		MetadataPolicyDigest: digestBytes([]byte(gate.ProductionMetadataPolicyLabel)),
		Limits:               workspace.DefaultLimits()}
	binding, err := catalog.bindingForEntry(entry)
	if err != nil {
		return retainedRun{}, err
	}
	if binding != nil {
		options.AdmissionScope = binding.scope
	}
	return retainedRun{StoreRootPath: entry.StoreRootPath, StoreJournalPath: entry.StoreJournalPath,
		CommandJournalPath: entry.CommandJournalPath, StoreOptions: options,
		WorkflowID: entry.WorkflowID, Session: productionRecoveryCommandSession{RunID: entry.RunID,
			SessionID: entry.SessionID, ConversationID: entry.ConversationID}}, nil
}
