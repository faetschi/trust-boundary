//go:build linux

package piruntime

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testD09ManagedPiServiceConfig() D09ManagedPiServiceConfig {
	return D09ManagedPiServiceConfig{
		SchemaVersion: D09ManagedPiServiceConfigSchema, SourcePolicy: "current-managed-live-root/v1",
		ProviderRoute:         D09ManagedPiProviderRouteEngineering,
		WorkflowAdmissionMode: "fresh", WorkflowAdmissionID: "host-task-2026-10-14",
		WorkflowTaskID: "approved-task", WorkflowPrompt: "Read the approved host task.",
		WorkflowCatalogPath: D09ManagedPiWorkflowCatalogPath, WorkflowRootDBPath: D09ManagedPiWorkflowRootDBPath,
		WorkflowOwnerStoreRoot: D09ManagedPiOwnerStoreRoot, WorkflowMaxPreEffectRetries: 2,
		WorkflowMaxCleanRestarts: 2, WorkflowMaxHostCalls: 4, WorkflowMaxTokens: 10000,
		WorkflowDurationSeconds: 1200,
		PodmanHome:              D09ManagedPiPodmanHome, PodmanGraphroot: D09ManagedPiPodmanGraphroot,
		PodmanRunroot: D09ManagedPiPodmanRunroot, XDGRuntimeDir: D09ManagedPiRuntimeDir,
		XDGConfigHome: D09ManagedPiPodmanConfigHome, XDGDataHome: D09ManagedPiPodmanDataHome,
		XDGCacheHome: D09ManagedPiPodmanCacheHome, TmpDir: D09ManagedPiTempDir,
		ContainersConfPath: D09ManagedPiContainersConfPath, ContainersConfDigest: DigestBytes([]byte("containers")),
		StorageConfPath: D09ManagedPiStorageConfPath, StorageConfDigest: DigestBytes([]byte("storage")),
	}
}

func validManagedLiveRootRegistration() D09ManagedLiveRootRegistration {
	rootOptions := DigestBytes([]byte("rw,nosuid,nodev,relatime\x00ext4\x00/dev/test"))
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
		RootPath: "/var/lib/tbound/live/root", RootDevice: 10, RootInode: 20, RootFilesystemMagic: 0xef53,
		RootMountID: 101, RootMountOptionsDigest: rootOptions, RootUID: 991, RootGID: 992, RootMode: 0o700,
		ParentPath: "/var/lib/tbound/live", ParentDevice: 10, ParentInode: 2, ParentFilesystemMagic: 0xef53,
		ParentMountID: 101, ParentMountOptionsDigest: rootOptions, ParentUID: 991, ParentGID: 992, ParentMode: 0o700,
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

// Publication stages artifacts and the live-root lock in the live root's parent,
// as the non-root writer, and refuses unless both are on one mount.
func TestD09ManagedLiveRootRegistrationRequiresTheStagingParentLayout(t *testing.T) {
	if err := validManagedLiveRootRegistration().Validate(); err != nil {
		t.Fatalf("live root as the fixed child of the service-owned mount root refused: %v", err)
	}
	refused := map[string]func(*D09ManagedLiveRootRegistration){
		"mount root itself": func(r *D09ManagedLiveRootRegistration) {
			r.RootPath, r.ParentPath = "/var/lib/tbound/live", "/var/lib/tbound"
		},
		"another child":            func(r *D09ManagedLiveRootRegistration) { r.RootPath = "/var/lib/tbound/live/other" },
		"parent on another mount":  func(r *D09ManagedLiveRootRegistration) { r.ParentMountID = 100 },
		"parent on another device": func(r *D09ManagedLiveRootRegistration) { r.ParentDevice = 11 },
		"root-owned parent the writer cannot stage in": func(r *D09ManagedLiveRootRegistration) {
			r.ParentUID, r.ParentGID, r.ParentMode = 0, 0, 0o755
		},
		"group-writable parent": func(r *D09ManagedLiveRootRegistration) { r.ParentMode = 0o720 },
	}
	for name, mutate := range refused {
		registration := validManagedLiveRootRegistration()
		mutate(&registration)
		if err := registration.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
			t.Errorf("%s: registration accepted: %v", name, err)
		}
	}
}

func TestD09ManagedLiveRootMountUnitMustMountTheRootsParent(t *testing.T) {
	serviceUnit := []byte("[Service]\nUser=tbound-pi\nGroup=tbound-pi\nExecStart=/usr/local/bin/tbound serve --pi\n" +
		"NoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=yes\nPrivateTmp=yes\nReadWritePaths=/var/lib/tbound/live\n" +
		"Delegate=yes\nDelegateSubgroup=runner\nKillMode=control-group\nUMask=0077\n")
	mountUnit := []byte("[Mount]\nWhat=/var/lib/tbound/live.img\nWhere=/var/lib/tbound/live\nType=ext4\nOptions=loop,rw,nosuid,nodev\n")
	check := func(rootPath string) error {
		return validateManagedSystemdPolicy(rootPath, "tbound-pi", "tbound-pi", "/usr/local/bin/tbound", 0xef53, serviceUnit, mountUnit)
	}
	if err := check("/var/lib/tbound/live/root"); err != nil {
		t.Fatalf("live root as the fixed child of the mount root refused: %v", err)
	}
	for _, rootPath := range []string{"/var/lib/tbound/live", "/var/lib/tbound/live/other",
		"/var/lib/tbound/live/root/nested", "/var/lib/tbound/root", "/var/lib/tbound/livex/root"} {
		if err := check(rootPath); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
			t.Errorf("live root %q accepted against Where=/var/lib/tbound/live: %v", rootPath, err)
		}
	}
}

func TestD09ManagedLiveRootOpensOnlyARealChildDirectory(t *testing.T) {
	mountRoot := t.TempDir()
	mount, err := os.Open(mountRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer mount.Close()
	child := filepath.Join(mountRoot, D09ManagedPiLiveRootName)
	if err := os.Mkdir(filepath.Join(mountRoot, "target"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", child); err != nil {
		t.Fatal(err)
	}
	if root, err := openManagedLiveRootChild(mount); err == nil {
		_ = root.Close()
		t.Fatal("a symlinked live root child was opened")
	}
	if err := os.Remove(child); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(mountRoot, "target"), child); err != nil {
		t.Fatal(err)
	}
	root, err := openManagedLiveRootChild(mount)
	if err != nil {
		t.Fatalf("real live root child refused: %v", err)
	}
	_ = root.Close()
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
DelegateSubgroup=runner
KillMode=control-group
UMask=0077
`)
	mountUnit := []byte(`[Mount]
What=UUID=01234567-89ab-cdef-0123-456789abcdef
Where=/var/lib/tbound/live
Type=ext4
Options=rw,nosuid,nodev
`)
	if err := validateManagedSystemdPolicy("/var/lib/tbound/live/root", "tbound-writer", "tbound-writer", "/usr/local/bin/tbound", 0xef53, serviceUnit, mountUnit); err != nil {
		t.Fatalf("valid measured service/mount policy refused: %v", err)
	}
	unsafe := strings.Replace(string(serviceUnit), "ProtectSystem=strict", "ProtectSystem=off", 1)
	if err := validateManagedSystemdPolicy("/var/lib/tbound/live/root", "tbound-writer", "tbound-writer", "/usr/local/bin/tbound", 0xef53, []byte(unsafe), mountUnit); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("unprotected service filesystem policy accepted: %v", err)
	}
	unsafe = strings.Replace(string(mountUnit), "Options=rw,nosuid,nodev", "Options=rw,suid,dev", 1)
	if err := validateManagedSystemdPolicy("/var/lib/tbound/live/root", "tbound-writer", "tbound-writer", "/usr/local/bin/tbound", 0xef53, serviceUnit, []byte(unsafe)); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("unsafe live-root mount options accepted: %v", err)
	}
	unsafe = strings.Replace(string(serviceUnit), "DelegateSubgroup=runner\n", "", 1)
	if err := validateManagedSystemdPolicy("/var/lib/tbound/live/root", "tbound-writer", "tbound-writer", "/usr/local/bin/tbound", 0xef53, []byte(unsafe), mountUnit); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("service unit without the closed runner delegate subgroup accepted: %v", err)
	}
}

func TestD09CellCgroupRootIsClosedSiblingOfRunner(t *testing.T) {
	parent := "/sys/fs/cgroup/system.slice/tbound-pi.service"
	if got := d09ManagedRunnerCgroupPath(parent); got != parent+"/runner" {
		t.Fatalf("runner leaf = %q", got)
	}
	if !d09CellCgroupRootIsClosedSibling(parent+"/cells", parent) {
		t.Fatal("closed cells sibling of the runner refused")
	}
	for name, cellRoot := range map[string]string{
		"service parent itself":     parent,
		"runner leaf":               parent + "/runner",
		"runner descendant":         parent + "/runner/cells",
		"nested below cells":        parent + "/cells/runner",
		"dotdot into runner":        parent + "/cells/../runner",
		"trailing slash":            parent + "/cells/",
		"other child of the parent": parent + "/other",
		"ancestor of the service":   "/sys/fs/cgroup/system.slice",
		"unrelated cgroup":          "/sys/fs/cgroup/tbound",
	} {
		if d09CellCgroupRootIsClosedSibling(cellRoot, parent) {
			t.Fatalf("cell root %s (%q) accepted", name, cellRoot)
		}
	}
}

func TestD09ManagedPiServiceUnitsPinCleanupAndNarrowWritablePaths(t *testing.T) {
	config := testD09ManagedPiServiceConfig()
	paths := d09ManagedPiServiceWritablePaths()
	unit := []byte("[Unit]\nDescription=tbound-pi\n[Service]\n" +
		"User=tbound-pi\nGroup=tbound-pi\nExecStart=/usr/local/bin/tbound serve --pi --service\n" +
		"KillMode=control-group\nUMask=0077\nNoNewPrivileges=yes\nProtectSystem=strict\n" +
		"ProtectHome=yes\nPrivateTmp=yes\nDelegate=yes\nDelegateSubgroup=runner\nRuntimeDirectory=tbound-pi\n" +
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

func TestD09ManagedPiServiceUnitRequiresClosedRunnerSubgroup(t *testing.T) {
	config := testD09ManagedPiServiceConfig()
	paths := d09ManagedPiServiceWritablePaths()
	unit := "[Unit]\nDescription=tbound-pi\n[Service]\n" +
		"User=tbound-pi\nGroup=tbound-pi\nExecStart=/usr/local/bin/tbound serve --pi --service\n" +
		"KillMode=control-group\nUMask=0077\nNoNewPrivileges=yes\nProtectSystem=strict\n" +
		"ProtectHome=yes\nPrivateTmp=yes\nDelegate=yes\nDelegateSubgroup=runner\nRuntimeDirectory=tbound-pi\n" +
		"RuntimeDirectoryMode=0700\nStateDirectory=tbound/audit tbound/live tbound/podman-home tbound/podman-storage/graphroot tbound/private-admin tbound/publication-state tbound/sessions\n" +
		"StateDirectoryMode=0700\nReadWritePaths=" + strings.Join(paths, " ") + "\n" +
		"Environment=HOME=/var/lib/tbound/podman-home USER=tbound-pi LOGNAME=tbound-pi XDG_RUNTIME_DIR=/run/tbound-pi XDG_CONFIG_HOME=/etc/tbound/podman-config XDG_DATA_HOME=/var/lib/tbound/podman-home/data XDG_CACHE_HOME=/run/tbound-pi/cache TMPDIR=/run/tbound-pi/tmp CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf LANG=C LC_ALL=C\n" +
		"UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS\n"
	if err := validateD09ManagedPiServiceUnit([]byte(unit), D09ManagedPiServiceName, D09ManagedPiServiceGroup,
		"/usr/local/bin/tbound", config, paths); err != nil {
		t.Fatalf("closed runner-subgroup unit refused: %v", err)
	}
	// The main process must live in exactly <service>/runner. Omitting the
	// subgroup leaves it in the service parent, which holds controllers and
	// must stay empty; the cell subtree and any dotdot/nested form are also refused.
	for name, mutated := range map[string]string{
		"missing subgroup":          strings.Replace(unit, "DelegateSubgroup=runner\n", "", 1),
		"empty subgroup":            strings.Replace(unit, "DelegateSubgroup=runner", "DelegateSubgroup=", 1),
		"cell subtree as main leaf": strings.Replace(unit, "DelegateSubgroup=runner", "DelegateSubgroup=cells", 1),
		"dotdot escape":             strings.Replace(unit, "DelegateSubgroup=runner", "DelegateSubgroup=../runner", 1),
		"nested descendant":         strings.Replace(unit, "DelegateSubgroup=runner", "DelegateSubgroup=runner/child", 1),
	} {
		if err := validateD09ManagedPiServiceUnit([]byte(mutated), D09ManagedPiServiceName, D09ManagedPiServiceGroup,
			"/usr/local/bin/tbound", config, paths); err == nil {
			t.Fatalf("service unit with %s accepted", name)
		}
	}
}

func TestD09ManagedPiServiceAndSocketUnitsAreExact(t *testing.T) {
	serviceConfig := testD09ManagedPiServiceConfig()
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
DelegateSubgroup=runner
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

func TestD09ManagedPiServiceConfigProviderRouteIsARequiredEnum(t *testing.T) {
	if D09ManagedPiServiceConfigSchema != "tbound-pi-service-config/v4" {
		t.Fatalf("config schema was not bumped for corpus admission: %q", D09ManagedPiServiceConfigSchema)
	}
	if D09ManagedPiResearchCredentialPath != "/etc/tbound/provider/openrouter.key" ||
		D09ManagedPiProviderCredentialPath != "/etc/tbound/provider/opencode-go.key" {
		t.Fatalf("fixed credential paths drifted: %q %q", D09ManagedPiProviderCredentialPath, D09ManagedPiResearchCredentialPath)
	}
	for _, route := range []string{D09ManagedPiProviderRouteEngineering, D09ManagedPiProviderRouteResearch} {
		config := testD09ManagedPiServiceConfig()
		config.ProviderRoute = route
		if err := config.Validate(); err != nil {
			t.Errorf("route %q refused: %v", route, err)
		}
	}
	for _, route := range []string{"", "Engineering", "research ", "opencode-go", "openrouter", "both"} {
		config := testD09ManagedPiServiceConfig()
		config.ProviderRoute = route
		if err := config.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
			t.Errorf("route %q accepted: %v", route, err)
		}
	}
	// A config document without provider_route decodes to the empty route and is
	// refused: the field is required, there is no engineering default.
	encoded, err := json.Marshal(testD09ManagedPiServiceConfig())
	if err != nil {
		t.Fatal(err)
	}
	withoutRoute := strings.Replace(string(encoded), `"provider_route":"engineering",`, ``, 1)
	if withoutRoute == string(encoded) {
		t.Fatalf("provider_route is not encoded as a config member: %s", encoded)
	}
	if _, err := decodeD09ManagedPiServiceConfig([]byte(withoutRoute)); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("config without provider_route decoded: %v", err)
	}
	if _, err := decodeD09ManagedPiServiceConfig(encoded); err != nil {
		t.Fatalf("config with provider_route refused: %v", err)
	}
}

func TestD09ManagedPiServiceConfigDigestChangesWithProviderRoute(t *testing.T) {
	digest := func(route string) string {
		config := testD09ManagedPiServiceConfig()
		config.ProviderRoute = route
		encoded, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		return DigestBytes(encoded)
	}
	engineering, research := digest(D09ManagedPiProviderRouteEngineering), digest(D09ManagedPiProviderRouteResearch)
	if engineering == research {
		t.Fatal("the service config digest does not cover provider_route")
	}
	if digest(D09ManagedPiProviderRouteResearch) != research {
		t.Fatal("the service config digest is not deterministic")
	}
}

func TestManagedLiveRootAccountChecksAcceptRealUbuntuGuestFiles(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", "ubuntu-24.04-accounts", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	passwd, groupFile, nsswitch := read("passwd"), read("group"), read("nsswitch.conf")
	if !localFilesOnlyNSSwitch(nsswitch) {
		t.Fatal("real guest files-only nsswitch.conf was rejected")
	}
	uid, gid, shell, ok := lookupLocalServiceAccount(passwd, groupFile, "tbound-pi", "tbound-pi")
	if !ok || uid != 995 || gid != 986 || shell != "/usr/sbin/nologin" {
		t.Fatalf("real guest service account lookup = %d %d %q %v", uid, gid, shell, ok)
	}
	reg := D09ManagedLiveRootRegistration{ServiceName: "tbound-pi", ServiceGroup: "tbound-pi",
		ServiceUID: uid, ServiceGID: gid, ServiceLoginShell: shell}
	if !registeredLocalServiceAccount(passwd, reg) {
		t.Fatal("real guest service account was rejected")
	}
	if !registeredLocalServiceGroup(groupFile, reg) {
		t.Fatal("real guest service group was rejected")
	}
	if socketGID, ok := registeredLocalGroupGID(groupFile, D09ManagedPiSocketGroup); !ok || socketGID != 985 {
		t.Fatalf("real guest socket group gid = %d %v, want 985 true", socketGID, ok)
	}
}

func TestRegisteredLocalGroupGIDRefusesNamedNonPositiveAndCollisions(t *testing.T) {
	for name, groupFile := range map[string]string{
		"named root gid 0":       "root:x:0:\ntbound-pi-clients:x:0:\n",
		"named negative gid":     "root:x:0:\ntbound-pi-clients:x:-5:\n",
		"another group same gid": "root:x:0:\ntbound-pi-clients:x:985:\nother:x:985:\n",
		"duplicate named group":  "root:x:0:\ntbound-pi-clients:x:985:\ntbound-pi-clients:x:986:\n",
		"missing":                "root:x:0:\nnogroup:x:65534:\n",
		"unparsable other gid":   "root:x:0:\nbroken:x:abc:\ntbound-pi-clients:x:985:\n",
	} {
		if gid, ok := registeredLocalGroupGID([]byte(groupFile), "tbound-pi-clients"); ok {
			t.Fatalf("%s: accepted with gid %d", name, gid)
		}
	}
	if gid, ok := registeredLocalGroupGID([]byte("root:x:0:\nnogroup:x:65534:\ntbound-pi-clients:x:985:\n"), "tbound-pi-clients"); !ok || gid != 985 {
		t.Fatalf("root and nogroup lines made the lookup fail: %d %v", gid, ok)
	}
}

func TestSystemdDropInCheckContract(t *testing.T) {
	reg := validManagedLiveRootRegistration()
	for _, path := range []string{reg.ServiceUnitPath, reg.MountUnitPath, reg.ServiceSocket.SocketUnitPath} {
		if !validSystemdUnitPath(path) {
			t.Fatalf("registered full unit path was refused: %q", path)
		}
		if validSystemdUnitPath(filepath.Base(path)) {
			t.Fatalf("a bare unit name was accepted as a unit path: %q", filepath.Base(path))
		}
		if err := verifyNoSystemdDropIns(filepath.Base(path)); err == nil {
			t.Fatalf("verifyNoSystemdDropIns accepted a bare unit name: %q", filepath.Base(path))
		}
	}
}

// verifyNoSystemdDropIns takes full /etc/systemd/system paths and refuses a bare
// name, so no caller may pass filepath.Base(...) (real-host defect: measure and
// writer-process verification refused on every real guest).
func TestNoCallerPassesBareUnitNamesToVerifyNoSystemdDropIns(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "managed_live_root_linux.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := call.Fun.(*ast.Ident); !ok || name.Name != "verifyNoSystemdDropIns" {
			return true
		}
		calls++
		for _, arg := range call.Args {
			if inner, ok := arg.(*ast.CallExpr); ok {
				t.Errorf("%s: verifyNoSystemdDropIns argument is a call, not a full unit path", fset.Position(inner.Pos()))
			}
		}
		return true
	})
	if calls != 3 {
		t.Fatalf("expected 3 verifyNoSystemdDropIns callers, found %d", calls)
	}
}
