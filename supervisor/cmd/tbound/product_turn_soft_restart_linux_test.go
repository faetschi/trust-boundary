//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

type softRefusalUnsettledRunner struct{ productTurnCommandRunner }

func (r softRefusalUnsettledRunner) Run(context.Context, *sessionrepo.CommandView, sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	os.Exit(productCrashExit)
	panic("unreachable")
}

func TestProductTurnSoftRefusalUnsettledEffectChild(t *testing.T) {
	root := os.Getenv(productCrashRootEnv)
	if root == "" {
		t.Skip("fresh-process crash helper")
	}
	f := openBashProductDriverFixture(t, root, productScopeNotes(), []string{})
	f.session.ID = os.Getenv(productCrashSessionEnv)
	newRunner := f.env.NewCommandRunner
	f.env.NewCommandRunner = func(c productTurnCommandContext) (productTurnCommandRunner, error) {
		runner, err := newRunner(c)
		return softRefusalUnsettledRunner{runner}, err
	}
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{
		{ResponseID: "soft-response", CallID: "soft-call", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)},
		bashCall("unsettled", `printf 'inside\n' > notes/a.txt`),
	}}
	_, _, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
	t.Fatalf("did not crash inside the unsettled effect: %v", err)
}

func TestProductTurnSoftRefusalRestartWithUnsettledEffectIsUnknown(t *testing.T) {
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
	if err := f.env.Catalog.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProductTurnSoftRefusalUnsettledEffectChild$", "-test.count=1")
	child.Env = append(os.Environ(), productCrashRootEnv+"="+f.root, productCrashSessionEnv+"="+f.session.ID)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != productCrashExit {
		t.Fatalf("child did not leave an unsettled effect: %v\n%s", err, output)
	}
	reopened := openBashProductDriverFixture(t, f.root, productScopeNotes(), []string{})
	reopened.session = f.session
	err = reopened.env.ReconcileAfterRestart(context.Background())
	turn := reopened.turn(t, f.session.ID+"/turn-1")
	if err == nil || !strings.Contains(err.Error(), "unresolved") || turn.State != productsession.StateUnknown || turn.UnknownReason == "" || !reopened.currentSession(t).Blocked {
		t.Fatalf("unsettled effect after refusal needs UNKNOWN and a reason: turn=%+v err=%v", turn, err)
	}
}

func crashProductSoftRefusal(t *testing.T, point string) (*productDriverFixture, string) {
	t.Helper()
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	if err := f.env.Catalog.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProductTurnCrashChild$", "-test.count=1")
	child.Env = append(os.Environ(), productCrashRootEnv+"="+f.root, productCrashPointEnv+"="+point, productCrashSessionEnv+"="+f.session.ID)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != productCrashExit {
		t.Fatalf("child missed %s: %v\n%s", point, err, output)
	}
	reopened := openProductDriverFixture(t, f.root)
	reopened.session = f.session
	return reopened, f.session.ID + "/turn-1"
}

func TestProductTurnSoftRefusalRestartNeverResumes(t *testing.T) {
	for _, point := range []string{"before-soft-refusal-record", "after-soft-refusal-record", "after-soft-refusal-retirement"} {
		t.Run(point, func(t *testing.T) {
			f, id := crashProductSoftRefusal(t, point)
			err := f.env.ReconcileAfterRestart(context.Background())
			turn := f.turn(t, id)
			if point == "after-soft-refusal-retirement" {
				if err != nil || turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomeFailed {
					t.Fatalf("clean retired refusal: %+v %v", turn, err)
				}
				f.followUp(t, "after-restart", productsession.OutcomeFailed)
			} else if err == nil || turn.State != productsession.StateUnknown {
				t.Fatalf("unretired capture must stay unknown: %+v %v", turn, err)
			}
		})
	}
}

func TestProductTurnSoftRefusalRestartRejectsRechainedEvidence(t *testing.T) {
	testProductTurnSoftRefusalRechainedEvidence(t, "code")
}

func TestProductTurnSoftRefusalRestartRejectsChangedEnvelope(t *testing.T) {
	testProductTurnSoftRefusalRechainedEvidence(t, "envelope")
}

func TestProductTurnSoftRefusalRestartRejectsChangedKind(t *testing.T) {
	testProductTurnSoftRefusalRechainedEvidence(t, "kind")
}

func TestProductTurnSoftRefusalRestartRejectsTruncationWithCleanStore(t *testing.T) {
	testProductTurnSoftRefusalRechainedEvidence(t, "truncate")
}

func testProductTurnSoftRefusalRechainedEvidence(t *testing.T, mutation string) {
	f, id := crashProductSoftRefusal(t, "after-soft-refusal-retirement")
	turn := f.turn(t, id)
	paths, err := productTurnPathsFromLink(*turn.Link)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := openExistingRecoveryJournal(paths.ProviderJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	rewritten, err := audit.Open(paths.ProviderJournalPath + ".tampered")
	if err != nil {
		t.Fatal(err)
	}
	defer rewritten.Close()
	changed := false
	for _, record := range trace.Records {
		if mutation == "truncate" && record.Event.Kind == providerbridge.SoftRefusalEvent {
			changed = true
			break
		}
		if mutation == "envelope" && record.Event.Kind == "provider_exchange" {
			var exchange providerExchangeEvidence
			if err := json.Unmarshal(record.Event.Data, &exchange); err != nil {
				t.Fatal(err)
			}
			// Changing capture metadata also changes the refusal's hash-chain envelope.
			exchange.Response = bytes.ReplaceAll(exchange.Response, []byte(`"created":1`), []byte(`"created":2`))
			record.Event.Data, err = json.Marshal(exchange)
			if err != nil {
				t.Fatal(err)
			}
		}
		if record.Event.Kind == providerbridge.SoftRefusalEvent {
			var e providerbridge.SoftRefusalEvidence
			if err := json.Unmarshal(record.Event.Data, &e); err != nil {
				t.Fatal(err)
			}
			if mutation == "code" {
				e.Code = "edit_old_text_absent"
			}
			if mutation == "kind" {
				record.Event.Kind = "provider_changed_kind"
			}
			e.Result = providerbridge.SoftRefusalText(e.Code, e.Hint)
			record.Event.Data, err = json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			changed = true
		}
		if _, err := rewritten.Append(record.Event); err != nil {
			t.Fatal(err)
		}
	}
	if !changed {
		t.Fatal("no refusal record")
	}
	if err := rewritten.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(paths.ProviderJournalPath+".tampered", paths.ProviderJournalPath); err != nil {
		t.Fatal(err)
	}
	if err := f.env.ReconcileAfterRestart(context.Background()); err == nil || !strings.Contains(err.Error(), "protected checkpoint prefix") {
		t.Fatalf("restart did not reject the changed checkpoint prefix: %v", err)
	}
	if got := f.turn(t, id); got.State != productsession.StateUnknown {
		t.Fatalf("tampered refusal did not block launch: %+v", got)
	}
}
