//go:build linux

package piruntime

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"syscall"

	"tbound/supervisor/internal/workspace"
)

const d09XattrVisibilityDomain = "tbound-d09-xattr-visibility/v2\x00"

// D09XattrScanPrivilegeProfile is a closed, measured scanner authority profile:
// the dedicated non-root service UID/GID, zero effective capabilities,
// NoNewPrivs, and the initial host user namespace (complete UID/GID maps).
const D09XattrScanPrivilegeProfile = "service-nonroot-no-extra-groups-empty-caps-nnp-initial-userns/v1"

// D09XattrVisibilityAuthority is the closed signed-template policy authorizing
// xattr-complete scanning for the measured filesystem/scanner identity. Runtime
// APIs still verify the actual protected profile and source descriptor; a
// caller-provided boolean or digest alone cannot issue the workspace attestation.
const D09XattrVisibilityAuthority = "root-profile-measured-complete-enumeration/v1"

// ValidD09Digest reports whether value is a canonical lowercase SHA-256 digest.
func ValidD09Digest(value string) bool { return validDigest(value) }

// D09XattrVisibilityMeasurements is the observed filesystem/mount/scanner
// identity of a protected source descriptor. It is a measurement, not an
// attestation by itself.
type D09XattrVisibilityMeasurements struct {
	FilesystemMagic       uint64 `json:"filesystem_magic"`
	MountOptionsDigest    string `json:"mount_options_digest"`
	ScanUID               int    `json:"scan_uid"`
	ScanGID               int    `json:"scan_gid"`
	ScanPrivilegeProfile  string `json:"scan_privilege_profile"`
	EffectiveCapabilities string `json:"effective_capabilities"`
	NoNewPrivs            bool   `json:"no_new_privs"`
	InitialUserNamespace  bool   `json:"initial_user_namespace"`
	ReadOnly              bool   `json:"read_only"`
	RootOwned             bool   `json:"root_owned"`
}

// MeasureD09XattrVisibility records the descriptor's filesystem type, actual
// read-only mount options and effective scanner uid/gid. An operator records
// these values in the protected D09 template during profile freeze.
func MeasureD09XattrVisibility(source *os.File) (D09XattrVisibilityMeasurements, error) {
	if source == nil {
		return D09XattrVisibilityMeasurements{}, ErrD09TemplateInvalid
	}
	info, err := source.Stat()
	if err != nil || !info.IsDir() {
		return D09XattrVisibilityMeasurements{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return D09XattrVisibilityMeasurements{}, ErrD09TemplateInvalid
	}
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(int(source.Fd()), &fs); err != nil {
		return D09XattrVisibilityMeasurements{}, err
	}
	mountID, err := descriptorMountID(source.Fd())
	if err != nil {
		return D09XattrVisibilityMeasurements{}, err
	}
	mountOptions, readOnly, err := mountOptionsForID(mountID)
	if err != nil {
		return D09XattrVisibilityMeasurements{}, err
	}
	privilegeProfile, capEff, noNewPrivs, initialUserNS, err := measureXattrScannerPrivilege()
	if err != nil {
		return D09XattrVisibilityMeasurements{}, err
	}
	return D09XattrVisibilityMeasurements{
		FilesystemMagic: uint64(fs.Type), MountOptionsDigest: DigestBytes([]byte(mountOptions)),
		ScanUID: os.Geteuid(), ScanGID: os.Getegid(), ScanPrivilegeProfile: privilegeProfile,
		EffectiveCapabilities: capEff, NoNewPrivs: noNewPrivs, InitialUserNamespace: initialUserNS, ReadOnly: readOnly,
		RootOwned: stat.Uid == 0 && info.Mode().Perm()&0o222 == 0,
	}, nil
}

func measureXattrScannerPrivilege() (profile, capEff string, noNewPrivs, initialUserNS bool, err error) {
	if os.Geteuid() <= 0 || os.Getegid() <= 0 {
		return "", "", false, false, errors.New("xattr scanner must run as the dedicated non-root service UID/GID")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "", "", false, false, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "CapEff:":
			capEff = strings.ToLower(fields[1])
		case "NoNewPrivs:":
			noNewPrivs = fields[1] == "1"
		}
	}
	if capEff != d09EmptyCapabilitySet || !noNewPrivs {
		return "", capEff, noNewPrivs, false, errors.New("xattr scanner requires zero effective capabilities and NoNewPrivs=1")
	}
	groups, err := os.Getgroups()
	if err != nil || len(groups) > 1 || (len(groups) == 1 && groups[0] != os.Getegid()) {
		return "", capEff, noNewPrivs, false, errors.Join(errors.New("xattr scanner has unregistered supplementary groups"), err)
	}
	uidMap, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return "", capEff, noNewPrivs, false, err
	}
	gidMap, err := os.ReadFile("/proc/self/gid_map")
	if err != nil {
		return "", capEff, noNewPrivs, false, err
	}
	initialUserNS = exactInitialIDMap(uidMap) && exactInitialIDMap(gidMap)
	if !initialUserNS {
		return "", capEff, noNewPrivs, false, errors.New("xattr scanner is not in the initial host user namespace")
	}
	return D09XattrScanPrivilegeProfile, capEff, noNewPrivs, true, nil
}

func exactInitialIDMap(data []byte) bool {
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return false
	}
	return fields[0] == "0" && fields[1] == "0" && fields[2] == "4294967295"
}

// DigestD09XattrVisibilityProfile deterministically binds the registered
// filesystem type, mount options and scanner identity into the digest stored in
// D09CellTemplate.XattrVisibilityProfileDigest.
func DigestD09XattrVisibilityProfile(filesystemMagic uint64, mountOptionsDigest string, scanUID, scanGID int, privilegeProfile string) string {
	identity := fmt.Sprintf("%s%d\x00%s\x00%d\x00%d\x00%s", d09XattrVisibilityDomain, filesystemMagic, mountOptionsDigest, scanUID, scanGID, privilegeProfile)
	return DigestBytes([]byte(identity))
}

// VerifyD09XattrVisibility checks a current source descriptor against the
// measured, registered, signed-template filesystem/privilege profile. It
// returns the workspace attestation only when the actual descriptor, mount
// options, read-only state, and scanning identity all match. The template's
// completeness claim remains an operator-measured profile fact; this runtime
// check prevents silently reusing it on a different filesystem/UID/mount.
func VerifyD09XattrVisibility(source *os.File, profile HostProfile, template D09CellTemplate) (workspace.XattrVisibilityAttestation, error) {
	if !template.registered || profile.Mode != ModeProduction || profile.ContainmentProfile != D09ContainmentProfile {
		return workspace.XattrVisibilityAttestation{}, errors.New("D09 xattr visibility requires the root-owned registered template")
	}
	trustedProfile, profileErr := LoadLinuxSignedHostProfile("")
	if profileErr != nil || !reflect.DeepEqual(profile, trustedProfile) {
		return workspace.XattrVisibilityAttestation{}, errors.Join(errors.New("xattr evidence requires the fixed-anchor verified production profile"), profileErr)
	}
	if err := validateD09CellTemplate(template); err != nil {
		return workspace.XattrVisibilityAttestation{}, err
	}
	templateDigest, err := template.Digest()
	if err != nil || profile.ContainmentProfileDigest != templateDigest {
		return workspace.XattrVisibilityAttestation{}, errors.Join(errors.New("xattr template digest is not bound to the signed production profile"), err)
	}
	observed, err := MeasureD09XattrVisibility(source)
	if err != nil {
		return workspace.XattrVisibilityAttestation{}, errors.Join(ErrD09TemplateInvalid, err)
	}
	if !observed.ReadOnly || !observed.RootOwned || !observed.NoNewPrivs || !observed.InitialUserNamespace ||
		observed.EffectiveCapabilities != d09EmptyCapabilitySet || observed.ScanPrivilegeProfile != D09XattrScanPrivilegeProfile ||
		observed.FilesystemMagic != template.XattrFilesystemMagic ||
		observed.MountOptionsDigest != template.XattrMountOptionsDigest ||
		observed.ScanUID != template.XattrScanUID || observed.ScanGID != template.XattrScanGID ||
		observed.ScanPrivilegeProfile != template.XattrScanPrivilegeProfile {
		return workspace.XattrVisibilityAttestation{}, errors.New("source filesystem/mount/scanner identity differs from registered xattr visibility profile")
	}
	expected := DigestD09XattrVisibilityProfile(template.XattrFilesystemMagic, template.XattrMountOptionsDigest,
		template.XattrScanUID, template.XattrScanGID, template.XattrScanPrivilegeProfile)
	if expected != template.XattrVisibilityProfileDigest {
		return workspace.XattrVisibilityAttestation{}, errors.New("registered xattr visibility profile digest is inconsistent")
	}
	return workspace.XattrVisibilityAttestation{ProfileDigest: expected, Complete: true}, nil
}

func mountOptionsForID(mountID uint64) (options string, readOnly bool, err error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 3 {
			continue
		}
		id, parseErr := strconv.ParseUint(left[0], 10, 64)
		if parseErr != nil || id != mountID {
			continue
		}
		options = strings.Join([]string{left[3], left[4], left[5], right[0], right[1], right[2]}, "\x00")
		readOnly = strings.Contains(","+left[5]+",", ",ro,")
		return options, readOnly, nil
	}
	return "", false, errors.New("source descriptor mount ID is absent from mountinfo")
}
