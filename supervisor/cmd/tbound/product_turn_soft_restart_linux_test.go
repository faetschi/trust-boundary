//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

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
		if record.Event.Kind == providerbridge.SoftRefusalEvent {
			var e providerbridge.SoftRefusalEvidence
			if err := json.Unmarshal(record.Event.Data, &e); err != nil {
				t.Fatal(err)
			}
			e.Code = "edit_old_text_absent"
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
	if err := f.env.ReconcileAfterRestart(context.Background()); err == nil {
		t.Fatal("recomputed-different refusal accepted after restart")
	}
	if got := f.turn(t, id); got.State != productsession.StateUnknown {
		t.Fatalf("tampered refusal did not block launch: %+v", got)
	}
}
