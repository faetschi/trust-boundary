//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/piruntime"
)

// signWorld is a directory with an admin key, a cosign executable and public key stand-in, and the init flags
// that fill every measured identity of a host profile.
type signWorld struct {
	dir, key                         string
	cosign, cosignPub                string
	initFlags                        []string
	templateDigest, nodeDigest       string
	bundleDigest, cgroupRootDigest   string
	sourceDigest                     string
	cosignDigest, cosignPublicDigest string
}

func newSignWorld(t *testing.T) signWorld {
	t.Helper()
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	w := signWorld{dir: dir, key: filepath.Join(dir, "admin.key"), cosign: filepath.Join(dir, "cosign"), cosignPub: filepath.Join(dir, "cosign.pub")}
	for p, data := range map[string][]byte{w.key: priv, w.cosign: []byte("cosign binary"), w.cosignPub: []byte("cosign public key")} {
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w.templateDigest, w.nodeDigest, w.bundleDigest = geDigest("TD"), geDigest("node"), geDigest("bundle")
	w.cgroupRootDigest, w.sourceDigest = geDigest("cgroup root"), geDigest("source tree of F")
	w.cosignDigest, w.cosignPublicDigest = piruntime.DigestBytes([]byte("cosign binary")), piruntime.DigestBytes([]byte("cosign public key"))
	w.initFlags = []string{"--id", "pi-host-profile-smoke", "--node-digest", w.nodeDigest, "--runtime-bundle-digest", w.bundleDigest,
		"--template-digest", w.templateDigest, "--cgroup-root-digest", w.cgroupRootDigest, "--source-digest", w.sourceDigest,
		"--cosign", w.cosign, "--cosign-pub", w.cosignPub, "--provider-route", "engineering"}
	return w
}

func (w signWorld) init(name string, extra ...string) (string, error) {
	path := filepath.Join(w.dir, name)
	var out, errOut bytes.Buffer
	return path, run(append(append([]string{"init", "--out", path}, w.initFlags...), extra...), &out, &errOut)
}

func (w signWorld) sign(profile, name string) (sig, pub, signed string, err error) {
	sig, pub, signed = filepath.Join(w.dir, name+".ed25519"), filepath.Join(w.dir, name+".pub"), filepath.Join(w.dir, name+".signed.json")
	var out, errOut bytes.Buffer
	err = run([]string{"sign", "--profile", profile, "--key", w.key, "--out-sig", sig, "--out-pub", pub, "--out-profile", signed}, &out, &errOut)
	return
}

// init fills every identity the operator measures from the values of the freeze, so that the signed profile binds
// the frozen source, the offline verifier and the provider route and not a placeholder.
func TestProfileInitFillsEveryMeasuredIdentity(t *testing.T) {
	w := newSignWorld(t)
	path, err := w.init("profile.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p piruntime.HostProfile
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	model, err := modelPinFor(pinRouteEngineering, "")
	if err != nil {
		t.Fatal(err)
	}
	wantProvider, err := corpusPin("model", model)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "pi-host-profile-smoke" || p.ExecutableDigest != w.nodeDigest || p.PiRuntimeBundleDigest != w.bundleDigest || p.ContainmentProfileDigest != w.templateDigest ||
		p.CgroupRootDigest != w.cgroupRootDigest || p.SourceProvenanceDigest != w.sourceDigest ||
		p.OfflineVerifierDigest != piruntime.OfflineVerifierDigest(w.cosignDigest, w.cosignPublicDigest) || p.ProviderProfileDigest != wantProvider {
		t.Fatalf("profile = %+v", p)
	}
	if got := p.PlaceholderFields(); len(got) != 0 {
		t.Fatalf("placeholders left: %v", got)
	}
	// The provider profile digest is the manifest's model pin: one definition of the engineering route profile.
	if model.Endpoint != openrouter.Endpoint || model.Model != openrouter.ApprovedModel {
		t.Fatalf("the engineering route profile is not the OpenCode Go endpoint and approved model: %+v", model)
	}
}

func TestProfileInitRefusesWhatIsNotAMeasuredValue(t *testing.T) {
	w := newSignWorld(t)
	for name, extra := range map[string][]string{
		"the template id":                      {"--id", piruntime.LinuxHostProfileTemplateID},
		"a node digest that is not one":        {"--node-digest", "node"},
		"a source digest that is not one":      {"--source-digest", "abc"},
		"cosign without its key":               {"--cosign-pub", ""},
		"a research route without its profile": {"--provider-route", "research"},
		"an unknown route":                     {"--provider-route", "local"},
	} {
		if _, err := w.init("bad-"+strings.ReplaceAll(name, " ", "-")+".json", extra...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestProfileSignRefusesAProfileWithPlaceholders(t *testing.T) {
	w := newSignWorld(t)
	template := filepath.Join(w.dir, "template.json")
	var out, errOut bytes.Buffer
	if err := run([]string{"init", "--out", template}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	sig, pub, signed, err := w.sign(template, "t")
	if err == nil || !strings.Contains(err.Error(), "SourceProvenanceDigest") || !strings.Contains(err.Error(), "ID") {
		t.Fatalf("an unedited template was signed or the refusal does not name its placeholders: %v", err)
	}
	for _, p := range []string{sig, pub, signed} {
		if _, statErr := os.Stat(p); statErr == nil {
			t.Errorf("a refused signing left %s", p)
		}
	}
	// One placeholder left among measured values is still refused.
	good, err := w.init("good.json")
	if err != nil {
		t.Fatal(err)
	}
	var p piruntime.HostProfile
	data, _ := os.ReadFile(good)
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	p.SourceProvenanceDigest = piruntime.DefaultLinuxHostProfileTemplate().SourceProvenanceDigest
	data, _ = json.Marshal(p)
	one := filepath.Join(w.dir, "one.json")
	if err := os.WriteFile(one, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.sign(one, "one"); err == nil || !strings.Contains(err.Error(), "SourceProvenanceDigest") {
		t.Fatalf("a profile with a placeholder source was signed: %v", err)
	}
	// A measured profile signs and verifies.
	sig, pub, signed, err = w.sign(good, "good")
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--profile", signed, "--sig", sig, "--pub", pub}, &out, &errOut); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// verify is the owner's review tool: it must not call a profile that still holds a placeholder verified.
func TestProfileVerifyRefusesAProfileWithPlaceholders(t *testing.T) {
	w := newSignWorld(t)
	_, priv, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profile := piruntime.DefaultLinuxHostProfileTemplate()
	profile.ID = "pi-host-profile-smoke"
	profileJSON, sig, pub, err := piruntime.SignLinuxHostProfile(profile, priv)
	if err != nil {
		t.Fatal(err)
	}
	var files [3]string
	for i, data := range [][]byte{profileJSON, sig, pub} {
		files[i] = filepath.Join(w.dir, []string{"p.json", "p.sig", "p.pub"}[i])
		if err := os.WriteFile(files[i], data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	err = run([]string{"verify", "--profile", files[0], "--sig", files[1], "--pub", files[2]}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "ExecutableDigest") {
		t.Fatalf("a signed profile with placeholders verified: %v", err)
	}
}

// Rule 4 of the runbook: a signature output is a new path. sign never overwrites and never rewrites the profile
// it reads, so a rollback copy and an earlier signature survive a second signing.
func TestProfileSignRefusesAnExistingOutputAndInPlaceSigning(t *testing.T) {
	w := newSignWorld(t)
	good, err := w.init("good.json")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(good)
	sig, pub, signed, err := w.sign(good, "first")
	if err != nil {
		t.Fatal(err)
	}
	firstSig, _ := os.ReadFile(sig)
	fresh := func(name string) string { return filepath.Join(w.dir, name) }
	var out, errOut bytes.Buffer
	for name, c := range map[string]struct {
		args    []string
		created []string // paths the refused run must not leave behind
	}{
		"the signature exists":        {[]string{"--out-sig", sig, "--out-pub", fresh("n1.pub"), "--out-profile", fresh("n1.json")}, []string{fresh("n1.pub"), fresh("n1.json")}},
		"the public key exists":       {[]string{"--out-sig", fresh("n2.sig"), "--out-pub", pub, "--out-profile", fresh("n2.json")}, []string{fresh("n2.sig"), fresh("n2.json")}},
		"the signed profile exists":   {[]string{"--out-sig", fresh("n3.sig"), "--out-pub", fresh("n3.pub"), "--out-profile", signed}, []string{fresh("n3.sig"), fresh("n3.pub")}},
		"in place (no out-profile)":   {[]string{"--out-sig", fresh("n4.sig"), "--out-pub", fresh("n4.pub")}, []string{fresh("n4.sig"), fresh("n4.pub")}},
		"in place (out is the input)": {[]string{"--out-sig", fresh("n5.sig"), "--out-pub", fresh("n5.pub"), "--out-profile", good}, []string{fresh("n5.sig"), fresh("n5.pub")}},
		"two outputs are one path":    {[]string{"--out-sig", fresh("n6"), "--out-pub", fresh("n6"), "--out-profile", fresh("n6.json")}, []string{fresh("n6"), fresh("n6.json")}},
	} {
		err := run(append([]string{"sign", "--profile", good, "--key", w.key}, c.args...), &out, &errOut)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		for _, p := range c.created {
			if _, statErr := os.Stat(p); statErr == nil {
				t.Errorf("%s: a refused signing left %s", name, p)
			}
		}
	}
	if after, _ := os.ReadFile(good); !bytes.Equal(before, after) {
		t.Error("the input profile was rewritten")
	}
	if s, _ := os.ReadFile(sig); !bytes.Equal(s, firstSig) {
		t.Error("an earlier signature was overwritten")
	}
}
