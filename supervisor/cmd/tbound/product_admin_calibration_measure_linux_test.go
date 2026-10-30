//go:build linux

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
)

// calibrationMeasureHost is a product host fixture plus the files the measurement command reads: the
// installed service config and a held probe corpus directory. The corpus measurement and the scanner are
// described offline by the two seams.
type calibrationMeasureHost struct {
	f       *productServiceFixture
	config  string
	corpus  string
	out     string
	digest  string
	scanner piruntime.D09XattrVisibilityMeasurements
}

func newCalibrationMeasureHost(t *testing.T) *calibrationMeasureHost {
	t.Helper()
	f := newProductHostFixture(t, false)
	h := &calibrationMeasureHost{f: f, config: filepath.Join(f.root, "service.json"), corpus: filepath.Join(f.root, "corpus"),
		out: filepath.Join(f.root, "measured.json"), digest: digestBytes([]byte("reviewed corpus"))}
	if err := os.Mkdir(h.corpus, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeHostFile(t, h.config, string(encoded))
	h.scanner, err = productHostMeasureXattr(nil)
	if err != nil {
		t.Fatal(err)
	}
	original := productCalibrationMeasure
	productCalibrationMeasure = func(_ *os.File, profile, digest string) (piruntime.D09XattrEnumerationCalibration, error) {
		calibration := unsignedProductCalibration(profile)
		calibration.ProbeCorpusDigest = digest
		return calibration, nil
	}
	t.Cleanup(func() { productCalibrationMeasure = original })
	return h
}

func (h *calibrationMeasureHost) args(extra ...string) []string {
	return append([]string{"product", "calibration", "measure", "--source", h.corpus, "--probe-corpus-digest", h.digest,
		"--out", h.out, "--config", h.config}, extra...)
}

func (h *calibrationMeasureHost) requireNoOutput(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(h.out); !os.IsNotExist(err) {
		t.Fatalf("a refused measurement left %s (%v)", h.out, err)
	}
	if temporaries, _ := filepath.Glob(filepath.Join(h.f.root, ".tbound-*")); len(temporaries) != 0 {
		t.Fatalf("a refused measurement left temporaries %v", temporaries)
	}
}

// The measurement is the unsigned artifact the signer reviews: the measured corpus under managed-rw,
// which the signing command and then product-serve accept.
func TestProductCalibrationMeasureWritesAnUnsignedMeasurementThatSignsAndStartsTheService(t *testing.T) {
	h := newCalibrationMeasureHost(t)
	out, err := adminRun(h.args()...)
	if err != nil {
		t.Fatalf("measure: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(h.out)
	if err != nil {
		t.Fatal(err)
	}
	measured, err := decodeProductCalibration(raw)
	if err != nil || measured.SourceAccessProfile != piruntime.D09XattrManagedRWSourceProfile || measured.ProbeCorpusDigest != h.digest || measured.Digest != "" {
		t.Fatalf("the output is not the unsigned managed-rw measurement: %+v %v", measured, err)
	}
	if info, err := os.Stat(h.out); err != nil || info.Mode().Perm()&0o022 != 0 {
		t.Fatalf("output mode: %v %v", info, err)
	}

	keyFile := filepath.Join(h.f.root, "registration.key")
	writeHostFile(t, keyFile, base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))+"\n")
	h.f.cfg.XattrCalibrationPath = filepath.Join(h.f.root, "reg", "measured-signed.json")
	if out, err := adminRun("product", "calibration", "sign", "--key", keyFile, "--public-key", h.f.cfg.RegistrationPublicKeyPath,
		"--in", h.out, "--out", h.f.cfg.XattrCalibrationPath); err != nil {
		t.Fatalf("sign: %v\n%s", err, out)
	}
	opts := h.f.options()
	opts.Host = newProductServiceHostProfile(h.f.cfg)
	svc, err := startProductService(h.f.cfg, opts)
	if err != nil {
		t.Fatalf("the service refused the measured, signed calibration: %v", err)
	}
	_ = svc.Shutdown()
}

func TestProductCalibrationMeasureRefusesARootWhoseScannerDiffers(t *testing.T) {
	for name, root := range map[string]func(h *calibrationMeasureHost) string{
		"state root":   func(h *calibrationMeasureHost) string { return h.f.stateRoot },
		"project root": func(h *calibrationMeasureHost) string { return h.f.projectDir },
	} {
		t.Run(name, func(t *testing.T) {
			h := newCalibrationMeasureHost(t)
			differs := root(h)
			productHostMeasureXattr = func(dir *os.File) (piruntime.D09XattrVisibilityMeasurements, error) {
				m := h.scanner
				if dir.Name() == differs {
					m.MountOptionsDigest = digestBytes([]byte("another mount"))
				}
				return m, nil
			}
			out, err := adminRun(h.args()...)
			if adminExit(err) != 2 || !strings.Contains(err.Error(), differs) || !strings.Contains(err.Error(), "differs from the probe corpus") {
				t.Fatalf("want exit 2 naming %s, got exit %d: %v\n%s", differs, adminExit(err), err, out)
			}
			h.requireNoOutput(t)
		})
	}
	t.Run("root measured read-only", func(t *testing.T) {
		h := newCalibrationMeasureHost(t)
		readOnly := h.scanner
		readOnly.ReadOnly = true
		productHostMeasureXattr = func(*os.File) (piruntime.D09XattrVisibilityMeasurements, error) { return readOnly, nil }
		if _, err := adminRun(h.args()...); adminExit(err) != 2 || !strings.Contains(err.Error(), "differs from the probe corpus") {
			t.Fatalf("a read-only root was accepted: %v", err)
		}
		h.requireNoOutput(t)
	})
}

func TestProductCalibrationMeasureRefusesBadArguments(t *testing.T) {
	h := newCalibrationMeasureHost(t)
	replace := func(flag, value string) []string {
		args := h.args()
		for i, a := range args {
			if a == flag {
				args[i+1] = value
			}
		}
		return args
	}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"empty corpus digest":      {replace("--probe-corpus-digest", ""), "invalid calibration command"},
		"invalid corpus digest":    {replace("--probe-corpus-digest", "sha256:short"), "probe-corpus-digest"},
		"uppercase corpus digest":  {replace("--probe-corpus-digest", "sha256:"+strings.Repeat("A", 64)), "probe-corpus-digest"},
		"missing corpus digest":    {append(append([]string{}, h.args()[:5]...), h.args()[7:]...), "invalid calibration command"},
		"relative source":          {replace("--source", "corpus"), "--source"},
		"empty source":             {replace("--source", ""), "invalid calibration command"},
		"missing source directory": {replace("--source", filepath.Join(h.f.root, "absent")), "cannot measure"},
		"missing out":              {replace("--out", ""), "invalid calibration command"},
		"missing service config":   {replace("--config", filepath.Join(h.f.root, "absent.json")), "service config"},
		"unknown access profile":   {append(h.args(), "--access-profile", "readonly-sealed-source/v1"), "invalid calibration command"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := adminRun(c.args...)
			if adminExit(err) != 2 || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want exit 2 naming %q, got exit %d: %v\n%s", c.want, adminExit(err), err, out)
			}
			h.requireNoOutput(t)
		})
	}
}

func TestProductCalibrationMeasureNeverOverwritesWithoutForce(t *testing.T) {
	h := newCalibrationMeasureHost(t)
	writeHostFile(t, h.out, "earlier measurement")
	if _, err := adminRun(h.args()...); adminExit(err) != 2 || readHostFile(t, h.out) != "earlier measurement" {
		t.Fatalf("an existing output was overwritten: %v", err)
	}
	if out, err := adminRun(h.args("--force")...); err != nil || readHostFile(t, h.out) == "earlier measurement" {
		t.Fatalf("--force did not replace the measurement: %v\n%s", err, out)
	}
	target := filepath.Join(h.f.root, "target.json")
	writeHostFile(t, target, "elsewhere")
	if err := os.Remove(h.out); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, h.out); err != nil {
		t.Fatal(err)
	}
	if _, err := adminRun(h.args("--force")...); adminExit(err) != 2 || readHostFile(t, target) != "elsewhere" {
		t.Fatalf("--force followed a symlink: %v", err)
	}
}

// Without the corpus seam the real measurement runs. A test process is neither the hardened service
// identity (NoNewPrivs, no capabilities, a probe corpus with trusted attributes) nor allowed to be root,
// so the real command refuses it and writes nothing.
func TestProductCalibrationMeasureRefusesAnUnhardenedScanner(t *testing.T) {
	h := newCalibrationMeasureHost(t)
	productCalibrationMeasure = piruntime.MeasureD09XattrEnumerationCalibration
	if out, err := adminRun(h.args()...); adminExit(err) != 2 {
		t.Fatalf("the real measurement accepted a test process: %v\n%s", err, out)
	}
	h.requireNoOutput(t)
}

// The offline seam is fenced by naming: no other non-test source may mention it, so no configuration or
// service path can reach it.
func TestProductCalibrationSeamIsNamedOnlyByItsOwnFile(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), "productCalibrationMeasure") && path != "product_admin_calibration_linux.go" {
			t.Errorf("%s names the offline test seam productCalibrationMeasure", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
