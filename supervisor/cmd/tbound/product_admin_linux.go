//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

const productAdminUsage = `Usage (run setup, update, rollback and uninstall as root on the product host):
  tbound product registration keygen|init|sign|verify ...     (offline; see: tbound product registration help)
  tbound product calibration sign|measure ...                 (host input; see: tbound product calibration help)
  tbound product setup --config FILE [--root DIR] [--dry-run] [--replace] [--binary PATH]
  tbound product update --binary NEW --manifest FILE --signature FILE [--root DIR] [--dry-run]
  tbound product rollback [--root DIR] [--dry-run]
  tbound product uninstall [--root DIR] [--dry-run] [--purge] [--yes]
  tbound product doctor [--self-test] [--root DIR]
  tbound product turns [--root DIR]                        (plumbing: JSON list of unsettled catalog turns)
  tbound product self-test [--root DIR]                    (plumbing: the self-test turn as the calling UID, request on stdin)
--root DIR runs against a test root: files are written below DIR, but users, owners and systemctl
calls are only recorded, never executed. --dry-run writes and executes nothing and prints the plan.
setup is idempotent ("unchanged" on a rerun), refuses any thesis path, and fails before any change
on a host that does not meet the prerequisites (use the qualified VM option).
uninstall keeps /etc/tbound-product and /var/lib/tbound-product unless --purge is given; --purge
asks for confirmation unless --yes is also given.
doctor is advisory. Only --self-test (a synthetic turn on the project named by self_test in the
setup config) can report governed readiness. The turn runs as self_test.client_uid, a registered
client UID (never root): a root caller drops to it in a child process. Setup and update refuse a
self_test whose client_uid is missing or not an allowed principal of the registration.
Exit codes: 0 success, 2 refused or invalid usage, 1 other, 3 doctor --self-test: not configured (readiness
not established); update and rollback: the self-test failed and the previous binary was restored.
`

// Seams for tests: the host prerequisite checks and the confirmation prompt's input.
var (
	productAdminPrereqs                = hostPrerequisites
	productAdminStdin        io.Reader = os.Stdin
	productAdminLookupUser             = user.Lookup
	productAdminTurnsCommand           = func(executable string, uid, gid uint32) *exec.Cmd {
		cmd := exec.Command(executable, "product", "turns")
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
		return cmd
	}
)

func runProductAdmin(args []string, out, stderr io.Writer) error {
	usage := func() error {
		_, _ = io.WriteString(stderr, productAdminUsage)
		return adminRefusal("invalid product command or flags")
	}
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "help", "--help":
		_, err := io.WriteString(out, productAdminUsage)
		return err
	case "registration":
		return runProductRegistration(args[1:], out, stderr)
	case "calibration":
		return runProductCalibration(args[1:], out, stderr)
	case "setup", "update", "rollback", "uninstall", "doctor", "turns", "self-test":
	default:
		return usage()
	}
	flags := flag.NewFlagSet("product "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var configFile, binary, manifest, signature, root string
	var dryRun, replace, purge, yes, selfTest bool
	flags.StringVar(&root, "root", "/", "")
	flags.BoolVar(&dryRun, "dry-run", false, "")
	switch args[0] {
	case "setup":
		flags.StringVar(&configFile, "config", "", "")
		flags.StringVar(&binary, "binary", "", "")
		flags.BoolVar(&replace, "replace", false, "")
	case "update":
		flags.StringVar(&binary, "binary", "", "")
		flags.StringVar(&manifest, "manifest", "", "")
		flags.StringVar(&signature, "signature", "", "")
	case "uninstall":
		flags.BoolVar(&purge, "purge", false, "")
		flags.BoolVar(&yes, "yes", false, "")
	case "doctor":
		flags.BoolVar(&selfTest, "self-test", false, "")
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !filepath.IsAbs(root) {
		return usage()
	}
	root = filepath.Clean(root)
	if err := productsetup.ValidateRoot(root); err != nil {
		return err
	}
	live := root == "/" && !dryRun
	if live && args[0] != "doctor" && args[0] != "turns" && args[0] != "self-test" && os.Geteuid() != 0 {
		return adminRefusal("`tbound product %s` changes the host and must run as root (or use --dry-run, or --root DIR)", args[0])
	}
	env := productAdminEnv(root, dryRun, replace, yes, out, stderr)
	switch args[0] {
	case "setup":
		if configFile == "" {
			return usage()
		}
		return productAdminSetup(env, configFile, binary, out)
	case "update":
		if binary == "" || manifest == "" || signature == "" {
			return usage()
		}
		rep, err := productsetup.Update(env, productsetup.Candidate{Binary: binary, Manifest: manifest, Signature: signature})
		return productAdminFinish(out, rep, err, "updated")
	case "rollback":
		rep, err := productsetup.Rollback(env)
		return productAdminFinish(out, rep, err, "rolled back")
	case "uninstall":
		rep, err := productsetup.Uninstall(env, purge)
		return productAdminFinish(out, rep, err, "uninstalled")
	case "self-test":
		return productAdminSelfTestChild(root, productAdminStdin, out)
	case "turns":
		turns, err := productAdminTurns(root)()
		if err != nil {
			return err
		}
		if turns == nil {
			turns = []productsetup.OpenTurn{}
		}
		return json.NewEncoder(out).Encode(turns)
	default:
		return productAdminDoctor(env, selfTest, out)
	}
}

// productAdminEnv wires the real seams. A live run is on the real root and not a dry run.
func productAdminEnv(root string, dryRun, replace, yes bool, out, stderr io.Writer) productsetup.Env {
	live := root == "/" && !dryRun
	env := productsetup.Env{
		Root: root, Live: live, DryRun: dryRun, Replace: replace, RequirePrereqs: root == "/",
		Prereqs: productAdminPrereqs, VerifyRegistration: verifyRegistrationBytes,
		ValidateServiceConfig: validateServiceConfigBytes, CheckHostInputs: checkProductHostInputs, Turns: productAdminTurnsFor(root),
		RunSelfTest: productAdminRunSelfTest(root),
	}
	if live {
		env.SelfTestHook = productAdminSelfTestHook
	}
	if root == "/" { // a test root holds fixture binaries without the marker
		env.BinaryFormat = productsetup.ScanInstallationFormat
		env.CredentialStatus = productAdminCredentialStatus
	}
	if yes {
		env.Confirm = func(string) (bool, error) { return true, nil }
	} else {
		env.Confirm = func(prompt string) (bool, error) {
			fmt.Fprintf(stderr, "%s [type 'purge' to confirm]: ", prompt)
			line, err := bufio.NewReader(productAdminStdin).ReadString('\n')
			if err != nil && line == "" {
				return false, err
			}
			return strings.TrimSpace(line) == "purge", nil
		}
	}
	return env
}

// productAdminExit maps a productsetup error to the command's exit code.
func productAdminExit(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, productsetup.ErrRefused), errors.Is(err, productsetup.ErrUnsupportedHost):
		return &adminError{msg: err.Error(), code: 2}
	case errors.Is(err, productsetup.ErrSelfTest):
		return &adminError{msg: err.Error(), code: 3}
	}
	return err
}

func productAdminFinish(out io.Writer, rep productsetup.Report, err error, done string) error {
	_, _ = io.WriteString(out, rep.String())
	if err == nil {
		switch {
		case rep.DryRun:
		case rep.Changed():
			fmt.Fprintln(out, done)
		default:
			fmt.Fprintln(out, "unchanged")
		}
	}
	return productAdminExit(err)
}

func productAdminSetup(env productsetup.Env, configFile, binary string, out io.Writer) error {
	cfg, err := productsetup.LoadConfig(configFile)
	if err != nil {
		return productAdminExit(err)
	}
	if binary == "" {
		exe, err := os.Executable()
		if err == nil {
			binary, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			return fmt.Errorf("locate this binary to install it (or pass --binary): %w", err)
		}
	}
	rep, err := productsetup.Setup(env, cfg, binary)
	if err := productAdminFinish(out, rep, err, "changed"); err != nil {
		return err
	}
	if rep.Changed() && !rep.DryRun {
		fmt.Fprintf(out, "next: install the provider key (`tbound provider setup --profile ID --service-user %s`), grant each client user access with `usermod -aG %s USER`, then start the service (`systemctl start %s`) and run `tbound product doctor --self-test`.\n",
			productsetup.ServiceUser, productsetup.ServiceGroup, productsetup.UnitName)
	}
	return nil
}

// validateServiceConfigBytes is the service's own validation of the config setup writes.
func validateServiceConfigBytes(data []byte) error {
	var cfg productServiceConfig
	if err := unmarshalGovernedJSON(data, &cfg); err != nil {
		return err
	}
	return cfg.validate(nil)
}

// productAdminTurns reads the installed catalog, which needs the service stopped, without appending to it, and lists the turns
// that fence a registration change (unresolved or unknown). A host that never ran the service has no catalog and no turns.
func productAdminTurns(root string) func() ([]productsetup.OpenTurn, error) {
	return func() ([]productsetup.OpenTurn, error) {
		real := func(p string) string { return filepath.Join(root, p) }
		if _, err := os.Lstat(real(productsetup.CatalogPath)); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		registration, err := productsession.LoadRegistration(real(productsetup.RegistrationPath),
			real(productsetup.RegistrationSignaturePath), real(productsetup.RegistrationPublicKeyPath))
		if err != nil {
			return nil, err
		}
		unsettled, err := productsession.UnsettledTurns(real(productsetup.CatalogPath), registration)
		if err != nil {
			return nil, err
		}
		var turns []productsetup.OpenTurn
		for _, t := range unsettled {
			turns = append(turns, productsetup.OpenTurn{ID: t.ID, State: string(t.State)})
		}
		return turns, nil
	}
}

// productAdminSelfTestHook runs the installed binary's `doctor --self-test`. Its exit code 3 means the
// installation names no self-test project, which is not a failure.
func productAdminSelfTestHook() error {
	out, err := exec.Command(productsetup.BinaryPath, "product", "doctor", "--self-test").CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == 3:
		return productsetup.ErrSelfTestNotConfigured
	}
	return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
}

// productAdminTurnsFor picks how the catalog is opened. A journal must be opened by its owner (the
// audit package refuses any other effective UID), so on the real root the administrative binary lists the
// turns as the service account. A test root is owned by the test process, which opens it directly.
func productAdminTurnsFor(root string) func() ([]productsetup.OpenTurn, error) {
	if root != "/" {
		return productAdminTurns(root)
	}
	return func() ([]productsetup.OpenTurn, error) {
		account, err := productAdminLookupUser(productsetup.ServiceUser)
		if err != nil {
			return nil, fmt.Errorf("the %s account does not exist: %w", productsetup.ServiceUser, err)
		}
		uid, uidErr := strconv.ParseUint(account.Uid, 10, 32)
		gid, gidErr := strconv.ParseUint(account.Gid, 10, 32)
		if uidErr != nil || gidErr != nil {
			return nil, errors.New("the service account has non-numeric IDs")
		}
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate the administrative binary: %w", err)
		}
		cmd := productAdminTurnsCommand(executable, uint32(uid), uint32(gid))
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("list the catalog's unresolved turns as %s: %w: %s", productsetup.ServiceUser, err, strings.TrimSpace(stderr.String()))
		}
		var turns []productsetup.OpenTurn
		if err := json.Unmarshal(out, &turns); err != nil {
			return nil, fmt.Errorf("the turn listing is not JSON: %w", err)
		}
		return turns, nil
	}
}
