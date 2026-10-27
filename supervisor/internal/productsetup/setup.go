package productsetup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"tbound/supervisor/internal/releasetrust"
)

// planned is one step with the effect that applies it.
type planned struct {
	Step
	apply func() error
}

func modeBits(info fs.FileInfo) os.FileMode {
	return info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

// Setup makes the host match the plan: accounts, directories, installed files and the unit. It plans
// everything before it changes anything, so a refusal leaves the host as it was, and a rerun on a
// converged host reports every step unchanged. binary is the file to install at BinaryPath, or "" to
// leave the binary to `product update`.
func Setup(env Env, cfg Config, binary string) (Report, error) {
	h := newHost(env)
	if err := h.openRoot(); err != nil {
		return *h.rep, err
	}
	defer h.dir.Close()
	if err := cfg.validate(); err != nil {
		return *h.rep, err
	}
	if env.Prereqs != nil {
		h.rep.Checks = env.Prereqs()
	}
	if env.RequirePrereqs {
		var failed []string
		for _, c := range h.rep.Checks {
			if c.Status == Fail {
				failed = append(failed, c.Name+": "+c.Detail)
			}
		}
		if len(failed) > 0 {
			return *h.rep, fmt.Errorf("%w: %s; nothing was changed. Use the qualified VM option (infra/guest/README.md, docs/guides/governed-setup.md)",
				ErrUnsupportedHost, strings.Join(failed, "; "))
		}
	}
	files, summary, err := h.desiredFiles(cfg, binary)
	if err != nil {
		return *h.rep, err
	}
	if cfg.SelfTest != nil {
		if err := cfg.SelfTest.checkRegistered(summary.Principals); err != nil {
			return *h.rep, err
		}
	}
	accounts, err := h.planAccounts(cfg)
	if err != nil {
		return *h.rep, err
	}
	// Every host input is checked, with the key this run installs and the service UID if the account exists,
	// before the first write. A dry run reports the same refusal.
	if err := h.refuseBadHostInputs(cfg, summary, files[RegistrationPublicKeyPath], cfg.RegistrationPublicKey); err != nil {
		return *h.rep, err
	}
	plan := accounts
	for _, n := range directories {
		p, err := h.planDir(n)
		if err != nil {
			return *h.rep, err
		}
		plan = append(plan, p)
	}
	for _, n := range installedFiles {
		data, ok := files[n.Path]
		if !ok { // the binary, when no source was given
			continue
		}
		p, err := h.planFile(n, data)
		if err != nil {
			return *h.rep, err
		}
		plan = append(plan, p)
	}
	var refused []string
	for _, p := range plan {
		h.rep.Steps = append(h.rep.Steps, p.Step)
		if p.Status == "refused" {
			refused = append(refused, p.Path+": "+p.Detail)
		}
	}
	enable := h.planEnable() // applied after the reload, so it is not part of plan
	h.rep.Steps = append(h.rep.Steps, enable.Step)
	if len(refused) > 0 {
		return *h.rep, refuse("%s; nothing was changed", strings.Join(refused, "; "))
	}
	if err := h.refuseRegistrationChangeWhileTurnsAreUnsettled(summary); err != nil {
		return *h.rep, err
	}
	for _, p := range plan {
		if p.apply == nil || p.Status == "unchanged" {
			continue
		}
		if err := p.apply(); err != nil {
			return *h.rep, fmt.Errorf("apply %s %s: %w", p.Kind, p.Path, err)
		}
	}
	for _, p := range plan {
		if p.Path == UnitPath && p.Status != "unchanged" {
			if err := h.command("systemctl", "daemon-reload"); err != nil {
				return *h.rep, err
			}
		}
	}
	if enable.apply != nil {
		if err := enable.apply(); err != nil {
			return *h.rep, fmt.Errorf("apply %s %s: %w", enable.Kind, enable.Path, err)
		}
	}
	return *h.rep, nil
}

func readSource(file string) ([]byte, error) { return readBounded(file, maxSourceBytes) }

func readBounded(file string, limit int64) ([]byte, error) {
	f, err := openSource(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := regularFile(f)
	if err != nil || info.Size() > limit {
		return nil, refuse("%s must be a regular non-symlink file within its size bound", file)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, refuse("%s exceeds its size bound", file)
	}
	return data, err
}

// desiredFiles builds the exact content of every installed file, verifying the registration first.
func (h *host) desiredFiles(cfg Config, binary string) (map[string][]byte, RegistrationSummary, error) {
	if h.env.VerifyRegistration == nil {
		return nil, RegistrationSummary{}, errors.New("no registration verifier configured")
	}
	registration, err := readSource(cfg.Registration)
	if err != nil {
		return nil, RegistrationSummary{}, err
	}
	signature, err := readSource(cfg.RegistrationSignature)
	if err != nil {
		return nil, RegistrationSummary{}, err
	}
	key, err := readSource(cfg.RegistrationPublicKey)
	if err != nil {
		return nil, RegistrationSummary{}, err
	}
	summary, err := h.env.VerifyRegistration(registration, signature, key)
	if err != nil {
		return nil, RegistrationSummary{}, refuse("the registration does not verify under the given public key: %v", err)
	}
	if want, got := cfg.projectIDs(), slices.Sorted(slices.Values(summary.Projects)); !slices.Equal(want, got) {
		return nil, RegistrationSummary{}, refuse("projects [%s] must name exactly the registered projects [%s]", joinIDs(want), joinIDs(got))
	}
	serviceConfig, err := cfg.serviceConfigJSON()
	if err != nil {
		return nil, RegistrationSummary{}, err
	}
	if h.env.ValidateServiceConfig != nil {
		if err := h.env.ValidateServiceConfig(serviceConfig); err != nil {
			return nil, RegistrationSummary{}, refuse("the service would refuse this configuration: %v", err)
		}
	}
	installed, err := cfg.installedJSON()
	if err != nil {
		return nil, RegistrationSummary{}, err
	}
	unit := RenderUnit()
	if err := ValidateUnit(unit); err != nil {
		return nil, RegistrationSummary{}, err
	}
	files := map[string][]byte{
		RegistrationPath: registration, RegistrationSignaturePath: signature, RegistrationPublicKeyPath: key,
		ServiceConfigPath: serviceConfig, SetupConfigPath: installed, UnitPath: unit,
	}
	if cfg.ReleasePublicKey != "" {
		publisher, err := readSource(cfg.ReleasePublicKey)
		if err == nil {
			_, err = releasetrust.ParsePublicKey(publisher)
		}
		if err != nil {
			return nil, RegistrationSummary{}, refuse("release_public_key is not a readable base64 Ed25519 publisher key: %v", err)
		}
		files[ReleasePublicKeyPath] = publisher
	}
	if binary != "" {
		if files[BinaryPath], err = readBounded(binary, maxBinaryBytes); err != nil {
			return nil, RegistrationSummary{}, err
		}
		// The format is read from the bytes that will be installed, so a swap of the file cannot change it.
		if err := h.checkSetupBinaryFormat(files[BinaryPath], binary); err != nil {
			return nil, RegistrationSummary{}, err
		}
	}
	return files, summary, nil
}

// planDir converges a directory: create it, or correct a differing mode or owner.
func (h *host) planDir(n node) (planned, error) {
	real := h.real(n.Path)
	info, err := os.Lstat(real)
	detail := fmt.Sprintf("%04o %s", n.Mode, n.Owner)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return planned{Step{"dir", n.Path, "created", detail}, func() error {
			if h.writes() {
				if err := h.withPath(n.Path, true, func(path string) error { return os.Mkdir(path, n.Mode) }); err != nil {
					return err
				}
				if err := h.chmod(n.Path, n.Mode); err != nil { // the umask must not decide the mode
					return err
				}
			}
			return h.chown(n.Path, n.Owner)
		}}, nil
	case err != nil:
		return planned{}, err
	case !info.IsDir():
		return planned{Step{"dir", n.Path, "refused", "exists and is not a directory (symlinks are refused)"}, nil}, nil
	}
	modeOK, ownerOK := modeBits(info) == n.Mode, h.ownerMatches(info, n.Owner)
	if modeOK && ownerOK {
		return planned{Step{"dir", n.Path, "unchanged", detail}, nil}, nil
	}
	return planned{Step{"dir", n.Path, "updated", detail}, func() error {
		if h.writes() && !modeOK {
			if err := h.chmod(n.Path, n.Mode); err != nil {
				return err
			}
		}
		if ownerOK {
			return nil
		}
		return h.chown(n.Path, n.Owner)
	}}, nil
}

// ownerMatches compares an owner only when the run is live: a test root cannot be chowned.
func (h *host) ownerMatches(info fs.FileInfo, o owner) bool {
	if !h.env.Live {
		return true
	}
	uid, gid, ok := ownerOf(info)
	wantUID, wantGID, known := h.wantIDs(o)
	return !ok || (known && uid == wantUID && gid == wantGID)
}

// planFile converges a file. A file with other content is refused unless Env.Replace is set.
func (h *host) planFile(n node, data []byte) (planned, error) {
	real := h.real(n.Path)
	info, err := os.Lstat(real)
	detail := fmt.Sprintf("%04o %s", n.Mode, n.Owner)
	write := func() error {
		if h.writes() {
			if err := h.withPath(n.Path, true, func(path string) error { return writeFile(path, data, n.Mode) }); err != nil {
				return err
			}
		}
		return h.chown(n.Path, n.Owner)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return planned{Step{"file", n.Path, "created", detail}, write}, nil
	case err != nil:
		return planned{}, err
	case !info.Mode().IsRegular():
		return planned{Step{"file", n.Path, "refused", "exists and is not a regular file (symlinks are refused)"}, nil}, nil
	}
	same := info.Size() == int64(len(data))
	if same {
		existing, err := os.ReadFile(real)
		if err != nil {
			return planned{}, err
		}
		same = bytes.Equal(existing, data)
	}
	if !same {
		if !h.env.Replace {
			return planned{Step{"file", n.Path, "refused", "exists with different content; use --replace"}, nil}, nil
		}
		return planned{Step{"file", n.Path, "updated", "content replaced; " + detail}, write}, nil
	}
	modeOK, ownerOK := modeBits(info) == n.Mode, h.ownerMatches(info, n.Owner)
	if modeOK && ownerOK {
		return planned{Step{"file", n.Path, "unchanged", detail}, nil}, nil
	}
	return planned{Step{"file", n.Path, "updated", detail}, func() error {
		if h.writes() && !modeOK {
			if err := h.chmod(n.Path, n.Mode); err != nil {
				return err
			}
		}
		if ownerOK {
			return nil
		}
		return h.chown(n.Path, n.Owner)
	}}, nil
}

// planAccounts converges the service group and user, or refuses when they exist with other IDs.
func (h *host) planAccounts(cfg Config) ([]planned, error) {
	gid, gFound, err := h.lookupGroup(ServiceGroup)
	if err != nil {
		return nil, err
	}
	uid, ugid, uFound, err := h.lookupUser(ServiceUser)
	if err != nil {
		return nil, err
	}
	switch {
	case gFound && cfg.ServiceGID != 0 && gid != cfg.ServiceGID:
		return nil, refuse("group %s exists with GID %d, but service_gid is %d", ServiceGroup, gid, cfg.ServiceGID)
	case uFound && cfg.ServiceUID != 0 && uid != cfg.ServiceUID:
		return nil, refuse("user %s exists with UID %d, but service_uid is %d", ServiceUser, uid, cfg.ServiceUID)
	case uFound && !gFound:
		return nil, refuse("user %s exists but group %s does not", ServiceUser, ServiceGroup)
	case uFound && ugid != gid:
		return nil, refuse("user %s exists with primary GID %d, not the GID %d of group %s", ServiceUser, ugid, gid, ServiceGroup)
	}
	if uFound {
		h.uid, h.gid, h.haveIDs = uid, gid, true
	}
	var plan []planned
	id := gid
	if gFound {
		plan = append(plan, planned{Step{"account", "group " + ServiceGroup, "unchanged", "GID " + strconv.Itoa(gid)}, nil})
	} else {
		plan = append(plan, planned{Step{"account", "group " + ServiceGroup, "created", "system group"}, func() error {
			args := []string{"--system"}
			if cfg.ServiceGID != 0 {
				args = append(args, "--gid", strconv.Itoa(cfg.ServiceGID))
			}
			if err := h.command("groupadd", append(args, ServiceGroup)...); err != nil {
				return err
			}
			var err error
			id, err = h.recordGroup(cfg.ServiceGID)
			return err
		}})
	}
	if uFound {
		return append(plan, planned{Step{"account", "user " + ServiceUser, "unchanged", "UID " + strconv.Itoa(uid)}, nil}), nil
	}
	return append(plan, planned{Step{"account", "user " + ServiceUser, "created", "system user, no login, home " + StateDir}, func() error {
		args := []string{"--system", "--gid", ServiceGroup}
		if cfg.ServiceUID != 0 {
			args = append(args, "--uid", strconv.Itoa(cfg.ServiceUID))
		}
		args = append(args, "--home-dir", StateDir, "--no-create-home", "--shell", "/usr/sbin/nologin", ServiceUser)
		if err := h.command("useradd", args...); err != nil {
			return err
		}
		return h.recordUser(cfg.ServiceUID, id)
	}}), nil
}

// refuseRegistrationChangeWhileTurnsAreUnsettled opens the catalog under the installed registration and
// refuses a registration with another digest while any turn is unresolved or unknown. The service refuses
// to start on a changed digest in that case (PREDECESSOR_UNRESOLVED) before its restart reconciliation
// runs, so nothing could settle those turns afterwards. It runs before the first write, in a dry run too,
// and only when an installed registration and a catalog exist.
func (h *host) refuseRegistrationChangeWhileTurnsAreUnsettled(next RegistrationSummary) error {
	if _, err := os.Lstat(h.real(RegistrationPath)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if installed, err := h.installedRegistration(); err == nil && installed.Digest == next.Digest {
		return nil
	}
	if _, err := os.Lstat(h.real(CatalogPath)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if h.env.Turns == nil {
		return refuse("no catalog check configured; refusing to replace the registration; nothing was changed")
	}
	turns, err := h.env.Turns()
	if err != nil {
		return refuse("the catalog cannot be opened under the installed registration, so its turns cannot be checked before the replacement (stop the service first): %v; nothing was changed", err)
	}
	if len(turns) == 0 {
		h.step("catalog", CatalogPath, "unchanged", "no unsettled turn under the installed registration")
		return nil
	}
	names := make([]string, len(turns))
	for i, t := range turns {
		names[i] = t.ID + " (" + t.State + ")"
	}
	return refuse("the replacement registration would be refused at the next start while these turns are unsettled under the installed one: %s. Start the service under the installed registration so its restart reconciliation settles them, stop it, and retry (a turn with an unknown outcome stays blocked); nothing was changed",
		strings.Join(names, ", "))
}

// installedRegistration verifies the installed registration like the service does.
func (h *host) installedRegistration() (RegistrationSummary, error) {
	if h.env.VerifyRegistration == nil {
		return RegistrationSummary{}, errors.New("no registration verifier configured")
	}
	var parts [3][]byte
	for i, logical := range []string{RegistrationPath, RegistrationSignaturePath, RegistrationPublicKeyPath} {
		data, err := readSource(h.real(logical))
		if err != nil {
			return RegistrationSummary{}, err
		}
		parts[i] = data
	}
	return h.env.VerifyRegistration(parts[0], parts[1], parts[2])
}
