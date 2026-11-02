//go:build linux

package piruntime

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/sessionrepo"
)

// This freezes the thesis D09 baseline before the product unit touches shared code. Each golden
// was derived from the baseline source, not regenerated from the code under test, so a change that
// moves a thesis byte, path, environment entry, or argv element fails here.

// thesisD09GoldenHostProfileDomain is linuxHostProfileSignatureDomain at the baseline.
const thesisD09GoldenHostProfileDomain = "tbound-linux-host-profile/v1\x00"

// thesisD09GoldenHostProfileJSON is json.Marshal of the profile built in the test, with Digest cleared.
const thesisD09GoldenHostProfileJSON = `{"ID":"tbound-golden-host-profile","Digest":"","Mode":"production","ChildExecutable":"","ExecutableDigest":"","NodeVersion":"","ChildArgumentsProfile":"","ChildArgumentsDigest":"","WorkingDirectory":"","WorkingDirectoryDigest":"","StdinProfile":"","StdinDigest":"","StdoutProfile":"","StdoutProfileDigest":"","ChildEnvironment":"","ChildEnvironmentDigest":"","IPCProfile":"","IPCProfileDigest":"","TerminalProfile":"","TerminalProfileDigest":"","ContainmentProfile":"d09-cell/v1","ContainmentProfileDigest":"containment-profile-digest-golden","CgroupRootDigest":"cgroup-root-digest-golden","SettlementProfile":"","SettlementProfileDigest":"","ObservationProfile":"","ObservationProfileDigest":"","DescriptorProfile":"","DescriptorProfileDigest":"","PiRuntimeBundleRoot":"","PiRuntimeBundleDigest":"","SourceProvenance":"","SourceProvenanceDigest":"","OfflineVerifier":"","OfflineVerifierDigest":"","ProviderProfile":"","ProviderProfileDigest":""}`

// thesisD09GoldenDraftTemplateJSON is MarshalDefaultD09CellTemplate: the unmeasured draft in D09CellTemplate field order.
const thesisD09GoldenDraftTemplateJSON = `{"image":"localhost/tbound/pi-cell:latest","expected_image_digest":"","cosign_public_key_path":"/etc/tbound/cosign.pub","cosign_public_key_digest":"","oci_layout_path":"/etc/tbound/pi-cell-oci","seccomp_profile_path":"/etc/tbound/seccomp.json","seccomp_profile_digest":"","landlock_abi":0,"landlock_ruleset_digest":"","landlock_ro_path":"","landlock_rw_path":"","frozen_capabilities":"","entrypoint_path":"/usr/local/bin/tbound-pi-entrypoint","entrypoint_digest":"","runtime":"crun","cgroups_path":"/sys/fs/cgroup/tbound","cpus":"","pids_max":0,"memory_mb":0,"podman_path":"","podman_digest":"","crun_path":"","crun_digest":"","cosign_path":"","cosign_digest":"","command_executable":"","command_executable_digest":"","cell_uid":0,"cell_gid":0,"namespace_profile":"user,mount,pid,ipc,uts,network-none/v1","cgroup_version":"v2","cgroup_controllers":"cpu,memory,pids","xattr_visibility_profile_digest":"","xattr_visibility_authority":"","xattr_filesystem_magic":0,"xattr_mount_options_digest":"","xattr_scan_uid":0,"xattr_scan_gid":0,"xattr_scan_privilege_profile":"","managed_live_root_registration_digest":"","user_namespace_mapping_profile":"","user_namespace_mapping_digest":""}`

// thesisD09GoldenEnvKeys are the variables d09ProbeEnv may read: its allow-list and the pinned Podman keys.
var thesisD09GoldenEnvKeys = []string{
	"HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "TMPDIR", "LANG", "LC_ALL",
	"DBUS_SESSION_BUS_ADDRESS", "CONTAINERS_CONF", "CONTAINERS_STORAGE_CONF", "XDG_CACHE_HOME",
}

// thesisD09GoldenInheritedEnv is the inherited environment of a thesis service. DBUS_SESSION_BUS_ADDRESS is absent.
func thesisD09GoldenInheritedEnv() map[string]string {
	return map[string]string{
		"HOME":                    "/home/tbound-golden",
		"USER":                    "tbound-golden",
		"LOGNAME":                 "tbound-golden",
		"XDG_RUNTIME_DIR":         "/run/user/4242",
		"XDG_CONFIG_HOME":         "/home/tbound-golden/.config",
		"XDG_DATA_HOME":           "/home/tbound-golden/.local/share",
		"TMPDIR":                  "/tmp",
		"LANG":                    "C.UTF-8",
		"LC_ALL":                  "C.UTF-8",
		"CONTAINERS_CONF":         "/etc/tbound/podman-config/containers.conf",
		"CONTAINERS_STORAGE_CONF": "/etc/tbound/podman-config/storage.conf",
		"XDG_CACHE_HOME":          "/run/tbound-pi/cache",
	}
}

// thesisD09GoldenProbeEnv runs d09ProbeEnv under exactly the inherited variables and returns it sorted.
func thesisD09GoldenProbeEnv(t *testing.T, inherited map[string]string) []string {
	t.Helper()
	for _, key := range thesisD09GoldenEnvKeys {
		t.Setenv(key, "") // registers the caller's value for restoration
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range inherited {
		t.Setenv(key, value)
	}
	env := d09ProbeEnv()
	slices.Sort(env)
	return env
}

func TestThesisD09DefaultAdmissionAndRecordsAreUnchanged(t *testing.T) {
	t.Run("default profile bytes and digest", func(t *testing.T) {
		raw, err := LinuxHostProfileSigningBytes(DefaultLinuxHostProfileTemplate())
		if err != nil {
			t.Fatal(err)
		}
		const golden = thesisD09GoldenHostProfileDomain + `{"ID":"pi-host-profile-template-v1","Digest":"","Mode":"production","ChildExecutable":"/usr/local/bin/node","ExecutableDigest":"sha256:bae70a6673abb67f5fec6fb8a51b8477c084ec3bbed7df7fbe4521a5cf95b53a","NodeVersion":"v24.21.0","ChildArgumentsProfile":"governed-pi-cli-rpc-argv/0.87.1","ChildArgumentsDigest":"sha256:83f98f24df18ca494edd326a80360125d80194438390459a2d9ff6cc4cb068fe","WorkingDirectory":"/var/lib/tbound/pi-worker","WorkingDirectoryDigest":"sha256:ef473fa0f5c30d16fd99e9c39c60f784bbac9187932dd8ce75cf108cf3043c6b","StdinProfile":"pi-rpc-jsonl-stdin/v0.87.1","StdinDigest":"sha256:bb5a7a5487d8fd67afdc2aaa30e3612ffebd0d09f3c05be19aa0c1b150aa7794","StdoutProfile":"pi-rpc-jsonl-stdout/v0.87.1","StdoutProfileDigest":"sha256:3e48dbe54decfcb54b44f30f0fbc2fa61bd348d10557fc86ca1717097c609963","ChildEnvironment":"governed-pi-cli-cell-env/v1","ChildEnvironmentDigest":"sha256:5c1819d74a612b44f3ee4b2df78cc3789c57587afeb6fa1e8a6a411a6cc78d73","IPCProfile":"tbound-ipc/v1/fd6","IPCProfileDigest":"sha256:c8afcf6de758c786b927e3f36f4f7c71cafe1a245baa6a8501a24d3dc00199a5","TerminalProfile":"pi-rpc-no-terminal/v1","TerminalProfileDigest":"sha256:a91e5826b45a0f0be98f457b307eb2b5fdc574da8822c81521103092175f42c7","ContainmentProfile":"d09-rootless-podman-crun-frozen-cell/v1","ContainmentProfileDigest":"sha256:05a691f5ee236f9824419730a0cac2eaa13d383b774b5087e7945f6968e317bc","CgroupRootDigest":"sha256:282d223e7747a9c769252fcd68ff1e48700da749e79abd87bd00253ccbb8a6f1","SettlementProfile":"d09-pidfd-cgroup-kill-settlement/v1","SettlementProfileDigest":"sha256:80fc1448dc9016a5e32148a4588a1dd78f0a65cfdf77dbc4196135ad7f818578","ObservationProfile":"worker-events/fd8/v1","ObservationProfileDigest":"sha256:55dc93a1fd09e3e65c7e4b0067a222b97469ee93ba25ac33a77f40e3689912b1","DescriptorProfile":"tbound-pi-cli-fdmap/v1;exec=3;exposure=4;bootstrap=5;ipc=6;provider=7;events=8;bundle=9;control=0","DescriptorProfileDigest":"sha256:30125bf5c90bf5aef722ec41a808db23d441e848c571ffff9edd15b93c451abf","PiRuntimeBundleRoot":"/var/lib/tbound/runtime","PiRuntimeBundleDigest":"sha256:e8622b683b984c27c713cbc84355b1225224011f62af13ee07ecb37b5c4ef167","SourceProvenance":"source-provenance/d06/v1","SourceProvenanceDigest":"sha256:28e85e8ee72816f5e24b6cbadf5a68f65d700bce576becbc31f1b0102d386eee","OfflineVerifier":"cosign-offline/v1","OfflineVerifierDigest":"sha256:41d81e899b1ffdaa26f87de123d687e0ef6444d358b34a5fbbe96934312d6f04","ProviderProfile":"go-providerbridge/v1","ProviderProfileDigest":"sha256:027cce8e0217db361eb2d1d4267a060226350015d12e8fd16ec6d249e4ffaa0b"}`
		if string(raw) != golden || DigestBytes(raw) != "sha256:4e864352a9f886b0168b626662889cde1345511368bc2ad236bc9a1df7fe553c" {
			t.Fatalf("default profile bytes or digest drifted: %q", raw)
		}
	})
	t.Run("fixed paths and signature domains", func(t *testing.T) {
		for _, pinned := range []struct{ name, got, want string }{
			{"registered D09 cell template", D09CellTemplatePath, "/etc/tbound/d09-cell.json"},
			{"host profile directory", linuxTrustedProfileDirectory, "/etc/tbound"},
			{"host profile", linuxTrustedProfilePath, "/etc/tbound/pi-host-profile.json"},
			{"host profile signature", linuxTrustedProfileSigPath, "/etc/tbound/pi-host-profile.ed25519"},
			{"host profile public key", linuxTrustedProfileKeyPath, "/etc/tbound/pi-host-profile.pub"},
			{"managed live-root registration", D09ManagedLiveRootRegistrationPath, "/etc/tbound/managed-live-root.json"},
			{"managed live-root signature", D09ManagedLiveRootSignaturePath, "/etc/tbound/managed-live-root.ed25519"},
			{"pinned containers.conf", D09ManagedPiContainersConfPath, "/etc/tbound/podman-config/containers.conf"},
			{"pinned storage.conf", D09ManagedPiStorageConfPath, "/etc/tbound/podman-config/storage.conf"},
			{"pinned Podman cache", D09ManagedPiPodmanCacheHome, "/run/tbound-pi/cache"},
			{"host profile signature domain", linuxHostProfileSignatureDomain, thesisD09GoldenHostProfileDomain},
			{"managed live-root signature domain", d09ManagedLiveRootSignatureDomain, "tbound-managed-live-root-registration/v1\x00"},
		} {
			if pinned.got != pinned.want {
				t.Errorf("%s moved: got %q, want %q", pinned.name, pinned.got, pinned.want)
			}
		}
	})

	t.Run("draft template bytes and digest", func(t *testing.T) {
		canonical, err := MarshalDefaultD09CellTemplate()
		if err != nil {
			t.Fatal(err)
		}
		if string(canonical) != thesisD09GoldenDraftTemplateJSON {
			t.Fatalf("draft template canonical bytes drifted:\n got %s\nwant %s", canonical, thesisD09GoldenDraftTemplateJSON)
		}
		digest, err := DefaultD09CellTemplate().Digest()
		if err != nil {
			t.Fatal(err)
		}
		// The SHA-256 is recomputed with the standard library over the golden bytes, not with DigestBytes under test.
		sum := sha256.Sum256([]byte(thesisD09GoldenDraftTemplateJSON))
		if want := "sha256:" + hex.EncodeToString(sum[:]); digest != want {
			t.Fatalf("draft template digest = %s, want %s", digest, want)
		}
	})

	t.Run("thesis decoder and validator refuse non-thesis templates", func(t *testing.T) {
		if err := validateD09CellTemplate(DefaultD09CellTemplate()); !errors.Is(err, ErrD09TemplateInvalid) {
			t.Fatalf("unmeasured draft admitted: %v", err)
		}
		if _, err := DecodeD09CellTemplate([]byte(thesisD09GoldenDraftTemplateJSON)); !errors.Is(err, ErrD09TemplateInvalid) {
			t.Fatalf("unmeasured draft decoded: %v", err)
		}
		withProductMember := strings.TrimSuffix(thesisD09GoldenDraftTemplateJSON, "}") + `,"product_registration_digest":"sha256:product"}`
		if _, err := DecodeD09CellTemplate([]byte(withProductMember)); !errors.Is(err, ErrD09TemplateInvalid) || !strings.Contains(err.Error(), "product_registration_digest") {
			t.Fatalf("thesis decoder did not refuse the product member: %v", err)
		}
		// The measured fixture admits, so the refusal below is caused by the removed thesis managed-root digest alone.
		measured := measuredRouteProbeTemplate(t)
		if err := validateD09CellTemplate(measured); err != nil {
			t.Fatalf("measured fixture must admit before the managed-root digest is removed: %v", err)
		}
		measured.ManagedLiveRootRegistrationDigest = ""
		raw, err := jsonOf(measured)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeD09CellTemplate([]byte(raw)); !errors.Is(err, ErrD09TemplateInvalid) {
			t.Fatalf("template without the thesis managed-root digest decoded: %v", err)
		}
	})

	t.Run("protected loader refuses an input the root does not own", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: test-created files are root-owned, so the owner refusal cannot be observed")
		}
		path := filepath.Join(t.TempDir(), "d09-cell.json")
		if err := os.WriteFile(path, []byte(thesisD09GoldenDraftTemplateJSON), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRootOwnedProfileFile(path, maxD09TemplateBytes); err == nil {
			t.Fatal("protected loader read a template that is not root-owned")
		}
	})

	t.Run("host profile signing bytes and signature domain", func(t *testing.T) {
		profile := HostProfile{
			ID: "tbound-golden-host-profile", Digest: "sha256:stale-digest-cleared-before-signing", Mode: "production",
			ContainmentProfile: "d09-cell/v1", ContainmentProfileDigest: "containment-profile-digest-golden",
			CgroupRootDigest: "cgroup-root-digest-golden",
		}
		signingBytes, err := LinuxHostProfileSigningBytes(profile)
		if err != nil {
			t.Fatal(err)
		}
		golden := thesisD09GoldenHostProfileDomain + thesisD09GoldenHostProfileJSON
		if string(signingBytes) != golden {
			t.Fatalf("host profile signing bytes drifted:\n got %q\nwant %q", signingBytes, golden)
		}
		sum := sha256.Sum256([]byte(golden))
		if digest := DigestBytes(signingBytes); digest != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Fatalf("signed profile digest = %s, want the standard-library SHA-256 of the golden bytes", digest)
		}
		privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte("g"), ed25519.SeedSize))
		_, signature, publicKey, err := SignLinuxHostProfile(profile, privateKey)
		if err != nil {
			t.Fatal(err)
		}
		if !ed25519.Verify(publicKey, []byte(golden), signature) {
			t.Fatal("signature does not verify over the domain-prefixed golden bytes")
		}
		if ed25519.Verify(publicKey, []byte(thesisD09GoldenHostProfileJSON), signature) {
			t.Fatal("signature verifies without the domain prefix")
		}
	})

	t.Run("pinned Podman environment", func(t *testing.T) {
		want := []string{
			"CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf",
			"CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf",
			"HOME=/home/tbound-golden",
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
			"LOGNAME=tbound-golden",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"TMPDIR=/tmp",
			"USER=tbound-golden",
			"XDG_CACHE_HOME=/run/tbound-pi/cache",
			"XDG_CONFIG_HOME=/home/tbound-golden/.config",
			"XDG_DATA_HOME=/home/tbound-golden/.local/share",
			"XDG_RUNTIME_DIR=/run/user/4242",
		}
		slices.Sort(want)
		if got := thesisD09GoldenProbeEnv(t, thesisD09GoldenInheritedEnv()); !slices.Equal(got, want) {
			t.Fatalf("pinned Podman environment drifted:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("pinned Podman environment drops inherited overrides", func(t *testing.T) {
		inherited := thesisD09GoldenInheritedEnv()
		inherited["CONTAINERS_CONF"] = "/tmp/attacker/containers.conf"
		inherited["CONTAINERS_STORAGE_CONF"] = "/tmp/attacker/storage.conf"
		inherited["XDG_CACHE_HOME"] = "/tmp/attacker/cache"
		inherited["CONTAINERS_REGISTRIES_CONF"] = "/tmp/attacker/registries.conf"
		inherited["HTTPS_PROXY"] = "http://127.0.0.1:9"
		want := []string{
			"HOME=/home/tbound-golden",
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
			"LOGNAME=tbound-golden",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"TMPDIR=/tmp",
			"USER=tbound-golden",
			"XDG_CONFIG_HOME=/home/tbound-golden/.config",
			"XDG_DATA_HOME=/home/tbound-golden/.local/share",
			"XDG_RUNTIME_DIR=/run/user/4242",
		}
		slices.Sort(want)
		if got := thesisD09GoldenProbeEnv(t, inherited); !slices.Equal(got, want) {
			t.Fatalf("inherited overrides reached the Podman environment:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("pinned Podman environment falls back to the caller's runtime directory", func(t *testing.T) {
		inherited := thesisD09GoldenInheritedEnv()
		delete(inherited, "XDG_RUNTIME_DIR")
		fallback := fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Geteuid())
		if got := thesisD09GoldenProbeEnv(t, inherited); !slices.Contains(got, fallback) {
			t.Fatalf("XDG_RUNTIME_DIR fallback %q missing from %q", fallback, got)
		}
	})

	t.Run("command cell argv", func(t *testing.T) {
		// The config of TestD09CommandArgumentsCommandMode with literal digests and a cell uid/gid mapping of 1000.
		config := D09Config{
			UID: 1000, GID: 1000, CellUID: 1000, CellGID: 1000, PidsMax: 128, MemoryMB: 512, CPUs: "1",
			SeccompProfilePath: "/etc/tbound/seccomp.json", CgroupsPath: "/sys/fs/cgroup/tbound", CrunPath: "/usr/bin/crun",
			LandlockABI: 8, LandlockRulesetDigest: "sha256:landlock-ruleset-golden", LandlockROPath: "/usr", LandlockRWPath: "/work",
			CommandExecutable: "/bin/bash", CommandExecutableDigest: "sha256:command-executable-golden",
		}
		spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}}
		got := d09CommandCellArguments(config, "tbound-pi-cell-cmd-x", "/tmp/cid",
			"localhost/tbound/pi-cell@sha256:image-golden", "/proc/123/fd/4", "view-1", "lease-1", 1, 2, spec)
		want := []string{
			"run", "--name", "tbound-pi-cell-cmd-x", "--runtime", "/usr/bin/crun", "--cidfile", "/tmp/cid",
			"--pull=never", "--network=none", "--pid=private", "--ipc=private", "--uts=private", "--read-only", "--cap-drop=ALL",
			"--security-opt", "no-new-privileges", "--security-opt", "seccomp=/etc/tbound/seccomp.json",
			"--pids-limit", "128", "--memory", "512m", "--cpus", "1",
			"--uidmap", "1000:0:1", "--gidmap", "1000:0:1", "--user", "1000:1000", "--workdir", "/work",
			"--preserve-fds", "3", "--umask=0022", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777",
			"--cgroup-parent", "/tbound", "--mount", "type=bind,src=/proc/123/fd/4,dst=/work,rw,bind-propagation=rprivate",
			"-e", "HOME=/tmp/home", "-e", "TMPDIR=/tmp/tbound-tmp", "-e", "LANG=C.UTF-8", "-e", "PI_OFFLINE=1",
			"-e", "TBOUND_CELL_UID=1000", "-e", "TBOUND_CELL_GID=1000",
			"-e", "TBOUND_USERNS_MAP_PROFILE=", "-e", "TBOUND_USERNS_MAP_DIGEST=",
			"-e", "TBOUND_USERNS_HOST_UID=0", "-e", "TBOUND_USERNS_HOST_GID=0",
			"-e", "TBOUND_LANDLOCK_ABI=8", "-e", "TBOUND_LANDLOCK_RULESET_DIGEST=sha256:landlock-ruleset-golden",
			"-e", "TBOUND_LANDLOCK_RO_PATH=/usr", "-e", "TBOUND_LANDLOCK_RW_PATH=/work",
			"-e", "TBOUND_CELL_MODE=command", "-e", "TBOUND_CELL_COMMAND=/bin/bash",
			"-e", "TBOUND_COMMAND_DIGEST=sha256:command-executable-golden",
			"-e", "TBOUND_IMAGE_REF=localhost/tbound/pi-cell@sha256:image-golden",
			"-e", "TBOUND_IMAGE_DIGEST=", "-e", "TBOUND_TEMPLATE_DIGEST=",
			"-e", "TBOUND_CONTAINER_NAME=tbound-pi-cell-cmd-x",
			"-e", "TBOUND_COMMAND_LEASE_ID=lease-1", "-e", "TBOUND_COMMAND_VIEW_ID=view-1",
			"-e", "TBOUND_COMMAND_WORKDIR=",
			"-e", "TBOUND_COMMAND_VIEW_DEVICE=1", "-e", "TBOUND_COMMAND_VIEW_INODE=2",
			"-e", "TBOUND_HOST_NS_USER=0", "-e", "TBOUND_HOST_NS_MOUNT=0", "-e", "TBOUND_HOST_NS_PID=0",
			"-e", "TBOUND_HOST_NS_IPC=0", "-e", "TBOUND_HOST_NS_UTS=0", "-e", "TBOUND_HOST_NS_NETWORK=0",
			"-e", "TBOUND_CELL_PROOF_FD=4", "-e", "TBOUND_CELL_ACK_FD=5",
			"--entrypoint", "/usr/local/bin/tbound-pi-entrypoint",
			"localhost/tbound/pi-cell@sha256:image-golden", "/bin/bash", "-lc", "true",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("command cell argv drifted:\n got %q\nwant %q", got, want)
		}
	})
}
