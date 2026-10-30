//go:build linux

package sessionrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/faultfs"
	"tbound/supervisor/internal/workspace"
)

// EVQ-COMMAND-VIEW-FAULTS (02-process-model section 11): setup, copy-on-write,
// promotion, teardown, head persistence and journal faults are injected through
// the faultfs seam with the errno a full or failing filesystem returns. Every
// case asserts the same fail-closed contract: a typed error, no result, no
// promoted generation in the approved ledger or the head, the sealed input
// unchanged, and a fresh process that either quarantines (an effect outcome is
// unknown) or recovers exactly the committed prefix without replaying anything.

type faultCase struct {
	name string
	site faultfs.Site
	op   faultfs.Op
	nth  int
}

// faultErrnos are the errors the obligation names: ENOSPC and EDQUOT (full),
// EIO (failing device) and EROFS (read-only). EXDEV is added for renames, the
// only operation a cross-device mount boundary can fail.
func faultErrnos(op faultfs.Op) []syscall.Errno {
	errnos := []syscall.Errno{syscall.ENOSPC, syscall.EDQUOT, syscall.EIO, syscall.EROFS}
	if op == faultfs.OpRename {
		errnos = append(errnos, syscall.EXDEV)
	}
	return errnos
}

type faultPlan struct {
	faultCase
	errno       syscall.Errno
	armed       func() bool
	seen, fired int
}

func (p *faultPlan) hook(site faultfs.Site, op faultfs.Op) error {
	if site != p.site || op != p.op || (p.armed != nil && !p.armed()) {
		return nil
	}
	p.seen++
	if p.seen != p.nth {
		return nil
	}
	p.fired++
	return p.errno
}

type faultFixture struct {
	t          *testing.T
	store      *Store
	journal    *audit.Journal
	journalOut string
	authority  *e2eReceiptAuthority
	g0         *Generation
	rootPath   string
	options    Options
}

func newFaultFixture(t *testing.T) *faultFixture {
	t.Helper()
	store, journal, journalPath, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureDir(t, filepath.Join(sourcePath, "build"), 0o755)
	writeFixtureFile(t, filepath.Join(sourcePath, "README.md"), "baseline text\n", 0o644)
	writeFixtureFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}
	return &faultFixture{t: t, store: store, journal: journal, journalOut: journalPath, authority: authority,
		g0: g0, rootPath: store.rootPath, options: store.options}
}

func (f *faultFixture) inject(c faultCase, errno syscall.Errno, armed func() bool) *faultPlan {
	plan := &faultPlan{faultCase: c, errno: errno, armed: armed}
	f.t.Cleanup(faultfs.Install(plan.hook))
	return plan
}

type storeImage struct {
	journal, admission, state []byte
	generations, views        []string
}

func (f *faultFixture) image() storeImage {
	f.t.Helper()
	read := func(path string) []byte {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			f.t.Fatal(err)
		}
		return data
	}
	names := func(path string) []string {
		entries, err := os.ReadDir(path)
		if err != nil {
			f.t.Fatal(err)
		}
		out := make([]string, 0, len(entries))
		for _, entry := range entries {
			out = append(out, entry.Name())
		}
		return out
	}
	return storeImage{
		journal: read(f.journalOut), admission: read(filepath.Join(f.rootPath, admissionJournalName)),
		state:       read(filepath.Join(f.rootPath, repositoryStateName)),
		generations: names(filepath.Join(f.rootPath, "generations")), views: names(filepath.Join(f.rootPath, "views")),
	}
}

func (img storeImage) effects(t *testing.T) []audit.EffectTrace {
	t.Helper()
	trace, err := audit.Verify(bytes.NewReader(img.journal))
	if err != nil {
		t.Fatalf("audit journal does not verify: %v", err)
	}
	return trace.Effects
}

func (img storeImage) admissionRecords(t *testing.T) int {
	t.Helper()
	trace, err := audit.Verify(bytes.NewReader(img.admission))
	if err != nil {
		t.Fatalf("admission journal does not verify: %v", err)
	}
	return len(trace.Records)
}

// requireSealedInput rescans g0 from disk: no fault may modify the sealed input.
func (f *faultFixture) requireSealedInput() {
	f.t.Helper()
	root, err := os.Open(filepath.Join(f.rootPath, "generations", "g0"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer root.Close()
	scan, err := workspace.Scan(root, f.options.workspaceOptions("g0", true))
	if err != nil || scan.TreeDigest != f.g0.TreeDigest() {
		f.t.Fatalf("sealed input g0 changed under the fault: digest=%q want %q err=%v", scan.TreeDigest, f.g0.TreeDigest(), err)
	}
}

// reopen emulates a fresh process: the Store and the journal handle are closed
// and both are opened again from disk.
func (f *faultFixture) reopen() (*Store, error) {
	f.t.Helper()
	_ = f.store.Close()
	_ = f.journal.Close()
	journal, err := audit.Open(f.journalOut)
	if err != nil {
		f.t.Fatalf("reopen audit journal: %v", err)
	}
	f.t.Cleanup(func() { _ = journal.Close() })
	options := f.options
	options.Journal = journal
	store, err := Open(openFixtureRoot(f.t, f.rootPath), options)
	if store != nil {
		f.t.Cleanup(func() { _ = store.Close() })
	}
	return store, err
}

func (f *faultFixture) write() (MutationResult, error) {
	op, decision := proposal("fault-write"), allow("fault-write")
	content := []byte("approved text\n")
	request := OperationRequest{Tool: "write", Operation: op, Decision: decision, InputGeneration: f.g0.ID(),
		InputTreeDigest: f.g0.TreeDigest(), ArgumentDigest: writeArgumentsDigestForTest("README.md", content),
		OutputScopePaths: []string{"README.md"}}
	f.authority.grant(request)
	f.authority.grantBinding(decision.ID, exactBinding(f.g0.snapshot, fixtureSnapshot(f.t, "g1", "approved text\n", "baseline result\n"), request, 1))
	return f.store.Write(context.Background(), f.g0, op, decision, "README.md", content)
}

var faultCommandSpec = CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "printf 'command output\\n' > build/result.txt"},
	DeclaredOutputPaths: []string{"build/result.txt"}}

func (f *faultFixture) bash(runner CommandRunner) (MutationResult, error) {
	op, decision := lease("fault-bash"), allow("fault-bash")
	viewID := reserveTestViewID(f.t, f.store)
	arguments, err := commandArgumentsDigest(faultCommandSpec)
	if err != nil {
		f.t.Fatal(err)
	}
	contextDigest, err := commandExecutionContextDigest(viewID, f.g0.TreeDigest(), op.LeaseID)
	if err != nil {
		f.t.Fatal(err)
	}
	request := OperationRequest{Tool: "bash", Operation: op, Decision: decision, InputGeneration: f.g0.ID(),
		InputTreeDigest: f.g0.TreeDigest(), ArgumentDigest: arguments, ViewID: viewID,
		OutputScopePaths: []string{"build/result.txt"}, ExecutionContextDigest: contextDigest}
	f.authority.grant(request)
	f.authority.grantBinding(decision.ID, exactBinding(f.g0.snapshot, fixtureSnapshot(f.t, "g1", "baseline text\n", "command output\n"), request, 1))
	return f.store.RunBash(context.Background(), f.g0, op, decision, faultCommandSpec, runner)
}

// countingRunner runs the authorized command in the view and counts entries.
func countingRunner(calls *int) CommandRunner {
	return CommandRunnerFunc(func(ctx context.Context, view *CommandView, spec CommandSpec) (CommandResult, CommandSettlement, error) {
		*calls++
		cmd := exec.CommandContext(ctx, spec.Executable, spec.Args...)
		cmd.Dir = view.testPath()
		if err := cmd.Run(); err != nil {
			return CommandResult{}, CommandSettlement{}, err
		}
		result := CommandResult{ExitCode: 0, ExitObserved: true}
		settled := CommandSettlement{LeaseID: "lease-fault-bash", ViewID: view.ID(), ProcessScopeEmpty: true,
			WritersStopped: true, MountDetached: true, ExitObserved: true, ExitCode: 0, EvidenceClass: "synthetic-test-fixture"}
		return result, settled, nil
	})
}

func forEachFault(t *testing.T, cases []faultCase, run func(t *testing.T, c faultCase, errno syscall.Errno)) {
	t.Helper()
	for _, c := range cases {
		for _, errno := range faultErrnos(c.op) {
			t.Run(c.name+"/"+errnoName(errno), func(t *testing.T) { run(t, c, errno) })
		}
	}
}

func errnoName(errno syscall.Errno) string {
	for name, candidate := range map[string]syscall.Errno{"ENOSPC": syscall.ENOSPC, "EDQUOT": syscall.EDQUOT,
		"EIO": syscall.EIO, "EROFS": syscall.EROFS, "EXDEV": syscall.EXDEV} {
		if candidate == errno {
			return name
		}
	}
	return errno.Error()
}

func requireNoStage(t *testing.T, img storeImage) {
	t.Helper()
	for _, name := range img.generations {
		if strings.HasPrefix(name, ".stage-") {
			t.Fatalf("staging directory %q survived the fault", name)
		}
	}
}

func sameImageExceptViews(t *testing.T, before, after storeImage) {
	t.Helper()
	if !bytes.Equal(before.journal, after.journal) || !bytes.Equal(before.admission, after.admission) ||
		!bytes.Equal(before.state, after.state) || fmt.Sprint(before.generations) != fmt.Sprint(after.generations) {
		t.Fatalf("a setup fault changed durable state:\n journal %d->%d bytes\n admission %d->%d\n state equal=%t\n generations %v->%v",
			len(before.journal), len(after.journal), len(before.admission), len(after.admission),
			bytes.Equal(before.state, after.state), before.generations, after.generations)
	}
}

// Command-view setup (before authorization, intent and runner launch): the
// view is cleaned up, nothing becomes durable, and the same operation can then
// run, which shows that no half-built view or consumed identity is left behind.
func TestCommandViewSetupFaultsFailClosedWithIdempotentCleanup(t *testing.T) {
	cases := []faultCase{
		{"view-mkdir", faultfs.SiteViewSetup, faultfs.OpMkdir, 1},
		{"import-mkdir", faultfs.SiteWorkspaceImport, faultfs.OpMkdir, 1},
		{"import-create-first", faultfs.SiteWorkspaceImport, faultfs.OpCreate, 1},
		{"import-create-second", faultfs.SiteWorkspaceImport, faultfs.OpCreate, 2},
		{"import-write-first", faultfs.SiteWorkspaceImport, faultfs.OpWrite, 1},
		{"import-write-second", faultfs.SiteWorkspaceImport, faultfs.OpWrite, 2},
		{"import-fsync-first", faultfs.SiteWorkspaceImport, faultfs.OpFsync, 1},
		{"view-tree-fsync", faultfs.SiteViewSetup, faultfs.OpFsync, 1},
	}
	forEachFault(t, cases, func(t *testing.T, c faultCase, errno syscall.Errno) {
		f := newFaultFixture(t)
		before := f.image()
		runnerCalls := 0
		plan := f.inject(c, errno, nil)
		result, err := f.bash(countingRunner(&runnerCalls))
		if plan.fired != 1 || !errors.Is(err, errno) || errors.Is(err, ErrAudit) {
			t.Fatalf("RunBash err=%v fired=%d, want the typed %s before any audit record", err, plan.fired, errnoName(errno))
		}
		if result.Generation != nil || runnerCalls != 0 {
			t.Fatalf("a setup fault released a generation or launched the runner: calls=%d", runnerCalls)
		}
		after := f.image()
		sameImageExceptViews(t, before, after)
		if len(after.views) != 0 {
			t.Fatalf("partial command view survived the fault: %v", after.views)
		}
		if f.store.quarantined || len(f.store.generations) != 1 || len(f.store.transitions) != 0 {
			t.Fatalf("setup fault changed the Store: quarantined=%t generations=%d", f.store.quarantined, len(f.store.generations))
		}
		f.requireSealedInput()
		// Cleanup was idempotent: with the fault gone the same operation succeeds.
		faultfs.Install(nil)
		retry, err := f.bash(countingRunner(&runnerCalls))
		if err != nil || retry.Generation == nil || retry.Generation.ID() != "g1" || runnerCalls != 1 {
			t.Fatalf("retry after cleanup: generation=%v err=%v runner calls=%d", retry.Generation, err, runnerCalls)
		}
		if final := f.image(); len(final.views) != 0 {
			t.Fatalf("view left after the retry: %v", final.views)
		}
	})
}

// Staging, first copy-on-write allocation, content write, promotion and the
// directory syncs of a Write. The intent is already durable, so the effect is
// unknown: the typed error is returned, nothing enters the approved ledger or
// the head, and a fresh process quarantines instead of recovering a guess.
func TestCopyOnWriteAndPromotionFaultsQuarantineWithoutPromotion(t *testing.T) {
	type cowCase struct {
		faultCase
		admissions    int
		generationDir bool
	}
	cases := []cowCase{
		{faultCase{"stage-mkdir", faultfs.SiteStageSetup, faultfs.OpMkdir, 1}, 0, false},
		{faultCase{"cow-mkdir", faultfs.SiteWorkspaceImport, faultfs.OpMkdir, 1}, 0, false},
		{faultCase{"cow-create-first", faultfs.SiteWorkspaceImport, faultfs.OpCreate, 1}, 0, false},
		{faultCase{"cow-create-second", faultfs.SiteWorkspaceImport, faultfs.OpCreate, 2}, 0, false},
		{faultCase{"cow-write-first", faultfs.SiteWorkspaceImport, faultfs.OpWrite, 1}, 0, false},
		{faultCase{"cow-write-second", faultfs.SiteWorkspaceImport, faultfs.OpWrite, 2}, 0, false},
		{faultCase{"cow-fsync-first", faultfs.SiteWorkspaceImport, faultfs.OpFsync, 1}, 0, false},
		{faultCase{"apply-create", faultfs.SiteStageApply, faultfs.OpCreate, 1}, 0, false},
		{faultCase{"apply-write", faultfs.SiteStageApply, faultfs.OpWrite, 1}, 0, false},
		{faultCase{"apply-fsync", faultfs.SiteStageApply, faultfs.OpFsync, 1}, 0, false},
		{faultCase{"apply-rename", faultfs.SiteStageApply, faultfs.OpRename, 1}, 0, false},
		{faultCase{"stage-tree-fsync", faultfs.SiteStageSetup, faultfs.OpFsync, 1}, 1, false},
		{faultCase{"promote-rename", faultfs.SitePromote, faultfs.OpRename, 1}, 1, false},
		{faultCase{"promote-dir-fsync", faultfs.SitePromote, faultfs.OpFsync, 1}, 1, true},
	}
	for _, c := range cases {
		for _, errno := range faultErrnos(c.op) {
			t.Run(c.name+"/"+errnoName(errno), func(t *testing.T) {
				f := newFaultFixture(t)
				before := f.image()
				plan := f.inject(c.faultCase, errno, nil)
				result, err := f.write()
				if plan.fired != 1 || !errors.Is(err, errno) || !errors.Is(err, ErrAudit) || !errors.Is(err, audit.ErrOutcomeUnknown) {
					t.Fatalf("Write err=%v fired=%d, want ErrAudit/ErrOutcomeUnknown wrapping %s", err, plan.fired, errnoName(errno))
				}
				if result.Generation != nil {
					t.Fatal("a faulted Write released a generation")
				}
				if !f.store.quarantined || len(f.store.generations) != 1 || len(f.store.transitions) != 0 {
					t.Fatalf("faulted Write must quarantine without advancing: quarantined=%t generations=%d transitions=%d",
						f.store.quarantined, len(f.store.generations), len(f.store.transitions))
				}
				if _, err := f.store.Verify(); !errors.Is(err, ErrQuarantined) {
					t.Fatalf("Verify after the fault = %v, want quarantine", err)
				}
				after := f.image()
				requireNoStage(t, after)
				wantGenerations := []string{"g0"}
				if c.generationDir {
					wantGenerations = []string{"g0", "g1"}
				}
				if fmt.Sprint(after.generations) != fmt.Sprint(wantGenerations) || len(after.views) != 0 {
					t.Fatalf("generations=%v views=%v, want %v and no view", after.generations, after.views, wantGenerations)
				}
				if !bytes.Equal(before.state, after.state) {
					t.Fatal("the head state changed although the effect outcome is unknown")
				}
				if got := after.admissionRecords(t); got != c.admissions {
					t.Fatalf("admission records=%d, want %d", got, c.admissions)
				}
				effects := after.effects(t)
				if len(effects) != len(before.effects(t))+1 || effects[len(effects)-1].Outcome == "success" {
					t.Fatalf("fault must leave exactly one new non-success effect: %+v", effects)
				}
				f.requireSealedInput()
				if reopened, err := f.reopen(); !errors.Is(err, ErrQuarantined) || reopened != nil {
					t.Fatalf("fresh Open = (%v, %v), want quarantine and no usable Store", reopened, err)
				}
			})
		}
	}
}

// Faults after the runner has settled: importing the settled command view is
// the first copy-on-write allocation of the command's output. The command may
// have run, so the outcome is unknown and the Store quarantines; the runner is
// not run a second time by recovery.
func TestSettledCommandViewImportFaultsQuarantineAfterOneRun(t *testing.T) {
	cases := []faultCase{
		{"import-mkdir", faultfs.SiteWorkspaceImport, faultfs.OpMkdir, 1},
		{"import-create-first", faultfs.SiteWorkspaceImport, faultfs.OpCreate, 1},
		{"import-write-first", faultfs.SiteWorkspaceImport, faultfs.OpWrite, 1},
		{"import-fsync-first", faultfs.SiteWorkspaceImport, faultfs.OpFsync, 1},
		{"stage-mkdir", faultfs.SiteStageSetup, faultfs.OpMkdir, 1},
	}
	forEachFault(t, cases, func(t *testing.T, c faultCase, errno syscall.Errno) {
		f := newFaultFixture(t)
		before := f.image()
		runnerCalls := 0
		plan := f.inject(c, errno, func() bool { return runnerCalls > 0 })
		result, err := f.bash(countingRunner(&runnerCalls))
		if plan.fired != 1 || runnerCalls != 1 || !errors.Is(err, errno) || !errors.Is(err, ErrAudit) {
			t.Fatalf("RunBash err=%v fired=%d runner calls=%d, want one run and the typed %s", err, plan.fired, runnerCalls, errnoName(errno))
		}
		if result.Generation != nil || !f.store.quarantined || len(f.store.generations) != 1 {
			t.Fatal("faulted command import released or admitted a generation")
		}
		after := f.image()
		requireNoStage(t, after)
		if fmt.Sprint(after.generations) != "[g0]" || !bytes.Equal(before.state, after.state) || after.admissionRecords(t) != 0 {
			t.Fatalf("durable state advanced under the fault: generations=%v", after.generations)
		}
		effects := after.effects(t)
		if effects[len(effects)-1].Outcome == "success" {
			t.Fatal("a faulted command import recorded a success outcome")
		}
		f.requireSealedInput()
		if reopened, err := f.reopen(); !errors.Is(err, ErrQuarantined) || reopened != nil {
			t.Fatalf("fresh Open = (%v, %v), want quarantine", reopened, err)
		}
		if runnerCalls != 1 {
			t.Fatalf("recovery re-ran the command: calls=%d", runnerCalls)
		}
	})
}

// Teardown after a durable success: the outcome and the candidate generation
// are durable, but the view could not be removed or synced. The caller gets
// the error and no result. A view that is still present quarantines the next
// Open; a removed view lets recovery adopt exactly the durable generation.
func TestCommandViewTeardownFaultsWithholdResultAndRecoverConsistently(t *testing.T) {
	cases := []struct {
		faultCase
		viewSurvives bool
	}{
		{faultCase{"teardown-unlink", faultfs.SiteViewTeardown, faultfs.OpUnlink, 1}, true},
		{faultCase{"teardown-dir-fsync", faultfs.SiteViewTeardown, faultfs.OpFsync, 1}, false},
	}
	for _, c := range cases {
		for _, errno := range faultErrnos(c.op) {
			t.Run(c.name+"/"+errnoName(errno), func(t *testing.T) {
				f := newFaultFixture(t)
				before := f.image()
				runnerCalls := 0
				plan := f.inject(c.faultCase, errno, nil)
				result, err := f.bash(countingRunner(&runnerCalls))
				if plan.fired != 1 || runnerCalls != 1 || !errors.Is(err, errno) {
					t.Fatalf("RunBash err=%v fired=%d runner calls=%d, want the typed %s after one run", err, plan.fired, runnerCalls, errnoName(errno))
				}
				if result.Generation != nil || result.Command != nil || !f.store.quarantined || len(f.store.generations) != 1 {
					t.Fatal("a teardown fault released a result or advanced the in-memory head")
				}
				after := f.image()
				requireNoStage(t, after)
				if !bytes.Equal(before.state, after.state) || fmt.Sprint(after.generations) != "[g0 g1]" ||
					(len(after.views) == 1) != c.viewSurvives || len(after.views) > 1 {
					t.Fatalf("teardown fault state: head unchanged=%t generations=%v views=%v", bytes.Equal(before.state, after.state), after.generations, after.views)
				}
				effects := after.effects(t)
				if last := effects[len(effects)-1]; last.Outcome != "success" || last.Unresolved {
					t.Fatalf("the durable command outcome was lost: %+v", last)
				}
				f.requireSealedInput()
				journalBeforeRecovery := after.journal
				reopened, err := f.reopen()
				if c.viewSurvives {
					if !errors.Is(err, ErrQuarantined) || reopened != nil {
						t.Fatalf("fresh Open with a surviving writable view = (%v, %v), want quarantine", reopened, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("fresh Open after a removed view: %v", err)
				}
				verified, err := reopened.Verify()
				if err != nil || verified.SealedGeneration != "g1" || reopened.TipGeneration().ID() != "g1" {
					t.Fatalf("recovered chain=%+v tip=%v err=%v, want the durable g1", verified, reopened.TipGeneration().ID(), err)
				}
				recovered := f.image()
				if !bytes.Equal(recovered.journal, journalBeforeRecovery) || runnerCalls != 1 {
					t.Fatal("recovery appended to the journal or re-ran the command")
				}
			})
		}
	}
}

// Head persistence after the durable outcome: the Store reports the failure
// and withholds the result; recovery repairs the head from the journals and
// the admission record without repeating the effect.
func TestHeadStateWriteFaultsRecoverCommittedPrefix(t *testing.T) {
	cases := []faultCase{
		{"head-create", faultfs.SiteHeadState, faultfs.OpCreate, 1},
		{"head-write", faultfs.SiteHeadState, faultfs.OpWrite, 1},
		{"head-fsync", faultfs.SiteHeadState, faultfs.OpFsync, 1},
		{"head-rename", faultfs.SiteHeadState, faultfs.OpRename, 1},
	}
	forEachFault(t, cases, func(t *testing.T, c faultCase, errno syscall.Errno) {
		f := newFaultFixture(t)
		before := f.image()
		plan := f.inject(c, errno, nil)
		result, err := f.write()
		if plan.fired != 1 || !errors.Is(err, errno) {
			t.Fatalf("Write err=%v fired=%d, want the typed %s", err, plan.fired, errnoName(errno))
		}
		if result.Generation != nil || !f.store.quarantined || len(f.store.generations) != 1 || len(f.store.transitions) != 0 {
			t.Fatal("a head-persistence fault released or admitted the generation in memory")
		}
		after := f.image()
		requireNoStage(t, after)
		root, err := os.ReadDir(f.rootPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range root {
			if strings.HasPrefix(entry.Name(), ".repository-state-") {
				t.Fatalf("temporary state file %q survived the fault", entry.Name())
			}
		}
		if !bytes.Equal(before.state, after.state) || fmt.Sprint(after.generations) != "[g0 g1]" {
			t.Fatalf("head state or generations unexpected: head unchanged=%t generations=%v", bytes.Equal(before.state, after.state), after.generations)
		}
		effects := after.effects(t)
		if last := effects[len(effects)-1]; last.Outcome != "success" || last.Unresolved {
			t.Fatalf("the durable outcome was lost: %+v", last)
		}
		f.requireSealedInput()
		reopened, err := f.reopen()
		if err != nil {
			t.Fatalf("fresh Open must repair the head from the journals: %v", err)
		}
		verified, err := reopened.Verify()
		if err != nil || verified.SealedGeneration != "g1" || reopened.TipGeneration().ID() != "g1" {
			t.Fatalf("recovered chain=%+v err=%v, want g1", verified, err)
		}
		repaired := f.image()
		var state durableRepositoryState
		if state, _, err = readRepositoryState(reopened.root); err != nil || state.HeadGeneration != "g1" || len(state.ApprovedDeltaLedger) != 1 {
			t.Fatalf("repaired head state=%+v err=%v", state, err)
		}
		if !bytes.Equal(repaired.journal, after.journal) || !bytes.Equal(repaired.admission, after.admission) {
			t.Fatal("recovery appended to a journal")
		}
	})
}

// Journal write and fsync faults on a Store mutation. A failed intent prevents
// the effect; a failed outcome withholds the result. Either way the journal is
// poisoned and the Store quarantined in this process.
func TestAuditJournalFaultsOnStoreMutationFailClosed(t *testing.T) {
	cases := []struct {
		faultCase
		effectRan     bool
		wantErr       error
		reopenInState string
	}{
		{faultCase{"intent-write", faultfs.SiteAuditJournal, faultfs.OpWrite, 1}, false, audit.ErrDurability, "usable-g0"},
		{faultCase{"intent-fsync", faultfs.SiteAuditJournal, faultfs.OpFsync, 1}, false, audit.ErrDurability, "quarantine"},
		// The delta-admission journal is a second audit journal that the callback appends to.
		{faultCase{"admission-write", faultfs.SiteAuditJournal, faultfs.OpWrite, 2}, false, audit.ErrOutcomeUnknown, "quarantine"},
		{faultCase{"admission-fsync", faultfs.SiteAuditJournal, faultfs.OpFsync, 2}, false, audit.ErrOutcomeUnknown, "quarantine"},
		{faultCase{"outcome-write", faultfs.SiteAuditJournal, faultfs.OpWrite, 3}, true, audit.ErrOutcomeUnknown, "quarantine"},
		{faultCase{"outcome-fsync", faultfs.SiteAuditJournal, faultfs.OpFsync, 3}, true, audit.ErrOutcomeUnknown, "adopt-g1"},
	}
	for _, c := range cases {
		for _, errno := range faultErrnos(c.op) {
			t.Run(c.name+"/"+errnoName(errno), func(t *testing.T) {
				f := newFaultFixture(t)
				before := f.image()
				plan := f.inject(c.faultCase, errno, nil)
				result, err := f.write()
				if plan.fired != 1 || !errors.Is(err, errno) || !errors.Is(err, ErrAudit) || !errors.Is(err, c.wantErr) || !errors.Is(err, audit.ErrPoisoned) {
					t.Fatalf("Write err=%v fired=%d, want ErrAudit/%v/ErrPoisoned wrapping %s", err, plan.fired, c.wantErr, errnoName(errno))
				}
				if result.Generation != nil || !f.store.quarantined || len(f.store.generations) != 1 || len(f.store.transitions) != 0 {
					t.Fatal("a journal fault released or admitted a generation")
				}
				after := f.image()
				requireNoStage(t, after)
				wantGenerations := "[g0]"
				if c.effectRan {
					wantGenerations = "[g0 g1]"
				}
				if fmt.Sprint(after.generations) != wantGenerations || !bytes.Equal(before.state, after.state) {
					t.Fatalf("generations=%v head unchanged=%t, want %s", after.generations, bytes.Equal(before.state, after.state), wantGenerations)
				}
				f.requireSealedInput()
				reopened, err := f.reopen()
				switch c.reopenInState {
				case "quarantine":
					if !errors.Is(err, ErrQuarantined) || reopened != nil {
						t.Fatalf("fresh Open = (%v, %v), want quarantine", reopened, err)
					}
				case "usable-g0":
					// Nothing of the effect reached the journal: the prefix is intact.
					if err != nil || reopened.TipGeneration().ID() != "g0" {
						t.Fatalf("fresh Open after a failed intent write = %v", err)
					}
				case "adopt-g1":
					// The outcome frame reached the file before fsync failed, so
					// the journal itself proves the outcome; no replay occurs.
					if err != nil || reopened.TipGeneration().ID() != "g1" {
						t.Fatalf("fresh Open after a failed outcome fsync = %v", err)
					}
				}
			})
		}
	}
}
