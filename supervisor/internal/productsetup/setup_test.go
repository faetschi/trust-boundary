//go:build linux

package productsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixture is a test root plus the admin's source files. The registration "verifier" accepts the text
// "projects: a,b" so these tests do not depend on registration crypto.
type fixture struct {
	t      *testing.T
	root   string
	inputs string
	cfg    Config
	binary string
	env    Env
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, root: t.TempDir(), inputs: t.TempDir()}
	write := func(name, content string) string {
		p := filepath.Join(f.inputs, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f.binary = write("tbound-v1", "binary v1")
	f.cfg = Config{
		SchemaVersion: ConfigSchema, WorkerMode: "supervisor-loop",
		Registration: write("registration.json", "projects: alpha,beta"), RegistrationSignature: write("registration.json.sig", "sig\n"),
		RegistrationPublicKey: write("registration.pub", "pub\n"),
		Projects:              map[string]string{"alpha": ProjectsDir + "/alpha", "beta": ProjectsDir + "/beta"},
		GatePolicyPaths:       map[string]string{"alpha": ConfigDir + "/policy-alpha.json", "beta": ConfigDir + "/policy-beta.json"},
		GitPath:               BinaryDir + "/git", GitDigest: "sha256:" + strings.Repeat("a", 64),
		XattrCalibrationPath: ConfigDir + "/xattr-calibration.json",
	}
	f.env = Env{
		Root: f.root, RequirePrereqs: true,
		Prereqs: func() []Check { return []Check{{Name: "systemd", Status: Pass, Detail: "present"}} },
		VerifyRegistration: func(reg, _, _ []byte) (RegistrationSummary, error) {
			list, ok := strings.CutPrefix(string(reg), "projects: ")
			if !ok {
				return RegistrationSummary{}, errors.New("bad registration")
			}
			return RegistrationSummary{ID: "reg-1", Digest: "sha256:abc", Projects: strings.Split(list, ","), Profiles: []string{"opencode-go-engineering"}, Principals: []int64{1000}}, nil
		},
	}
	return f
}

func (f *fixture) setup() (Report, error) { return Setup(f.env, f.cfg, f.binary) }

func (f *fixture) path(logical string) string { return filepath.Join(f.root, logical) }

func (f *fixture) mode(logical string) os.FileMode {
	f.t.Helper()
	info, err := os.Lstat(f.path(logical))
	if err != nil {
		f.t.Fatal(err)
	}
	return modeBits(info)
}

func (f *fixture) read(logical string) string {
	f.t.Helper()
	data, err := os.ReadFile(f.path(logical))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func TestSetupFreshRootBuildsTheExpectedTree(t *testing.T) {
	f := newFixture(t)
	rep, err := f.setup()
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, rep)
	}
	for _, n := range append(append([]node{}, directories...), installedFiles...) {
		if got := f.mode(n.Path); got != n.Mode {
			t.Errorf("%s mode = %04o, want %04o", n.Path, got, n.Mode)
		}
	}
	// Owners cannot be applied to a test root, so the plan records them.
	for _, want := range []string{
		"groupadd --system " + ServiceGroup,
		"useradd --system --gid " + ServiceGroup + " --home-dir " + StateDir + " --no-create-home --shell /usr/sbin/nologin " + ServiceUser,
		"chown root:root " + ConfigDir,
		"chown " + ServiceUser + ":" + ServiceGroup + " " + CredentialsDir,
		"chown " + ServiceUser + ":" + ServiceGroup + " " + StateDir,
		"chown root:root " + RegistrationPath,
		"chown root:root " + BinaryPath,
		"systemctl daemon-reload",
	} {
		if !slices.Contains(rep.Commands, want) {
			t.Errorf("recorded commands lack %q:\n%v", want, rep.Commands)
		}
	}
	if err := ValidateUnit([]byte(f.read(UnitPath))); err != nil {
		t.Fatalf("installed unit is invalid: %v", err)
	}
	for _, want := range []string{"User=tbound-product", "RuntimeDirectory=tbound-product", "ExecStart=" + BinaryPath + " product-serve --config " + ServiceConfigPath} {
		if !strings.Contains(f.read(UnitPath), want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	if !strings.Contains(f.read(ServiceConfigPath), `"socket_path": "`+SocketPath+`"`) || strings.Contains(f.read(SetupConfigPath), f.inputs) {
		t.Fatalf("service config or installed record is wrong:\n%s\n%s", f.read(ServiceConfigPath), f.read(SetupConfigPath))
	}
	if !strings.Contains(f.read("/etc/passwd"), ServiceUser+":x:") || !strings.Contains(f.read("/etc/group"), ServiceGroup+":x:") {
		t.Fatal("the test root's account files do not show the created accounts")
	}
}

func TestSetupRerunIsUnchanged(t *testing.T) {
	f := newFixture(t)
	if _, err := f.setup(); err != nil {
		t.Fatal(err)
	}
	rep, err := f.setup()
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if rep.Changed() || len(rep.Commands) != 0 {
		t.Fatalf("a rerun changed something or issued commands:\n%s", rep)
	}
	for _, s := range rep.Steps {
		if s.Status != "unchanged" {
			t.Errorf("step %s %s is %s", s.Kind, s.Path, s.Status)
		}
	}
	if strings.Count(f.read("/etc/passwd"), ServiceUser+":x:") != 1 {
		t.Fatal("the account was duplicated")
	}
}

func TestSetupRefusesAConflictingFileUnlessReplace(t *testing.T) {
	f := newFixture(t)
	if _, err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path(ServiceConfigPath), []byte("operator edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.path(RegistrationPublicKeyPath)); err != nil {
		t.Fatal(err)
	}
	_, err := f.setup()
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("a conflicting file was not refused with a --replace hint: %v", err)
	}
	if f.read(ServiceConfigPath) != "operator edit" {
		t.Fatal("the conflicting file was changed")
	}
	if _, err := os.Lstat(f.path(RegistrationPublicKeyPath)); err == nil {
		t.Fatal("a refusal still applied another step")
	}
	f.env.Replace = true
	if _, err := f.setup(); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if f.read(ServiceConfigPath) == "operator edit" {
		t.Fatal("--replace did not restore the file")
	}
}

func TestSetupRefusesThesisPathsAndChangesNothing(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"project in /var/lib/tbound":   func(c *Config) { c.Projects["alpha"] = "/var/lib/tbound/live/alpha" },
		"project in /etc/tbound":       func(c *Config) { c.Projects["alpha"] = "/etc/tbound/alpha" },
		"project in /run/tbound-pi":    func(c *Config) { c.Projects["alpha"] = "/run/tbound-pi/alpha" },
		"project in /run/tbound":       func(c *Config) { c.Projects["alpha"] = "/run/tbound/alpha" },
		"thesis key as registration":   func(c *Config) { c.RegistrationPublicKey = "/etc/tbound/pi-host-profile.pub" },
		"whole component, not prefix":  func(c *Config) { c.Projects["alpha"] = "/var/lib/tbound-other/alpha" },
		"project outside the projects": func(c *Config) { c.Projects["alpha"] = StateDir + "/alpha" },
		"unclean path":                 func(c *Config) { c.Projects["alpha"] = ProjectsDir + "/../alpha" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			mutate(&f.cfg)
			if _, err := f.setup(); !errors.Is(err, ErrRefused) {
				t.Fatalf("not refused: %v", err)
			}
			if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
				t.Fatalf("a refused setup changed the root: %v", entries)
			}
		})
	}
	t.Run("the unit", func(t *testing.T) {
		unit := strings.Replace(string(RenderUnit()), "ReadWritePaths=", "ReadWritePaths=/var/lib/tbound/live ", 1)
		if err := ValidateUnit([]byte(unit)); !errors.Is(err, ErrRefused) {
			t.Fatalf("a unit naming a thesis path was accepted: %v", err)
		}
	})
}

func TestSetupRequiresAndRecordsTheHostProfileInputs(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"no git":                 func(c *Config) { c.GitPath = "" },
		"no git digest":          func(c *Config) { c.GitDigest = "" },
		"no calibration":         func(c *Config) { c.XattrCalibrationPath = "" },
		"git outside the binary": func(c *Config) { c.GitPath = "/usr/bin/git" },
		"thesis calibration":     func(c *Config) { c.XattrCalibrationPath = "/etc/tbound/xattr.json" },
		"calibration elsewhere":  func(c *Config) { c.XattrCalibrationPath = StateDir + "/xattr.json" },
		"uppercase digest":       func(c *Config) { c.GitDigest = "sha256:" + strings.Repeat("A", 64) },
		"policy missing":         func(c *Config) { delete(c.GatePolicyPaths, "beta") },
		"policy for no project":  func(c *Config) { c.GatePolicyPaths["gamma"] = ConfigDir + "/policy-gamma.json" },
		"policy outside config":  func(c *Config) { c.GatePolicyPaths["alpha"] = StateDir + "/policy.json" },
		"thesis policy":          func(c *Config) { c.GatePolicyPaths["alpha"] = "/etc/tbound/policy.json" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			mutate(&f.cfg)
			if _, err := f.setup(); !errors.Is(err, ErrRefused) {
				t.Fatalf("not refused: %v", err)
			}
		})
	}
	f := newFixture(t)
	if _, err := f.setup(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"git_path": "` + BinaryDir + `/git"`, `"git_digest": "sha256:` + strings.Repeat("a", 64) + `"`,
		`"xattr_calibration_path": "` + ConfigDir + `/xattr-calibration.json"`, `"alpha": "` + ConfigDir + `/policy-alpha.json"`} {
		if !strings.Contains(f.read(ServiceConfigPath), want) {
			t.Errorf("service config lacks %s:\n%s", want, f.read(ServiceConfigPath))
		}
	}
}

func TestSetupDryRunWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.env.DryRun = true
	rep, err := f.setup()
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
		t.Fatalf("a dry run wrote %v", entries)
	}
	if !rep.Changed() || !slices.Contains(rep.Commands, "systemctl daemon-reload") || !strings.Contains(rep.String(), "dry run") {
		t.Fatalf("the dry run did not describe the plan:\n%s", rep)
	}
}

func TestSetupFailsOnAnUnsupportedHostBeforeAnyChange(t *testing.T) {
	f := newFixture(t)
	f.env.Prereqs = func() []Check {
		return []Check{{Name: "cgroup-v2", Status: Fail, Detail: "no delegation"}, {Name: "podman", Status: Pass}}
	}
	rep, err := f.setup()
	if !errors.Is(err, ErrUnsupportedHost) || !strings.Contains(err.Error(), "cgroup-v2") || !strings.Contains(err.Error(), "qualified VM") {
		t.Fatalf("setup did not fail on the unsupported host with the VM pointer: %v", err)
	}
	if entries, _ := os.ReadDir(f.root); len(entries) != 0 || len(rep.Commands) != 0 {
		t.Fatalf("the unsupported host was changed: %v %v", entries, rep.Commands)
	}
	f.env.RequirePrereqs = false // a test root only reports
	if rep, err := f.setup(); err != nil || len(rep.Checks) != 2 {
		t.Fatalf("an advisory run must proceed and report the checks: %v %v", err, rep.Checks)
	}
}

func TestSetupRefusesAccountsWithOtherIDs(t *testing.T) {
	for name, tc := range map[string]struct{ passwd, group string }{
		"primary group differs": {ServiceUser + ":x:950:951::/:/bin/false", ServiceGroup + ":x:950:"},
		"group missing":         {ServiceUser + ":x:950:950::/:/bin/false", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if err := os.MkdirAll(f.path("/etc"), 0o755); err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(f.path("/etc/passwd"), []byte(tc.passwd+"\n"), 0o644)
			_ = os.WriteFile(f.path("/etc/group"), []byte(tc.group+"\n"), 0o644)
			if _, err := f.setup(); !errors.Is(err, ErrRefused) {
				t.Fatalf("not refused: %v", err)
			}
			if _, err := os.Lstat(f.path(ConfigDir)); err == nil {
				t.Fatal("directories were created despite the refusal")
			}
		})
	}
	f := newFixture(t)
	f.cfg.ServiceUID, f.cfg.ServiceGID = 4242, 4242
	if _, err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.read("/etc/passwd"), ServiceUser+":x:4242:4242:") {
		t.Fatalf("service_uid/service_gid were not used:\n%s", f.read("/etc/passwd"))
	}
	f.cfg.ServiceUID, f.cfg.ServiceGID = 4243, 4243
	if _, err := f.setup(); !errors.Is(err, ErrRefused) {
		t.Fatalf("an account with other IDs than the config was accepted: %v", err)
	}
}

func TestSetupTightensAWeakenedModeAndRefusesSymlinks(t *testing.T) {
	f := newFixture(t)
	if _, err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.path(CredentialsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := f.setup()
	if err != nil || f.mode(CredentialsDir) != 0o700 || !rep.Changed() {
		t.Fatalf("a weakened mode was not restored: %v %04o", err, f.mode(CredentialsDir))
	}
	if err := os.RemoveAll(f.path(StateDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.inputs, f.path(StateDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.setup(); !errors.Is(err, ErrRefused) {
		t.Fatalf("a symlinked state directory was accepted: %v", err)
	}
}

func TestSetupRefusesAnUnverifiedOrMismatchedRegistration(t *testing.T) {
	f := newFixture(t)
	f.cfg.Projects = map[string]string{"alpha": ProjectsDir + "/alpha"}
	f.cfg.GatePolicyPaths = map[string]string{"alpha": ConfigDir + "/policy-alpha.json"}
	if _, err := f.setup(); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "exactly the registered projects") {
		t.Fatalf("a project set that differs from the registration was accepted: %v", err)
	}
	f = newFixture(t)
	if err := os.WriteFile(f.cfg.Registration, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.setup(); !errors.Is(err, ErrRefused) {
		t.Fatalf("a registration that does not verify was accepted: %v", err)
	}
	if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
		t.Fatalf("the root changed: %v", entries)
	}
}

type exitCode2 struct{}

func (exitCode2) Error() string { return "exit status 2" }
func (exitCode2) ExitCode() int { return 2 }

func TestSetupLiveRunsAccountsOwnersAndReloadInOrder(t *testing.T) {
	f := newFixture(t)
	var calls []string
	var chowns []string
	groupAdded, userAdded := false, false
	f.env.Live = true
	f.env.Exec = func(name string, args ...string) ([]byte, error) {
		call := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, call)
		switch {
		case call == "getent group "+ServiceGroup && groupAdded:
			return []byte(ServiceGroup + ":x:777:\n"), nil
		case call == "getent passwd "+ServiceUser && userAdded:
			return []byte(ServiceUser + ":x:888:777::/var/lib/tbound-product:/usr/sbin/nologin\n"), nil
		case strings.HasPrefix(call, "getent"):
			return nil, exitCode2{}
		case name == "groupadd":
			groupAdded = true
		case name == "useradd":
			userAdded = true
		}
		return nil, nil
	}
	f.env.Chown = func(path string, uid, gid int) error {
		path, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		chowns = append(chowns, fmt.Sprintf("%s %d:%d", strings.TrimPrefix(path, f.root), uid, gid))
		return nil
	}
	rep, err := f.setup()
	if err != nil {
		t.Fatalf("live setup: %v", err)
	}
	var order []string
	for _, c := range calls {
		if !strings.HasPrefix(c, "getent") {
			order = append(order, strings.Fields(c)[0])
		}
	}
	if strings.Join(order, " ") != "groupadd useradd systemctl" || calls[len(calls)-1] != "systemctl daemon-reload" {
		t.Fatalf("live commands out of order: %v", calls)
	}
	for _, want := range []string{ConfigDir + " 0:0", CredentialsDir + " 888:777", StateDir + " 888:777", BinaryPath + " 0:0"} {
		if !slices.Contains(chowns, want) {
			t.Errorf("live chowns lack %q: %v", want, chowns)
		}
	}
	if !strings.Contains(rep.String(), "commands (executed)") {
		t.Fatalf("the report must say the commands were executed:\n%s", rep)
	}
}

func TestSetupRefusesHostInputsThatCollideWithPathsItOrAnUpdateWrites(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"git is the installed binary":          func(c *Config) { c.GitPath = BinaryPath },
		"git is the last-known-good":           func(c *Config) { c.GitPath = LastKnownGoodPath },
		"git is the staged binary":             func(c *Config) { c.GitPath = stagedBinaryPath },
		"git is the rollback record":           func(c *Config) { c.GitPath = rollbackIntentPath },
		"git is the binary directory":          func(c *Config) { c.GitPath = BinaryDir },
		"calibration is the registration":      func(c *Config) { c.XattrCalibrationPath = RegistrationPath },
		"calibration is the service config":    func(c *Config) { c.XattrCalibrationPath = ServiceConfigPath },
		"calibration is the setup record":      func(c *Config) { c.XattrCalibrationPath = SetupConfigPath },
		"calibration is the registration key":  func(c *Config) { c.XattrCalibrationPath = RegistrationPublicKeyPath },
		"calibration is the config directory":  func(c *Config) { c.XattrCalibrationPath = ConfigDir },
		"calibration is a credential":          func(c *Config) { c.XattrCalibrationPath = CredentialsDir + "/opencode-go-engineering.key" },
		"policy is the registration signature": func(c *Config) { c.GatePolicyPaths["alpha"] = RegistrationSignaturePath },
		"policy is the setup record":           func(c *Config) { c.GatePolicyPaths["beta"] = SetupConfigPath },
		"policy is the credentials directory":  func(c *Config) { c.GatePolicyPaths["alpha"] = CredentialsDir },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			mutate(&f.cfg)
			if _, err := f.setup(); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "collides") {
				t.Fatalf("a host input on a managed path was not refused as a collision: %v", err)
			}
			if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
				t.Fatalf("a refused setup changed the root: %v", entries)
			}
		})
	}
	f := newFixture(t)
	f.cfg.GatePolicyPaths["beta"] = f.cfg.GatePolicyPaths["alpha"]
	if _, err := f.setup(); err != nil {
		t.Fatalf("two projects sharing one policy file are fine: %v", err)
	}
}
