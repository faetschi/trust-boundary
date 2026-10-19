//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

// drWorld is a complete direct-runtime probe run: the production and probe templates, the signed host profile
// of the probe run, the TCB proof the probe cell gave, the session journal that holds it, run.json and the
// OCI layout of the probed image.
type drWorld struct {
	dir                string
	probeTemplate      piruntime.D09CellTemplate
	productionTemplate piruntime.D09CellTemplate
	proof              piruntime.D09TCBProof
	opts               routeProbeOptions
	runConfig          string
	imageDigest        string
	expect             guestevidence.Expect
}

// drOpts shapes a world. The zero value is a good probe run.
type drOpts struct {
	probe   func(*piruntime.D09RouteProbeResult)
	proof   func(*piruntime.D09TCBProof)
	event   func(*map[string]any)
	image   [][]drEntry                  // layers of the probed image; nil is drGoodImage
	profile func(*piruntime.HostProfile) // changes the production profile, and with it the probe profile and the pins
	// probeProfile changes the probe profile only.
	probeProfile func(*piruntime.HostProfile)
}

// drEntry is one tar entry of a layer.
type drEntry struct {
	name     string
	typ      byte
	mode     int64
	uid, gid int
	body     string
	link     string
}

func drDir(name string, mode int64, uid int) drEntry {
	return drEntry{name: name, typ: tar.TypeDir, mode: mode, uid: uid, gid: uid}
}

func drFile(name string, mode int64, uid int, body string) drEntry {
	return drEntry{name: name, typ: tar.TypeReg, mode: mode, uid: uid, gid: uid, body: body}
}

func drSymlink(name, link string) drEntry {
	return drEntry{name: name, typ: tar.TypeSymlink, mode: 0o777, link: link}
}

// drGoodImage is an image in which the target is world-readable below searchable directories.
func drGoodImage() [][]drEntry {
	return [][]drEntry{{drDir("./", 0o755, 0), drDir("home/", 0o755, 0), drDir("home/node/", 0o755, 1000),
		drFile("home/node/.profile", 0o644, 1000, "export PATH=$HOME/bin:$PATH\n")}}
}

// drLayout writes an OCI layout into layout holding one image with the given layers (the first layer
// gzip-compressed, the others plain) and returns the manifest digest.
func drLayout(t *testing.T, layout string, layers [][]drEntry) string {
	t.Helper()
	blobs := filepath.Join(layout, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(data []byte) string {
		sum := sha256.Sum256(data)
		name := hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(blobs, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		return "sha256:" + name
	}
	var descriptors []map[string]any
	for i, entries := range layers {
		var raw bytes.Buffer
		tw := tar.NewWriter(&raw)
		for _, e := range entries {
			hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Uid: e.uid, Gid: e.uid, Linkname: e.link}
			if e.typ == tar.TypeReg {
				hdr.Size = int64(len(e.body))
			}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatal(err)
			}
			if e.typ == tar.TypeReg {
				if _, err := tw.Write([]byte(e.body)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		data, media := raw.Bytes(), "application/vnd.oci.image.layer.v1.tar"
		if i == 0 {
			var zipped bytes.Buffer
			zw := gzip.NewWriter(&zipped)
			if _, err := zw.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			data, media = zipped.Bytes(), "application/vnd.oci.image.layer.v1.tar+gzip"
		}
		descriptors = append(descriptors, map[string]any{"mediaType": media, "digest": put(data), "size": len(data)})
	}
	config := put([]byte(`{"architecture":"amd64","os":"linux"}`))
	manifest, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": config, "size": 36}, "layers": descriptors})
	if err != nil {
		t.Fatal(err)
	}
	digest := put(manifest)
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []map[string]any{{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": digest, "size": len(manifest)}}})
	if err := os.WriteFile(filepath.Join(layout, "index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	return digest
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
	layers := o.image
	if layers == nil {
		layers = drGoodImage()
	}
	layout := filepath.Join(dir, "oci")
	imageDigest := drLayout(t, layout, layers)
	w := drWorld{dir: dir, imageDigest: imageDigest, productionTemplate: drTemplate(t, imageDigest, layout)}
	w.probeTemplate = w.productionTemplate
	probe := piruntime.D09RouteProbe{Name: piruntime.D09RouteProbeName, Target: drTarget, Control: "/work/" + drControl}
	w.probeTemplate.RouteProbe = &probe
	probeDigest, err := w.probeTemplate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	productionDigest, err := w.productionTemplate.Digest()
	if err != nil {
		t.Fatal(err)
	}

	// The signed profile of the probe run is the production profile with the probe template's digest.
	production := piruntime.DefaultLinuxHostProfileTemplate()
	production.ContainmentProfileDigest = productionDigest
	production.SourceProvenanceDigest = rtSource
	if o.profile != nil {
		o.profile(&production)
	}
	_, _, _, profileHash := drSignProfile(t, t.TempDir(), "production", production)
	probeProfile := production
	probeProfile.ContainmentProfileDigest = probeDigest
	if o.probeProfile != nil {
		o.probeProfile(&probeProfile)
	}
	hostProfile, hostSig, hostKey, probeProfileDigest := drSignProfile(t, dir, "probe-profile", probeProfile)

	w.proof = piruntime.D09TCBProof{Schema: "tbound-pi-entrypoint/v1", UID: w.probeTemplate.CellUID, GID: w.probeTemplate.CellGID, CapEff: "0000000000000000",
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: w.probeTemplate.LandlockRulesetDigest, LandlockTSYNC: true, CgroupV2: true,
		ContainerName: "tbound-d09-cell-abc", LeaderPID: 4242, ContainerLeaderPID: 1, LauncherProcessPID: 4000,
		ImageRef: "localhost/tbound/pi-cell@" + w.probeTemplate.ExpectedImageDigest, ImageDigest: w.probeTemplate.ExpectedImageDigest, TemplateDigest: probeDigest,
		ConsumerMountsVerified: true, RawSourceFDsClosed: true, WorkerMountID: 11, RuntimeMountID: 12, WorkerDevice: 1, WorkerInode: 2, RuntimeDevice: 3, RuntimeInode: 4,
		NoExternalRoutes: true, RouteProbe: drDeniedProbe(probe, o.probe)}
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
	drWriteJSON(t, w.runConfig, map[string]any{"freeze_commit": geCommit, "pins": map[string]string{"profile_hash": profileHash,
		"containment_profile_digest": productionDigest, "frozen_source_digest": rtSource}, "other": 1})
	w.opts = routeProbeOptions{route: "direct-runtime", runID: rtRunID, auditRoot: dir, target: drTarget, controlPath: drControl,
		profileHash: profileHash, frozenSource: rtSource, containmentDigest: productionDigest,
		probeTemplate: probeTemplatePath, productionTemplate: productionTemplatePath,
		hostProfile: hostProfile, hostProfileSig: hostSig, hostProfileKey: hostKey}
	w.expect = guestevidence.Expect{Commit: geCommit, BinarySHA256: geDigest("self"), ProfileHash: profileHash, FrozenSourceDigest: rtSource, ContainmentProfileDigest: productionDigest}
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
		"another profile pin":                  {opts: func(o *routeProbeOptions) { o.profileHash = geDigest("other") }},
		"another source pin":                   {opts: func(o *routeProbeOptions) { o.frozenSource = geDigest("another source") }},
		"another production template pin":      {opts: func(o *routeProbeOptions) { o.containmentDigest = geDigest("another production template") }},
		"a layout that is not there":           {opts: func(o *routeProbeOptions) { o.ociLayout = filepath.Join(o.auditRoot, "nowhere") }},
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

// ---- the baseline is read from the probed image, for the cell identity ----

func TestDirectRuntimeProbeRecordsTheBaselineOfTheProbedImage(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	d, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err != nil {
		t.Fatal(err)
	}
	f := d.TargetFact
	if f == nil || f.ImageDigest != w.imageDigest || f.ImageDigest != d.Cell.ImageDigest || f.ResolvedPath != drTarget || f.UID != 1000 || f.Mode != 0o644 ||
		f.ParentMode != 0o755 || f.CellUID != uint32(w.probeTemplate.CellUID) || f.CellGID != uint32(w.probeTemplate.CellGID) || f.Size != int64(len("export PATH=$HOME/bin:$PATH\n")) {
		t.Fatalf("baseline = %+v", f)
	}
	var paths []string
	for _, a := range f.Ancestors {
		paths = append(paths, a.Path)
	}
	if strings.Join(paths, " ") != "/ /home /home/node" {
		t.Errorf("ancestors = %v", paths)
	}
}

func TestDirectRuntimeProbeNeedsTheTargetToBeReadableByTheCellIdentity(t *testing.T) {
	cell := 100731 // the cell uid of drTemplate
	for name, file := range map[string]drEntry{
		"only its owner can read it":                                drFile("home/node/.profile", 0o600, 1000, "x\n"),
		"it is the cell's own file and the owner class has no read": drFile("home/node/.profile", 0o044, cell, "x\n"),
		"it is empty":                drFile("home/node/.profile", 0o644, 1000, ""),
		"it is a directory":          drDir("home/node/.profile", 0o755, 1000),
		"it is a symlink to nothing": drSymlink("home/node/.profile", "/nowhere"),
	} {
		w := drBuildWith(t, drOpts{image: [][]drEntry{{drDir("./", 0o755, 0), drDir("home/", 0o755, 0), drDir("home/node/", 0o755, 1000), file}}})
		if err := drRefusal(t, w); err == nil {
			t.Errorf("%s: a valid artifact came out", name)
		}
	}
}

func TestDirectRuntimeProbeNeedsEveryAncestorToBeSearchableByTheCellIdentity(t *testing.T) {
	cell := 100731
	file := drFile("home/node/.profile", 0o644, 1000, "export X=1\n")
	for name, dirs := range map[string][]drEntry{
		"the image root":                                {drDir("./", 0o700, 0), drDir("home/", 0o755, 0), drDir("home/node/", 0o755, 1000)},
		"an ancestor of the parent":                     {drDir("./", 0o755, 0), drDir("home/", 0o700, 0), drDir("home/node/", 0o755, 1000)},
		"the parent":                                    {drDir("./", 0o755, 0), drDir("home/", 0o755, 0), drDir("home/node/", 0o750, 1000)},
		"the parent cannot be listed":                   {drDir("./", 0o755, 0), drDir("home/", 0o755, 0), drDir("home/node/", 0o711, 1000)},
		"the cell's own directory with no owner search": {drDir("./", 0o755, 0), drDir("home/", 0o655, cell), drDir("home/node/", 0o755, 1000)},
		"an ancestor with no entry in the image":        {drDir("./", 0o755, 0), drDir("home/node/", 0o755, 1000)},
	} {
		w := drBuildWith(t, drOpts{image: [][]drEntry{append(dirs, file)}})
		if err := drRefusal(t, w); err == nil {
			t.Errorf("%s cannot be searched or has no entry, and a valid artifact came out", name)
		}
	}
}

func TestDirectRuntimeProbeFollowsSymlinksOfTheImage(t *testing.T) {
	file := drFile("srv/home/node/.profile", 0o644, 1000, "export X=1\n")
	open := []drEntry{drDir("./", 0o755, 0), drDir("srv/", 0o755, 0), drDir("srv/home/", 0o755, 0), drDir("srv/home/node/", 0o755, 1000), file, drSymlink("home", "srv/home")}
	w := drBuildWith(t, drOpts{image: [][]drEntry{open}})
	d, _, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err != nil {
		t.Fatalf("a target below a symlinked directory: %v", err)
	}
	if d.TargetFact.ResolvedPath != "/srv/home/node/.profile" {
		t.Errorf("resolved to %q", d.TargetFact.ResolvedPath)
	}
	if err := guestevidence.Validate(guestevidence.KindRouteDirectRuntime, mustEncodeGE(t, guestevidence.KindRouteDirectRuntime, d), w.expect); err != nil {
		t.Errorf("the artifact is refused: %v", err)
	}
	// The directories the link leads through count as ancestors: one that cannot be searched refuses the artifact.
	closed := []drEntry{drDir("./", 0o755, 0), drDir("srv/", 0o700, 0), drDir("srv/home/", 0o755, 0), drDir("srv/home/node/", 0o755, 1000), file, drSymlink("home", "srv/home")}
	if err := drRefusal(t, drBuildWith(t, drOpts{image: [][]drEntry{closed}})); err == nil {
		t.Error("a target below a closed directory that a symlink leads through gave a valid artifact")
	}
	loop := []drEntry{drDir("./", 0o755, 0), drDir("home/", 0o755, 0), drSymlink("home/node", "/home/node")}
	if err := drRefusal(t, drBuildWith(t, drOpts{image: [][]drEntry{loop}})); err == nil {
		t.Error("a symlink loop gave a valid artifact")
	}
}

func TestDirectRuntimeProbeAppliesTheLayersInOrder(t *testing.T) {
	base := drGoodImage()[0]
	for name, upper := range map[string][]drEntry{
		"a layer narrows the file":         {drFile("home/node/.profile", 0o600, 1000, "export X=1\n")},
		"a layer narrows the directory":    {drDir("home/", 0o700, 0)},
		"a whiteout removes the file":      {{name: "home/node/.wh..profile", typ: tar.TypeReg}},
		"an opaque directory empties it":   {{name: "home/node/.wh..wh..opq", typ: tar.TypeReg}, drFile("home/node/other", 0o644, 1000, "x\n")},
		"a whiteout removes the directory": {{name: "home/.wh.node", typ: tar.TypeReg}},
	} {
		w := drBuildWith(t, drOpts{image: [][]drEntry{base, upper}})
		if err := drRefusal(t, w); err == nil {
			t.Errorf("%s: a valid artifact came out", name)
		}
	}
	// A later layer can also open what an earlier one closed.
	closed := []drEntry{drDir("./", 0o755, 0), drDir("home/", 0o700, 0), drDir("home/node/", 0o755, 1000), drFile("home/node/.profile", 0o644, 1000, "export X=1\n")}
	w := drBuildWith(t, drOpts{image: [][]drEntry{closed, {drDir("home/", 0o755, 0)}}})
	if err := drRefusal(t, w); err != nil {
		t.Errorf("a layer that opens the directory: %v", err)
	}
}

func TestDirectRuntimeProbeRefusesABaselineThatIsNotTheProbedImage(t *testing.T) {
	// The same target, readable and listable, in an image that is not the probed one.
	w := drBuild(t, nil, nil, nil)
	other := filepath.Join(w.dir, "other-oci")
	otherDigest := drLayout(t, other, [][]drEntry{append(drGoodImage()[0], drFile("etc/motd", 0o644, 0, "other image\n"))})
	if otherDigest == w.imageDigest {
		t.Fatal("the decoy image is the probed image")
	}
	w.opts.ociLayout = other
	if err := drRefusal(t, w); err == nil {
		t.Error("a baseline from a layout that does not hold the probed image gave a valid artifact")
	}

	// A layout whose index names the probed image but whose blobs are not the image's.
	w = drBuild(t, nil, nil, nil)
	blobs := filepath.Join(w.dir, "oci", "blobs", "sha256")
	entries, err := os.ReadDir(blobs)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p := filepath.Join(blobs, e.Name())
		orig, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, append(append([]byte(nil), orig...), 'x'), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := drRefusal(t, w); err == nil {
			t.Errorf("blob %s was altered and a valid artifact came out", e.Name())
		}
		if err := os.WriteFile(p, orig, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := drRefusal(t, w); err != nil {
		t.Fatalf("the restored layout is refused: %v", err)
	}
}

func TestDirectRuntimeArtifactStatesTheBaselineOfAnotherImageIsNotValid(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	d, kind, err := probeRouteDenied(w.opts, productionRouteProbeEnv())
	if err != nil {
		t.Fatal(err)
	}
	d.TargetFact.ImageDigest = geDigest("another image")
	if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), w.expect); err == nil {
		t.Error("an artifact whose baseline is of another image than the cell's was accepted")
	}
}

// ---- the artifact is bound to what the audited journal records, not to what the caller says ----

func TestDirectRuntimeProbeRefusesASourceDigestOtherThanTheJournalRecords(t *testing.T) {
	// An unchanged journal and profile packaged under another source digest.
	w := drBuild(t, nil, nil, nil)
	w.opts.frozenSource = geDigest("another source")
	if drProbeFails(t, w) == nil {
		t.Error("a journal of one source was packaged under another source digest")
	}
	// The profile of the journal holds another source than the run pins, though its own digest is as pinned.
	w = drBuildWith(t, drOpts{profile: func(p *piruntime.HostProfile) { p.SourceProvenanceDigest = geDigest("another source") }})
	if drProbeFails(t, w) == nil {
		t.Error("a profile of another source was accepted under the run's source pin")
	}
}

func TestDirectRuntimeProbeRefusesAProfileThatIsNotTheJournaledProductionProfilePlusTheProbe(t *testing.T) {
	// The probe profile differs from the production profile in more than the template digest.
	w := drBuildWith(t, drOpts{probeProfile: func(p *piruntime.HostProfile) { p.ExecutableDigest = geDigest("another node") }})
	if drProbeFails(t, w) == nil {
		t.Error("a probe profile that differs from the production profile beyond the template was accepted")
	}
	// A signed profile that is not the one the service journaled.
	w = drBuild(t, nil, nil, nil)
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
		"another commit":              func(e *guestevidence.Expect) { e.Commit = strings.Repeat("1", 40) },
		"another source digest":       func(e *guestevidence.Expect) { e.FrozenSourceDigest = geDigest("another source") },
		"another profile hash":        func(e *guestevidence.Expect) { e.ProfileHash = geDigest("another profile") },
		"another production template": func(e *guestevidence.Expect) { e.ContainmentProfileDigest = geDigest("another template") },
		"no production template pin":  func(e *guestevidence.Expect) { e.ContainmentProfileDigest = "" },
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

func TestDirectRuntimePinsComeFromTheRunConfigThatNamesTheCommit(t *testing.T) {
	w := drBuild(t, nil, nil, nil)
	b, err := directRuntimePins(w.runConfig, geCommit, "", "")
	if err != nil || b.Commit != geCommit || b.FrozenSourceDigest != rtSource || b.ProfileHash != w.opts.profileHash || b.ContainmentProfileDigest != w.opts.containmentDigest {
		t.Fatalf("pins = %+v %v", b, err)
	}
	if _, err := directRuntimePins(w.runConfig, geCommit, b.ProfileHash, b.FrozenSourceDigest); err != nil {
		t.Errorf("flags that repeat the file are refused: %v", err)
	}
	for name, args := range map[string][4]string{
		"no run config":             {"", geCommit, "", ""},
		"another commit":            {w.runConfig, strings.Repeat("2", 40), "", ""},
		"a profile flag that lies":  {w.runConfig, geCommit, geDigest("other"), ""},
		"a source flag that lies":   {w.runConfig, geCommit, "", geDigest("other")},
		"a run config that is gone": {filepath.Join(w.dir, "missing.json"), geCommit, "", ""},
	} {
		if _, err := directRuntimePins(args[0], args[1], args[2], args[3]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	drWriteJSON(t, w.runConfig, map[string]any{"freeze_commit": geCommit, "pins": map[string]string{"profile_hash": geDigest("p")}})
	if _, err := directRuntimePins(w.runConfig, geCommit, "", ""); err == nil {
		t.Error("a run config with no containment and source pins was accepted")
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
