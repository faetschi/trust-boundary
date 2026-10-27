//go:build linux

package productsetup

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runningExecutable is the binary that runs this test: the one setup may install without a signature.
func runningExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestSetupAuthenticatesAnExplicitBinaryUnderTheConfiguredPublisherKey(t *testing.T) {
	otherKey := func() ed25519.PrivateKey {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}()
	cases := map[string]struct {
		candidate func(f *fixture) Candidate
		want      string
	}{
		"no manifest and no signature": {func(f *fixture) Candidate { return Candidate{Binary: f.binary} }, "--manifest"},
		"a manifest signed by another key": {func(f *fixture) Candidate {
			return f.signWith(f.binary, otherKey)
		}, "signature"},
		"a binary that differs from the manifest": {func(f *fixture) Candidate {
			c := f.sign(f.binary)
			if err := os.WriteFile(f.binary, []byte("binary v9"), 0o755); err != nil { // same size
				t.Fatal(err)
			}
			return c
		}, "manifest"},
		"a binary the manifest does not list": {func(f *fixture) Candidate {
			c := f.sign(f.binary)
			other := filepath.Join(f.inputs, "tbound-other")
			if err := os.Rename(f.binary, other); err != nil {
				t.Fatal(err)
			}
			c.Binary = other
			return c
		}, "not listed"},
	}
	for name, tc := range cases {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, dry run %v", name, dryRun), func(t *testing.T) {
				f := newFixture(t)
				f.env.DryRun = dryRun
				rep, err := Setup(f.env, f.cfg, tc.candidate(f))
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("the unauthenticated binary was not refused as %q: %v", tc.want, err)
				}
				if entries, _ := os.ReadDir(f.root); len(entries) != 0 || len(rep.Commands) != 0 {
					t.Fatalf("a refused setup changed the host: %v %v", entries, rep.Commands)
				}
			})
		}
	}
}

func TestSetupInstallsASignedBinaryAndADryRunPlansItWithoutWriting(t *testing.T) {
	f := newFixture(t)
	f.env.DryRun = true
	rep, err := f.setup()
	if err != nil || !rep.DryRun {
		t.Fatalf("a signed binary was refused by a dry run: %v", err)
	}
	if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
		t.Fatalf("a dry run wrote %v", entries)
	}
	f.env.DryRun = false
	if _, err := f.setup(); err != nil || f.read(BinaryPath) != "binary v1" {
		t.Fatalf("a signed binary was not installed: %v", err)
	}
}

func TestSetupWithoutAPublisherKeyInstallsOnlyTheRunningExecutable(t *testing.T) {
	for name, candidate := range map[string]func(f *fixture) Candidate{
		"a foreign binary":                        func(f *fixture) Candidate { return Candidate{Binary: f.binary} },
		"a foreign binary with a signed manifest": func(f *fixture) Candidate { return f.sign(f.binary) },
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, dry run %v", name, dryRun), func(t *testing.T) {
				f := newFixture(t)
				f.cfg.ReleasePublicKey = ""
				f.env.DryRun = dryRun
				rep, err := Setup(f.env, f.cfg, candidate(f))
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "release_public_key") || !strings.Contains(err.Error(), "running tbound") {
					t.Fatalf("a foreign binary without a publisher key was not refused with the way out: %v", err)
				}
				if entries, _ := os.ReadDir(f.root); len(entries) != 0 || len(rep.Commands) != 0 {
					t.Fatalf("a refused setup changed the host: %v %v", entries, rep.Commands)
				}
			})
		}
	}

	f := newFixture(t)
	f.cfg.ReleasePublicKey = ""
	if _, err := Setup(f.env, f.cfg, Candidate{Binary: runningExecutable(t)}); err != nil {
		t.Fatalf("the running executable was refused: %v", err)
	}
	installed, err := os.Stat(f.path(BinaryPath))
	running, _ := os.Stat(runningExecutable(t))
	if err != nil || installed.Size() != running.Size() {
		t.Fatalf("the running executable was not installed: %v", err)
	}
}

func TestSetupWithAPublisherKeyStillInstallsTheRunningExecutableWithoutASignature(t *testing.T) {
	f := newFixture(t)
	if _, err := Setup(f.env, f.cfg, Candidate{Binary: runningExecutable(t)}); err != nil {
		t.Fatalf("the running executable was refused: %v", err)
	}
}

func TestSetupAuthenticatesAForeignBinaryUnderThePinnedKeyWhenTheConfigNamesNone(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.cfg.ReleasePublicKey = ""
	f.env.Replace = true
	if _, err := Setup(f.env, f.cfg, Candidate{Binary: f.binary}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "--manifest") {
		t.Fatalf("an unsigned binary was accepted although a publisher key is pinned: %v", err)
	}
	if err := os.WriteFile(f.binary, []byte("binary v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(f.env, f.cfg, f.sign(f.binary)); err != nil || f.read(BinaryPath) != "binary v2" {
		t.Fatalf("a binary signed under the pinned key was refused: %v", err)
	}
}
