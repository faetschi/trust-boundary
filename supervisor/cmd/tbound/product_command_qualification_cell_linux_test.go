//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsetup"
)

const productQualificationCellsEnv = "TB_PRODUCT_QUALIFICATION_CELLS"

// qualificationRunDirs lists the run directories a start left under the state root's qualification directory.
func qualificationRunDirs(t *testing.T, stateRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateRoot, productQualificationDir))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, entry := range entries {
		dirs = append(dirs, filepath.Join(stateRoot, productQualificationDir, entry.Name()))
	}
	return dirs
}

func readQualificationEvidence(t *testing.T, runDir string) productQualificationEvidence {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, productQualificationEvidenceFile))
	if err != nil {
		t.Fatal(err)
	}
	var evidence productQualificationEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence
}

func phaseStatuses(evidence productQualificationEvidence) map[productQualificationPhase]string {
	statuses := map[productQualificationPhase]string{}
	for _, phase := range evidence.Phases {
		statuses[phase.Phase] = phase.Status
	}
	return statuses
}

func TestProductQualificationRunsEveryPhaseInTheSignedCellAndRetainsEvidence(t *testing.T) {
	h := newQualificationHarness(t)
	env := h.newEnv(privateGovernedTestRoot(t))
	h.install(t, h.steps())
	hooks, err := qualifyProductCommandHost(context.Background(), productServiceConfig{}, env, true)
	if err != nil {
		t.Fatalf("a healthy cell was refused: %v", err)
	}
	if hooks.NewCommandRunner == nil || hooks.OpenCommandHistory == nil {
		t.Fatal("a passing qualification installed no command hook")
	}
	dirs := qualificationRunDirs(t, env.StateRoot)
	if len(dirs) != 1 {
		t.Fatalf("qualification runs %v, want one", dirs)
	}
	evidence := readQualificationEvidence(t, dirs[0])
	if evidence.Status != "passed" || evidence.Failure != "" || evidence.ProfileDigest != h.profile.Digest || evidence.ImageDigest != h.template.ExpectedImageDigest {
		t.Fatalf("evidence %+v", evidence)
	}
	if evidence.Limits == nil || evidence.Limits.PidsMax != "128" || evidence.Limits.MemoryMax != "1073741824" {
		t.Fatalf("supervisor limit evidence: %+v", evidence.Limits)
	}
	for _, phase := range productQualificationPhases {
		if phaseStatuses(evidence)[phase] != "passed" {
			t.Fatalf("phase %s is %q in the retained evidence", phase, phaseStatuses(evidence)[phase])
		}
	}
	if len(evidence.Commands) != 7 || len(h.specs) != 7 {
		t.Fatalf("the cell ran %d commands, evidence records %d, want the seven fixed ones", len(h.specs), len(evidence.Commands))
	}
	for _, spec := range h.specs {
		if spec.Executable != "/bin/bash" || len(spec.Args) != 2 || spec.Args[0] != "-lc" {
			t.Fatalf("command spec %+v is not the product's /bin/bash -lc mapping", spec)
		}
	}
	write := evidence.Commands[2]
	if write.Name != "write" || !slices.Equal(write.Changed, []string{productQualificationOutputFile}) || write.InputGeneration == write.OutputGeneration {
		t.Fatalf("scoped write record %+v", write)
	}
	for _, command := range evidence.Commands {
		if command.Name != "write" && len(command.Changed) != 0 {
			t.Fatalf("%s changed %v", command.Name, command.Changed)
		}
	}
	// Both retained files are private, and the settled scope was drained, not left for the next start.
	for path, mode := range map[string]os.FileMode{dirs[0]: 0o700, filepath.Join(dirs[0], productQualificationEvidenceFile): 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode %v err %v, want %v", path, info.Mode().Perm(), err, mode)
		}
	}
	if kinds := ownerJournalKinds(t, filepath.Join(dirs[0], productCommandOwnerJournal)); !slices.Equal(kinds, []string{"product_command_scope_opened", "product_command_scope_drained"}) {
		t.Fatalf("owner journal %v", kinds)
	}
	if entries, _ := os.ReadDir(h.cells); len(entries) != 0 {
		t.Fatalf("the qualification left cells %v", entries)
	}
	if pending := h.runners[0].Pending(); pending != 0 {
		t.Fatalf("%d command owners remain", pending)
	}
}

// The probe runs through the Node the signed profile names and is judged against service-side controls, so a
// dead network cannot pass as containment.
func TestProductQualificationProbeUsesThePinnedNodeAndServiceSideControls(t *testing.T) {
	h := newQualificationHarness(t)
	env := h.newEnv(privateGovernedTestRoot(t))
	h.install(t, h.steps())
	if _, err := qualifyProductCommandHost(context.Background(), productServiceConfig{}, env, true); err != nil {
		t.Fatal(err)
	}
	var probe string
	for _, spec := range h.specs {
		if strings.HasPrefix(spec.Args[1], "exec ") {
			probe = spec.Args[1]
		}
	}
	if !strings.HasPrefix(probe, "exec '/usr/local/bin/node' -e '") {
		t.Fatalf("the probe did not run through the signed profile's Node: %q", probe)
	}
	evidence := readQualificationEvidence(t, qualificationRunDirs(t, env.StateRoot)[0])
	if evidence.Network == nil || evidence.Network.Policy != productNetworkPolicyDenyAll || !strings.HasPrefix(evidence.Network.Node, "v") || len(evidence.Network.Checks) != 3 {
		t.Fatalf("network evidence %+v", evidence.Network)
	}
	controls := map[string]productNetworkCheck{}
	for _, control := range evidence.Network.Controls {
		controls[control.ID] = control
	}
	if controls["host-listener"].Outcome != "reachable" || controls["ipv4-connect"].Outcome != "reachable" || controls["ipv6-connect"].Outcome != "not available on host" || controls["name-resolution"].Outcome != "resolved" {
		t.Fatalf("controls %+v", controls)
	}
	if !strings.Contains(probe, `"`+controls["host-listener"].Destination+`"`) {
		t.Fatal("the probe was not given the listener the service opened")
	}
	if !slices.Equal(h.routes, []string{"tcp4 192.0.2.1:443", "tcp6 [2001:db8::1]:443"}) {
		t.Fatalf("route controls %v", h.routes)
	}
}

func TestProductQualificationRefusesEachFailingPhase(t *testing.T) {
	cases := []struct {
		name   string
		phase  productQualificationPhase
		break_ func(h *qualificationHarness)
		want   string
	}{
		{"proof without readiness", productPhaseProofAck, func(h *qualificationHarness) {
			h.editProof = func(name string, p *piruntime.D09CommandProof) {
				if name == "proof-ack" {
					p.CommandReady = false
				}
			}
		}, "command runner failed"},
		{"proof as another identity", productPhaseProofAck, func(h *qualificationHarness) {
			h.editProof = func(name string, p *piruntime.D09CommandProof) {
				if name == "proof-ack" {
					p.CapEff = "0000000000000001"
				}
			}
		}, "command runner failed"},
		{"listing without the seed", productPhaseListing, func(h *qualificationHarness) {
			h.override = func(name, stdout string, exit int) (string, int) {
				if name == "ls" {
					return "other.txt\n", exit
				}
				return stdout, exit
			}
		}, "does not list"},
		{"write that changes nothing", productPhaseScopedWrite, func(h *qualificationHarness) { h.skipWrite = true }, "want exactly"},
		{"readback of other bytes", productPhaseScopedWrite, func(h *qualificationHarness) {
			h.override = func(name, stdout string, exit int) (string, int) {
				if name == "readback" {
					return "tampered\n", exit
				}
				return stdout, exit
			}
		}, "readback printed"},
		{"a rootfs write the cell allowed", productPhaseRootfsDenial, func(h *qualificationHarness) {
			h.override = func(name, stdout string, exit int) (string, int) {
				if name == "rootfs" {
					return "rootfs-write-allowed /qualification-rootfs-probe\n", 3
				}
				return stdout, exit
			}
		}, "rootfs exited 3"},
		{"a limit the supervisor cannot read", productPhaseLimitsMapping, func(h *qualificationHarness) { h.limitsErr = true }, "pids.max"},
		{"a proof mapping other than the signed one", productPhaseLimitsMapping, func(h *qualificationHarness) {
			h.editProof = func(name string, p *piruntime.D09CommandProof) {
				if name == "limits" {
					p.UIDMap = "1 0 65536"
				}
			}
		}, "ID maps"},
		{"a cell map other than the signed one", productPhaseLimitsMapping, func(h *qualificationHarness) {
			h.override = func(name, stdout string, exit int) (string, int) {
				if name == "limits" {
					return strings.Replace(stdout, "         1", "        42", 1), exit
				}
				return stdout, exit
			}
		}, "map"},
		{"a missing node executable", productPhaseNetworkProbe, func(h *qualificationHarness) {
			h.override = func(name, stdout string, exit int) (string, int) {
				if name == "network-probe" {
					return "", 127
				}
				return stdout, exit
			}
		}, "exited 127"},
		{"a host the cell reached", productPhaseNetworkProbe, func(h *qualificationHarness) {
			h.override = func(name, stdout string, exit int) (string, int) {
				if name == "network-probe" {
					var report productNetworkReport
					json.Unmarshal([]byte(stdout), &report)
					for i := range report.Checks {
						if report.Checks[i].ID == "host-listener" {
							report.Checks[i].Outcome, report.Checks[i].Errno = "reachable", ""
						}
					}
					raw, _ := json.Marshal(report)
					return string(raw), exit
				}
				return stdout, exit
			}
		}, "host-listener"},
		{"a service host without an IPv4 route", productPhaseNetworkProbe, func(h *qualificationHarness) {
			h.connect = func(network, _ string) error {
				if network == "tcp4" {
					return errors.New("network is unreachable")
				}
				return nil
			}
		}, "dead network"},
		{"a scope that cannot be drained", productPhaseSettlement, func(h *qualificationHarness) { h.cleanupErr = errors.New("scope stuck") }, "scope stuck"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newQualificationHarness(t)
			env := h.newEnv(privateGovernedTestRoot(t))
			h.install(t, h.steps())
			tc.break_(h)
			hooks, err := qualifyProductCommandHost(context.Background(), productServiceConfig{}, env, true)
			if err == nil || !strings.Contains(err.Error(), "phase "+string(tc.phase)) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("a %s passed or failed for another reason: %v", tc.name, err)
			}
			if hooks.NewCommandRunner != nil || hooks.OpenCommandHistory != nil {
				t.Fatal("a failed qualification returned a command hook")
			}
			evidence := readQualificationEvidence(t, qualificationRunDirs(t, env.StateRoot)[0])
			if evidence.Status != "failed" || !strings.Contains(evidence.Failure, string(tc.phase)) {
				t.Fatalf("failed run retained %+v", evidence)
			}
			failedAt := slices.Index(productQualificationPhases, tc.phase)
			for index, phase := range productQualificationPhases {
				want := "passed"
				switch {
				case index == failedAt:
					want = "failed"
				case index > failedAt:
					want = "not-run"
				}
				if got := phaseStatuses(evidence)[phase]; got != want {
					t.Fatalf("phase %s is %q, want %q", phase, got, want)
				}
			}
			h.exitProcess()
			kinds := ownerJournalKinds(t, filepath.Join(qualificationRunDirs(t, env.StateRoot)[0], productCommandOwnerJournal))
			entries, _ := os.ReadDir(h.cells)
			// A command the cell never settled, and a scope that cannot be drained, keep their owner: the scope stays recorded as
			// opened for the next start to drain.
			if tc.phase == productPhaseSettlement || tc.phase == productPhaseProofAck {
				if len(entries) != 1 || !slices.Equal(kinds, []string{"product_command_scope_opened"}) {
					t.Fatalf("an undrained scope lost its owner: cells %v journal %v", entries, kinds)
				}
				h.cleanupErr = nil
				h.installScopeDrain(t, 0)
				root := openProductCellsRoot(t, h.cells)
				roots := []string{filepath.Join(env.StateRoot, productQualificationDir)}
				if err := reconcileProductCommandScopesUnder(context.Background(), root, roots...); err != nil {
					t.Fatalf("the restart could not drain the retained scope: %v", err)
				}
				if left, _ := os.ReadDir(h.cells); len(left) != 0 {
					t.Fatalf("the drained scope remains: %v", left)
				}
				return
			}
			if len(entries) != 0 || !slices.Equal(kinds, []string{"product_command_scope_opened", "product_command_scope_drained"}) {
				t.Fatalf("a failed phase left its scope behind: cells %v journal %v", entries, kinds)
			}
		})
	}
}

// qualificationPolicyFile writes an installed gate policy, with or without an allow rule for bash, and returns its path
// and the digest a signed registration would commit to.
func qualificationPolicyFile(t *testing.T, bash bool) (string, string) {
	t.Helper()
	rules := []gate.Rule{{Tool: "read", Effect: gate.EffectAllow}}
	if bash {
		rules = append(rules, gate.Rule{Tool: "bash", Effect: gate.EffectAllow})
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(policy.Profile())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(privateGovernedTestRoot(t), "policy.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, productRegisteredPolicyDigest(policy)
}

// The structural read names a policy that allows bash as configured. It never admits bash, and doctor and setup
// never report the command cell as available from static inputs.
func TestProductStaticPolicyReadsNeverReportBashAvailable(t *testing.T) {
	path, digest := qualificationPolicyFile(t, true)
	owners := []uint32{0, uint32(os.Geteuid())}
	if _, _, err := loadProductGatePolicy("alpha", digest, path, owners, productBashRefused); err == nil || !strings.Contains(err.Error(), "allows bash") {
		t.Fatalf("a refusing read accepted a bash policy: %v", err)
	}
	policy, _, err := loadProductGatePolicy("alpha", digest, path, owners, productBashStructural)
	if err != nil || !productPolicyAllowsBash(policy) {
		t.Fatalf("a structural read refused or lost the bash rule: %v", err)
	}
	inputs := productsetup.HostInputs{PolicyDigests: map[string]string{"alpha": digest}, GatePolicyPaths: map[string]string{"alpha": path}, Owners: owners}
	policyCheck := func() productsetup.Check {
		for _, c := range checkProductHostInputs(inputs) {
			if c.Name == "input policy alpha" {
				return c
			}
		}
		t.Fatal("no policy check")
		return productsetup.Check{}
	}
	if c := policyCheck(); c.Status != productsetup.Fail || !strings.Contains(c.Detail, "allows bash") {
		t.Fatalf("a bash policy without verified command inputs passed: %+v", c)
	}
	inputs.CommandInputsValid = true
	c := policyCheck()
	if c.Status != productsetup.Pass || !strings.Contains(c.Detail, "configured, not qualified") ||
		strings.Contains(strings.ToLower(c.Detail), "available") || strings.Contains(strings.ToLower(c.Detail), "ready") {
		t.Fatalf("verified static inputs were reported as more than configuration: %+v", c)
	}
	denyPath, denyDigest := qualificationPolicyFile(t, false)
	inputs.PolicyDigests, inputs.GatePolicyPaths = map[string]string{"alpha": denyDigest}, map[string]string{"alpha": denyPath}
	if c := policyCheck(); c.Status != productsetup.Pass || strings.Contains(c.Detail, "bash") {
		t.Fatalf("a deny-only policy was annotated: %+v", c)
	}
}

func TestProductQualificationEvidenceCheckSummarizesTheLatestRunAsAdvisory(t *testing.T) {
	stateDir := privateGovernedTestRoot(t)
	if c := productQualificationEvidenceCheck(stateDir); c.Status != productsetup.Warn || !strings.Contains(c.Detail, "no qualification evidence") {
		t.Fatalf("no evidence: %+v", c)
	}
	write := func(run, status, failure string, phases ...string) {
		dir := filepath.Join(stateDir, productQualificationDir, run)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		evidence := productQualificationEvidence{Schema: productQualificationEvidenceSchema, RunID: run, Status: status, Failure: failure,
			FinishedAt: "2026-11-03T10:00:00Z", Network: &productNetworkReport{Node: "v22.12.0"}}
		for index, state := range phases {
			evidence.Phases = append(evidence.Phases, productQualificationPhaseResult{Phase: productQualificationPhases[index], Status: state})
		}
		encoded, _ := json.Marshal(evidence)
		if err := os.WriteFile(filepath.Join(dir, productQualificationEvidenceFile), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("q-1", "failed", "phase ls: boom", "passed", "failed")
	write("q-2", "passed", "", "passed", "passed", "passed")
	c := productQualificationEvidenceCheck(stateDir)
	if c.Status != productsetup.Pass || !strings.Contains(c.Detail, "q-2") || !strings.Contains(c.Detail, "3 of 3") ||
		!strings.Contains(c.Detail, "advisory") || !strings.Contains(c.Detail, "Node v22.12.0") {
		t.Fatalf("passed run: %+v", c)
	}
	// A run killed before it retained evidence has none: the latest retained run answers.
	if err := os.MkdirAll(filepath.Join(stateDir, productQualificationDir, "q-3"), 0o700); err != nil {
		t.Fatal(err)
	}
	if c := productQualificationEvidenceCheck(stateDir); !strings.Contains(c.Detail, "q-2") {
		t.Fatalf("a run without evidence hid the latest retained one: %+v", c)
	}
	write("q-4", "failed", "phase network-probe: dead network", "passed", "failed")
	if c := productQualificationEvidenceCheck(stateDir); c.Status != productsetup.Warn || !strings.Contains(c.Detail, "dead network") || c.Fix == "" {
		t.Fatalf("failed run: %+v", c)
	}
	if err := os.WriteFile(filepath.Join(stateDir, productQualificationDir, "q-5"+"x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, productQualificationDir, "q-5"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, productQualificationDir, "q-5", productQualificationEvidenceFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := productQualificationEvidenceCheck(stateDir); c.Status != productsetup.Warn || !strings.Contains(c.Detail, "unreadable") {
		t.Fatalf("malformed evidence: %+v", c)
	}
	if !slices.ContainsFunc(hostPrerequisites(), func(c productsetup.Check) bool { return c.Name == "command qualification" }) {
		t.Fatal("doctor's host prerequisites do not include the qualification evidence")
	}
}

// plantOpenedOrphan records one command scope as a killed process leaves it: an fsynced opened record, and a scope
// directory when present is true.
func (h *qualificationHarness) plantOpenedOrphan(t *testing.T, stateRoot, workflow string, present bool) productD09ScopeOwner {
	t.Helper()
	name := "turn-" + workflow
	owner := productD09ScopeOwner{Schema: "tbound-product-d09-scope-owner/v1", State: "opened", WorkflowID: workflow, RunID: "run-" + workflow,
		RegistrationDigest: digestBytes([]byte("registration")), ProfileDigest: digestBytes([]byte("profile")), RootDigest: h.rootDigest,
		ScopePath: qualificationCellsPath + "/" + name, ScopeIdentity: h.scopeIdentity(name), CommandOwnershipVersion: 1}
	plantOwnerJournal(t, stateRoot, workflow, owner)
	if present {
		if err := os.Mkdir(filepath.Join(h.cells, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return owner
}

func TestProductD09OrphanScopeIsDrainedOrBlocksStartup(t *testing.T) {
	cases := []struct {
		name    string
		present bool
		writers int
		arrange func(h *qualificationHarness)
		drained bool
		want    string
	}{
		{"a recorded scope is revoked and recorded drained", true, 0, func(*qualificationHarness) {}, true, ""},
		{"a scope whose root differs from the record", true, 0, func(h *qualificationHarness) { h.rootDigest = digestBytes([]byte("other-root")) }, false, "scope"},
		{"a scope that cannot be removed", true, 0, func(h *qualificationHarness) { h.removeErr = errors.New("still populated") }, false, "still populated"},
		{"a scope that is gone with no process left", false, 0, func(*qualificationHarness) {}, true, ""},
		{"a scope that is gone while a process still reports it", false, 1, func(*qualificationHarness) {}, false, "still report"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newQualificationHarness(t)
			stateRoot := privateGovernedTestRoot(t)
			retained := filepath.Join(stateRoot, "turns")
			h.plantOpenedOrphan(t, stateRoot, "pwf-orphan", tc.present)
			tc.arrange(h)
			commandJournal := filepath.Join(retained, "pwf-orphan", productTurnCommandJournal)
			if err := os.WriteFile(commandJournal, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			h.installScopeDrain(t, tc.writers)
			err := reconcileProductCommandScopesUnder(context.Background(), openProductCellsRoot(t, h.cells), retained)
			kinds := ownerJournalKinds(t, filepath.Join(retained, "pwf-orphan", productCommandOwnerJournal))
			if tc.drained {
				if err != nil || !slices.Equal(kinds, []string{"product_command_scope_opened", "product_command_scope_drained"}) {
					t.Fatalf("an owned orphan was not drained: %v %v", err, kinds)
				}
				if left, _ := os.ReadDir(h.cells); len(left) != 0 {
					t.Fatalf("the scope remains: %v", left)
				}
				if err := reconcileProductCommandScopesUnder(context.Background(), openProductCellsRoot(t, h.cells), retained); err != nil {
					t.Fatalf("a drained owner blocked a later start: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || !slices.Equal(kinds, []string{"product_command_scope_opened"}) {
				t.Fatalf("unproved cleanup did not block: %v %v", err, kinds)
			}
			// Killing a leftover never manufactures a result: the turn's command journal gains no receipt.
			if info, statErr := os.Stat(commandJournal); statErr != nil || info.Size() != 0 {
				t.Fatalf("the drain wrote command evidence: %v", statErr)
			}
		})
	}
	t.Run("an identity that differs from the record blocks and removes nothing", func(t *testing.T) {
		h := newQualificationHarness(t)
		stateRoot := privateGovernedTestRoot(t)
		owner := h.plantOpenedOrphan(t, stateRoot, "pwf-swapped", true)
		_ = os.RemoveAll(filepath.Join(stateRoot, "turns", "pwf-swapped"))
		owner.ScopeIdentity = digestBytes([]byte("another-inode"))
		plantOwnerJournal(t, stateRoot, "pwf-swapped", owner)
		h.installScopeDrain(t, 0)
		err := reconcileProductCommandScopesUnder(context.Background(), openProductCellsRoot(t, h.cells), filepath.Join(stateRoot, "turns"))
		if !errors.Is(err, piruntime.ErrCgroupScopeRefused) {
			t.Fatalf("a swapped scope was drained: %v", err)
		}
		if left, _ := os.ReadDir(h.cells); len(left) != 1 {
			t.Fatalf("a refused identity removed the scope: %v", left)
		}
	})
	t.Run("a child with no owner record is never removed", func(t *testing.T) {
		h := newQualificationHarness(t)
		if err := os.Mkdir(filepath.Join(h.cells, "turn-stray"), 0o700); err != nil {
			t.Fatal(err)
		}
		h.installScopeDrain(t, 0)
		err := reconcileProductCommandScopesUnder(context.Background(), openProductCellsRoot(t, h.cells), filepath.Join(privateGovernedTestRoot(t), "turns"))
		if err == nil || !strings.Contains(err.Error(), "unrecorded product command scope") {
			t.Fatalf("an unknown child did not block: %v", err)
		}
		if left, _ := os.ReadDir(h.cells); len(left) != 1 {
			t.Fatalf("an unknown child was removed: %v", left)
		}
	})
}

// TestProductQualificationCrashChild runs only as the fresh process the kill and restart test starts. It qualifies a
// Bash host and exits without any cleanup at the named point.
func TestProductQualificationCrashChild(t *testing.T) {
	state, point, cells := os.Getenv(productCrashRootEnv), os.Getenv(productCrashPointEnv), os.Getenv(productQualificationCellsEnv)
	if state == "" || point == "" || cells == "" {
		t.Skip("fresh-process qualification crash helper")
	}
	h := newQualificationHarness(t)
	h.cells = cells
	env := h.newEnv(state)
	env.crashAt = func(at string) {
		if at == point {
			os.Exit(productCrashExit)
		}
	}
	h.install(t, h.steps())
	_, err := qualifyProductCommandHost(context.Background(), productServiceConfig{}, env, true)
	t.Fatalf("the child reached no crash point %q: %v", point, err)
}

// A process killed at a boundary of the qualification leaves what it owned. The next start drains an owned scope with
// the identity its record names, and refuses to start while it cannot prove a leftover is owned or gone.
func TestProductQualificationKillAndRestart(t *testing.T) {
	cases := []struct {
		point   string
		blocked bool
		settled int
	}{
		{"scope-created", true, 0},
		{"owner-recorded", false, 0},
		{"qualification-command-settled", false, 1},
		{"qualification-before-settle", false, 7},
		{"before-drained", false, 7},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			root := privateGovernedTestRoot(t)
			state, cells := filepath.Join(root, "state"), filepath.Join(root, "cells")
			for _, dir := range []string{state, cells} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			child := exec.Command(os.Args[0], "-test.run=^TestProductQualificationCrashChild$", "-test.count=1")
			child.Env = append(os.Environ(), productCrashRootEnv+"="+state, productCrashPointEnv+"="+tc.point, productQualificationCellsEnv+"="+cells)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != productCrashExit {
				t.Fatalf("the child did not die at %s: %v\n%s", tc.point, err, output)
			}
			crashed := qualificationRunDirs(t, state)
			if len(crashed) != 1 {
				t.Fatalf("the killed process left %v", crashed)
			}
			settledEvents := func(dir string) int {
				journal, err := os.ReadFile(filepath.Join(dir, productTurnCommandJournal))
				if err != nil {
					return 0
				}
				return strings.Count(string(journal), productionD09SettledKind)
			}
			before := settledEvents(crashed[0])
			if before != tc.settled {
				t.Fatalf("the killed run had bound %d receipts, want %d", before, tc.settled)
			}

			h := newQualificationHarness(t)
			h.cells = cells
			env := h.newEnv(state)
			steps := h.steps()
			steps.reconcileScopes = func(ctx context.Context, _ *piruntime.D09ProductCommandHost, roots []string) error {
				return reconcileProductCommandScopesUnder(ctx, openProductCellsRoot(t, cells), roots...)
			}
			h.install(t, steps)
			h.installScopeDrain(t, 0)
			hooks, err := qualifyProductCommandHost(context.Background(), productServiceConfig{}, env, true)
			if tc.blocked {
				if err == nil || !strings.Contains(err.Error(), "unrecorded product command scope") || hooks.NewCommandRunner != nil {
					t.Fatalf("a scope without an owner record did not block the restart: %v", err)
				}
				if left, _ := os.ReadDir(cells); len(left) != 1 {
					t.Fatalf("the restart removed a scope it does not own: %v", left)
				}
				return
			}
			if err != nil || hooks.NewCommandRunner == nil {
				t.Fatalf("the restart did not drain the owned orphan: %v", err)
			}
			if kinds := ownerJournalKinds(t, filepath.Join(crashed[0], productCommandOwnerJournal)); !slices.Equal(kinds, []string{"product_command_scope_opened", "product_command_scope_drained"}) {
				t.Fatalf("the killed run's owner journal %v", kinds)
			}
			if _, err := os.Stat(filepath.Join(crashed[0], productQualificationEvidenceFile)); !os.IsNotExist(err) {
				t.Fatal("a killed run retained evidence it never wrote")
			}
			if after := settledEvents(crashed[0]); after != before {
				t.Fatalf("the drain changed the killed run's receipts from %d to %d", before, after)
			}
			if left, _ := os.ReadDir(cells); len(left) != 0 {
				t.Fatalf("cells remain after the restart: %v", left)
			}
			runs := qualificationRunDirs(t, state)
			if len(runs) != 2 {
				t.Fatalf("runs %v, want the killed one and the new one", runs)
			}
			var fresh []string
			for _, run := range runs {
				if run != crashed[0] {
					fresh = append(fresh, run)
				}
			}
			if readQualificationEvidence(t, fresh[0]).Status != "passed" {
				t.Fatal("the restarted start did not qualify")
			}
			if c := productQualificationEvidenceCheck(state); c.Status != productsetup.Pass || !strings.Contains(c.Detail, filepath.Base(fresh[0])) {
				t.Fatalf("doctor's summary %+v", c)
			}
		})
	}
}

func TestProductBashStartupRunsTheRealCellBeforeListening(t *testing.T) {
	t.Run("a healthy cell opens the socket with both hooks", func(t *testing.T) {
		f := newProductHostFixture(t, true)
		h := newQualificationHarness(t)
		h.install(t, h.steps())
		svc, env, err := startWithHostCapture(f)
		if err != nil {
			t.Fatalf("a qualified Bash host was refused: %v", err)
		}
		defer func() { _ = svc.Shutdown() }()
		if env.NewCommandRunner == nil || env.OpenCommandHistory == nil {
			t.Fatal("qualification installed no hook")
		}
		if _, err := os.Stat(f.socketPath); err != nil {
			t.Fatalf("no socket: %v", err)
		}
		if evidence := readQualificationEvidence(t, qualificationRunDirs(t, f.stateRoot)[0]); evidence.Status != "passed" {
			t.Fatalf("evidence %+v", evidence)
		}
	})
	t.Run("a rootfs the cell could write refuses the start", func(t *testing.T) {
		f := newProductHostFixture(t, true)
		h := newQualificationHarness(t)
		h.install(t, h.steps())
		h.override = func(name, stdout string, exit int) (string, int) {
			if name == "rootfs" {
				return "rootfs-write-allowed /qualification-rootfs-probe\n", 3
			}
			return stdout, exit
		}
		svc, env, err := startWithHostCapture(f)
		if svc != nil {
			_ = svc.Shutdown()
		}
		if err == nil || !strings.Contains(err.Error(), "phase rootfs-denial") {
			t.Fatalf("a writable rootfs started the service: %v", err)
		}
		if _, statErr := os.Stat(f.socketPath); !os.IsNotExist(statErr) {
			t.Fatal("a failed qualification opened the socket")
		}
		if env.NewCommandRunner != nil || env.OpenCommandHistory != nil {
			t.Fatal("a failed qualification installed a hook")
		}
	})
}
