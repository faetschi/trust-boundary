//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

const productImageUsage = `Usage:
  tbound product image template-digest --template FILE
  tbound product image provision --archive FILE --signed-layout DIR [--dry-run]
template-digest prints the canonical product template digest; it grants no host authority.
provision runs as root with the product service stopped and no unresolved turn. Inputs must
be root-owned, non-symlink and not group/world writable, including their ancestors.
It verifies the original OCI archive and signed layout offline, then loads, tags by config ID,
and inspects in fresh tbound-product transient contexts with NoNewPrivileges and no network.
The registration, template, signed OCI manifest and imported Digest/RepoDigest must agree.
No pull, store reset, repin or service start is performed. --dry-run runs no cosign or Podman
and establishes static validity only. Owner signing remains offline.
Exit codes: 0 success, 2 refused or invalid usage, 1 other.
`

func runProductImage(args []string, out, stderr io.Writer) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help") {
		_, err := io.WriteString(out, productImageUsage)
		return err
	}
	if len(args) > 0 {
		switch args[0] {
		case "template-digest":
			return productImageTemplateDigestCommand(args[1:], out)
		case "provision":
			return productImageProvisionCommand(args[1:], out)
		}
	}
	io.WriteString(stderr, productImageUsage)
	return adminRefusal("invalid image command or flags")
}

func productImageTemplateDigestCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("product image template-digest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("template", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *name == "" {
		return adminRefusal("%s", productImageUsage)
	}
	f, err := os.Open(*name)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return adminRefusal("product template exceeds 1 MiB")
	}
	template, err := piruntime.DecodeProductD09CellTemplate(raw)
	if err != nil {
		return adminRefusal("%v", err)
	}
	digest, err := template.Digest()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, digest)
	return err
}

func productImageProvisionCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("product image provision", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	archive := flags.String("archive", "", "")
	layout := flags.String("signed-layout", "", "")
	dry := flags.Bool("dry-run", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *archive == "" || *layout == "" {
		return adminRefusal("%s", productImageUsage)
	}
	if os.Geteuid() != 0 {
		return adminRefusal("image provision must run as root, including --dry-run")
	}
	env := productAdminEnv("/", *dry, false, false, out, io.Discard)
	return productImageProvision(env, *archive, *layout, out)
}

type productImagePlan struct {
	root                    string
	registration            productsession.Registration
	inputs                  productCommandInputs
	profile                 piruntime.HostProfile
	template                piruntime.D09CellTemplate
	uid, gid                int
	configID, archiveDigest string
	layoutFiles             map[string]productOCIFile
	storeRecord             []byte
}

func productImageExec(env productsetup.Env, name string, args ...string) ([]byte, error) {
	if env.Exec != nil {
		return env.Exec(name, args...)
	}
	// Bound output from tools and systemd-run: diagnostics must not exhaust root's memory.
	var stdout, stderr productImageOutput
	if name == "systemctl" || name == "systemd-run" {
		name = "/usr/bin/" + name
	}
	cmd := exec.Command(name, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("image command exceeded its 1 MiB output bound")
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

type productImageOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *productImageOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 1<<20 {
		b.overflow = true
		return n, nil
	}
	b.Buffer.Write(p)
	return n, nil
}

func productImageStopped(env productsetup.Env) error {
	args := []string{"show", productsetup.UnitName, "--no-pager"}
	for _, property := range []string{"LoadState", "ActiveState", "NeedDaemonReload", "FragmentPath", "DropInPaths", "EnvironmentFiles", "Environment", "User", "Group"} {
		args = append(args, "--property="+property)
	}
	raw, err := productImageExec(env, "systemctl", args...)
	if err != nil {
		return fmt.Errorf("cannot establish stopped product service: %w", err)
	}
	var load, active string
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "LoadState="); ok {
			load = v
		}
		if v, ok := strings.CutPrefix(line, "ActiveState="); ok {
			active = v
		}
	}
	if load != "loaded" || active != "inactive" {
		return errors.New("product service must be installed and stopped before image provision")
	}
	properties := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			properties[key] = value
		}
	}
	for key, want := range map[string]string{"NeedDaemonReload": "no", "FragmentPath": productsetup.UnitPath, "DropInPaths": "", "EnvironmentFiles": "", "User": productsetup.ServiceUser, "Group": productsetup.ServiceGroup} {
		if got, ok := properties[key]; !ok || got != want {
			return fmt.Errorf("effective product unit differs at %s", key)
		}
	}
	environment := strings.Split(properties["Environment"], " ")
	slices.Sort(environment)
	wantEnvironment := productImageEnvironment()
	slices.Sort(wantEnvironment)
	if !slices.Equal(environment, wantEnvironment) {
		return errors.New("effective product environment differs from execution")
	}
	if env.Turns == nil {
		return errors.New("unresolved-turn check is unavailable")
	}
	turns, err := env.Turns()
	if err != nil {
		return err
	}
	if len(turns) != 0 {
		return fmt.Errorf("image provision refused: %d unresolved turn(s)", len(turns))
	}
	return nil
}

func productImageLoadPlan(env productsetup.Env, archive, layout string) (productImagePlan, error) {
	p := productImagePlan{root: env.Root}
	if p.root == "" {
		p.root = "/"
	}
	real := func(name string) string { return filepath.Join(p.root, name) }
	read := func(name string, limit int64) ([]byte, error) { return productImageRead(p.root, real(name), limit) }
	var cfg productServiceConfig
	raw, err := read(productsetup.ServiceConfigPath, productServiceMaxConfigBytes)
	if err != nil {
		return p, err
	}
	if err := unmarshalGovernedJSON(raw, &cfg); err != nil {
		return p, err
	}
	for name, dest := range map[string]*[]byte{productsetup.RegistrationPath: &p.inputs.Registration, productsetup.RegistrationSignaturePath: &p.inputs.RegistrationSignature, productsetup.RegistrationPublicKeyPath: &p.inputs.PublicKey, productsetup.CommandHostProfilePath: &p.inputs.Profile, productsetup.CommandHostProfileSignaturePath: &p.inputs.ProfileSignature, productsetup.CommandCellTemplatePath: &p.inputs.Template, productsetup.CommandSeccompPath: &p.inputs.Seccomp, productsetup.CommandCosignPublicKeyPath: &p.inputs.CosignPublicKey, productsetup.UnitPath: &p.inputs.Unit} {
		*dest, err = read(name, 1<<20)
		if err != nil {
			return p, err
		}
	}
	key, err := decodeProductPublicKey(p.inputs.PublicKey)
	if err != nil {
		return p, err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(p.inputs.RegistrationSignature)))
	if err != nil {
		return p, err
	}
	p.registration, err = productsession.VerifyRegistration(p.inputs.Registration, sig, ed25519.PublicKey(key))
	if err != nil {
		return p, err
	}
	if cfg.RegistrationPath != productsetup.RegistrationPath || cfg.RegistrationSignaturePath != productsetup.RegistrationSignaturePath || cfg.RegistrationPublicKeyPath != productsetup.RegistrationPublicKeyPath {
		return p, errors.New("image provision requires canonical installed registration paths")
	}
	p.profile, p.template, err = validateProductCommandInputs(cfg, p.registration, p.inputs, p.root)
	if err != nil {
		return p, err
	}
	for name, want := range map[string][]byte{productsetup.ContainersConfPath: productsetup.RenderProductContainersConf(), productsetup.StorageConfPath: productsetup.RenderProductStorageConf()} {
		got, err := read(name, 64<<10)
		if err != nil {
			return p, err
		}
		if !bytes.Equal(got, want) {
			return p, fmt.Errorf("%s differs from execution config; do not reset the store", name)
		}
	}
	if err := productImageNoUnitOverrides(p.root); err != nil {
		return p, err
	}
	p.uid, p.gid, err = productImageAccount(p.root)
	if err != nil {
		return p, err
	}
	if p.template.XattrScanUID != p.uid || p.template.XattrScanGID != p.gid || p.template.UserNamespaceMappingDigest != piruntime.DigestD09UserNamespaceMapping(p.template.CellUID, p.template.CellGID, p.uid, p.gid) {
		return p, errors.New("signed single own-ID mapping differs from the installed service account")
	}
	// The config ID is measured from the shipped archive, never inferred from a tag or load output.
	archiveFiles, archiveDigest, err := productOCIArchive(p.root, archive)
	if err != nil {
		return p, err
	}
	p.archiveDigest = archiveDigest
	if err := productOCIArchiveMetadata(archive, archiveFiles, p.template.ExpectedImageDigest); err != nil {
		return p, err
	}
	p.configID, err = productOCIIdentity(archiveFiles, p.registration.ImageDigest)
	if err != nil {
		return p, err
	}
	p.layoutFiles, err = productOCILayout(p.root, layout)
	if err != nil {
		return p, err
	}
	configID, err := productOCIIdentity(p.layoutFiles, p.template.ExpectedImageDigest)
	if err != nil {
		return p, err
	}
	if configID != p.configID {
		return p, errors.New("archive and signed layout have different image config IDs")
	}
	p.storeRecord, err = json.Marshal(struct {
		Schema, Mapping, Containers, Storage string
		UID, GID                             int
	}{"tbound-product-image-store/v1", p.template.UserNamespaceMappingDigest, piruntime.DigestBytes(productsetup.RenderProductContainersConf()), piruntime.DigestBytes(productsetup.RenderProductStorageConf()), p.uid, p.gid})
	if err != nil {
		return p, err
	}
	if err := p.checkStore(); err != nil {
		return p, err
	}
	for _, tool := range []struct{ path, digest string }{{p.template.PodmanPath, p.template.PodmanDigest}, {p.template.CrunPath, p.template.CrunDigest}, {p.template.CosignPath, p.template.CosignDigest}} {
		name := tool.path
		if p.root != "/" {
			name = real(name)
		}
		if err := productImageProtectedPath(p.root, name, false); err != nil {
			return p, err
		}
		f, err := os.Open(name)
		if err != nil {
			return p, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return p, err
		}
		measured, err := productOCIHash(f, info.Size(), false)
		f.Close()
		if err != nil || measured.digest != tool.digest || info.Mode()&0111 == 0 {
			return p, fmt.Errorf("pinned tool differs: %s", tool.path)
		}
	}
	return p, nil
}

func productImageAccount(root string) (int, int, error) {
	read := func(p string) ([]byte, error) { return productImageRead(root, filepath.Join(root, p), 1<<20) }
	passwd, err := read("/etc/passwd")
	if err != nil {
		return 0, 0, err
	}
	groups, err := read("/etc/group")
	if err != nil {
		return 0, 0, err
	}
	uid, gid := 0, 0
	found := false
	for _, line := range strings.Split(string(passwd), "\n") {
		f := strings.Split(line, ":")
		if len(f) == 7 && f[0] == productsetup.ServiceUser {
			if found || f[6] != "/usr/sbin/nologin" {
				return 0, 0, errors.New("invalid product account")
			}
			uid, _ = strconv.Atoi(f[2])
			gid, _ = strconv.Atoi(f[3])
			found = true
		}
	}
	groupOK := false
	for _, line := range strings.Split(string(groups), "\n") {
		f := strings.Split(line, ":")
		if len(f) == 4 && f[0] == productsetup.ServiceGroup {
			n, _ := strconv.Atoi(f[2])
			if groupOK || n != gid {
				return 0, 0, errors.New("product group must match the service account GID")
			}
			groupOK = true
		}
	}
	if !found || !groupOK || uid <= 0 || gid <= 0 {
		return 0, 0, errors.New("non-root nologin product account is required")
	}
	for _, p := range []string{"/etc/subuid", "/etc/subgid"} {
		raw, err := read(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			owner, _, _ := strings.Cut(line, ":")
			if owner == productsetup.ServiceUser || owner == strconv.Itoa(uid) || owner == strconv.Itoa(gid) {
				return 0, 0, fmt.Errorf("product account has subordinate IDs in %s; remove the allocation explicitly before provisioning", p)
			}
		}
	}
	return uid, gid, nil
}

const productImageStoreRecord = productsetup.PodmanConfigDir + "/store.json"
const productImageStoreRecovery = "stop the service, preserve retained evidence, explicitly relocate the incompatible Podman home and graphroot, recreate the empty setup-owned directories, then provision again; no store was reset"

func (p productImagePlan) checkStore() error {
	for _, dir := range []string{productsetup.PodmanHomeDir, productsetup.PodmanStorageDir, productsetup.PodmanGraphRootDir} {
		uid, gid := p.uid, p.gid
		if p.root != "/" {
			uid, gid = os.Geteuid(), os.Getegid()
		}
		if err := requireCommandRuntimeDirectory(filepath.Join(p.root, dir), 0700, uid, gid); err != nil {
			return err
		}
	}
	name := filepath.Join(p.root, productImageStoreRecord)
	raw, err := productImageRead(p.root, name, 64<<10)
	if err == nil {
		if !bytes.Equal(raw, p.storeRecord) {
			return fmt.Errorf("incompatible store configuration/mapping: %s", productImageStoreRecovery)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, dir := range []string{productsetup.PodmanHomeDir, productsetup.PodmanGraphRootDir} {
		name := filepath.Join(p.root, dir)
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return errors.New("Podman home and graphroot must be private real setup-owned directories")
		}
		entries, err := os.ReadDir(name)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("nonempty store has no verified mapping record: %s", productImageStoreRecovery)
		}
	}
	return nil
}

// Every import operation starts from the same closed unit contract, in a fresh
// process. PrivateNetwork is added only to these offline transient services.
func productImageContextArgs(binary string, args ...string) []string {
	result := []string{"--quiet", "--wait", "--collect", "--pipe", "--expand-environment=no", "--service-type=exec"}
	section := ""
	for _, line := range strings.Split(string(productsetup.RenderUnit()), "\n") {
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section != "[Service]" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ExecStart", "Restart", "RestartSec", "Environment":
			continue
		}
		result = append(result, "--property="+key+"="+value)
	}
	result = append(result, "--property=PrivateNetwork=yes", "--property=ExecStartPre=/usr/bin/mkdir -p -m 0700 "+productsetup.PodmanTmpDir+" "+productsetup.PodmanCacheDir+" "+productsetup.PodmanRunrootDir)
	result = append(result, "--", "/usr/bin/env", "-i")
	result = append(result, productImageEnvironment()...)
	result = append(result, binary)
	return append(result, args...)
}

func productImageProvision(env productsetup.Env, archive, layout string, out io.Writer) error {
	refuse := func(err error) error { return adminRefusal("image provision: %v", err) }
	if err := productImageStopped(env); err != nil {
		return refuse(err)
	}
	p, err := productImageLoadPlan(env, archive, layout)
	if err != nil {
		return refuse(err)
	}
	fmt.Fprintf(out, "archive transfer SHA-256 %s\nsigned image manifest %s\nmeasured config ID %s\n", p.archiveDigest, p.template.ExpectedImageDigest, p.configID)
	if env.DryRun {
		fmt.Fprintln(out, "dry run: static inputs verified; offline cosign, load, tag and fresh-context inspection planned, not run; readiness NOT established")
		return nil
	}
	real := func(name string) string { return filepath.Join(p.root, name) }
	if err := productImageProtectedPath(p.root, real(productsetup.PodmanConfigDir), true); err != nil {
		return refuse(err)
	}
	lock, err := os.OpenFile(real(productsetup.PodmanConfigDir+"/.provision.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return refuse(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return refuse(errors.New("another image provision is running"))
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if err := productImageStopped(env); err != nil {
		return refuse(err)
	}
	run := func(binary string, args ...string) ([]byte, error) {
		return productImageExec(env, "systemd-run", productImageContextArgs(binary, args...)...)
	}
	// Source files and all their ancestors are protected. Only root can replace
	// them between measurement and these copies; root is already trusted to install.
	stage, err := os.MkdirTemp(real(productsetup.PodmanConfigDir), ".image-provision-")
	if err != nil {
		return refuse(err)
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0755); err != nil {
		return refuse(err)
	}
	stagedArchive := filepath.Join(stage, "image.tar")
	if err := productImageCopy(archive, stagedArchive, productImageMaxBytes); err != nil {
		return refuse(err)
	}
	stagedLayout := filepath.Join(stage, "cell-oci")
	if err := productImageCopyLayout(layout, stagedLayout, p.layoutFiles); err != nil {
		return refuse(err)
	}
	raw, err := run(p.template.CosignPath, "verify", "--offline", "--insecure-ignore-tlog=true", "--key", productsetup.CommandCosignPublicKeyPath, "--local-image", stagedLayout)
	if err != nil {
		return refuse(err)
	}
	if err := productImageSignatureClaim(raw, p.registration.ImageDigest); err != nil {
		return refuse(err)
	}
	if err := p.checkStore(); err != nil {
		return refuse(err)
	}
	if err := productImageWriteOnce(real(productImageStoreRecord), p.storeRecord); err != nil {
		return refuse(err)
	}
	if _, err := run(p.template.PodmanPath, "load", "--input", stagedArchive); err != nil {
		return refuse(fmt.Errorf("offline load failed (no reset or repin): %w; %s", err, productImageStoreRecovery))
	}
	if _, err := run(p.template.PodmanPath, "tag", strings.TrimPrefix(p.configID, "sha256:"), p.template.Image); err != nil {
		return refuse(err)
	}
	if _, err := run(p.template.PodmanPath, "image", "exists", p.template.Image); err != nil {
		return refuse(err)
	}
	raw, err = run(p.template.PodmanPath, "image", "inspect", p.template.Image)
	if err != nil {
		return refuse(err)
	}
	if err := productImageImportedIdentity(raw, p.configID, p.template.ExpectedImageDigest); err != nil {
		return refuse(err)
	}
	// Retain immutable public bindings for later historical command verification.
	profileDigest := p.profile.Digest
	if !piruntime.ValidD09Digest(profileDigest) {
		return refuse(errors.New("signed host profile has no digest"))
	}
	history := real(productsetup.PodmanConfigDir + "/hosts/" + strings.TrimPrefix(profileDigest, "sha256:"))
	if err := productImageHistory(p, history); err != nil {
		return refuse(err)
	}
	destination := real(productsetup.CommandOCILayoutPath)
	if _, err := os.Lstat(destination); err == nil {
		old, err := productOCILayout(p.root, destination)
		if err != nil {
			return refuse(err)
		}
		if _, err := productOCIIdentity(old, p.template.ExpectedImageDigest); err != nil {
			previous, err := productImageRead(p.root, real(productsetup.PodmanConfigDir+"/current-image"), 128)
			if err != nil || !piruntime.ValidD09Digest(string(previous)) {
				return refuse(errors.New("installed layout has no protected previous manifest record; retain and relocate it explicitly"))
			}
			if _, err := productOCIIdentity(old, string(previous)); err != nil {
				return refuse(err)
			}
			retained := real(productsetup.PodmanConfigDir + "/images/" + strings.TrimPrefix(string(previous), "sha256:"))
			if err := productImageMkdir(p.root, filepath.Dir(retained)); err != nil {
				return refuse(err)
			}
			if _, err := os.Lstat(retained); errors.Is(err, os.ErrNotExist) {
				if err := productImageCopyLayout(destination, retained, old); err != nil {
					return refuse(err)
				}
			} else if err != nil {
				return refuse(err)
			}
			retainedFiles, err := productOCILayout(p.root, retained)
			if err != nil {
				return refuse(err)
			}
			if _, err := productOCIIdentity(retainedFiles, string(previous)); err != nil {
				return refuse(err)
			}
			if err := unix.Renameat2(unix.AT_FDCWD, stagedLayout, unix.AT_FDCWD, destination, unix.RENAME_EXCHANGE); err != nil {
				return refuse(fmt.Errorf("atomic OCI layout replacement: %w", err))
			}
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stagedLayout, destination); err != nil {
			return refuse(err)
		}
	} else {
		return refuse(err)
	}
	if err := productImageReplaceRecord(real(productsetup.PodmanConfigDir+"/current-image"), []byte(p.template.ExpectedImageDigest)); err != nil {
		return refuse(err)
	}
	fmt.Fprintf(out, "provisioned %s; registration == template == signed OCI manifest == imported Digest/RepoDigest; service remains stopped\n", p.template.ExpectedImageDigest)
	return nil
}

func productImageSignatureClaim(raw []byte, expected string) error {
	if len(raw) > 1<<20 {
		return errors.New("cosign output exceeds bound")
	}
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return err
	}
	var claims []struct {
		Critical struct {
			Image struct {
				Digest string `json:"docker-manifest-digest"`
			} `json:"image"`
		} `json:"critical"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return err
	}
	if len(claims) == 0 {
		return errors.New("cosign returned no signed image claim")
	}
	for _, c := range claims {
		if c.Critical.Image.Digest != expected {
			return errors.New("cosign image claim differs from the registered manifest")
		}
	}
	return nil
}
func productImageImportedIdentity(raw []byte, configID, expected string) error {
	if len(raw) > 1<<20 {
		return errors.New("image inspect exceeds bound")
	}
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return err
	}
	var images []struct {
		ID          string   `json:"Id"`
		Digest      string   `json:"Digest"`
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(raw, &images); err != nil {
		return err
	}
	if len(images) != 1 || "sha256:"+strings.TrimPrefix(images[0].ID, "sha256:") != configID || images[0].Digest != expected {
		return errors.New("imported config ID or manifest Digest differs; never repin a rewritten manifest")
	}
	for _, repo := range images[0].RepoDigests {
		_, digest, ok := strings.Cut(repo, "@")
		if ok && digest == expected {
			return nil
		}
	}
	return errors.New("imported RepoDigest differs from the signed manifest")
}

func productImageCopy(source, dest string, limit int64) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, limit+1))
	if err == nil && n > limit {
		err = errors.New("image copy exceeds bound")
	}
	return errors.Join(err, out.Chmod(0644), out.Sync(), out.Close())
}
func productImageCopyLayout(source, dest string, files map[string]productOCIFile) error {
	for _, dir := range []string{dest, filepath.Join(dest, "blobs"), filepath.Join(dest, "blobs", "sha256")} {
		if err := os.Mkdir(dir, 0755); err == nil {
			if err := os.Chmod(dir, 0755); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	for name := range files {
		if err := productImageCopy(filepath.Join(source, name), filepath.Join(dest, name), productImageMaxBytes); err != nil {
			return err
		}
	}
	return nil
}
func productImageWriteOnce(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0644)
	if errors.Is(err, os.ErrExist) {
		raw, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, data) {
			return fmt.Errorf("immutable image artifact differs: %s", name)
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Chmod(0644), f.Sync(), f.Close())
}
func productImageHistory(p productImagePlan, dir string) error {
	if err := productImageMkdir(p.root, dir); err != nil {
		return err
	}
	for name, raw := range map[string][]byte{"host-profile.json": p.inputs.Profile, "host-profile.json.sig": p.inputs.ProfileSignature, "d09-cell.json": p.inputs.Template, "registration.json": p.inputs.Registration, "registration.json.sig": p.inputs.RegistrationSignature, "registration.pub": p.inputs.PublicKey, "seccomp.json": p.inputs.Seccomp, "cosign.pub": p.inputs.CosignPublicKey} {
		dest := filepath.Join(dir, name)
		if _, err := os.Lstat(dest); err == nil {
			if err := productImageProtectedPath(p.root, dest, false); err != nil {
				return err
			}
		}
		if err := productImageWriteOnce(dest, raw); err != nil {
			return err
		}
	}
	return nil
}

func productImageMkdir(root, dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		if err := productImageMkdir(root, filepath.Dir(dir)); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0755); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0755); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return productImageProtectedPath(root, dir, true)
}

func productImageEnvironment() []string {
	for _, line := range strings.Split(string(productsetup.RenderUnit()), "\n") {
		if value, ok := strings.CutPrefix(line, "Environment="); ok {
			return strings.Fields(value)
		}
	}
	return nil
}

// This mirrors the product loader's closed override locations without invoking
// its live admission (which requires the image this command is installing).
func productImageNoUnitOverrides(root string) error {
	for _, base := range []string{"/etc/systemd/system", "/run/systemd/system", "/usr/local/lib/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system", "/etc/systemd/system.control", "/run/systemd/system.control", "/run/systemd/transient"} {
		for _, suffix := range []string{productsetup.UnitName + ".d", "service.d"} {
			name := filepath.Join(root, base, suffix)
			info, err := os.Lstat(name)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("product unit drop-in directory is symlinked or not a directory")
			}
			entries, err := os.ReadDir(name)
			if err != nil {
				return err
			}
			if len(entries) > 0 {
				return fmt.Errorf("product unit drop-ins are refused: %s", name)
			}
		}
	}
	for _, base := range []string{"/run/systemd/system", "/run/systemd/system.control", "/run/systemd/transient", "/run/systemd/generator", "/run/systemd/generator.early", "/run/systemd/generator.late"} {
		if _, err := os.Lstat(filepath.Join(root, base, productsetup.UnitName)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("a shadow unit overrides the installed product unit")
		}
	}
	return nil
}

func productImageReplaceRecord(name string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".image-record-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	err = errors.Join(err, f.Chmod(0644), f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), name); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
