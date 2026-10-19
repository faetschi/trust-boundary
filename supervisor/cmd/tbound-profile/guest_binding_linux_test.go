//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
)

// bindTemplate is the cell template of a run under a config of its own: the pilot's cell (same image,
// mounts, Landlock ruleset) with the registration digest of that config.
func bindTemplate(t *testing.T) piruntime.D09CellTemplate {
	t.Helper()
	return drTemplate(t, geDigest("image"), "/opt/tbound/cell/pi-cell-oci")
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
	template := bindTemplate(t)
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profile := piruntime.DefaultLinuxHostProfileTemplate()
	profile.ContainmentProfileDigest, profile.SourceProvenanceDigest = templateDigest, rtSource
	profilePath, sigPath, keyPath, profileDigest := drSignProfile(t, dir, "run-profile", profile)
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	w := bindWorld{profileDigest: profileDigest, templateDigest: templateDigest,
		files:     runProfileFiles{template: filepath.Join(dir, "run-template.json"), hostProfile: profilePath, hostProfileSig: sigPath, hostProfileKey: keyPath},
		runConfig: filepath.Join(dir, "run.json"),
		pins:      guestevidence.RunBinding{Commit: geCommit, FrozenSourceDigest: rtSource, BaseTemplateDigest: bindPilotBase(t), ImageDigest: template.ExpectedImageDigest, AdminPubkeyDigest: piruntime.DigestBytes(keyBytes)}}
	drWriteJSON(t, w.files.template, template)
	drWriteJSON(t, w.runConfig, map[string]any{"freeze_commit": geCommit, "v_image_digest": w.pins.ImageDigest,
		"pins": map[string]string{"profile_hash": geDigest("pilot profile"), "containment_profile_digest": geDigest("pilot template"),
			"base_template_digest": w.pins.BaseTemplateDigest, "frozen_source_digest": rtSource, "admin_pubkey_digest": w.pins.AdminPubkeyDigest}})
	return w
}

func (w bindWorld) expect() guestevidence.Expect {
	return guestevidence.Expect{Commit: geCommit, BinarySHA256: geDigest("self"), FrozenSourceDigest: w.pins.FrozenSourceDigest,
		BaseTemplateDigest: w.pins.BaseTemplateDigest, ImageDigest: w.pins.ImageDigest, AdminPubkeyDigest: w.pins.AdminPubkeyDigest}
}
