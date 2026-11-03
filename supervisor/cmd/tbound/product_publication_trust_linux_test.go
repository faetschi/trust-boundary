//go:build linux

package main

import (
	"context"
	"testing"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

// Only the sealed cell is a fixture; runner binding, authority and publication trust are real.
func installProductTrustCell(f *productDriverFixture, cell func(string, piruntime.D09CellTemplate, string) productionD09CommandInvoker) {
	profile, template, _ := productionProofFixture()
	template.CommandExecutable = "/bin/bash"
	template.CommandExecutableDigest = digestBytes([]byte("bash"))
	digest, _ := template.Digest()
	profile.ContainmentProfileDigest = digest
	f.env.NewCommandRunner = func(c productTurnCommandContext) (productTurnCommandRunner, error) {
		return newProductD09CommandRunnerWithFactory(func(lease string) (sessionrepo.CommandRunner, error) {
			return newProductionD09BashRunnerWithInvoker(lease, cell(lease, template, digest), c.Journal,
				c.RunID, c.SessionID, c.WorkflowID, c.ConversationID, profile, template), nil
		}, func(context.Context) error { return nil }), nil
	}
	f.env.OpenCommandHistory = productD09CommandHistory(profile, template)
	f.env.PublicationTrust = func(a *SessionOperationAuthority) workflow.Trust {
		return &productPublicationTrust{env: f.env, authority: a, xattr: func() (workspace.XattrVisibilityAttestation, error) { return f.env.Xattr, nil }}
	}
}

func productTrustCell(lease string, template piruntime.D09CellTemplate, digest string) productionD09CommandInvoker {
	return &sealedD09Cell{leaseID: lease, template: template, templateDigest: digest}
}

func TestProductTrustResolvesOnlyTheTerminalBashLease(t *testing.T) {
	for _, name := range []string{"Bash after edit", "consecutive Bash", "no-diff Bash", "tampered receipt", "missing origin", "pending settlement", "changed baseline", "provider still open"} {
		t.Run(name, func(t *testing.T) {
			f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, []string{"task.txt"}, nil, true)
			installProductTrustCell(f, productTrustCell)
			admission := f.submit(t, "command", "append task")
			calls := []providerbridge.ScriptTurn{bashCall("last", "append task")}
			if name == "Bash after edit" || name == "no-diff Bash" {
				calls = append([]providerbridge.ScriptTurn{{ResponseID: "edit", CallID: "call-edit", Tool: "edit", Arguments: []byte(`{"path":"task.txt","edits":[{"oldText":"turn fixture","newText":"edited"}]}`)}}, calls...)
			}
			if name == "consecutive Bash" {
				calls = append([]providerbridge.ScriptTurn{bashCall("first", "append task")}, calls...)
			}
			if name == "no-diff Bash" {
				installProductTrustCell(f, func(lease string, template piruntime.D09CellTemplate, digest string) productionD09CommandInvoker {
					return &sealedD09Cell{leaseID: lease, template: template, templateDigest: digest, readOnly: true}
				})
			}
			f.script = &productDriverScript{turns: append(calls, scopeDriverFinal("done", scopeDriverAnswer))}
			w, err := f.env.composeProductTurnWorkflow(context.Background(), admission.Turn, productDriverProfile, providerbridge.PredecessorContext{})
			if err != nil {
				t.Fatal(err)
			}
			defer w.close()
			trust := f.env.PublicationTrust(w.authority).(*productPublicationTrust)
			if err := trust.CheckAdmission(context.Background()); err != nil {
				t.Fatal(err)
			}
			worker, _ := newProductTurnWorker(8, func() bool { return false }, func(string) {})
			if name == "provider still open" {
				f.script.onRequest = func(n int) {
					if n != 2 {
						return
					}

					if err := trust.AttestQuiescent(context.Background(), w.projectRoot, w.baseline, w.durable.Tip()); err == nil {
						t.Error("accepted open provider")
					}

				}
			}
			if _, err := f.env.driveProductTurn(context.Background(), admission.Turn, w, worker); err != nil {
				t.Fatal(err)
			}
			bundle, err := w.store.EvidenceBundle()
			if err != nil {
				t.Fatal(err)
			}
			terminal := bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1]
			if name != "pending settlement" {
				if err := w.settleCommands(); err != nil {
					t.Fatal(err)
				}
			}
			origin, err := trust.ResolveTerminalLeaseOrigin(context.Background(), bundle, terminal)
			if err != nil || origin.Kind != delta.OperationProposalCall || origin.CallID != "call-last" {
				t.Fatalf("terminal origin: %+v %v", origin, err)
			}
			request := workflow.EvidenceRequest{Workflow: w.workflowID, OwnerEpoch: 1, Bundle: bundle.Clone()}
			switch name {
			case "tampered receipt":
				request.Bundle.Operations[len(bundle.Operations)-1].CommandRunnerProfile = "altered"
			case "missing origin":
				request.Bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1].Operation.LeaseID = "missing"
				if _, err := trust.ResolveTerminalLeaseOrigin(context.Background(), request.Bundle, request.Bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1]); err == nil {
					t.Error("resolved missing origin")
				}
			case "changed baseline":
				writeProductDriverFile(t, f.projectDir, "task.txt", "external writer\n")
			}
			err = trust.CheckPublicationPrerequisites(context.Background(), request)
			negative := name == "tampered receipt" || name == "missing origin" || name == "pending settlement"
			if (err != nil) != negative {
				t.Fatalf("prerequisites: %v, want refusal=%v", err, negative)
			}
			err = trust.AttestQuiescent(context.Background(), w.projectRoot, w.baseline, w.durable.Tip())
			if (err != nil) != (name == "pending settlement" || name == "changed baseline") {
				t.Fatalf("quiescence: %v", err)
			}
		})
	}
}
