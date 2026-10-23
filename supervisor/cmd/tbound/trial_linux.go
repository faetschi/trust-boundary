//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/wsoracle"
)

// `tbound trial reset <case_id> [--check]` is the guest half of the runner's fixture reset:
// it puts the live root back to the workspace fixture of one corpus case before a trial. It
// writes exactly one JSON document (trialfix.ResetDoc) to stdout and refuses to run as any
// uid other than trialRequiredUID, which is root. That is the uid the trial SSH key logs in
// as, with a forced command restricted to this subcommand (authorized_keys
// restrict,command="/usr/local/bin/tbound trial ssh-original-command", the same shape as the
// evidence key, see infra/guest/PROVISIONING-SIGNING.md). The case and its fixture come from
// the pinned corpus manifest the signed service config references, never from the caller;
// the fixture spec is read from the root-owned artifact directory and must recompute to the
// digest the manifest pins.
//
//	reset <case_id>
//	    takes the maintenance lock first (maintenance_lock_linux.go) and holds it to the end: it
//	    refuses while the service has a workflow admitted or another reset runs. It then refuses when a workflow admission has no terminal, a managed-root run still holds the
//	    live root, or the fixture artifact does not verify. Otherwise it kills whatever still
//	    runs in the cells cgroup subtree (a write of 1 to cgroup.kill, then a bounded wait for
//	    populated 0; a missing cgroup.kill or a cgroup that stays populated refuses), removes the
//	    containers and tbound-nr-* cgroups that no-reap/v1 cells left (trial_host_linux.go), makes the
//	    net-sink/v1 confinement current when /etc/tbound/net-sink.nft is installed (parent cgroup,
//	    nft load, read-back, attestation), wipes the live root in place (the root inode, its mode and owner and a top-level lost+found are
//	    kept), populates it from the fixture, and prints the digest of the fixture next to the
//	    digest of the resulting tree, which must be equal.
//	reset <case_id> --check
//	    the refusals only, and a populated cells cgroup is one of them, except for live processes in
//	    tbound-nr-* scopes: no-reap/v1 leaves them by design and the reset kills them. It changes nothing.
//	accept <case_id> [--tree live]
//	    evaluates the case's pinned acceptance artifact against the live root (read-only,
//	    links are never followed) and prints the result document (trial_accept_linux.go).
//	v-run <case_id>
//	    runs native Pi (Configuration V) once in a plain Podman container on a fresh copy of
//	    the case fixture and prints the run document (trial_vrun_linux.go).
//	ssh-original-command
//	    the forced-command entry: reads $SSH_ORIGINAL_COMMAND, which must be
//	    "/usr/local/bin/tbound trial <args>" in the word grammar of the evidence entry.
const (
	trialRequiredUID = 0
	// trialArtifactDir is where the signing session installs the fixture artifacts
	// (fixture-<hex>.json, root:root 0644). Only a test replaces it, through trialEnv.
	trialArtifactDir = "/opt/tbound/corpus/artifacts"
	trialResetLabel  = "reset"
)

// trialKillWait bounds the wait for the cells cgroup to become unpopulated after cgroup.kill
// (a variable so a test can shorten it).
var trialKillWait = 5 * time.Second

const trialUsage = `usage: tbound trial reset <case_id> [--check]   (root only; one JSON document on stdout)
  reset <case_id>           reset the live root to the fixture of the case
  reset <case_id> --check   verify the preconditions and the fixture artifact only
  accept <case_id> [--tree live]   evaluate the acceptance artifact of a compliant case on the live root
  v-run <case_id>           run native Pi (Configuration V) on a fresh fixture copy in a plain container
  ssh-original-command     (forced-command entry; reads $SSH_ORIGINAL_COMMAND)
`

// trialEnv is everything the subcommand touches outside its arguments; tests replace it.
type trialEnv struct {
	geteuid func() int
	getenv  func(string) string
	// fixtureDigest is the workspace_fixture_digest the pinned corpus manifest gives a case.
	fixtureDigest func(ctx context.Context, caseID string) (string, error)
	// artifacts holds the fixture artifacts.
	artifacts fs.FS
	// liveRoot is the root that is reset, and owner the owner of what is put in it.
	liveRoot string
	owner    trialfix.Owner
	// registeredRoot returns the device and inode of the verified managed live root
	// registration: the identity a path-only consumer of liveRoot must pin.
	registeredRoot func(ctx context.Context) (device, inode uint64, err error)
	// cell resolves the registered cell, as the evidence kinds do, with its host identity.
	cell func(ctx context.Context) (evidenceCell, error)
	// lock takes the maintenance lock without waiting (maintenance_lock_linux.go) and returns its
	// release. The service holds it from the admission of a workflow until it is settled.
	lock func() (func(), error)
	// pending returns an error naming what is still pending: a workflow admission without a
	// terminal, or a managed-root run that holds the live root.
	pending func(ctx context.Context) error
	// cellPopulated reports whether the cells cgroup (or one below it) holds a live process that is
	// not in a tbound-nr-* scope (no-reap/v1 survivors are the reset's work, not a refusal).
	cellPopulated func(cell evidenceCell) (bool, error)
	// killCell kills every process of the cells cgroup subtree and fails unless the cgroup is
	// unpopulated afterwards.
	killCell func(cell evidenceCell) error
	// cleanNoReap removes the containers and the tbound-nr-* cgroups that no-reap/v1 cells leave
	// behind, once the cells cgroup is unpopulated (trial_host_linux.go).
	cleanNoReap func(ctx context.Context, cell evidenceCell) error
	// netSink makes the net-sink/v1 confinement current: parent cgroup, nft table, attestation.
	netSink func(ctx context.Context, cell evidenceCell) error
	// snapshot is wsoracle.Snapshot.
	snapshot func(root, label string) wsoracle.Manifest
	// snapshotDir is wsoracle.SnapshotDir: the digest snapshot of a directory the caller holds.
	snapshotDir func(dir *os.File, label string) wsoracle.Manifest
	// task is what the accept and v-run verbs need (trial_accept_linux.go, trial_vrun_linux.go).
	task trialTaskEnv
}

func productionTrialEnv() trialEnv {
	host := productionTrialHost()
	return trialEnv{
		geteuid: os.Geteuid, getenv: os.Getenv,
		fixtureDigest: trialPinnedFixtureDigest, artifacts: os.DirFS(trialArtifactDir),
		liveRoot: piruntime.D09ManagedPiLiveRootPath, owner: trialfix.Canonical,
		registeredRoot: func(ctx context.Context) (uint64, uint64, error) {
			reg, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
			if err != nil {
				return 0, 0, err
			}
			if reg.RootPath != piruntime.D09ManagedPiLiveRootPath {
				return 0, 0, fmt.Errorf("the registration names the live root %q, not %q", reg.RootPath, piruntime.D09ManagedPiLiveRootPath)
			}
			return reg.RootDevice, reg.RootInode, nil
		},
		cell: loadTrialCell, lock: resetMaintenanceLock.acquire, pending: trialPending, cellPopulated: cellsCgroupPopulated, killCell: killCellsCgroup,
		cleanNoReap: host.cleanNoReap, netSink: host.ensureNetSink,
		snapshot: wsoracle.Snapshot, snapshotDir: wsoracle.SnapshotDir,
		task: productionTrialTaskEnv(),
	}
}

// init dispatches `tbound trial ...` before main runs, as evidence does.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "trial" {
		os.Exit(trialMain(os.Args[2:], os.Stdout, os.Stderr, productionTrialEnv()))
	}
}

func trialMain(args []string, stdout, stderr io.Writer, env trialEnv) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		_, _ = io.WriteString(stderr, trialUsage)
		return 0
	}
	if err := runTrial(context.Background(), args, stdout, env); err != nil {
		fmt.Fprintf(stderr, "tbound trial: %v\n%s", err, trialUsage)
		return 2
	}
	return 0
}

func runTrial(ctx context.Context, args []string, stdout io.Writer, env trialEnv) error {
	if env.geteuid == nil || env.geteuid() != trialRequiredUID {
		return fmt.Errorf("trial may run only as uid %d", trialRequiredUID)
	}
	if len(args) == 0 {
		return errors.New("no trial verb")
	}
	switch verb, rest := args[0], args[1:]; verb {
	case "ssh-original-command":
		if len(rest) != 0 {
			return fmt.Errorf("%s takes no argument", verb)
		}
		words, err := parseOriginalCommand(env.getenv("SSH_ORIGINAL_COMMAND"), "trial")
		if err != nil {
			return err
		}
		if words[0] == "ssh-original-command" {
			return errors.New("the forced command may not nest itself")
		}
		return runTrial(ctx, words, stdout, env)
	case "reset":
		doc, err := trialReset(ctx, rest, env)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(encoded, '\n'))
		return err
	case "accept", "v-run":
		return runTrialTask(ctx, verb, rest, stdout, env)
	default:
		return fmt.Errorf("unknown trial verb %q", verb)
	}
}

// parseTrialReset accepts exactly "<case_id>" or "<case_id> --check".
func parseTrialReset(args []string) (caseID string, check bool, err error) {
	if len(args) < 1 || len(args) > 2 {
		return "", false, errors.New("reset takes a case id and optionally --check")
	}
	caseID = args[0]
	if len(caseID) == 0 || len(caseID) > evidenceMaxValueBytes || strings.HasPrefix(caseID, "-") || !evidenceWordRE.MatchString(caseID) {
		return "", false, errors.New("the case id is not in the safe alphabet")
	}
	if len(args) == 2 {
		if args[1] != "--check" {
			return "", false, fmt.Errorf("reset: unknown argument %q", args[1])
		}
		check = true
	}
	return caseID, check, nil
}

func trialReset(ctx context.Context, args []string, env trialEnv) (trialfix.ResetDoc, error) {
	caseID, check, err := parseTrialReset(args)
	if err != nil {
		return trialfix.ResetDoc{}, err
	}
	digest, err := env.fixtureDigest(ctx, caseID)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	spec, err := corpusadmit.LoadFixture(env.artifacts, digest)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	cell, err := env.cell(ctx)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("the registered cell cannot be resolved: %w", err)
	}
	if cell.HostUID != env.owner.UID || cell.HostGID != env.owner.GID {
		return trialfix.ResetDoc{}, fmt.Errorf("the cell's host identity %d:%d is not the fixture owner %d:%d", cell.HostUID, cell.HostGID, env.owner.UID, env.owner.GID)
	}
	// From here to the final digest check no admission and no other reset may run.
	release, err := env.lock()
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("the maintenance lock is not available (a workflow is admitted or another reset runs): %w", err)
	}
	defer release()
	if err := env.pending(ctx); err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("a workflow, admission or publication is pending: %w", err)
	}
	populated, err := env.cellPopulated(cell)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("the cells cgroup cannot be read: %w", err)
	}
	doc := trialfix.ResetDoc{Schema: trialfix.ResetSchema, CaseID: caseID, FixtureDigest: digest, Check: check}
	if check {
		if populated {
			return trialfix.ResetDoc{}, errors.New("the cells cgroup holds processes")
		}
		return doc, nil
	}
	// Whatever still runs in the cells cgroup goes first: a process that outlived its cell
	// could write into the tree that is about to be built. The kill is the kernel's and
	// atomic over the subtree, and the reset proceeds only on populated 0.
	device, inode, err := env.registeredRoot(ctx)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("the managed live root registration cannot be verified: %w", err)
	}
	if err := env.killCell(cell); err != nil {
		return trialfix.ResetDoc{}, err
	}
	// Whatever a no-reap/v1 cell left (its container, its tbound-nr-* cgroup) goes next, and the
	// net-sink confinement is made current, before the tree is touched.
	if err := env.cleanNoReap(ctx, cell); err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("refusing the reset: no-reap leftovers: %w", err)
	}
	if err := env.netSink(ctx, cell); err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("refusing the reset: net-sink confinement: %w", err)
	}
	if err := trialfix.ResetPinned(env.liveRoot, spec, env.owner, trialfix.RootIdentity{Device: device, Inode: inode}); err != nil {
		return trialfix.ResetDoc{}, err
	}
	// The digest is taken through the registered inode the reset verified, not through the path:
	// a root that is renamed after the reset and replaced by a decoy does not change what is
	// digested.
	pinned, err := openPinnedLiveRoot(env.liveRoot, device, inode)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("the reset root is no longer the registered live root: %w", err)
	}
	pinnedFile := os.NewFile(uintptr(pinned), env.liveRoot)
	manifest := env.snapshotDir(pinnedFile, trialResetLabel)
	_ = pinnedFile.Close()
	if manifest.Dev != device || manifest.Root == nil || manifest.Root.Ino != inode {
		return trialfix.ResetDoc{}, fmt.Errorf("the reset tree is not the registered live root: device %d inode %d, want device %d inode %d",
			manifest.Dev, manifestRootIno(manifest), device, inode)
	}
	tree, err := trialfix.TreeDigest(manifest)
	if err != nil {
		return trialfix.ResetDoc{}, err
	}
	if want, err := trialfix.DigestOwned(spec, env.owner); err != nil || tree != want {
		return trialfix.ResetDoc{}, fmt.Errorf("the reset tree does not digest to the fixture: %s, want %s", tree, want)
	}
	doc.TreeDigest = tree
	return doc, nil
}

// cellHostIdentity is the host uid and gid the signed user-namespace mapping puts the cell's
// processes on. The profile admits one mapping only (rootless-single-own-uid-gid/v1): the
// template's in-cell uid and gid map onto the service's own host uid and gid, which own the live
// root and the fixtures. The in-cell identity is not a host identity. The template's mapping digest
// is recomputed from the registered identities, as the launcher does, so a template that maps the
// cell anywhere else is refused. Reset and v-run both take the host identity from here.
func cellHostIdentity(reg piruntime.D09ManagedLiveRootRegistration, tmpl piruntime.D09CellTemplate) (uid, gid uint32, err error) {
	if reg.ServiceUID <= 0 || reg.ServiceGID <= 0 || tmpl.CellUID <= 0 || tmpl.CellGID <= 0 ||
		tmpl.UserNamespaceMappingProfile != piruntime.D09UserNamespaceMappingProfile ||
		tmpl.UserNamespaceMappingDigest != piruntime.DigestD09UserNamespaceMapping(tmpl.CellUID, tmpl.CellGID, reg.ServiceUID, reg.ServiceGID) {
		return 0, 0, errors.New("the cell template does not map the cell onto the registered service identity")
	}
	return uint32(reg.ServiceUID), uint32(reg.ServiceGID), nil
}

// trialPinnedFixtureDigest is the fixture digest of caseID in the pinned corpus manifest: the
// manifest the signed service config references, admitted with the service's own loader.
func trialPinnedFixtureDigest(ctx context.Context, caseID string) (string, error) {
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return "", err
	}
	_, contract, err := serviceSocketRegistrationFromSignedRegistration(registration)
	if err != nil {
		return "", err
	}
	config, err := readProductionPiServiceConfig(contract.ServiceConfigPath, contract.ServiceConfigDigest)
	if err != nil {
		return "", err
	}
	corpus, err := loadProductionCorpus(config)
	if err != nil {
		return "", err
	}
	digest, ok := corpus.fixtures[caseID]
	if !ok {
		return "", errors.New("not a case of the pinned corpus manifest")
	}
	return digest, nil
}

// trialPending reads the workflow catalog, the publication lifecycle journal and the live root
// identity without writing, and reports what is still pending.
func trialPending(ctx context.Context) error {
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return err
	}
	catalog, err := readEvidenceTrace(piruntime.D09ManagedPiWorkflowCatalogPath, false)
	if err != nil {
		return fmt.Errorf("the workflow catalog cannot be read: %w", err)
	}
	lifecycle, err := readEvidenceTrace(filepath.Join(productionAuditRootDefault, "publication-audit.jsonl"), false)
	if err != nil {
		return fmt.Errorf("the publication lifecycle journal cannot be read: %w", err)
	}
	root, err := trialRootIdentity(registration, piruntime.D09ManagedPiLiveRootPath)
	if err != nil {
		return err
	}
	return trialPendingFrom(catalog, lifecycle, root, registration.Digest)
}

// trialRootIdentity reports the registration's device and inode for path, and refuses when the
// path no longer is that directory: a live root that was renamed and replaced by a decoy is not
// the root the registered runs were made on.
func trialRootIdentity(registration piruntime.D09ManagedLiveRootRegistration, path string) (workflow.RootIdentity, error) {
	if registration.RootPath != path {
		return workflow.RootIdentity{}, fmt.Errorf("the registration names %q, not %q", registration.RootPath, path)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return workflow.RootIdentity{}, fmt.Errorf("the live root cannot be identified: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return workflow.RootIdentity{}, errors.New("the live root cannot be identified")
	}
	if uint64(stat.Dev) != registration.RootDevice || stat.Ino != registration.RootInode {
		return workflow.RootIdentity{}, fmt.Errorf("the live root is device %d inode %d, not the registered live root device %d inode %d",
			stat.Dev, stat.Ino, registration.RootDevice, registration.RootInode)
	}
	return workflow.RootIdentity{Device: registration.RootDevice, Inode: registration.RootInode}, nil
}

// trialPendingFrom is trialPending over the two traces: a catalog admission without a terminal
// record is a pending workflow, and a managed-root run on the root that has no resolution
// record (managedLiveRootPriorRunsFromTrace, the rule the service applies to its prior runs)
// is a pending run or publication.
func trialPendingFrom(catalog, lifecycle audit.Trace, root workflow.RootIdentity, registrationDigest string) error {
	var open []string
	terminal := map[string]bool{}
	for _, record := range catalog.Records {
		if record.Event.Kind != "pi_workflow_admission_reserved" && record.Event.Kind != "pi_workflow_terminal" {
			continue
		}
		var event struct {
			SchemaVersion string `json:"schema_version"`
			AdmissionHash string `json:"admission_hash"`
		}
		if err := json.Unmarshal(record.Event.Data, &event); err != nil || event.SchemaVersion != workflowCatalogSchema || event.AdmissionHash == "" {
			return fmt.Errorf("catalog record %d is unreadable", record.Sequence)
		}
		if record.Event.Kind == "pi_workflow_terminal" {
			terminal[event.AdmissionHash] = true
		} else {
			open = append(open, event.AdmissionHash)
		}
	}
	for _, hash := range open {
		if !terminal[hash] {
			return fmt.Errorf("workflow admission %s is not terminal", hash)
		}
	}
	runs, err := managedLiveRootPriorRunsFromTrace(lifecycle, root, registrationDigest)
	if err != nil {
		return fmt.Errorf("the publication lifecycle journal is not readable as settled: %w", err)
	}
	if len(runs) != 0 {
		return fmt.Errorf("managed-root run %s (workflow %s) still holds the live root", runs[0].Event.RunID, runs[0].Event.WorkflowID)
	}
	return nil
}

// A reset does not look for the cell's processes: every one of them lives in the registered
// cells cgroup subtree (the launcher creates each cell below it, podman --cgroup-parent), and
// the kernel kills a cgroup subtree as one operation. A write of "1" to cgroup.kill sends SIGKILL
// to every process of the subtree, and a fork or a migration into the subtree after that point
// is killed too, so a process cannot slip out of a scan between its start and its end. The
// reset waits until cgroup.events reports populated 0 (no live process in the subtree, a
// zombie does not count) and refuses otherwise. A kernel without cgroup.kill refuses: there is
// no fallback.
//
// The stated limit: a process that left the subtree is not killed or found. Nothing in the
// launcher flags lets a cell do that (--cap-drop=ALL, no-new-privileges, --read-only, a
// seccomp profile, podman's default cgroup namespace and read-only cgroup mount, no
// --privileged; containment_linux.go, d09_command_runner_linux.go), but that is not measured
// on a running cell, and a scan for such a process is the race this design removes.

// openCellsCgroup opens the cells cgroup directory without following a final symlink and
// requires it to be a cgroup2 directory with the inode the cell registration was resolved with.
func openCellsCgroup(cell evidenceCell) (*os.File, error) {
	dir, err := openEvidenceCellDir(cell.CgroupPath)
	if err != nil {
		return nil, err
	}
	var fsStat unix.Statfs_t
	var st unix.Stat_t
	if err := unix.Fstatfs(int(dir.Fd()), &fsStat); err != nil || fsStat.Type != unix.CGROUP2_SUPER_MAGIC {
		dir.Close()
		return nil, fmt.Errorf("%s is not on a cgroup2 filesystem (%v)", cell.CgroupPath, err)
	}
	if err := unix.Fstat(int(dir.Fd()), &st); err != nil || st.Ino != cell.Inode {
		dir.Close()
		return nil, fmt.Errorf("%s is not the registered cells cgroup (inode %d, want %d, %v)", cell.CgroupPath, st.Ino, cell.Inode, err)
	}
	return dir, nil
}

// cellsCgroupPopulated reports whether the cells cgroup or one below it holds a live process
// outside the tbound-nr-* scopes (cgroupLiveOutsideNoReap).
func cellsCgroupPopulated(cell evidenceCell) (bool, error) {
	dir, err := openCellsCgroup(cell)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	return cgroupLiveOutsideNoReap(dir)
}

// cgroupLiveOutsideNoReap reports whether dir itself or a child cgroup other than a tbound-nr-*
// scope holds a live process. Those scopes hold what no-reap/v1 leaves running by design; only the
// reset's cgroup.kill ends it, and the reset runs after the --check of the runner's Prepare, so
// counting them would refuse every trial after a no-reap run.
func cgroupLiveOutsideNoReap(dir *os.File) (bool, error) {
	if populated, err := cgroupPopulated(dir); err != nil || !populated {
		return populated, err
	}
	fd, err := unix.Openat(int(dir.Fd()), "cgroup.procs", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("read cgroup.procs: %w", err)
	}
	procs := os.NewFile(uintptr(fd), "cgroup.procs")
	data, err := io.ReadAll(io.LimitReader(procs, 1))
	procs.Close()
	if err != nil || len(data) != 0 {
		return true, err
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), trialNoReapPrefix) {
			continue
		}
		fd, err := unix.Openat(int(dir.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return false, fmt.Errorf("cgroup %s: %w", entry.Name(), err)
		}
		child := os.NewFile(uintptr(fd), entry.Name())
		populated, err := cgroupPopulated(child)
		child.Close()
		if err != nil || populated {
			return populated, err
		}
	}
	return false, nil
}

// killCellsCgroup is the production killCell: cgroup.kill, then the wait for populated 0.
func killCellsCgroup(cell evidenceCell) error {
	dir, err := openCellsCgroup(cell)
	if err != nil {
		return fmt.Errorf("refusing the reset: %w", err)
	}
	defer dir.Close()
	return killCgroup(dir, trialKillWait)
}

// killCgroup writes "1" to cgroup.kill of dir and waits up to wait for populated 0.
func killCgroup(dir *os.File, wait time.Duration) error {
	if err := writeCgroupFile(dir, "cgroup.kill", "1"); err != nil {
		return fmt.Errorf("refusing the reset: cgroup.kill cannot be written: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		populated, err := cgroupPopulated(dir)
		if err != nil {
			return fmt.Errorf("refusing the reset: %w", err)
		}
		if !populated {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("refusing the reset: the cells cgroup still holds a live process %s after cgroup.kill", wait)
		}
		time.Sleep(trialKillPoll)
	}
}

// trialKillPoll is the pause between two reads of cgroup.events while the processes leave.
const trialKillPoll = 5 * time.Millisecond

func writeCgroupFile(dir *os.File, name, value string) error {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	_, err = file.WriteString(value)
	return err
}

// cgroupPopulated reads the populated key of cgroup.events of the cgroup2 directory dir
// (relative to its descriptor, no symlink followed). The key covers the directory and every
// cgroup below it.
func cgroupPopulated(dir *os.File) (bool, error) {
	fd, err := unix.Openat(int(dir.Fd()), "cgroup.events", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("read cgroup.events: %w", err)
	}
	file := os.NewFile(uintptr(fd), "cgroup.events")
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "populated "); ok {
			switch value {
			case "0":
				return false, nil
			case "1":
				return true, nil
			}
			break
		}
	}
	return false, errors.New("cgroup.events has no valid populated key")
}
