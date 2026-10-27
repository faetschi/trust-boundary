//go:build linux

package productsetup

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/releasetrust"
)

// sentinelScript is a candidate that records, in the file it names, that it ran.
func sentinelScript(sentinel, comment string) string {
	return "#!/bin/sh\ntouch '" + sentinel + "'\n# " + comment + "\n"
}

// executingSelfTest makes the self-test run the installed binary, as the real one does, and counts the runs.
func (f *fixture) executingSelfTest() *int {
	runs := new(int)
	f.env.SelfTestHook = func() error {
		*runs++
		return exec.Command(f.path(BinaryPath)).Run()
	}
	return runs
}

func TestSetupPinsThePublisherKeyAndDoctorShowsItsFingerprint(t *testing.T) {
	f := newFixture(t)
	f.installed()
	source, err := os.ReadFile(f.cfg.ReleasePublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if f.read(ReleasePublicKeyPath) != string(source) || f.mode(ReleasePublicKeyPath) != 0o644 {
		t.Fatalf("the pinned key is %q (%04o), want the config's key at 0644", f.read(ReleasePublicKeyPath), f.mode(ReleasePublicKeyPath))
	}
	if strings.Contains(f.read(SetupConfigPath), "release_public_key") || strings.Contains(f.read(SetupConfigPath), f.inputs) {
		t.Fatalf("the installed record carries the key's source path:\n%s", f.read(SetupConfigPath))
	}
	if rep, err := f.setup(); err != nil || rep.Changed() {
		t.Fatalf("a rerun changed the pinned key: %v\n%s", err, rep)
	}
	pub := f.releaseKey.Public().(ed25519.PublicKey)
	var found bool
	for _, c := range Doctor(f.env, false).Checks {
		if c.Name == "release key" {
			found = true
			if c.Status != Pass || !strings.Contains(c.Detail, releasetrust.Fingerprint(pub)) {
				t.Fatalf("doctor does not show the pinned key's fingerprint: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("doctor has no release key check")
	}
}

func TestSetupRefusesAPublisherKeyThatIsNotAnEd25519KeyBeforeChangingAnything(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.cfg.ReleasePublicKey, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := f.setup()
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "publisher") {
		t.Fatalf("a malformed publisher key was accepted: %v", err)
	}
	if entries, _ := os.ReadDir(f.root); len(entries) != 0 || len(rep.Commands) != 0 {
		t.Fatalf("a refused setup changed the host: %v %v", entries, rep.Commands)
	}
}

func TestDoctorWarnsWhenNoPublisherKeyIsPinned(t *testing.T) {
	f := newFixture(t)
	f.cfg.ReleasePublicKey = ""
	f.installed()
	for _, c := range Doctor(f.env, false).Checks {
		if c.Name == "release key" {
			if c.Status != Warn || !strings.Contains(c.Detail, "update") {
				t.Fatalf("doctor must warn that update is refused without a pinned key: %+v", c)
			}
			return
		}
	}
	t.Fatal("doctor has no release key check")
}

func TestUpdateAuthenticatesTheCandidateBeforeStagingInstallingOrExecutingIt(t *testing.T) {
	otherKey := func() ed25519.PrivateKey {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}()
	type candidateFor func(f *fixture, sentinel string) Candidate
	signed := func(f *fixture, sentinel string) (string, Candidate) {
		path := filepath.Join(f.inputs, "tbound-new")
		if err := os.WriteFile(path, []byte(sentinelScript(sentinel, "genuine")), 0o755); err != nil {
			t.Fatal(err)
		}
		return path, f.sign(path)
	}
	cases := map[string]struct {
		pinned    bool
		candidate candidateFor
		want      string
	}{
		"no manifest and no signature": {true, func(f *fixture, sentinel string) Candidate {
			path, _ := signed(f, sentinel)
			return Candidate{Binary: path, Manifest: filepath.Join(f.inputs, "absent.json"), Signature: filepath.Join(f.inputs, "absent.json.sig")}
		}, "manifest"},
		"a signature that is not the publisher's": {true, func(f *fixture, sentinel string) Candidate {
			_, c := signed(f, sentinel)
			if err := os.WriteFile(c.Signature, []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return c
		}, "signature"},
		"a manifest signed by another key": {true, func(f *fixture, sentinel string) Candidate {
			path, _ := signed(f, sentinel)
			return f.signWith(path, otherKey)
		}, "signature"},
		"a binary whose digest differs from the manifest": {true, func(f *fixture, sentinel string) Candidate {
			path, c := signed(f, sentinel)
			if err := os.WriteFile(path, []byte(sentinelScript(sentinel, "forgery")), 0o755); err != nil { // same size
				t.Fatal(err)
			}
			return c
		}, "manifest"},
		"a binary whose size differs from the manifest": {true, func(f *fixture, sentinel string) Candidate {
			path, c := signed(f, sentinel)
			if err := os.WriteFile(path, []byte(sentinelScript(sentinel, "tampered, longer")), 0o755); err != nil {
				t.Fatal(err)
			}
			return c
		}, "manifest"},
		"a binary the manifest does not list": {true, func(f *fixture, sentinel string) Candidate {
			path, c := signed(f, sentinel)
			other := filepath.Join(f.inputs, "tbound-other")
			if err := os.Rename(path, other); err != nil {
				t.Fatal(err)
			}
			c.Binary = other
			return c
		}, "not listed"},
		"no pinned publisher key": {false, func(f *fixture, sentinel string) Candidate {
			_, c := signed(f, sentinel)
			return c
		}, "no publisher key is pinned"},
	}
	for name, tc := range cases {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, dry run %v", name, dryRun), func(t *testing.T) {
				f := newFixture(t)
				if !tc.pinned {
					f.cfg.ReleasePublicKey = ""
				}
				f.installed()
				f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
				runs := f.executingSelfTest()
				f.env.DryRun = dryRun
				sentinel := filepath.Join(f.inputs, "executed")
				rep, err := Update(f.env, tc.candidate(f, sentinel))
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("the candidate was not refused as %q: %v", tc.want, err)
				}
				if got := systemctlCommands(rep.Commands); len(got) != 0 {
					t.Fatalf("a refused candidate touched the service: %v", got)
				}
				for _, written := range []string{stagedBinaryPath, LastKnownGoodPath} {
					if _, err := os.Lstat(f.path(written)); err == nil {
						t.Fatalf("a refused candidate left %s", written)
					}
				}
				if f.read(BinaryPath) != "binary v1" || *runs != 0 {
					t.Fatalf("a refused candidate was installed or executed: %q, %d self-tests", f.read(BinaryPath), *runs)
				}
				if _, err := os.Lstat(sentinel); err == nil {
					t.Fatal("the candidate was executed")
				}
			})
		}
	}
}

func TestUpdateDryRunAuthenticatesAGenuineCandidateAndInstallsAndExecutesNothing(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	runs := f.executingSelfTest()
	f.env.DryRun = true
	sentinel := filepath.Join(f.inputs, "executed")
	path := filepath.Join(f.inputs, "tbound-new")
	if err := os.WriteFile(path, []byte(sentinelScript(sentinel, "genuine")), 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := Update(f.env, f.sign(path))
	if err != nil || !rep.DryRun {
		t.Fatalf("a genuine candidate was refused by a dry run: %v", err)
	}
	if _, err := os.Lstat(f.path(stagedBinaryPath)); err == nil || f.read(BinaryPath) != "binary v1" || *runs != 0 {
		t.Fatalf("a dry run staged, installed or executed the candidate (%d self-tests)", *runs)
	}
	if _, err := os.Lstat(sentinel); err == nil {
		t.Fatal("a dry run executed the candidate")
	}
}

func TestUpdateInstallsAndExecutesOnlyTheBytesItVerified(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	runs := f.executingSelfTest()
	sentinel := filepath.Join(f.inputs, "executed")
	genuine := "#!/bin/sh\nexit 0\n"
	candidate := f.newBinary(genuine)

	// After the candidate was verified, the file it came from is replaced by one that records its execution.
	hooked := 0
	interruptHook = func(step string) {
		if step != "verified" {
			return
		}
		hooked++
		if err := os.WriteFile(candidate.Binary, []byte(sentinelScript(sentinel, "swapped")), 0o755); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { interruptHook = nil })
	if _, err := Update(f.env, candidate); err != nil {
		t.Fatalf("a genuine candidate failed: %v", err)
	}
	if hooked != 1 {
		t.Fatalf("the update did not report the verified step (%d)", hooked)
	}
	if f.read(BinaryPath) != genuine || *runs != 1 {
		t.Fatalf("the installed binary is %q after %d self-tests, want the verified bytes", f.read(BinaryPath), *runs)
	}
	if _, err := os.Lstat(sentinel); err == nil {
		t.Fatal("the swapped file was executed")
	}
}

func TestUpdateStagesAtMostTheDeclaredSizePlusOneByteOfASourceThatGrew(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	runs := f.executingSelfTest()
	candidate := f.newBinary("#!/bin/sh\nexit 0\n")
	declared := int64(len("#!/bin/sh\nexit 0\n"))
	// The file keeps growing after the manifest was signed: an unbounded copy would stage all of it.
	if err := os.WriteFile(candidate.Binary, []byte("#!/bin/sh\nexit 0\n"+strings.Repeat("# padding\n", 1<<16)), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := int64(-1)
	interruptHook = func(step string) {
		if info, err := os.Lstat(f.path(stagedBinaryPath)); step == "staged" && err == nil {
			staged = info.Size()
		}
	}
	t.Cleanup(func() { interruptHook = nil })
	_, err := Update(f.env, candidate)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("a source larger than the manifest declares was not refused: %v", err)
	}
	if staged != declared+1 {
		t.Fatalf("the staged copy is %d bytes, want the declared %d plus one", staged, declared)
	}
	if f.read(BinaryPath) != "binary v1" || *runs != 0 {
		t.Fatalf("the refused candidate was installed or executed: %q, %d self-tests", f.read(BinaryPath), *runs)
	}
}
