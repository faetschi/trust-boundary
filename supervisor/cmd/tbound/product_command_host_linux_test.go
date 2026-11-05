//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/workspace"
)

func TestProductCommandHostRequiresCanonicalInstalledInputs(t *testing.T) {
	cfg := productServiceConfig{
		RegistrationPath:          productsetup.RegistrationPath,
		RegistrationSignaturePath: productsetup.RegistrationSignaturePath,
		RegistrationPublicKeyPath: productsetup.RegistrationPublicKeyPath,
	}
	for _, field := range []string{"registration", "signature", "key"} {
		changed := cfg
		switch field {
		case "registration":
			changed.RegistrationPath = "/etc/tbound/registration.json"
		case "signature":
			changed.RegistrationSignaturePath += ".other"
		case "key":
			changed.RegistrationPublicKeyPath += ".other"
		}
		if _, err := loadProductCommandHost(context.Background(), changed, &productTurnEnv{}); err == nil {
			t.Errorf("alternate %s path accepted", field)
		}
	}
	if _, err := loadProductCommandHost(context.Background(), cfg, nil); err == nil {
		t.Fatal("nil product environment accepted")
	}
}

func TestProductCommandHostStaticValidationGrantsNoAuthority(t *testing.T) {
	if _, _, err := validateProductCommandInputs(productServiceConfig{}, productsession.Registration{}, productCommandInputs{}); err == nil {
		t.Fatal("empty unsigned candidate accepted")
	}
	var host piruntime.D09ProductCommandHost
	if err := host.Revalidate(context.Background()); err == nil {
		t.Fatal("zero product host passed revalidation")
	}
}

func TestProductCommandHostStaticSignedInputs(t *testing.T) {
	cfg, registration, inputs := productCommandStaticFixture(t)
	profile, template, err := validateProductCommandInputs(cfg, registration, inputs, filepath.Dir(filepath.Dir(cfg.RegistrationPath)))
	if err != nil {
		t.Fatalf("valid prospective inputs: %v", err)
	}
	if template.ProductRegistrationDigest != registration.Digest || profile.ContainmentProfileDigest == "" {
		t.Fatal("static bindings missing")
	}
	for _, name := range []string{"wrong key", "profile signature", "registration signature", "offline verifier binding", "seccomp bytes", "cosign key bytes", "unit override", "config identity", "proposed registration", "thesis state", "outside product state", "relative project", "duplicate projects", "oversized key", "oversized profile signature", "oversized registration signature", "oversized unit"} {
		t.Run(name, func(t *testing.T) {
			changed, candidate, config := inputs, registration, cfg
			switch name {
			case "offline verifier binding":
				var profile piruntime.HostProfile
				if err := json.Unmarshal(inputs.Profile, &profile); err != nil {
					t.Fatal(err)
				}
				profile.OfflineVerifierDigest = piruntime.DigestBytes([]byte("another offline verifier"))
				raw, signature, _, err := piruntime.SignLinuxHostProfile(profile, ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
				if err != nil {
					t.Fatal(err)
				}
				changed.Profile, changed.ProfileSignature = raw, []byte(base64.StdEncoding.EncodeToString(signature))
			case "oversized unit":
				changed.Unit = append(append([]byte(nil), inputs.Unit...), []byte("\n#"+strings.Repeat("x", productServiceMaxConfigBytes))...)
			case "thesis state":
				config.StateRoot = "/var/lib/tbound"
			case "outside product state":
				config.StateRoot = "/var/lib/unrelated"
			case "relative project":
				config.Projects = map[string]string{registration.Projects[0].ProjectID: "relative"}
			case "duplicate projects":
				config.Projects = map[string]string{"a": "/var/lib/tbound-product/projects/same", "b": "/var/lib/tbound-product/projects/same"}
			case "oversized key":
				changed.PublicKey = append([]byte(strings.Repeat(" ", 1024)), inputs.PublicKey...)
			case "oversized profile signature":
				changed.ProfileSignature = append([]byte(strings.Repeat(" ", 1024)), inputs.ProfileSignature...)
			case "oversized registration signature":
				changed.RegistrationSignature = append([]byte(strings.Repeat(" ", 1024)), inputs.RegistrationSignature...)
			case "wrong key":
				changed.PublicKey = []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)))
			case "profile signature":
				changed.ProfileSignature = []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)))
			case "registration signature":
				changed.RegistrationSignature = []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)))
			case "seccomp bytes":
				changed.Seccomp = []byte("changed")
			case "cosign key bytes":
				changed.CosignPublicKey = []byte("changed")
			case "unit override":
				changed.Unit = append(append([]byte(nil), inputs.Unit...), []byte("\n[Service]\nEnvironment=HOME=/tmp\n")...)
			case "config identity":
				config.GitDigest = piruntime.DigestBytes([]byte("another git"))
			case "proposed registration":
				candidate.RegistrationID += "-other"
			}
			if _, _, err := validateProductCommandInputs(config, candidate, changed, filepath.Dir(filepath.Dir(cfg.RegistrationPath)), "/"); err == nil {
				t.Fatal("invalid prospective inputs accepted")
			}
		})
	}
}

func productCommandStaticFixture(t *testing.T) (productServiceConfig, productsession.Registration, productCommandInputs) {
	t.Helper()
	f := newProductHostFixture(t, true)
	registration, err := productsession.LoadRegistration(f.cfg.RegistrationPath, f.cfg.RegistrationSignaturePath, f.cfg.RegistrationPublicKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs := productCommandInputs{Unit: productsetup.RenderUnit(), Seccomp: []byte("{}"), CosignPublicKey: []byte("cosign public test key")}
	hash := func(value string) string { return piruntime.DigestBytes([]byte(value)) }
	landlock, err := piruntime.DigestD09LandlockRules([]string{"/usr"}, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	template := piruntime.D09CellTemplate{
		Image: "localhost/tbound/pi-cell:latest", ExpectedImageDigest: registration.ImageDigest,
		CosignPublicKeyPath: productsetup.CommandCosignPublicKeyPath, CosignPublicKeyDigest: piruntime.DigestBytes(inputs.CosignPublicKey),
		OCILayoutPath: productsetup.CommandOCILayoutPath, SeccompProfilePath: productsetup.CommandSeccompPath, SeccompProfileDigest: piruntime.DigestBytes(inputs.Seccomp),
		LandlockABI: 8, LandlockRulesetDigest: landlock, LandlockROPath: "/usr", LandlockRWPath: "/work", FrozenCapabilities: "0000000000000000",
		EntrypointPath: "/usr/local/bin/tbound-pi-entrypoint", EntrypointDigest: hash("entrypoint"), Runtime: "crun",
		CgroupsPath: "/sys/fs/cgroup/system.slice/tbound-product.service/cells", CPUs: "1", PidsMax: 256, MemoryMB: 1024,
		PodmanPath: "/usr/bin/podman", PodmanDigest: hash("podman"), CrunPath: "/usr/bin/crun", CrunDigest: hash("crun"), CosignPath: "/usr/bin/cosign", CosignDigest: hash("cosign"),
		CommandExecutable: "/bin/bash", CommandExecutableDigest: hash("bash"), CellUID: 1000, CellGID: 1000,
		NamespaceProfile: "user,mount,pid,ipc,uts,network-none/v1", CgroupVersion: "v2", CgroupControllers: "cpu,memory,pids",
		XattrFilesystemMagic: 0xef53, XattrMountOptionsDigest: hash("mount"), XattrScanUID: 1001, XattrScanGID: 1002,
		XattrScanPrivilegeProfile: piruntime.D09XattrScanPrivilegeProfile, XattrVisibilityAuthority: piruntime.D09XattrVisibilityAuthority,
		ProductRegistrationDigest: registration.Digest, UserNamespaceMappingProfile: piruntime.D09UserNamespaceMappingProfile,
		UserNamespaceMappingDigest: piruntime.DigestD09UserNamespaceMapping(1000, 1000, 1001, 1002),
	}
	template.XattrVisibilityProfileDigest = piruntime.DigestD09XattrVisibilityProfile(template.XattrFilesystemMagic, template.XattrMountOptionsDigest, 1001, 1002, template.XattrScanPrivilegeProfile)
	profile := piruntime.DefaultLinuxHostProfileTemplate()
	// These are offline fixture measurements, never an admitted live host.
	profile.ID, profile.ExecutableDigest, profile.CgroupRootDigest = "product-command-static-test", hash("node"), hash("cgroup")
	profile.PiRuntimeBundleDigest, profile.SourceProvenanceDigest, profile.ProviderProfileDigest = hash("bundle"), hash("source"), hash("provider")
	profile.OfflineVerifierDigest = piruntime.OfflineVerifierDigest(template.CosignDigest, template.CosignPublicKeyDigest)
	profile.ContainmentProfileDigest, err = template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	inputs.Template, err = json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	raw, signature, publicKey, err := piruntime.SignLinuxHostProfile(profile, key)
	if err != nil {
		t.Fatal(err)
	}
	inputs.Profile, inputs.ProfileSignature = raw, []byte(base64.StdEncoding.EncodeToString(signature))
	inputs.PublicKey = []byte(base64.StdEncoding.EncodeToString(publicKey))
	inputs.Registration, signature, err = productsession.SignRegistration(registration, key)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RegistrationSignature = []byte(base64.StdEncoding.EncodeToString(signature))
	return f.cfg, registration, inputs
}

func TestProductCommandHostCalibrationCannotChangeBetweenChecks(t *testing.T) {
	_, _, in := productCommandStaticFixture(t)
	template, err := piruntime.DecodeProductD09CellTemplate(in.Template)
	if err != nil {
		t.Fatal(err)
	}
	calibration := piruntime.D09XattrEnumerationCalibration{
		Digest: piruntime.DigestBytes([]byte("calibration")), Authority: template.XattrVisibilityAuthority,
		FilesystemMagic: template.XattrFilesystemMagic, MountOptionsDigest: template.XattrMountOptionsDigest,
		ScanUID: template.XattrScanUID, ScanGID: template.XattrScanGID, ScanPrivilegeProfile: template.XattrScanPrivilegeProfile,
	}
	attestation := workspace.XattrVisibilityAttestation{ProfileDigest: calibration.Digest, Authority: workspace.XattrVisibilityAuthority(calibration.Authority)}
	if !productCommandCalibrationMatches(attestation, calibration, template) {
		t.Fatal("matching measured calibration refused")
	}
	replaced := calibration
	replaced.Digest = piruntime.DigestBytes([]byte("replacement calibration"))
	if productCommandCalibrationMatches(attestation, replaced, template) {
		t.Fatal("calibration replacement between measurement and binding was accepted")
	}
	template.XattrScanUID++
	if productCommandCalibrationMatches(attestation, calibration, template) {
		t.Fatal("template/live scanner mismatch accepted")
	}
}
