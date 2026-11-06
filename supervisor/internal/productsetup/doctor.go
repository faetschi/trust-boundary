package productsetup

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"tbound/supervisor/internal/releasetrust"
)

// DoctorReport is the advisory report. Only a passed self-test establishes governed readiness.
type DoctorReport struct {
	Checks []Check
	DryRun bool
	// SelfTestRequested is set for `doctor --self-test`.
	SelfTestRequested bool
	// SelfTestErr is nil for a passed or planned self-test, ErrSelfTestNotConfigured when the installation names no
	// self-test project, and otherwise the failure wrapped in ErrSelfTest.
	SelfTestErr    error
	SelfTestDetail string
}

// String renders the report.
func (r DoctorReport) String() string {
	var b strings.Builder
	b.WriteString("tbound product doctor (advisory: these checks are not an admission result)\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "  [%s] %-24s %s\n", c.Status, c.Name, c.Detail)
		if c.Status != Pass && c.Fix != "" {
			fmt.Fprintf(&b, "         fix: %s\n", c.Fix)
		}
	}
	switch {
	case r.DryRun && r.SelfTestRequested:
		b.WriteString("dry run: would run the configured self-test; no turn was submitted.\ngoverned readiness: NOT established.\n")
	case !r.SelfTestRequested:
		b.WriteString("governed readiness: NOT established. Only `tbound product doctor --self-test` can establish it.\n")
	case r.SelfTestErr == nil:
		fmt.Fprintf(&b, "self-test: PASSED %s\ngoverned readiness: established by that self-test alone.\n", r.SelfTestDetail)
	case errors.Is(r.SelfTestErr, ErrSelfTestNotConfigured):
		b.WriteString("self-test not configured: add self_test (project_id, provider_profile) to the setup config and rerun setup.\ngoverned readiness: NOT established.\n")
	default:
		fmt.Fprintf(&b, "self-test: FAILED: %v\ngoverned readiness: NOT established.\n", r.SelfTestErr)
	}
	return b.String()
}

// Doctor inspects the installation and, when selfTest is set, runs the explicit self-test turn. It
// reads only: it never changes the host and never reads a provider key.
func Doctor(env Env, selfTest bool) DoctorReport {
	h := newHost(env)
	rep := DoctorReport{SelfTestRequested: selfTest, DryRun: env.DryRun}
	if err := h.openRoot(); err != nil {
		rep.Checks = append(rep.Checks, Check{Name: "root", Status: Fail, Detail: err.Error()})
		if selfTest {
			rep.SelfTestErr = fmt.Errorf("%w: %v", ErrSelfTest, err)
		}
		return rep
	}
	defer h.dir.Close()
	add := func(name, status, detail, fix string) {
		rep.Checks = append(rep.Checks, Check{Name: name, Status: status, Detail: detail, Fix: fix})
	}
	if env.Prereqs != nil {
		for _, c := range env.Prereqs() {
			add("prereq "+c.Name, c.Status, c.Detail, c.Fix)
		}
	}
	const setupFix = "run `tbound product setup`"
	for _, n := range append(append([]node{}, directories...), installedFiles...) {
		info, err := os.Lstat(h.real(n.Path))
		switch {
		case err != nil && n.Path == ReleasePublicKeyPath: // optional; the release key check below reports it
		case errors.Is(err, os.ErrNotExist) && isCommandArtifact(n.Path): // static command check reports missing bundles when required
		case err != nil:
			add(n.Path, Fail, "missing", setupFix)
		case info.IsDir() != n.Dir || info.Mode()&os.ModeSymlink != 0:
			add(n.Path, Fail, "wrong file type", setupFix)
		case modeBits(info) != n.Mode:
			add(n.Path, Fail, fmt.Sprintf("mode %04o, want %04o", modeBits(info), n.Mode), setupFix)
		case !h.ownerMatchesWithIDs(info, n.Owner):
			add(n.Path, Fail, "owner differs from "+n.Owner.String(), setupFix)
		default:
			add(n.Path, Pass, fmt.Sprintf("%04o %s%s", n.Mode, n.Owner, h.ownerNote()), "")
		}
	}
	rep.Checks = append(rep.Checks, h.podmanConfigChecks()...)
	rep.Checks = append(rep.Checks, h.commandProvisionChecks()...)
	rep.Checks = append(rep.Checks, h.unitChecks()...)
	rep.Checks = append(rep.Checks, h.releaseKeyCheck())
	conn, err := net.DialTimeout("unix", h.real(SocketPath), 2*time.Second)
	if err != nil {
		add("socket", Fail, "no listener at "+SocketPath+": "+err.Error(), "start the service: systemctl start "+UnitName)
	} else {
		_ = conn.Close()
		add("socket", Pass, SocketPath+" accepts connections", "")
	}
	summary, ok := h.registrationCheck(&rep)
	if ok {
		rep.Checks = append(rep.Checks, h.installedHostInputChecks(summary)...)
	}
	if ok && env.CredentialStatus != nil {
		for _, profile := range summary.Profiles {
			status, err := env.CredentialStatus(profile)
			if err != nil {
				add("credential "+profile, Fail, err.Error(), "install it with `tbound provider setup --profile "+profile+" --service-user "+ServiceUser+"`")
			} else {
				add("credential "+profile, Pass, status, "")
			}
		}
	} else if ok {
		add("credentials", Warn, "not checked on this root", "")
	}
	if selfTest && !env.DryRun {
		rep.SelfTestDetail, rep.SelfTestErr = h.runSelfTest()
	}
	return rep
}

// ownerMatchesWithIDs resolves the service account IDs the first time it needs them.
func (h *host) ownerMatchesWithIDs(info os.FileInfo, o owner) bool {
	if h.env.Live && o == ownService && !h.haveIDs {
		if uid, gid, found, err := h.lookupUser(ServiceUser); err == nil && found {
			h.uid, h.gid, h.haveIDs = uid, gid, true
		}
	}
	return h.ownerMatches(info, o)
}

func (h *host) ownerNote() string {
	if h.env.Live {
		return ""
	}
	return " (owner not checked on a test root)"
}

// podmanConfigChecks compares the Podman configurations with the bytes setup renders. The layout check sees only
// their owner and mode, so it cannot tell a drifted configuration from the rendered one.
func (h *host) podmanConfigChecks() []Check {
	var checks []Check
	for _, file := range []struct {
		path string
		want []byte
	}{{ContainersConfPath, RenderProductContainersConf()}, {StorageConfPath, RenderProductStorageConf()}} {
		name := file.path + " content"
		data, err := readSource(h.real(file.path))
		switch {
		case errors.Is(err, os.ErrNotExist): // the layout check reports the missing file
		case err != nil:
			checks = append(checks, Check{Name: name, Status: Fail, Detail: err.Error(), Fix: "run `tbound product setup`"})
		case !bytes.Equal(data, file.want):
			checks = append(checks, Check{Name: name, Status: Fail, Detail: "differs from the configuration setup renders", Fix: "rerun `tbound product setup --replace`"})
		default:
			checks = append(checks, Check{Name: name, Status: Pass, Detail: "matches the rendered configuration"})
		}
	}
	return checks
}

func (h *host) unitChecks() []Check {
	data, err := os.ReadFile(h.real(UnitPath))
	if err != nil {
		return nil // the layout check reported the missing file
	}
	var checks []Check
	if err := ValidateUnit(data); err != nil {
		checks = append(checks, Check{Name: "unit content", Status: Fail, Detail: err.Error(), Fix: "rerun `tbound product setup --replace`"})
	} else {
		checks = append(checks, Check{Name: "unit content", Status: Pass, Detail: "matches the pinned product unit"})
	}
	if h.unitEnabled() {
		checks = append(checks, Check{Name: "unit enabled", Status: Pass, Detail: UnitName + " starts at boot (WantedBy=" + unitWantedBy + ")"})
	} else {
		checks = append(checks, Check{Name: "unit enabled", Status: Fail, Detail: UnitName + " does not start at boot", Fix: "run `tbound product setup`"})
	}
	if !h.system() {
		return append(checks, Check{Name: "unit state", Status: Warn, Detail: "not queried on a test root"})
	}
	out, err := h.exec("systemctl", "is-active", UnitName)
	state := strings.TrimSpace(string(out))
	if err != nil || state != "active" {
		return append(checks, Check{Name: "unit state", Status: Fail, Detail: UnitName + " is " + state, Fix: "systemctl start " + UnitName})
	}
	return append(checks, Check{Name: "unit state", Status: Pass, Detail: UnitName + " is active"})
}

// releaseKeyCheck shows the pinned publisher key's fingerprint, which the administrator compares with the one
// the publisher distributes. Without a pinned key `product update` refuses every candidate.
func (h *host) releaseKeyCheck() Check {
	data, err := readSource(h.real(ReleasePublicKeyPath))
	if errors.Is(err, os.ErrNotExist) {
		return Check{Name: "release key", Status: Warn, Detail: "no publisher key is pinned, so `product update` refuses every candidate binary",
			Fix: "set release_public_key in the setup config and rerun `tbound product setup`"}
	}
	var key ed25519.PublicKey
	if err == nil {
		key, err = releasetrust.ParsePublicKey(data)
	}
	if err != nil {
		return Check{Name: "release key", Status: Fail, Detail: err.Error(), Fix: "rerun `tbound product setup --replace` with a valid release_public_key"}
	}
	return Check{Name: "release key", Status: Pass, Detail: "update accepts only binaries signed by the publisher key with fingerprint " + releasetrust.Fingerprint(key)}
}

func (h *host) registrationCheck(rep *DoctorReport) (RegistrationSummary, bool) {
	read := func(p string) []byte { data, _ := readSource(h.real(p)); return data }
	registration, signature, key := read(RegistrationPath), read(RegistrationSignaturePath), read(RegistrationPublicKeyPath)
	if h.env.VerifyRegistration == nil || registration == nil || signature == nil || key == nil {
		rep.Checks = append(rep.Checks, Check{Name: "registration", Status: Fail, Detail: "registration, signature or public key unreadable", Fix: "run `tbound product setup`"})
		return RegistrationSummary{}, false
	}
	summary, err := h.env.VerifyRegistration(registration, signature, key)
	if err != nil {
		rep.Checks = append(rep.Checks, Check{Name: "registration", Status: Fail, Detail: err.Error(), Fix: "sign a new registration offline and rerun setup with --replace"})
		return RegistrationSummary{}, false
	}
	rep.Checks = append(rep.Checks, Check{Name: "registration", Status: Pass, Detail: summary.ID + " " + summary.Digest})
	return summary, true
}

// runSelfTest runs the synthetic turn named by the installed setup config. It reports "not configured"
// instead of faking readiness.
func (h *host) runSelfTest() (string, error) {
	data, err := readSource(h.real(SetupConfigPath))
	if err != nil {
		return "", ErrSelfTestNotConfigured
	}
	cfg, err := parseInstalled(data)
	if err != nil || cfg.SelfTest == nil {
		return "", ErrSelfTestNotConfigured
	}
	if err := cfg.SelfTest.validateClient(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrSelfTest, err)
	}
	if h.env.RunSelfTest == nil {
		return "", fmt.Errorf("%w: no way to reach the service from this root", ErrSelfTest)
	}
	detail, err := h.env.RunSelfTest(*cfg.SelfTest)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSelfTest, err)
	}
	return detail, nil
}

// parseInstalled decodes the installed setup record, which has no source paths.
func parseInstalled(data []byte) (Config, error) {
	c, err := decodeConfig(data)
	if err != nil {
		return Config{}, err
	}
	if c.SchemaVersion != ConfigSchema || len(c.Projects) == 0 {
		return Config{}, refuse("the installed setup record is invalid")
	}
	return c, nil
}

// checkInstalledSelfTestClient refuses, before anything changes, an update or rollback whose self-test
// could not authenticate: the installed record names a self-test whose client UID is missing, or is not
// an allowed principal of the installed registration. A record without a self-test is not checked, and
// the run says so after the swap.
func (h *host) checkInstalledSelfTestClient() error {
	data, err := readSource(h.real(SetupConfigPath))
	if err != nil {
		return nil
	}
	cfg, err := parseInstalled(data)
	if err != nil || cfg.SelfTest == nil {
		return nil
	}
	const fix = "; rerun `tbound product setup --replace` with a registered client UID in self_test.client_uid"
	if err := cfg.SelfTest.validateClient(); err != nil {
		return refuse("%v%s", err, fix)
	}
	installed, err := h.installedRegistration()
	if err != nil {
		return refuse("the installed registration cannot be verified, so the self-test client UID %d cannot be validated: %v", cfg.SelfTest.ClientUID, err)
	}
	if err := cfg.SelfTest.checkRegistered(installed.Principals); err != nil {
		return refuse("%v%s", err, fix)
	}
	return nil
}
