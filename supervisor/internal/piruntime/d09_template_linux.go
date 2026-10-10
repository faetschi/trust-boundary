//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
)

// The registered D09 cell template is the only source of D09 image, entrypoint,
// static seccomp, Landlock, capability, resource-bound, runtime, and cgroup
// identity. It is read only from a protected, root-owned path, and its canonical
// digest must equal the signed host profile's ContainmentProfileDigest. Every
// measured identity field is required to be non-empty; a draft template with
// unmeasured fields is never admissible.

const (
	// D09CellTemplatePath is the registered, root-owned D09 cell template.
	D09CellTemplatePath = "/etc/tbound/d09-cell.json"
	maxD09TemplateBytes = 1 << 20
)

// ErrD09TemplateInvalid reports a missing, unprotected, malformed, or
// unmeasured D09 cell template. It wraps ErrProductionLauncherUnavailable.
var ErrD09TemplateInvalid = errors.New("registered D09 cell template is missing or invalid")

// D09CellTemplate is the registered, measured description of the frozen D09
// cell. It deliberately has no placeholder fields: an unmeasured template fails
// validation and can never be bound into a signed profile.
type D09CellTemplate struct {
	Image                             string `json:"image"`
	ExpectedImageDigest               string `json:"expected_image_digest"`
	CosignPublicKeyPath               string `json:"cosign_public_key_path"`
	CosignPublicKeyDigest             string `json:"cosign_public_key_digest"`
	OCILayoutPath                     string `json:"oci_layout_path"`
	SeccompProfilePath                string `json:"seccomp_profile_path"`
	SeccompProfileDigest              string `json:"seccomp_profile_digest"`
	LandlockABI                       int    `json:"landlock_abi"`
	LandlockRulesetDigest             string `json:"landlock_ruleset_digest"`
	LandlockROPath                    string `json:"landlock_ro_path"`
	LandlockRWPath                    string `json:"landlock_rw_path"`
	FrozenCapabilities                string `json:"frozen_capabilities"`
	EntrypointPath                    string `json:"entrypoint_path"`
	EntrypointDigest                  string `json:"entrypoint_digest"`
	Runtime                           string `json:"runtime"`
	CgroupsPath                       string `json:"cgroups_path"`
	CPUs                              string `json:"cpus"`
	PidsMax                           int    `json:"pids_max"`
	MemoryMB                          int    `json:"memory_mb"`
	PodmanPath                        string `json:"podman_path"`
	PodmanDigest                      string `json:"podman_digest"`
	CrunPath                          string `json:"crun_path"`
	CrunDigest                        string `json:"crun_digest"`
	CosignPath                        string `json:"cosign_path"`
	CosignDigest                      string `json:"cosign_digest"`
	CommandExecutable                 string `json:"command_executable"`
	CommandExecutableDigest           string `json:"command_executable_digest"`
	CellUID                           int    `json:"cell_uid"`
	CellGID                           int    `json:"cell_gid"`
	NamespaceProfile                  string `json:"namespace_profile"`
	CgroupVersion                     string `json:"cgroup_version"`
	CgroupControllers                 string `json:"cgroup_controllers"`
	XattrVisibilityProfileDigest      string `json:"xattr_visibility_profile_digest"`
	XattrVisibilityAuthority          string `json:"xattr_visibility_authority"`
	XattrFilesystemMagic              uint64 `json:"xattr_filesystem_magic"`
	XattrMountOptionsDigest           string `json:"xattr_mount_options_digest"`
	XattrScanUID                      int    `json:"xattr_scan_uid"`
	XattrScanGID                      int    `json:"xattr_scan_gid"`
	XattrScanPrivilegeProfile         string `json:"xattr_scan_privilege_profile"`
	ManagedLiveRootRegistrationDigest string `json:"managed_live_root_registration_digest"`
	UserNamespaceMappingProfile      string `json:"user_namespace_mapping_profile"`
	UserNamespaceMappingDigest       string `json:"user_namespace_mapping_digest"`
	registered                        bool
}

// Digest returns the canonical template digest that a signed profile binds in
// its ContainmentProfileDigest.
func (t D09CellTemplate) Digest() (string, error) {
	encoded, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize D09 cell template: %v", ErrD09TemplateInvalid, err)
	}
	return DigestBytes(encoded), nil
}

// Config maps the registered template to the launcher configuration. No value
// is taken from an unsigned CLI argument.
func (t D09CellTemplate) Config() D09Config {
	return D09Config{
		Image:                             t.Image,
		ExpectedImageDigest:               t.ExpectedImageDigest,
		CosignPublicKeyPath:               t.CosignPublicKeyPath,
		CosignPublicKeyDigest:             t.CosignPublicKeyDigest,
		OCILayoutPath:                     t.OCILayoutPath,
		SeccompProfilePath:                t.SeccompProfilePath,
		SeccompProfileDigest:              t.SeccompProfileDigest,
		LandlockABI:                       t.LandlockABI,
		LandlockRulesetDigest:             t.LandlockRulesetDigest,
		LandlockROPath:                    t.LandlockROPath,
		LandlockRWPath:                    t.LandlockRWPath,
		FrozenCapabilities:                t.FrozenCapabilities,
		EntrypointPath:                    t.EntrypointPath,
		EntrypointDigest:                  t.EntrypointDigest,
		Runtime:                           t.Runtime,
		CgroupsPath:                       t.CgroupsPath,
		CPUs:                              t.CPUs,
		PidsMax:                           t.PidsMax,
		MemoryMB:                          t.MemoryMB,
		PodmanPath:                        t.PodmanPath,
		PodmanDigest:                      t.PodmanDigest,
		CrunPath:                          t.CrunPath,
		CrunDigest:                        t.CrunDigest,
		CosignPath:                        t.CosignPath,
		CosignDigest:                      t.CosignDigest,
		TemplateDigest:                    templateDigestOrEmpty(t),
		CommandExecutable:                 t.CommandExecutable,
		CommandExecutableDigest:           t.CommandExecutableDigest,
		ManagedLiveRootRegistrationDigest: t.ManagedLiveRootRegistrationDigest,
		UserNamespaceMappingProfile:      t.UserNamespaceMappingProfile,
		UserNamespaceMappingDigest:       t.UserNamespaceMappingDigest,
		CellUID:                           t.CellUID,
		CellGID:                           t.CellGID,
		NamespaceProfile:                  t.NamespaceProfile,
		CgroupVersion:                     t.CgroupVersion,
		CgroupControllers:                 t.CgroupControllers,
		XattrVisibilityProfileDigest:      t.XattrVisibilityProfileDigest,
		XattrVisibilityAuthority:          t.XattrVisibilityAuthority,
		registeredTemplate:                t.registered,
		XattrFilesystemMagic:              t.XattrFilesystemMagic,
		XattrMountOptionsDigest:           t.XattrMountOptionsDigest,
		XattrScanUID:                      t.XattrScanUID,
		XattrScanGID:                      t.XattrScanGID,
		XattrScanPrivilegeProfile:         t.XattrScanPrivilegeProfile,
	}
}

func templateDigestOrEmpty(t D09CellTemplate) string {
	digest, err := t.Digest()
	if err != nil {
		return ""
	}
	return digest
}

// validateD09CellTemplate checks that every identity the launcher relies on is
// present, well-formed, and measured. An unmeasured draft fails.
func validateD09CellTemplate(t D09CellTemplate) error {
	roPaths, roErr := ParseD09LandlockPaths(t.LandlockROPath)
	rwPaths, rwErr := ParseD09LandlockPaths(t.LandlockRWPath)
	landlockDigest, landlockErr := DigestD09LandlockRules(roPaths, rwPaths)
	if strings.TrimSpace(t.Image) == "" || !validDigest(t.ExpectedImageDigest) ||
		!filepath.IsAbs(t.CosignPublicKeyPath) || !validDigest(t.CosignPublicKeyDigest) || !filepath.IsAbs(t.OCILayoutPath) ||
		!filepath.IsAbs(t.SeccompProfilePath) || !validDigest(t.SeccompProfileDigest) ||
		t.LandlockABI != D09LandlockABI || !validDigest(t.LandlockRulesetDigest) || roErr != nil || rwErr != nil || landlockErr != nil || landlockDigest != t.LandlockRulesetDigest ||
		strings.TrimSpace(t.FrozenCapabilities) != d09EmptyCapabilitySet ||
		!filepath.IsAbs(t.EntrypointPath) || !validDigest(t.EntrypointDigest) ||
		!strings.EqualFold(strings.TrimSpace(t.Runtime), "crun") || !filepath.IsAbs(t.CgroupsPath) ||
		strings.TrimSpace(t.CPUs) == "" || t.PidsMax <= 0 || t.MemoryMB <= 0 ||
		!filepath.IsAbs(t.PodmanPath) || !validDigest(t.PodmanDigest) ||
		!filepath.IsAbs(t.CrunPath) || !validDigest(t.CrunDigest) ||
		!filepath.IsAbs(t.CosignPath) || !validDigest(t.CosignDigest) ||
		!filepath.IsAbs(t.CommandExecutable) || !validDigest(t.CommandExecutableDigest) ||
		t.CellUID <= 0 || t.CellGID <= 0 ||
		t.NamespaceProfile != "user,mount,pid,ipc,uts,network-none/v1" ||
		t.CgroupVersion != "v2" || t.CgroupControllers != "cpu,memory,pids" ||
		!validDigest(t.XattrVisibilityProfileDigest) || t.XattrVisibilityAuthority != D09XattrVisibilityAuthority || t.XattrFilesystemMagic == 0 ||
		!validDigest(t.XattrMountOptionsDigest) || t.XattrScanUID != t.CellUID || t.XattrScanGID != t.CellGID ||
		t.XattrScanPrivilegeProfile != D09XattrScanPrivilegeProfile ||
		!validDigest(t.ManagedLiveRootRegistrationDigest) ||
		t.UserNamespaceMappingProfile != D09UserNamespaceMappingProfile ||
		t.UserNamespaceMappingDigest != DigestD09UserNamespaceMapping(t.CellUID, t.CellGID) ||
		DigestD09XattrVisibilityProfile(t.XattrFilesystemMagic, t.XattrMountOptionsDigest, t.XattrScanUID, t.XattrScanGID,
			t.XattrScanPrivilegeProfile) != t.XattrVisibilityProfileDigest {
		return ErrD09TemplateInvalid
	}
	return nil
}

// DefaultD09CellTemplate returns a structurally complete but UNMEASURED draft
// template. Every identity digest is empty and the resource bounds are zero, so
// it fails validation and can never be bound into a signed profile. The operator
// must fill every measured value before installation.
func DefaultD09CellTemplate() D09CellTemplate {
	return D09CellTemplate{
		Image:               d09CellImageReference,
		CosignPublicKeyPath: "/etc/tbound/cosign.pub",
		OCILayoutPath:       "/etc/tbound/pi-cell-oci",
		SeccompProfilePath:  "/etc/tbound/seccomp.json",
		EntrypointPath:      d09EntrypointPath,
		Runtime:             "crun",
		CgroupsPath:         "/sys/fs/cgroup/tbound",
		NamespaceProfile:    "user,mount,pid,ipc,uts,network-none/v1",
		CgroupVersion:       "v2",
		CgroupControllers:   "cpu,memory,pids",
	}
}

// MarshalDefaultD09CellTemplate returns the canonical JSON of the unmeasured
// draft template.
func MarshalDefaultD09CellTemplate() ([]byte, error) {
	return json.Marshal(DefaultD09CellTemplate())
}

// DecodeD09CellTemplate decodes a strict D09 cell template from bytes and
// requires every measured identity. It is used by operator tooling and by the
// protected loader.
func DecodeD09CellTemplate(raw []byte) (D09CellTemplate, error) {
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return D09CellTemplate{}, fmt.Errorf("%w: template is not strict JSON: %v", ErrD09TemplateInvalid, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var template D09CellTemplate
	if err := decoder.Decode(&template); err != nil {
		return D09CellTemplate{}, fmt.Errorf("%w: decode D09 cell template: %v", ErrD09TemplateInvalid, err)
	}
	if err := validateD09CellTemplate(template); err != nil {
		return D09CellTemplate{}, err
	}
	return template, nil
}

// LoadD09CellTemplate reads and validates the registered, root-owned D09 cell
// template. Any missing, non-root-owned, group/world-writable, oversized, or
// unmeasured template fails closed.
func LoadD09CellTemplate(_ context.Context) (D09CellTemplate, string, error) {
	if err := validateRootOwnedProfileDirectory(filepath.Dir(D09CellTemplatePath)); err != nil {
		return D09CellTemplate{}, "", fmt.Errorf("%w: protected D09 template directory: %v", ErrD09TemplateInvalid, err)
	}
	raw, err := readRootOwnedProfileFile(D09CellTemplatePath, maxD09TemplateBytes)
	if err != nil {
		return D09CellTemplate{}, "", fmt.Errorf("%w: %v", ErrD09TemplateInvalid, err)
	}
	template, err := DecodeD09CellTemplate(raw)
	if err != nil {
		return D09CellTemplate{}, "", err
	}
	template.registered = true
	digest, err := template.Digest()
	if err != nil {
		return template, "", err
	}
	return template, digest, nil
}

// hashProtectedFile hashes a bounded, non-symlink regular file. Integrity comes
// from comparing the observed digest with the value recorded in the registered,
// root-owned template and bound into the signed profile.
func hashProtectedFile(path string, maximum int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maximum {
		return "", ErrD09TemplateInvalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("protected artifact must be root-owned and not group/world writable")
	}
	if err := requireReadOnlyMount(path); err != nil {
		return "", fmt.Errorf("protected artifact is not on a read-only mount: %w", err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("protected artifact changed while it was opened")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil {
		return "", err
	}
	if n != info.Size() {
		return "", ErrD09TemplateInvalid
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
