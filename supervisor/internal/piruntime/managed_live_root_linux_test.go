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
	return D09ManagedLiveRootRegistration{
		SchemaVersion: D09ManagedLiveRootSchema, WriterPolicy: D09ManagedLiveRootWriterPolicy,
		RootPath: "/var/lib/tbound/live", RootDevice: 10, RootInode: 20, RootFilesystemMagic: 0xef53,
		RootMountID: 101, RootMountOptionsDigest: rootOptions, RootUID: 991, RootGID: 992, RootMode: 0o700,
		ParentPath: "/var/lib/tbound", ParentDevice: 10, ParentInode: 19, ParentFilesystemMagic: 0xef53,
		ParentMountID: 100, ParentMountOptionsDigest: parentOptions, ParentUID: 0, ParentGID: 0, ParentMode: 0o755,
		ServiceName: "tbound-writer", ServiceGroup: "tbound-writer", ServiceUID: 991, ServiceGID: 992,
		ServiceLoginShell: "/usr/sbin/nologin", ServiceShellDigest: DigestBytes([]byte("nologin")),
		PasswdDigest: DigestBytes([]byte("passwd")), GroupDigest: DigestBytes([]byte("group")), NSSwitchDigest: DigestBytes([]byte("nsswitch")),
		ServiceUnitPath: "/etc/systemd/system/tbound-writer.service", ServiceUnitDigest: DigestBytes([]byte("service")),
		MountUnitPath: "/etc/systemd/system/var-lib-tbound-live.mount", MountUnitDigest: DigestBytes([]byte("mount")),
		RunnerPath: "/usr/local/bin/tbound", RunnerDigest: DigestBytes([]byte("runner")),
		ServiceCgroupPath: "/system.slice/tbound-writer.service",
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
	if !registeredLocalServiceAccount([]byte("tbound-writer:x:991:992:TBound:/var/lib/tbound:/usr/sbin/nologin\n"), registration) {
		t.Fatal("dedicated local non-login service account was rejected")
	}
	if !registeredLocalServiceGroup([]byte("tbound-writer:x:992:\n"), registration) {
		t.Fatal("dedicated local service group was rejected")
	}
	if registeredLocalServiceGroup([]byte("tbound-writer:x:992:\nusers:x:100:alice,tbound-writer\n"), registration) {
		t.Fatal("service account in an adjacent group was accepted")
	}
}
