//go:build linux

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/releasetrust"
)

var adminHostInputNames = []string{"input policy alpha", "input policy beta", "input git", "input calibration", "input calibration signature", "input registration public key"}

const adminHostIdentityName = "input host identity"

func (h *adminHost) policyFile(project string) string {
	return h.path(productsetup.ConfigDir + "/policy-" + project + ".json")
}
func (h *adminHost) calibrationFile() string {
	return h.path(productsetup.ConfigDir + "/xattr-calibration.json")
}
func (h *adminHost) gitFile() string { return h.path(productsetup.BinaryDir + "/git") }

// reregister signs a new registration whose projects commit to policyDigest and uses it for the next setup.
func (h *adminHost) reregister(policyDigest string) {
	h.t.Helper()
	template := filepath.Join(h.dir, "template-2.json")
	mustAdmin(h.t, adminInitArgsFor(template, policyDigest, h.gitDigest)...)
	h.reg = filepath.Join(h.dir, "registration-2.json")
	mustAdmin(h.t, "product", "registration", "sign", "--registration", template, "--key", filepath.Join(h.keys, "registration.key"), "--out", h.reg)
	h.sig = h.reg + ".sig"
}

// resignRegistration signs the original template after edit and uses it for the next setup. When setup
// already installed a registration, that one is replaced too, so doctor reads the edit.
func (h *adminHost) resignRegistration(edit func(template string) string) {
	h.t.Helper()
	template := filepath.Join(h.dir, "template-3.json")
	writeHostFile(h.t, template, edit(readHostFile(h.t, filepath.Join(h.dir, "template.json"))))
	h.reg = filepath.Join(h.dir, "registration-3.json")
	mustAdmin(h.t, "product", "registration", "sign", "--registration", template, "--key", filepath.Join(h.keys, "registration.key"), "--out", h.reg)
	h.sig = h.reg + ".sig"
	if _, err := os.Lstat(h.path(productsetup.RegistrationPath)); err == nil {
		writeHostFile(h.t, h.path(productsetup.RegistrationPath), readHostFile(h.t, h.reg))
		writeHostFile(h.t, h.path(productsetup.RegistrationSignaturePath), readHostFile(h.t, h.sig))
	}
}

// resignCalibration replaces the installed calibration pair with one signed by key.
func (h *adminHost) resignCalibration(calibration piruntime.D09XattrEnumerationCalibration, key ed25519.PrivateKey) {
	h.t.Helper()
	encoded, signature, _, err := piruntime.SignD09XattrEnumerationCalibration(calibration, key)
	if err != nil {
		h.t.Fatal(err)
	}
	writeHostFile(h.t, h.calibrationFile(), string(encoded))
	writeHostFile(h.t, h.calibrationFile()+".sig", base64.StdEncoding.EncodeToString(signature)+"\n")
}

func (h *adminHost) registrationKey() ed25519.PrivateKey {
	h.t.Helper()
	key, err := releasetrust.ReadPrivateKey(filepath.Join(h.keys, "registration.key"))
	if err != nil {
		h.t.Fatal(err)
	}
	return key
}

func chmodOrFatal(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// adminBreakage damages one placed host input. input is the check that must name it and want the reason.
// setupOnly marks damage that lives in what setup is given, not in what it installs, so doctor cannot see it.
type adminBreakage struct {
	input, want string
	setupOnly   bool
	damage      func(t *testing.T, h *adminHost)
}

func adminBreakages() map[string]adminBreakage {
	protected := "protected bounded regular file"
	return map[string]adminBreakage{
		"policy group-writable": {"input policy alpha", protected, false, func(t *testing.T, h *adminHost) { chmodOrFatal(t, h.policyFile("alpha"), 0o664) }},
		"policy a symlink": {"input policy alpha", "read the gate policy", false, func(t *testing.T, h *adminHost) {
			if err := os.Rename(h.policyFile("alpha"), h.policyFile("alpha")+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(h.policyFile("alpha")+".real", h.policyFile("alpha")); err != nil {
				t.Fatal(err)
			}
		}},
		"policy missing": {"input policy beta", "read the gate policy", false, func(t *testing.T, h *adminHost) {
			if err := os.Remove(h.policyFile("beta")); err != nil {
				t.Fatal(err)
			}
		}},
		"policy unparsable": {"input policy alpha", "parse the gate policy", false, func(t *testing.T, h *adminHost) { writeHostFile(t, h.policyFile("alpha"), "not json") }},
		"policy digest differs from the registration": {"input policy beta", "differs from its registration", false, func(t *testing.T, h *adminHost) {
			writeHostFile(t, h.policyFile("beta"), `{"version":"tbound-policy/v1","rules":[]}`)
		}},
		"policy allows bash without a contained runner": {"input policy alpha", "allows bash", true, func(t *testing.T, h *adminHost) {
			_, bash := adminGatePolicy(true)
			writeHostFile(t, h.policyFile("alpha"), string(bash))
			writeHostFile(t, h.policyFile("beta"), string(bash))
			h.reregister(adminPolicyDigest(true))
		}},
		"git digest differs": {"input git", "pin approved Git executable", true, func(t *testing.T, h *adminHost) { h.gitDigest = digestBytes([]byte("another git")) }},
		"git digest differs from the registration": {"input git", "differs from the registered git_digest", false, func(t *testing.T, h *adminHost) {
			h.resignRegistration(func(raw string) string { return strings.Replace(raw, h.gitDigest, digestBytes([]byte("another git")), 1) })
		}},
		"host identity differs from the registration": {adminHostIdentityName, "differs from the registered host_identity_digest", false, func(t *testing.T, h *adminHost) {
			h.resignRegistration(func(raw string) string {
				return regexp.MustCompile(`"host_identity_digest": "sha256:[0-9a-f]{64}"`).ReplaceAllString(raw, `"host_identity_digest": "`+digestBytes([]byte("an older build"))+`"`)
			})
		}},
		"git bytes altered": {"input git", "pin approved Git executable", false, func(t *testing.T, h *adminHost) {
			f, err := os.OpenFile(h.gitFile(), os.O_APPEND|os.O_WRONLY, 0)
			if err == nil {
				_, err = f.WriteString("tampered")
				err = errors.Join(err, f.Close())
			}
			if err != nil {
				t.Fatal(err)
			}
		}},
		"git writable by others": {"input git", "pin approved Git executable", false, func(t *testing.T, h *adminHost) { chmodOrFatal(t, h.gitFile(), 0o777) }},
		"calibration missing": {"input calibration", "read the xattr calibration", false, func(t *testing.T, h *adminHost) {
			if err := os.Remove(h.calibrationFile()); err != nil {
				t.Fatal(err)
			}
		}},
		"calibration group-writable": {"input calibration", protected, false, func(t *testing.T, h *adminHost) { chmodOrFatal(t, h.calibrationFile(), 0o664) }},
		"calibration bytes altered": {"input calibration", "verify the xattr calibration", false, func(t *testing.T, h *adminHost) {
			writeHostFile(t, h.calibrationFile(), strings.Replace(readHostFile(t, h.calibrationFile()), `"probe_entry_count":1`, `"probe_entry_count":2`, 1))
		}},
		"calibration for read-only roots": {"input calibration", "managed writable roots", false, func(t *testing.T, h *adminHost) {
			h.resignCalibration(unsignedProductCalibration(piruntime.D09XattrReadonlySourceProfile), h.registrationKey())
		}},
		"calibration signature wrong": {"input calibration signature", "verify the xattr calibration", false, func(t *testing.T, h *adminHost) {
			writeHostFile(t, h.calibrationFile()+".sig", base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))+"\n")
		}},
		"calibration signed by another key": {"input calibration signature", "verify the xattr calibration", false, func(t *testing.T, h *adminHost) {
			_, other, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			h.resignCalibration(unsignedProductCalibration(piruntime.D09XattrManagedRWSourceProfile), other)
		}},
		"calibration signature missing": {"input calibration signature", "read the xattr calibration signature", false, func(t *testing.T, h *adminHost) {
			if err := os.Remove(h.calibrationFile() + ".sig"); err != nil {
				t.Fatal(err)
			}
		}},
		"calibration signature group-writable": {"input calibration signature", protected, false, func(t *testing.T, h *adminHost) {
			chmodOrFatal(t, h.calibrationFile()+".sig", 0o664)
		}},
	}
}

// The setup that is about to install the registration public key refuses every bad host input by name,
// before the first write, and a dry run reports the same refusal.
func TestProductAdminSetupRefusesEachBadHostInputBeforeAnyWrite(t *testing.T) {
	for name, breakage := range adminBreakages() {
		t.Run(name, func(t *testing.T) {
			h := newAdminHost(t, privateGovernedTestRoot(t))
			breakage.damage(t, h)
			before := adminTree(t, h.root)
			var messages []string
			for _, extra := range [][]string{nil, {"--dry-run"}} {
				out, err := h.setup(extra...)
				if adminExit(err) != 2 || !strings.Contains(err.Error(), breakage.input+": ") || !strings.Contains(err.Error(), breakage.want) ||
					!strings.Contains(err.Error(), "nothing was changed") {
					t.Fatalf("setup %v: want exit 2 refusing %q for %q, got exit %d: %v\n%s", extra, breakage.input, breakage.want, adminExit(err), err, out)
				}
				for _, other := range adminHostInputNames {
					bothProjectsAreBash := strings.Contains(breakage.want, "allows bash") // the registration gives both projects one policy digest
					if !bothProjectsAreBash && other != breakage.input && strings.Contains(err.Error(), other+": ") && !strings.HasPrefix(breakage.input, other) {
						t.Errorf("setup %v blamed %q for damage to %q: %v", extra, other, breakage.input, err)
					}
				}
				if after := adminTree(t, h.root); after != before {
					t.Fatalf("setup %v wrote despite the refusal:\nbefore:\n%s\nafter:\n%s", extra, before, after)
				}
				messages = append(messages, err.Error())
			}
			if messages[0] != messages[1] {
				t.Errorf("dry run reports another refusal:\n%s\n%s", messages[0], messages[1])
			}
		})
	}
}

// inputLines are the report lines of the host inputs, keyed by check name. The longest name wins, so the
// calibration signature is not mistaken for the calibration.
func inputLines(out string) map[string]string {
	names := slices.SortedFunc(slices.Values(append(slices.Clone(adminHostInputNames), adminHostIdentityName)), func(a, b string) int { return len(b) - len(a) })
	lines := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		_, afterStatus, found := strings.Cut(line, "] ")
		if !strings.HasPrefix(line, "[") || !found {
			continue
		}
		for _, name := range names {
			if strings.HasPrefix(afterStatus, name+" ") {
				lines[name] = line
				break
			}
		}
	}
	return lines
}

var inputLineShape = regexp.MustCompile(`uid \d+ mode 0[0-7]{3} (digest|fingerprint) [0-9a-f]{12,16}`)

func requireInputLines(t *testing.T, out string, status map[string]string) {
	t.Helper()
	lines := inputLines(out)
	for _, name := range adminHostInputNames {
		line, ok := lines[name]
		if !ok {
			t.Errorf("no report line for %q:\n%s", name, out)
			continue
		}
		want := status[name]
		if want == "" {
			want = "[PASS]"
		}
		if !strings.Contains(line, want) {
			t.Errorf("%q is not %s: %s", name, want, line)
		}
		if want == "[PASS]" && !inputLineShape.MatchString(line) {
			t.Errorf("%q does not show owner, mode and a digest prefix: %s", name, line)
		}
	}
}

func TestProductAdminSetupDryRunReportsEveryHostInput(t *testing.T) {
	h := newAdminHost(t, privateGovernedTestRoot(t))
	out, err := h.setup("--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	requireInputLines(t, out, nil)
	if line := inputLines(out)[adminHostIdentityName]; !strings.Contains(line, "[PASS]") {
		t.Fatalf("the compiled host identity is not reported as matching the registration: %q\n%s", line, out)
	}
	if !strings.Contains(out, h.path(productsetup.ConfigDir+"/policy-alpha.json")) || !strings.Contains(out, h.pub) {
		t.Fatalf("the report must name the inputs by path, including the key setup will install:\n%s", out)
	}
}

// doctor reads the installed setup record and reports one advisory line per input.
func TestProductAdminDoctorReportsEachHostInput(t *testing.T) {
	h := newAdminHost(t, privateGovernedTestRoot(t))
	if out, err := h.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	healthy, _ := adminRun("product", "doctor", "--root", h.root)
	requireInputLines(t, healthy, nil)
	if line := inputLines(healthy)[adminHostIdentityName]; !strings.Contains(line, "[PASS]") {
		t.Fatalf("the compiled host identity is not reported as matching the registration: %q\n%s", line, healthy)
	}
	if strings.Contains(healthy, strings.TrimSpace(readHostFile(t, filepath.Join(h.keys, "registration.key")))) {
		t.Fatal("the doctor printed key material")
	}
	for name, breakage := range adminBreakages() {
		if breakage.setupOnly {
			continue
		}
		t.Run(name, func(t *testing.T) {
			h := newAdminHost(t, privateGovernedTestRoot(t))
			if out, err := h.setup(); err != nil {
				t.Fatalf("setup: %v\n%s", err, out)
			}
			breakage.damage(t, h)
			out, err := adminRun("product", "doctor", "--root", h.root)
			if adminExit(err) != 1 {
				t.Fatalf("doctor exit %d, want 1: %v", adminExit(err), err)
			}
			line, ok := inputLines(out)[breakage.input]
			if !ok || !strings.Contains(line, "[FAIL]") || !strings.Contains(line, breakage.want) {
				t.Fatalf("doctor does not report %q failing with %q:\n%s", breakage.input, breakage.want, out)
			}
		})
	}
}
