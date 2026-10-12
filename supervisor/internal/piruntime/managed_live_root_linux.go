//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
)

const (
	D09ManagedLiveRootRegistrationPath  = "/etc/tbound/managed-live-root.json"
	D09ManagedLiveRootSignaturePath     = "/etc/tbound/managed-live-root.ed25519"
	D09ManagedLiveRootSchema            = "tbound-managed-live-root-registration/v1"
	D09ManagedLiveRootWriterPolicy      = "managed-nonlogin-single-service-writer/v1"
	D09ManagedPiServiceSocketSchema     = "tbound-managed-pi-service-socket/v1"
	D09ManagedPiServiceSocketMode       = "systemd-socket-activation-single-task/v1"
	D09ManagedPiServiceConfigSchema     = "tbound-pi-service-config/v1"
	D09ManagedPiServiceConfigPath       = "/etc/tbound/serve-pi-service.json"
	D09ManagedPiServiceSocketPath       = "/run/tbound/serve-pi.sock"
	D09ManagedPiSocketUnitPath          = "/etc/systemd/system/tbound-pi.socket"
	D09ManagedPiServiceUnitPath         = "/etc/systemd/system/tbound-pi.service"
	D09ManagedPiServiceCgroupPath       = "/system.slice/tbound-pi.service"
	D09ManagedPiServiceName             = "tbound-pi"
	D09ManagedPiServiceGroup            = "tbound-pi"
	D09ManagedPiSocketGroup             = "tbound-pi-clients"
	D09ManagedPiLiveRootPath            = "/var/lib/tbound/live"
	D09ManagedPiLiveRootParentPath      = "/var/lib/tbound"
	D09ManagedPiServiceSocketParentPath = "/run/tbound"
	D09ManagedPiRuntimeDir              = "/run/tbound-pi"
	D09ManagedPiPodmanHome              = "/var/lib/tbound/podman-home"
	D09ManagedPiPodmanGraphroot         = "/var/lib/tbound/podman-storage/graphroot"
	D09ManagedPiPodmanRunroot           = "/run/tbound-pi/podman-runroot"
	D09ManagedPiPodmanConfigHome        = "/etc/tbound/podman-config"
	D09ManagedPiPodmanDataHome          = "/var/lib/tbound/podman-home/data"
	D09ManagedPiPodmanCacheHome         = "/run/tbound-pi/cache"
	D09ManagedPiTempDir                 = "/run/tbound-pi/tmp"
	D09ManagedPiContainersConfPath      = "/etc/tbound/podman-config/containers.conf"
	D09ManagedPiStorageConfPath         = "/etc/tbound/podman-config/storage.conf"
	D09ManagedPiXattrCalibrationPath    = "/etc/tbound/d09-xattr-enumeration-calibration.json"
	D09ManagedPiXattrCalibrationSig     = "/etc/tbound/d09-xattr-enumeration-calibration.ed25519"
	D09ManagedPiServiceConfigMaxBytes   = 16 << 10
	d09ManagedLiveRootSignatureDomain   = "tbound-managed-live-root-registration/v1\x00"
	d09ManagedLiveRootMaximumBytes      = 64 << 10
)

// D09ManagedPiServiceSocket is the fixed systemd activation policy for the
// authenticated local CLI-to-service boundary. Socket and parent inode/mount
// IDs are intentionally runtime measurements because /run is recreated at boot.
type D09ManagedPiServiceSocket struct {
	SchemaVersion           string `json:"schema_version"`
	Mode                    string `json:"mode"`
	SocketPath              string `json:"socket_path"`
	SocketUnitPath          string `json:"socket_unit_path"`
	SocketUnitDigest        string `json:"socket_unit_digest"`
	ListenStream            string `json:"listen_stream"`
	Accept                  string `json:"accept"`
	ServiceUnit             string `json:"service_unit"`
	SocketUser              string `json:"socket_user"`
	SocketGroup             string `json:"socket_group"`
	SocketUID               int    `json:"socket_uid"`
	SocketGID               int    `json:"socket_gid"`
	SocketMode              uint32 `json:"socket_mode"`
	DirectoryMode           uint32 `json:"directory_mode"`
	ParentPath              string `json:"parent_path"`
	ParentUID               int    `json:"parent_uid"`
	ParentGID               int    `json:"parent_gid"`
	ParentMode              uint32 `json:"parent_mode"`
	ParentFilesystemMagic   uint64 `json:"parent_filesystem_magic"`
	ParentMountPolicy       string `json:"parent_mount_policy"`
	AllowedClientUIDs       []int  `json:"allowed_client_uids"`
	AllowedClientPrimaryGID int    `json:"allowed_client_primary_gid"`
}

func (s D09ManagedPiServiceSocket) Validate(serviceUID, serviceGID int) error {
	if s.SchemaVersion != D09ManagedPiServiceSocketSchema || s.Mode != D09ManagedPiServiceSocketMode ||
		s.SocketPath != D09ManagedPiServiceSocketPath || s.SocketUnitPath != D09ManagedPiSocketUnitPath ||
		!validDigest(s.SocketUnitDigest) || s.ListenStream != D09ManagedPiServiceSocketPath || s.Accept != "no" ||
		s.ServiceUnit != "tbound-pi.service" || s.SocketUser != "root" || s.SocketGroup != D09ManagedPiSocketGroup ||
		s.SocketUID != 0 || s.SocketGID <= 0 || s.SocketGID == serviceGID || s.SocketMode != 0o660 || s.DirectoryMode != 0o755 ||
		s.ParentPath != D09ManagedPiServiceSocketParentPath || s.ParentUID != 0 || s.ParentGID != 0 || s.ParentMode != 0o755 ||
		s.ParentFilesystemMagic != 0x01021994 || s.ParentMountPolicy != "tmpfs-rw-nodev-nosuid/v1" ||
		s.AllowedClientPrimaryGID < 0 || s.AllowedClientPrimaryGID == serviceGID || len(s.AllowedClientUIDs) == 0 {
		return ErrD09ManagedLiveRootRefused
	}
	previous := -1
	for _, uid := range s.AllowedClientUIDs {
		if uid < 0 || uid == serviceUID || uid <= previous {
			return fmt.Errorf("%w: allowed client UIDs must be sorted, unique and distinct from the service", ErrD09ManagedLiveRootRefused)
		}
		previous = uid
	}
	return nil
}

// D09ManagedPiServiceConfig is the complete fixed non-client service config.
// It contains no task, project, source path, profile, environment, or credential
// values. The registration pins the file digest and runtime verifies every path.
type D09ManagedPiServiceConfig struct {
	SchemaVersion        string `json:"schema_version"`
	SourcePolicy         string `json:"source_policy"`
	PodmanHome           string `json:"podman_home"`
	PodmanGraphroot      string `json:"podman_graphroot"`
	PodmanRunroot        string `json:"podman_runroot"`
	XDGConfigHome        string `json:"xdg_config_home"`
	XDGDataHome          string `json:"xdg_data_home"`
	XDGCacheHome         string `json:"xdg_cache_home"`
	XDGRuntimeDir        string `json:"xdg_runtime_dir"`
	TmpDir               string `json:"tmp_dir"`
	ContainersConfPath   string `json:"containers_conf_path"`
	ContainersConfDigest string `json:"containers_conf_digest"`
	StorageConfPath      string `json:"storage_conf_path"`
	StorageConfDigest    string `json:"storage_conf_digest"`
}

func (c D09ManagedPiServiceConfig) Validate() error {
	if c.SchemaVersion != D09ManagedPiServiceConfigSchema || c.SourcePolicy != "current-managed-live-root/v1" ||
		c.PodmanHome != D09ManagedPiPodmanHome || c.PodmanGraphroot != D09ManagedPiPodmanGraphroot ||
		c.PodmanRunroot != D09ManagedPiPodmanRunroot || c.XDGRuntimeDir != D09ManagedPiRuntimeDir ||
		c.XDGConfigHome != D09ManagedPiPodmanConfigHome || c.XDGDataHome != D09ManagedPiPodmanDataHome ||
		c.XDGCacheHome != D09ManagedPiPodmanCacheHome || c.TmpDir != D09ManagedPiTempDir ||
		c.ContainersConfPath != D09ManagedPiContainersConfPath || !validDigest(c.ContainersConfDigest) ||
		c.StorageConfPath != D09ManagedPiStorageConfPath || !validDigest(c.StorageConfDigest) {
		return ErrD09ManagedLiveRootRefused
	}
	return nil
}

// D09ManagedLiveRootMeasurementOptions contains explicit client authorization
// policy chosen by the host operator. It is never inferred from the invoking
// user's UID/GID. The calibration digest must name a separately fixed-anchor-
// signed artifact; a plain digest is not sufficient at load/verification time.
type D09ManagedLiveRootMeasurementOptions struct {
	AllowedClientUIDs                 []int
	AllowedClientPrimaryGID           int
	ManagedRootXattrCalibrationDigest string
}

func d09ManagedPiServiceWritablePaths() []string {
	paths := []string{
		"/var/lib/tbound/audit",
		D09ManagedPiLiveRootPath,
		"/var/lib/tbound/private-admin",
		"/var/lib/tbound/publication-state",
		"/var/lib/tbound/sessions",
		D09ManagedPiPodmanHome,
		D09ManagedPiPodmanGraphroot,
		D09ManagedPiRuntimeDir,
	}
	sort.Strings(paths)
	return paths
}

func sameCanonicalStringList(got, expected []string) bool {
	if len(got) != len(expected) {
		return false
	}
	for index, item := range got {
		if !canonicalAbsolutePath(item) || item != expected[index] || (index > 0 && got[index-1] >= item) {
			return false
		}
	}
	return true
}

var ErrD09ManagedLiveRootRefused = errors.New("managed D09 live-root registration or runtime identity is not admitted")

// D09ManagedLiveRootRegistration is a signed policy registration. It describes
// the one dedicated writable filesystem directory and the trusted non-login
// service/account which may host the writer process. It is NOT evidence that the
// root is currently quiescent: the CLI's ManagedLiveRootAuthority must still
// acquire its exclusive writer epoch, drain/reconcile command cells, and issue
// its opaque live permit. A root-owned host/service-manager trust boundary is
// assumed; arbitrary writers running as this dedicated UID are prohibited by
// deployment policy, not detected by mode bits or this signature.
type D09ManagedLiveRootRegistration struct {
	SchemaVersion string `json:"schema_version"`
	WriterPolicy  string `json:"writer_policy"`

	RootPath               string `json:"root_path"`
	RootDevice             uint64 `json:"root_device"`
	RootInode              uint64 `json:"root_inode"`
	RootFilesystemMagic    uint64 `json:"root_filesystem_magic"`
	RootMountID            uint64 `json:"root_mount_id"`
	RootMountOptionsDigest string `json:"root_mount_options_digest"`
	RootUID                int    `json:"root_uid"`
	RootGID                int    `json:"root_gid"`
	RootMode               uint32 `json:"root_mode"`

	ParentPath               string `json:"parent_path"`
	ParentDevice             uint64 `json:"parent_device"`
	ParentInode              uint64 `json:"parent_inode"`
	ParentFilesystemMagic    uint64 `json:"parent_filesystem_magic"`
	ParentMountID            uint64 `json:"parent_mount_id"`
	ParentMountOptionsDigest string `json:"parent_mount_options_digest"`
	ParentUID                int    `json:"parent_uid"`
	ParentGID                int    `json:"parent_gid"`
	ParentMode               uint32 `json:"parent_mode"`

	ServiceName                       string                    `json:"service_name"`
	ServiceGroup                      string                    `json:"service_group"`
	ServiceUID                        int                       `json:"service_uid"`
	ServiceGID                        int                       `json:"service_gid"`
	ServiceLoginShell                 string                    `json:"service_login_shell"`
	ServiceShellDigest                string                    `json:"service_shell_digest"`
	PasswdDigest                      string                    `json:"passwd_digest"`
	GroupDigest                       string                    `json:"group_digest"`
	NSSwitchDigest                    string                    `json:"nsswitch_digest"`
	ServiceUnitPath                   string                    `json:"service_unit_path"`
	ServiceUnitDigest                 string                    `json:"service_unit_digest"`
	MountUnitPath                     string                    `json:"mount_unit_path"`
	MountUnitDigest                   string                    `json:"mount_unit_digest"`
	ServiceConfigPath                 string                    `json:"service_config_path"`
	ServiceConfigDigest               string                    `json:"service_config_digest"`
	ServiceWritablePaths              []string                  `json:"service_writable_paths"`
	ServiceSocket                     D09ManagedPiServiceSocket `json:"service_socket"`
	ManagedRootXattrCalibrationDigest string                    `json:"managed_root_xattr_calibration_digest"`
	RunnerPath                        string                    `json:"runner_path"`
	RunnerDigest                      string                    `json:"runner_digest"`
	ServiceCgroupPath                 string                    `json:"service_cgroup_path"`
	Digest                            string                    `json:"digest"`
}

func (r D09ManagedLiveRootRegistration) Validate() error {
	if r.SchemaVersion != D09ManagedLiveRootSchema || r.WriterPolicy != D09ManagedLiveRootWriterPolicy ||
		r.RootPath != D09ManagedPiLiveRootPath || r.ParentPath != D09ManagedPiLiveRootParentPath ||
		r.RootDevice == 0 || r.RootInode == 0 || r.RootFilesystemMagic == 0 || r.RootMountID == 0 ||
		!validDigest(r.RootMountOptionsDigest) || r.RootUID <= 0 || r.RootGID <= 0 || r.RootMode != 0o700 ||
		r.ParentDevice == 0 || r.ParentInode == 0 || r.ParentFilesystemMagic == 0 || r.ParentMountID == 0 ||
		r.RootMountID == r.ParentMountID ||
		!validDigest(r.ParentMountOptionsDigest) || r.ParentUID != 0 || r.ParentGID != 0 || r.ParentMode&0o022 != 0 ||
		r.ServiceName != D09ManagedPiServiceName || r.ServiceGroup != D09ManagedPiServiceGroup || r.ServiceUID <= 0 || r.ServiceGID <= 0 ||
		r.ServiceUID != r.RootUID || r.ServiceGID != r.RootGID || r.ServiceLoginShell != "/usr/sbin/nologin" ||
		!validDigest(r.ServiceShellDigest) || !validDigest(r.PasswdDigest) || !validDigest(r.GroupDigest) || !validDigest(r.NSSwitchDigest) ||
		r.ServiceUnitPath != D09ManagedPiServiceUnitPath || !validDigest(r.ServiceUnitDigest) ||
		r.MountUnitPath != "/etc/systemd/system/var-lib-tbound-live.mount" || !validDigest(r.MountUnitDigest) ||
		r.ServiceConfigPath != D09ManagedPiServiceConfigPath || !validDigest(r.ServiceConfigDigest) ||
		!sameCanonicalStringList(r.ServiceWritablePaths, d09ManagedPiServiceWritablePaths()) ||
		!validDigest(r.ManagedRootXattrCalibrationDigest) ||
		!canonicalAbsolutePath(r.RunnerPath) || !validDigest(r.RunnerDigest) ||
		r.ServiceCgroupPath != D09ManagedPiServiceCgroupPath ||
		!validDigest(r.Digest) {
		return ErrD09ManagedLiveRootRefused
	}
	serviceBase := filepath.Base(r.ServiceUnitPath)
	if serviceBase != "tbound-pi.service" || !validSystemdUnitPath(r.ServiceUnitPath) || !validSystemdUnitPath(r.MountUnitPath) ||
		filepath.Base(r.MountUnitPath) != systemdMountUnitName(r.RootPath)+".mount" {
		return ErrD09ManagedLiveRootRefused
	}
	if err := r.ServiceSocket.Validate(r.ServiceUID, r.ServiceGID); err != nil {
		return err
	}
	for _, filesystemMagic := range []uint64{r.RootFilesystemMagic, r.ParentFilesystemMagic} {
		if !isD09ManagedLocalFilesystem(filesystemMagic) {
			return fmt.Errorf("%w: unsupported or non-local filesystem magic %#x", ErrD09ManagedLiveRootRefused, filesystemMagic)
		}
	}
	return nil
}

func isD09ManagedLocalFilesystem(magic uint64) bool {
	// ext-family, XFS and btrfs are local filesystems with established
	// xattr behavior. Network, overlay, FUSE and unknown filesystem types refuse.
	switch magic {
	case 0xef53, 0x58465342, 0x9123683e:
		return true
	default:
		return false
	}
}

func validSystemdUnitPath(path string) bool {
	return filepath.Dir(path) == "/etc/systemd/system"
}

func systemdMountUnitName(path string) string {
	trimmed := strings.TrimPrefix(path, string(os.PathSeparator))
	var out strings.Builder
	for _, r := range trimmed {
		switch {
		case r == '/':
			out.WriteByte('-')
		case r == '-' || r == '\\' || r == ' ' || r == '\t':
			fmt.Fprintf(&out, "\\x%02x", r)
		case r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ':' || r == '_' || r == '.':
			out.WriteRune(r)
		default:
			fmt.Fprintf(&out, "\\x%02x", r)
		}
	}
	return out.String()
}

func canonicalAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}

// D09ManagedLiveRootSigningBytes returns the exact canonical, domain-separated
// registration bytes. Digest is excluded before signing and hashing.
func D09ManagedLiveRootSigningBytes(registration D09ManagedLiveRootRegistration) ([]byte, error) {
	registration.Digest = ""
	canonical, err := json.Marshal(registration)
	if err != nil {
		return nil, err
	}
	return append([]byte(d09ManagedLiveRootSignatureDomain), canonical...), nil
}

// SignD09ManagedLiveRootRegistration signs a measured registration with the
// same authorized offline Ed25519 key as the host profile. It does not perform
// host installation or claim that a writer domain is presently exclusive.
func SignD09ManagedLiveRootRegistration(registration D09ManagedLiveRootRegistration,
	privateKey ed25519.PrivateKey) ([]byte, []byte, string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, nil, "", ErrD09ManagedLiveRootRefused
	}
	registration.SchemaVersion = D09ManagedLiveRootSchema
	registration.Digest = DigestBytes(mustD09ManagedLiveRootSigningBytes(registration))
	if err := registration.Validate(); err != nil {
		return nil, nil, "", err
	}
	message, err := D09ManagedLiveRootSigningBytes(registration)
	if err != nil {
		return nil, nil, "", err
	}
	encoded, err := json.Marshal(registration)
	if err != nil {
		return nil, nil, "", err
	}
	return encoded, ed25519.Sign(privateKey, message), registration.Digest, nil
}

// MeasureD09ManagedLiveRootRegistration constructs operator input from the
// currently opened root, its parent object, local account database, installed
// systemd unit files and pinned TBound executable. It does not sign, install,
// acquire a writer lease, or assert quiescence. The provided service cgroup path
// must be the exact system.slice unit path selected by the root administrator.
func MeasureD09ManagedLiveRootRegistration(rootPath, serviceName, serviceGroup,
	serviceUnitPath, mountUnitPath, runnerPath, serviceCgroupPath string) (D09ManagedLiveRootRegistration, error) {
	return MeasureD09ManagedLiveRootRegistrationWithOptions(rootPath, serviceName, serviceGroup,
		serviceUnitPath, mountUnitPath, runnerPath, serviceCgroupPath, D09ManagedLiveRootMeasurementOptions{})
}

func MeasureD09ManagedLiveRootRegistrationWithOptions(rootPath, serviceName, serviceGroup,
	serviceUnitPath, mountUnitPath, runnerPath, serviceCgroupPath string,
	options D09ManagedLiveRootMeasurementOptions) (D09ManagedLiveRootRegistration, error) {
	var registration D09ManagedLiveRootRegistration
	if !canonicalAbsolutePath(rootPath) || !validIdentity(serviceName) || !validIdentity(serviceGroup) ||
		!canonicalAbsolutePath(serviceUnitPath) || !canonicalAbsolutePath(mountUnitPath) ||
		!canonicalAbsolutePath(runnerPath) || !canonicalAbsolutePath(serviceCgroupPath) ||
		!validDigest(options.ManagedRootXattrCalibrationDigest) || options.AllowedClientPrimaryGID <= 0 ||
		len(options.AllowedClientUIDs) == 0 {
		return registration, ErrD09ManagedLiveRootRefused
	}
	for index, uid := range options.AllowedClientUIDs {
		if uid < 0 || (index > 0 && options.AllowedClientUIDs[index-1] >= uid) {
			return registration, ErrD09ManagedLiveRootRefused
		}
	}
	root, err := openAbsoluteDirectoryNoSymlinks(rootPath)
	if err != nil {
		return registration, err
	}
	defer root.Close()
	parentFD, err := syscall.Openat(int(root.Fd()), "..", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return registration, err
	}
	parent := os.NewFile(uintptr(parentFD), "managed-live-parent")
	defer parent.Close()
	rootInfo, err := root.Stat()
	parentInfo, parentErr := parent.Stat()
	rootPathObserved, rootPathErr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd()))
	parentPathObserved, parentPathErr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", parent.Fd()))
	var rootStat, parentStat *syscall.Stat_t
	var rootOK, parentOK bool
	if rootInfo != nil {
		rootStat, rootOK = rootInfo.Sys().(*syscall.Stat_t)
	}
	if parentInfo != nil {
		parentStat, parentOK = parentInfo.Sys().(*syscall.Stat_t)
	}
	if err != nil || parentErr != nil || rootPathErr != nil || parentPathErr != nil || !rootOK || !parentOK ||
		filepath.Clean(rootPathObserved) != rootPath || filepath.Clean(parentPathObserved) != filepath.Dir(rootPath) ||
		!rootInfo.IsDir() || !parentInfo.IsDir() || parentStat.Uid != 0 || parentInfo.Mode()&os.ModeSymlink != 0 ||
		rootInfo.Mode()&os.ModeSymlink != 0 || uint32(rootStat.Mode)&0o7777 != 0o700 || uint32(parentStat.Mode)&0o022 != 0 {
		return registration, errors.Join(ErrD09ManagedLiveRootRefused, err, parentErr, rootPathErr, parentPathErr)
	}
	rootUID, rootGID, rootMode := int(rootStat.Uid), int(rootStat.Gid), uint32(rootStat.Mode)&0o7777
	var rootFS, parentFS syscall.Statfs_t
	if err := syscall.Fstatfs(int(root.Fd()), &rootFS); err != nil {
		return registration, err
	}
	if err := syscall.Fstatfs(int(parent.Fd()), &parentFS); err != nil {
		return registration, err
	}
	rootMountID, err := descriptorMountID(root.Fd())
	if err != nil {
		return registration, err
	}
	parentMountID, err := descriptorMountID(parent.Fd())
	if err != nil {
		return registration, err
	}
	rootOptions, rootReadOnly, err := mountOptionsForID(rootMountID)
	if err != nil || rootReadOnly || !hasMountOption(rootOptions, "nosuid") || !hasMountOption(rootOptions, "nodev") {
		return registration, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	parentOptions, _, err := mountOptionsForID(parentMountID)
	if err != nil {
		return registration, err
	}
	passwd, err := readRootOwnedConfigFile("/etc/passwd", 8<<20)
	if err != nil {
		return registration, err
	}
	groupFile, err := readRootOwnedConfigFile("/etc/group", 8<<20)
	if err != nil {
		return registration, err
	}
	nsswitch, err := readRootOwnedConfigFile("/etc/nsswitch.conf", 1<<20)
	if err != nil || !localFilesOnlyNSSwitch(nsswitch) {
		return registration, errors.Join(ErrD09ManagedLiveRootRefused, err, errors.New("managed service requires files-only passwd/group resolution"))
	}
	serviceUID, serviceGID, shell, ok := lookupLocalServiceAccount(passwd, groupFile, serviceName, serviceGroup)
	if !ok || serviceUID != rootUID || serviceGID != rootGID || shell != "/usr/sbin/nologin" {
		return registration, errors.New("managed writer account/group must be local, dedicated, non-login, and match the live-root group")
	}
	accountPolicy := D09ManagedLiveRootRegistration{ServiceName: serviceName, ServiceGroup: serviceGroup,
		ServiceUID: serviceUID, ServiceGID: serviceGID, ServiceLoginShell: shell}
	if !registeredLocalServiceAccount(passwd, accountPolicy) || !registeredLocalServiceGroup(groupFile, accountPolicy) {
		return registration, errors.New("managed writer UID/GID is shared with an adjacent local account or group")
	}
	if options.AllowedClientPrimaryGID == serviceGID {
		return registration, ErrD09ManagedLiveRootRefused
	}
	for _, uid := range options.AllowedClientUIDs {
		if uid == serviceUID {
			return registration, ErrD09ManagedLiveRootRefused
		}
	}
	socketGID, ok := registeredLocalGroupGID(groupFile, D09ManagedPiSocketGroup)
	if !ok || socketGID == serviceGID {
		return registration, errors.New("managed Pi client socket group is missing or collides with writer group")
	}
	if _, err := measureManagedServiceCgroup(serviceCgroupPath, serviceUID); err != nil {
		return registration, err
	}
	serviceUnitDigest, err := hashRootOwnedConfigFile(serviceUnitPath, 1<<20)
	if err != nil {
		return registration, err
	}
	serviceUnitBytes, err := readRootOwnedConfigFile(serviceUnitPath, 1<<20)
	if err != nil {
		return registration, err
	}
	mountUnitDigest, err := hashRootOwnedConfigFile(mountUnitPath, 1<<20)
	if err != nil {
		return registration, err
	}
	mountUnitBytes, err := readRootOwnedConfigFile(mountUnitPath, 1<<20)
	if err != nil {
		return registration, err
	}
	socketUnitBytes, err := readRootOwnedConfigFile(D09ManagedPiSocketUnitPath, 1<<20)
	if err != nil {
		return registration, err
	}
	socketUnitDigest := DigestBytes(socketUnitBytes)
	serviceConfigBytes, err := readRootOwnedConfigFile(D09ManagedPiServiceConfigPath, D09ManagedPiServiceConfigMaxBytes)
	if err != nil {
		return registration, err
	}
	serviceConfig, err := decodeD09ManagedPiServiceConfig(serviceConfigBytes)
	if err != nil {
		return registration, err
	}
	if err := verifyD09ManagedPiServiceConfigFiles(serviceConfig); err != nil {
		return registration, err
	}
	serviceWritablePaths := d09ManagedPiServiceWritablePaths()
	serviceSocket := D09ManagedPiServiceSocket{
		SchemaVersion: D09ManagedPiServiceSocketSchema, Mode: D09ManagedPiServiceSocketMode,
		SocketPath: D09ManagedPiServiceSocketPath, SocketUnitPath: D09ManagedPiSocketUnitPath,
		SocketUnitDigest: socketUnitDigest, ListenStream: D09ManagedPiServiceSocketPath,
		Accept: "no", ServiceUnit: filepath.Base(D09ManagedPiServiceUnitPath), SocketUser: "root",
		SocketGroup: D09ManagedPiSocketGroup, SocketUID: 0, SocketGID: socketGID,
		SocketMode: 0o660, DirectoryMode: 0o755,
		ParentPath: D09ManagedPiServiceSocketParentPath, ParentUID: 0, ParentGID: 0,
		ParentMode: 0o755, ParentFilesystemMagic: 0x01021994, ParentMountPolicy: "tmpfs-rw-nodev-nosuid/v1",
		AllowedClientUIDs:       append([]int(nil), options.AllowedClientUIDs...),
		AllowedClientPrimaryGID: options.AllowedClientPrimaryGID,
	}
	if err := verifyD09ManagedPiServiceSocketParent(); err != nil {
		return registration, err
	}
	if err := validateD09ManagedPiServiceSocketUnit(socketUnitBytes, serviceSocket); err != nil {
		return registration, err
	}
	if err := validateD09ManagedPiServiceUnit(serviceUnitBytes, serviceName, serviceGroup, runnerPath,
		serviceConfig, serviceWritablePaths); err != nil {
		return registration, err
	}
	if err := validateManagedSystemdPolicy(rootPath, serviceName, serviceGroup, runnerPath, uint64(rootFS.Type), serviceUnitBytes, mountUnitBytes); err != nil {
		return registration, err
	}
	if err := verifyNoSystemdDropIns(filepath.Base(serviceUnitPath), filepath.Base(mountUnitPath)); err != nil {
		return registration, err
	}
	shellDigest, err := measureProtectedNonLoginShell(shell)
	if err != nil {
		return registration, err
	}
	runnerDigest, err := hashProtectedFile(runnerPath, 512<<20)
	if err != nil {
		return registration, err
	}
	if err := verifyPinnedToolBinary(runnerPath, runnerDigest); err != nil {
		return registration, err
	}
	registration = D09ManagedLiveRootRegistration{
		SchemaVersion: D09ManagedLiveRootSchema, WriterPolicy: D09ManagedLiveRootWriterPolicy,
		RootPath: rootPath, RootDevice: uint64(rootStat.Dev), RootInode: rootStat.Ino,
		RootFilesystemMagic: uint64(rootFS.Type), RootMountID: rootMountID,
		RootMountOptionsDigest: DigestBytes([]byte(rootOptions)), RootUID: rootUID, RootGID: rootGID, RootMode: rootMode,
		ParentPath: filepath.Dir(rootPath), ParentDevice: uint64(parentStat.Dev), ParentInode: parentStat.Ino,
		ParentFilesystemMagic: uint64(parentFS.Type), ParentMountID: parentMountID,
		ParentMountOptionsDigest: DigestBytes([]byte(parentOptions)), ParentUID: int(parentStat.Uid),
		ParentGID: int(parentStat.Gid), ParentMode: uint32(parentStat.Mode) & 0o7777,
		ServiceName: serviceName, ServiceGroup: serviceGroup, ServiceUID: serviceUID, ServiceGID: serviceGID,
		ServiceLoginShell: shell, ServiceShellDigest: shellDigest,
		PasswdDigest: DigestBytes(passwd), GroupDigest: DigestBytes(groupFile), NSSwitchDigest: DigestBytes(nsswitch),
		ServiceUnitPath: serviceUnitPath, ServiceUnitDigest: serviceUnitDigest,
		MountUnitPath: mountUnitPath, MountUnitDigest: mountUnitDigest,
		ServiceConfigPath: D09ManagedPiServiceConfigPath, ServiceConfigDigest: DigestBytes(serviceConfigBytes),
		ServiceWritablePaths: serviceWritablePaths, ServiceSocket: serviceSocket,
		ManagedRootXattrCalibrationDigest: options.ManagedRootXattrCalibrationDigest,
		RunnerPath:                        runnerPath, RunnerDigest: runnerDigest, ServiceCgroupPath: serviceCgroupPath,
	}
	message, err := D09ManagedLiveRootSigningBytes(registration)
	if err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	registration.Digest = DigestBytes(message)
	if err := registration.Validate(); err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	return registration, nil
}

func openAbsoluteDirectoryNoSymlinks(path string) (*os.File, error) {
	if !canonicalAbsolutePath(path) {
		return nil, ErrD09ManagedLiveRootRefused
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "managed-live-path-root")
	if err := validateProtectedDirectoryOwner(current, true); err != nil {
		_ = current.Close()
		return nil, err
	}
	components := strings.Split(strings.TrimPrefix(path, string(os.PathSeparator)), string(os.PathSeparator))
	for index, component := range components {
		if component == "" {
			continue
		}
		nextFD, openErr := syscall.Openat(int(current.Fd()), component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), component)
		if err := validateProtectedDirectoryOwner(next, index != len(components)-1); err != nil {
			_ = next.Close()
			_ = current.Close()
			return nil, err
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}

func validateProtectedDirectoryOwner(directory *os.File, requireRootOwner bool) error {
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (requireRootOwner && stat.Uid != 0) || info.Mode().Perm()&0o022 != 0 {
		return ErrD09ManagedLiveRootRefused
	}
	return nil
}

func hasMountOption(options, want string) bool {
	for _, group := range strings.Split(options, "\x00") {
		for _, option := range strings.Split(group, ",") {
			if option == want {
				return true
			}
		}
	}
	return false
}

func lookupLocalServiceAccount(passwd, groupFile []byte, serviceName, serviceGroup string) (int, int, string, bool) {
	var uid, gid int
	var shell string
	foundUser, foundGroup := false, false
	for _, line := range strings.Split(strings.TrimSpace(string(passwd)), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 || fields[0] != serviceName {
			continue
		}
		var err error
		uid, err = strconv.Atoi(fields[2])
		if err != nil {
			return 0, 0, "", false
		}
		gid, err = strconv.Atoi(fields[3])
		if err != nil {
			return 0, 0, "", false
		}
		shell, foundUser = fields[6], true
	}
	for _, line := range strings.Split(strings.TrimSpace(string(groupFile)), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 4 || fields[0] != serviceGroup {
			continue
		}
		observedGID, err := strconv.Atoi(fields[2])
		if err != nil || observedGID != gid {
			return 0, 0, "", false
		}
		for _, member := range strings.Split(fields[3], ",") {
			if member != "" && member != serviceName {
				return 0, 0, "", false
			}
		}
		foundGroup = true
	}
	return uid, gid, shell, foundUser && foundGroup
}

func mustHashProtectedFile(path string, maximum int64) string {
	digest, _ := hashProtectedFile(path, maximum)
	return digest
}

func mustD09ManagedLiveRootSigningBytes(registration D09ManagedLiveRootRegistration) []byte {
	message, _ := D09ManagedLiveRootSigningBytes(registration)
	return message
}

// VerifyD09ManagedLiveRootRegistration verifies strict canonical JSON, the
// exact domain-separated signature and digest, and all closed policy fields.
func VerifyD09ManagedLiveRootRegistration(encoded, signature []byte, publicKey ed25519.PublicKey) (D09ManagedLiveRootRegistration, error) {
	var registration D09ManagedLiveRootRegistration
	if len(encoded) == 0 || len(encoded) > d09ManagedLiveRootMaximumBytes || len(signature) != ed25519.SignatureSize ||
		len(publicKey) != ed25519.PublicKeySize {
		return registration, ErrD09ManagedLiveRootRefused
	}
	if err := protocolValidateStrictJSON(encoded); err != nil {
		return registration, fmt.Errorf("%w: registration JSON is not strict: %v", ErrD09ManagedLiveRootRefused, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registration); err != nil {
		return registration, fmt.Errorf("%w: decode registration: %v", ErrD09ManagedLiveRootRefused, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return registration, ErrD09ManagedLiveRootRefused
	}
	message, err := D09ManagedLiveRootSigningBytes(registration)
	if err != nil || registration.Digest != DigestBytes(message) || !ed25519.Verify(publicKey, message, signature) {
		return registration, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	canonical, err := json.Marshal(registration)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return registration, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := registration.Validate(); err != nil {
		return registration, err
	}
	return registration, nil
}

func protocolValidateStrictJSON(encoded []byte) error {
	// Use the shared duplicate-key/depth/size validator used by supervisor IPC.
	return protocol.ValidateStrictJSON(encoded)
}

// VerifiedD09ManagedLiveRoot holds descriptor-bound registration verification.
// It is a registration capability only, not an exclusivity or quiescence permit.
type VerifiedD09ManagedLiveRoot struct {
	mu            sync.Mutex
	registration  D09ManagedLiveRootRegistration
	root          *os.File
	parent        *os.File
	serviceCgroup managedServiceCgroupIdentity
	closed        bool
}

// D09ManagedLiveRootCgroupIdentity is measured from /proc/self/cgroup and the
// held cgroup-v2 mount at verification time. Unlike registration policy fields,
// its inode/mount ID is per-service-invocation runtime evidence.
type D09ManagedLiveRootCgroupIdentity struct {
	Path    string
	Device  uint64
	Inode   uint64
	MountID uint64
}

func (v *VerifiedD09ManagedLiveRoot) CgroupIdentity() (D09ManagedLiveRootCgroupIdentity, bool) {
	if v == nil {
		return D09ManagedLiveRootCgroupIdentity{}, false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || v.serviceCgroup.inode == 0 {
		return D09ManagedLiveRootCgroupIdentity{}, false
	}
	return D09ManagedLiveRootCgroupIdentity{Path: v.registration.ServiceCgroupPath, Device: v.serviceCgroup.device,
		Inode: v.serviceCgroup.inode, MountID: v.serviceCgroup.mountID}, true
}

func (v *VerifiedD09ManagedLiveRoot) Registration() D09ManagedLiveRootRegistration {
	if v == nil {
		return D09ManagedLiveRootRegistration{}
	}
	return v.registration
}

// DuplicateRoot returns an owned duplicate descriptor after rechecking the
// object identity. The caller must still coordinate its writer epoch through
// ManagedLiveRootAuthority; this descriptor does not imply exclusive access.
func (v *VerifiedD09ManagedLiveRoot) DuplicateRoot() (*os.File, error) {
	if v == nil {
		return nil, ErrD09ManagedLiveRootRefused
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || v.root == nil {
		return nil, ErrD09ManagedLiveRootRefused
	}
	if err := verifyD09ManagedRootDescriptor(v.registration, v.root); err != nil {
		return nil, err
	}
	fd, err := syscall.Dup(int(v.root.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "registered-managed-live-root"), nil
}

// DuplicateCapability returns an independently owned verified descriptor set
// for a second lifecycle owner (for example the D09 launcher). Closing the copy
// never closes the caller's authority object or its permit state.
func (v *VerifiedD09ManagedLiveRoot) DuplicateCapability(ctx context.Context) (*VerifiedD09ManagedLiveRoot, error) {
	if v == nil || ctx == nil {
		return nil, ErrD09ManagedLiveRootRefused
	}
	if err := v.Revalidate(ctx, v.root); err != nil {
		return nil, err
	}
	return v.duplicateCapabilityDescriptors()
}

func (v *VerifiedD09ManagedLiveRoot) duplicateCapabilityDescriptors() (*VerifiedD09ManagedLiveRoot, error) {
	// Private descriptor-copy helper only. It adds no admission authority and
	// intentionally does not repeat signed-policy validation: the public
	// DuplicateCapability path must Revalidate first. Here we only prove that the
	// two held descriptors are directories and that the held parent is the actual
	// descriptor-relative parent object of the held root.
	if v == nil {
		return nil, ErrD09ManagedLiveRootRefused
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || v.root == nil || v.parent == nil {
		return nil, ErrD09ManagedLiveRootRefused
	}
	rootInfo, err := v.root.Stat()
	parentInfo, parentErr := v.parent.Stat()
	if err != nil || parentErr != nil || !rootInfo.IsDir() || !parentInfo.IsDir() {
		return nil, errors.Join(ErrD09ManagedLiveRootRefused, err, parentErr)
	}
	actualParentFD, err := syscall.Openat(int(v.root.Fd()), "..", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	actualParent := os.NewFile(uintptr(actualParentFD), "managed-live-root-actual-parent-check")
	actualParentInfo, statErr := actualParent.Stat()
	if statErr != nil || !actualParentInfo.IsDir() || !os.SameFile(parentInfo, actualParentInfo) {
		_ = actualParent.Close()
		return nil, errors.Join(ErrD09ManagedLiveRootRefused, statErr)
	}
	_ = actualParent.Close()
	rootFD, err := syscall.Openat(int(v.root.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	rootCopy := os.NewFile(uintptr(rootFD), "managed-live-root-launcher-copy")
	parentFD, err := syscall.Openat(int(v.parent.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = rootCopy.Close()
		return nil, err
	}
	parentCopy := os.NewFile(uintptr(parentFD), "managed-live-parent-launcher-copy")
	rootCopyInfo, rootCopyErr := rootCopy.Stat()
	parentCopyInfo, parentCopyErr := parentCopy.Stat()
	if rootCopyErr != nil || parentCopyErr != nil || !os.SameFile(rootInfo, rootCopyInfo) || !os.SameFile(parentInfo, parentCopyInfo) {
		_ = rootCopy.Close()
		_ = parentCopy.Close()
		return nil, errors.Join(ErrD09ManagedLiveRootRefused, rootCopyErr, parentCopyErr)
	}
	copy := &VerifiedD09ManagedLiveRoot{registration: v.registration, root: rootCopy, parent: parentCopy,
		serviceCgroup: v.serviceCgroup}
	return copy, nil
}

// Revalidate compares an active candidate descriptor and protected host policy
// to the signed registration again. This is for C's authority at admission and
// terminal drain, and it never asserts that no other writer exists.
func (v *VerifiedD09ManagedLiveRoot) Revalidate(ctx context.Context, candidate *os.File) error {
	if v == nil || ctx == nil || candidate == nil {
		return ErrD09ManagedLiveRootRefused
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ErrD09ManagedLiveRootRefused
	}
	currentRegistration, err := LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil || currentRegistration.Digest != v.registration.Digest {
		return errors.Join(ErrD09ManagedLiveRootRefused, errors.New("protected managed-root registration/profile changed during the active epoch"), err)
	}
	if err := verifyD09ManagedRootDescriptor(v.registration, candidate); err != nil {
		return err
	}
	if err := verifyD09ManagedWriterProcess(v.registration); err != nil {
		return err
	}
	cgroup, err := measureManagedServiceCgroup(v.registration.ServiceCgroupPath, v.registration.ServiceUID)
	if err != nil || cgroup != v.serviceCgroup {
		return errors.Join(ErrD09ManagedLiveRootRefused, errors.New("managed service cgroup identity changed during the verified epoch"), err)
	}
	if err := verifyD09TemplateCgroupPath(ctx, v.registration, cgroup); err != nil {
		return err
	}
	return nil
}

func (v *VerifiedD09ManagedLiveRoot) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true
	var rootErr, parentErr error
	if v.root != nil {
		rootErr = v.root.Close()
	}
	if v.parent != nil {
		parentErr = v.parent.Close()
	}
	return errors.Join(rootErr, parentErr)
}

// VerifyD09ManagedLiveRoot verifies the fixed-anchor signature, matching signed
// host profile/template binding, protected service/account policy, and the
// caller's actual root descriptor. Signature verification by itself is never
// sufficient: root/parent filesystem and mount identities, access mode,
// non-login account, process executable/UID/GID/groups, unit config digests and
// exact service cgroup membership must all match current host state.
func VerifyD09ManagedLiveRoot(ctx context.Context, candidate *os.File) (*VerifiedD09ManagedLiveRoot, error) {
	if ctx == nil || candidate == nil {
		return nil, ErrD09ManagedLiveRootRefused
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	registration, err := LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if err := verifyD09ManagedRootDescriptor(registration, candidate); err != nil {
		return nil, err
	}
	if err := verifyD09ManagedWriterProcess(registration); err != nil {
		return nil, err
	}
	serviceCgroup, err := measureManagedServiceCgroup(registration.ServiceCgroupPath, registration.ServiceUID)
	if err != nil {
		return nil, err
	}
	if err := verifyD09TemplateCgroupPath(ctx, registration, serviceCgroup); err != nil {
		return nil, err
	}
	rootFD, err := syscall.Dup(int(candidate.Fd()))
	if err != nil {
		return nil, err
	}
	rootCopy := os.NewFile(uintptr(rootFD), "registered-managed-live-root")
	parentFD, err := syscall.Openat(int(rootCopy.Fd()), "..", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = rootCopy.Close()
		return nil, err
	}
	parentCopy := os.NewFile(uintptr(parentFD), "registered-managed-live-parent")
	return &VerifiedD09ManagedLiveRoot{registration: registration, root: rootCopy, parent: parentCopy, serviceCgroup: serviceCgroup}, nil
}

// LoadVerifiedD09ManagedLiveRootPolicy verifies the public, fixed-anchor-signed
// managed-root and local-service policy. It is safe for an unprivileged CLI
// client: it does not inspect the caller's UID/GID, executable, supplementary
// groups, or cgroup. Socket endpoint authentication is performed separately by
// checking the registered socket path and the connected peer's SO_PEERCRED.
// An operator-supplied registration path/key cannot be its own trust root.
func LoadVerifiedD09ManagedLiveRootPolicy(ctx context.Context) (D09ManagedLiveRootRegistration, error) {
	if ctx == nil {
		return D09ManagedLiveRootRegistration{}, ErrD09ManagedLiveRootRefused
	}
	if err := ctx.Err(); err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	if err := validateRootOwnedProfileDirectory(linuxTrustedProfileDirectory); err != nil {
		return D09ManagedLiveRootRegistration{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	encoded, err := readRootOwnedProfileFile(D09ManagedLiveRootRegistrationPath, d09ManagedLiveRootMaximumBytes)
	if err != nil {
		return D09ManagedLiveRootRegistration{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	signature, err := readRootOwnedProfileFile(D09ManagedLiveRootSignaturePath, ed25519.SignatureSize)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return D09ManagedLiveRootRegistration{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	publicKey, err := readProtectedLinuxHostProfilePublicKey()
	if err != nil {
		return D09ManagedLiveRootRegistration{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	registration, err := VerifyD09ManagedLiveRootRegistration(encoded, signature, ed25519.PublicKey(publicKey))
	if err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	profile, err := LoadLinuxSignedHostProfile("")
	if err != nil {
		return D09ManagedLiveRootRegistration{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	template, templateDigest, err := LoadD09CellTemplate(ctx)
	if err != nil || template.ManagedLiveRootRegistrationDigest != registration.Digest ||
		registration.ServiceUID == template.CellUID || registration.ServiceGID == template.CellGID ||
		template.UserNamespaceMappingDigest != DigestD09UserNamespaceMapping(template.CellUID, template.CellGID,
			registration.ServiceUID, registration.ServiceGID) ||
		template.XattrScanUID != registration.ServiceUID || template.XattrScanGID != registration.ServiceGID ||
		profile.Mode != ModeProduction || profile.ContainmentProfile != D09ContainmentProfile ||
		profile.ContainmentProfileDigest != templateDigest {
		return D09ManagedLiveRootRegistration{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := verifyD09StaticPolicyFiles(ctx, registration, template); err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	return registration, nil
}

// LoadD09ManagedLiveRootRegistration is retained as a compatibility alias for
// the static policy loader. It deliberately does not establish that the caller
// is the managed service; use VerifyD09ManagedLiveRoot for that runtime check.
func LoadD09ManagedLiveRootRegistration(ctx context.Context) (D09ManagedLiveRootRegistration, error) {
	return LoadVerifiedD09ManagedLiveRootPolicy(ctx)
}

func verifyD09StaticPolicyFiles(ctx context.Context, reg D09ManagedLiveRootRegistration, template D09CellTemplate) error {
	if ctx == nil || !template.registered {
		return ErrD09ManagedLiveRootRefused
	}
	checks := []struct {
		path    string
		digest  string
		maximum int64
	}{
		{reg.ServiceUnitPath, reg.ServiceUnitDigest, 1 << 20},
		{reg.MountUnitPath, reg.MountUnitDigest, 1 << 20},
		{D09ManagedPiSocketUnitPath, reg.ServiceSocket.SocketUnitDigest, 1 << 20},
		{reg.ServiceConfigPath, reg.ServiceConfigDigest, D09ManagedPiServiceConfigMaxBytes},
	}
	for _, check := range checks {
		data, err := readRootOwnedConfigFile(check.path, check.maximum)
		if err != nil || DigestBytes(data) != check.digest {
			return errors.Join(ErrD09ManagedLiveRootRefused,
				fmt.Errorf("protected policy file digest differs from signed registration: %s", check.path), err)
		}
	}
	serviceUnit, err := readRootOwnedConfigFile(reg.ServiceUnitPath, 1<<20)
	if err != nil {
		return err
	}
	mountUnit, err := readRootOwnedConfigFile(reg.MountUnitPath, 1<<20)
	if err != nil {
		return err
	}
	socketUnit, err := readRootOwnedConfigFile(reg.ServiceSocket.SocketUnitPath, 1<<20)
	if err != nil {
		return err
	}
	serviceConfigBytes, err := readRootOwnedConfigFile(reg.ServiceConfigPath, D09ManagedPiServiceConfigMaxBytes)
	if err != nil {
		return err
	}
	serviceConfig, err := decodeD09ManagedPiServiceConfig(serviceConfigBytes)
	if err != nil {
		return err
	}
	if err := verifyD09ManagedPiServiceConfigFiles(serviceConfig); err != nil {
		return err
	}
	if err := validateD09ManagedPiServiceUnit(serviceUnit, reg.ServiceName, reg.ServiceGroup,
		reg.RunnerPath, serviceConfig, reg.ServiceWritablePaths); err != nil {
		return err
	}
	if err := validateD09ManagedPiServiceSocketUnit(socketUnit, reg.ServiceSocket); err != nil {
		return err
	}
	if err := validateManagedSystemdPolicy(reg.RootPath, reg.ServiceName, reg.ServiceGroup,
		reg.RunnerPath, reg.RootFilesystemMagic, serviceUnit, mountUnit); err != nil {
		return err
	}
	if err := verifyNoSystemdDropIns(reg.ServiceUnitPath, reg.MountUnitPath, reg.ServiceSocket.SocketUnitPath); err != nil {
		return err
	}
	passwd, err := readRootOwnedConfigFile("/etc/passwd", 8<<20)
	if err != nil || DigestBytes(passwd) != reg.PasswdDigest || !registeredLocalServiceAccount(passwd, reg) {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	groupFile, err := readRootOwnedConfigFile("/etc/group", 8<<20)
	if err != nil || DigestBytes(groupFile) != reg.GroupDigest || !registeredLocalServiceGroup(groupFile, reg) {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	socketGID, groupOK := registeredLocalGroupGID(groupFile, D09ManagedPiSocketGroup)
	if !groupOK || socketGID != reg.ServiceSocket.SocketGID {
		return ErrD09ManagedLiveRootRefused
	}
	nsswitch, err := readRootOwnedConfigFile("/etc/nsswitch.conf", 1<<20)
	if err != nil || DigestBytes(nsswitch) != reg.NSSwitchDigest || !localFilesOnlyNSSwitch(nsswitch) {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	calibration, err := LoadVerifiedD09XattrEnumerationCalibration(ctx)
	if err != nil || calibration.Digest != reg.ManagedRootXattrCalibrationDigest ||
		calibration.SourceAccessProfile != D09XattrManagedRWSourceProfile ||
		calibration.FilesystemMagic != reg.RootFilesystemMagic || calibration.MountOptionsDigest != reg.RootMountOptionsDigest ||
		calibration.ScanUID != reg.ServiceUID || calibration.ScanGID != reg.ServiceGID ||
		calibration.VisibilityProfileDigest != template.XattrVisibilityProfileDigest {
		return errors.Join(ErrD09ManagedLiveRootRefused,
			errors.New("managed RW-root xattr calibration differs from signed registration/template/scanner identity"), err)
	}
	return nil
}

// verifyD09TemplateCgroupPath is intentionally a service-mode check. Static
// policy loading has no dependency on the caller's cgroup namespace; the
// service verifier calls this after measuring the actual delegated cgroup.
func verifyD09TemplateCgroupPath(ctx context.Context, registration D09ManagedLiveRootRegistration,
	serviceCgroup managedServiceCgroupIdentity) error {
	if ctx == nil {
		return ErrD09ManagedLiveRootRefused
	}
	template, _, err := LoadD09CellTemplate(ctx)
	if err != nil || (filepath.Clean(template.CgroupsPath) != serviceCgroup.hostPath &&
		!strings.HasPrefix(filepath.Clean(template.CgroupsPath), serviceCgroup.hostPath+string(os.PathSeparator))) {
		return errors.Join(ErrD09ManagedLiveRootRefused,
			errors.New("registered Podman cgroup root must be under the delegated managed service cgroup"), err)
	}
	if template.ManagedLiveRootRegistrationDigest != registration.Digest {
		return ErrD09ManagedLiveRootRefused
	}
	return nil
}

func verifyD09ManagedRootDescriptor(reg D09ManagedLiveRootRegistration, root *os.File) error {
	if err := reg.Validate(); err != nil {
		return err
	}
	info, err := root.Stat()
	if err != nil || !info.IsDir() {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd()))
	if err != nil || filepath.Clean(path) != reg.RootPath || strings.HasSuffix(path, " (deleted)") {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != reg.RootDevice || stat.Ino != reg.RootInode || int(stat.Uid) != reg.RootUID ||
		int(stat.Gid) != reg.RootGID || uint32(stat.Mode)&0o7777 != reg.RootMode {
		return ErrD09ManagedLiveRootRefused
	}
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(int(root.Fd()), &fs); err != nil || uint64(fs.Type) != reg.RootFilesystemMagic {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	rootMountID, err := descriptorMountID(root.Fd())
	if err != nil || rootMountID != reg.RootMountID {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	rootOptions, rootRO, err := mountOptionsForID(rootMountID)
	if err != nil || rootRO || !hasMountOption(rootOptions, "nosuid") || !hasMountOption(rootOptions, "nodev") || DigestBytes([]byte(rootOptions)) != reg.RootMountOptionsDigest {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	parentFD, err := syscall.Openat(int(root.Fd()), "..", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	parent := os.NewFile(uintptr(parentFD), "registered-live-root-parent-check")
	defer parent.Close()
	parentInfo, err := parent.Stat()
	parentPath, pathErr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", parent.Fd()))
	var parentStat *syscall.Stat_t
	var parentOK bool
	if parentInfo != nil {
		parentStat, parentOK = parentInfo.Sys().(*syscall.Stat_t)
	}
	if err != nil || pathErr != nil || !parentInfo.IsDir() || filepath.Clean(parentPath) != reg.ParentPath || !parentOK ||
		uint64(parentStat.Dev) != reg.ParentDevice || parentStat.Ino != reg.ParentInode || int(parentStat.Uid) != reg.ParentUID ||
		int(parentStat.Gid) != reg.ParentGID || uint32(parentStat.Mode)&0o7777 != reg.ParentMode || parentStat.Mode&0o022 != 0 {
		return errors.Join(ErrD09ManagedLiveRootRefused, err, pathErr)
	}
	var parentFS syscall.Statfs_t
	if err := syscall.Fstatfs(int(parent.Fd()), &parentFS); err != nil || uint64(parentFS.Type) != reg.ParentFilesystemMagic {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	parentMountID, err := descriptorMountID(parent.Fd())
	if err != nil || parentMountID != reg.ParentMountID {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	parentOptions, _, err := mountOptionsForID(parentMountID)
	if err != nil || DigestBytes([]byte(parentOptions)) != reg.ParentMountOptionsDigest {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return nil
}

func verifyD09ManagedWriterProcess(reg D09ManagedLiveRootRegistration) error {
	if os.Geteuid() != reg.ServiceUID || os.Getegid() != reg.ServiceGID {
		return ErrD09ManagedLiveRootRefused
	}
	profile, capEff, noNewPrivs, initialUserNS, privilegeErr := measureXattrScannerPrivilege()
	if privilegeErr != nil || profile != D09XattrScanPrivilegeProfile || capEff != d09EmptyCapabilitySet ||
		!noNewPrivs || !initialUserNS {
		return errors.Join(ErrD09ManagedLiveRootRefused,
			errors.New("managed writer requires the measured non-root, no-capability, NoNewPrivs initial-userns profile"), privilegeErr)
	}
	groups, err := os.Getgroups()
	if err != nil {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	for _, group := range groups {
		if group != reg.ServiceGID {
			return fmt.Errorf("%w: managed writer has an unregistered supplementary group", ErrD09ManagedLiveRootRefused)
		}
	}
	passwd, err := readRootOwnedConfigFile("/etc/passwd", 8<<20)
	if err != nil || DigestBytes(passwd) != reg.PasswdDigest {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	groupFile, err := readRootOwnedConfigFile("/etc/group", 8<<20)
	if err != nil || DigestBytes(groupFile) != reg.GroupDigest {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	nsswitch, err := readRootOwnedConfigFile("/etc/nsswitch.conf", 1<<20)
	if err != nil || DigestBytes(nsswitch) != reg.NSSwitchDigest || !localFilesOnlyNSSwitch(nsswitch) {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if !registeredLocalServiceAccount(passwd, reg) || !registeredLocalServiceGroup(groupFile, reg) {
		return ErrD09ManagedLiveRootRefused
	}
	if err := verifyD09ManagedServiceWritablePaths(reg); err != nil {
		return err
	}
	shellDigest, err := measureProtectedNonLoginShell(reg.ServiceLoginShell)
	if err != nil || shellDigest != reg.ServiceShellDigest {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	serviceUnitBytes, err := readRootOwnedConfigFile(reg.ServiceUnitPath, 1<<20)
	if err != nil || DigestBytes(serviceUnitBytes) != reg.ServiceUnitDigest {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	mountUnitBytes, err := readRootOwnedConfigFile(reg.MountUnitPath, 1<<20)
	if err != nil || DigestBytes(mountUnitBytes) != reg.MountUnitDigest {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := validateManagedSystemdPolicy(reg.RootPath, reg.ServiceName, reg.ServiceGroup, reg.RunnerPath, reg.RootFilesystemMagic, serviceUnitBytes, mountUnitBytes); err != nil {
		return err
	}
	if err := verifyNoSystemdDropIns(filepath.Base(reg.ServiceUnitPath), filepath.Base(reg.MountUnitPath)); err != nil {
		return err
	}
	if err := verifyD09ManagedServiceWritableDirectories(reg); err != nil {
		return err
	}
	runnerPath, err := os.Readlink("/proc/self/exe")
	if err != nil || filepath.Clean(strings.TrimSuffix(runnerPath, " (deleted)")) != reg.RunnerPath || strings.HasSuffix(runnerPath, " (deleted)") {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	if err := verifyPinnedToolBinary(reg.RunnerPath, reg.RunnerDigest); err != nil {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	cgroup, err := currentUnifiedCgroupPath()
	if err != nil || cgroup != reg.ServiceCgroupPath {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	cgroupIdentity, err := measureManagedServiceCgroup(reg.ServiceCgroupPath, reg.ServiceUID)
	if err != nil || cgroupIdentity.inode == 0 || cgroupIdentity.mountID == 0 {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return nil
}

func verifyD09ManagedServiceWritableDirectories(reg D09ManagedLiveRootRegistration) error {
	for _, path := range reg.ServiceWritablePaths {
		directory, err := openAbsoluteDirectoryNoSymlinks(path)
		if err != nil {
			return errors.Join(ErrD09ManagedLiveRootRefused,
				fmt.Errorf("registered service writable directory is missing or unsafe: %s", path), err)
		}
		info, statErr := directory.Stat()
		stat, ok := (*syscall.Stat_t)(nil), false
		if info != nil {
			stat, ok = info.Sys().(*syscall.Stat_t)
		}
		_ = directory.Close()
		if statErr != nil || !ok || !info.IsDir() || int(stat.Uid) != reg.ServiceUID ||
			int(stat.Gid) != reg.ServiceGID || uint32(stat.Mode)&0o7777 != 0o700 {
			return errors.Join(ErrD09ManagedLiveRootRefused,
				fmt.Errorf("registered service writable directory owner/mode differs: %s", path), statErr)
		}
	}
	return nil
}

func measureProtectedNonLoginShell(path string) (string, error) {
	file, info, err := openRootOwnedConfigFile(path, 512<<20)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if info.Mode().Perm()&0o111 == 0 {
		return "", ErrD09ManagedLiveRootRefused
	}
	data, err := io.ReadAll(io.LimitReader(file, (512<<20)+1))
	if err != nil || int64(len(data)) != info.Size() || int64(len(data)) > 512<<20 {
		return "", errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return DigestBytes(data), nil
}

func localFilesOnlyNSSwitch(data []byte) bool {
	foundPasswd, foundGroup := false, false
	for _, rawLine := range strings.Split(string(data), "\n") {
		line, _, _ := strings.Cut(rawLine, "#")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "passwd:":
			if foundPasswd || len(fields) != 2 || fields[1] != "files" {
				return false
			}
			foundPasswd = true
		case "group:":
			if foundGroup || len(fields) != 2 || fields[1] != "files" {
				return false
			}
			foundGroup = true
		}
	}
	return foundPasswd && foundGroup
}

func parseSystemdUnit(data []byte) map[string]map[string][]string {
	sections := make(map[string]map[string][]string)
	section := ""
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && len(line) > 2 {
			section = line[1 : len(line)-1]
			if sections[section] == nil {
				sections[section] = make(map[string][]string)
			}
			continue
		}
		if section == "" {
			return nil
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil
		}
		sections[section][key] = append(sections[section][key], strings.TrimSpace(value))
	}
	return sections
}

func oneUnitValue(sections map[string]map[string][]string, section, key string) (string, bool) {
	values := sections[section][key]
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func validateManagedSystemdPolicy(rootPath, serviceName, serviceGroup, runnerPath string, filesystemMagic uint64, serviceUnit, mountUnit []byte) error {
	service := parseSystemdUnit(serviceUnit)
	user, userOK := oneUnitValue(service, "Service", "User")
	group, groupOK := oneUnitValue(service, "Service", "Group")
	execStart, execOK := oneUnitValue(service, "Service", "ExecStart")
	noNewPrivs, nnpOK := oneUnitValue(service, "Service", "NoNewPrivileges")
	protectSystem, protectSystemOK := oneUnitValue(service, "Service", "ProtectSystem")
	protectHome, protectHomeOK := oneUnitValue(service, "Service", "ProtectHome")
	privateTmp, privateTmpOK := oneUnitValue(service, "Service", "PrivateTmp")
	readWritePaths, rwOK := oneUnitValue(service, "Service", "ReadWritePaths")
	delegate, delegateOK := oneUnitValue(service, "Service", "Delegate")
	killMode, killModeOK := oneUnitValue(service, "Service", "KillMode")
	umask, umaskOK := oneUnitValue(service, "Service", "UMask")
	startFields := strings.Fields(execStart)
	if !userOK || user != serviceName || !groupOK || group != serviceGroup || !execOK || len(startFields) == 0 || startFields[0] != runnerPath ||
		!nnpOK || !strings.EqualFold(noNewPrivs, "yes") || !protectSystemOK || protectSystem != "strict" ||
		!protectHomeOK || !strings.EqualFold(protectHome, "yes") || !privateTmpOK || !strings.EqualFold(privateTmp, "yes") ||
		!rwOK || !containsExactPath(strings.Fields(readWritePaths), rootPath) ||
		!delegateOK || !strings.EqualFold(delegate, "yes") || !killModeOK || killMode != "control-group" || !umaskOK || umask != "0077" {
		return fmt.Errorf("%w: protected service unit is not a single non-login managed-root writer with strict filesystem policy", ErrD09ManagedLiveRootRefused)
	}
	for _, key := range []string{"ExecStartPre", "ExecStartPost", "ExecCondition", "ExecReload", "ExecStop", "ExecStopPost",
		"EnvironmentFile", "SupplementaryGroups", "DynamicUser", "RootDirectory", "RootImage", "PAMName"} {
		if len(service["Service"][key]) != 0 {
			return fmt.Errorf("%w: service unit contains disallowed %s policy", ErrD09ManagedLiveRootRefused, key)
		}
	}
	mount := parseSystemdUnit(mountUnit)
	where, whereOK := oneUnitValue(mount, "Mount", "Where")
	what, whatOK := oneUnitValue(mount, "Mount", "What")
	options, optionsOK := oneUnitValue(mount, "Mount", "Options")
	typeValue, typeOK := oneUnitValue(mount, "Mount", "Type")
	mountOptions := strings.Split(options, ",")
	if !whereOK || where != rootPath || !whatOK || !(filepath.IsAbs(what) || strings.HasPrefix(what, "UUID=") || strings.HasPrefix(what, "LABEL=")) ||
		!optionsOK || !typeOK || !mountTypeMatchesMagic(typeValue, filesystemMagic) ||
		!containsExactPath(mountOptions, "rw") || !containsExactPath(mountOptions, "nosuid") || !containsExactPath(mountOptions, "nodev") ||
		containsExactPath(mountOptions, "ro") || containsExactPath(mountOptions, "suid") || containsExactPath(mountOptions, "dev") {
		return fmt.Errorf("%w: protected mount unit does not mount the exact writable, nosuid,nodev live root", ErrD09ManagedLiveRootRefused)
	}
	return nil
}

func containsExactPath(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func verifyNoSystemdDropIns(unitPaths ...string) error {
	if len(unitPaths) == 0 {
		return ErrD09ManagedLiveRootRefused
	}
	unitNames := make([]string, 0, len(unitPaths))
	for _, unitPath := range unitPaths {
		if !validSystemdUnitPath(unitPath) {
			return ErrD09ManagedLiveRootRefused
		}
		unitNames = append(unitNames, filepath.Base(unitPath))
	}
	unitRoots := []string{"/etc/systemd/system", "/run/systemd/system", "/usr/local/lib/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"}
	for _, root := range unitRoots {
		for _, unit := range unitNames {
			dropIns := filepath.Join(root, unit+".d")
			info, statErr := os.Lstat(dropIns)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return errors.Join(ErrD09ManagedLiveRootRefused, statErr)
			}
			owner, ownerOK := info.Sys().(*syscall.Stat_t)
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownerOK || owner.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("%w: systemd drop-in directory is not protected at %s", ErrD09ManagedLiveRootRefused, dropIns)
			}
			entries, err := os.ReadDir(dropIns)
			if err != nil || len(entries) != 0 {
				return errors.Join(ErrD09ManagedLiveRootRefused, fmt.Errorf("systemd drop-in policy is present or unreadable at %s", dropIns), err)
			}
		}
	}
	for _, root := range []string{"/run/systemd/system", "/run/systemd/system.control", "/run/systemd/transient",
		"/run/systemd/generator", "/run/systemd/generator.early", "/run/systemd/generator.late"} {
		for _, unit := range unitNames {
			path := filepath.Join(root, unit)
			if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
				return errors.Join(ErrD09ManagedLiveRootRefused, fmt.Errorf("higher-priority systemd unit override is present at %s", path), err)
			}
		}
	}
	return nil
}

func mountTypeMatchesMagic(mountType string, magic uint64) bool {
	switch strings.ToLower(mountType) {
	case "ext2", "ext3", "ext4":
		return magic == 0xef53
	case "xfs":
		return magic == 0x58465342
	case "btrfs":
		return magic == 0x9123683e
	default:
		return false
	}
}

func currentUnifiedCgroupPath() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	found := false
	var current string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "0::") {
			path := strings.TrimPrefix(line, "0::")
			if found || !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return "", ErrD09ManagedLiveRootRefused
			}
			found, current = true, path
		}
	}
	if !found {
		return "", ErrD09ManagedLiveRootRefused
	}
	return current, nil
}

type managedServiceCgroupIdentity struct {
	device   uint64
	inode    uint64
	mountID  uint64
	hostPath string
}

func measureManagedServiceCgroup(relative string, expectedUID int) (managedServiceCgroupIdentity, error) {
	var identity managedServiceCgroupIdentity
	if expectedUID <= 0 || !filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return identity, ErrD09ManagedLiveRootRefused
	}
	for _, component := range strings.Split(strings.TrimPrefix(relative, string(os.PathSeparator)), string(os.PathSeparator)) {
		if component == "" || component == "." || component == ".." {
			return identity, ErrD09ManagedLiveRootRefused
		}
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return identity, err
	}
	var hostPath string
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "cgroup2 ") {
			continue
		}
		left := strings.Fields(parts[0])
		if len(left) < 5 {
			continue
		}
		rootPath := filepath.Clean(unescapeMountField(left[3]))
		mountPath := filepath.Clean(unescapeMountField(left[4]))
		if rootPath == "/" {
			hostPath = filepath.Join(mountPath, strings.TrimPrefix(relative, string(os.PathSeparator)))
		} else if relative == rootPath || strings.HasPrefix(relative, rootPath+string(os.PathSeparator)) {
			remainder := strings.TrimPrefix(strings.TrimPrefix(relative, rootPath), string(os.PathSeparator))
			hostPath = filepath.Join(mountPath, remainder)
		}
		if hostPath != "" {
			break
		}
	}
	if hostPath == "" {
		return identity, ErrD09ManagedLiveRootRefused
	}
	file, err := openAbsoluteDirectoryNoSymlinks(hostPath)
	if err != nil {
		return identity, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return identity, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != expectedUID || stat.Mode&0o300 != 0o300 || stat.Mode&0o022 != 0 {
		return identity, ErrD09ManagedLiveRootRefused
	}
	typeData, err := readCgroupFile(file, "cgroup.type", 128)
	if err != nil || strings.TrimSpace(string(typeData)) != "domain" {
		return identity, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	available, err := readCgroupFile(file, "cgroup.controllers", 1024)
	if err != nil || !hasCgroupController(available, "cpu") || !hasCgroupController(available, "memory") || !hasCgroupController(available, "pids") {
		return identity, errors.Join(ErrD09ManagedLiveRootRefused, errors.New("managed service cgroup lacks required D09 controllers"), err)
	}
	enabled, err := readCgroupFile(file, "cgroup.subtree_control", 1024)
	if err != nil || !hasCgroupController(enabled, "cpu") || !hasCgroupController(enabled, "memory") || !hasCgroupController(enabled, "pids") {
		return identity, errors.Join(ErrD09ManagedLiveRootRefused, errors.New("managed service cgroup has not delegated required D09 controllers"), err)
	}
	mountID, err := descriptorMountID(file.Fd())
	if err != nil {
		return identity, err
	}
	identity = managedServiceCgroupIdentity{device: uint64(stat.Dev), inode: stat.Ino, mountID: mountID, hostPath: hostPath}
	return identity, nil
}

func readCgroupFile(directory *os.File, name string, maximum int64) ([]byte, error) {
	fd, err := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return data, nil
}

func registeredLocalServiceAccount(passwd []byte, reg D09ManagedLiveRootRegistration) bool {
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(passwd)), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		uid, uidErr := strconv.Atoi(fields[2])
		gid, gidErr := strconv.Atoi(fields[3])
		if uidErr != nil || gidErr != nil {
			return false
		}
		if fields[0] != reg.ServiceName {
			if uid == reg.ServiceUID || gid == reg.ServiceGID {
				return false
			}
			continue
		}
		if found || uid != reg.ServiceUID || gid != reg.ServiceGID || fields[6] != reg.ServiceLoginShell {
			return false
		}
		found = true
	}
	return found
}

func registeredLocalServiceGroup(groupFile []byte, reg D09ManagedLiveRootRegistration) bool {
	foundPrimary := false
	for _, line := range strings.Split(strings.TrimSpace(string(groupFile)), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 4 {
			continue
		}
		gid, err := strconv.Atoi(fields[2])
		if err != nil {
			return false
		}
		if gid == reg.ServiceGID && fields[0] != reg.ServiceGroup {
			return false
		}
		for _, member := range strings.Split(fields[3], ",") {
			if member == reg.ServiceName && (fields[0] != reg.ServiceGroup || gid != reg.ServiceGID) {
				return false
			}
		}
		if fields[0] == reg.ServiceGroup {
			if foundPrimary || gid != reg.ServiceGID {
				return false
			}
			foundPrimary = true
		}
	}
	return foundPrimary
}

func verifyD09ManagedServiceWritablePaths(reg D09ManagedLiveRootRegistration) error {
	for _, path := range reg.ServiceWritablePaths {
		if path == reg.RootPath {
			continue // the registered root descriptor is verified independently
		}
		directory, err := openAbsoluteDirectoryNoSymlinks(path)
		if err != nil {
			return errors.Join(ErrD09ManagedLiveRootRefused,
				fmt.Errorf("open exact service writable directory %s", path), err)
		}
		info, statErr := directory.Stat()
		stat, ok := (*syscall.Stat_t)(nil), false
		if info != nil {
			stat, ok = info.Sys().(*syscall.Stat_t)
		}
		_ = directory.Close()
		if statErr != nil || !ok || !info.IsDir() || int(stat.Uid) != reg.ServiceUID ||
			int(stat.Gid) != reg.ServiceGID || uint32(stat.Mode)&0o7777 != 0o700 {
			return errors.Join(ErrD09ManagedLiveRootRefused,
				fmt.Errorf("service writable directory %s must be service-owned mode 0700", path), statErr)
		}
	}
	return nil
}

func registeredLocalGroupGID(groupFile []byte, name string) (int, bool) {
	if !validIdentity(name) {
		return 0, false
	}
	found := false
	result := 0
	var groups []struct {
		name string
		gid  int
	}
	for _, line := range strings.Split(strings.TrimSpace(string(groupFile)), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 4 {
			continue
		}
		gid, err := strconv.Atoi(fields[2])
		if err != nil || gid <= 0 {
			return 0, false
		}
		groups = append(groups, struct {
			name string
			gid  int
		}{fields[0], gid})
		if fields[0] == name {
			if found {
				return 0, false
			}
			found, result = true, gid
		}
	}
	for _, group := range groups {
		if found && group.name != name && group.gid == result {
			return 0, false
		}
	}
	return result, found
}

func decodeD09ManagedPiServiceConfig(encoded []byte) (D09ManagedPiServiceConfig, error) {
	var config D09ManagedPiServiceConfig
	if len(encoded) == 0 || len(encoded) > D09ManagedPiServiceConfigMaxBytes || protocol.ValidateStrictJSON(encoded) != nil {
		return config, ErrD09ManagedLiveRootRefused
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return D09ManagedPiServiceConfig{}, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return D09ManagedPiServiceConfig{}, ErrD09ManagedLiveRootRefused
	}
	if err := config.Validate(); err != nil {
		return D09ManagedPiServiceConfig{}, err
	}
	return config, nil
}

func verifyD09ManagedPiServiceConfigFiles(config D09ManagedPiServiceConfig) error {
	if err := config.Validate(); err != nil {
		return err
	}
	for _, item := range []struct {
		path   string
		digest string
	}{{config.ContainersConfPath, config.ContainersConfDigest}, {config.StorageConfPath, config.StorageConfDigest}} {
		data, err := readRootOwnedConfigFile(item.path, 1<<20)
		if err != nil || DigestBytes(data) != item.digest {
			return errors.Join(ErrD09ManagedLiveRootRefused,
				fmt.Errorf("pinned Podman configuration digest differs at %s", item.path), err)
		}
	}
	return nil
}

func validateD09ManagedPiServiceUnit(data []byte, serviceName, serviceGroup, runnerPath string,
	config D09ManagedPiServiceConfig, writablePaths []string) error {
	sections := parseSystemdUnit(data)
	service := sections["Service"]
	get := func(key string) (string, bool) { return oneUnitValue(sections, "Service", key) }
	user, userOK := get("User")
	group, groupOK := get("Group")
	execStart, execOK := get("ExecStart")
	killMode, killOK := get("KillMode")
	umask, umaskOK := get("UMask")
	nnp, nnpOK := get("NoNewPrivileges")
	protectSystem, protectSystemOK := get("ProtectSystem")
	protectHome, protectHomeOK := get("ProtectHome")
	privateTmp, privateTmpOK := get("PrivateTmp")
	delegate, delegateOK := get("Delegate")
	runtimeDirectory, runtimeDirectoryOK := get("RuntimeDirectory")
	runtimeDirectoryMode, runtimeDirectoryModeOK := get("RuntimeDirectoryMode")
	stateDirectories, stateDirectoriesOK := get("StateDirectory")
	stateDirectoryMode, stateDirectoryModeOK := get("StateDirectoryMode")
	readWritePaths, readWritePathsOK := get("ReadWritePaths")
	unsetEnvironment, unsetEnvironmentOK := get("UnsetEnvironment")
	wantState := []string{"tbound/audit", "tbound/live", "tbound/podman-home", "tbound/podman-storage/graphroot",
		"tbound/private-admin", "tbound/publication-state", "tbound/sessions"}
	if !userOK || user != serviceName || !groupOK || group != serviceGroup || !execOK ||
		!equalStrings(strings.Fields(execStart), []string{runnerPath, "serve", "--pi", "--service"}) ||
		!killOK || killMode != "control-group" || !umaskOK || umask != "0077" ||
		!nnpOK || !strings.EqualFold(nnp, "yes") || !protectSystemOK || protectSystem != "strict" ||
		!protectHomeOK || !strings.EqualFold(protectHome, "yes") || !privateTmpOK || !strings.EqualFold(privateTmp, "yes") ||
		!delegateOK || !strings.EqualFold(delegate, "yes") || !runtimeDirectoryOK || runtimeDirectory != "tbound-pi" ||
		!runtimeDirectoryModeOK || runtimeDirectoryMode != "0700" || !stateDirectoriesOK ||
		!equalSortedStringList(strings.Fields(stateDirectories), wantState) || !stateDirectoryModeOK || stateDirectoryMode != "0700" ||
		!readWritePathsOK || !equalSortedStringList(strings.Fields(readWritePaths), writablePaths) ||
		!unsetEnvironmentOK || !equalStrings(strings.Fields(unsetEnvironment), []string{"DBUS_SESSION_BUS_ADDRESS"}) {
		return fmt.Errorf("%w: protected Pi service unit differs from the fixed UID, NNP, path, or cleanup contract", ErrD09ManagedLiveRootRefused)
	}
	wantEnvironment := map[string]string{
		"HOME": config.PodmanHome, "USER": serviceName, "LOGNAME": serviceName,
		"XDG_RUNTIME_DIR": config.XDGRuntimeDir, "XDG_CONFIG_HOME": config.XDGConfigHome,
		"XDG_DATA_HOME": config.XDGDataHome, "XDG_CACHE_HOME": config.XDGCacheHome, "TMPDIR": config.TmpDir,
		"CONTAINERS_CONF": config.ContainersConfPath, "CONTAINERS_STORAGE_CONF": config.StorageConfPath,
		"LANG": "C", "LC_ALL": "C",
	}
	if !systemdEnvironmentEqual(service["Environment"], wantEnvironment) {
		return fmt.Errorf("%w: protected Pi service environment differs from the fixed Podman profile", ErrD09ManagedLiveRootRefused)
	}
	for _, key := range []string{"EnvironmentFile", "SupplementaryGroups", "ExecStartPre", "ExecStartPost", "ExecCondition",
		"ExecReload", "ExecStop", "ExecStopPost", "RootDirectory", "RootImage", "DynamicUser", "PAMName"} {
		if len(service[key]) != 0 {
			return fmt.Errorf("%w: protected Pi service contains disallowed %s", ErrD09ManagedLiveRootRefused, key)
		}
	}
	return nil
}

func validateD09ManagedPiServiceSocketUnit(data []byte, socket D09ManagedPiServiceSocket) error {
	sections := parseSystemdUnit(data)
	get := func(key string) (string, bool) { return oneUnitValue(sections, "Socket", key) }
	listen, listenOK := get("ListenStream")
	accept, acceptOK := get("Accept")
	service, serviceOK := get("Service")
	user, userOK := get("SocketUser")
	group, groupOK := get("SocketGroup")
	mode, modeOK := get("SocketMode")
	directoryMode, directoryModeOK := get("DirectoryMode")
	fdName, fdNameOK := get("FileDescriptorName")
	if !listenOK || listen != socket.SocketPath || !acceptOK || !strings.EqualFold(accept, "no") ||
		!serviceOK || service != socket.ServiceUnit || !userOK || user != socket.SocketUser ||
		!groupOK || group != socket.SocketGroup || !modeOK || mode != "0660" ||
		!directoryModeOK || directoryMode != "0755" || !fdNameOK || fdName != "tbound-pi" ||
		len(sections["Socket"]["ListenStream"]) != 1 {
		return fmt.Errorf("%w: protected Pi socket unit differs from the fixed activation contract", ErrD09ManagedLiveRootRefused)
	}
	return nil
}

func equalSortedStringList(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	actual = append([]string(nil), actual...)
	expected = append([]string(nil), expected...)
	sort.Strings(actual)
	sort.Strings(expected)
	for i := range actual {
		if actual[i] != expected[i] || (i > 0 && actual[i-1] == actual[i]) {
			return false
		}
	}
	return true
}

func systemdEnvironmentEqual(directives []string, expected map[string]string) bool {
	actual := make(map[string]string)
	for _, directive := range directives {
		for _, assignment := range strings.Fields(directive) {
			key, value, ok := strings.Cut(assignment, "=")
			if !ok || key == "" || value == "" {
				return false
			}
			if _, duplicate := actual[key]; duplicate {
				return false
			}
			actual[key] = strings.Trim(value, `"'`)
		}
	}
	if len(actual) != len(expected) {
		return false
	}
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func verifyD09ManagedPiServiceSocketParent() error {
	parent, err := openAbsoluteDirectoryNoSymlinks(D09ManagedPiServiceSocketParentPath)
	if err != nil {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	defer parent.Close()
	info, err := parent.Stat()
	stat, ok := (*syscall.Stat_t)(nil), false
	if info != nil {
		stat, ok = info.Sys().(*syscall.Stat_t)
	}
	if err != nil || !ok || !info.IsDir() || stat.Uid != 0 || stat.Gid != 0 ||
		uint32(stat.Mode)&0o7777 != 0o755 || stat.Mode&0o022 != 0 {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Fstatfs(int(parent.Fd()), &filesystem); err != nil || uint64(filesystem.Type) != 0x01021994 {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	mountID, err := descriptorMountID(parent.Fd())
	if err != nil {
		return err
	}
	options, readOnly, err := mountOptionsForID(mountID)
	if err != nil || readOnly || !hasMountOption(options, "nodev") || !hasMountOption(options, "nosuid") {
		return errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return nil
}

func readRootOwnedConfigFile(path string, maximum int64) ([]byte, error) {
	file, _, err := openRootOwnedConfigFile(path, maximum)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	info, statErr := file.Stat()
	if err != nil || statErr != nil || int64(len(data)) > maximum || int64(len(data)) != info.Size() {
		return nil, errors.Join(ErrD09ManagedLiveRootRefused, err, statErr)
	}
	return data, nil
}

func hashRootOwnedConfigFile(path string, maximum int64) (string, error) {
	data, err := readRootOwnedConfigFile(path, maximum)
	if err != nil {
		return "", err
	}
	return DigestBytes(data), nil
}

func openRootOwnedConfigFile(path string, maximum int64) (*os.File, os.FileInfo, error) {
	if !canonicalAbsolutePath(path) || maximum <= 0 {
		return nil, nil, ErrD09ManagedLiveRootRefused
	}
	parent, err := openAbsoluteDirectoryNoSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	fd, err := syscall.Openat(int(parent.Fd()), filepath.Base(path), syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum || info.Mode().Perm()&0o022 != 0 {
		_ = file.Close()
		return nil, nil, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		_ = file.Close()
		return nil, nil, ErrD09ManagedLiveRootRefused
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, nil, errors.Join(ErrD09ManagedLiveRootRefused, err)
	}
	return file, info, nil
}
