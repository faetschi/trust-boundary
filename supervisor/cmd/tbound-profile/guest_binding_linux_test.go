//go:build linux

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
)

// bindRunner is the service executable the pilot pins from the reproducible build of the frozen commit: the runner the
// signed registration of a run names.
var bindRunner = geDigest("runner")

// bindTemplate is the cell template of a run under a config of its own: the pilot's cell (same image,
// mounts, Landlock ruleset) with the registration digest of that config.
func bindTemplate(t *testing.T) piruntime.D09CellTemplate {
	t.Helper()
	template := drTemplate(t, geDigest("image"), "/opt/tbound/cell/pi-cell-oci")
	// The digest of a registration is the hash of its signing bytes, so it does not depend on the key that signs it.
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, _, template.ManagedLiveRootRegistrationDigest, err = piruntime.SignD09ManagedLiveRootRegistration(testRegistration(bindRunner), key)
	if err != nil {
		t.Fatal(err)
	}
	return template
}

// bindPilotBase is the base template digest the pilot pins: the same cell with the registration of the pilot's
// config, registration emptied.
func bindPilotBase(t *testing.T) string {
	t.Helper()
	pilot := bindTemplate(t)
	pilot.ManagedLiveRootRegistrationDigest = geDigest("registration of the pilot config")
	base, err := baseTemplateDigest(pilot)
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func rtTemplateDigest(t *testing.T) string {
	t.Helper()
	d, err := bindTemplate(t).Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// bindWorld is a run under a config of its own (a G1 or route-probe run): its template, the signed host profile
// that names it, and the run.json that pins what the run shares with the pilot.
type bindWorld struct {
	profileDigest, templateDigest string
	files                         runProfileFiles
	runConfig                     string
	pins                          guestevidence.RunBinding
}

func newBindWorld(t *testing.T, dir string) bindWorld {
	t.Helper()
	_, adminKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registrationPath, registrationSigPath, registrationDigest := drSignRegistration(t, dir, "run-registration", adminKey, bindRunner)
	template := bindTemplate(t)
	if template.ManagedLiveRootRegistrationDigest != registrationDigest {
		t.Fatal("the registration digest depends on the key")
	}
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profile := piruntime.DefaultLinuxHostProfileTemplate()
	bundle := profile.PiRuntimeBundleDigest
	profile.ContainmentProfileDigest, profile.SourceProvenanceDigest = templateDigest, rtSource
	profilePath, sigPath, keyPath, profileDigest := drSignProfileWith(t, dir, "run-profile", profile, adminKey)
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	w := bindWorld{profileDigest: profileDigest, templateDigest: templateDigest,
		files:     runProfileFiles{template: filepath.Join(dir, "run-template.json"), hostProfile: profilePath, hostProfileSig: sigPath, hostProfileKey: keyPath, registration: registrationPath, registrationSig: registrationSigPath},
		runConfig: filepath.Join(dir, "run.json"),
		pins:      guestevidence.RunBinding{Commit: geCommit, FrozenSourceDigest: rtSource, BaseTemplateDigest: bindPilotBase(t), ImageDigest: template.ExpectedImageDigest, AdminPubkeyDigest: piruntime.DigestBytes(keyBytes), RuntimeBundleDigest: bundle, RunnerDigest: bindRunner}}
	drWriteJSON(t, w.files.template, template)
	drWriteJSON(t, w.runConfig, map[string]any{"freeze_commit": geCommit, "v_image_digest": w.pins.ImageDigest,
		"pins": map[string]string{"profile_hash": geDigest("pilot profile"), "containment_profile_digest": geDigest("pilot template"),
			"base_template_digest": w.pins.BaseTemplateDigest, "frozen_source_digest": rtSource, "admin_pubkey_digest": w.pins.AdminPubkeyDigest,
			"runtime_bundle_digest": bundle, "runner_digest": bindRunner}})
	return w
}

func (w bindWorld) expect() guestevidence.Expect {
	return guestevidence.Expect{Commit: geCommit, BinarySHA256: geDigest("self"), FrozenSourceDigest: w.pins.FrozenSourceDigest,
		BaseTemplateDigest: w.pins.BaseTemplateDigest, ImageDigest: w.pins.ImageDigest, AdminPubkeyDigest: w.pins.AdminPubkeyDigest,
		RuntimeBundleDigest: w.pins.RuntimeBundleDigest, RunnerDigest: w.pins.RunnerDigest}
}
