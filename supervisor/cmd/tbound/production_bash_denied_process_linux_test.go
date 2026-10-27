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
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const d09RecoveryProcessInputsEnv = "TBOUND_TEST_D09_RECOVERY_INPUTS"

type d09RecoveryProcessCase struct {
	Name            string
	Root            string
	CommandPath     string
	CommandIdentity string
	Outcome         string
	Refusal         string
}

type d09RecoveryProcessInputs struct {
	Profile  piruntime.HostProfile
	Template piruntime.D09CellTemplate
	Cases    []d09RecoveryProcessCase
}

// d09RecoveryChildInputs returns the inputs the parent test handed to this fresh process, and whether
// this is that process.
func d09RecoveryChildInputs(t *testing.T) (d09RecoveryProcessInputs, bool) {
	t.Helper()
	inputPath := os.Getenv(d09RecoveryProcessInputsEnv)
	if inputPath == "" {
		return d09RecoveryProcessInputs{}, false
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	var inputs d09RecoveryProcessInputs
	if err := json.Unmarshal(data, &inputs); err != nil {
		t.Fatal(err)
	}
	if len(inputs.Cases) != 6 {
		t.Fatalf("fresh process received %d cases, want both controls and four refusals", len(inputs.Cases))
	}
	return inputs, true
}

// runD09RecoveryChild runs test again as a fresh process over inputs and fails if that process fails.
func runD09RecoveryChild(t *testing.T, inputs d09RecoveryProcessInputs, test string) {
	t.Helper()
	inputPath := filepath.Join(privateGovernedTestRoot(t), "recovery-inputs.json")
	if err := os.WriteFile(inputPath, mustJSON(inputs), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^"+test+"$", "-test.v")
	child.Env = []string{d09RecoveryProcessInputsEnv + "=" + inputPath, "TMPDIR=" + os.TempDir()}
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh-process recovery: %v\n%s", err, output)
	}
	t.Logf("fresh-process recovery:\n%s", output)
}

// buildD09RecoveryProcessInputs persists a denied and a promoted Bash run and, from the denied run's
// command journal, four refusals: the settlement removed, its receipt tampered, the settlement
// duplicated, and a promoted-shape settlement for the denied operation.
func buildD09RecoveryProcessInputs(t *testing.T) d09RecoveryProcessInputs {
	t.Helper()
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
			CommandPath: commandPath, Outcome: run.operation.Outcome, Refusal: refusal, CommandIdentity: identity})
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
	promotedShape := derivedJournal(t, trace, func(_ int, event audit.Event) (audit.Event, bool) {
		if event.Kind == productionD09SettledKind {
			var payload productionD09DeniedCommandSettledEvent
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				t.Fatal(err)
			}
			payload.Operation.Outcome = "success"
			event.Data = mustJSON(productionD09CommandSettledEvent{SchemaVersion: productionD09SettledSchemaSuccess,
				RunID: payload.RunID, SessionID: payload.SessionID, WorkflowID: payload.WorkflowID,
				ConversationID: payload.ConversationID, ProfileID: payload.ProfileID, ProfileDigest: payload.ProfileDigest,
				ProviderCall: payload.ProviderCall, Operation: payload.Operation, Transition: payload.DeniedCandidate,
				ReceiptDigest: payload.ReceiptDigest, Receipt: payload.Receipt})
		}
		return event, true
	})
	addCase("promoted_shape_for_denied", denied, promotedShape, "differs from Store evidence")
	for _, journal := range []*audit.Journal{missing, tampered, duplicate, promotedShape} {
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
	}
	denied.release()
	promoted.release()
	return inputs
}

func TestProductionD09SettlementsRecoverInFreshProcess(t *testing.T) {
	if inputs, child := d09RecoveryChildInputs(t); child {
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
	runD09RecoveryChild(t, buildD09RecoveryProcessInputs(t), "TestProductionD09SettlementsRecoverInFreshProcess")
}

// fixtureRetainedRun is the retainedRun of a persisted bash-run fixture case, under the fixture's policies.
func fixtureRetainedRun(profile piruntime.HostProfile, template piruntime.D09CellTemplate, test d09RecoveryProcessCase) retainedRun {
	return retainedRun{StoreRootPath: filepath.Join(test.Root, "store"), StoreJournalPath: filepath.Join(test.Root, "store.jsonl"),
		CommandJournalPath: test.CommandPath,
		StoreOptions: sessionrepo.Options{PolicyDigest: bashRunStorePolicy, MetadataPolicyDigest: bashRunMetadataPolicy,
			Limits: workspace.DefaultLimits(), XattrVisibility: workspace.XattrVisibilityAttestation{
				ProfileDigest: bashRunXattrPolicy, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}},
		Profile: profile, Template: template, WorkflowID: bashRunWorkflowID,
		Session: productionRecoveryCommandSession{RunID: bashRunRunID, SessionID: bashRunSessionID,
			ConversationID: bashRunConversationID}}
}

func verifyD09PersistedRecovery(t *testing.T, profile piruntime.HostProfile, template piruntime.D09CellTemplate,
	test d09RecoveryProcessCase) error {
	t.Helper()
	identity, err := recoveryFileIdentityDigest(test.CommandPath, false)
	if err != nil || identity != test.CommandIdentity {
		t.Fatalf("persisted command journal identity changed: %v", err)
	}
	report, err := verifyRetainedRun(fixtureRetainedRun(profile, template, test))
	if err != nil {
		return err
	}
	want := retainedRunReport{RunID: bashRunRunID, Verified: true, Operations: 1, PromotedBash: 1}
	if test.Outcome != "success" {
		want.PromotedBash, want.DeniedBash = 0, 1
	}
	report.StoreAuditRecords, report.StoreAuditHead = 0, ""
	if report != want {
		t.Fatalf("persisted Store operation differs from the parent run: %+v, want %+v", report, want)
	}
	return nil
}
