//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

func adminRun(args ...string) (string, error) {
	var out, errb bytes.Buffer
	err := run(args, &out, &errb)
	return out.String() + errb.String(), err
}

func adminExit(err error) int {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	if err == nil {
		return 0
	}
	return -1
}

func mustAdmin(t *testing.T, args ...string) string {
	t.Helper()
	out, err := adminRun(args...)
	if err != nil {
		t.Fatalf("tbound %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

var adminDigest = "sha256:" + strings.Repeat("a", 64)

func adminInitArgs(out string) []string {
	return []string{"product", "registration", "init", "--out", out,
		"--registration-id", "reg-1", "--service-identity", "svc-1", "--principal", "1000", "--principal", "0",
		"--provider", "opencode-go-engineering",
		"--project", "id=alpha,root=" + adminDigest + ",policy=" + adminDigest + ",modes=review+auto,writable=src",
		"--project", "id=beta,root=" + adminDigest + ",policy=" + adminDigest + ",modes=review",
		"--max-turns", "10", "--review-timeout", "600", "--turn-limit", "10,1000,60", "--session-limit", "100,10000,600",
		"--runtime-digest", adminDigest, "--image-digest", adminDigest, "--extension-digest", adminDigest}
}

// adminHost is a test root plus the administrator's signed registration, made with the CLI itself.
type adminHost struct {
	t        *testing.T
	root     string
	dir      string
	reg, sig string
	pub      string
	binary   string
}

func newAdminHost(t *testing.T, root string) *adminHost {
	t.Helper()
	old := productAdminPrereqs
	productAdminPrereqs = func() []productsetup.Check {
		return []productsetup.Check{{Name: "systemd", Status: productsetup.Pass, Detail: "fake host"}}
	}
	t.Cleanup(func() { productAdminPrereqs = old })
	h := &adminHost{t: t, root: root, dir: t.TempDir()}
	keys := filepath.Join(h.dir, "keys")
	mustAdmin(t, "product", "registration", "keygen", "--out", keys)
	template := filepath.Join(h.dir, "template.json")
	mustAdmin(t, adminInitArgs(template)...)
	h.reg = filepath.Join(h.dir, "registration.json")
	mustAdmin(t, "product", "registration", "sign", "--registration", template, "--key", filepath.Join(keys, "registration.key"), "--out", h.reg)
	h.sig, h.pub = h.reg+".sig", filepath.Join(keys, "registration.pub")
	h.binary = filepath.Join(h.dir, "tbound-v1")
	if err := os.WriteFile(h.binary, []byte("binary v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *adminHost) setupConfig() productsetup.Config {
	return productsetup.Config{
		SchemaVersion: productsetup.ConfigSchema, Registration: h.reg, RegistrationSignature: h.sig, RegistrationPublicKey: h.pub,
		WorkerMode: productsession.WorkerSupervisorLoop,
		Projects:   map[string]string{"alpha": productsetup.ProjectsDir + "/alpha", "beta": productsetup.ProjectsDir + "/beta"},
		GatePolicyPaths: map[string]string{"alpha": productsetup.ConfigDir + "/policy-alpha.json",
			"beta": productsetup.ConfigDir + "/policy-beta.json"},
		GitPath: productsetup.BinaryDir + "/git", GitDigest: "sha256:" + strings.Repeat("a", 64),
		XattrCalibrationPath: productsetup.ConfigDir + "/xattr-calibration.json",
	}
}

func (h *adminHost) config(selfTest bool) string {
	h.t.Helper()
	cfg := h.setupConfig()
	if selfTest {
		cfg.SelfTest = &productsetup.SelfTest{ProjectID: "alpha", ProviderProfile: "opencode-go-engineering", TimeoutSeconds: 10}
	}
	data, _ := json.Marshal(cfg)
	file := filepath.Join(h.dir, "setup.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		h.t.Fatal(err)
	}
	return file
}

func (h *adminHost) setup(extra ...string) (string, error) {
	return adminRun(append([]string{"product", "setup", "--config", h.config(false), "--root", h.root, "--binary", h.binary}, extra...)...)
}

func (h *adminHost) path(logical string) string { return filepath.Join(h.root, logical) }

func TestProductAdminThesisRootsMatchTheService(t *testing.T) {
	if !slices.Equal(productsetup.ThesisRoots, productServiceThesisRoots) {
		t.Fatalf("productsetup.ThesisRoots %v differ from the service's %v", productsetup.ThesisRoots, productServiceThesisRoots)
	}
}

func TestProductAdminRegistrationToolingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	out := mustAdmin(t, "product", "registration", "keygen", "--out", keys)
	if !strings.Contains(out, "fingerprint") {
		t.Fatalf("keygen output: %s", out)
	}
	info, err := os.Stat(filepath.Join(keys, "registration.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode: %v %v", info, err)
	}
	if _, err := adminRun("product", "registration", "keygen", "--out", keys); adminExit(err) != 2 {
		t.Fatalf("keygen overwrote an existing key pair: %v", err)
	}

	template := filepath.Join(dir, "template.json")
	mustAdmin(t, adminInitArgs(template)...)
	if _, err := adminRun(adminInitArgs(template)...); err == nil {
		t.Fatal("init overwrote an existing template")
	}
	signed := filepath.Join(dir, "registration.json")
	mustAdmin(t, "product", "registration", "sign", "--registration", template, "--key", filepath.Join(keys, "registration.key"), "--out", signed)
	if _, err := adminRun("product", "registration", "sign", "--registration", template, "--key", filepath.Join(keys, "registration.key"), "--out", signed); adminExit(err) != 2 {
		t.Fatalf("sign overwrote its output: %v", err)
	}
	pub := filepath.Join(keys, "registration.pub")
	verify := mustAdmin(t, "product", "registration", "verify", "--registration", signed, "--signature", signed+".sig", "--public-key", pub)
	reg, err := productsession.LoadRegistration(signed, signed+".sig", pub)
	if err != nil {
		t.Fatalf("the service's own loader refused the signed registration: %v", err)
	}
	for _, want := range []string{reg.Digest, "project alpha", "project beta", "principals (UIDs): [1000 0]", "opencode-go-engineering"} {
		if !strings.Contains(verify, want) {
			t.Errorf("verify output lacks %q:\n%s", want, verify)
		}
	}

	data, _ := os.ReadFile(signed)
	if err := os.WriteFile(signed, bytes.Replace(data, []byte(`"reg-1"`), []byte(`"reg-2"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := adminRun("product", "registration", "verify", "--registration", signed, "--signature", signed+".sig", "--public-key", pub); adminExit(err) != 2 {
		t.Fatalf("a tampered registration verified: %v", err)
	}
}

func TestProductAdminRegistrationInitAndSignRefuseWhatTheRegistrationRefuses(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	args := adminInitArgs(bad)
	args[slices.Index(args, "--runtime-digest")+1] = "sha256:short"
	if _, err := adminRun(args...); adminExit(err) != 2 {
		t.Fatalf("a template with an invalid digest was written: %v", err)
	}
	if _, err := os.Stat(bad); err == nil {
		t.Fatal("a refused init left a file")
	}
	template := filepath.Join(dir, "template.json")
	mustAdmin(t, adminInitArgs(template)...)
	keys := filepath.Join(dir, "keys")
	mustAdmin(t, "product", "registration", "keygen", "--out", keys)
	if err := os.Chmod(filepath.Join(keys, "registration.key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := adminRun("product", "registration", "sign", "--registration", template, "--key", filepath.Join(keys, "registration.key"), "--out", filepath.Join(dir, "r.json")); adminExit(err) != 2 {
		t.Fatalf("a world-readable signing key was used: %v", err)
	}
}

func TestProductAdminSetupWritesTheTreeAndARerunIsUnchanged(t *testing.T) {
	root := t.TempDir()
	h := newAdminHost(t, root)
	out, err := h.setup()
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	for _, want := range []string{productsetup.CredentialsDir + " (0700 " + productsetup.ServiceUser + ":" + productsetup.ServiceGroup + ")", "chown root:root " + productsetup.ConfigDir,
		"useradd --system", "systemctl daemon-reload", "recorded, not executed"} {
		if !strings.Contains(out, want) {
			t.Errorf("setup output lacks %q:\n%s", want, out)
		}
	}
	serviceConfig := h.path(productsetup.ServiceConfigPath)
	cfg, err := loadProductServiceConfig(serviceConfig)
	if err != nil || cfg.validate(nil) != nil {
		t.Fatalf("the service refuses the config setup wrote: %v %v", err, cfg.validate(nil))
	}
	if cfg.RegistrationPath != productsession.DefaultRegistrationPath || cfg.SocketPath != productsession.DefaultServiceSocketPath {
		t.Fatalf("service config does not use the product defaults: %+v", cfg)
	}
	if cfg.GitPath != productsetup.BinaryDir+"/git" || cfg.GitDigest != "sha256:"+strings.Repeat("a", 64) || len(cfg.GatePolicyPaths) != 2 ||
		cfg.XattrCalibrationPath != productsetup.ConfigDir+"/xattr-calibration.json" {
		t.Fatalf("service config lost the host profile inputs: %+v", cfg)
	}
	unit, _ := os.ReadFile(h.path(productsetup.UnitPath))
	if err := productsetup.ValidateUnit(unit); err != nil {
		t.Fatal(err)
	}
	again, err := h.setup()
	if err != nil || !strings.HasSuffix(strings.TrimSpace(again), "unchanged") || strings.Contains(again, "commands (") {
		t.Fatalf("a rerun must report unchanged and issue no commands: %v\n%s", err, again)
	}
}

func TestProductAdminSetupRefusesThesisPathsConflictsAndWritesNothingOnDryRun(t *testing.T) {
	root := t.TempDir()
	h := newAdminHost(t, root)
	thesis := h.setupConfig()
	thesis.Projects = map[string]string{"alpha": "/var/lib/tbound/live/alpha", "beta": "/var/lib/tbound-product/projects/beta"}
	data, _ := json.Marshal(thesis)
	file := filepath.Join(h.dir, "thesis.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := adminRun("product", "setup", "--config", file, "--root", root, "--binary", h.binary); adminExit(err) != 2 || !strings.Contains(err.Error(), "thesis service path") {
		t.Fatalf("a thesis path was not refused with exit 2: %v\n%s", err, out)
	}
	if out, err := h.setup("--dry-run"); err != nil || !strings.Contains(out, "dry run: nothing was changed") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("refusal and dry run wrote %v", entries)
	}
	if _, err := h.setup(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.path(productsetup.ServiceConfigPath), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.setup(); adminExit(err) != 2 || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("a conflicting file was not refused: %v", err)
	}
	if _, err := h.setup("--replace"); err != nil {
		t.Fatalf("--replace: %v", err)
	}
}

// adminCatalog opens the installed catalog like the service would, creates one turn that never
// settles, and closes it again so the service is "stopped".
func adminCatalog(t *testing.T, h *adminHost, withTurn bool) {
	t.Helper()
	reg, err := productsession.LoadRegistration(h.reg, h.sig, h.pub)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := productsession.Open(h.path(productsetup.CatalogPath), reg)
	if err != nil {
		t.Skipf("the audit journal refuses this test directory: %v", err)
	}
	defer catalog.Close()
	if !withTurn {
		return
	}
	session, _, err := catalog.CreateSession(1000, productsession.CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "opencode-go-engineering",
		PublicationMode: "review", Budget: productsession.Budget{HostCalls: 10, Tokens: 1000, DurationSeconds: 60}, RequestKey: "k-session"}, adminDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SubmitTurn(1000, productsession.SubmitTurnRequest{SessionID: session.ID, ExpectedRevision: session.Revision,
		RequestKey: "k-turn", Prompt: "hello"}, adminDigest); err != nil {
		t.Fatal(err)
	}
}

func TestProductAdminUpdateRefusesAnUnresolvedTurnThenUpdatesAndRollsBack(t *testing.T) {
	root := t.TempDir()
	h := newAdminHost(t, root)
	if out, err := h.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	next := filepath.Join(h.dir, "tbound-v2")
	if err := os.WriteFile(next, []byte("binary v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	adminCatalog(t, h, true)
	out, err := adminRun("product", "update", "--binary", next, "--root", root)
	if adminExit(err) != 2 || !strings.Contains(err.Error(), "unresolved turns") {
		t.Fatalf("an unresolved turn did not refuse the update: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(h.path(productsetup.BinaryPath)); string(data) != "binary v1" {
		t.Fatal("the binary changed despite the refusal")
	}

	other := t.TempDir()
	h2 := newAdminHost(t, other)
	if out, err := h2.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	adminCatalog(t, h2, false)
	out, err = adminRun("product", "update", "--binary", next, "--root", other)
	if err != nil || !strings.Contains(out, "updated") || !strings.Contains(out, "systemctl stop "+productsetup.UnitName) {
		t.Fatalf("update: %v\n%s", err, out)
	}
	read := func(p string) string { data, _ := os.ReadFile(h2.path(p)); return string(data) }
	if read(productsetup.BinaryPath) != "binary v2" || read(productsetup.LastKnownGoodPath) != "binary v1" {
		t.Fatalf("binary %q last-known-good %q", read(productsetup.BinaryPath), read(productsetup.LastKnownGoodPath))
	}
	if backups, _ := filepath.Glob(h2.path(productsetup.BackupsDir + "/catalog-*.jsonl")); len(backups) != 1 {
		t.Fatalf("catalog backups: %v", backups)
	}
	if _, err := adminRun("product", "rollback", "--root", other); err != nil || read(productsetup.BinaryPath) != "binary v1" {
		t.Fatalf("rollback: %v", err)
	}
	if out, err := adminRun("product", "update", "--binary", next, "--root", other, "--dry-run"); err != nil || !strings.Contains(out, "dry run") || read(productsetup.BinaryPath) != "binary v1" {
		t.Fatalf("update dry run: %v\n%s", err, out)
	}
}

func TestProductAdminUninstallKeepsDataAndPurgeAsksForConfirmation(t *testing.T) {
	root := t.TempDir()
	h := newAdminHost(t, root)
	if out, err := h.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if out, err := adminRun("product", "uninstall", "--root", root); err != nil || !strings.Contains(out, "uninstalled") {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	for _, kept := range []string{productsetup.ConfigDir, productsetup.StateDir, productsetup.RegistrationPath} {
		if _, err := os.Stat(h.path(kept)); err != nil {
			t.Errorf("%s was removed without --purge", kept)
		}
	}
	for _, gone := range []string{productsetup.UnitPath, productsetup.BinaryPath} {
		if _, err := os.Stat(h.path(gone)); err == nil {
			t.Errorf("%s survived", gone)
		}
	}
	oldStdin := productAdminStdin
	t.Cleanup(func() { productAdminStdin = oldStdin })
	productAdminStdin = strings.NewReader("no\n")
	if _, err := adminRun("product", "uninstall", "--root", root, "--purge"); adminExit(err) != 2 {
		t.Fatalf("a declined purge was not refused: %v", err)
	}
	if _, err := os.Stat(h.path(productsetup.ConfigDir)); err != nil {
		t.Fatal("a declined purge removed data")
	}
	if out, err := adminRun("product", "uninstall", "--root", root, "--purge", "--yes"); err != nil {
		t.Fatalf("purge --yes: %v\n%s", err, out)
	}
	for _, gone := range []string{productsetup.ConfigDir, productsetup.StateDir} {
		if _, err := os.Stat(h.path(gone)); err == nil {
			t.Errorf("%s survived the purge", gone)
		}
	}
}

// fakeProductService answers the self-test's three operations on the product socket.
func fakeProductService(t *testing.T, socket string, outcome *atomic.Value) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socket), 0o750); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var req productprotocol.Request
				if productprotocol.ReadFrame(conn, productprotocol.MaxRequestBytes, &req) != nil {
					return // a bare connect, like the reachability probe
				}
				resp := productprotocol.Response{Version: productprotocol.Version, Status: productprotocol.StatusOK}
				switch req.Op {
				case productprotocol.OpCreateSession:
					if !req.CreateSession.ReadOnly || req.CreateSession.ProjectID != "alpha" {
						resp = productprotocol.Response{Version: productprotocol.Version, Status: productprotocol.StatusError, Error: &productprotocol.Error{Code: "INVALID_REQUEST"}}
					}
					resp.Session = &productprotocol.SessionView{ID: "psess-fake", Revision: 1}
				case productprotocol.OpSubmitTurn:
					resp.Turn = &productprotocol.TurnView{ID: "psess-fake/turn-1", State: "admitted"}
				case productprotocol.OpGetSession:
					resp.Session = &productprotocol.SessionView{ID: "psess-fake", Turns: []productprotocol.TurnView{
						{ID: "psess-fake/turn-1", State: "terminal", Outcome: outcome.Load().(string)}}}
				}
				_ = productprotocol.WriteFrame(conn, resp, productprotocol.MaxResponseBytes)
			}()
		}
	}()
}

func TestProductAdminDoctorSelfTestThroughTheServiceSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "tbd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	h := newAdminHost(t, root)
	if out, err := adminRun("product", "setup", "--config", h.config(false), "--root", root, "--binary", h.binary); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	const sentinel = "sk-SENTINEL-PROVIDER-KEY-0123456789"
	if err := os.WriteFile(h.path(productsetup.CredentialsDir+"/opencode-go-engineering.key"), []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	var outcome atomic.Value
	outcome.Store("no_diff")
	fakeProductService(t, h.path(productsetup.SocketPath), &outcome)

	advisory, err := adminRun("product", "doctor", "--root", root)
	if err != nil || !strings.Contains(advisory, "governed readiness: NOT established") || strings.Contains(advisory, sentinel) {
		t.Fatalf("advisory doctor: %v\n%s", err, advisory)
	}
	notConfigured, err := adminRun("product", "doctor", "--self-test", "--root", root)
	if adminExit(err) != 3 || !strings.Contains(notConfigured, "self-test not configured") || strings.Contains(notConfigured, "PASSED") {
		t.Fatalf("an unconfigured self-test must say so and exit 3: %v\n%s", err, notConfigured)
	}

	if out, err := adminRun("product", "setup", "--config", h.config(true), "--root", root, "--binary", h.binary, "--replace"); err != nil {
		t.Fatalf("setup with self-test: %v\n%s", err, out)
	}
	passed, err := adminRun("product", "doctor", "--self-test", "--root", root)
	if err != nil || !strings.Contains(passed, "self-test: PASSED on project alpha") || strings.Contains(passed, sentinel) {
		t.Fatalf("passing self-test: %v\n%s", err, passed)
	}
	outcome.Store("failed")
	failed, err := adminRun("product", "doctor", "--self-test", "--root", root)
	if adminExit(err) != 1 || !strings.Contains(failed, "self-test: FAILED") || strings.Contains(failed, "PASSED") {
		t.Fatalf("failing self-test must exit 1: %v\n%s", err, failed)
	}
}

func TestProductAdminDoctorFailsWithoutAnInstallation(t *testing.T) {
	out, err := adminRun("product", "doctor", "--root", t.TempDir())
	if adminExit(err) != 1 || !strings.Contains(out, "[FAIL]") || !strings.Contains(out, "governed readiness: NOT established") {
		t.Fatalf("doctor on an empty root: %v\n%s", err, out)
	}
}

func TestProductAdminReviewRegistrationSignRefusesAmbiguousJSON(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	mustAdmin(t, "product", "registration", "keygen", "--out", keys)
	template := filepath.Join(dir, "template.json")
	mustAdmin(t, adminInitArgs(template)...)
	raw, err := os.ReadFile(template)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"concatenated": append(append([]byte{}, raw...), []byte(`{}`)...),
		"trailing":     append(append([]byte{}, raw...), []byte(`garbage`)...),
		"duplicate":    []byte(strings.Replace(string(raw), `"registration_id": "reg-1"`, `"registration_id": "discarded", "registration_id": "reg-1"`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(template, data, 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, name+".json")
			if _, err := adminRun("product", "registration", "sign", "--registration", template, "--key", filepath.Join(keys, "registration.key"), "--out", out); adminExit(err) != 2 {
				t.Fatalf("ambiguous JSON signed: %v", err)
			}
			for _, path := range []string{out, out + ".sig"} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refused signing wrote %s: %v", path, err)
				}
			}
		})
	}
}

func TestProductAdminUninstallRefusesLockedCatalog(t *testing.T) {
	h := newAdminHost(t, t.TempDir())
	if out, err := h.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	reg, err := productsession.LoadRegistration(h.reg, h.sig, h.pub)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := productsession.Open(h.path(productsetup.CatalogPath), reg)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	if out, err := adminRun("product", "uninstall", "--root", h.root, "--purge", "--yes"); err == nil || !strings.Contains(err.Error(), "refusing purge") {
		t.Fatalf("locked catalog did not refuse purge: %v\n%s", err, out)
	}
	for _, kept := range []string{productsetup.UnitPath, productsetup.BinaryPath, productsetup.RegistrationPath, productsetup.CatalogPath} {
		if _, err := os.Stat(h.path(kept)); err != nil {
			t.Fatalf("removed %s: %v", kept, err)
		}
	}
}

func TestProductAdminTurnsDoesNotRequireRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root caller")
	}
	out, err := adminRun("product", "turns")
	if err != nil && strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("catalog owner cannot list turns: %v\n%s", err, out)
	}
	for _, command := range []string{"setup", "update", "rollback", "uninstall"} {
		if _, err := adminRun("product", command); err == nil || !strings.Contains(err.Error(), "must run as root") {
			t.Fatalf("%s no longer requires root: %v", command, err)
		}
	}
	h := newAdminHost(t, t.TempDir())
	if _, err := h.setup(); err != nil {
		t.Fatal(err)
	}
	adminCatalog(t, h, false)
	if err := os.Chmod(h.path(productsetup.CatalogPath), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := adminRun("product", "turns", "--root", h.root); err == nil {
		t.Fatal("turn listing bypassed catalog permission checks")
	}
}

func TestProductAdminPurgeAfterOrdinaryUninstallUsesAdministrativeBinary(t *testing.T) {
	h := newAdminHost(t, t.TempDir())
	if _, err := h.setup(); err != nil {
		t.Fatal(err)
	}
	adminCatalog(t, h, false)
	if _, err := adminRun("product", "uninstall", "--root", h.root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.path(productsetup.BinaryPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed binary survived uninstall: %v", err)
	}
	oldLookup, oldCommand := productAdminLookupUser, productAdminTurnsCommand
	t.Cleanup(func() { productAdminLookupUser, productAdminTurnsCommand = oldLookup, oldCommand })
	productAdminLookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Geteuid()), Gid: strconv.Itoa(os.Getegid())}, nil
	}
	called := false
	productAdminTurnsCommand = func(name string, uid, gid uint32) *exec.Cmd {
		called = true
		cmd := oldCommand(name, uid, gid)
		if credential := cmd.SysProcAttr.Credential; credential.Uid != uint32(os.Geteuid()) || credential.Gid != uint32(os.Getegid()) {
			t.Fatalf("catalog check did not select the service account: %+v", credential)
		}
		cmd.SysProcAttr = nil // The test root's catalog is owned by this test process.
		cmd.Args = append([]string{name, "-test.run=^TestProductAdminHelperProcess$", "--"}, cmd.Args[1:]...)
		cmd.Args = append(cmd.Args, "--root", h.root)
		cmd.Env = append(os.Environ(), "TBOUND_ADMIN_HELPER=1")
		return cmd
	}
	env := productAdminEnv(h.root, false, false, true, io.Discard, io.Discard)
	env.Turns = productAdminTurnsFor("/")
	if rep, err := productsetup.Uninstall(env, true); err != nil {
		t.Fatalf("purge after uninstall: %v\n%s", err, rep)
	}
	if !called {
		t.Fatal("purge did not run the catalog child")
	}
	for _, path := range []string{productsetup.ConfigDir, productsetup.StateDir} {
		if _, err := os.Stat(h.path(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("purge kept %s: %v", path, err)
		}
	}
}

func TestProductAdminUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"product"}, {"product", "bogus"}, {"product", "setup"}, {"product", "update"}, {"product", "setup", "--config", "x", "--root", "relative"},
		{"release"}, {"release", "verify"}, {"release", "sign", "--manifest", "m"}} {
		if _, err := adminRun(args...); adminExit(err) != 2 {
			t.Errorf("%v: exit %d (%v), want 2", args, adminExit(err), err)
		}
	}
}
