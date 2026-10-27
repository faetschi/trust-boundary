//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/releasetrust"
)

// calibrationSignHost is a directory with an offline key pair and an unsigned measurement. The signer's
// private key and the registration public key are the product's base64 files, not the research raw keys.
type calibrationSignHost struct {
	t         *testing.T
	dir       string
	key, pub  string
	unsigned  string
	out       string
	candidate piruntime.D09XattrEnumerationCalibration
}

func unsignedProductCalibration(profile string) piruntime.D09XattrEnumerationCalibration {
	const magic, uid = 0xef53, 1000
	mount := digestBytes([]byte("offline-mount"))
	return piruntime.D09XattrEnumerationCalibration{Authority: piruntime.D09XattrBoundedAuthority,
		FilesystemMagic: magic, MountOptionsDigest: mount, ScanUID: uid, ScanGID: uid,
		ScanPrivilegeProfile: piruntime.D09XattrScanPrivilegeProfile, SourceAccessProfile: profile,
		EnumerationProfile:      piruntime.D09XattrEnumerationProfile,
		VisibilityProfileDigest: piruntime.DigestD09XattrVisibilityProfile(magic, mount, uid, uid, piruntime.D09XattrScanPrivilegeProfile),
		ProbeCorpusDigest:       digestBytes([]byte("offline-probe")), ProbeObservationDigest: digestBytes([]byte("offline-observation")),
		ProbeEntryCount: 1, ProbeXattrNameCount: 1}
}

func newCalibrationSignHost(t *testing.T) *calibrationSignHost {
	t.Helper()
	h := &calibrationSignHost{t: t, dir: t.TempDir()}
	keys := filepath.Join(h.dir, "keys")
	mustAdmin(t, "product", "registration", "keygen", "--out", keys)
	h.key, h.pub = filepath.Join(keys, "registration.key"), filepath.Join(keys, "registration.pub")
	h.candidate = unsignedProductCalibration(piruntime.D09XattrManagedRWSourceProfile)
	h.unsigned = h.writeUnsigned("unsigned.json", h.candidate)
	h.out = filepath.Join(h.dir, "xattr.json")
	return h
}

func (h *calibrationSignHost) writeUnsigned(name string, calibration piruntime.D09XattrEnumerationCalibration) string {
	h.t.Helper()
	data, err := json.MarshalIndent(calibration, "", "  ")
	if err != nil {
		h.t.Fatal(err)
	}
	path := filepath.Join(h.dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func (h *calibrationSignHost) sign(key, pub string) (string, error) {
	return adminRun("product", "calibration", "sign", "--key", key, "--public-key", pub, "--in", h.unsigned, "--out", h.out)
}

// requireNoOutput proves a refused signing left neither file nor a temporary behind.
func (h *calibrationSignHost) requireNoOutput() {
	h.t.Helper()
	entries, err := filepath.Glob(filepath.Join(h.dir, "xattr.json*"))
	if err != nil || len(entries) != 0 {
		h.t.Fatalf("a refused signing left %v (%v)", entries, err)
	}
	if temporaries, _ := filepath.Glob(filepath.Join(h.dir, ".tbound-*")); len(temporaries) != 0 {
		h.t.Fatalf("a refused signing left temporaries %v", temporaries)
	}
}

func TestProductCalibrationSignWritesAPairTheVerifierAccepts(t *testing.T) {
	h := newCalibrationSignHost(t)
	out, err := h.sign(h.key, h.pub)
	if err != nil {
		t.Fatalf("sign: %v\n%s", err, out)
	}
	pub, err := releasetrust.ReadPublicKey(h.pub)
	if err != nil {
		t.Fatal(err)
	}
	calibration, _, _, err := verifyProductCalibration(h.out, pub, []uint32{uint32(os.Geteuid())})
	if err != nil {
		t.Fatalf("the service's own check refused the signed calibration: %v", err)
	}
	if calibration.Digest == "" || !strings.Contains(out, calibration.Digest) {
		t.Fatalf("the output does not name the digest %q:\n%s", calibration.Digest, out)
	}
	private, err := os.ReadFile(h.key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, strings.TrimSpace(string(private))) {
		t.Fatal("the private key reached the output")
	}
	signature, err := os.ReadFile(h.out + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature))); err != nil || len(decoded) != ed25519.SignatureSize {
		t.Fatalf("the signature file is not one base64 signature: %v", err)
	}
	for _, path := range []string{h.out, h.out + ".sig"} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o022 != 0 {
			t.Fatalf("%s: %v %v", path, info, err)
		}
	}
	if _, err := h.sign(h.key, h.pub); adminExit(err) != 2 {
		t.Fatalf("sign overwrote its output: %v", err)
	}
}

func TestProductCalibrationSignRefusesBadKeysAndLeavesNoOutput(t *testing.T) {
	h := newCalibrationSignHost(t)
	other := filepath.Join(h.dir, "other")
	mustAdmin(t, "product", "registration", "keygen", "--out", other)
	raw, err := os.ReadFile(h.key)
	if err != nil {
		t.Fatal(err)
	}
	worldReadable := filepath.Join(h.dir, "readable.key")
	if err := os.WriteFile(worldReadable, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(worldReadable, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(h.dir, "link.key")
	if err := os.Symlink(h.key, link); err != nil {
		t.Fatal(err)
	}
	rawKey := filepath.Join(h.dir, "raw.key")
	if err := os.WriteFile(rawKey, make([]byte, ed25519.PrivateKeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(h.dir, "absent")
	for name, c := range map[string]struct{ key, pub, want string }{
		"private key of another pair":       {filepath.Join(other, "registration.key"), h.pub, "does not match"},
		"public key of another pair":        {h.key, filepath.Join(other, "registration.pub"), "does not match"},
		"world-readable private key":        {worldReadable, h.pub, "must not be readable by group or others"},
		"private key through a symlink":     {link, h.pub, "regular file"},
		"research raw private key":          {rawKey, h.pub, "base64"},
		"missing private key":               {absent + ".key", h.pub, "regular file"},
		"public key where the private goes": {h.pub, h.pub, "must not be readable by group or others"},
		"private key where the public goes": {h.key, h.key, "not a base64 Ed25519 public key"},
		"missing registration public key":   {h.key, absent + ".pub", "regular file"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := h.sign(c.key, c.pub)
			if adminExit(err) != 2 || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want exit 2 naming %q, got exit %d: %v\n%s", c.want, adminExit(err), err, out)
			}
			if strings.Contains(out+err.Error(), strings.TrimSpace(string(raw))) {
				t.Fatal("the private key reached the output")
			}
			h.requireNoOutput()
		})
	}
}

func TestProductCalibrationSignRefusesBadMeasurements(t *testing.T) {
	h := newCalibrationSignHost(t)
	readOnly := h.writeUnsigned("readonly.json", unsignedProductCalibration(piruntime.D09XattrReadonlySourceProfile))
	original, err := os.ReadFile(h.unsigned)
	if err != nil {
		t.Fatal(err)
	}
	mutated := func(from, to string) string {
		return writeCalibrationInput(t, h, bytes.Replace(original, []byte(from), []byte(to), 1))
	}
	for name, c := range map[string]struct{ in, want string }{
		"read-only access profile":       {readOnly, "managed"},
		"unknown field":                  {mutated(`"digest": ""`, `"digest": "", "extra": 1`), "unknown field"},
		"trailing data":                  {writeCalibrationInput(t, h, append(append([]byte{}, original...), []byte(`{}`)...)), "trailing"},
		"duplicate key":                  {mutated(`"scan_uid": 1000,`, `"scan_uid": 1, "scan_uid": 1000,`), "strict JSON"},
		"inconsistent visibility digest": {mutated(`"scan_uid": 1000`, `"scan_uid": 1001`), "cannot sign"},
		"missing input":                  {filepath.Join(h.dir, "absent.json"), "cannot read"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := adminRun("product", "calibration", "sign", "--key", h.key, "--public-key", h.pub, "--in", c.in, "--out", h.out)
			if adminExit(err) != 2 || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want exit 2 naming %q, got exit %d: %v\n%s", c.want, adminExit(err), err, out)
			}
			h.requireNoOutput()
		})
	}
}

func writeCalibrationInput(t *testing.T, h *calibrationSignHost, data []byte) string {
	t.Helper()
	path := filepath.Join(h.dir, "mutated.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The pair the signing command writes is what `product-serve` verifies at start: the fixture's calibration
// is re-signed by the command under the registration key and the service starts on it.
func TestProductCalibrationSignOutputStartsTheService(t *testing.T) {
	f := newProductHostFixture(t, false)
	signed, err := os.ReadFile(f.cfg.XattrCalibrationPath)
	if err != nil {
		t.Fatal(err)
	}
	var calibration piruntime.D09XattrEnumerationCalibration
	if err := json.Unmarshal(signed, &calibration); err != nil {
		t.Fatal(err)
	}
	calibration.Digest = ""
	unsigned, err := json.MarshalIndent(calibration, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(f.root, "unsigned.json")
	writeHostFile(t, in, string(unsigned))
	keyFile := filepath.Join(f.root, "registration.key")
	writeHostFile(t, keyFile, base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))+"\n")
	f.cfg.XattrCalibrationPath = filepath.Join(f.root, "reg", "resigned.json")
	if out, err := adminRun("product", "calibration", "sign", "--key", keyFile, "--public-key", f.cfg.RegistrationPublicKeyPath,
		"--in", in, "--out", f.cfg.XattrCalibrationPath); err != nil {
		t.Fatalf("sign: %v\n%s", err, out)
	}
	opts := f.options()
	opts.Host = newProductServiceHostProfile(f.cfg)
	svc, err := startProductService(f.cfg, opts)
	if err != nil {
		t.Fatalf("the service refused the signed calibration: %v", err)
	}
	_ = svc.Shutdown()
}
