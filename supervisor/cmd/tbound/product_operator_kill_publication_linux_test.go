//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

const productPublicationBashEnv = "TB_PRODUCT_PUBLICATION_BASH"

// TestProductOperatorPublicationCrashChild runs only as the fresh process the matrix below starts. It drives a
// review-mode turn that changes two objects, requests publication and exits without cleanup at the named point.
func TestProductOperatorPublicationCrashChild(t *testing.T) {
	root := os.Getenv(productCrashRootEnv)
	if root == "" {
		t.Skip("fresh-process crash helper")
	}
	point, bash := os.Getenv(productCrashPointEnv), os.Getenv(productPublicationBashEnv) != ""
	var f *productDriverFixture
	prompt, script := productDriverPromptA, productDriverTurnOne(false)
	script.turns = []providerbridge.ScriptTurn{script.turns[0], scopeDriverWrite("task", "task.txt", "updated task\n"), script.turns[1]}
	if bash {
		f, prompt = openBashProductDriverFixture(t, root, productScopeNotes(), []string{}), scopeDriverPrompt
		script = &productDriverScript{turns: []providerbridge.ScriptTurn{
			bashCall("create", `printf 'a\n' > notes/a.txt && printf 'b\n' > notes/b.txt`), scopeDriverFinal("t1", scopeDriverAnswer)}}
	} else {
		f = openProductDriverFixture(t, root)
	}
	f.session.ID = os.Getenv(productCrashSessionEnv)
	f.env.crashAt = func(at string) {
		if at == point {
			os.Exit(productCrashExit)
		}
	}
	f.env.publicationFaults = func(at string, index int) error {
		if at != "after-object-checkpoint" || index == 0 {
			f.env.crash("publication-" + at)
		}
		return nil
	}
	_, turn, err := f.run(t, f.submit(t, "turn-1", prompt), script)
	if err != nil || turn.State != productsession.StateAwaitingReview {
		t.Fatalf("review turn before the crash point %s: %+v %v", point, turn, err)
	}
	_, err = f.resolve(t, turn.ID, "resolve-1", turn.CandidateDigest, productsession.DecisionPublish)
	t.Fatalf("the child reached no crash point %s: %v", point, err)
}

// crashPublicationChild runs the fresh process above against the fixture's root and requires it to exit at point.
func crashPublicationChild(t *testing.T, created *productDriverFixture, point string, bash bool) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestProductOperatorPublicationCrashChild$", "-test.count=1")
	child.Env = append(os.Environ(), productCrashRootEnv+"="+created.root, productCrashPointEnv+"="+point,
		productCrashSessionEnv+"="+created.session.ID)
	if bash {
		child.Env = append(child.Env, productPublicationBashEnv+"=1")
	}
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != productCrashExit {
		t.Fatalf("child did not crash at %s: %v\n%s", point, err, output)
	}
}

type publicationLifecycle struct{ pending, resolved, prepared, terminal int }

// readPublicationLifecycle counts the lifecycle records of the project's publication journal and returns its bytes.
func readPublicationLifecycle(t *testing.T, f *productDriverFixture, projectID string) (publicationLifecycle, []byte) {
	t.Helper()
	path := filepath.Join(f.env.projectPublicationDir(projectID), "publication-audit.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := audit.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := journal.Trace()
	if err := errors.Join(err, journal.Close()); err != nil {
		t.Fatal(err)
	}
	var counts publicationLifecycle
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "workflow_publication_pending":
			counts.pending++
		case "workflow_publication_resolved":
			counts.resolved++
		case "publication_prepare":
			counts.prepared++
		case "publication_terminal", "publication_aborted":
			counts.terminal++
		}
	}
	return counts, raw
}

// A kill inside publication leaves the project's publication lifecycle record unresolved and the turn unknown, and
// restart reconciliation stays fail-closed. Operator resolution settles it only when the live tree proves that the
// publication cannot have applied: the admitted baseline, whether the kill came before the prepare, before the token
// or before the first object. Anything else is refused, naming the state, and nothing is written.
func TestProductOperatorResolvesAKillInsidePublicationOnlyWhenProvablyUnapplied(t *testing.T) {
	for _, c := range []struct {
		point, refusal string
		measured       string // "baseline", "candidate" or "partial"
		prepared       int    // prepare records the kill left
		unbound        int    // staging directories without a checkpointed identity that the resolution must leave in place and report
		kept           int    // identity-bound staging directories that recovery keeps after a consumed token
		overwriteOnly  bool   // the hook runs only for a turn that overwrites a file
	}{
		{"publication-after-source-validation", "", "baseline", 0, 0, 0, false},
		{"publication-after-prepare-checkpoint", "", "baseline", 1, 0, 0, false},
		{"publication-after-artifact-create", "", "baseline", 1, 1, 0, false},
		{"publication-after-artifact-parent-sync", "", "baseline", 1, 0, 0, false},
		{"publication-after-stage-sync", "", "baseline", 1, 0, 0, false},
		{"publication-after-backup-sync", "", "baseline", 1, 0, 0, true},
		{"publication-after-stage-checkpoint", "", "baseline", 1, 0, 0, false},
		{"publication-after-token-checkpoint", "", "baseline", 1, 0, 1, false},
		{"publication-after-object-checkpoint", "not provably unapplied", "partial", 1, 0, 0, false},
		{"publication-after-checkpoint", "was applied", "candidate", 1, 0, 0, false},
		{"publication-after-terminal-checkpoint", "was applied", "candidate", 1, 0, 0, false},
	} {
		for _, bash := range []bool{true, false} {
			if bash && c.overwriteOnly {
				continue
			}
			t.Run(fmt.Sprintf("%s/bash=%v", c.point, bash), func(t *testing.T) {
				var created *productDriverFixture
				if bash {
					created = newBashProductDriverFixture(t, productsession.PublicationReview, productScopeNotes(), false, []string{}, seedBashProject(t))
				} else {
					created = newProductDriverFixture(t, productsession.PublicationReview)
				}
				baseline := created.measure(t)
				if err := created.env.Catalog.Close(); err != nil {
					t.Fatal(err)
				}
				crashPublicationChild(t, created, c.point, bash)
				open := func() *productDriverFixture {
					var f *productDriverFixture
					if bash {
						f = openBashProductDriverFixture(t, created.root, productScopeNotes(), []string{})
					} else {
						f = openProductDriverFixture(t, created.root)
					}
					f.session = created.session
					return f
				}
				f := open()
				id := f.session.ID + "/turn-1"
				if err := f.env.ReconcileAfterRestart(context.Background()); err == nil {
					t.Fatal("restart reconciliation settled a turn killed inside publication")
				}
				turn := f.turn(t, id)
				if turn.State != productsession.StateUnknown || !f.currentSession(t).Blocked {
					t.Fatalf("after restart: %+v", turn)
				}
				measured := f.measure(t)
				switch c.measured {
				case "baseline":
					if measured != baseline {
						t.Fatal("the kill changed the project")
					}
				case "candidate":
					if measured != turn.CandidateDigest {
						t.Fatal("the project does not hold the candidate")
					}
				case "partial":
					if measured == baseline || measured == turn.CandidateDigest {
						t.Fatal("the project does not hold a partial publication")
					}
				}
				lifecycle, journalBefore := readPublicationLifecycle(t, f, turn.ProjectID)
				if lifecycle.pending != 1 || lifecycle.resolved != 0 || lifecycle.prepared != c.prepared {
					t.Fatalf("the kill left lifecycle %+v", lifecycle)
				}
				stagingDirs := func() []string {
					found, _ := filepath.Glob(filepath.Join(filepath.Dir(f.env.ProjectRoots[turn.ProjectID]), ".tbound-publication-[0-9a-f]*"))
					return found
				}
				stagingBefore := stagingDirs()
				catalogPath := filepath.Join(f.env.StateRoot, "catalog.jsonl")
				catalogBefore, err := os.ReadFile(catalogPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.env.Catalog.Close(); err != nil {
					t.Fatal(err)
				}
				for _, dry := range []bool{true, false} {
					_, err := operatorResolveOnDisk(f, id, dry)
					if c.refusal != "" {
						if err == nil || !strings.Contains(err.Error(), c.refusal) {
							t.Fatalf("dry=%v: want a refusal naming %q, got %v", dry, c.refusal, err)
						}
					} else if err != nil {
						t.Fatalf("dry=%v: %v", dry, err)
					}
					lifecycle, journalAfter := readPublicationLifecycle(t, f, turn.ProjectID)
					catalogAfter, _ := os.ReadFile(catalogPath)
					if c.refusal != "" || dry {
						if !bytes.Equal(journalBefore, journalAfter) || !bytes.Equal(catalogBefore, catalogAfter) {
							t.Fatalf("dry=%v refusal=%q wrote state", dry, c.refusal)
						}
					} else if lifecycle.pending != 1 || lifecycle.resolved != 1 || lifecycle.prepared != c.prepared || lifecycle.terminal != c.prepared {
						t.Fatalf("the pending publication was not closed with its transaction ended: %+v", lifecycle)
					} else if left := stagingDirs(); len(left) != c.unbound+c.kept {
						t.Fatalf("want %d staging directories left in place, found %v", c.unbound+c.kept, left)
					}
					if f.measure(t) != measured {
						t.Fatal("operator resolution changed the project")
					}
				}
				reopened := open()
				got := reopened.turn(t, id)
				if c.refusal != "" {
					if got.State != productsession.StateUnknown || !reopened.currentSession(t).Blocked {
						t.Fatalf("a refused turn changed: %+v", got)
					}
					return
				}
				if got.State != productsession.StateTerminal || got.Outcome != productsession.OutcomeAbandoned || got.ContextEligible ||
					got.Usage != got.Reservation || reopened.currentSession(t).Blocked {
					t.Fatalf("the abandonment did not replay: %+v", got)
				}
				// The unbound staging directory stays in place, and the evidence names it for the operator to remove.
				var wantLeft []string
				if c.unbound != 0 {
					wantLeft = stagingBefore
				}
				if !slices.Equal(got.OperatorResolution.PublicationStagingLeft, wantLeft) {
					t.Fatalf("evidence names staging %q, want %q", got.OperatorResolution.PublicationStagingLeft, wantLeft)
				}
				// The project publishes again, so no pending record is left that could apply later.
				script := &productDriverScript{turns: []providerbridge.ScriptTurn{scopeDriverWrite("t2", "notes.txt", productDriverNotes), scopeDriverFinal("t2", productDriverAnswerA)}}
				if bash {
					script = &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("again", `printf 'c\n' > notes/c.txt`), scopeDriverFinal("t2", scopeDriverAnswer)}}
				}
				_, next, err := reopened.run(t, reopened.submit(t, "turn-2", productDriverPromptA), script)
				if err != nil || next.State != productsession.StateAwaitingReview {
					t.Fatalf("turn after the resolution: %+v %v", next, err)
				}
				if _, err := reopened.resolve(t, next.ID, "resolve-2", next.CandidateDigest, productsession.DecisionPublish); err != nil {
					t.Fatalf("publication after the resolution: %v", err)
				}
				if done := reopened.turn(t, next.ID); done.Outcome != productsession.OutcomePublished || reopened.measure(t) != next.CandidateDigest {
					t.Fatalf("second publication: %+v", done)
				}
				if err := reopened.env.Catalog.Close(); err != nil {
					t.Fatal(err)
				}
				open().turn(t, next.ID) // the catalog replays with both terminals
			})
		}
	}
}

// A resolution that dies after the ledger settled the publication and before the catalog append leaves the unbound
// staging directory with no pending record to find it by. The retry, dry or real, still names the directory while it
// exists, and names nothing once the operator removed it.
func TestProductOperatorRetryReportsTheStagingDirectoryAnInterruptedResolutionLeft(t *testing.T) {
	for _, removedByOperator := range []bool{false, true} {
		t.Run(fmt.Sprintf("removedByOperator=%v", removedByOperator), func(t *testing.T) {
			created := newProductDriverFixture(t, productsession.PublicationReview)
			if err := created.env.Catalog.Close(); err != nil {
				t.Fatal(err)
			}
			crashPublicationChild(t, created, "publication-after-artifact-create", false)
			f := openProductDriverFixture(t, created.root)
			f.session = created.session
			id := f.session.ID + "/turn-1"
			if err := f.env.ReconcileAfterRestart(context.Background()); err == nil {
				t.Fatal("restart reconciliation settled a turn killed inside publication")
			}
			turn := f.turn(t, id)
			staging, _ := filepath.Glob(filepath.Join(filepath.Dir(f.env.ProjectRoots[turn.ProjectID]), ".tbound-publication-[0-9a-f]*"))
			if len(staging) != 1 {
				t.Fatalf("the kill left staging directories %v", staging)
			}
			if err := f.env.Catalog.Close(); err != nil {
				t.Fatal(err)
			}
			catalogPath := filepath.Join(f.env.StateRoot, "catalog.jsonl")
			errInterrupted := errors.New("interrupted after the ledger settled the publication")
			_, err := productsession.OperatorResolve(catalogPath, f.env.Registration, id, "operator test", false,
				func(turn productsession.Turn) (productsession.OperatorEvidence, error) {
					evidence, err := f.env.operatorEvidence(context.Background(), turn)
					settle := evidence.Settle
					evidence.Settle = func() error {
						if err := settle(); err != nil {
							return err
						}
						return errInterrupted
					}
					return evidence, err
				})
			if !errors.Is(err, errInterrupted) {
				t.Fatalf("the interruption did not reach the settled ledger: %v", err)
			}
			if lifecycle, _ := readPublicationLifecycle(t, f, turn.ProjectID); lifecycle.pending != 1 || lifecycle.resolved != 1 {
				t.Fatalf("the ledger was not settled before the interruption: %+v", lifecycle)
			}
			if removedByOperator {
				if err := os.Remove(staging[0]); err != nil {
					t.Fatal(err)
				}
				staging = nil
			}
			var evidence productsession.OperatorEvidence
			if _, err := productsession.OperatorResolve(catalogPath, f.env.Registration, id, "operator test", true,
				func(turn productsession.Turn) (productsession.OperatorEvidence, error) {
					var err error
					evidence, err = f.env.operatorEvidence(context.Background(), turn)
					return evidence, err
				}); err != nil {
				t.Fatalf("dry-run retry: %v", err)
			}
			if !slices.Equal(evidence.PublicationStagingLeft, staging) {
				t.Fatalf("dry-run retry names staging %q, want %q", evidence.PublicationStagingLeft, staging)
			}
			if note := publicationNote(evidence.PublicationClosed, evidence.PublicationStagingLeft, true); (len(staging) != 0) != strings.Contains(note, ".tbound-publication-") {
				t.Fatalf("dry-run retry note %q does not match staging %q", note, staging)
			}
			if _, err := operatorResolveOnDisk(f, id, false); err != nil {
				t.Fatalf("real retry: %v", err)
			}
			got := openProductDriverFixture(t, created.root).turn(t, id)
			if got.State != productsession.StateTerminal || got.Outcome != productsession.OutcomeAbandoned {
				t.Fatalf("the retry did not resolve the turn: %+v", got)
			}
			if !slices.Equal(got.OperatorResolution.PublicationStagingLeft, staging) {
				t.Fatalf("abandonment record names staging %q, want %q", got.OperatorResolution.PublicationStagingLeft, staging)
			}
		})
	}
}

// The operator is told which staging directory a resolution leaves behind, whether or not the resolution closed a
// record itself, and nothing about publication when there is neither.
func TestPublicationNoteNamesTheStagingDirectoriesLeftForTheOperator(t *testing.T) {
	left := []string{"/srv/p/.tbound-publication-aa"}
	if note := publicationNote([]string{"event"}, left, false); !strings.Contains(note, "1 pending publication record(s) closed as unapplied") || !strings.Contains(note, left[0]) {
		t.Fatalf("real note: %q", note)
	}
	if note := publicationNote([]string{"event"}, left, true); !strings.Contains(note, "would be closed") || !strings.Contains(note, left[0]) {
		t.Fatalf("dry-run note: %q", note)
	}
	if note := publicationNote([]string{"event"}, nil, false); strings.Contains(note, "staging") {
		t.Fatalf("a note without a left directory mentions staging: %q", note)
	}
	if note := publicationNote(nil, left, false); strings.Contains(note, "pending publication record") || !strings.Contains(note, left[0]) {
		t.Fatalf("a retry that closed nothing must still name the staging directory: %q", note)
	}
	if note := publicationNote(nil, nil, false); note != "" {
		t.Fatalf("a resolution with no publication to report mentions it: %q", note)
	}
}
