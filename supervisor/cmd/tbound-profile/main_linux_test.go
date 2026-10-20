//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
)

func TestManagedRootSignVerifyOfflineCommands(t *testing.T) {
	dir := t.TempDir()
	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "offline-test.key")
	if err := os.WriteFile(keyPath, privateKey, 0o600); err != nil {
		t.Fatal(err)
	}
	rootMountDigest := piruntime.DigestBytes([]byte("rw,nosuid,nodev,relatime\x00ext4\x00/dev/test"))
	parentMountDigest := piruntime.DigestBytes([]byte("ro,nosuid,nodev,relatime\x00ext4\x00/dev/test"))
	serviceSocket := piruntime.D09ManagedPiServiceSocket{
		SchemaVersion: piruntime.D09ManagedPiServiceSocketSchema, Mode: piruntime.D09ManagedPiServiceSocketMode,
		SocketPath: piruntime.D09ManagedPiServiceSocketPath, SocketUnitPath: piruntime.D09ManagedPiSocketUnitPath,
		SocketUnitDigest: piruntime.DigestBytes([]byte("socket-unit")), ListenStream: piruntime.D09ManagedPiServiceSocketPath,
		Accept: "no", ServiceUnit: "tbound-pi.service", SocketUser: "root", SocketGroup: piruntime.D09ManagedPiSocketGroup,
		SocketUID: 0, SocketGID: 993, SocketMode: 0o660, DirectoryMode: 0o755,
		ParentPath: piruntime.D09ManagedPiServiceSocketParentPath, ParentUID: 0, ParentGID: 0,
		ParentMode: 0o755, ParentFilesystemMagic: 0x01021994, ParentMountPolicy: "tmpfs-rw-nodev-nosuid/v1",
		AllowedClientUIDs: []int{1000}, AllowedClientPrimaryGID: 993,
	}
	registration := piruntime.D09ManagedLiveRootRegistration{
		SchemaVersion: piruntime.D09ManagedLiveRootSchema, WriterPolicy: piruntime.D09ManagedLiveRootWriterPolicy,
		RootPath: piruntime.D09ManagedPiLiveRootPath, RootDevice: 10, RootInode: 20, RootFilesystemMagic: 0xef53,
		RootMountID: 101, RootMountOptionsDigest: rootMountDigest, RootUID: 991, RootGID: 992, RootMode: 0o700,
		ParentPath: piruntime.D09ManagedPiLiveRootParentPath, ParentDevice: 10, ParentInode: 19, ParentFilesystemMagic: 0xef53,
		ParentMountID: 100, ParentMountOptionsDigest: parentMountDigest, ParentUID: 0, ParentGID: 0, ParentMode: 0o755,
		ServiceName: piruntime.D09ManagedPiServiceName, ServiceGroup: piruntime.D09ManagedPiServiceGroup, ServiceUID: 991, ServiceGID: 992,
		ServiceLoginShell: "/usr/sbin/nologin", ServiceShellDigest: piruntime.DigestBytes([]byte("nologin")),
		PasswdDigest: piruntime.DigestBytes([]byte("passwd")), GroupDigest: piruntime.DigestBytes([]byte("group")),
		NSSwitchDigest:  piruntime.DigestBytes([]byte("nsswitch")),
		ServiceUnitPath: piruntime.D09ManagedPiServiceUnitPath, ServiceUnitDigest: piruntime.DigestBytes([]byte("service")),
		MountUnitPath: "/etc/systemd/system/var-lib-tbound-live.mount", MountUnitDigest: piruntime.DigestBytes([]byte("mount")),
		ServiceConfigPath: piruntime.D09ManagedPiServiceConfigPath, ServiceConfigDigest: piruntime.DigestBytes([]byte("service-config")),
		ServiceWritablePaths: []string{
			"/run/tbound-pi", "/var/lib/tbound/audit", "/var/lib/tbound/live", "/var/lib/tbound/podman-home",
			"/var/lib/tbound/podman-storage/graphroot", "/var/lib/tbound/private-admin", "/var/lib/tbound/publication-state",
			"/var/lib/tbound/sessions",
		},
		ServiceSocket: serviceSocket, ManagedRootXattrCalibrationDigest: piruntime.DigestBytes([]byte("managed-root-xattr-calibration")),
		RunnerPath: "/usr/local/bin/tbound", RunnerDigest: piruntime.DigestBytes([]byte("runner")),
		ServiceCgroupPath: piruntime.D09ManagedPiServiceCgroupPath,
	}
	registrationPath := filepath.Join(dir, "registration.json")
	encoded, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registrationPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	signaturePath := filepath.Join(dir, "registration.ed25519")
	signedPath := filepath.Join(dir, "signed-registration.json")
	var out, errOut bytes.Buffer
	if err := run([]string{"managed-root-sign", "--registration", registrationPath, "--key", keyPath,
		"--out-registration", signedPath, "--out-sig", signaturePath}, &out, &errOut); err != nil {
		t.Fatalf("managed-root-sign: %v (%s)", err, errOut.String())
	}
	if strings.Contains(out.String(), hex.EncodeToString(privateKey)) {
		t.Fatal("managed-root-sign printed private key material")
	}
	publicKeyPath := filepath.Join(dir, "registration.pub")
	if err := os.WriteFile(publicKeyPath, privateKey.Public().(ed25519.PublicKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"managed-root-verify", "--registration", signedPath, "--sig", signaturePath, "--pub", publicKeyPath}, &out, &errOut); err != nil {
		t.Fatalf("managed-root-verify: %v (%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "not a live-root quiescence permit") {
		t.Fatal("offline verification overstated registration as present quiescence")
	}
	if err := os.WriteFile(signaturePath, []byte(fmt.Sprintf("%064d", 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"managed-root-verify", "--registration", signedPath, "--sig", signaturePath, "--pub", publicKeyPath}, &out, &errOut); err == nil {
		t.Fatal("managed-root-verify accepted an altered signature")
	}
}

func TestProfileCommandInitSignVerify(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "pi-host-profile.json")
	var out, errOut bytes.Buffer
	if err := run([]string{"init", "--out", profilePath}, &out, &errOut); err != nil {
		t.Fatalf("init: %v (%s)", err, errOut.String())
	}
	if _, err := os.Stat(profilePath); err != nil {
		t.Fatalf("init did not write the template: %v", err)
	}
	if err := run([]string{"init", "--out", profilePath}, &out, &errOut); err == nil {
		t.Fatal("init overwrote an existing profile without --force")
	}
	if err := run([]string{"init", "--out", profilePath, "--force"}, &out, &errOut); err != nil {
		t.Fatalf("init --force: %v", err)
	}

	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "host.ed25519")
	if err := os.WriteFile(keyPath, privateKey, 0o600); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "pi-host-profile.ed25519")
	pubPath := filepath.Join(dir, "pi-host-profile.pub")
	if err := run([]string{"sign", "--profile", profilePath, "--key", keyPath, "--out-sig", sigPath, "--out-pub", pubPath}, &out, &errOut); err != nil {
		t.Fatalf("sign: %v (%s)", err, errOut.String())
	}
	if err := run([]string{"verify", "--profile", profilePath, "--sig", sigPath, "--pub", pubPath}, &out, &errOut); err != nil {
		t.Fatalf("verify: %v (%s)", err, errOut.String())
	}

	// A tampered signed profile must be rejected.
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["NodeVersion"] = "v0.0.0"
	tampered, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--profile", profilePath, "--sig", sigPath, "--pub", pubPath}, &out, &errOut); err == nil {
		t.Fatal("verify accepted a tampered profile")
	}
}

func TestProfileCommandTemplateInitDigest(t *testing.T) {
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "d09-cell.json")
	var out, errOut bytes.Buffer
	if err := run([]string{"template-init", "--out", templatePath}, &out, &errOut); err != nil {
		t.Fatalf("template-init: %v (%s)", err, errOut.String())
	}
	if err := run([]string{"template-init", "--out", templatePath}, &out, &errOut); err == nil {
		t.Fatal("template-init overwrote an existing template without --force")
	}
	if err := run([]string{"template-init", "--out", templatePath, "--force"}, &out, &errOut); err != nil {
		t.Fatalf("template-init --force: %v", err)
	}
	// An unmeasured draft must not yield a bindable digest.
	if err := run([]string{"template-digest", "--template", templatePath}, &out, &errOut); err == nil {
		t.Fatal("template-digest accepted an unmeasured draft template")
	}
	xattrOptionsDigest := piruntime.DigestBytes([]byte("ro,relatime"))
	xattrProfileDigest := piruntime.DigestD09XattrVisibilityProfile(0xef53, xattrOptionsDigest, 1000, 1000, piruntime.D09XattrScanPrivilegeProfile)
	landlockDigest, err := piruntime.DigestD09LandlockRules([]string{"/usr", "/lib", "/lib64", "/etc", "/runtime"}, []string{"/work", "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	measured := `{"image":"localhost/tbound/pi-cell:latest",` +
		`"expected_image_digest":"sha256:` + strings.Repeat("a", 64) + `",` +
		`"cosign_public_key_path":"/etc/tbound/cosign.pub",` +
		`"cosign_public_key_digest":"sha256:` + strings.Repeat("9", 64) + `",` +
		`"oci_layout_path":"/etc/tbound/oci",` +
		`"seccomp_profile_path":"/etc/tbound/seccomp.json",` +
		`"seccomp_profile_digest":"sha256:` + strings.Repeat("b", 64) + `",` +
		`"landlock_abi":8,` +
		`"landlock_ruleset_digest":"` + landlockDigest + `",` +
		`"landlock_ro_path":"/usr,/lib,/lib64,/etc,/runtime","landlock_rw_path":"/work,/tmp",` +
		`"frozen_capabilities":"0000000000000000",` +
		`"entrypoint_path":"/usr/local/bin/tbound-pi-entrypoint",` +
		`"entrypoint_digest":"sha256:` + strings.Repeat("d", 64) + `",` +
		`"runtime":"crun","cgroups_path":"/sys/fs/cgroup/tbound",` +
		`"cpus":"1","pids_max":256,"memory_mb":1024,` +
		`"podman_path":"/usr/bin/podman","podman_digest":"sha256:` + strings.Repeat("e", 64) + `",` +
		`"crun_path":"/usr/bin/crun","crun_digest":"sha256:` + strings.Repeat("f", 64) + `",` +
		`"cosign_path":"/usr/bin/cosign","cosign_digest":"sha256:` + strings.Repeat("1", 64) + `",` +
		`"command_executable":"/bin/bash","command_executable_digest":"sha256:` + strings.Repeat("2", 64) + `",` +
		`"cell_uid":1000,"cell_gid":1000,` +
		`"namespace_profile":"user,mount,pid,ipc,uts,network-none/v1",` +
		`"cgroup_version":"v2","cgroup_controllers":"cpu,memory,pids",` +
		`"xattr_filesystem_magic":61267,"xattr_mount_options_digest":"` + xattrOptionsDigest + `",` +
		`"xattr_scan_uid":1000,"xattr_scan_gid":1000,` +
		`"xattr_scan_privilege_profile":"` + piruntime.D09XattrScanPrivilegeProfile + `",` +
		`"xattr_visibility_profile_digest":"` + xattrProfileDigest + `",` +
		`"xattr_visibility_authority":"` + piruntime.D09XattrVisibilityAuthority + `",` +
		`"managed_live_root_registration_digest":"sha256:` + strings.Repeat("3", 64) + `",` +
		`"user_namespace_mapping_profile":"` + piruntime.D09UserNamespaceMappingProfile + `",` +
		`"user_namespace_mapping_digest":"` + piruntime.DigestD09UserNamespaceMapping(1000, 1000, 991, 992) + `"}`
	measuredPath := filepath.Join(dir, "measured.json")
	if err := os.WriteFile(measuredPath, []byte(measured), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"template-digest", "--template", measuredPath}, &out, &errOut); err != nil {
		t.Fatalf("template-digest on a measured template: %v (%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "sha256:") {
		t.Fatalf("template-digest printed no digest: %q", out.String())
	}
	// The two command-cell delta identities are derived from the printed template digest.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "delta net-sink/v1 template digest: sha256:") ||
		!strings.Contains(lines[2], "delta no-reap/v1 template digest: sha256:") {
		t.Fatalf("template-digest did not print the two delta template digests: %q", out.String())
	}
	base := strings.TrimSpace(lines[0][strings.LastIndex(lines[0], " ")+1:])
	netSink, _ := piruntime.ParseD09CellDelta(piruntime.D09CellDeltaNetSink)
	want, err := netSink.TemplateDigest(base)
	if err != nil || !strings.HasSuffix(lines[1], want) {
		t.Fatalf("printed net-sink digest is not the derived one: %q want %s (%v)", lines[1], want, err)
	}
}

func TestProfileCommandRejectsShortPrivateKey(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "pi-host-profile.json")
	var out, errOut bytes.Buffer
	if err := run([]string{"init", "--out", profilePath}, &out, &errOut); err != nil {
		t.Fatalf("init: %v", err)
	}
	keyPath := filepath.Join(dir, "short.ed25519")
	if err := os.WriteFile(keyPath, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", "--profile", profilePath, "--key", keyPath, "--out-sig", filepath.Join(dir, "sig"), "--out-pub", filepath.Join(dir, "pub")}, &out, &errOut); err == nil {
		t.Fatal("sign accepted a short Ed25519 private key")
	}
}

func TestProfileCommandCgroupRootDigestIsDerivedAndStable(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"cgroup-root-digest", "--uid", "995"}, &out, &errOut); err != nil {
		t.Fatalf("cgroup-root-digest: %v (%s)", err, errOut.String())
	}
	want, err := piruntime.D09ManagedCellsCgroupRootDigest(995)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != want {
		t.Fatalf("cgroup-root-digest printed %q, want the derived %s", out.String(), want)
	}
	// Deterministic: a second run, and the explicit canonical path, agree.
	var again bytes.Buffer
	if err := run([]string{"cgroup-root-digest", "--uid", "995", "--cgroups-path", "/system.slice/tbound-pi.service/cells"}, &again, &errOut); err != nil {
		t.Fatal(err)
	}
	if again.String() != out.String() {
		t.Fatalf("derived value is not deterministic: %q vs %q", again.String(), out.String())
	}
	for _, args := range [][]string{
		{"cgroup-root-digest"},
		{"cgroup-root-digest", "--uid", "0"},
		{"cgroup-root-digest", "--uid", "995", "--cgroups-path", "relative/cells"},
		{"cgroup-root-digest", "--uid", "995", "--cgroups-path", "/system.slice/../cells"},
		{"cgroup-root-digest", "--uid", "995", "extra"},
	} {
		if err := run(args, &bytes.Buffer{}, &errOut); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
}

func TestProfileCommandXattrCalibrationVerifyWrongKeyIsNotATemplateError(t *testing.T) {
	dir := t.TempDir()
	signerPublic, signerPrivate, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPublic, _, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	options := piruntime.DigestBytes([]byte("rw,nodev,nosuid"))
	calibration := piruntime.D09XattrEnumerationCalibration{
		Authority: piruntime.D09XattrVisibilityAuthority, FilesystemMagic: 0xef53, MountOptionsDigest: options,
		ScanUID: 1001, ScanGID: 1002, ScanPrivilegeProfile: piruntime.D09XattrScanPrivilegeProfile,
		SourceAccessProfile: piruntime.D09XattrManagedRWSourceProfile, EnumerationProfile: piruntime.D09XattrEnumerationProfile,
		VisibilityProfileDigest: piruntime.DigestD09XattrVisibilityProfile(0xef53, options, 1001, 1002,
			piruntime.D09XattrScanPrivilegeProfile),
		ProbeCorpusDigest:      piruntime.DigestBytes([]byte("corpus")),
		ProbeObservationDigest: piruntime.DigestBytes([]byte("observation")), ProbeEntryCount: 2, ProbeXattrNameCount: 1,
	}
	encoded, signature, _, err := piruntime.SignD09XattrEnumerationCalibration(calibration, signerPrivate)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	calibrationPath, sigPath := write("cal.json", encoded), write("cal.sig", signature)
	var out, errOut bytes.Buffer
	if err := run([]string{"xattr-calibration-verify", "--calibration", calibrationPath, "--sig", sigPath,
		"--pub", write("signer.pub", signerPublic)}, &out, &errOut); err != nil {
		t.Fatalf("correct key refused: %v", err)
	}
	err = run([]string{"xattr-calibration-verify", "--calibration", calibrationPath, "--sig", sigPath,
		"--pub", write("other.pub", otherPublic)}, &out, &errOut)
	if err == nil {
		t.Fatal("wrong key verified")
	}
	if strings.Contains(err.Error(), "cell template") || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("wrong-key error is misleading: %q", err)
	}
}
