//go:build linux

package productsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// bundleImageDigest is the image digest the bundle tests' registration commits to.
var bundleImageDigest = "sha256:" + strings.Repeat("c", 64)

// bundleArtifacts maps each installed command artifact to its content. A bundle holds each file under the
// basename of its installed path, so the bundle and the install cannot drift apart.
var bundleArtifacts = map[string]string{
	CommandHostProfilePath:          "profile\n",
	CommandHostProfileSignaturePath: "profile signature\n",
	CommandCellTemplatePath:         "template\n",
	CommandSeccompPath:              "seccomp\n",
	CommandCosignPublicKeyPath:      "cosign\n",
}

// writeBundleAt writes the five public artifacts into dir, creating it, and returns dir.
func (f *fixture) writeBundleAt(dir string) string {
	f.t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for installed, content := range bundleArtifacts {
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(installed)), []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return dir
}

// bundleRegistration makes the fixture's registration verifier name the image digest the bundle is built for.
func (f *fixture) bundleRegistration() {
	f.env.VerifyRegistration = func(reg, sig, key []byte) (RegistrationSummary, error) {
		summary, err := contentDigestRegistration(reg, sig, key)
		summary.ImageDigest = bundleImageDigest
		return summary, err
	}
}

// commandInputSpy is a ValidateCommandInputs seam. It records each call and whether the root was empty then.
type commandInputSpy struct {
	err       error
	calls     []HostInputs
	rootEmpty []bool
}

func (s *commandInputSpy) seam(f *fixture) func(HostInputs) error {
	return func(in HostInputs) error {
		s.calls = append(s.calls, in)
		entries, _ := os.ReadDir(f.root)
		s.rootEmpty = append(s.rootEmpty, len(entries) == 0)
		return s.err
	}
}

// findNamedCheck returns the first check with the given name.
func findNamedCheck(checks []Check, name string) (Check, bool) {
	i := slices.IndexFunc(checks, func(c Check) bool { return c.Name == name })
	if i < 0 {
		return Check{}, false
	}
	return checks[i], true
}

func TestProductBashFirstInstallAndImageReplacementHaveNoBootstrapCycle(t *testing.T) {
	t.Run("the candidate hook sees the proposed bytes and image digest before any write", func(t *testing.T) {
		f := newFixture(t)
		f.bundleRegistration()
		bundle := f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
		f.cfg.CommandBundle = bundle
		spy := &commandInputSpy{}
		f.env.ValidateCommandInputs = spy.seam(f)
		if _, err := f.setup(); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if len(spy.calls) != 1 || !spy.rootEmpty[0] {
			t.Fatalf("the hook must run once, before the root is written: %d calls, root empty %v", len(spy.calls), spy.rootEmpty)
		}
		in := spy.calls[0]
		if in.Root != f.root || in.CommandBundlePath != bundle || in.Registration.ImageDigest != bundleImageDigest {
			t.Fatalf("root %q, bundle %q or image digest %q is wrong", in.Root, in.CommandBundlePath, in.Registration.ImageDigest)
		}
		if string(in.RegistrationData) != "projects: alpha,beta" || string(in.RegistrationSignature) != "sig\n" || string(in.PublicKey) != "pub\n" {
			t.Fatalf("the hook must see the proposed registration bytes: %q %q %q", in.RegistrationData, in.RegistrationSignature, in.PublicKey)
		}
		if !bytes.Equal(in.Unit, RenderUnit()) || !bytes.Equal(in.ContainersConfig, RenderProductContainersConf()) || !bytes.Equal(in.StorageConfig, RenderProductStorageConf()) {
			t.Fatal("the hook must see the freshly rendered unit and Podman configurations")
		}
		if !strings.Contains(string(in.ServiceConfig), `"schema_version": "`+ServiceConfigSchema+`"`) {
			t.Fatalf("the hook must see the rendered service configuration:\n%s", in.ServiceConfig)
		}
	})

	t.Run("a refusing hook stops the first install in setup and in its dry run, with nothing written", func(t *testing.T) {
		for _, dry := range []bool{false, true} {
			f := newFixture(t)
			f.bundleRegistration()
			f.cfg.CommandBundle = f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
			spy := &commandInputSpy{err: errors.New("host-profile.json does not verify under the registration")}
			f.env.ValidateCommandInputs = spy.seam(f)
			f.env.DryRun = dry
			if _, err := f.setup(); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "does not verify under the registration") || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("dry run %v: a refusing hook did not refuse setup: %v", dry, err)
			}
			if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
				t.Fatalf("dry run %v: a refused setup wrote %v", dry, entries)
			}
		}
	})

	t.Run("a deny-only bootstrap calls the hook with no bundle, which decides, and installs no command file", func(t *testing.T) {
		f := newFixture(t)
		f.bundleRegistration()
		spy := &commandInputSpy{}
		f.env.ValidateCommandInputs = spy.seam(f)
		if _, err := f.setup(); err != nil {
			t.Fatalf("deny-only setup: %v", err)
		}
		if len(spy.calls) != 1 {
			t.Fatalf("the hook ran %d times, want once", len(spy.calls))
		}
		if spy.calls[0].CommandBundlePath != "" {
			t.Fatalf("a deny-only setup gave the hook a bundle path %q", spy.calls[0].CommandBundlePath)
		}
		for installed := range bundleArtifacts {
			if _, err := os.Lstat(f.path(installed)); err == nil {
				t.Fatalf("a deny-only setup installed %s", installed)
			}
		}
	})

	t.Run("a refusing hook also stops a bundle-less setup, so the hook is never bypassed", func(t *testing.T) {
		f := newFixture(t)
		f.bundleRegistration()
		f.env.ValidateCommandInputs = (&commandInputSpy{err: errors.New("Bash is allowed, but no bundle is configured")}).seam(f)
		if _, err := f.setup(); !errors.Is(err, ErrRefused) {
			t.Fatalf("a refusing hook did not stop a bundle-less setup: %v", err)
		}
		if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
			t.Fatalf("a refused setup wrote %v", entries)
		}
	})

	t.Run("a replacement that adds a bundle is validated from the proposed registration, and its dry run writes nothing", func(t *testing.T) {
		f := newFixture(t)
		f.bundleRegistration()
		spy := &commandInputSpy{}
		f.env.ValidateCommandInputs = spy.seam(f)
		if _, err := f.setup(); err != nil {
			t.Fatalf("deny-only install: %v", err)
		}
		installedRegistration := f.read(RegistrationPath)
		proposedRegistration := "projects: alpha,beta\nrevision: 2\n"
		verify := f.env.VerifyRegistration
		proposedImage := "sha256:" + strings.Repeat("d", 64)
		f.env.VerifyRegistration = func(reg, sig, key []byte) (RegistrationSummary, error) {
			summary, err := verify(reg, sig, key)
			if string(reg) == proposedRegistration {
				summary.ImageDigest = proposedImage
			}
			return summary, err
		}
		if err := os.WriteFile(f.cfg.Registration, []byte(proposedRegistration), 0o600); err != nil {
			t.Fatal(err)
		}
		f.cfg.CommandBundle = f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
		f.env.Replace = true

		before := treeState(t, f.root)
		f.env.DryRun = true
		if _, err := f.setup(); err != nil {
			t.Fatalf("dry-run replacement: %v", err)
		}
		if after := treeState(t, f.root); after != before {
			t.Fatalf("a dry-run replacement changed the root:\nbefore:\n%safter:\n%s", before, after)
		}
		if len(spy.calls) != 2 {
			t.Fatalf("the hook ran %d times through the install and the dry run, want 2", len(spy.calls))
		}
		dry := spy.calls[1]
		if dry.Registration.ImageDigest != proposedImage || dry.Registration.ImageDigest == spy.calls[0].Registration.ImageDigest {
			t.Fatal("replacement validated the previous image pin instead of the proposed image")
		}
		if string(dry.RegistrationData) != proposedRegistration || string(dry.RegistrationData) == installedRegistration || dry.CommandBundlePath != f.cfg.CommandBundle {
			t.Fatalf("the dry run must validate the proposed registration and bundle, not the installed ones: %q %q", dry.RegistrationData, dry.CommandBundlePath)
		}

		f.env.DryRun = false
		if _, err := f.setup(); err != nil {
			t.Fatalf("replacement: %v", err)
		}
		if got := f.read(RegistrationPath); got != proposedRegistration {
			t.Fatalf("the replacement installed registration %q", got)
		}
		for installed, content := range bundleArtifacts {
			if got := f.read(installed); got != content {
				t.Fatalf("%s is %q after the replacement, want %q", installed, got, content)
			}
		}
	})

	t.Run("a replacement that drops an installed bundle is refused and changes nothing", func(t *testing.T) {
		f := newFixture(t)
		f.bundleRegistration()
		f.cfg.CommandBundle = f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
		if _, err := f.setup(); err != nil {
			t.Fatalf("install with a bundle: %v", err)
		}
		before := treeState(t, f.root)
		f.cfg.CommandBundle = ""
		f.env.Replace = true
		if _, err := f.setup(); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "command bundle") {
			t.Fatalf("dropping the installed bundle was not refused: %v", err)
		}
		if after := treeState(t, f.root); after != before {
			t.Fatalf("a refused replacement changed the root:\nbefore:\n%safter:\n%s", before, after)
		}
	})
}

func TestCommandBundleInstallsOnlyItsFivePublicArtifactsAtFixedPaths(t *testing.T) {
	if got := filepath.Base(CommandHostProfileSignaturePath); got != "host-profile.json.sig" {
		t.Fatalf("the signature is installed as %s, want host-profile.json.sig", got)
	}
	f := newFixture(t)
	bundle := f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
	if err := os.WriteFile(filepath.Join(bundle, "cell-oci.tar"), []byte("provisioned later"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.cfg.CommandBundle = bundle
	if _, err := f.setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	for installed, content := range bundleArtifacts {
		if f.read(installed) != content || f.mode(installed) != 0o644 {
			t.Fatalf("%s is %q at %04o, want %q at 0644", installed, f.read(installed), f.mode(installed), content)
		}
	}
	entries, err := os.ReadDir(f.path(PodmanConfigDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"containers.conf", "cosign.pub", "d09-cell.json", "host-profile.json", "host-profile.json.sig", "seccomp.json", "storage.conf"}
	if !slices.Equal(names, want) {
		t.Fatalf("the Podman configuration directory holds %v, want only the bounded artifacts and configs %v", names, want)
	}
	if record := f.read(SetupConfigPath); strings.Contains(record, "command_bundle") || strings.Contains(record, bundle) {
		t.Fatalf("the installed record names the bundle source:\n%s", record)
	}
	rep, err := f.setup()
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	for _, s := range rep.Steps {
		if _, ok := bundleArtifacts[s.Path]; ok && s.Status != "unchanged" {
			t.Fatalf("a rerun reports %s %s, want unchanged", s.Status, s.Path)
		}
	}
}

func TestCommandBundleReplacementRefusesKeyRotationAcrossRetainedHistory(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		for _, changeRegistration := range []bool{false, true} {
			t.Run(fmt.Sprintf("dry=%v/changed-registration=%v", dryRun, changeRegistration), func(t *testing.T) {
				f := newFixture(t)
				f.bundleRegistration()
				f.cfg.CommandBundle = f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
				f.env.ValidateCommandInputs = (&commandInputSpy{}).seam(f)
				if _, err := f.setup(); err != nil {
					t.Fatal(err)
				}
				history := f.path(PodmanConfigDir + "/hosts/old-profile")
				if err := os.MkdirAll(history, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(history, "registration.pub"), []byte("pub\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if _, err := f.setup(); err != nil {
					t.Fatalf("retained history with the unchanged key: %v", err)
				}
				before := treeState(t, f.root)
				if err := os.WriteFile(f.cfg.RegistrationPublicKey, []byte("replacement pub\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.cfg.RegistrationSignature, []byte("replacement sig\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if changeRegistration {
					if err := os.WriteFile(f.cfg.Registration, []byte("projects: alpha,beta\nrevision: 2\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				f.env.Replace, f.env.DryRun = true, dryRun
				rep, err := f.setup()
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "signing-key rotation") || !strings.Contains(err.Error(), "nothing was changed") {
					t.Fatalf("retained history must refuse key rotation before setup: %v", err)
				}
				if after := treeState(t, f.root); after != before || len(rep.Commands) != 0 {
					t.Fatalf("refused replacement changed the installation or ran commands: %v", rep.Commands)
				}
			})
		}
	}
}

func TestCommandBundleSourceIsRootProtectedNonSymlinkAndNeverAManagedPath(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(t *testing.T, f *fixture)
		want   string
	}{
		"relative bundle": {func(t *testing.T, f *fixture) { f.cfg.CommandBundle = "bundle" }, "command_bundle"},
		"thesis bundle":   {func(t *testing.T, f *fixture) { f.cfg.CommandBundle = "/etc/tbound/bundle" }, "command_bundle"},
		"bundle inside the Podman configuration directory": {func(t *testing.T, f *fixture) { f.cfg.CommandBundle = PodmanConfigDir + "/bundle" }, "command_bundle"},
		"bundle inside a managed state tree":               {func(t *testing.T, f *fixture) { f.cfg.CommandBundle = StateDir + "/bundle" }, "collides"},
		"bundle directory is a symlink": {func(t *testing.T, f *fixture) {
			source := f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
			alias := filepath.Join(f.inputs, "alias")
			if err := os.Symlink(source, alias); err != nil {
				t.Fatal(err)
			}
			f.cfg.CommandBundle = alias
		}, "symlink"},
		"artifact is a symlink": {func(t *testing.T, f *fixture) {
			bundle := f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
			profile := filepath.Join(bundle, "host-profile.json")
			if err := os.Remove(profile); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f.cfg.Registration, profile); err != nil {
				t.Fatal(err)
			}
			f.cfg.CommandBundle = bundle
		}, "symlink"},
		"bundle inside the root is group-writable": {func(t *testing.T, f *fixture) {
			bundle := f.writeBundleAt(f.path("/bundle"))
			if err := os.Chmod(bundle, 0o775); err != nil {
				t.Fatal(err)
			}
			f.cfg.CommandBundle = bundle
		}, "/bundle is writable by group or others"},
		"an ancestor of a bundle inside the root is group-writable": {func(t *testing.T, f *fixture) {
			bundle := f.writeBundleAt(f.path("/drop/bundle"))
			if err := os.Chmod(f.path("/drop"), 0o777); err != nil {
				t.Fatal(err)
			}
			f.cfg.CommandBundle = bundle
		}, "/drop is writable by group or others"},
	} {
		for _, dry := range []bool{false, true} {
			label := "real run"
			if dry {
				label = "dry run"
			}
			t.Run(name+", "+label, func(t *testing.T) {
				f := newFixture(t)
				tc.mutate(t, f)
				before := treeState(t, f.root)
				f.env.DryRun = dry
				if _, err := f.setup(); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("setup was not refused with %q: %v", tc.want, err)
				}
				if after := treeState(t, f.root); after != before {
					t.Fatalf("a refused setup changed the root:\nbefore:\n%safter:\n%s", before, after)
				}
			})
		}
	}
}

func TestCommandBundleIsOptionalInTheConfigAndNeverRecordedInstalled(t *testing.T) {
	f := newFixture(t)
	bundle := f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
	f.cfg.CommandBundle = bundle
	raw, err := json.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(raw)
	if err != nil || cfg.CommandBundle != bundle {
		t.Fatalf("command_bundle did not parse: %v, %q", err, cfg.CommandBundle)
	}
	installed, err := cfg.installedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(installed, []byte("command_bundle")) || bytes.Contains(installed, []byte(bundle)) {
		t.Fatalf("the installed record carries the bundle source:\n%s", installed)
	}
	f.cfg.CommandBundle = ""
	if raw, err = json.Marshal(f.cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(raw); err != nil {
		t.Fatalf("a config without command_bundle must stay valid: %v", err)
	}
}

func TestCommandBundleArtifactPathsAreManagedSoHostInputsCannotCollideWithThem(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"policy is the command host profile":       func(c *Config) { c.GatePolicyPaths["alpha"] = CommandHostProfilePath },
		"calibration is the command cell template": func(c *Config) { c.XattrCalibrationPath = CommandCellTemplatePath },
		"policy is the seccomp profile":            func(c *Config) { c.GatePolicyPaths["beta"] = CommandSeccompPath },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			mutate(&f.cfg)
			if _, err := f.setup(); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "collides") {
				t.Fatalf("a host input on a command artifact was not refused as a collision: %v", err)
			}
			if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
				t.Fatalf("a refused setup wrote %v", entries)
			}
		})
	}
}

func TestDoctorChecksTheInstalledCommandInputsWithTheSameHookAndNeverPodman(t *testing.T) {
	f := newFixture(t)
	f.bundleRegistration()
	f.cfg.CommandBundle = f.writeBundleAt(filepath.Join(f.inputs, "bundle"))
	if _, err := f.setup(); err != nil {
		t.Fatalf("install with a bundle: %v", err)
	}
	spy := &commandInputSpy{}
	f.env.ValidateCommandInputs = spy.seam(f)
	var execs []string
	f.env.Exec = func(name string, args ...string) ([]byte, error) {
		execs = append(execs, strings.Join(append([]string{name}, args...), " "))
		return nil, nil
	}
	rep := Doctor(f.env, false)
	if len(spy.calls) != 1 {
		t.Fatalf("doctor ran the hook %d times, want once", len(spy.calls))
	}
	in := spy.calls[0]
	if in.Root != f.root || in.CommandBundlePath != f.path(PodmanConfigDir) || string(in.RegistrationData) != "projects: alpha,beta" ||
		!bytes.Equal(in.Unit, RenderUnit()) || string(in.ServiceConfig) != f.read(ServiceConfigPath) {
		t.Fatalf("doctor gave the hook the wrong inputs: root %q, path %q", in.Root, in.CommandBundlePath)
	}
	if check, ok := findNamedCheck(rep.Checks, "command bundle"); !ok || check.Status != Pass {
		t.Fatalf("doctor lacks a passing command bundle check:\n%s", rep)
	}
	spy.err = errors.New("seccomp.json does not match the template")
	rep = Doctor(f.env, false)
	if check, ok := findNamedCheck(rep.Checks, "command bundle"); !ok || check.Status != Fail || !strings.Contains(check.Detail, "does not match the template") || !rep.Failed() {
		t.Fatalf("a failing hook must fail doctor with its detail:\n%s", rep)
	}
	for _, call := range execs {
		if strings.HasPrefix(call, "podman") {
			t.Fatalf("doctor ran %q; the command check must stay static", call)
		}
	}
}

func TestDoctorDoesNotFailADenyOnlyInstallForTheAbsentCommandArtifacts(t *testing.T) {
	f := newFixture(t)
	f.bundleRegistration()
	if _, err := f.setup(); err != nil {
		t.Fatalf("deny-only install: %v", err)
	}
	rep := Doctor(f.env, false)
	for installed := range bundleArtifacts {
		if check, ok := findNamedCheck(rep.Checks, installed); ok && check.Status == Fail {
			t.Fatalf("doctor fails a deny-only install on the absent %s:\n%s", installed, rep)
		}
	}
}

func TestMissingCommandBundleCannotHideRetainedObligations(t *testing.T) {
	for _, path := range []string{PodmanConfigDir + "/store.json", PodmanConfigDir + "/hosts/old-profile", CommandOCILayoutPath + "/index.json"} {
		t.Run(path, func(t *testing.T) {
			f := newFixture(t)
			f.env.ValidateCommandInputs = (&commandInputSpy{}).seam(f)
			if _, err := f.setup(); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(f.path(path)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.path(path), []byte("retained"), 0644); err != nil {
				t.Fatal(err)
			}
			rep := Doctor(f.env, false)
			if check, ok := findNamedCheck(rep.Checks, "command bundle"); !ok || check.Status != Fail || !strings.Contains(check.Detail, "retained command obligations") {
				t.Fatalf("doctor ignored retained command obligations: %s", rep)
			}
			if _, err := f.setup(); !errors.Is(err, ErrRefused) {
				t.Fatalf("setup ignored retained command obligations: %v", err)
			}
		})
	}
}
