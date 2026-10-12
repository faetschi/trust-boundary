//go:build linux

package piruntime

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validManagedLiveRootRegistration() D09ManagedLiveRootRegistration {
	rootOptions := DigestBytes([]byte("rw,nosuid,nodev,relatime\x00ext4\x00/dev/test"))
	parentOptions := DigestBytes([]byte("ro,nosuid,nodev,relatime\x00ext4\x00/dev/test"))
	serviceUID, serviceGID := 991, 992
	serviceSocket := D09ManagedPiServiceSocket{
		SchemaVersion: D09ManagedPiServiceSocketSchema, Mode: D09ManagedPiServiceSocketMode,
		SocketPath: D09ManagedPiServiceSocketPath, SocketUnitPath: D09ManagedPiSocketUnitPath,
		SocketUnitDigest: DigestBytes([]byte("socket-unit")), ListenStream: D09ManagedPiServiceSocketPath,
		Accept: "no", ServiceUnit: "tbound-pi.service", SocketUser: "root", SocketGroup: D09ManagedPiSocketGroup,
		SocketUID: 0, SocketGID: 993, SocketMode: 0o660, DirectoryMode: 0o755,
		ParentPath: D09ManagedPiServiceSocketParentPath, ParentUID: 0, ParentGID: 0,
		ParentMode: 0o755, ParentFilesystemMagic: 0x01021994, ParentMountPolicy: "tmpfs-rw-nodev-nosuid/v1",
		AllowedClientUIDs: []int{1000}, AllowedClientPrimaryGID: 994,
	}
	return D09ManagedLiveRootRegistration{
		SchemaVersion: D09ManagedLiveRootSchema, WriterPolicy: D09ManagedLiveRootWriterPolicy,
		RootPath: "/var/lib/tbound/live", RootDevice: 10, RootInode: 20, RootFilesystemMagic: 0xef53,
		RootMountID: 101, RootMountOptionsDigest: rootOptions, RootUID: 991, RootGID: 992, RootMode: 0o700,
		ParentPath: "/var/lib/tbound", ParentDevice: 10, ParentInode: 19, ParentFilesystemMagic: 0xef53,
		ParentMountID: 100, ParentMountOptionsDigest: parentOptions, ParentUID: 0, ParentGID: 0, ParentMode: 0o755,
		ServiceName: D09ManagedPiServiceName, ServiceGroup: D09ManagedPiServiceGroup, ServiceUID: serviceUID, ServiceGID: serviceGID,
		ServiceLoginShell: "/usr/sbin/nologin", ServiceShellDigest: DigestBytes([]byte("nologin")),
		PasswdDigest: DigestBytes([]byte("passwd")), GroupDigest: DigestBytes([]byte("group")), NSSwitchDigest: DigestBytes([]byte("nsswitch")),
		ServiceUnitPath: D09ManagedPiServiceUnitPath, ServiceUnitDigest: DigestBytes([]byte("service")),
		MountUnitPath: "/etc/systemd/system/var-lib-tbound-live.mount", MountUnitDigest: DigestBytes([]byte("mount")),
		ServiceConfigPath: D09ManagedPiServiceConfigPath, ServiceConfigDigest: DigestBytes([]byte("service-config")),
		ServiceWritablePaths: d09ManagedPiServiceWritablePaths(), ServiceSocket: serviceSocket,
		ManagedRootXattrCalibrationDigest: DigestBytes([]byte("signed-managed-rw-calibration")),
		RunnerPath:                        "/usr/local/bin/tbound", RunnerDigest: DigestBytes([]byte("runner")),
		ServiceCgroupPath: D09ManagedPiServiceCgroupPath,
	}
}

func TestD09ManagedLiveRootRegistrationSignatureAndClosedPolicy(t *testing.T) {
	seed := [ed25519.SeedSize]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	encoded, signature, digest, err := SignD09ManagedLiveRootRegistration(validManagedLiveRootRegistration(), privateKey)
	if err != nil {
		t.Fatalf("sign policy registration: %v", err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("registration digest is not canonical: %q", digest)
	}
	verified, err := VerifyD09ManagedLiveRootRegistration(encoded, signature, privateKey.Public().(ed25519.PublicKey))
	if err != nil || verified.Digest != digest {
		t.Fatalf("verify signed registration: digest=%q err=%v", verified.Digest, err)
	}
	if _, err := VerifyD09ManagedLiveRootRegistration(encoded, signature, ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("registration verified with a self-selected trust root: %v", err)
	}
	if _, err := VerifyD09ManagedLiveRootRegistration(append(encoded, []byte(` {"writer_policy":"caller-complete"}`)...), signature, privateKey.Public().(ed25519.PublicKey)); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("duplicate/extra registration content was accepted: %v", err)
	}

	var roundTrip D09ManagedLiveRootRegistration
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	mutated := roundTrip
	mutated.WriterPolicy = "0700-means-exclusive/v1"
	if err := mutated.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("unknown writer policy enum was accepted: %v", err)
	}
	mutated = roundTrip
	mutated.RootUID = 0
	if err := mutated.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("root ownership by the privileged account was accepted: %v", err)
	}
}

func TestD09ManagedLiveRootSystemdPolicyIsConcrete(t *testing.T) {
	serviceUnit := []byte(`[Unit]
Description=TBound managed writer
[Service]
User=tbound-writer
Group=tbound-writer
ExecStart=/usr/local/bin/tbound serve --pi
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/var/lib/tbound/live
Delegate=yes
KillMode=control-group
UMask=0077
`)
	mountUnit := []byte(`[Mount]
What=UUID=01234567-89ab-cdef-0123-456789abcdef
Where=/var/lib/tbound/live
Type=ext4
Options=rw,nosuid,nodev
`)
	if err := validateManagedSystemdPolicy("/var/lib/tbound/live", "tbound-writer", "tbound-writer", "/usr/local/bin/tbound", 0xef53, serviceUnit, mountUnit); err != nil {
		t.Fatalf("valid measured service/mount policy refused: %v", err)
	}
	unsafe := strings.Replace(string(serviceUnit), "ProtectSystem=strict", "ProtectSystem=off", 1)
	if err := validateManagedSystemdPolicy("/var/lib/tbound/live", "tbound-writer", "tbound-writer", "/usr/local/bin/tbound", 0xef53, []byte(unsafe), mountUnit); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("unprotected service filesystem policy accepted: %v", err)
	}
	unsafe = strings.Replace(string(mountUnit), "Options=rw,nosuid,nodev", "Options=rw,suid,dev", 1)
	if err := validateManagedSystemdPolicy("/var/lib/tbound/live", "tbound-writer", "tbound-writer", "/usr/local/bin/tbound", 0xef53, serviceUnit, []byte(unsafe)); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("unsafe live-root mount options accepted: %v", err)
	}
}

func TestD09ManagedPiServiceUnitsPinCleanupAndNarrowWritablePaths(t *testing.T) {
	config := D09ManagedPiServiceConfig{
		SchemaVersion: D09ManagedPiServiceConfigSchema, SourcePolicy: "current-managed-live-root/v1",
		PodmanHome: D09ManagedPiPodmanHome, PodmanGraphroot: D09ManagedPiPodmanGraphroot,
		PodmanRunroot: D09ManagedPiPodmanRunroot, XDGRuntimeDir: D09ManagedPiRuntimeDir,
		XDGConfigHome: D09ManagedPiPodmanConfigHome, XDGDataHome: D09ManagedPiPodmanDataHome,
		XDGCacheHome: D09ManagedPiPodmanCacheHome, TmpDir: D09ManagedPiTempDir,
		ContainersConfPath: D09ManagedPiContainersConfPath, ContainersConfDigest: DigestBytes([]byte("containers")),
		StorageConfPath: D09ManagedPiStorageConfPath, StorageConfDigest: DigestBytes([]byte("storage")),
	}
	paths := d09ManagedPiServiceWritablePaths()
	unit := []byte("[Unit]\nDescription=tbound-pi\n[Service]\n" +
		"User=tbound-pi\nGroup=tbound-pi\nExecStart=/usr/local/bin/tbound serve --pi --service\n" +
		"KillMode=control-group\nUMask=0077\nNoNewPrivileges=yes\nProtectSystem=strict\n" +
		"ProtectHome=yes\nPrivateTmp=yes\nDelegate=yes\nRuntimeDirectory=tbound-pi\n" +
		"RuntimeDirectoryMode=0700\nStateDirectory=tbound/audit tbound/live tbound/podman-home tbound/podman-storage/graphroot tbound/private-admin tbound/publication-state tbound/sessions\n" +
		"StateDirectoryMode=0700\nReadWritePaths=" + strings.Join(paths, " ") + "\n" +
		"Environment=HOME=/var/lib/tbound/podman-home USER=tbound-pi LOGNAME=tbound-pi XDG_RUNTIME_DIR=/run/tbound-pi XDG_CONFIG_HOME=/etc/tbound/podman-config XDG_DATA_HOME=/var/lib/tbound/podman-home/data XDG_CACHE_HOME=/run/tbound-pi/cache TMPDIR=/run/tbound-pi/tmp CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf LANG=C LC_ALL=C\n" +
		"UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS\n")
	if err := validateD09ManagedPiServiceUnit(unit, D09ManagedPiServiceName, D09ManagedPiServiceGroup,
		"/usr/local/bin/tbound", config, paths); err != nil {
		t.Fatalf("valid fixed service unit refused: %v", err)
	}
	unsafe := []byte(strings.Replace(string(unit), "KillMode=control-group", "KillMode=process", 1))
	if err := validateD09ManagedPiServiceUnit(unsafe, D09ManagedPiServiceName, D09ManagedPiServiceGroup,
		"/usr/local/bin/tbound", config, paths); err == nil {
		t.Fatal("service unit without cgroup-wide cleanup accepted")
	}
	unsafe = []byte(strings.Replace(string(unit), "/var/lib/tbound/sessions", "/var/lib/tbound", 1))
	if err := validateD09ManagedPiServiceUnit(unsafe, D09ManagedPiServiceName, D09ManagedPiServiceGroup,
		"/usr/local/bin/tbound", config, paths); err == nil {
		t.Fatal("service unit granting the broad managed-root parent accepted")
	}

	socket := validManagedLiveRootRegistration().ServiceSocket
	socketUnit := []byte("[Unit]\nDescription=tbound-pi socket\n[Socket]\n" +
		"ListenStream=/run/tbound/serve-pi.sock\nAccept=no\nService=tbound-pi.service\n" +
		"SocketUser=root\nSocketGroup=tbound-pi-clients\nSocketMode=0660\nDirectoryMode=0755\n" +
		"FileDescriptorName=tbound-pi\n")
	if err := validateD09ManagedPiServiceSocketUnit(socketUnit, socket); err != nil {
		t.Fatalf("valid fixed socket unit refused: %v", err)
	}
	socketUnit = []byte(strings.Replace(string(socketUnit), "Accept=no", "Accept=yes", 1))
	if err := validateD09ManagedPiServiceSocketUnit(socketUnit, socket); err == nil {
		t.Fatal("per-connection service activation accepted")
	}
}

func TestD09ManagedPiServiceAndSocketUnitsAreExact(t *testing.T) {
	serviceConfig := D09ManagedPiServiceConfig{
		SchemaVersion: D09ManagedPiServiceConfigSchema, SourcePolicy: "current-managed-live-root/v1",
		PodmanHome: D09ManagedPiPodmanHome, PodmanGraphroot: D09ManagedPiPodmanGraphroot,
		PodmanRunroot: D09ManagedPiPodmanRunroot, XDGRuntimeDir: D09ManagedPiRuntimeDir,
		XDGConfigHome: D09ManagedPiPodmanConfigHome, XDGDataHome: D09ManagedPiPodmanDataHome,
		XDGCacheHome: D09ManagedPiPodmanCacheHome, TmpDir: D09ManagedPiTempDir,
		ContainersConfPath: D09ManagedPiContainersConfPath, ContainersConfDigest: DigestBytes([]byte("containers")),
		StorageConfPath: D09ManagedPiStorageConfPath, StorageConfDigest: DigestBytes([]byte("storage")),
	}
	serviceUnit := []byte(`[Unit]
Description=TBound Pi service
[Service]
User=tbound-pi
Group=tbound-pi
ExecStart=/usr/local/bin/tbound serve --pi --service
KillMode=control-group
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
Delegate=yes
RuntimeDirectory=tbound-pi
RuntimeDirectoryMode=0700
StateDirectory=tbound/audit tbound/live tbound/podman-home tbound/podman-storage/graphroot tbound/private-admin tbound/publication-state tbound/sessions
StateDirectoryMode=0700
ReadWritePaths=/run/tbound-pi /var/lib/tbound/audit /var/lib/tbound/live /var/lib/tbound/podman-home /var/lib/tbound/podman-storage/graphroot /var/lib/tbound/private-admin /var/lib/tbound/publication-state /var/lib/tbound/sessions
Environment=HOME=/var/lib/tbound/podman-home USER=tbound-pi LOGNAME=tbound-pi XDG_RUNTIME_DIR=/run/tbound-pi XDG_CONFIG_HOME=/etc/tbound/podman-config XDG_DATA_HOME=/var/lib/tbound/podman-home/data XDG_CACHE_HOME=/run/tbound-pi/cache TMPDIR=/run/tbound-pi/tmp CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf LANG=C LC_ALL=C
UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS
`)
	if err := validateD09ManagedPiServiceUnit(serviceUnit, "tbound-pi", "tbound-pi",
		"/usr/local/bin/tbound", serviceConfig, d09ManagedPiServiceWritablePaths()); err != nil {
		t.Fatalf("valid exact Pi service unit refused: %v", err)
	}
	unsafe := strings.Replace(string(serviceUnit), "KillMode=control-group", "KillMode=process", 1)
	if err := validateD09ManagedPiServiceUnit([]byte(unsafe), "tbound-pi", "tbound-pi",
		"/usr/local/bin/tbound", serviceConfig, d09ManagedPiServiceWritablePaths()); err == nil {
		t.Fatal("service unit with unsafe kill mode accepted")
	}
	unsafe = strings.Replace(string(serviceUnit), "/run/tbound-pi /var/lib/tbound/audit", "/var/lib/tbound /var/lib/tbound/audit", 1)
	if err := validateD09ManagedPiServiceUnit([]byte(unsafe), "tbound-pi", "tbound-pi",
		"/usr/local/bin/tbound", serviceConfig, d09ManagedPiServiceWritablePaths()); err == nil {
		t.Fatal("broad /var/lib/tbound write grant accepted")
	}
	socket := D09ManagedPiServiceSocket{
		SchemaVersion: D09ManagedPiServiceSocketSchema, Mode: D09ManagedPiServiceSocketMode,
		SocketPath: D09ManagedPiServiceSocketPath, ServiceUnit: "tbound-pi.service",
		SocketUser: "root", SocketGroup: D09ManagedPiSocketGroup,
	}
	socketUnit := []byte(`[Unit]
Description=TBound Pi socket
[Socket]
ListenStream=/run/tbound/serve-pi.sock
Accept=no
Service=tbound-pi.service
SocketUser=root
SocketGroup=tbound-pi-clients
SocketMode=0660
DirectoryMode=0755
FileDescriptorName=tbound-pi
`)
	if err := validateD09ManagedPiServiceSocketUnit(socketUnit, socket); err != nil {
		t.Fatalf("valid fixed socket unit refused: %v", err)
	}
	unsafe = strings.Replace(string(socketUnit), "FileDescriptorName=tbound-pi", "FileDescriptorName=other", 1)
	if err := validateD09ManagedPiServiceSocketUnit([]byte(unsafe), socket); err == nil {
		t.Fatal("socket unit with another descriptor name accepted")
	}
}

func TestD09ImageFD3ExecutableMustMatchSignedHostDigest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, strings.TrimPrefix(PiWorkerCellExecutable, string(os.PathSeparator)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("immutable Node ELF fixture"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o555); err != nil {
		t.Fatal(err)
	}
	digest, err := hashImageExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	imageRef := "localhost/tbound/pi-cell@sha256:" + strings.Repeat("a", 64)
	runner := &fakeD09Runner{results: map[string]D09CommandResult{
		"podman image mount " + imageRef:   {ExitCode: 0, Stdout: []byte(root + "\n")},
		"podman image unmount " + imageRef: {ExitCode: 0},
	}}
	launcher := &D09PodmanCrunLauncher{runner: runner}
	if err := launcher.verifyImageWorkerExecutable(context.Background(), "podman", imageRef, PiWorkerCellExecutable, digest); err != nil {
		t.Fatalf("matching image/host fd3 executable was refused: %v", err)
	}
	if err := launcher.verifyImageWorkerExecutable(context.Background(), "podman", imageRef, PiWorkerCellExecutable, DigestBytes([]byte("different host node"))); err == nil {
		t.Fatal("different host fd3 executable digest was accepted for the signed image")
	}
}

func TestManagedWriterRequiresFilesOnlyLocalIdentityAndSingleGroup(t *testing.T) {
	valid := []byte("passwd: files\ngroup: files\nshadow: files\n")
	if !localFilesOnlyNSSwitch(valid) {
		t.Fatal("files-only local account resolution was rejected")
	}
	for _, untrusted := range [][]byte{
		[]byte("passwd: files ldap\ngroup: files\n"),
		[]byte("passwd: systemd\ngroup: files\n"),
		[]byte("passwd: files\ngroup: files systemd\n"),
	} {
		if localFilesOnlyNSSwitch(untrusted) {
			t.Fatalf("non-files-only account resolver accepted: %q", untrusted)
		}
	}
	registration := validManagedLiveRootRegistration()
	if !registeredLocalServiceAccount([]byte("tbound-pi:x:991:992:TBound:/var/lib/tbound:/usr/sbin/nologin\n"), registration) {
		t.Fatal("dedicated local non-login service account was rejected")
	}
	if !registeredLocalServiceGroup([]byte("tbound-pi:x:992:\n"), registration) {
		t.Fatal("dedicated local service group was rejected")
	}
	if registeredLocalServiceGroup([]byte("tbound-pi:x:992:\nusers:x:100:alice,tbound-pi\n"), registration) {
		t.Fatal("service account in an adjacent group was accepted")
	}
}
