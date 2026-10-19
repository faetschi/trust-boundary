//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/cellview"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
)

const (
	drTarget  = "/home/node/.profile"
	drControl = "src/release.txt"
)

// drWorld is a complete direct-runtime probe run: the production and probe templates, the signed host profile
// of the probe run, the TCB proof the probe cell gave, the session journal that holds it and run.json.
type drWorld struct {
	dir                string
	probeTemplate      piruntime.D09CellTemplate
	productionTemplate piruntime.D09CellTemplate
	proof              piruntime.D09TCBProof
	opts               routeProbeOptions
	runConfig          string
	expect             guestevidence.Expect
}

// drOpts shapes a world. The zero value is a good probe run.
type drOpts struct {
	probe func(*piruntime.D09RouteProbeResult)
	proof func(*piruntime.D09TCBProof)
	event func(*map[string]any)
	// facts changes what the probe measures in the cell (the cell's own view of the path), acl what it reads as ACLs.
	facts func(map[string]cellview.Fact)
	acl   map[string]string
	// pin is the admin key digest run.json pins; empty pins the key that signed the probe profile.
	pin     string
	profile func(*piruntime.HostProfile) // changes the production profile, and with it the probe profile and the pins
	// probeProfile changes the probe profile only.
	probeProfile func(*piruntime.HostProfile)
	// template changes the production template (and with it the probe template) before the pins are computed from the pilot's
	// copy of the unchanged one.
	template func(*piruntime.D09CellTemplate)
}

func drTemplate(t *testing.T, imageDigest, ociLayout string) piruntime.D09CellTemplate {
	t.Helper()
	landlock, err := piruntime.DigestD09LandlockRules([]string{"/usr", "/etc"}, []string{"/work", "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	cellUID, cellGID := 100731, 100732
	mountOptions := piruntime.DigestBytes([]byte("ro,relatime"))
	return piruntime.D09CellTemplate{
		Image: "localhost/tbound/pi-cell:latest", ExpectedImageDigest: imageDigest,
		CosignPublicKeyPath: "/etc/tbound/cosign.pub", CosignPublicKeyDigest: geDigest("cosign-pub"),
		OCILayoutPath: ociLayout, SeccompProfilePath: "/etc/tbound/seccomp.json", SeccompProfileDigest: geDigest("seccomp"),
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

// drGoodFacts is what the probe measures in a cell whose permissions would allow the read: a world-readable
// file below searchable directories, none owned by the cell identity (100731:100732).
func drGoodFacts() map[string]cellview.Fact {
	d := func(p string, uid uint32) cellview.Fact {
		return cellview.Fact{Path: p, Kind: cellview.KindDir, Mode: 0o755, UID: uid, GID: uid}
	}
	return map[string]cellview.Fact{"/": d("/", 0), "/home": d("/home", 0), "/home/node": d("/home/node", 1000),
		drTarget: {Path: drTarget, Kind: cellview.KindFile, Mode: 0o644, UID: 1000, GID: 1000, Size: 807}}
}

// drDeniedProbe is the result a cell that denies the target reports.
func drDeniedProbe(p piruntime.D09RouteProbe, fs deniedFS, mutate func(*piruntime.D09RouteProbeResult)) *piruntime.D09RouteProbeResult {
	result := piruntime.RunD09RouteProbe(fs, p)
	if mutate != nil {
		mutate(&result)
	}
	return &result
}

// deniedFS refuses every open and listing and shows the metadata of every stat, as the cell does. It measures
// the path from facts, with the cell identity of drTemplate.
type deniedFS struct {
	facts map[string]cellview.Fact
	acl   map[string]string
}

func (deniedFS) OpenRead(p string) error {
	if strings.HasPrefix(p, "/work/") {
		return nil
	}
	return syscall.EACCES
}
func (deniedFS) Stat(string) error { return nil }
func (deniedFS) List(string) error { return syscall.EACCES }
func (f deniedFS) Lstat(p string) (cellview.Fact, error) {
	fact, ok := f.facts[p]
	if !ok {
		return cellview.Fact{}, syscall.ENOENT
	}
	return fact, nil
}
func (f deniedFS) ACL(p string) string                { return f.acl[p] }
func (deniedFS) Identity() (uint32, uint32, []uint32) { return 100731, 100732, nil }
func (deniedFS) LSM() string                          { return "unconfined" }
func (deniedFS) Overflow() (uint32, uint32, bool)     { return 65534, 65534, true }

// drSignProfile signs the host profile with a fresh key, writes the three files and returns the digest the
// service journals for it.
func drSignProfile(t *testing.T, dir, name string, profile piruntime.HostProfile) (profilePath, sigPath, keyPath, digest string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profileJSON, sig, pubKey, err := piruntime.SignLinuxHostProfile(profile, priv)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := piruntime.VerifyLinuxSignedHostProfile(profileJSON, sig, pub)
	if err != nil {
		t.Fatal(err)
	}
	profilePath, sigPath, keyPath = filepath.Join(dir, name+".json"), filepath.Join(dir, name+".ed25519"), filepath.Join(dir, name+".pub")
	for p, data := range map[string][]byte{profilePath: profileJSON, sigPath: sig, keyPath: pubKey} {
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return profilePath, sigPath, keyPath, signed.Digest
}

// drBuild writes the world. mutateProof changes the proof before it is journaled; mutateEvent changes the
// journal event.
// drFS is the cell's file system as the probe measures it in a world.
func drFS(o drOpts) deniedFS {
	facts := drGoodFacts()
	if o.facts != nil {
		o.facts(facts)
	}
	return deniedFS{facts: facts, acl: o.acl}
}

func drBuild(t *testing.T, mutateProbe func(*piruntime.D09RouteProbeResult), mutateProof func(*piruntime.D09TCBProof), mutateEvent func(*map[string]any)) drWorld {
	return drBuildWith(t, drOpts{probe: mutateProbe, proof: mutateProof, event: mutateEvent})
}

func drBuildWith(t *testing.T, o drOpts) drWorld {
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
	pilot := drTemplate(t, geDigest("image"), "/opt/tbound/cell/pi-cell-oci")
	w := drWorld{dir: dir, productionTemplate: drTemplate(t, geDigest("image"), "/opt/tbound/cell/pi-cell-oci")}
	if o.template != nil {
		o.template(&w.productionTemplate)
	}
	w.probeTemplate = w.productionTemplate
	probe := piruntime.D09RouteProbe{Name: piruntime.D09RouteProbeName, Target: drTarget, Control: "/work/" + drControl}
	w.probeTemplate.RouteProbe = &probe
	probeDigest, err := w.probeTemplate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// The pilot's template is this cell with the registration of the pilot's config: the probe run has a registration of its
	// own, and the two share only the base template.
	pilot.ManagedLiveRootRegistrationDigest = geDigest("registration of the pilot config")
	base, err := baseTemplateDigest(pilot)
	if err != nil {
		t.Fatal(err)
	}

	// The signed profile of the probe run names the probe template.
	production := piruntime.DefaultLinuxHostProfileTemplate()
	production.SourceProvenanceDigest = rtSource
	if o.profile != nil {
		o.profile(&production)
	}
	probeProfile := production
	probeProfile.ContainmentProfileDigest = probeDigest
	if o.probeProfile != nil {
		o.probeProfile(&probeProfile)
	}
	hostProfile, hostSig, hostKey, probeProfileDigest := drSignProfile(t, dir, "probe-profile", probeProfile)
	keyBytes, err := os.ReadFile(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	adminPin := piruntime.DigestBytes(keyBytes)
	if o.pin != "" {
		adminPin = o.pin
	}

	w.proof = piruntime.D09TCBProof{Schema: "tbound-pi-entrypoint/v1", UID: w.probeTemplate.CellUID, GID: w.probeTemplate.CellGID, CapEff: "0000000000000000",
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: w.probeTemplate.LandlockRulesetDigest, LandlockTSYNC: true, CgroupV2: true,
		ContainerName: "tbound-d09-cell-abc", LeaderPID: 4242, ContainerLeaderPID: 1, LauncherProcessPID: 4000,
		ImageRef: "localhost/tbound/pi-cell@" + w.probeTemplate.ExpectedImageDigest, ImageDigest: w.probeTemplate.ExpectedImageDigest, TemplateDigest: probeDigest,
		ConsumerMountsVerified: true, RawSourceFDsClosed: true, WorkerMountID: 11, RuntimeMountID: 12, WorkerDevice: 1, WorkerInode: 2, RuntimeDevice: 3, RuntimeInode: 4,
		NoExternalRoutes: true, RouteProbe: drDeniedProbe(probe, drFS(o), o.probe)}
	if o.proof != nil {
		o.proof(&w.proof)
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
		"conversation_id": "c", "source_generation_id": "g0", "source_binding_digest": geDigest("binding"), "profile_id": probeProfile.ID, "profile_digest": probeProfileDigest,
		"proof_digest": piruntime.DigestBytes(encodedProof), "proof": json.RawMessage(encodedProof)}
	if o.event != nil {
		o.event(&event)
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

	probeTemplatePath, productionTemplatePath := filepath.Join(dir, "probe-template.json"), filepath.Join(dir, "production-template.json")
	drWriteJSON(t, probeTemplatePath, w.probeTemplate)
	drWriteJSON(t, productionTemplatePath, w.productionTemplate)
	w.runConfig = filepath.Join(dir, "run.json")
	drWriteJSON(t, w.runConfig, map[string]any{"freeze_commit": geCommit, "v_image_digest": w.productionTemplate.ExpectedImageDigest,
		"pins": map[string]string{"profile_hash": geDigest("pilot profile"), "containment_profile_digest": geDigest("pilot template"), "base_template_digest": base,
			"frozen_source_digest": rtSource, "admin_pubkey_digest": adminPin}, "other": 1})
	w.opts = routeProbeOptions{route: "direct-runtime", runID: rtRunID, auditRoot: dir, target: drTarget, controlPath: drControl,
		pins:          guestevidence.RunBinding{Commit: geCommit, FrozenSourceDigest: rtSource, BaseTemplateDigest: base, ImageDigest: w.productionTemplate.ExpectedImageDigest, AdminPubkeyDigest: adminPin},
		probeTemplate: probeTemplatePath, productionTemplate: productionTemplatePath,
		hostProfile: hostProfile, hostProfileSig: hostSig, hostProfileKey: hostKey}
	w.expect = guestevidence.Expect{Commit: geCommit, BinarySHA256: geDigest("self"), FrozenSourceDigest: rtSource, BaseTemplateDigest: base,
		ImageDigest: w.productionTemplate.ExpectedImageDigest, AdminPubkeyDigest: adminPin}
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
	if d.View == nil || len(d.View.Facts) != 4 || d.Binding.AdminPubkeyDigest != w.opts.pins.AdminPubkeyDigest {
		t.Errorf("view = %+v admin key %s", d.View, d.Binding.AdminPubkeyDigest)
	}
	if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), w.expect); err != nil {
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
		if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), w.expect); err == nil {
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
		"another source pin":                   {opts: func(o *routeProbeOptions) { o.pins.FrozenSourceDigest = geDigest("another source") }},
		"another base template pin":            {opts: func(o *routeProbeOptions) { o.pins.BaseTemplateDigest = geDigest("another base template") }},
		"another image pin":                    {opts: func(o *routeProbeOptions) { o.pins.ImageDigest = geDigest("another image") }},
		"no admin key pin":                     {opts: func(o *routeProbeOptions) { o.pins.AdminPubkeyDigest = "" }},
		"another admin key pin":                {opts: func(o *routeProbeOptions) { o.pins.AdminPubkeyDigest = geDigest("another admin key") }},
		"no host profile":                      {opts: func(o *routeProbeOptions) { o.hostProfile = "" }},
		"a profile signature of nothing":       {opts: func(o *routeProbeOptions) { o.hostProfileSig = o.hostProfileKey }},
		"no production template":               {opts: func(o *routeProbeOptions) { o.productionTemplate = "" }},
		"a relative audit root":                {opts: func(o *routeProbeOptions) { o.auditRoot = "audit" }},
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

// drRefusal runs the producer and the validator over the world: nil means a valid artifact came out.
func drRefusal(t *testing.T, w drWorld) error {
	t.Helper()
	d, kind, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err == nil {
		err = guestevidence.Validate(kind, mustEncodeGE(t, kind, d), w.expect)
	}
	if err != nil {
		t.Logf("refused: %v", err)
	}
	return err
}

// ---- the evidence is what the cell measured, for the cell identity ----

func TestDirectRuntimeProbeRecordsWhatTheCellMeasured(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	d, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err != nil {
		t.Fatal(err)
	}
	v := d.View
	if v == nil || v.Err != "" || v.EUID != uint32(w.probeTemplate.CellUID) || v.EGID != uint32(w.probeTemplate.CellGID) || v.LSM != "unconfined" ||
		d.Cell.CellUID != uint32(w.probeTemplate.CellUID) || d.Cell.CellGID != uint32(w.probeTemplate.CellGID) {
		t.Fatalf("view = %+v cell = %+v", v, d.Cell)
	}
	var paths []string
	for _, f := range v.Facts {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, " ") != "/ /home /home/node /home/node/.profile" {
		t.Errorf("components = %v", paths)
	}
	if !slices.Equal(d.Limits, guestevidence.DirectRuntimeLimits) {
		t.Errorf("limits = %v", d.Limits)
	}
}

// The cell sees its own mounts: a file the image holds and a mount replaces is judged by what the mount shows.
func TestDirectRuntimeProbeJudgesWhatTheCellSeesAndNotTheImage(t *testing.T) {
	file := cellview.Fact{Path: drTarget, Kind: cellview.KindFile, Mode: 0o644, UID: 1000, GID: 1000, Size: 807}
	dirFact := func(p string, mode, uid uint32) cellview.Fact {
		return cellview.Fact{Path: p, Kind: cellview.KindDir, Mode: mode, UID: uid, GID: uid}
	}
	cell := uint32(100731)
	for name, c := range map[string]struct {
		facts func(map[string]cellview.Fact)
		acl   map[string]string
	}{
		"the workspace mount replaces /home/node with a link into /work": {facts: func(m map[string]cellview.Fact) {
			m["/home/node"] = cellview.Fact{Path: "/home/node", Kind: cellview.KindLink, Mode: 0o777}
			m["/work"] = dirFact("/work", 0o755, 0)
			m["/work/private"] = dirFact("/work/private", 0o755, 0)
			m["/work/private/.profile"] = cellview.Fact{Path: "/work/private/.profile", Kind: cellview.KindFile, Mode: 0o644, Size: 807}
		}},
		"a mount closes the home directory": {facts: func(m map[string]cellview.Fact) { m["/home/node"] = dirFact("/home/node", 0o700, 0) }},
		"the file is owner-only":            {facts: func(m map[string]cellview.Fact) { f := file; f.Mode = 0o600; m[drTarget] = f }},
		"the file is the cell's own and its owner class has no read": {facts: func(m map[string]cellview.Fact) {
			f := file
			f.UID, f.Mode = cell, 0o044
			m[drTarget] = f
		}},
		"the file is empty":                              {facts: func(m map[string]cellview.Fact) { f := file; f.Size = 0; m[drTarget] = f }},
		"the file is a socket":                           {facts: func(m map[string]cellview.Fact) { f := file; f.Kind = cellview.KindOther; m[drTarget] = f }},
		"the root is closed":                             {facts: func(m map[string]cellview.Fact) { m["/"] = dirFact("/", 0o700, 0) }},
		"an ancestor is closed":                          {facts: func(m map[string]cellview.Fact) { m["/home"] = dirFact("/home", 0o700, 0) }},
		"the parent has no read":                         {facts: func(m map[string]cellview.Fact) { m["/home/node"] = dirFact("/home/node", 0o711, 1000) }},
		"the cell owns an ancestor with no owner search": {facts: func(m map[string]cellview.Fact) { m["/home"] = dirFact("/home", 0o655, cell) }},
		"a component is missing in the cell":             {facts: func(m map[string]cellview.Fact) { delete(m, "/home/node") }},
		"an ACL on the file":                             {acl: map[string]string{drTarget: "access"}},
		"a default ACL on the parent":                    {acl: map[string]string{"/home/node": "default"}},
		"an ACL that could not be read":                  {acl: map[string]string{"/home": cellview.ACLUnreadable}},
	} {
		w := drBuildWith(t, drOpts{facts: c.facts, acl: c.acl})
		if err := drRefusal(t, w); err == nil {
			t.Errorf("%s: a valid artifact came out", name)
		}
	}
}

// A symbolic link on any component of the walked name is never a permission baseline: the kernel may refuse
// to follow it (fs.protected_symlinks) for a reason the modes do not show.
func TestDirectRuntimeProbeRefusesAnySymbolicLinkOnTheWalk(t *testing.T) {
	linkAt := func(p string) func(map[string]cellview.Fact) {
		return func(m map[string]cellview.Fact) {
			m[p] = cellview.Fact{Path: p, Kind: cellview.KindLink, Mode: 0o777, UID: 1000, GID: 1000}
		}
	}
	for name, facts := range map[string]func(map[string]cellview.Fact){
		"the final name is a link":     linkAt(drTarget),
		"the home directory is a link": linkAt("/home/node"),
		"the root is a link":           linkAt("/"),
	} {
		err := drRefusal(t, drBuildWith(t, drOpts{facts: facts}))
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := drRefusal(t, drBuildWith(t, drOpts{})); err != nil {
		t.Errorf("the path without a link: %v", err)
	}
}

// ---- the image baseline is gone ----

func TestDirectRuntimeProducerHoldsNoImageBaseline(t *testing.T) {
	if _, err := os.Stat("guest_route_image_linux.go"); err == nil {
		t.Error("guest_route_image_linux.go exists: the producer applies image layers on the host")
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range []string{"imageBaseline", "applyLayer", "applyEntry", ".wh.", "wh..opq", "archive/tar", "ociLayout", "oci-layout"} {
			if strings.Contains(string(src), token) {
				t.Errorf("%s contains %q: the host must not emulate the image", e.Name(), token)
			}
		}
	}
}

// ---- the admin key is the one the run pins ----

func TestDirectRuntimeProbeRefusesAHostProfileKeyThatTheRunDoesNotPin(t *testing.T) {
	// The run pins one admin key; the profile, its signature and the journal are all consistent under
	// another key. Before the pin the producer verified under whatever --host-profile-key said.
	w := drBuildWith(t, drOpts{pin: geDigest("the admin key of the run")})
	_, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err == nil || !strings.Contains(err.Error(), "admin_pubkey_digest") {
		t.Fatalf("a profile signed under an unpinned key: %v", err)
	}
	// The same world under the pin of its own key is a valid artifact.
	if err := drRefusal(t, drBuild(t, nil, nil, nil)); err != nil {
		t.Fatalf("the pinned key is refused: %v", err)
	}
	// A key file of the right size that is not the pinned key, over the pinned world.
	w = drBuild(t, nil, nil, nil)
	other, _, err2 := ed25519.GenerateKey(rand.Reader)
	if err2 != nil {
		t.Fatal(err2)
	}
	if err := os.WriteFile(w.opts.hostProfileKey, other, 0o600); err != nil {
		t.Fatal(err)
	}
	if drProbeFails(t, w) == nil {
		t.Error("a caller-supplied key replaced the pinned admin key")
	}
	// The artifact states the key digest; one packaged under another pin is refused by the validator.
	w = drBuild(t, nil, nil, nil)
	d, kind, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err != nil {
		t.Fatal(err)
	}
	if d.Binding.AdminPubkeyDigest != w.opts.pins.AdminPubkeyDigest {
		t.Errorf("the artifact states key %s, the pin is %s", d.Binding.AdminPubkeyDigest, w.opts.pins.AdminPubkeyDigest)
	}
	e := w.expect
	e.AdminPubkeyDigest = geDigest("another admin key")
	if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), e); err == nil {
		t.Error("an artifact verified under one key validates under the pin of another")
	}
}

// ---- the artifact is bound to what the audited journal records, not to what the caller says ----

func TestDirectRuntimeProbeRefusesASourceDigestOtherThanTheJournalRecords(t *testing.T) {
	// An unchanged journal and profile packaged under another source digest.
	w := drBuild(t, nil, nil, nil)
	w.opts.pins.FrozenSourceDigest = geDigest("another source")
	if drProbeFails(t, w) == nil {
		t.Error("a journal of one source was packaged under another source digest")
	}
	// The profile of the journal holds another source than the run pins, though its own digest is as pinned.
	w = drBuildWith(t, drOpts{profile: func(p *piruntime.HostProfile) { p.SourceProvenanceDigest = geDigest("another source") }})
	if drProbeFails(t, w) == nil {
		t.Error("a profile of another source was accepted under the run's source pin")
	}
}

func TestDirectRuntimeProbeRefusesAProfileThatIsNotTheJournaledOne(t *testing.T) {
	// A signed profile that is not the one the service journaled.
	w := drBuild(t, nil, nil, nil)
	w2 := drBuildWith(t, drOpts{probeProfile: func(p *piruntime.HostProfile) { p.ID = "another-profile" }})
	w.opts.hostProfile, w.opts.hostProfileSig, w.opts.hostProfileKey = w2.opts.hostProfile, w2.opts.hostProfileSig, w2.opts.hostProfileKey
	if drProbeFails(t, w) == nil {
		t.Error("a signed profile other than the journaled one was accepted")
	}
	// A profile whose signature does not verify.
	w = drBuild(t, nil, nil, nil)
	sig, err := os.ReadFile(w.opts.hostProfileSig)
	if err != nil {
		t.Fatal(err)
	}
	sig[0] ^= 0xff
	if err := os.WriteFile(w.opts.hostProfileSig, sig, 0o600); err != nil {
		t.Fatal(err)
	}
	if drProbeFails(t, w) == nil {
		t.Error("a profile with a bad signature was accepted")
	}
}

func TestDirectRuntimeArtifactRepackagedUnderOtherPinsIsRefused(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	d, kind, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err != nil {
		t.Fatal(err)
	}
	data := mustEncodeGE(t, kind, d)
	if err := guestevidence.Validate(kind, data, w.expect); err != nil {
		t.Fatalf("the artifact is refused: %v", err)
	}
	for name, mutate := range map[string]func(*guestevidence.Expect){
		"another commit":        func(e *guestevidence.Expect) { e.Commit = strings.Repeat("1", 40) },
		"another source digest": func(e *guestevidence.Expect) { e.FrozenSourceDigest = geDigest("another source") },
		"another base template": func(e *guestevidence.Expect) { e.BaseTemplateDigest = geDigest("another base template") },
		"another image":         func(e *guestevidence.Expect) { e.ImageDigest = geDigest("another image") },
		"another admin key":     func(e *guestevidence.Expect) { e.AdminPubkeyDigest = geDigest("another admin key") },
		"no admin key pin":      func(e *guestevidence.Expect) { e.AdminPubkeyDigest = "" },
		"no base template pin":  func(e *guestevidence.Expect) { e.BaseTemplateDigest = "" },
	} {
		e := w.expect
		mutate(&e)
		if err := guestevidence.Validate(kind, data, e); err == nil {
			t.Errorf("the artifact validates under %s", name)
		}
	}
	// Editing the artifact's own claim of the source does not help: the journal's profile source is stated too.
	d.FrozenSourceDigest = geDigest("another source")
	e := w.expect
	e.FrozenSourceDigest = d.FrozenSourceDigest
	if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), e); err == nil {
		t.Error("an artifact re-pinned to another source, with the journal's profile source unchanged, validates")
	}
}

func TestRunBindingPinsComeFromTheRunConfigThatNamesTheCommit(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	b, err := runBindingPins(w.runConfig, geCommit)
	if err != nil || b != w.opts.pins {
		t.Fatalf("pins = %+v %v, want %+v", b, err, w.opts.pins)
	}
	for name, args := range map[string][2]string{
		"no run config":             {"", geCommit},
		"another commit":            {w.runConfig, strings.Repeat("2", 40)},
		"a run config that is gone": {filepath.Join(w.dir, "missing.json"), geCommit},
	} {
		if _, err := runBindingPins(args[0], args[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// run.json without one of the pins the artifact is bound to: the value would be whatever the caller passes.
	pins := func(drop string) map[string]any {
		p := map[string]string{"base_template_digest": w.opts.pins.BaseTemplateDigest, "frozen_source_digest": rtSource, "admin_pubkey_digest": w.opts.pins.AdminPubkeyDigest}
		doc := map[string]any{"freeze_commit": geCommit, "v_image_digest": w.opts.pins.ImageDigest, "pins": p}
		if drop == "v_image_digest" {
			delete(doc, drop)
		}
		delete(p, drop)
		return doc
	}
	for _, drop := range []string{"base_template_digest", "frozen_source_digest", "admin_pubkey_digest", "v_image_digest"} {
		drWriteJSON(t, w.runConfig, pins(drop))
		if _, err := runBindingPins(w.runConfig, geCommit); err == nil {
			t.Errorf("a run config with no %s was accepted", drop)
		}
	}
}

// The probe run has a config and so a registration of its own: its production template is not the pilot's, and
// what it shares with the pilot is the base template. A probe over another cell is refused.
func TestDirectRuntimeProbeRunsUnderItsOwnRegistrationNotThePilots(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	pilot := w.productionTemplate
	pilot.ManagedLiveRootRegistrationDigest = geDigest("registration of the pilot config")
	pilotDigest, _ := pilot.Digest()
	ownDigest, _ := w.productionTemplate.Digest()
	if pilotDigest == ownDigest {
		t.Fatal("the world's registration is the pilot's")
	}
	d, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err != nil {
		t.Fatalf("a probe under its own registration is refused: %v", err)
	}
	if d.Binding.RegistrationDigest != w.probeTemplate.ManagedLiveRootRegistrationDigest || d.Binding.TemplateDigest != d.Cell.TemplateDigest {
		t.Errorf("binding = %+v", d.Binding)
	}
	other := drBuildWith(t, drOpts{template: func(c *piruntime.D09CellTemplate) { c.MemoryMB++ }})
	if _, _, err := probeRouteDenied(other.opts, productionRouteProbeEnv()); err == nil || !strings.Contains(err.Error(), "base_template_digest") {
		t.Errorf("a probe in a cell that is not the pilot's base cell was accepted: %v", err)
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
			"--audit-root", w.dir, "--target", drTarget, "--control-path", drControl, "--run-config", w.runConfig,
			"--probe-template", w.opts.probeTemplate, "--production-template", w.opts.productionTemplate, "--host-profile", w.opts.hostProfile,
			"--host-profile-signature", w.opts.hostProfileSig, "--host-profile-key", w.opts.hostProfileKey,
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

// drProbeFails runs the producer alone and returns its refusal, logged so that a test shows why.
func drProbeFails(t *testing.T, w drWorld) error {
	t.Helper()
	_, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err != nil {
		t.Logf("refused: %v", err)
	}
	return err
}
