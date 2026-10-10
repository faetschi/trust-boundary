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
	registration := piruntime.D09ManagedLiveRootRegistration{
		SchemaVersion: piruntime.D09ManagedLiveRootSchema, WriterPolicy: piruntime.D09ManagedLiveRootWriterPolicy,
		RootPath: "/var/lib/tbound/live", RootDevice: 10, RootInode: 20, RootFilesystemMagic: 0xef53,
		RootMountID: 101, RootMountOptionsDigest: rootMountDigest, RootUID: 991, RootGID: 992, RootMode: 0o700,
		ParentPath: "/var/lib/tbound", ParentDevice: 10, ParentInode: 19, ParentFilesystemMagic: 0xef53,
		ParentMountID: 100, ParentMountOptionsDigest: parentMountDigest, ParentUID: 0, ParentGID: 0, ParentMode: 0o755,
		ServiceName: "tbound-writer", ServiceGroup: "tbound-writer", ServiceUID: 991, ServiceGID: 992,
		ServiceLoginShell: "/usr/sbin/nologin", ServiceShellDigest: piruntime.DigestBytes([]byte("nologin")),
		PasswdDigest: piruntime.DigestBytes([]byte("passwd")), GroupDigest: piruntime.DigestBytes([]byte("group")),
		NSSwitchDigest:  piruntime.DigestBytes([]byte("nsswitch")),
		ServiceUnitPath: "/etc/systemd/system/tbound-writer.service", ServiceUnitDigest: piruntime.DigestBytes([]byte("service")),
		MountUnitPath: "/etc/systemd/system/var-lib-tbound-live.mount", MountUnitDigest: piruntime.DigestBytes([]byte("mount")),
		RunnerPath: "/usr/local/bin/tbound", RunnerDigest: piruntime.DigestBytes([]byte("runner")),
		ServiceCgroupPath: "/system.slice/tbound-writer.service",
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
		`"managed_live_root_registration_digest":"sha256:` + strings.Repeat("3", 64) + `"}`
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
