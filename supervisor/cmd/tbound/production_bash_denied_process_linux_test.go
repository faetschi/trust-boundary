//go:build linux

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const d09RecoveryProcessInputsEnv = "TBOUND_TEST_D09_RECOVERY_INPUTS"

type d09RecoveryProcessCase struct {
	Name        string
	Root        string
	CommandPath string
	CommandPin  productionHistoricalCommandPin
	Outcome     string
	Refusal     string
}

type d09RecoveryProcessInputs struct {
	Profile  piruntime.HostProfile
	Template piruntime.D09CellTemplate
	Cases    []d09RecoveryProcessCase
}

func TestProductionD09SettlementsRecoverInFreshProcess(t *testing.T) {
	if inputPath := os.Getenv(d09RecoveryProcessInputsEnv); inputPath != "" {
		data, err := os.ReadFile(inputPath)
		if err != nil {
			t.Fatal(err)
		}
		var inputs d09RecoveryProcessInputs
		if err := json.Unmarshal(data, &inputs); err != nil {
			t.Fatal(err)
		}
		if len(inputs.Cases) != 5 {
			t.Fatalf("fresh process received %d cases, want both controls and three refusals", len(inputs.Cases))
		}
		for _, test := range inputs.Cases {
			t.Run(test.Name, func(t *testing.T) {
				err := verifyD09PersistedRecovery(t, inputs.Profile, inputs.Template, test)
				if test.Refusal == "" {
					if err != nil {
						t.Fatalf("untouched %s run refused: %v", test.Outcome, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), test.Refusal) {
					t.Fatalf("want settlement refusal containing %q, got %v", test.Refusal, err)
				}
			})
		}
		return
	}

	denied, promoted := deniedBashRun(t), promotedBashRun(t)
	inputs := d09RecoveryProcessInputs{Profile: denied.profile, Template: denied.template}
	addCase := func(name string, run *productionBashRun, journal *audit.Journal, refusal string) {
		t.Helper()
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		commandPath := run.commandJournalPath
		if refusal != "" {
			commandPath = filepath.Join(run.root, name+"-command.jsonl")
			data, err := marshalGovernedAuditRecords(trace.Records)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(commandPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		identity, err := recoveryFileIdentityDigest(commandPath, false)
		if err != nil {
			t.Fatal(err)
		}
		inputs.Cases = append(inputs.Cases, d09RecoveryProcessCase{Name: name, Root: run.root,
			CommandPath: commandPath, Outcome: run.operation.Outcome, Refusal: refusal,
			CommandPin: productionHistoricalCommandPin{IdentityDigest: identity,
				Head: auditHeadHash(trace.Records), RecordCount: uint64(len(trace.Records))}})
	}
	addCase("untouched_denied", denied, denied.journals[2], "")
	addCase("untouched_promoted", promoted, promoted.journals[2], "")
	trace, err := denied.journals[2].Trace()
	if err != nil {
		t.Fatal(err)
	}
	if len(settledRecords(t, denied.journals[2])) != 1 {
		t.Fatal("denied control must have exactly one settlement")
	}
	missing := derivedJournal(t, trace, func(_ int, event audit.Event) (audit.Event, bool) {
		return event, event.Kind != productionD09SettledKind
	})
	addCase("missing_settlement", denied, missing, "old Bash settlement is not bound to exactly one retained command owner")
	tampered := derivedJournal(t, trace, func(_ int, event audit.Event) (audit.Event, bool) {
		if event.Kind == productionD09SettledKind {
			var payload productionD09DeniedCommandSettledEvent
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				t.Fatal(err)
			}
			var receipt piruntime.D09CommandReceipt
			if err := json.Unmarshal(payload.Receipt, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt.ExitCode++
			payload.Receipt = mustJSON(receipt)
			event.Data = mustJSON(payload)
		}
		return event, true
	})
	addCase("tampered_receipt", denied, tampered, "validate retained D09 command receipt")
	duplicate := derivedJournal(t, trace, func(_ int, event audit.Event) (audit.Event, bool) { return event, true })
	if _, err := duplicate.Append(settledRecords(t, denied.journals[2])[0].Event); err != nil {
		t.Fatal(err)
	}
	addCase("duplicated_settlement", denied, duplicate, "repeats one Store effect")
	for _, journal := range []*audit.Journal{missing, tampered, duplicate} {
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
	}
	denied.release()
	promoted.release()
	inputPath := filepath.Join(privateGovernedTestRoot(t), "recovery-inputs.json")
	if err := os.WriteFile(inputPath, mustJSON(inputs), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestProductionD09SettlementsRecoverInFreshProcess$", "-test.v")
	child.Env = []string{d09RecoveryProcessInputsEnv + "=" + inputPath, "TMPDIR=" + os.TempDir()}
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh-process recovery: %v\n%s", err, output)
	}
	t.Logf("fresh-process recovery:\n%s", output)
}

func verifyD09PersistedRecovery(t *testing.T, profile piruntime.HostProfile, template piruntime.D09CellTemplate,
	test d09RecoveryProcessCase) error {
	t.Helper()
	storeJournal, err := openExistingRecoveryJournal(filepath.Join(test.Root, "store.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer storeJournal.Close()
	root, err := openExistingPrivateRecoveryDirectory(filepath.Join(test.Root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	managed, err := sessionlaunch.OpenRecoveryPreview(root, sessionrepo.Options{Journal: storeJournal,
		PolicyDigest: bashRunStorePolicy, MetadataPolicyDigest: bashRunMetadataPolicy, Limits: workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: bashRunXattrPolicy,
			Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}})
	if err != nil {
		t.Fatalf("persisted Store preview: %v", err)
	}
	defer managed.Close()
	bundle, err := managed.Store().EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Operations) != 1 || bundle.Operations[0].Outcome != test.Outcome {
		t.Fatalf("persisted Store operation differs from the parent run: %+v", bundle.Operations)
	}
	commandJournal, err := openExistingRecoveryJournal(test.CommandPath)
	if err != nil {
		t.Fatal(err)
	}
	defer commandJournal.Close()
	identity, err := recoveryFileIdentityDigest(test.CommandPath, false)
	if err != nil || identity != test.CommandPin.IdentityDigest {
		t.Fatalf("persisted command journal identity changed: %v", err)
	}
	history, err := newProductionHistoricalBashHistory([]productionRecoveryCommandJournal{{Journal: commandJournal,
		Path: test.CommandPath, Pin: test.CommandPin,
		Sessions: []productionRecoveryCommandSession{{RunID: bashRunRunID, SessionID: bashRunSessionID,
			ConversationID: bashRunConversationID}}}}, profile, template, bashRunWorkflowID,
		map[string]struct{}{bashRunConversationID: {}})
	if err != nil {
		return err
	}
	historical, err := newGovernedHistoricalStoreVerifier(storeJournal, bashRunStorePolicy, bashRunMetadataPolicy, history.VerifySettlement)
	if err != nil {
		return err
	}
	if err := historical.VerifyBundle(bundle); err != nil {
		return err
	}
	if err := history.VerifyStoreBundle(bundle); err != nil {
		return err
	}
	if err := historical.VerifyUnchanged(); err != nil {
		return err
	}
	return history.VerifyUnchanged()
}
