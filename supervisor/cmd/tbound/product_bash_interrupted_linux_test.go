//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
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
	for _, kind := range []string{"product_command_intent", "product_command_started"} {
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
	for _, mode := range []string{"drained", "undrained", "missing-command-journal", "missing-owner-journal"} {
		t.Run(mode, func(t *testing.T) {
			created := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
			_ = created.env.Catalog.Close()
			child := exec.Command(os.Args[0], "-test.run=^TestProductInterruptedBashCrashChild$", "-test.count=1")
			child.Env = append(os.Environ(), productCrashRootEnv+"="+created.root, productCrashSessionEnv+"="+created.session.ID)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != productCrashExit {
				t.Fatalf("crash: %v\n%s", err, output)
			}
			f := openBashProductDriverFixture(t, created.root, productScopeNotes(), []string{})
			f.session = created.session
			f.env.OpenCommandHistory = openProductD09CommandHistory
			turn := f.turn(t, f.session.ID+"/turn-1")
			ownerPath := filepath.Join(filepath.Dir(turn.Link.StoreJournalPath), productCommandOwnerJournal)
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
				if mode != "drained" {
					if err == nil {
						t.Fatal("missing cleanup evidence accepted")
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
