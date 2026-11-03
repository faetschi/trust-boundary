//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const qualificationCellsPath = "/sys/fs/cgroup/system.slice/tbound-product.service/cells"

var qualificationWriteCommand = regexp.MustCompile(`^printf '%s\\n' (\S+) > (\S+)$`)
var qualificationListenerLiteral = regexp.MustCompile(`const listener = "([^"]+)";`)

// qualificationHarness stands in for the Podman/crun cell and the delegated cells cgroup of an unprivileged test host.
// Everything above the cell is production code: the Store, the per-lease runner, the receipt binding and the
// qualification phases. The harness answers each fixed command with what a healthy signed cell prints, and a test
// changes one answer to make a phase fail. Its newRunner follows the owner-record protocol of newProductD09CommandRunner
// with a directory in place of the scope, so a restart can find, drain or refuse what a killed process left.
type qualificationHarness struct {
	t          *testing.T
	profile    piruntime.HostProfile
	template   piruntime.D09CellTemplate
	cells      string
	env        *productTurnEnv
	rootDigest string
	cleanupErr error
	removeErr  error
	override   func(name, stdout string, exit int) (string, int)
	editProof  func(name string, proof *piruntime.D09CommandProof)
	skipWrite  bool
	routable   func(network, address string) error

	mu            sync.Mutex
	specs         []sessionrepo.CommandSpec
	routes        []string
	runners       []*productD09TurnRunner
	limitsErr     bool
	ownerJournals []*audit.Journal
}

func newQualificationHarness(t *testing.T) *qualificationHarness {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the single-ID mapping fixture needs a non-root service UID")
	}
	profile, template, _ := productionProofFixture()
	template.CommandExecutable, template.CommandExecutableDigest = "/bin/bash", digestBytes([]byte("bash"))
	template.CellUID, template.CellGID = os.Geteuid(), os.Getegid()
	template.PidsMax, template.MemoryMB = 128, 1024
	template.UserNamespaceMappingProfile = piruntime.D09UserNamespaceMappingProfile
	template.UserNamespaceMappingDigest = piruntime.DigestD09UserNamespaceMapping(template.CellUID, template.CellGID, os.Geteuid(), os.Getegid())
	digest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profile.ContainmentProfileDigest = digest
	profile.ChildExecutable, profile.ExecutableDigest = "/usr/local/bin/node", digestBytes([]byte("node"))
	return &qualificationHarness{t: t, profile: profile, template: template, cells: t.TempDir(), rootDigest: digestBytes([]byte("cells-root")),
		routable: func(network, address string) error {
			if network == "tcp6" {
				return syscall.ENETUNREACH
			}
			return nil
		}}
}

func (h *qualificationHarness) target() productQualificationTarget {
	return productQualificationTarget{Profile: h.profile, Template: h.template, ProviderHostname: "provider.test"}
}

// newEnv is the environment a qualification run reads: a private state root and the xattr attestation of a start.
func (h *qualificationHarness) newEnv(stateRoot string) *productTurnEnv {
	h.env = &productTurnEnv{StateRoot: stateRoot, Xattr: workspace.XattrVisibilityAttestation{
		ProfileDigest: digestBytes([]byte("qualification-xattr")), Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}}
	return h.env
}

func (h *qualificationHarness) crash(point string) {
	if h.env != nil {
		h.env.crash(point)
	}
}

func (h *qualificationHarness) scopeIdentity(name string) string { return digestBytes([]byte(name)) }

// steps wires the real cell over this harness. The load, runtime and reconcile steps are no-ops unless a test sets them.
func (h *qualificationHarness) steps() productCommandQualificationSteps {
	return productCommandQualificationSteps{
		loadHost: func(context.Context, productServiceConfig, *productTurnEnv) (*piruntime.D09ProductCommandHost, error) {
			return nil, nil
		},
		prepareRuntime:  func() error { return nil },
		reconcileScopes: func(context.Context, *piruntime.D09ProductCommandHost, []string) error { return nil },
		openCell: func(ctx context.Context, _ *piruntime.D09ProductCommandHost, env *productTurnEnv, newRunner productCommandRunnerFactory) (productQualificationCell, error) {
			return openProductQualificationCell(ctx, h.target(), env, newRunner)
		},
		newRunner: func(_ context.Context, _ *piruntime.D09ProductCommandHost, c productTurnCommandContext) (productTurnCommandRunner, error) {
			return h.newRunner(c)
		},
	}
}

// install replaces the qualification steps and the service-side network controls for one test.
func (h *qualificationHarness) install(t *testing.T, steps productCommandQualificationSteps) {
	t.Helper()
	withProductCommandQualification(t, steps)
	original := productQualificationNetwork
	productQualificationNetwork.listener = dialProductListener
	productQualificationNetwork.resolve = func(context.Context, string) ([]net.IPAddr, error) {
		return productTestResolve(context.Background(), "provider.test")
	}
	productQualificationNetwork.routable = func(_ context.Context, network, address string) error {
		h.mu.Lock()
		h.routes = append(h.routes, network+" "+address)
		h.mu.Unlock()
		return h.routable(network, address)
	}
	t.Cleanup(func() { productQualificationNetwork = original })
}

// installScopeDrain makes a directory under h.cells the scope a restart reopens for its owner record.
func (h *qualificationHarness) installScopeDrain(t *testing.T, writers int) {
	t.Helper()
	originalOpen, originalWriters := openProductScopeForDrain, productProcessesInScope
	openProductScopeForDrain = func(_ *os.File, name, identity string) (productDrainableScope, error) {
		dir := filepath.Join(h.cells, name)
		if _, err := os.Lstat(dir); err != nil {
			return nil, errors.Join(piruntime.ErrCgroupScopeRefused, err)
		}
		if identity != h.scopeIdentity(name) {
			return nil, piruntime.ErrCgroupScopeRefused
		}
		return &qualificationFakeScope{h: h, dir: dir}, nil
	}
	productProcessesInScope = func(string) (int, error) { return writers, nil }
	t.Cleanup(func() { openProductScopeForDrain, productProcessesInScope = originalOpen, originalWriters })
}

type qualificationFakeScope struct {
	h   *qualificationHarness
	dir string
}

func (s *qualificationFakeScope) RootDigest() string { return s.h.rootDigest }
func (s *qualificationFakeScope) Close() error       { return nil }
func (s *qualificationFakeScope) Remove(context.Context) error {
	if s.h.removeErr != nil {
		return s.h.removeErr
	}
	return os.Remove(s.dir)
}

func (h *qualificationHarness) newRunner(c productTurnCommandContext) (productTurnCommandRunner, error) {
	journal, err := audit.Open(filepath.Join(c.TurnDir, productCommandOwnerJournal))
	if err != nil {
		return nil, err
	}
	name := "turn-" + strings.TrimPrefix(digestBytes([]byte(c.WorkflowID+"\n"+c.RunID)), "sha256:")
	scopeDir := filepath.Join(h.cells, name)
	if err := os.Mkdir(scopeDir, 0o700); err != nil {
		return nil, errors.Join(err, journal.Close())
	}
	h.mu.Lock()
	h.ownerJournals = append(h.ownerJournals, journal)
	h.mu.Unlock()
	h.crash("scope-created")
	owner := productD09ScopeOwner{Schema: "tbound-product-d09-scope-owner/v1", State: "opened", WorkflowID: c.WorkflowID, RunID: c.RunID,
		RegistrationDigest: digestBytes([]byte("registration")), ProfileDigest: h.profile.Digest, RootDigest: h.rootDigest,
		ScopePath: qualificationCellsPath + "/" + name, ScopeIdentity: h.scopeIdentity(name), CommandOwnershipVersion: 1}
	if err := persistProductCommandScopeOwner(journal, owner); err != nil {
		return nil, errors.Join(err, journal.Close())
	}
	h.crash("owner-recorded")
	var runner *productD09TurnRunner
	runner = newProductD09CommandRunnerWithFactory(func(lease string) (sessionrepo.CommandRunner, error) {
		runner.limitsMu.Lock()
		if runner.limits == nil {
			runner.limits = map[string]piruntime.D09ProductCommandLimits{}
		}
		pids := strconv.Itoa(h.template.PidsMax)
		if h.limitsErr {
			pids = "unreadable"
		}
		runner.limits[lease] = piruntime.D09ProductCommandLimits{PidsMax: pids, MemoryMax: strconv.FormatInt(int64(h.template.MemoryMB)<<20, 10)}
		runner.limitsMu.Unlock()
		cell := &qualificationScriptedCell{h: h, leaseID: lease}
		return newProductionD09BashRunnerWithInvoker(lease, cell, c.Journal, c.RunID, c.SessionID, c.WorkflowID, c.ConversationID, h.profile, h.template), nil
	}, func(context.Context) error {
		if h.cleanupErr != nil {
			return h.cleanupErr
		}
		if err := os.Remove(scopeDir); err != nil {
			return err
		}
		h.crash("before-drained")
		owner.State = "drained"
		if err := persistProductCommandScopeOwner(journal, owner); err != nil {
			return err
		}
		return journal.Close()
	})
	h.mu.Lock()
	h.runners = append(h.runners, runner)
	h.mu.Unlock()
	return runner, nil
}

func (h *qualificationHarness) record(spec sessionrepo.CommandSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.specs = append(h.specs, spec)
}

func (h *qualificationHarness) commandName(script string) string {
	switch {
	case script == productQualificationProofScript:
		return "proof-ack"
	case script == productQualificationListScript:
		return "ls"
	case qualificationWriteCommand.MatchString(script):
		return "write"
	case script == productQualificationReadbackScript:
		return "readback"
	case script == productQualificationRootfsScript:
		return "rootfs"
	case script == productQualificationLimitsScript:
		return "limits"
	case strings.HasPrefix(script, "exec "+productShellQuote(h.profile.ChildExecutable)+" -e "):
		return "network-probe"
	}
	h.t.Fatalf("the cell was given a command that is not a fixed qualification command: %q", script)
	return ""
}

// respond is what a healthy signed cell prints for the command, with its effect on the view.
func (h *qualificationHarness) respond(name, viewDir, script string) (string, int) {
	output := filepath.Join(viewDir, productQualificationOutputFile)
	switch name {
	case "proof-ack":
		return "qualification-proof-ack\n", 0
	case "ls":
		entries, err := os.ReadDir(viewDir)
		if err != nil {
			h.t.Fatal(err)
		}
		var listing string
		for _, entry := range entries {
			listing += entry.Name() + "\n"
		}
		return listing, 0
	case "write":
		if !h.skipWrite {
			if err := os.WriteFile(output, []byte(qualificationWriteCommand.FindStringSubmatch(script)[1]+"\n"), 0o600); err != nil {
				h.t.Fatal(err)
			}
		}
		return "", 0
	case "readback":
		data, _ := os.ReadFile(output)
		return string(data), 0
	case "rootfs":
		return "rootfs-write-denied\n", 0
	case "limits":
		if strings.Contains(script, "/sys/fs/cgroup") {
			h.t.Fatal("frozen Landlock denies cgroup reads")
		}
		return fmt.Sprintf("uid_map=%s\ngid_map=%s\nid=%d:%d\n", fmt.Sprintf("%10d %10d %10d", h.template.CellUID, 0, 1), fmt.Sprintf("%10d %10d %10d", h.template.CellGID, 0, 1), h.template.CellUID, h.template.CellGID), 0
	case "network-probe":
		listener := qualificationListenerLiteral.FindStringSubmatch(script)[1]
		return string(productTestNetworkOutputForProgram(h.t, listener, script)), 0
	}
	h.t.Fatalf("no answer for %s", name)
	return "", 0
}

// qualificationScriptedCell is the per-lease command invoker the harness puts where the signed Podman cell runs.
type qualificationScriptedCell struct {
	h       *qualificationHarness
	leaseID string
	receipt piruntime.D09CommandReceipt
}

func (c *qualificationScriptedCell) Run(_ context.Context, view *sessionrepo.CommandView,
	spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	source, err := view.MountSource()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	defer source.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(source.Fd()), &stat); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	c.h.record(spec)
	name := c.h.commandName(spec.Args[1])
	stdout, exit := c.h.respond(name, fmt.Sprintf("/proc/self/fd/%d", source.Fd()), spec.Args[1])
	if c.h.override != nil {
		stdout, exit = c.h.override(name, stdout, exit)
	}
	template := c.h.template
	result := sessionrepo.CommandResult{ExitObserved: true, ExitCode: exit, Stdout: []byte(stdout)}
	settlement := sessionrepo.CommandSettlement{LeaseID: c.leaseID, ViewID: view.ID(), ExitObserved: true, ExitCode: exit,
		ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true, EvidenceClass: piruntime.D09CommandRunnerProfile + ":kill-at-settlement"}
	templateDigest, err := template.Digest()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	const cellName, cellMountID = "cell-qualification", 706
	proof := piruntime.D09CommandProof{Schema: "tbound-d09-command-proof/v1", UID: template.CellUID, GID: template.CellGID,
		CapEff: "0000000000000000", NoNewPrivs: true, Seccomp: 2, LandlockABI: template.LandlockABI,
		LandlockRulesetDigest: template.LandlockRulesetDigest, LandlockTSYNC: true, CgroupV2: true,
		UIDMap: piruntime.D09InsideUIDMap(template.CellUID), GIDMap: piruntime.D09InsideGIDMap(template.CellGID),
		UserNamespaceMapDigest: template.UserNamespaceMappingDigest,
		LeaderPID:              4242, ContainerLeaderPID: 1, LeaseID: c.leaseID, ViewID: view.ID(),
		CommandStarted: true, CommandReady: true, CommandExitObserved: true, CommandExitCode: exit,
		CommandPath: spec.Executable, CommandDigest: template.CommandExecutableDigest, CommandWorkdir: ".",
		ContainerName: cellName, ImageRef: registeredD09ImageDigestRef(template), ImageDigest: template.ExpectedImageDigest,
		TemplateDigest: templateDigest, ConsumerMountsVerified: true, RawSourceFDsClosed: true,
		WorkerMountID: cellMountID, RuntimeMountID: cellMountID, WorkerDevice: uint64(stat.Dev), WorkerInode: stat.Ino,
		RuntimeDevice: uint64(stat.Dev), RuntimeInode: stat.Ino, NoExternalRoutes: true}
	if c.h.editProof != nil {
		c.h.editProof(name, &proof)
	}
	evidence := piruntime.D09CommandSettlementEvidence{ContainerExitInspected: true, FinalCommandProofRead: true,
		PodmanWaitCompleted: true, OutputWritersDrained: true, ContainerRemoved: true, LeaderPidfdExited: true,
		CgroupPopulatedZero: true, MountNamespaceReleased: true, CommandOutputWithinBound: true, CommandCgroupRemoved: true}
	c.receipt, err = sealD09CommandReceipt(c.leaseID, view.ID(), cellName, strings.Repeat("e", 64), spec, result, settlement, proof, evidence)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	return result, settlement, nil
}

func (c *qualificationScriptedCell) TakeCommandReceipt(*sessionrepo.CommandView, sessionrepo.CommandSpec,
	sessionrepo.CommandResult, sessionrepo.CommandSettlement) (piruntime.D09CommandReceipt, error) {
	receipt := c.receipt
	c.receipt = piruntime.D09CommandReceipt{}
	return receipt, nil
}

// exitProcess closes the owner journals a runner could not drain, as the end of a process does, so a restart can reopen them.
func (h *qualificationHarness) exitProcess() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, journal := range h.ownerJournals {
		_ = journal.Close()
	}
}

// ownerJournalKinds lists the event kinds of one retained owner journal: opened, then drained once the scope was removed.
func ownerJournalKinds(t *testing.T, path string) []string {
	t.Helper()
	journal, err := audit.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, record := range trace.Records {
		kinds = append(kinds, record.Event.Kind)
	}
	return kinds
}
