//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productdriver"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

func TestProductD09HistoryHandlesEmptyAndMixedProfileJournals(t *testing.T) {
	if root := os.Getenv("TB_PRODUCT_D09_HISTORY_ROOT"); root != "" {
		f := openBashProductDriverFixture(t, root, nil, []string{"task.txt"})
		f.env.OpenCommandHistory = func(j *audit.Journal, link productsession.TurnLink) (governedRecoveryCommandVerifier, error) {
			return openProductD09CommandHistoryAt(os.Getenv("TB_PRODUCT_D09_ARTIFACT_ROOT"), j, link)
		}
		turn, ok := f.env.Catalog.Turn(os.Getenv("TB_PRODUCT_D09_NEXT_TURN"))
		if !ok {
			t.Fatal("missing retained successor")
		}
		if _, err := f.env.predecessorContext(turn, productDriverProfile); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Run("empty without installed artifacts", func(t *testing.T) {
		journal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "commands.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		if history, err := openProductD09CommandHistory(journal, productsession.TurnLink{}); err != nil || history != nil {
			t.Fatalf("empty history: %v %v", history, err)
		}
	})
	t.Run("read write only predecessor", func(t *testing.T) {
		f := newProductDriverFixture(t, productsession.PublicationAuto)
		f.env.OpenCommandHistory = openProductD09CommandHistory
		_, turn, err := f.run(t, f.submit(t, "first", "write notes"), &productDriverScript{turns: []providerbridge.ScriptTurn{
			{ResponseID: "write", CallID: "call-write", Tool: "write", Arguments: []byte(`{"path":"task.txt","content":"notes\n"}`)}, scopeDriverFinal("first", scopeDriverAnswer)}})
		if err != nil || turn.Outcome != productsession.OutcomePublished {
			t.Fatalf("write: %+v %v", turn, err)
		}
		next := f.submit(t, "next", "read notes")
		if _, err := f.env.predecessorContext(next.Turn, productDriverProfile); err != nil {
			t.Fatal(err)
		}
	})
	for _, name := range []string{"mixed", "malformed digest", "unknown schema", "unknown kind"} {
		t.Run(name, func(t *testing.T) {
			journal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "commands.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			for i := 0; i < 2; i++ {
				digest, schema, kind := digestBytes([]byte("profile")), productionD09SettledSchemaSuccess, productionD09SettledKind
				if i == 1 {
					switch name {
					case "mixed":
						digest = digestBytes([]byte("other"))
					case "malformed digest":
						digest = "sha256:../escape"
					case "unknown schema":
						schema = "unknown"
					case "unknown kind":
						kind = "unknown"
					}
				}
				raw, _ := json.Marshal(map[string]string{"schema_version": schema, "profile_digest": digest})
				if _, err := journal.Append(audit.Event{Kind: kind, ID: "event", Data: raw}); err != nil {
					t.Fatal(err)
				}
			}
			_, err = openProductD09CommandHistory(journal, productsession.TurnLink{})
			if err == nil || os.IsNotExist(err) {
				t.Fatalf("journal was not refused before artifact access: %v", err)
			}
		})
	}
	t.Run("historical signed image survives replacement", func(t *testing.T) {
		image := newProductImageFixture(t)
		profile, err := piruntime.VerifyLinuxSignedHostProfile(image.inputs.Profile, mustDecodeImage64(t, image.inputs.ProfileSignature), mustDecodeImage64(t, image.inputs.PublicKey))
		if err != nil {
			t.Fatal(err)
		}
		template, err := piruntime.DecodeProductD09CellTemplate(image.inputs.Template)
		if err != nil {
			t.Fatal(err)
		}
		historyDir := image.path(productsetup.PodmanConfigDir + "/hosts/" + strings.TrimPrefix(profile.Digest, "sha256:"))
		if err := productImageHistory(productImagePlan{root: image.root, inputs: image.inputs}, historyDir); err != nil {
			t.Fatal(err)
		}
		f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, []string{"task.txt"}, nil)
		f.env.NewCommandRunner = func(c productTurnCommandContext) (productTurnCommandRunner, error) {
			return newProductD09CommandRunnerWithFactory(func(lease string) (sessionrepo.CommandRunner, error) {
				return newProductionD09BashRunnerWithInvoker(lease, &sealedD09Cell{leaseID: lease, template: template, templateDigest: profile.ContainmentProfileDigest}, c.Journal,
					c.RunID, c.SessionID, c.WorkflowID, c.ConversationID, profile, template), nil
			}, func(context.Context) error { return nil }), nil
		}
		admission := f.submit(t, "command", "append task")
		f.script = &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("append", "echo bash-stage >> task.txt"), scopeDriverFinal("done", scopeDriverAnswer)}}
		w, err := f.env.composeProductTurnWorkflow(context.Background(), admission.Turn, productDriverProfile, providerbridge.PredecessorContext{})
		if err != nil {
			t.Fatal(err)
		}
		defer w.close()
		if err := f.env.Catalog.LinkTurn(admission.Turn.ID, w.link(providerbridge.PredecessorContext{})); err != nil {
			t.Fatal(err)
		}
		worker, _ := newProductTurnWorker(8, func() bool { return false }, func(string) {})
		work, err := f.env.driveProductTurn(context.Background(), admission.Turn, w, worker)
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := w.store.EvidenceBundle()
		if err != nil {
			t.Fatal(err)
		}
		link := w.link(providerbridge.PredecessorContext{})
		image.write(productsetup.RegistrationPath, []byte("today's different registration is not replay authority"))
		check := func() error {
			h, err := openProductD09CommandHistoryAt(image.root, w.commandJournal, link)
			if err != nil {
				return err
			}
			return h.VerifyStoreBundle(bundle)
		}
		if err := check(); err != nil {
			t.Fatalf("historical image replay: %v", err)
		}
		for _, name := range []string{"registration.json", "host-profile.json.sig", "d09-cell.json", "registration.pub", "seccomp.json", "cosign.pub"} {
			path := filepath.Join(historyDir, name)
			raw, _ := os.ReadFile(path)
			if err := os.WriteFile(path, []byte("tampered"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := check(); err == nil {
				t.Errorf("accepted altered historical %s", name)
			}
			if err := os.WriteFile(path, raw, 0644); err != nil {
				t.Fatal(err)
			}
		}
		if err := check(); err != nil {
			t.Fatal(err)
		}
		// Settle as the driver does: commands first, then a receipt bound to the journals and a terminal catalog record.
		prepared := &productComposedTurn{env: f.env, w: w, worker: worker}
		if err := prepared.SettleCommands(); err != nil {
			t.Fatal(err)
		}
		ev, err := prepared.Evidence(productdriver.EvidenceSettlement)
		if err != nil {
			t.Fatal(err)
		}
		usage := productsession.Budget{HostCalls: ev.HostCalls, Tokens: ev.Tokens, DurationSeconds: 1}
		receipt := productsession.TurnReceipt{SchemaVersion: productsession.ReceiptSchema,
			SessionID: admission.Turn.SessionID, TurnID: admission.Turn.ID, Ordinal: admission.Turn.Ordinal,
			WorkflowID: link.WorkflowID, RunID: link.RunID, ConversationID: link.ConversationID,
			UnverifiedInputs: []string{"runtime_digest", "image_digest", "extension_digest"},
			WorkerKind:       productsession.WorkerSupervisorLoop, WorkerInstance: prepared.WorkerInstance(),
			ProviderJournalHead: ev.ProviderHead, ProviderJournalCount: ev.ProviderCount,
			StoreJournalHead: ev.StoreHead, StoreJournalCount: ev.StoreCount,
			CommandJournalHead: ev.CommandHead, CommandJournalCount: ev.CommandCount,
			BaselineDigest: admission.Turn.BaselineDigest, ResultBaselineDigest: admission.Turn.BaselineDigest,
			Outcome: productsession.OutcomeFailed, Settlement: productsession.SettlementSupervisorLoopClosed,
			Usage: usage, ToolSequence: work.ToolSequence, Denials: work.Denials}
		encoded, err := productdriver.EncodeReceipt(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.env.Catalog.CompleteTurn(admission.Turn.ID, productsession.TurnTerminal{Outcome: receipt.Outcome,
			Settlement: receipt.Settlement, Receipt: encoded, Usage: usage, ResultBaselineDigest: receipt.ResultBaselineDigest,
			ContextEligible: work.FinalAnswer}); err != nil {
			t.Fatal(err)
		}
		if err := w.close(); err != nil {
			t.Fatal(err)
		}
		paths, err := productTurnPathsFromLink(link)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(paths.CommandJournalPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.CommandJournalPath, nil, 0600); err != nil {
			t.Fatal(err)
		}
		retained, err := openRetainedProductTurn(paths, f.env.storeOptions(admission.Turn), func(j *audit.Journal, link productsession.TurnLink) (governedRecoveryCommandVerifier, error) {
			return openProductD09CommandHistoryAt(image.root, j, link)
		})
		if err == nil {
			retained.Close()
			t.Fatal("empty command history authorized retained Bash effects")
		}
		if err := os.WriteFile(paths.CommandJournalPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		next := f.submit(t, "next", "read task")
		if err := f.env.driver().Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.env.Catalog.Close(); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(os.Args[0], "-test.run=^TestProductD09HistoryHandlesEmptyAndMixedProfileJournals$", "-test.v")
		command.Env = append(os.Environ(), "TB_PRODUCT_D09_HISTORY_ROOT="+f.root, "TB_PRODUCT_D09_ARTIFACT_ROOT="+image.root, "TB_PRODUCT_D09_NEXT_TURN="+next.Turn.ID)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fresh process predecessor replay: %v\n%s", err, output)
		}
	})
}
