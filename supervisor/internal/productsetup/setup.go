package productsetup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	files, err := h.desiredFiles(cfg, binary)
	if err != nil {
		return *h.rep, err
	}
	accounts, err := h.planAccounts(cfg)
	if err != nil {
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
	if len(refused) > 0 {
		return *h.rep, refuse("%s; nothing was changed", strings.Join(refused, "; "))
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
	return *h.rep, nil
}

func readSource(file string) ([]byte, error) { return readBounded(file, maxSourceBytes) }

func readBounded(file string, limit int64) ([]byte, error) {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, refuse("%s must be a regular non-symlink file within its size bound", file)
	}
	return os.ReadFile(file)
}

// desiredFiles builds the exact content of every installed file, verifying the registration first.
func (h *host) desiredFiles(cfg Config, binary string) (map[string][]byte, error) {
	if h.env.VerifyRegistration == nil {
		return nil, errors.New("no registration verifier configured")
	}
	registration, err := readSource(cfg.Registration)
	if err != nil {
		return nil, err
	}
	signature, err := readSource(cfg.RegistrationSignature)
	if err != nil {
		return nil, err
	}
	key, err := readSource(cfg.RegistrationPublicKey)
	if err != nil {
		return nil, err
	}
	summary, err := h.env.VerifyRegistration(registration, signature, key)
	if err != nil {
		return nil, refuse("the registration does not verify under the given public key: %v", err)
	}
	if want, got := cfg.projectIDs(), slices.Sorted(slices.Values(summary.Projects)); !slices.Equal(want, got) {
		return nil, refuse("projects [%s] must name exactly the registered projects [%s]", joinIDs(want), joinIDs(got))
	}
	serviceConfig, err := cfg.serviceConfigJSON()
	if err != nil {
		return nil, err
	}
	if h.env.ValidateServiceConfig != nil {
		if err := h.env.ValidateServiceConfig(serviceConfig); err != nil {
			return nil, refuse("the service would refuse this configuration: %v", err)
		}
	}
	installed, err := cfg.installedJSON()
	if err != nil {
		return nil, err
	}
	unit := RenderUnit()
	if err := ValidateUnit(unit); err != nil {
		return nil, err
	}
	files := map[string][]byte{
		RegistrationPath: registration, RegistrationSignaturePath: signature, RegistrationPublicKeyPath: key,
		ServiceConfigPath: serviceConfig, SetupConfigPath: installed, UnitPath: unit,
	}
	if binary != "" {
		if files[BinaryPath], err = readBounded(binary, maxBinaryBytes); err != nil {
			return nil, err
		}
	}
	return files, nil
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
				if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
					return err
				}
				if err := os.Mkdir(real, n.Mode); err != nil {
					return err
				}
				if err := os.Chmod(real, n.Mode); err != nil { // the umask must not decide the mode
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
			if err := os.Chmod(real, n.Mode); err != nil {
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
			if err := writeFile(real, data, n.Mode); err != nil {
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
			if err := os.Chmod(real, n.Mode); err != nil {
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
