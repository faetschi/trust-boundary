package productsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// commandProvisionChecks checks the public replay artifacts provision retains for the installed profile.
func (h *host) commandProvisionChecks() []Check {
	raw, err := readSource(h.real(CommandHostProfilePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	c := Check{Name: "command profile provision", Status: Fail, Fix: "run tbound product image provision"}
	var profile struct{ Digest string }
	if err != nil || json.Unmarshal(raw, &profile) != nil || !sha256Digest.MatchString(profile.Digest) {
		c.Detail = "installed command profile is unreadable or has no valid digest"
		return []Check{c}
	}
	base := filepath.Join(PodmanConfigDir, "hosts", strings.TrimPrefix(profile.Digest, "sha256:"))
	for _, path := range []string{CommandHostProfilePath, CommandHostProfileSignaturePath, CommandCellTemplatePath, CommandSeccompPath, CommandCosignPublicKeyPath, RegistrationPath, RegistrationSignaturePath, RegistrationPublicKeyPath} {
		retained := filepath.Join(base, filepath.Base(path))
		got, err := readSource(h.real(retained))
		want, installedErr := readSource(h.real(path))
		if err != nil || installedErr != nil || !bytes.Equal(got, want) {
			c.Detail = retained + " is missing, unreadable or differs from the installed public input"
			return []Check{c}
		}
	}
	c.Status, c.Detail, c.Fix = Pass, "installed profile has matching retained public inputs", ""
	return []Check{c}
}

// commandBundleFiles reads only the public files setup owns. The same bytes reach validation and installation.
func (h *host) commandBundleFiles(source string, files map[string][]byte) error {
	if source == "" {
		for _, p := range []string{CommandOCILayoutPath, PodmanConfigDir + "/hosts", PodmanConfigDir + "/images", PodmanConfigDir + "/store.json"} {
			if _, err := os.Lstat(h.real(p)); !errors.Is(err, os.ErrNotExist) {
				return refuse("retained command obligations require a command bundle")
			}
		}
		for _, n := range installedFiles {
			if isCommandArtifact(n.Path) {
				if _, err := os.Lstat(h.real(n.Path)); !errors.Is(err, os.ErrNotExist) {
					return refuse("a replacement with retained command artifacts requires a command bundle")
				}
			}
		}
		return nil
	}
	// External sources are rooted at / on the live host. Test roots may also use fixture sources outside the root.
	boundary := "/"
	if h.root != "/" {
		boundary = h.root
		if !PathInside(source, boundary) {
			boundary = filepath.Dir(source)
		}
	}
	check := newHost(h.env)
	check.root = boundary
	check.env.Live = h.root == "/"
	if err := check.openRoot(); err != nil {
		return err
	}
	defer check.dir.Close()
	logical, err := filepath.Rel(boundary, source)
	if err != nil {
		return err
	}
	if err := check.protectedDirectory(logical); err != nil {
		return refuse("command bundle directory must be protected and non-symlink: %v", err)
	}
	for _, n := range installedFiles {
		if !isCommandArtifact(n.Path) {
			continue
		}
		f, err := openSource(filepath.Join(source, filepath.Base(n.Path)))
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		uid, _, ok := ownerOf(info)
		if !info.Mode().IsRegular() || modeBits(info)&0022 != 0 || !ok || (h.root == "/" && uid != 0) || info.Size() > maxSourceBytes {
			err = refuse("command bundle artifact %s must be a bounded root-protected regular file", n.Path)
		}
		if err == nil {
			files[n.Path], err = io.ReadAll(io.LimitReader(f, maxSourceBytes+1))
			if len(files[n.Path]) > maxSourceBytes {
				err = refuse("command bundle artifact exceeds its size bound")
			}
		}
		closeErr := f.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return err
		}
	}
	return nil
}

func (h *host) commandInputs(cfg Config, summary RegistrationSummary, files map[string][]byte) HostInputs {
	in := h.hostInputs(cfg, summary, files[RegistrationPublicKeyPath], cfg.RegistrationPublicKey)
	in.Root, in.CommandBundlePath = h.root, cfg.CommandBundle
	in.RegistrationData, in.RegistrationSignature = files[RegistrationPath], files[RegistrationSignaturePath]
	in.ServiceConfig, in.Unit = files[ServiceConfigPath], files[UnitPath]
	in.ContainersConfig, in.StorageConfig = files[ContainersConfPath], files[StorageConfPath]
	in.CommandFiles = files
	return in
}

func (h *host) installedCommandInputChecks(summary RegistrationSummary) []Check {
	if h.env.ValidateCommandInputs == nil {
		return nil
	}
	c := Check{Name: "command bundle", Status: Pass, Detail: "static validity only; command readiness not checked"}
	check := func() error {
		record, err := readSource(h.real(SetupConfigPath))
		if err != nil {
			return err
		}
		cfg, err := parseInstalled(record)
		if err != nil {
			return err
		}
		files := map[string][]byte{}
		for _, p := range []string{RegistrationPath, RegistrationSignaturePath, RegistrationPublicKeyPath, ServiceConfigPath, UnitPath, ContainersConfPath, StorageConfPath} {
			files[p], err = readSource(h.real(p))
			if err != nil {
				return err
			}
		}
		for _, n := range installedFiles {
			if isCommandArtifact(n.Path) {
				if _, err := os.Lstat(h.real(n.Path)); !errors.Is(err, os.ErrNotExist) {
					cfg.CommandBundle = h.real(PodmanConfigDir)
					break
				}
			}
		}
		if err := h.commandBundleFiles(cfg.CommandBundle, files); err != nil {
			return err
		}
		return h.env.ValidateCommandInputs(h.commandInputs(cfg, summary, files))
	}
	if err := check(); err != nil {
		c.Status, c.Detail, c.Fix = Fail, err.Error(), "install matching signed public command inputs with `tbound product setup --replace`"
	}
	return []Check{c}
}
