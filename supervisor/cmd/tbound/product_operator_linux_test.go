//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

func TestProductOperatorResolveFrontEnd(t *testing.T) {
	oldUID, oldStop, oldDelegate := productOperatorEUID, productOperatorStopped, productOperatorDelegate
	t.Cleanup(func() {
		productOperatorEUID, productOperatorStopped, productOperatorDelegate = oldUID, oldStop, oldDelegate
	})
	for _, mode := range []string{"root", "non-root", "running", "worker", "reason"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			productOperatorEUID = func() int { return 0 }
			productOperatorStopped = func() error { return nil }
			productOperatorDelegate = func(id, reason string, dry bool, out, stderr io.Writer) error { calls++; return nil }
			// Use the public parser: failures must happen before catalog access/delegation.
			if mode == "non-root" {
				productOperatorEUID = func() int { return 1000 }
			}
			if mode == "running" || mode == "worker" {
				productOperatorStopped = func() error { return errors.New("service or worker is alive") }
			}
			args := []string{"turns", "resolve", "--turn", "psess-id/turn-1", "--reason", "abandon inert work", "--dry-run"}
			if mode == "reason" {
				args = []string{"turns", "resolve", "--turn", "psess-id/turn-1"}
			}
			var out, stderr bytes.Buffer
			err := runProductAdmin(args, &out, &stderr)
			if mode != "root" && (err == nil || calls != 0) {
				t.Fatal("unsafe command accepted")
			}
			if mode == "root" && (err != nil || calls != 1) {
				t.Fatalf("root: %v, calls=%d", err, calls)
			}
		})
	}
}

func TestProductOperatorSandboxCommand(t *testing.T) {
	if os.Getenv("TB_OPERATOR_SANDBOX_CHILD") == "1" {
		var request productOperatorRequest
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Turn != "turn" || request.Reason != "reason" || !request.DryRun {
			t.Fatalf("request: %+v", request)
		}
		return
	}
	old := productOperatorSandboxCommand
	t.Cleanup(func() { productOperatorSandboxCommand = old })
	productOperatorSandboxCommand = func(name string, args ...string) *exec.Cmd {
		if name != "systemd-run" {
			t.Fatal(name)
		}
		joined := strings.Join(args, " ")
		for _, required := range []string{"--wait --collect --pipe --quiet", "NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes", "PrivateTmp=yes", "UMask=0077", "ReadWritePaths=/var/lib/tbound-product", "product turns resolve-sandbox"} {
			if !strings.Contains(joined, required) {
				t.Fatalf("missing sandbox setting %s: %s", required, joined)
			}
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestProductOperatorSandboxCommand$")
		cmd.Env = append(os.Environ(), "TB_OPERATOR_SANDBOX_CHILD=1")
		return cmd
	}
	var out, stderr bytes.Buffer
	if err := sandboxProductOperator("turn", "reason", true, &out, &stderr); err != nil {
		t.Fatalf("sandbox: %v %s", err, stderr.String())
	}
	if err := runProductOperatorSandbox(strings.NewReader(`{}`), io.Discard, io.Discard); err == nil {
		t.Fatal("non-root sandbox request accepted")
	}
}

func TestProductOperatorResolvedReviewRemainsObservable(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	_, turn, err := f.run(t, f.submit(t, "review", productDriverPromptA), productDriverTurnOne(false))
	if err != nil || turn.State != productsession.StateAwaitingReview {
		t.Fatalf("review: %v %+v", err, turn)
	}
	reviewEvents, err := newProductEventProjector(f.env).session(f.currentSession(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.env.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Catalog.MarkTurnUnknown(turn.ID, "review disposition unknown"); err != nil {
		t.Fatal(err)
	}
	unknownEvents, err := newProductEventProjector(f.env).session(f.currentSession(t))
	if err != nil {
		t.Fatal(err)
	}
	f.env.Catalog.Close()
	path := filepath.Join(f.env.StateRoot, "catalog.jsonl")
	if _, err := productsession.OperatorResolve(path, f.env.Registration, turn.ID, "discard inert review", false, func(turn productsession.Turn) (productsession.OperatorEvidence, error) {
		return f.env.operatorEvidence(context.Background(), turn)
	}); err != nil {
		t.Fatal(err)
	}
	reopened := openProductDriverFixture(t, f.root)
	reopened.session = f.session
	events, err := newProductEventProjector(reopened.env).session(reopened.currentSession(t))
	if err != nil {
		t.Fatalf("observe after resolution: %v", err)
	}
	if len(events) != len(unknownEvents)+1 || !reflect.DeepEqual(events[:len(unknownEvents)], unknownEvents) {
		t.Fatal("operator resolution rewrote the issued event prefix")
	}
	for _, cursor := range []uint64{uint64(len(reviewEvents)), uint64(len(unknownEvents))} {
		response := observeWindow(events, cursor)
		last := response.Events[len(response.Events)-1]
		if response.NextCursor <= cursor || last.Outcome != productsession.OutcomeAbandoned {
			t.Fatalf("resume from %d: %+v", cursor, response)
		}
	}
	last := events[len(events)-1]
	if last.Kind != productprotocol.EventTurnTerminal || last.Outcome != productsession.OutcomeAbandoned {
		t.Fatalf("terminal: %+v", last)
	}
}

func TestProductOperatorEvidenceAndAdmission(t *testing.T) {
	for _, mode := range []string{"success", "changed", "held-journal", "dry-run", "missing-checkpoint", "commands", "publication", "provider-truncated"} {
		t.Run(mode, func(t *testing.T) {
			f, id := crashProductSoftRefusal(t, "after-capture-refusal-record")
			if err := f.env.ReconcileAfterRestart(context.Background()); err == nil {
				t.Fatal("expected unknown")
			}
			turn, _ := f.env.Catalog.Turn(id)
			if turn.State != productsession.StateUnknown {
				t.Fatalf("turn: %+v", turn)
			}
			path := filepath.Join(f.env.StateRoot, "catalog.jsonl")
			if mode == "commands" || mode == "publication" {
				journalPath := turn.Link.CommandJournalPath
				kind := "operator-test-command"
				if mode == "publication" {
					journalPath = filepath.Join(f.env.projectPublicationDir(turn.ProjectID), "publication-audit.jsonl")
					kind = "workflow_publication_pending"
				}
				journal, err := audit.Open(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(map[string]string{"workflow_id": turn.Link.WorkflowID})
				if _, err := journal.Append(audit.Event{Kind: kind, ID: turn.Link.WorkflowID, Data: data}); err != nil {
					t.Fatal(err)
				}
				journal.Close()
			}
			if mode == "provider-truncated" {
				if err := os.WriteFile(turn.Link.ProviderJournalPath, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed" {
				writeProductDriverFile(t, f.projectDir, "task.txt", "external change")
			}
			if mode == "missing-checkpoint" {
				if err := os.Rename(filepath.Join(filepath.Dir(turn.Link.StoreJournalPath), "owner-store", "workflow.sqlite"), filepath.Join(f.root, "saved.sqlite")); err != nil {
					t.Fatal(err)
				}
			}
			var held interface{ Close() error }
			if mode == "held-journal" {
				j, err := openExistingRecoveryJournal(turn.Link.ProviderJournalPath)
				if err != nil {
					t.Fatal(err)
				}
				held = j
				defer held.Close()
			}
			f.env.Catalog.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			beforeState := ""
			if mode == "dry-run" {
				beforeState = productOperatorStateSnapshot(t, f.root)
			}
			_, err = productsession.OperatorResolve(path, f.env.Registration, id, "provider capture failed", mode == "dry-run", func(turn productsession.Turn) (productsession.OperatorEvidence, error) {
				return f.env.operatorEvidence(context.Background(), turn)
			})
			if mode != "success" && mode != "dry-run" {
				if err == nil {
					t.Fatal("unsafe evidence accepted")
				}
				t.Log(err)
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("refusal wrote catalog")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "dry-run" {
				after, _ := os.ReadFile(path)
				if string(before) != string(after) {
					t.Fatal("dry run wrote")
				}
				if productOperatorStateSnapshot(t, f.root) != beforeState {
					t.Fatal("dry run changed retained state files")
				}
				return
			}
			reopened := openProductDriverFixture(t, f.root)
			reopened.session = f.session
			if _, _, err := reopened.env.Catalog.CreateSession(f.principal, productsession.CreateSessionRequest{ProjectID: productDriverProject, ProviderProfile: productDriverProfile, PublicationMode: "auto", Budget: f.session.Budget, RequestKey: "new-session"}, turn.BaselineDigest); err != nil {
				t.Fatal(err)
			}
			next := reopened.submit(t, "after-resolution", "continue")
			if next.Turn.PredecessorTurnID != "" {
				t.Fatal("unknown context imported")
			}
		})
	}
}

func productOperatorStateSnapshot(t *testing.T, root string) string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += digestBytes(data)
		}
		files[path] = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestProductOperatorCgroupInventory(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "delegated-command")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "cgroup.procs"), filepath.Join(child, "cgroup.procs")} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := productOperatorEmptyCgroup(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "cgroup.procs"), []byte("12345\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := productOperatorEmptyCgroup(root); err == nil || !strings.Contains(err.Error(), "alive") {
		t.Fatalf("live nested worker: %v", err)
	}
}

func TestProductOperatorRejectsUnresolvedStore(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: "bad", CallID: "bad-write", Tool: "write", Arguments: json.RawMessage(`{"path":"missing/file","content":"x"}`)}}}
	_, turn, err := f.run(t, f.submit(t, "unknown-effect", productDriverPromptA), script)
	if err == nil || turn.State != productsession.StateUnknown {
		t.Fatalf("unresolved fixture: %v %+v", err, turn)
	}
	path := filepath.Join(f.env.StateRoot, "catalog.jsonl")
	f.env.Catalog.Close()
	before, _ := os.ReadFile(path)
	if _, err := productsession.OperatorResolve(path, f.env.Registration, turn.ID, "abandon", false, func(turn productsession.Turn) (productsession.OperatorEvidence, error) {
		return f.env.operatorEvidence(context.Background(), turn)
	}); err == nil {
		t.Fatal("unresolved Store accepted")
	} else {
		t.Log(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("refusal wrote catalog")
	}
}

func TestProductOperatorWorkerRejectsForgedRoot(t *testing.T) {
	if os.Getenv("TB_OPERATOR_SOCKET_CHILD") == "1" {
		if err := runProductOperatorWorker(io.Discard); err == nil || !strings.Contains(err.Error(), "root-authenticated") {
			t.Fatalf("forged root socket: %v", err)
		}
		return
	}
	if os.Geteuid() == 0 {
		t.Fatal("run this test as the required unprivileged WSL test user")
	}
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(sockets[0]), "fake-root"), os.NewFile(uintptr(sockets[1]), "request")
	defer parent.Close()
	defer child.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProductOperatorWorkerRejectsForgedRoot$")
	cmd.Env = append(os.Environ(), "TB_OPERATOR_SOCKET_CHILD=1")
	cmd.ExtraFiles = []*os.File{child}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("socket credential test: %v %s", err, out)
	}
}
