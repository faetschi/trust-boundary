//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
)

func pinsDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// pinsTemplate writes a measured D09 cell template and returns its path. mods replaces
// top-level string members.
func pinsTemplate(t *testing.T, dir, name string, mods map[string]string) string {
	t.Helper()
	xattrOptions := piruntime.DigestBytes([]byte("ro,relatime"))
	landlock, err := piruntime.DigestD09LandlockRules([]string{"/usr", "/lib", "/lib64", "/etc", "/runtime"}, []string{"/work", "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"image": "localhost/tbound/pi-cell:latest", "expected_image_digest": pinsDigest("a"),
		"cosign_public_key_path": "/etc/tbound/cosign.pub", "cosign_public_key_digest": pinsDigest("9"),
		"oci_layout_path": "/etc/tbound/oci", "seccomp_profile_path": "/etc/tbound/seccomp.json", "seccomp_profile_digest": pinsDigest("b"),
		"landlock_abi": 8, "landlock_ruleset_digest": landlock,
		"landlock_ro_path": "/usr,/lib,/lib64,/etc,/runtime", "landlock_rw_path": "/work,/tmp",
		"frozen_capabilities": "0000000000000000",
		"entrypoint_path":     "/usr/local/bin/tbound-pi-entrypoint", "entrypoint_digest": pinsDigest("d"),
		"runtime": "crun", "cgroups_path": "/sys/fs/cgroup/tbound", "cpus": "1", "pids_max": 256, "memory_mb": 1024,
		"podman_path": "/usr/bin/podman", "podman_digest": pinsDigest("e"), "crun_path": "/usr/bin/crun", "crun_digest": pinsDigest("f"),
		"cosign_path": "/usr/bin/cosign", "cosign_digest": pinsDigest("1"),
		"command_executable": "/bin/bash", "command_executable_digest": pinsDigest("2"),
		"cell_uid": 1000, "cell_gid": 1000, "namespace_profile": "user,mount,pid,ipc,uts,network-none/v1",
		"cgroup_version": "v2", "cgroup_controllers": "cpu,memory,pids",
		"xattr_filesystem_magic": 61267, "xattr_mount_options_digest": xattrOptions, "xattr_scan_uid": 1000, "xattr_scan_gid": 1000,
		"xattr_scan_privilege_profile":          piruntime.D09XattrScanPrivilegeProfile,
		"xattr_visibility_profile_digest":       piruntime.DigestD09XattrVisibilityProfile(61267, xattrOptions, 1000, 1000, piruntime.D09XattrScanPrivilegeProfile),
		"xattr_visibility_authority":            piruntime.D09XattrVisibilityAuthority,
		"managed_live_root_registration_digest": pinsDigest("3"),
		"user_namespace_mapping_profile":        piruntime.D09UserNamespaceMappingProfile,
		"user_namespace_mapping_digest":         piruntime.DigestD09UserNamespaceMapping(1000, 1000, 991, 992),
	}
	for k, v := range mods {
		if _, ok := doc[k]; !ok {
			t.Fatalf("no template member %q", k)
		}
		doc[k] = v
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runPins(t *testing.T, args ...string) (evalcorpus.Pins, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := run(append([]string{"corpus-pins"}, args...), &out, &errOut); err != nil {
		return evalcorpus.Pins{}, err
	}
	var pins evalcorpus.Pins
	if err := json.Unmarshal(out.Bytes(), &pins); err != nil {
		t.Fatalf("corpus-pins printed %q: %v", out.String(), err)
	}
	return pins, nil
}

func pinsDiff(a, b evalcorpus.Pins) []string {
	var changed []string
	for name, pair := range map[string][2]string{
		"environment": {a.EnvironmentDigest, b.EnvironmentDigest}, "model": {a.ModelDigest, b.ModelDigest},
		"profile": {a.ProfileDigest, b.ProfileDigest}, "policy": {a.PolicyDigest, b.PolicyDigest}} {
		if pair[0] != pair[1] {
			changed = append(changed, name)
		}
	}
	slices.Sort(changed)
	return changed
}

func TestCorpusPinsAreWellFormedAndDeterministic(t *testing.T) {
	dir := t.TempDir()
	tmpl := pinsTemplate(t, dir, "d09-cell.json", nil)
	args := []string{"--template", tmpl, "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6")}
	first, err := runPins(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runPins(t, args...)
	if err != nil || first != second {
		t.Fatalf("not deterministic: %v %+v %+v", err, first, second)
	}
	m := evalcorpus.Manifest{Pins: first}
	seen := map[string]bool{}
	for _, d := range []string{m.Pins.EnvironmentDigest, m.Pins.ModelDigest, m.Pins.ProfileDigest, m.Pins.PolicyDigest} {
		if len(d) != len("sha256:")+64 || !strings.HasPrefix(d, "sha256:") || seen[d] {
			t.Fatalf("pin %q is malformed or repeated", d)
		}
		seen[d] = true
	}
	if first.AttackerDigest != "" {
		t.Fatal("corpus-pins must not set the attacker digest")
	}
}

// The circularity BL-4 names: the registration digest changes with the corpus-mode config (S3'),
// and the template digest TD with it. None of the manifest pins may move with it.
func TestCorpusPinsDoNotDependOnTheRegistrationOrTheFinalTemplateDigest(t *testing.T) {
	dir := t.TempDir()
	smoke := pinsTemplate(t, dir, "smoke.json", nil)
	corpus := pinsTemplate(t, dir, "corpus.json", map[string]string{"managed_live_root_registration_digest": pinsDigest("4")})
	args := func(tmpl string) []string {
		return []string{"--template", tmpl, "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6")}
	}
	a, err := runPins(t, args(smoke)...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := runPins(t, args(corpus)...)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("pins moved with the registration digest: %+v vs %+v", a, b)
	}
	var tdA, tdB bytes.Buffer
	if err := run([]string{"template-digest", "--template", smoke}, &tdA, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"template-digest", "--template", corpus}, &tdB, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if tdA.String() == tdB.String() {
		t.Fatal("the fixture does not show the circularity: the template digest did not change with the registration digest")
	}
	for _, pin := range []string{a.EnvironmentDigest, a.ModelDigest, a.ProfileDigest, a.PolicyDigest} {
		if strings.Contains(tdA.String(), pin) || strings.Contains(tdB.String(), pin) {
			t.Fatalf("pin %s equals a template digest", pin)
		}
	}
}

func TestCorpusPinsBindTheirInputs(t *testing.T) {
	dir := t.TempDir()
	base := pinsTemplate(t, dir, "base.json", nil)
	baseArgs := []string{"--template", base, "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6")}
	want, err := runPins(t, baseArgs...)
	if err != nil {
		t.Fatal(err)
	}
	withTemplate := func(name string, mods map[string]string) []string {
		return []string{"--template", pinsTemplate(t, dir, name+".json", mods), "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6")}
	}
	research := filepath.Join(dir, "research.json")
	if err := os.WriteFile(research, []byte(frozenResearchProfile("7")), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args    []string
		changed []string
	}{
		"runtime bundle": {[]string{"--template", base, "--runtime-bundle-digest", pinsDigest("8"), "--node-digest", pinsDigest("6")}, []string{"environment"}},
		"node":           {[]string{"--template", base, "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("8")}, []string{"environment"}},
		"image":          {withTemplate("image", map[string]string{"expected_image_digest": pinsDigest("c")}), []string{"environment", "profile"}},
		"entrypoint":     {withTemplate("entry", map[string]string{"entrypoint_digest": pinsDigest("c")}), []string{"environment", "profile"}},
		"bash":           {withTemplate("bash", map[string]string{"command_executable_digest": pinsDigest("c")}), []string{"environment", "profile"}},
		"seccomp":        {withTemplate("seccomp", map[string]string{"seccomp_profile_digest": pinsDigest("c")}), []string{"environment", "profile"}},
		"podman":         {withTemplate("podman", map[string]string{"podman_digest": pinsDigest("c")}), []string{"profile"}},
		"crun":           {withTemplate("crun", map[string]string{"crun_digest": pinsDigest("c")}), []string{"profile"}},
		"cosign key":     {withTemplate("cosign", map[string]string{"cosign_public_key_digest": pinsDigest("c")}), []string{"profile"}},
		"research route": {append(slices.Clone(baseArgs), "--provider-route", "research", "--research-profile", research), []string{"model"}},
	} {
		got, err := runPins(t, tc.args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if diff := pinsDiff(want, got); !slices.Equal(diff, tc.changed) {
			t.Errorf("%s: changed %v, want %v", name, diff, tc.changed)
		}
	}
	// A different research profile moves the model pin; the same one does not.
	research2 := filepath.Join(dir, "research2.json")
	if err := os.WriteFile(research2, []byte(frozenResearchProfile("8")), 0o600); err != nil {
		t.Fatal(err)
	}
	r1, err1 := runPins(t, append(slices.Clone(baseArgs), "--provider-route", "research", "--research-profile", research)...)
	r2, err2 := runPins(t, append(slices.Clone(baseArgs), "--provider-route", "research", "--research-profile", research2)...)
	if err1 != nil || err2 != nil || r1.ModelDigest == r2.ModelDigest {
		t.Fatalf("research profile does not bind the model pin: %v %v", err1, err2)
	}
}

// The policy pin binds the production gate profile and the two admission policy digests that
// run.json carries (runbook: e6a77851... and bf937a0d...).
func TestCorpusPolicyPinBindsTheGateAndAdmissionPolicies(t *testing.T) {
	session := sha256.Sum256([]byte(gate.ProductionSessionPolicyLabel))
	metadata := sha256.Sum256([]byte(gate.ProductionMetadataPolicyLabel))
	if hex.EncodeToString(session[:]) != "e6a77851e90357efcba0fdc8036493acab5f74e911f38d5f32e001477419f683" ||
		hex.EncodeToString(metadata[:]) != "bf937a0d4d98f54111e82d025dc3b9aa7eeec939f71343547ba4392624e0785c" {
		t.Fatal("the admission policy labels no longer give the digests run.json admission_policy documents")
	}
	policy, err := gate.NewPolicy(gate.ProductionProfile())
	if err != nil {
		t.Fatal(err)
	}
	want, err := corpusPin("policy", policyPinDoc{GateProfileDigest: policy.Digest(),
		SessionPolicyDigest: "sha256:" + hex.EncodeToString(session[:]), MetadataPolicyDigest: "sha256:" + hex.EncodeToString(metadata[:])})
	if err != nil {
		t.Fatal(err)
	}
	got, err := runPins(t, "--template", pinsTemplate(t, t.TempDir(), "t.json", nil), "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6"))
	if err != nil || got.PolicyDigest != want {
		t.Fatalf("policy pin = %q, want %q (%v)", got.PolicyDigest, want, err)
	}
}

func TestCorpusPinsManifestCheck(t *testing.T) {
	dir := t.TempDir()
	tmpl := pinsTemplate(t, dir, "t.json", nil)
	args := []string{"--template", tmpl, "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6")}
	pins, err := runPins(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, p evalcorpus.Pins) string {
		m := evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: "s", Pins: p, Cases: []evalcorpus.Case{}}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := runPins(t, append(slices.Clone(args), "--manifest", write("good.json", pins))...); err != nil {
		t.Fatalf("a manifest with the computed pins was refused: %v", err)
	}
	stale := pins
	stale.ProfileDigest, stale.ModelDigest = pinsDigest("0"), pinsDigest("0")
	_, err = runPins(t, append(slices.Clone(args), "--manifest", write("stale.json", stale))...)
	if err == nil || !strings.Contains(err.Error(), "profile_digest") || !strings.Contains(err.Error(), "model_digest") ||
		strings.Contains(err.Error(), "environment_digest") {
		t.Fatalf("stale manifest pins: %v", err)
	}
}

func TestCorpusPinsRefusals(t *testing.T) {
	dir := t.TempDir()
	tmpl := pinsTemplate(t, dir, "t.json", nil)
	unmeasured := filepath.Join(dir, "unmeasured.json")
	if err := run([]string{"template-init", "--out", unmeasured}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	research := filepath.Join(dir, "research.json")
	if err := os.WriteFile(research, []byte(frozenResearchProfile("7")), 0o600); err != nil {
		t.Fatal(err)
	}
	ok := []string{"--template", tmpl, "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6")}
	for name, args := range map[string][]string{
		"no args":               {},
		"no template":           {"--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6")},
		"no bundle digest":      {"--template", tmpl, "--node-digest", pinsDigest("6")},
		"no node digest":        {"--template", tmpl, "--runtime-bundle-digest", pinsDigest("5")},
		"bad bundle digest":     {"--template", tmpl, "--runtime-bundle-digest", "sha256:abc", "--node-digest", pinsDigest("6")},
		"upper case digest":     {"--template", tmpl, "--runtime-bundle-digest", "sha256:" + strings.Repeat("A", 64), "--node-digest", pinsDigest("6")},
		"unmeasured template":   {"--template", unmeasured, "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6")},
		"missing template":      {"--template", filepath.Join(dir, "none.json"), "--runtime-bundle-digest", pinsDigest("5"), "--node-digest", pinsDigest("6")},
		"unknown route":         append(slices.Clone(ok), "--provider-route", "openrouter"),
		"research no profile":   append(slices.Clone(ok), "--provider-route", "research"),
		"engineering + profile": append(slices.Clone(ok), "--research-profile", research),
		"positional":            append(slices.Clone(ok), "extra"),
		"unknown flag":          append(slices.Clone(ok), "--registration-digest", pinsDigest("3")),
	} {
		if _, err := runPins(t, args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
