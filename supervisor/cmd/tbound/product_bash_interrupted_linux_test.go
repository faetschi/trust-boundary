//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

type interruptedProductRunner struct {
	productTurnCommandRunner
	t            *testing.T
	c            productTurnCommandContext
	registration string
	lease        string
}

func (r interruptedProductRunner) Run(_ context.Context, view *sessionrepo.CommandView, _ sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	j, err := audit.Open(filepath.Join(r.c.TurnDir, productCommandOwnerJournal))
	if err != nil {
		r.t.Fatal(err)
	}
	owner := productD09ScopeOwner{Schema: "tbound-product-d09-scope-owner/v1", State: "opened", WorkflowID: r.c.WorkflowID, RunID: r.c.RunID,
		RegistrationDigest: r.registration, ProfileDigest: digestBytes([]byte("profile")), RootDigest: digestBytes([]byte("root")),
		ScopeIdentity: digestBytes([]byte("scope")), ScopePath: "/sys/fs/cgroup/system.slice/tbound-product.service/cells/turn-killed", CommandOwnershipVersion: 1}
	if err := persistProductCommandScopeOwner(j, owner); err != nil {
		r.t.Fatal(err)
	}
	command := piruntime.D09ProductCommandOwnership{ContainerName: "tbound-pi-cell-cmd-killed", LeaseID: r.c.RunID, ViewID: view.ID(), ViewInode: 1}
	// The registered lease is the request's lease, not the run identity.
	trace, err := r.c.Journal.Trace()
	if err != nil || len(trace.Records) != 0 {
		r.t.Fatal("unexpected settlement", err)
	}
	command.LeaseID = r.lease
	kinds := []string{"product_command_intent", "product_command_started"}
	if os.Getenv("TB_PRODUCT_INTERRUPT_MODE") == "no-intent" {
		kinds = nil // Store.RunEffect has synced its intent, but the launcher has not recorded ownership yet.
	}
	for _, kind := range kinds {
		if kind == "product_command_started" {
			command.MountNSInode = 42
		}
		raw, _ := json.Marshal(command)
		if _, err := j.Append(audit.Event{Kind: kind, ID: command.ContainerName, Data: raw}); err != nil {
			r.t.Fatal(err)
		}
	}
	_ = j.Close()
	os.Exit(productCrashExit)
	panic("unreachable")
}

func TestProductInterruptedBashCrashChild(t *testing.T) {
	root := os.Getenv(productCrashRootEnv)
	if root == "" {
		t.Skip("fresh-process crash helper")
	}
	f := openBashProductDriverFixture(t, root, productScopeNotes(), []string{})
	f.session.ID = os.Getenv(productCrashSessionEnv)
	factory := f.env.NewCommandRunner
	f.env.NewCommandRunner = func(c productTurnCommandContext) (productTurnCommandRunner, error) {
		r, err := factory(c)
		return &interruptedLeaseRunner{interruptedProductRunner: interruptedProductRunner{productTurnCommandRunner: r, t: t, c: c, registration: f.env.Registration.Digest}}, err
	}
	_, _, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("slow", "sleep 150")}})
	t.Fatalf("did not crash: %v", err)
}

type interruptedLeaseRunner struct{ interruptedProductRunner }

func (r *interruptedLeaseRunner) RegisterLease(view, lease string) error {
	r.lease = lease
	return r.productTurnCommandRunner.RegisterLease(view, lease)
}

func TestProductInterruptedBashRestartAndOperatorResolution(t *testing.T) {
	for _, mode := range []string{"drained", "no-intent", "undrained", "missing-command-journal", "missing-owner-journal", "history-error", "policy-mismatch", "failed-effect"} {
		t.Run(mode, func(t *testing.T) {
			created := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
			_ = created.env.Catalog.Close()
			child := exec.Command(os.Args[0], "-test.run=^TestProductInterruptedBashCrashChild$", "-test.count=1")
			child.Env = append(os.Environ(), productCrashRootEnv+"="+created.root, productCrashSessionEnv+"="+created.session.ID, "TB_PRODUCT_INTERRUPT_MODE="+mode)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != productCrashExit {
				t.Fatalf("crash: %v\n%s", err, output)
			}
			f := openBashProductDriverFixture(t, created.root, productScopeNotes(), []string{})
			f.session = created.session
			f.env.OpenCommandHistory = openProductD09CommandHistory
			if mode == "history-error" {
				f.env.OpenCommandHistory = func(*audit.Journal, productsession.TurnLink) (governedRecoveryCommandVerifier, error) {
					return nil, errors.New("unrelated history failure")
				}
			}
			if mode == "policy-mismatch" {
				f.env.StorePolicyDigest = bareSHA256Digest(digestBytes([]byte("different policy")))
			}
			turn := f.turn(t, f.session.ID+"/turn-1")
			ownerPath := filepath.Join(filepath.Dir(turn.Link.StoreJournalPath), productCommandOwnerJournal)
			if mode == "failed-effect" {
				rewriteInterruptedProductJournal(t, turn.Link.StoreJournalPath, func(event *audit.Event) {
					if event.Kind == "effect_outcome" {
						event.Outcome, event.Data = "unknown", nil
					}
				})
			}
			if mode != "undrained" {
				j, err := audit.OpenExisting(ownerPath)
				if err != nil {
					t.Fatal(err)
				}
				trace, _ := j.Trace()
				var owner productD09ScopeOwner
				_ = json.Unmarshal(trace.Records[0].Event.Data, &owner)
				owner.State = "drained"
				if err := persistProductCommandScopeOwner(j, owner); err != nil {
					t.Fatal(err)
				}
				_ = j.Close()
			}
			if mode == "missing-command-journal" {
				if err := os.Remove(turn.Link.CommandJournalPath); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing-owner-journal" {
				if err := os.Remove(ownerPath); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.env.ReconcileAfterRestart(context.Background()); err == nil {
				t.Fatal("killed effect silently accepted")
			}
			if got := f.turn(t, turn.ID); got.State != productsession.StateUnknown {
				t.Fatalf("restart: %+v", got)
			}
			_ = f.env.Catalog.Close()
			for _, dry := range []bool{true, false} {
				got, err := productsession.OperatorResolve(filepath.Join(f.env.StateRoot, "catalog.jsonl"), f.env.Registration, turn.ID, "discard killed Bash", dry, func(turn productsession.Turn) (productsession.OperatorEvidence, error) {
					return f.env.operatorEvidence(context.Background(), turn)
				})
				if mode != "drained" && mode != "no-intent" {
					if err == nil {
						t.Fatal("missing cleanup evidence accepted")
					}
					if mode == "history-error" && !strings.Contains(err.Error(), "unrelated history failure") {
						t.Fatalf("original failure lost: %v", err)
					}
					if mode == "undrained" && !strings.Contains(err.Error(), "old command-settlement context") {
						t.Fatalf("original history-context failure lost: %v", err)
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if !dry && (got.Outcome != productsession.OutcomeAbandoned || got.ContextEligible || got.Usage != got.Reservation) {
					t.Fatalf("resolution: %+v", got)
				}
			}
			if f.measure(t) != turn.BaselineDigest {
				t.Fatal("live effect")
			}
		})
	}
}

func TestProductInterruptedBashRequiresResolvedEffectReservation(t *testing.T) {
	for _, disposition := range []workflowrecovery.EffectReservationDisposition{
		workflowrecovery.EffectReservationReserved, workflowrecovery.EffectReservationUnknown,
		workflowrecovery.EffectReservationTerminal, workflowrecovery.EffectReservationRetired,
	} {
		pins := workflowrecovery.Pins{EffectReservation: &workflowrecovery.EffectReservationPin{Disposition: disposition}}
		wantError := disposition == workflowrecovery.EffectReservationReserved || disposition == workflowrecovery.EffectReservationUnknown
		if err := productTurnReservationsResolved(pins); (err != nil) != wantError {
			t.Fatalf("reservation %s: %v", disposition, err)
		}
	}
}

// Rechain tampered evidence so refusals test bindings rather than audit hash damage.
func rewriteInterruptedProductJournal(t *testing.T, path string, change func(*audit.Event)) {
	t.Helper()
	j, err := audit.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := j.Trace()
	if closeErr := j.Close(); err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	var encoded bytes.Buffer
	head := ""
	for _, record := range trace.Records {
		change(&record.Event)
		record.PreviousHash = head
		unsigned, err := json.Marshal(struct {
			Version      int         `json:"version"`
			Sequence     uint64      `json:"sequence"`
			PreviousHash string      `json:"previous_hash"`
			Event        audit.Event `json:"event"`
		}{record.Version, record.Sequence, record.PreviousHash, record.Event})
		if err != nil {
			t.Fatal(err)
		}
		record.Hash = bareSHA256Digest(digestBytes(unsigned))
		if err := json.NewEncoder(&encoded).Encode(record); err != nil {
			t.Fatal(err)
		}
		head = record.Hash
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
