//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
)

const (
	drTarget  = "/home/node/.profile"
	drControl = "src/release.txt"
)

// drWorld is a complete direct-runtime probe run: the production and probe templates, the TCB proof the
// probe cell gave, the session journal that holds it and the image tree root sees the target in.
type drWorld struct {
	dir                string
	probeTemplate      piruntime.D09CellTemplate
	productionTemplate piruntime.D09CellTemplate
	proof              piruntime.D09TCBProof
	opts               routeProbeOptions
}

func drTemplate(t *testing.T) piruntime.D09CellTemplate {
	t.Helper()
	landlock, err := piruntime.DigestD09LandlockRules([]string{"/usr", "/etc"}, []string{"/work", "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	cellUID, cellGID := 100731, 100732
	mountOptions := piruntime.DigestBytes([]byte("ro,relatime"))
	return piruntime.D09CellTemplate{
		Image: "localhost/tbound/pi-cell:latest", ExpectedImageDigest: geDigest("image"),
		CosignPublicKeyPath: "/etc/tbound/cosign.pub", CosignPublicKeyDigest: geDigest("cosign-pub"),
		OCILayoutPath: "/etc/tbound/oci", SeccompProfilePath: "/etc/tbound/seccomp.json", SeccompProfileDigest: geDigest("seccomp"),
		LandlockABI: 8, LandlockRulesetDigest: landlock, LandlockROPath: "/usr,/etc", LandlockRWPath: "/work,/tmp",
		FrozenCapabilities: "0000000000000000", EntrypointPath: "/usr/local/bin/tbound-pi-entrypoint", EntrypointDigest: geDigest("entrypoint"),
		Runtime: "crun", CgroupsPath: "/sys/fs/cgroup/tbound", CPUs: "1", PidsMax: 256, MemoryMB: 1024,
		PodmanPath: "/usr/bin/podman", PodmanDigest: geDigest("podman"), CrunPath: "/usr/bin/crun", CrunDigest: geDigest("crun"),
		CosignPath: "/usr/bin/cosign", CosignDigest: geDigest("cosign"),
		CommandExecutable: "/bin/bash", CommandExecutableDigest: geDigest("bash"),
		CellUID: cellUID, CellGID: cellGID,
		NamespaceProfile: "user,mount,pid,ipc,uts,network-none/v1", CgroupVersion: "v2", CgroupControllers: "cpu,memory,pids",
		XattrFilesystemMagic: 0xef53, XattrMountOptionsDigest: mountOptions, XattrScanUID: uid, XattrScanGID: gid,
		XattrVisibilityProfileDigest: piruntime.DigestD09XattrVisibilityProfile(0xef53, mountOptions, uid, gid, piruntime.D09XattrScanPrivilegeProfile),
		XattrScanPrivilegeProfile:    piruntime.D09XattrScanPrivilegeProfile, XattrVisibilityAuthority: piruntime.D09XattrVisibilityAuthority,
		ManagedLiveRootRegistrationDigest: geDigest("registration"),
		UserNamespaceMappingProfile:       piruntime.D09UserNamespaceMappingProfile,
		UserNamespaceMappingDigest:        piruntime.DigestD09UserNamespaceMapping(cellUID, cellGID, uid, gid),
	}
}

func drWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// drDeniedProbe is the result a cell that denies the target reports.
func drDeniedProbe(p piruntime.D09RouteProbe, mutate func(*piruntime.D09RouteProbeResult)) *piruntime.D09RouteProbeResult {
	result := piruntime.RunD09RouteProbe(deniedFS{}, p)
	if mutate != nil {
		mutate(&result)
	}
	return &result
}

// deniedFS refuses every open and listing and shows the metadata of every stat, as the cell does.
type deniedFS struct{}

func (deniedFS) OpenRead(p string) error {
	if strings.HasPrefix(p, "/work/") {
		return nil
	}
	return syscall.EACCES
}
func (deniedFS) Stat(string) error { return nil }
func (deniedFS) List(string) error { return syscall.EACCES }

// drBuild writes the world. mutateProof changes the proof before it is journaled; mutateEvent changes the
// journal event.
func drBuild(t *testing.T, mutateProbe func(*piruntime.D09RouteProbeResult), mutateProof func(*piruntime.D09TCBProof), mutateEvent func(*map[string]any)) drWorld {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory for a private audit journal")
	}
	dir, err := os.MkdirTemp(home, ".direct-runtime-test-")
	if err != nil {
		t.Skipf("cannot make a private audit directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w := drWorld{dir: dir, productionTemplate: drTemplate(t)}
	w.probeTemplate = w.productionTemplate
	probe := piruntime.D09RouteProbe{Name: piruntime.D09RouteProbeName, Target: drTarget, Control: "/work/" + drControl}
	w.probeTemplate.RouteProbe = &probe
	probeDigest, err := w.probeTemplate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	w.proof = piruntime.D09TCBProof{Schema: "tbound-pi-entrypoint/v1", UID: w.probeTemplate.CellUID, GID: w.probeTemplate.CellGID, CapEff: "0000000000000000",
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: w.probeTemplate.LandlockRulesetDigest, LandlockTSYNC: true, CgroupV2: true,
		ContainerName: "tbound-d09-cell-abc", LeaderPID: 4242, ContainerLeaderPID: 1, LauncherProcessPID: 4000,
		ImageRef: "localhost/tbound/pi-cell@" + w.probeTemplate.ExpectedImageDigest, ImageDigest: w.probeTemplate.ExpectedImageDigest, TemplateDigest: probeDigest,
		ConsumerMountsVerified: true, RawSourceFDsClosed: true, WorkerMountID: 11, RuntimeMountID: 12, WorkerDevice: 1, WorkerInode: 2, RuntimeDevice: 3, RuntimeInode: 4,
		NoExternalRoutes: true, RouteProbe: drDeniedProbe(probe, mutateProbe)}
	if mutateProof != nil {
		mutateProof(&w.proof)
	}
	encodedProof, err := json.Marshal(w.proof)
	if err != nil {
		t.Fatal(err)
	}
	j, err := audit.Open(filepath.Join(dir, rtRunID+"-session-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(audit.Event{Kind: "production_session_started", ID: "start"}); err != nil {
		t.Fatal(err)
	}
	event := map[string]any{"schema_version": "tbound-production-launch-tcb-proof/v1", "run_id": rtRunID, "session_id": "s", "workflow_id": "w",
		"conversation_id": "c", "source_generation_id": "g0", "source_binding_digest": geDigest("binding"), "profile_id": "p", "profile_digest": rtProfile,
		"proof_digest": piruntime.DigestBytes(encodedProof), "proof": json.RawMessage(encodedProof)}
	if mutateEvent != nil {
		mutateEvent(&event)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(audit.Event{Kind: "production_tcb_proof", ID: "production-tcb-proof:" + rtRunID, Data: data}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// The image tree root sees: the target is world-readable in a listable directory.
	targetRoot := filepath.Join(dir, "image")
	if err := os.MkdirAll(filepath.Join(targetRoot, "home", "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(targetRoot, "home", "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, drTarget), []byte("export PATH=$HOME/bin:$PATH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(targetRoot, drTarget), 0o644); err != nil { // the umask of the run must not narrow it
		t.Fatal(err)
	}
	probeTemplatePath, productionTemplatePath := filepath.Join(dir, "probe-template.json"), filepath.Join(dir, "production-template.json")
	drWriteJSON(t, probeTemplatePath, w.probeTemplate)
	drWriteJSON(t, productionTemplatePath, w.productionTemplate)
	w.opts = routeProbeOptions{route: "direct-runtime", runID: rtRunID, auditRoot: dir, target: drTarget, controlPath: drControl,
		profileHash: rtProfile, frozenSource: rtSource, probeTemplate: probeTemplatePath, productionTemplate: productionTemplatePath, targetRoot: targetRoot}
	return w
}

func TestDirectRuntimeProbeBuildsAnArtifactThatPasses(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	d, kind, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err != nil {
		t.Fatal(err)
	}
	if kind != guestevidence.KindRouteDirectRuntime || d.Cell.Kind != guestevidence.RouteCellPiRuntime || len(d.Attempts) != 9 || d.Journal.Records != 2 {
		t.Fatalf("artifact = %+v kind %s", d, kind)
	}
	probeDigest, _ := w.probeTemplate.Digest()
	productionDigest, _ := w.productionTemplate.Digest()
	if d.Cell.TemplateDigest != probeDigest || d.Cell.ProductionTemplateDigest != productionDigest || d.Cell.ImageDigest != w.probeTemplate.ExpectedImageDigest {
		t.Errorf("cell = %+v", d.Cell)
	}
	if d.TargetFact == nil || !d.TargetFact.Regular || d.TargetFact.Mode != 0o644 || d.TargetFact.ParentMode != 0o755 || d.TargetFact.Size == 0 {
		t.Errorf("target fact = %+v", d.TargetFact)
	}
	if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), rtExpect()); err != nil {
		t.Fatalf("the artifact is refused: %v", err)
	}
}

func TestDirectRuntimeProbeWhereAnAttemptGotThroughIsNotEvidence(t *testing.T) {
	open := func(method, op string, errno int) func(*piruntime.D09RouteProbeResult) {
		return func(r *piruntime.D09RouteProbeResult) {
			for i := range r.Attempts {
				if r.Attempts[i].Method == method && r.Attempts[i].Op == op {
					r.Attempts[i].Errno = errno
				}
			}
		}
	}
	for name, mutate := range map[string]func(*piruntime.D09RouteProbeResult){
		"the absolute open read the target": open("absolute", "open", 0),
		"the traversal open read it":        open("traversal", "open", 0),
		"the symlink open read it":          open("symlink", "open", 0),
		"the listing of its directory":      open("absolute", "list", 0),
		"the target is absent":              open("absolute", "open", int(syscall.ENOENT)),
		"a failure with no errno":           open("absolute", "open", -1),
		"the control was denied":            func(r *piruntime.D09RouteProbeResult) { r.ControlRead.Errno = int(syscall.EACCES) },
	} {
		w := drBuild(t, mutate, nil, nil)
		d, kind, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
		if err != nil {
			continue // refused by the producer
		}
		if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), rtExpect()); err == nil {
			t.Errorf("%s: an artifact was accepted", name)
		}
	}
}

func TestDirectRuntimeProbeRefusesWhatIsNotTheProbeCell(t *testing.T) {
	cases := map[string]struct {
		probe func(*piruntime.D09RouteProbeResult)
		proof func(*piruntime.D09TCBProof)
		event func(*map[string]any)
		opts  func(*routeProbeOptions)
	}{
		"no probe result in the proof":         {proof: func(p *piruntime.D09TCBProof) { p.RouteProbe = nil }},
		"a probe of another target":            {probe: func(r *piruntime.D09RouteProbeResult) { r.Target = "/home/node/.bashrc" }},
		"another template":                     {proof: func(p *piruntime.D09TCBProof) { p.TemplateDigest = geDigest("other") }},
		"another image":                        {proof: func(p *piruntime.D09TCBProof) { p.ImageDigest = geDigest("other") }},
		"another ruleset":                      {proof: func(p *piruntime.D09TCBProof) { p.LandlockRulesetDigest = geDigest("other") }},
		"capabilities left":                    {proof: func(p *piruntime.D09TCBProof) { p.CapEff = "0000000000000001" }},
		"no seccomp":                           {proof: func(p *piruntime.D09TCBProof) { p.Seccomp = 0 }},
		"not PID 1":                            {proof: func(p *piruntime.D09TCBProof) { p.ContainerLeaderPID = 7 }},
		"consumer mounts unverified":           {proof: func(p *piruntime.D09TCBProof) { p.ConsumerMountsVerified = false }},
		"another profile in the event":         {event: func(e *map[string]any) { (*e)["profile_digest"] = geDigest("other") }},
		"another run in the event":             {event: func(e *map[string]any) { (*e)["run_id"] = "run-other" }},
		"a proof digest that is not the proof": {event: func(e *map[string]any) { (*e)["proof_digest"] = geDigest("other") }},
		"a control other than the probe's":     {opts: func(o *routeProbeOptions) { o.controlPath = "src/other.txt" }},
		"a target other than the probe's":      {opts: func(o *routeProbeOptions) { o.target = "/home/node/.bashrc" }},
		"another profile pin":                  {opts: func(o *routeProbeOptions) { o.profileHash = geDigest("other") }},
		"a target the image does not hold": {opts: func(o *routeProbeOptions) {
			o.targetRoot = filepath.Join(o.targetRoot, "nowhere")
		}},
		"no target root":         {opts: func(o *routeProbeOptions) { o.targetRoot = "" }},
		"no production template": {opts: func(o *routeProbeOptions) { o.productionTemplate = "" }},
		"a relative audit root":  {opts: func(o *routeProbeOptions) { o.auditRoot = "audit" }},
	}
	for name, c := range cases {
		w := drBuild(t, c.probe, c.proof, c.event)
		if c.opts != nil {
			c.opts(&w.opts)
		}
		if _, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDirectRuntimeProbeRefusesATemplatePairThatIsNotTheSameCell(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	// A production template that differs from the probe template in more than the probe member.
	other := w.productionTemplate
	other.MemoryMB++
	drWriteJSON(t, w.opts.productionTemplate, other)
	if _, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv()); err == nil {
		t.Error("a production template of a different cell was accepted")
	}
	// A production template that is itself a probe template.
	drWriteJSON(t, w.opts.productionTemplate, w.probeTemplate)
	if _, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv()); err == nil {
		t.Error("a probe template was accepted as the production template")
	}
	// A probe template with no probe.
	w = drBuild(t, nil, nil, nil)
	drWriteJSON(t, w.opts.probeTemplate, w.productionTemplate)
	if _, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv()); err == nil {
		t.Error("a template with no probe was accepted as the probe template")
	}
}

func TestDirectRuntimeProbeNeedsTheTargetToBeReadableAndListableFromOutside(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	if err := os.Chmod(filepath.Join(w.opts.targetRoot, drTarget), 0o600); err != nil {
		t.Fatal(err)
	}
	d, kind, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err == nil {
		if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), rtExpect()); err == nil {
			t.Error("a target only its owner can read gave a valid artifact")
		}
	}
}

func TestDirectRuntimeProbeRefusesACorruptedOrDuplicatedJournal(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	path := filepath.Join(w.dir, rtRunID+"-session-audit.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(raw, []byte(`"hash":"`), []byte(`"hash":"0`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv()); err == nil {
		t.Error("a journal whose chain does not verify was read")
	}
}

func TestDirectRuntimeParallelsTheProbeItReads(t *testing.T) {
	// The producer states operands by guestevidence's rule; the probe in the cell uses piruntime's.
	if strings.Join(guestevidence.RouteMethods, ",") != strings.Join(piruntime.D09RouteProbeMethods, ",") ||
		strings.Join(guestevidence.RouteDirectOps, ",") != strings.Join(piruntime.D09RouteProbeOps, ",") {
		t.Fatal("the producer and the probe name different methods or operations")
	}
	for _, method := range guestevidence.RouteMethods {
		for _, op := range guestevidence.RouteDirectOps {
			want, err := piruntime.D09RouteProbeOperand(method, op, rtTarget)
			got, gotErr := guestevidence.RouteDirectOperand(method, op, rtTarget)
			if err != nil || gotErr != nil || want != got {
				t.Errorf("%s %s: probe requests %q, artifact states %q", method, op, want, got)
			}
		}
	}
}

func TestGuestEvidenceDirectRuntimeCommandWritesOnlyValidArtifacts(t *testing.T) {
	run := func(w drWorld, out string) error {
		var stdout, stderr bytes.Buffer
		return runGuestEvidence([]string{"route-denied", "--commit", geCommit, "--route", "direct-runtime", "--run-id", rtRunID,
			"--audit-root", w.dir, "--target", drTarget, "--control-path", drControl, "--profile-hash", rtProfile, "--frozen-source-digest", rtSource,
			"--probe-template", w.opts.probeTemplate, "--production-template", w.opts.productionTemplate, "--target-root", w.opts.targetRoot,
			"--out", out}, &stdout, &stderr)
	}
	good := drBuild(t, nil, nil, nil)
	out := filepath.Join(t.TempDir(), "direct-runtime.json")
	if err := run(good, out); err != nil {
		t.Fatalf("a good probe run: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil || !bytes.Contains(data, []byte(guestevidence.KindRouteDirectRuntime)) {
		t.Fatalf("the artifact is not written: %v %s", err, data)
	}

	// One attempt got through: nothing is written.
	bad := drBuild(t, func(r *piruntime.D09RouteProbeResult) {
		for i := range r.Attempts {
			if r.Attempts[i].Method == "symlink" && r.Attempts[i].Op == "open" {
				r.Attempts[i].Errno = 0
			}
		}
	}, nil, nil)
	badOut := filepath.Join(t.TempDir(), "bad.json")
	err = run(bad, badOut)
	if err == nil || !strings.Contains(err.Error(), "symlink open") {
		t.Fatalf("a probe where the symlink open got through: %v", err)
	}
	if _, statErr := os.Stat(badOut); statErr == nil {
		t.Fatal("an artifact was written for a read that succeeded")
	}
}
