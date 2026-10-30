//go:build linux

package piruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/sessionrepo"
)

// fakeD09Runner is the injected host probe surface used by in-package unit
// tests. It is a unit fixture only: the exported production constructor binds
// the real host exec runner, and StartVerified launches the real podman binary,
// so no external caller can substitute a fake host.
type fakeD09Runner struct {
	present map[string]string
	results map[string]D09CommandResult
	calls   [][]string
}

func (r *fakeD09Runner) LookPath(name string) (string, error) {
	if path, ok := r.present[name]; ok && path != "" {
		return path, nil
	}
	return "", errors.New(name + " not found")
}

func (r *fakeD09Runner) Run(_ context.Context, name string, args ...string) D09CommandResult {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if result, ok := r.results[strings.Join(call, " ")]; ok {
		return result
	}
	return D09CommandResult{ExitCode: 1}
}

const fakeD09Digest = "sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6"

func testD09CellID(serviceID, preferred int) int {
	if serviceID == preferred {
		return preferred + 1
	}
	return preferred
}

func bindTestD09UserNamespace(config *D09Config, cellUID, cellGID, serviceUID, serviceGID int) {
	config.UID, config.GID = cellUID, cellGID
	config.CellUID, config.CellGID = cellUID, cellGID
	config.UserNamespaceHostUID, config.UserNamespaceHostGID = serviceUID, serviceGID
	config.UserNamespaceMappingProfile = D09UserNamespaceMappingProfile
	config.UserNamespaceMappingDigest = DigestD09UserNamespaceMapping(cellUID, cellGID, serviceUID, serviceGID)
}

func bindTestD09TCBProofMaps(proof *D09TCBProof, config D09Config) {
	proof.UIDMap = D09InsideUIDMap(config.CellUID)
	proof.GIDMap = D09InsideGIDMap(config.CellGID)
	proof.UserNamespaceMapDigest = config.UserNamespaceMappingDigest
}

func completeD09Host(t *testing.T) (D09Config, *fakeD09Runner) {
	t.Helper()
	layout := t.TempDir()
	configBlob := []byte(`{"architecture":"amd64","os":"linux"}`)
	layerBlob := []byte("test-oci-layer")
	configDigest := DigestBytes(configBlob)
	layerDigest := DigestBytes(layerBlob)
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + configDigest + `"},"layers":[{"digest":"` + layerDigest + `"}]}`)
	digest := DigestBytes(manifest)
	if err := os.MkdirAll(filepath.Join(layout, "blobs", "sha256"), 0o700); err != nil {
		t.Fatal(err)
	}
	for blobDigest, blob := range map[string][]byte{digest: manifest, configDigest: configBlob, layerDigest: layerBlob} {
		if err := os.WriteFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(blobDigest, "sha256:")), blob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(layout, "index.json"), []byte(`{"manifests":[{"digest":"`+digest+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	seccomp := filepath.Join(t.TempDir(), "seccomp.json")
	if err := os.WriteFile(seccomp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cosignKey := filepath.Join(t.TempDir(), "cosign.pub")
	cosignKeyBytes := []byte("fake test public key")
	if err := os.WriteFile(cosignKey, cosignKeyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	landlockDigest, err := DigestD09LandlockRules([]string{"/usr"}, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	config := D09Config{
		Image:                             "localhost/tbound/pi-cell:latest",
		ExpectedImageDigest:               digest,
		CosignPublicKeyPath:               cosignKey,
		CosignPublicKeyDigest:             DigestBytes(cosignKeyBytes),
		OCILayoutPath:                     layout,
		SeccompProfilePath:                seccomp,
		SeccompProfileDigest:              DigestBytes([]byte("{}")),
		LandlockABI:                       D09LandlockABI,
		LandlockRulesetDigest:             landlockDigest,
		LandlockROPath:                    "/usr",
		LandlockRWPath:                    "/work",
		FrozenCapabilities:                d09EmptyCapabilitySet,
		EntrypointPath:                    d09EntrypointPath,
		EntrypointDigest:                  DigestBytes([]byte("entrypoint")),
		Runtime:                           "crun",
		CgroupsPath:                       "/sys/fs/cgroup/tbound",
		TemplateDigest:                    DigestBytes([]byte("test-template")),
		ManagedLiveRootRegistrationDigest: DigestBytes([]byte("managed-live-root-registration")),
		CPUs:                              "1",
		PidsMax:                           128,
		MemoryMB:                          512,
		UID:                               testD09CellID(os.Geteuid(), 731),
		GID:                               testD09CellID(os.Getegid(), 732),
		CellUID:                           testD09CellID(os.Geteuid(), 731),
		CellGID:                           testD09CellID(os.Getegid(), 732),
		NamespaceProfile:                  "user,mount,pid,ipc,uts,network-none/v1",
		CgroupVersion:                     "v2",
		CgroupControllers:                 "cpu,memory,pids",
		XattrFilesystemMagic:              0xef53,
		XattrMountOptionsDigest:           DigestBytes([]byte("ro,relatime")),
		XattrScanUID:                      os.Geteuid(),
		XattrScanGID:                      os.Getegid(),
		XattrScanPrivilegeProfile:         D09XattrScanPrivilegeProfile,
		XattrVisibilityAuthority:          D09XattrVisibilityAuthority,
		XattrVisibilityProfileDigest:      DigestD09XattrVisibilityProfile(0xef53, DigestBytes([]byte("ro,relatime")), os.Geteuid(), os.Getegid(), D09XattrScanPrivilegeProfile),
	}
	bindTestD09UserNamespace(&config, config.CellUID, config.CellGID, os.Geteuid(), os.Getegid())
	digestRef := "localhost/tbound/pi-cell@" + digest
	runner := &fakeD09Runner{
		present: map[string]string{"podman": "/usr/bin/podman", "crun": "/usr/bin/crun", "cosign": "/usr/bin/cosign"},
		results: map[string]D09CommandResult{
			"/usr/bin/podman --version":                                    {ExitCode: 0, Stdout: []byte("podman version 4.9.3\n")},
			"/usr/bin/podman info --format json":                           {ExitCode: 0, Stdout: []byte(`{"host":{"cgroupVersion":"v2","ociRuntime":{"name":"crun","version":"1.14.1"},"security":{"rootless":true}}}`)},
			"/usr/bin/podman image exists localhost/tbound/pi-cell:latest": {ExitCode: 0},
			"/usr/bin/podman image inspect localhost/tbound/pi-cell:latest": {
				ExitCode: 0, Stdout: []byte(`[{"RepoDigests":["` + digestRef + `"]}]`),
			},
			"cosign verify --offline --insecure-ignore-tlog=true --key " + cosignKey + " --local-image " + layout: {
				ExitCode: 0, Stdout: []byte(`[{"critical":{"image":{"docker-manifest-digest":"` + digest + `"}}}]`),
			},
		},
	}
	return config, runner
}

func newTestLauncher(config D09Config, runner D09ProbeRunner) *D09PodmanCrunLauncher {
	// A zero-value scope is sufficient for probe/admission unit tests; the real
	// composition root constructs it through NewLinuxCgroupScope.
	config.registeredTemplate = true // in-package fixture; production constructor always uses protected template provenance.
	return &D09PodmanCrunLauncher{runner: runner, config: config.normalized(), scope: &LinuxCgroupScope{}}
}

func validD09Profile() HostProfile {
	profile := measuredProfile()
	profile.ID = "pi-host-profile-v1"
	profile.Digest = DigestBytes([]byte("signed-test"))
	profile.ContainmentProfileDigest = DigestBytes([]byte("test-template"))
	return profile
}

func TestProductionD09ProbeRequiresRegisteredToolPathsNoPathFallback(t *testing.T) {
	config := DefaultD09Config("/etc/tbound/cosign.pub")
	config.ExpectedImageDigest = fakeD09Digest
	config.OCILayoutPath = "/etc/tbound/pi-cell-oci"
	config.TemplateDigest = DigestBytes([]byte("registered"))
	_, err := NewD09PodmanCrunLauncher(config).Probe(context.Background())
	var prerequisites *D09PrerequisiteError
	if !errors.As(err, &prerequisites) {
		t.Fatalf("production probe did not return exact pinned-tool refusal: %v", err)
	}
	seen := map[string]bool{}
	for _, item := range prerequisites.Missing {
		seen[item.Name] = true
	}
	for _, name := range []string{"podman", "crun", "cosign"} {
		if !seen[name] {
			t.Errorf("production probe lacks missing pinned tool %q: %v", name, err)
		}
	}
}

func TestD09ProbeReportsEveryMissingPrerequisite(t *testing.T) {
	launcher := newTestLauncher(DefaultD09Config("/keys/cosign.pub"), &fakeD09Runner{present: map[string]string{}})
	report, err := launcher.Probe(context.Background())
	if !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("empty host probe did not fail closed: %v", err)
	}
	var prereq *D09PrerequisiteError
	if !errors.As(err, &prereq) {
		t.Fatalf("probe error is not a D09PrerequisiteError: %v", err)
	}
	names := make(map[string]bool, len(prereq.Missing))
	for _, item := range prereq.Missing {
		names[item.Name] = true
	}
	for _, want := range []string{"podman", "crun", "cosign", "image"} {
		if !names[want] {
			t.Errorf("missing prerequisite list does not name %q: %v", want, err)
		}
	}
	if report.PodmanPresent || report.CrunPresent || report.CosignPresent || report.ImagePinned {
		t.Fatalf("probe reported a mechanism it did not observe: %+v", report)
	}
}

func TestLinuxCgroupScopeRemoveRequiresHeldVerifiedScope(t *testing.T) {
	if err := (*LinuxCgroupScope)(nil).Remove(context.Background()); !errors.Is(err, ErrCgroupScopeRefused) {
		t.Fatalf("Remove accepted a nil scope: %v", err)
	}
	scope := &LinuxCgroupScope{}
	if err := scope.Remove(context.Background()); !errors.Is(err, ErrCgroupScopeRefused) {
		t.Fatalf("Remove accepted a scope without held descriptors: %v", err)
	}
}

func TestD09ProbeAcceptsCompleteHost(t *testing.T) {
	config, runner := completeD09Host(t)
	launcher := newTestLauncher(config, runner)
	report, err := launcher.Probe(context.Background())
	if err != nil {
		t.Fatalf("complete host probe failed: %v", err)
	}
	if !report.Rootless || !report.CrunPresent || !report.CosignPresent {
		t.Fatalf("probe did not observe the runtime mechanisms: %+v", report)
	}
	if !report.ImagePinned || !report.ImageVerified || report.ImageRef == "" {
		t.Fatalf("probe did not verify the pinned image locally: %+v", report)
	}

	t.Run("missing signed mapping digest remains refused", func(t *testing.T) {
		bad := config
		bad.UserNamespaceMappingDigest = ""
		if _, err := newTestLauncher(bad, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
			t.Fatalf("missing mapping digest was accepted: %v", err)
		}
	})
	t.Run("service UID equal to cell UID remains refused", func(t *testing.T) {
		bad := config
		bad.UserNamespaceHostUID = bad.CellUID
		bad.UserNamespaceMappingDigest = DigestD09UserNamespaceMapping(bad.CellUID, bad.CellGID,
			bad.UserNamespaceHostUID, bad.UserNamespaceHostGID)
		if _, err := newTestLauncher(bad, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
			t.Fatalf("equal cell/service UID was accepted: %v", err)
		}
	})
	t.Run("service GID equal to cell GID remains refused", func(t *testing.T) {
		bad := config
		bad.UserNamespaceHostGID = bad.CellGID
		bad.UserNamespaceMappingDigest = DigestD09UserNamespaceMapping(bad.CellUID, bad.CellGID,
			bad.UserNamespaceHostUID, bad.UserNamespaceHostGID)
		if _, err := newTestLauncher(bad, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
			t.Fatalf("equal cell/service GID was accepted: %v", err)
		}
	})
}

// TestD09ProbeFailsClosedOnUnmeasuredRuntime proves an unparseable or failed
// podman info is a hard prerequisite failure, never a fail-open.
func TestD09ProbeFailsClosedOnUnmeasuredRuntime(t *testing.T) {
	for name, result := range map[string]D09CommandResult{
		"not json":       {ExitCode: 0, Stdout: []byte("rootless")},
		"nonzero exit":   {ExitCode: 125, Stderr: []byte("cannot connect")},
		"empty upstream": {ExitCode: 0, Stdout: []byte("")},
	} {
		config, runner := completeD09Host(t)
		runner.results["/usr/bin/podman info --format json"] = result
		launcher := newTestLauncher(config, runner)
		_, err := launcher.Probe(context.Background())
		if !errors.Is(err, ErrProductionLauncherUnavailable) || !strings.Contains(err.Error(), "podman-info") {
			t.Fatalf("%s: unmeasured podman info did not fail closed: %v", name, err)
		}
	}
}

func TestD09ProbeRejectsUnpinnedUnsignedOrDriftedPolicy(t *testing.T) {
	config, runner := completeD09Host(t)
	runner.results["/usr/bin/podman image inspect localhost/tbound/pi-cell:latest"] = D09CommandResult{
		ExitCode: 0, Stdout: []byte(`[{"RepoDigests":["localhost/tbound/pi-cell@sha256:` + strings.Repeat("c", 64) + `"]}]`),
	}
	if _, err := newTestLauncher(config, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("mismatched local digest did not fail closed: %v", err)
	}

	config, runner = completeD09Host(t)
	runner.results["cosign verify --offline --insecure-ignore-tlog=true --key "+config.CosignPublicKeyPath+" --local-image "+config.OCILayoutPath] = D09CommandResult{ExitCode: 1, Stderr: []byte("signature mismatch")}
	if _, err := newTestLauncher(config, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("failed local cosign verification did not fail closed: %v", err)
	}

	config, runner = completeD09Host(t)
	config.SeccompProfileDigest = DigestBytes([]byte("different"))
	if _, err := newTestLauncher(config, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("drifted static seccomp profile did not fail closed: %v", err)
	}

	config, runner = completeD09Host(t)
	config.LandlockABI = 0
	if _, err := newTestLauncher(config, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("unmeasured Landlock ABI did not fail closed: %v", err)
	}
}

func TestD09AdmissionRequiresARealBoundManagedLiveRoot(t *testing.T) {
	config, runner := completeD09Host(t)
	launcher := newTestLauncher(config, runner)
	profile := validD09Profile()
	if err := launcher.admitsFrozenLinuxProfile(profile); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("profile-only host probe admitted without the trusted managed live-root binding: %v", err)
	}
	templateProfile := DefaultLinuxHostProfileTemplate()
	templateProfile.Digest = DigestBytes([]byte("signed-test"))
	if err := launcher.admitsFrozenLinuxProfile(templateProfile); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("an unedited template profile was admitted: %v", err)
	}
	// A profile with a real id that still holds a placeholder identity (here the source provenance of the frozen
	// commit) is not admitted for any config, smoke or corpus.
	placeholder := validD09Profile()
	placeholder.SourceProvenanceDigest = placeholderDigest("source-provenance")
	if err := launcher.admitsFrozenLinuxProfile(placeholder); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("a profile with a placeholder source provenance was admitted: %v", err)
	}
	profile.ContainmentProfile = "host-exec-v1"
	if err := launcher.admitsFrozenLinuxProfile(profile); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("a non-D09 profile was admitted: %v", err)
	}
	unscoped := &D09PodmanCrunLauncher{runner: runner, config: config.normalized()}
	if err := unscoped.admitsFrozenLinuxProfile(validD09Profile()); !errors.Is(err, ErrCgroupScopeRefused) {
		t.Fatalf("a launcher without a verified cgroup scope was admitted: %v", err)
	}
}

func TestD09LauncherCloseOwnsOnlyItsRegistrationCloneAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	rootPath := t.TempDir()
	parentPath := filepath.Dir(rootPath)
	originalRoot, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer originalRoot.Close()
	originalParent, err := os.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer originalParent.Close()
	callerVerified := &VerifiedD09ManagedLiveRoot{root: originalRoot, parent: originalParent,
		registration: D09ManagedLiveRootRegistration{Digest: DigestBytes([]byte("root-policy"))}}
	if _, err := callerVerified.DuplicateCapability(context.Background()); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("public descriptor capability bypassed fixed-anchor policy revalidation: %v", err)
	}
	verifiedClone, err := callerVerified.duplicateCapabilityDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	launcher := &D09PodmanCrunLauncher{runner: &fakeD09Runner{}, managedRoot: verifiedClone,
		admittedProfile: validD09Profile(), profileAdmitted: true}
	if err := launcher.Close(ctx); err != nil {
		t.Fatalf("close quiescent owned launcher binding: %v", err)
	}
	if _, err := callerVerified.root.Stat(); err != nil {
		t.Fatalf("launcher closed the caller-owned managed-root handle: %v", err)
	}
	if _, err := callerVerified.parent.Stat(); err != nil {
		t.Fatalf("launcher closed the caller-owned verified parent descriptor: %v", err)
	}
	if _, err := verifiedClone.root.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("launcher did not close its independently owned root descriptor: %v", err)
	}
	if _, err := verifiedClone.parent.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("launcher did not close its independently owned parent descriptor: %v", err)
	}
	if _, bound := launcher.ManagedLiveRootRegistration(); bound {
		t.Fatal("launcher retained a managed-root registration after Close")
	}
	if launcher.profileAdmitted {
		t.Fatal("launcher retained a bound production profile after Close")
	}
	if err := launcher.Close(ctx); err != nil {
		t.Fatalf("Close was not idempotent: %v", err)
	}
	commandRunner := &D09CommandRunner{leaseID: "lease-1", launcher: launcher}
	if _, _, err := commandRunner.Run(ctx, nil, sessionrepo.CommandSpec{}); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("command invocation was accepted after launcher Close: %v", err)
	}
}

func TestD09LauncherCloseRefusesWhileAnySettlementIsOutstanding(t *testing.T) {
	ctx := context.Background()
	launcher := &D09PodmanCrunLauncher{}
	if err := launcher.beginOperation(); err != nil {
		t.Fatal(err)
	}
	if err := launcher.Close(ctx); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("Close did not retain an active launch/run operation: %v", err)
	}
	launcher.endOperation()
	cmd := &exec.Cmd{}
	launcher.proofs = map[*exec.Cmd]*d09LaunchRecord{cmd: {proof: D09TCBProof{LeaderPID: 101}}}
	if err := launcher.Close(ctx); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("Close discarded a per-launch settlement record: %v", err)
	}
	launcher.proofs = nil
	launcher.commandScopes = map[string]*d09OwnedCommandScope{"pending": {}}
	if err := launcher.Close(ctx); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("Close discarded a command cgroup scope: %v", err)
	}
	launcher.commandScopes = nil
	launcher.pendingCommandReceipts = 1
	if err := launcher.Close(ctx); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("Close discarded an unconsumed command proof receipt: %v", err)
	}
	launcher.pendingCommandReceipts = 0
	if err := launcher.Close(ctx); err != nil {
		t.Fatalf("Close after all settlement records were removed failed: %v", err)
	}
}

func TestD09LauncherManagedRootBindingRejectsWrongIdentityAndDoubleBind(t *testing.T) {
	digest := DigestBytes([]byte("registered-root"))
	other := DigestBytes([]byte("other-root"))
	launcher := &D09PodmanCrunLauncher{config: D09Config{ManagedLiveRootRegistrationDigest: digest}}
	wrong := &VerifiedD09ManagedLiveRoot{registration: D09ManagedLiveRootRegistration{Digest: other}}
	if err := launcher.BindManagedLiveRoot(wrong); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("managed-root registration from another identity was accepted: %v", err)
	}
	alreadyBound := &VerifiedD09ManagedLiveRoot{registration: D09ManagedLiveRootRegistration{Digest: digest}}
	launcher.managedRoot = alreadyBound
	duplicate := &VerifiedD09ManagedLiveRoot{registration: D09ManagedLiveRootRegistration{Digest: digest}}
	if err := launcher.BindManagedLiveRoot(duplicate); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Fatalf("managed-root descriptor was double-bound: %v", err)
	}
	if launcher.managedRoot != alreadyBound {
		t.Fatal("failed double-bind replaced the launcher's existing owned descriptor")
	}
}

func TestD09ContainerArgumentsComposeFrozenCell(t *testing.T) {
	config := D09Config{UID: 1000, GID: 1000, PidsMax: 128, MemoryMB: 512, CPUs: "1", SeccompProfilePath: "/etc/tbound/seccomp.json", CgroupsPath: "/sys/fs/cgroup/tbound", EntrypointPath: "/usr/local/bin/tbound-pi-entrypoint", CrunPath: "/usr/bin/crun", LandlockABI: 1, LandlockRulesetDigest: DigestBytes([]byte("landlock")), LandlockROPath: "/usr", LandlockRWPath: "/work"}
	bindTestD09UserNamespace(&config, 1000, 1000, 1701, 1702)
	args := d09ContainerArgumentsAtWithEnv(config, "tbound-pi-cell-deadbeef",
		"localhost/tbound/pi-cell@"+fakeD09Digest, PiWorkerCellEnvironment(), nil, PiWorkerCellArguments(), PiWorkerCellWorkdir, nil, "")
	workerArguments := PiWorkerCellArguments()
	if len(args) < len(workerArguments) || !equalStrings(args[len(args)-len(workerArguments):], workerArguments) {
		t.Fatalf("D09 container argv does not end with the exact frozen Pi CLI argv: %q", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run --name tbound-pi-cell-deadbeef",
		"--rm",
		"--pull=never",
		"--network=none",
		"--read-only",
		"--cap-drop=ALL",
		"no-new-privileges",
		"seccomp=/etc/tbound/seccomp.json",
		"--pids-limit 128",
		"--memory 512m",
		"--cpus 1",
		"--runtime /usr/bin/crun",
		"--cgroup-parent /tbound",
		"--preserve-fds 8",
		"--uidmap 1000:0:1",
		"--gidmap 1000:0:1",
		"--user 1000:1000",
		"--workdir /work",
		"-e HOME=" + PiWorkerCellHome,
		"-e LANG=C.UTF-8",
		"-e PI_OFFLINE=1",
		"-e TMPDIR=" + PiWorkerCellTemp,
		"-e TBOUND_CELL_UID=1000",
		"-e TBOUND_LANDLOCK_ABI=1",
		"localhost/tbound/pi-cell@" + fakeD09Digest,
		"/usr/local/bin/tbound-pi-entrypoint",
		"/proc/self/fd/3",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("container argument vector is missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--userns") {
		t.Errorf("container arguments must not use a userns profile override: %s", joined)
	}
	if strings.Contains(joined, "--mount") {
		t.Errorf("container argument vector must not bind-mount a host path: %s", joined)
	}
	if !strings.Contains(joined, "--security-opt seccomp=/etc/tbound/seccomp.json") {
		t.Errorf("seccomp profile is not passed as a security-opt: %s", joined)
	}
}

func TestD09StartVerifiedRefusesWithoutScopeOrCapability(t *testing.T) {
	config, runner := completeD09Host(t)
	unscoped := &D09PodmanCrunLauncher{runner: runner, config: config.normalized()}
	if _, err := unscoped.StartVerified(context.Background(), ExecutableLaunchRequest{}); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("StartVerified without a scope did not fail closed: %v", err)
	}
	scoped := newTestLauncher(config, runner)
	if _, err := scoped.StartVerified(context.Background(), ExecutableLaunchRequest{}); !errors.Is(err, ErrChildCapabilityMissing) {
		t.Fatalf("StartVerified without an admission capability did not fail closed: %v", err)
	}
}

func TestD09ProbeBindsCosignPayloadToFrozenImage(t *testing.T) {
	// The verification key path itself is content-pinned, not merely a trusted
	// filename. Replacing its bytes must refuse before accepting a signature.
	config, runner := completeD09Host(t)
	if err := os.WriteFile(config.CosignPublicKeyPath, []byte("changed test key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestLauncher(config, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("changed cosign public key bytes were accepted: %v", err)
	}

	// Valid signature over a DIFFERENT image digest must refuse.
	config, runner = completeD09Host(t)
	key := "cosign verify --offline --insecure-ignore-tlog=true --key " + config.CosignPublicKeyPath + " --local-image " + config.OCILayoutPath
	runner.results[key] = D09CommandResult{ExitCode: 0, Stdout: []byte(`[{"critical":{"image":{"docker-manifest-digest":"sha256:` + strings.Repeat("9", 64) + `"}}}]`)}
	if _, err := newTestLauncher(config, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("signature over a different image was accepted: %v", err)
	}

	// Valid exit but no identity claim must refuse.
	config, runner = completeD09Host(t)
	key = "cosign verify --offline --insecure-ignore-tlog=true --key " + config.CosignPublicKeyPath + " --local-image " + config.OCILayoutPath
	runner.results[key] = D09CommandResult{ExitCode: 0, Stdout: []byte(`[{"critical":{}}]`)}
	if _, err := newTestLauncher(config, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("identity-less cosign payload was accepted: %v", err)
	}

	// Tampered local OCI blob must refuse even with a matching claim.
	config, runner = completeD09Host(t)
	blobPath := filepath.Join(config.OCILayoutPath, "blobs", "sha256", strings.TrimPrefix(config.ExpectedImageDigest, "sha256:"))
	if err := os.WriteFile(blobPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestLauncher(config, runner).Probe(context.Background()); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("tampered OCI blob was accepted: %v", err)
	}
}

func TestD09TCBProofValidation(t *testing.T) {
	imageDigest := DigestBytes([]byte("image"))
	templateDigest := DigestBytes([]byte("template"))
	config := D09Config{UID: 1000, GID: 1000, LandlockABI: 8, LandlockRulesetDigest: DigestBytes([]byte("landlock")), ExpectedImageDigest: imageDigest, TemplateDigest: templateDigest}
	bindTestD09UserNamespace(&config, 1000, 1000, 1701, 1702)
	good := D09TCBProof{
		Schema: "tbound-pi-entrypoint/v1", UID: 1000, GID: 1000, CapEff: d09EmptyCapabilitySet,
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8,
		LandlockRulesetDigest: config.LandlockRulesetDigest, LandlockTSYNC: true, CgroupV2: true,
		ContainerName: "tbound-pi-cell-x", LeaderPID: 4242, ContainerLeaderPID: 1,
		LauncherProcessPID: 99, ImageRef: "localhost/tbound/pi-cell@" + imageDigest,
		ImageDigest: imageDigest, TemplateDigest: templateDigest,
		ConsumerMountsVerified: true, RawSourceFDsClosed: true,
		WorkerMountID: 1, RuntimeMountID: 2,
		WorkerDevice: 1, WorkerInode: 2, RuntimeDevice: 1, RuntimeInode: 3,
	}
	bindTestD09TCBProofMaps(&good, config)
	if err := validateD09TCBProof(good, config); err != nil {
		t.Fatalf("valid TCB proof rejected: %v", err)
	}
	for name, mutate := range map[string]func(*D09TCBProof){
		"wrong abi":          func(p *D09TCBProof) { p.LandlockABI = 7 },
		"wrong digest":       func(p *D09TCBProof) { p.LandlockRulesetDigest = DigestBytes([]byte("x")) },
		"no tsync":           func(p *D09TCBProof) { p.LandlockTSYNC = false },
		"caps":               func(p *D09TCBProof) { p.CapEff = "ffffffffffffffff" },
		"no nnp":             func(p *D09TCBProof) { p.NoNewPrivs = false },
		"no cgroup":          func(p *D09TCBProof) { p.CgroupV2 = false },
		"wrong image":        func(p *D09TCBProof) { p.ImageDigest = DigestBytes([]byte("other")) },
		"wrong tmpl":         func(p *D09TCBProof) { p.TemplateDigest = DigestBytes([]byte("other")) },
		"no container":       func(p *D09TCBProof) { p.ContainerName = "" },
		"no leader pid":      func(p *D09TCBProof) { p.LeaderPID = 0 },
		"no mounts":          func(p *D09TCBProof) { p.ConsumerMountsVerified = false },
		"raw source":         func(p *D09TCBProof) { p.RawSourceFDsClosed = false },
		"no worker mount id": func(p *D09TCBProof) { p.WorkerMountID = 0 },
		"missing uid map":    func(p *D09TCBProof) { p.UIDMap = "" },
		"missing gid map":    func(p *D09TCBProof) { p.GIDMap = "" },
		"substituted map digest": func(p *D09TCBProof) {
			p.UserNamespaceMapDigest = DigestD09UserNamespaceMapping(1000, 1000, 1703, 1704)
		},
		"extra uid map range": func(p *D09TCBProof) { p.UIDMap += "1001 0 1\n" },
		"extra gid map range": func(p *D09TCBProof) { p.GIDMap += "1001 0 1\n" },
	} {
		bad := good
		mutate(&bad)
		if err := validateD09TCBProof(bad, config); !errors.Is(err, ErrProductionLauncherUnavailable) {
			t.Errorf("%s: invalid TCB proof was accepted: %v", name, err)
		}
	}
	if D09ProofChildFD != 10 || D09ExecChildFD != 3 {
		t.Fatalf("proof/exec child fds drifted: proof=%d exec=%d", D09ProofChildFD, D09ExecChildFD)
	}
}

func TestD09CommandSpecValidation(t *testing.T) {
	config := D09Config{CommandExecutable: "/bin/bash", CommandExecutableDigest: fakeD09Digest}
	if err := validateD09CommandSpec(sessionrepo.CommandSpec{Executable: "relative"}, config); err == nil {
		t.Fatal("relative command executable was accepted")
	}
	if err := validateD09CommandSpec(sessionrepo.CommandSpec{Executable: config.CommandExecutable, WorkingDirectory: "../x"}, config); err == nil {
		t.Fatal("escaping working directory was accepted")
	}
	if err := validateD09CommandSpec(sessionrepo.CommandSpec{Executable: config.CommandExecutable, Args: []string{"-lc", "true"}}, config); err != nil {
		t.Fatalf("valid command spec rejected: %v", err)
	}
}

func TestD09CommandArgumentsCommandMode(t *testing.T) {
	config := D09Config{UID: 1000, GID: 1000, PidsMax: 128, MemoryMB: 512, CPUs: "1", SeccompProfilePath: "/etc/tbound/seccomp.json", CgroupsPath: "/sys/fs/cgroup/tbound", CrunPath: "/usr/bin/crun", LandlockABI: 8, LandlockRulesetDigest: DigestBytes([]byte("ll")), LandlockROPath: "/usr", LandlockRWPath: "/work", CommandExecutable: "/bin/bash", CommandExecutableDigest: fakeD09Digest}
	args := d09CommandCellArguments(config, "tbound-pi-cell-cmd-x", "/tmp/cid", "localhost/tbound/pi-cell@sha256:"+strings.Repeat("a", 64), "/proc/123/fd/4", "view-1", "lease-1", 1, 2, sessionrepo.CommandSpec{Executable: config.CommandExecutable, Args: []string{"-lc", "true"}})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--runtime /usr/bin/crun", "--cidfile /tmp/cid", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--preserve-fds 3", "-e TBOUND_CELL_MODE=command", "-e TBOUND_CELL_COMMAND=/bin/bash", "-e TBOUND_CELL_PROOF_FD=4", "-e TBOUND_CELL_ACK_FD=5", "-e TBOUND_COMMAND_VIEW_ID=view-1", "-e TBOUND_COMMAND_LEASE_ID=lease-1", "TBOUND_COMMAND_VIEW_DEVICE=1", "type=bind,src=/proc/123/fd/4,dst=/work,rw", "localhost/tbound/pi-cell@sha256:", "/bin/bash -lc true"} {
		if !strings.Contains(joined, want) {
			t.Errorf("command vector is missing %q:\n%s", want, joined)
		}
	}
}

func TestD09CommandProofBindsLeaseViewContainerAndMount(t *testing.T) {
	imageDigest := DigestBytes([]byte("image"))
	templateDigest := DigestBytes([]byte("template"))
	commandDigest := DigestBytes([]byte("bash"))
	config := D09Config{UID: 1000, GID: 1000, LandlockABI: 8, CommandExecutable: "/bin/bash", CommandExecutableDigest: commandDigest,
		LandlockRulesetDigest: DigestBytes([]byte("landlock")), ExpectedImageDigest: imageDigest, TemplateDigest: templateDigest,
		HostNamespaces: D09NamespaceInodes{User: 100, Mount: 101, PID: 102, IPC: 103, UTS: 104, Network: 105}}
	bindTestD09UserNamespace(&config, 1000, 1000, 1701, 1702)
	proof := d09CommandProof{
		Schema: "tbound-pi-entrypoint/v1", UID: 1000, GID: 1000, CapEff: d09EmptyCapabilitySet,
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: config.LandlockRulesetDigest,
		LandlockTSYNC: true, CgroupV2: true, LeaderPID: 10, ContainerLeaderPID: 1, LeaseID: "lease-1", ViewID: "view-1",
		CommandStarted: false, CommandReady: true, ContainerName: "cell-1", ImageRef: "localhost/tbound/pi-cell@" + imageDigest,
		ImageDigest: imageDigest, TemplateDigest: templateDigest, ConsumerMountsVerified: true,
		CommandPath: "/bin/bash", CommandDigest: commandDigest, CommandWorkdir: ".",
		RawSourceFDsClosed: true, WorkerMountID: 10, RuntimeMountID: 10,
		WorkerDevice: 1, WorkerInode: 2, RuntimeDevice: 1, RuntimeInode: 2,
		UserNSInode: 200, MountNSInode: 201, PIDNSInode: 202, IPCNSInode: 203, UTSNSInode: 204, NetworkNSInode: 205,
		NoExternalRoutes: true,
	}
	proof.UIDMap = D09InsideUIDMap(config.CellUID)
	proof.GIDMap = D09InsideGIDMap(config.CellGID)
	proof.UserNamespaceMapDigest = config.UserNamespaceMappingDigest
	spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}}
	if err := validateD09CommandProof(proof, config, proof.ImageRef, "lease-1", "view-1", "cell-1", 1, 2, spec); err != nil {
		t.Fatalf("valid D09 command proof rejected: %v", err)
	}
	for name, mutate := range map[string]func(*d09CommandProof){
		"missing UID map": func(p *d09CommandProof) { p.UIDMap = "" },
		"missing GID map": func(p *d09CommandProof) { p.GIDMap = "" },
		"substituted map digest": func(p *d09CommandProof) {
			p.UserNamespaceMapDigest = DigestD09UserNamespaceMapping(1000, 1000, 1703, 1704)
		},
		"extra UID range": func(p *d09CommandProof) { p.UIDMap += "1001 0 1\n" },
		"extra GID range": func(p *d09CommandProof) { p.GIDMap += "1001 0 1\n" },
	} {
		bad := proof
		mutate(&bad)
		if err := validateD09CommandProof(bad, config, proof.ImageRef, "lease-1", "view-1", "cell-1", 1, 2, spec); !errors.Is(err, ErrProductionLauncherUnavailable) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}
	if err := validateD09CommandProof(proof, config, proof.ImageRef, "other-lease", "view-1", "cell-1", 1, 2, spec); err == nil {
		t.Fatal("foreign lease command proof was accepted")
	}
	if err := validateD09CommandProof(proof, config, proof.ImageRef, "lease-1", "other-view", "cell-1", 1, 2, spec); err == nil {
		t.Fatal("foreign view command proof was accepted")
	}
	if err := validateD09CommandProof(proof, config, proof.ImageRef, "lease-1", "view-1", "other-cell", 1, 2, spec); err == nil {
		t.Fatal("foreign container command proof was accepted")
	}
	if err := validateD09CommandProof(proof, config, proof.ImageRef, "lease-1", "view-1", "cell-1", 9, 2, spec); err == nil {
		t.Fatal("foreign view inode command proof was accepted")
	}
}

func TestD09CommandReceiptRequiresIssuedSealAndCompleteObservedSettlement(t *testing.T) {
	imageDigest := DigestBytes([]byte("image"))
	templateDigest := DigestBytes([]byte("template"))
	commandDigest := DigestBytes([]byte("bash"))
	proof := D09CommandProof{
		Schema: "tbound-pi-entrypoint/v1", UID: 1000, GID: 1000, CapEff: d09EmptyCapabilitySet,
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: DigestBytes([]byte("landlock")),
		LandlockTSYNC: true, CgroupV2: true, LeaderPID: 321, ContainerLeaderPID: 1,
		LeaseID: "lease-1", ViewID: "view-1", CommandStarted: true, CommandReady: true,
		CommandExitObserved: true, CommandExitCode: 7, CommandPath: "/bin/bash", CommandDigest: commandDigest,
		CommandWorkdir: ".", ContainerName: "cell-1", ImageRef: "localhost/tbound/pi-cell@" + imageDigest,
		ImageDigest: imageDigest, TemplateDigest: templateDigest,
	}
	result := sessionrepo.CommandResult{ExitCode: 7, ExitObserved: true, Stderr: []byte("expected non-zero")}
	settlement := sessionrepo.CommandSettlement{
		LeaseID: "lease-1", ViewID: "view-1", ProcessScopeEmpty: true, WritersStopped: true,
		MountDetached: true, ExitObserved: true, ExitCode: 7,
		EvidenceClass: D09CommandRunnerProfile + ":inspect-exit+podman-wait+proof-pipe-eof+output-drained+container-removed+leader-pidfd+cgroup-populated0+mountns-released+cgroup-removed",
	}
	evidence := D09CommandSettlementEvidence{
		ContainerExitInspected: true, FinalCommandProofRead: true, PodmanWaitCompleted: true,
		OutputWritersDrained: true, ContainerRemoved: true, LeaderPidfdExited: true,
		CgroupPopulatedZero: true, MountNamespaceReleased: true, CommandOutputWithinBound: true,
		CommandCgroupRemoved: true,
	}
	receipt, err := newD09CommandReceipt("lease-1", "view-1", "cell-1", strings.Repeat("a", 64),
		sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "false"}}, result, settlement, proof, evidence)
	if err != nil {
		t.Fatalf("issue runtime command receipt: %v", err)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("issued receipt did not validate: %v", err)
	}
	if digest, err := receipt.Digest(); err != nil || digest == "" {
		t.Fatalf("issued receipt digest missing: digest=%q err=%v", digest, err)
	}
	spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "false"}}
	runner := &D09CommandRunner{leaseID: "lease-1", receipts: map[string]D09CommandReceipt{"view-1": receipt}}
	taken, err := runner.takeCommandReceipt("view-1", spec, result, settlement)
	if err != nil || taken.Validate() != nil {
		t.Fatalf("exact command invocation receipt was not consumable: receipt=%+v err=%v", taken, err)
	}
	if _, err := runner.takeCommandReceipt("view-1", spec, result, settlement); !errors.Is(err, ErrD09CommandReceiptInvalid) {
		t.Fatalf("runtime receipt was reusable: %v", err)
	}
	staleRunner := &D09CommandRunner{leaseID: "lease-1", receipts: map[string]D09CommandReceipt{"view-1": receipt}}
	if _, err := staleRunner.takeCommandReceipt("view-1", sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}}, result, settlement); !errors.Is(err, ErrD09CommandReceiptInvalid) {
		t.Fatalf("receipt was accepted for a different command spec: %v", err)
	}
	if _, err := staleRunner.takeCommandReceipt("view-1", spec, result, settlement); !errors.Is(err, ErrD09CommandReceiptInvalid) {
		t.Fatalf("mismatched receipt attempt did not consume stale entry: %v", err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot D09CommandReceipt
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateSnapshot(); err != nil {
		t.Fatalf("durable receipt snapshot did not validate structurally: %v", err)
	}
	if err := snapshot.Validate(); !errors.Is(err, ErrD09CommandReceiptInvalid) {
		t.Fatalf("caller/journal-decoded snapshot incorrectly retained runtime issuance authority: %v", err)
	}

	tampered := receipt
	tampered.Proof.CommandExitCode = 0
	if err := tampered.Validate(); !errors.Is(err, ErrD09CommandReceiptInvalid) {
		t.Fatalf("mutated captured proof was accepted: %v", err)
	}
	missingObservation := evidence
	missingObservation.OutputWritersDrained = false
	if _, err := newD09CommandReceipt("lease-1", "view-1", "cell-1", strings.Repeat("a", 64),
		sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "false"}}, result, settlement, proof, missingObservation); !errors.Is(err, ErrD09CommandReceiptInvalid) {
		t.Fatalf("receipt without observed writer drain was minted: %v", err)
	}
}

func TestD09CommandViewIsOneUseAtomicAndUnknownCannotReplay(t *testing.T) {
	runner := &D09CommandRunner{usedViews: make(map[string]struct{})}
	if err := runner.reserveCommandView("view-first"); err != nil {
		t.Fatalf("first exact authorized view was refused: %v", err)
	}
	if err := runner.reserveCommandView("view-first"); !errors.Is(err, ErrD09CommandReceiptInvalid) {
		t.Fatalf("second run for the exact same view was not refused: %v", err)
	}

	// A failed/UNKNOWN run retains its one-use reservation even though no
	// receipt exists; the command cannot be relaunched through the same view.
	unknown := &D09CommandRunner{usedViews: make(map[string]struct{})}
	if err := unknown.reserveCommandView("view-unknown"); err != nil {
		t.Fatal(err)
	}
	if len(unknown.receipts) != 0 {
		t.Fatal("test precondition: unknown run unexpectedly has a result receipt")
	}
	if err := unknown.reserveCommandView("view-unknown"); !errors.Is(err, ErrD09CommandReceiptInvalid) {
		t.Fatalf("UNKNOWN view could be replayed: %v", err)
	}

	concurrent := &D09CommandRunner{usedViews: make(map[string]struct{})}
	const contenders = 32
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- concurrent.reserveCommandView("view-concurrent")
		}()
	}
	wait.Wait()
	close(results)
	successes, refusals := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrD09CommandReceiptInvalid) {
			refusals++
		} else {
			t.Fatalf("unexpected concurrent reservation error: %v", err)
		}
	}
	if successes != 1 || refusals != contenders-1 {
		t.Fatalf("concurrent exact-view admission was not atomic: success=%d refuse=%d", successes, refusals)
	}
}

func TestD09NamespaceProofRequiresDistinctNamespacesAndNoRoutes(t *testing.T) {
	host := D09NamespaceInodes{User: 10, Mount: 11, PID: 12, IPC: 13, UTS: 14, Network: 15}
	proof := D09TCBProof{UserNSInode: 20, MountNSInode: 21, PIDNSInode: 22, IPCNSInode: 23, UTSNSInode: 24, NetworkNSInode: 25, NoExternalRoutes: true}
	if err := validateD09NamespaceProof(proof, host); err != nil {
		t.Fatalf("distinct namespace proof rejected: %v", err)
	}
	proof.PIDNSInode = host.PID
	if err := validateD09NamespaceProof(proof, host); err == nil {
		t.Fatal("host PID namespace reuse was accepted")
	}
	proof.PIDNSInode = 22
	proof.NoExternalRoutes = false
	if err := validateD09NamespaceProof(proof, host); err == nil {
		t.Fatal("unverified external route state was accepted")
	}
}

func TestD09CommandRunnerFailsClosed(t *testing.T) {
	if _, err := NewD09CommandRunner("", &D09PodmanCrunLauncher{}); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("runner without an authenticated lease was built: %v", err)
	}
	runner := &D09CommandRunner{leaseID: "lease-1", launcher: &D09PodmanCrunLauncher{config: D09Config{CommandExecutable: "/bin/bash"}}}
	if _, _, err := runner.Run(context.Background(), nil, sessionrepo.CommandSpec{Executable: "/bin/sh"}); err == nil {
		t.Fatal("nil command view was accepted")
	}
	if _, _, err := runner.Run(context.Background(), new(sessionrepo.CommandView), sessionrepo.CommandSpec{Executable: "/bin/bash"}); err == nil {
		t.Fatal("unregistered command view was accepted")
	}
}

func TestD09TakeLaunchProofIsPerCommand(t *testing.T) {
	launcher := &D09PodmanCrunLauncher{config: D09Config{}.normalized()}
	cmdA := exec.Command("/bin/true")
	cmdB := exec.Command("/bin/true")
	proofRead, proofWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = proofWrite.Close()
	defer proofRead.Close()
	proof := D09TCBProof{
		Schema: "tbound-pi-entrypoint/v1", ContainerName: "cell-a", LeaderPID: 1, LauncherProcessPID: 2,
		ImageDigest: DigestBytes([]byte("image")), TemplateDigest: DigestBytes([]byte("template")),
	}
	stdoutA := io.NopCloser(strings.NewReader(`{"id":"request-a","type":"response","command":"prompt","success":true}` + "\n"))
	launcher.storeLaunchProof(cmdA, proof, D09NamespaceInodes{}, proofRead, stdoutA)
	if _, ok := launcher.TakeLaunchProof(cmdB); ok {
		t.Fatal("a foreign command was returned a proof")
	}
	if got := launcher.processStdoutFor(cmdB); got != nil {
		t.Fatal("a foreign command was returned a stdout reader")
	}
	if got := launcher.processStdoutFor(cmdA); got != nil {
		t.Fatal("stdout was exposed before the exact launch proof was taken")
	}
	got, ok := launcher.TakeLaunchProof(cmdA)
	if !ok {
		t.Fatal("the stored proof was not returned for its own command")
	}
	if got := launcher.processStdoutFor(cmdA); got != stdoutA {
		t.Fatal("the exact command did not receive its stored stdout reader")
	}
	typed, ok := got.(D09TCBProof)
	if !ok || typed.ContainerName != "cell-a" || typed.LeaderPID != 1 {
		t.Fatalf("wrong proof returned: %#v", got)
	}
	if _, ok := launcher.TakeLaunchProof(cmdA); ok {
		t.Fatal("the same proof was returned twice")
	}
	settler, ok := launcher.DescendantSettlerFor(cmdA)
	if !ok || settler == nil {
		t.Fatal("the exact command did not get its per-launch settler")
	}
	if _, ok := launcher.DescendantSettlerFor(cmdB); ok {
		t.Fatal("a foreign command was returned a per-launch settler")
	}
	if _, ok := launcher.TakeLaunchProof(nil); ok {
		t.Fatal("a nil command was returned a proof")
	}
}

func TestD09UnknownFailedLaunchRetainsPendingStdoutOwner(t *testing.T) {
	command := exec.Command("sleep", "30")
	stdout, err := attachOwnedStdout(command)
	if err != nil {
		t.Fatal(err)
	}
	if err := stdout.Start(); err != nil {
		t.Fatalf("start child for pending stdout ownership: %v", err)
	}
	launcher := &D09PodmanCrunLauncher{
		config: D09Config{}.normalized(),
		proofs: make(map[*exec.Cmd]*d09LaunchRecord),
	}
	launcher.storePendingLaunch(command, stdout.reader)

	err = launcher.cleanupFailedContainer(command, "", "")
	if !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("cleanup without cgroup settlement = %v, want UNKNOWN", err)
	}
	launcher.proofMu.Lock()
	record := launcher.proofs[command]
	launcher.proofMu.Unlock()
	if record == nil || record.stdout != stdout.reader {
		t.Fatal("UNKNOWN cleanup did not retain the pending stdout owner")
	}
	if err := stdout.CloseReader(); err != nil {
		t.Fatalf("close retained test stdout owner: %v", err)
	}
}

func TestD09FailedCommandWaitRequiresExitAndCachesResult(t *testing.T) {
	waitFailure := errors.New("wait did not observe process exit")
	tests := []struct {
		name        string
		waitErr     error
		wantUnknown bool
	}{
		{name: "nil wait result"},
		{name: "observed nonzero exit", waitErr: &exec.ExitError{}},
		{name: "non-exit wait error", waitErr: waitFailure, wantUnknown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("/bin/true")
			stdout := io.NopCloser(strings.NewReader("pending launch stdout"))
			launcher := &D09PodmanCrunLauncher{
				proofs: make(map[*exec.Cmd]*d09LaunchRecord),
			}
			launcher.storePendingLaunch(command, stdout)

			launcher.proofMu.Lock()
			record := launcher.proofs[command]
			record.waitDone = make(chan struct{})
			record.waitErr = test.waitErr
			close(record.waitDone)
			launcher.proofMu.Unlock()

			for attempt := 0; attempt < 2; attempt++ {
				err := launcher.waitFailedCommand(context.Background(), command, record)
				if test.wantUnknown {
					if !errors.Is(err, ErrUnknownSettlement) || !errors.Is(err, waitFailure) {
						t.Fatalf("cached non-exit wait result = %v, want UNKNOWN and wait error", err)
					}
				} else if err != nil {
					t.Fatalf("cached observed-exit result = %v, want nil", err)
				}
			}
			if launcher.proofs[command] != record || record.stdout != stdout {
				t.Fatal("wait result discarded the pending launch stdout owner")
			}
		})
	}
}

func TestD09FailedLaunchCloseErrorRetainsOwnedRecord(t *testing.T) {
	pidfd, peer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := pidfd.Close(); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("/bin/true")
	stdout := &retryCloseReader{reader: strings.NewReader("")}
	record := &d09LaunchRecord{pidfd: pidfd, stdout: stdout}
	launcher := &D09PodmanCrunLauncher{
		proofs:       map[*exec.Cmd]*d09LaunchRecord{command: record},
		launchInProg: true,
	}

	for attempt := 0; attempt < 2; attempt++ {
		err := launcher.finishFailedLaunchRecord(command, record)
		if !errors.Is(err, ErrUnknownSettlement) || !errors.Is(err, os.ErrClosed) {
			t.Fatalf("failed launch close result = %v, want UNKNOWN and closed-file error", err)
		}
		launcher.proofMu.Lock()
		retained := launcher.proofs[command] == record && launcher.launchInProg
		launcher.proofMu.Unlock()
		if !retained || record.pidfd != pidfd || record.stdout != stdout {
			t.Fatal("close failure discarded the launch record or an owned resource reference")
		}
	}
	if got := stdout.calls.Load(); got != 1 {
		t.Fatalf("successful stdout close calls across pidfd retries = %d, want 1", got)
	}
}

func TestD09LaunchProofMustMatchTheExactSupervisorCommand(t *testing.T) {
	imageDigest := DigestBytes([]byte("image"))
	templateDigest := DigestBytes([]byte("template"))
	config := D09Config{UID: 1, GID: 1, ExpectedImageDigest: imageDigest, TemplateDigest: templateDigest,
		LandlockABI: 8, LandlockRulesetDigest: DigestBytes([]byte("landlock")),
		HostNamespaces: D09NamespaceInodes{User: 100, Mount: 101, PID: 102, IPC: 103, UTS: 104, Network: 105}}
	bindTestD09UserNamespace(&config, 1, 1, 1701, 1702)
	profile := HostProfile{Mode: ModeProduction, ContainmentProfileDigest: templateDigest}
	launcher := &D09PodmanCrunLauncher{config: config}
	cmd := exec.Command("/bin/true")
	cmd.Process = &os.Process{Pid: 4242}
	proof := D09TCBProof{
		Schema: "tbound-pi-entrypoint/v1", UID: 1, GID: 1, CapEff: d09EmptyCapabilitySet,
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: config.LandlockRulesetDigest,
		LandlockTSYNC: true, CgroupV2: true, ContainerName: "cell", LeaderPID: 9,
		ContainerLeaderPID: 1, LauncherProcessPID: 4242, ImageRef: "image@" + imageDigest,
		ImageDigest: imageDigest, TemplateDigest: templateDigest, ConsumerMountsVerified: true,
		RawSourceFDsClosed: true, WorkerMountID: 1, RuntimeMountID: 2,
		WorkerDevice: 1, WorkerInode: 2, RuntimeDevice: 1, RuntimeInode: 3,
		UserNSInode: 200, MountNSInode: 201, PIDNSInode: 202, IPCNSInode: 203, UTSNSInode: 204, NetworkNSInode: 205,
		NoExternalRoutes: true,
	}
	bindTestD09TCBProofMaps(&proof, config)
	if err := launcher.ValidateLaunchProof(proof, profile, cmd); err != nil {
		t.Fatalf("matching per-command proof rejected: %v", err)
	}
	foreign := exec.Command("/bin/true")
	foreign.Process = &os.Process{Pid: 4243}
	if err := launcher.ValidateLaunchProof(proof, profile, foreign); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("proof for another command was accepted: %v", err)
	}
	if err := launcher.ValidateLaunchProof(nil, profile, cmd); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("absent proof was accepted: %v", err)
	}
}

func TestProcessLaunchProofDefaultsNil(t *testing.T) {
	process := newProcess(exec.Command("/bin/true"), NoDescendants{}, nil, false)
	if process.LaunchProof() != nil {
		t.Fatal("process had a launch proof without a proof-providing launcher")
	}
}

func TestD09ChildRoleMapMatchesFixedLayout(t *testing.T) {
	roles := D09ChildRoleMap()
	if len(roles) != len(piWorkerDescriptorLayout()) {
		t.Fatalf("child role map length drifted: %d", len(roles))
	}
	for index, expected := range piWorkerDescriptorLayout() {
		if roles[index].Role != expected.role || roles[index].ChildFD != expected.fd {
			t.Fatalf("child role %d drifted: got %+v want %s@%d", index, roles[index], expected.role, expected.fd)
		}
	}
	if D09ExecChildFD != 3 {
		t.Fatalf("exec child fd drifted: %d", D09ExecChildFD)
	}
}

func TestD09CellTemplateDraftIsNotAdmissible(t *testing.T) {
	if err := validateD09CellTemplate(DefaultD09CellTemplate()); !errors.Is(err, ErrD09TemplateInvalid) {
		t.Fatalf("unmeasured draft template was accepted: %v", err)
	}
	landlockDigest, err := DigestD09LandlockRules([]string{"/usr"}, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	measured := D09CellTemplate{
		Image:                             "localhost/tbound/pi-cell:latest",
		ExpectedImageDigest:               fakeD09Digest,
		CosignPublicKeyPath:               "/etc/tbound/cosign.pub",
		CosignPublicKeyDigest:             DigestBytes([]byte("cosign-pub")),
		OCILayoutPath:                     "/etc/tbound/oci",
		SeccompProfilePath:                "/etc/tbound/seccomp.json",
		SeccompProfileDigest:              DigestBytes([]byte("{}")),
		LandlockABI:                       8,
		LandlockRulesetDigest:             landlockDigest,
		LandlockROPath:                    "/usr",
		LandlockRWPath:                    "/work",
		FrozenCapabilities:                d09EmptyCapabilitySet,
		EntrypointPath:                    "/usr/local/bin/tbound-pi-entrypoint",
		EntrypointDigest:                  DigestBytes([]byte("entrypoint")),
		Runtime:                           "crun",
		CgroupsPath:                       "/sys/fs/cgroup/tbound",
		CPUs:                              "1",
		PidsMax:                           256,
		MemoryMB:                          1024,
		PodmanPath:                        "/usr/bin/podman",
		PodmanDigest:                      DigestBytes([]byte("podman")),
		CrunPath:                          "/usr/bin/crun",
		CrunDigest:                        DigestBytes([]byte("crun")),
		CosignPath:                        "/usr/bin/cosign",
		CosignDigest:                      DigestBytes([]byte("cosign")),
		CommandExecutable:                 "/bin/bash",
		CommandExecutableDigest:           DigestBytes([]byte("bash")),
		CellUID:                           testD09CellID(os.Geteuid(), 731),
		CellGID:                           testD09CellID(os.Getegid(), 732),
		NamespaceProfile:                  "user,mount,pid,ipc,uts,network-none/v1",
		CgroupVersion:                     "v2",
		CgroupControllers:                 "cpu,memory,pids",
		XattrFilesystemMagic:              0xef53,
		XattrMountOptionsDigest:           DigestBytes([]byte("ro,relatime")),
		XattrScanUID:                      os.Geteuid(),
		XattrScanGID:                      os.Getegid(),
		XattrVisibilityProfileDigest:      DigestD09XattrVisibilityProfile(0xef53, DigestBytes([]byte("ro,relatime")), os.Geteuid(), os.Getegid(), D09XattrScanPrivilegeProfile),
		XattrScanPrivilegeProfile:         D09XattrScanPrivilegeProfile,
		XattrVisibilityAuthority:          D09XattrVisibilityAuthority,
		ManagedLiveRootRegistrationDigest: DigestBytes([]byte("managed-live-root-registration")),
		UserNamespaceMappingProfile:       D09UserNamespaceMappingProfile,
		UserNamespaceMappingDigest: DigestD09UserNamespaceMapping(
			testD09CellID(os.Geteuid(), 731), testD09CellID(os.Getegid(), 732), os.Geteuid(), os.Getegid()),
	}
	if err := validateD09CellTemplate(measured); err != nil {
		t.Fatalf("measured template rejected: %v", err)
	}
	if measured.XattrVisibilityAuthority != "root-attested-bounded-no-hidden-trusted/v1" {
		t.Fatalf("the documented default xattr authority is %q", measured.XattrVisibilityAuthority)
	}
	for _, unknown := range []string{"", "root-profile-measured-complete-enumeration/v1", "root-attested-bounded-no-hidden-trusted/v2"} {
		unknownAuthority := measured
		unknownAuthority.XattrVisibilityAuthority = unknown
		if err := validateD09CellTemplate(unknownAuthority); !errors.Is(err, ErrD09TemplateInvalid) {
			t.Fatalf("template with xattr authority %q was admitted: %v", unknown, err)
		}
	}
	badXattrAuthority := measured
	badXattrAuthority.XattrScanPrivilegeProfile = "caller-claimed-complete/v1"
	if err := validateD09CellTemplate(badXattrAuthority); !errors.Is(err, ErrD09TemplateInvalid) {
		t.Fatalf("unrecognized xattr scanner privilege authority was accepted: %v", err)
	}
	first, err := measured.Digest()
	if err != nil || !validDigest(first) {
		t.Fatalf("template digest is not a valid identity: %q %v", first, err)
	}
	drifted := measured
	drifted.Runtime = "runc"
	if err := validateD09CellTemplate(drifted); !errors.Is(err, ErrD09TemplateInvalid) {
		t.Fatalf("non-crun runtime template was accepted: %v", err)
	}
	if measured.Config().ExpectedImageDigest != fakeD09Digest {
		t.Fatalf("template did not map to launcher config: %+v", measured.Config())
	}
	sourceDir := t.TempDir()
	source, err := os.Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	measurement, err := MeasureD09XattrVisibility(source)
	if err != nil {
		t.Skipf("host does not provide the registered D09 xattr scanner privilege profile: %v", err)
	}
	measured.XattrFilesystemMagic = measurement.FilesystemMagic
	measured.XattrMountOptionsDigest = measurement.MountOptionsDigest
	measured.XattrScanUID, measured.XattrScanGID = measurement.ScanUID, measurement.ScanGID
	measured.XattrScanPrivilegeProfile = measurement.ScanPrivilegeProfile
	measured.XattrVisibilityProfileDigest = DigestD09XattrVisibilityProfile(measurement.FilesystemMagic,
		measurement.MountOptionsDigest, measurement.ScanUID, measurement.ScanGID, measurement.ScanPrivilegeProfile)
	measured.registered = true // package test exercises the internal trusted-loader bit.
	measuredDigest, err := measured.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profile := validD09Profile()
	profile.ContainmentProfileDigest = measuredDigest
	if _, err := VerifyD09XattrVisibility(source, profile, measured); err == nil {
		t.Fatal("writable/unprotected temp source minted a complete xattr visibility attestation")
	}
}

func TestD09XattrAttestationRejectsUnregisteredTemplate(t *testing.T) {
	template := D09CellTemplate{}
	dir := t.TempDir()
	source, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := VerifyD09XattrVisibility(source, HostProfile{}, template); err == nil {
		t.Fatal("caller-built/unregistered template minted a workspace xattr attestation")
	}
}

// The launcher config built from the registered template carries no service
// uid/gid (they are only set when the managed root is bound), but Probe checks
// the cell identity before that bind. The signed registration supplies them.
func TestProbeCellIdentityUsesTheRegisteredServiceIdentityBeforeTheManagedRootIsBound(t *testing.T) {
	config, _ := completeD09Host(t)
	serviceUID, serviceGID := config.UserNamespaceHostUID, config.UserNamespaceHostGID
	config.registeredTemplate = true
	unbound := config
	unbound.UserNamespaceHostUID, unbound.UserNamespaceHostGID = 0, 0
	if err := validateD09CellIdentity(unbound); err == nil {
		t.Fatal("a config without the service identity passed the cell identity check")
	}
	registration := D09ManagedLiveRootRegistration{Digest: config.ManagedLiveRootRegistrationDigest,
		ServiceUID: serviceUID, ServiceGID: serviceGID}
	if err := validateD09CellIdentity(withRegisteredServiceIdentity(unbound, registration)); err != nil {
		t.Fatalf("the registered service identity was not used: %v", err)
	}
	other := registration
	other.Digest = DigestBytes([]byte("another registration"))
	if err := validateD09CellIdentity(withRegisteredServiceIdentity(unbound, other)); err == nil {
		t.Fatal("the service identity of another registration was used")
	}
	wrong := registration
	wrong.ServiceUID++
	if err := validateD09CellIdentity(withRegisteredServiceIdentity(unbound, wrong)); err == nil {
		t.Fatal("a registered service identity that does not reproduce the signed mapping digest was accepted")
	}
	bound := config
	bound.UserNamespaceHostUID++
	if got := withRegisteredServiceIdentity(bound, registration); got.UserNamespaceHostUID != bound.UserNamespaceHostUID {
		t.Fatal("an already-bound service identity was overwritten")
	}
}

func envValue(env []string, key string) (string, bool) {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, key+"="); ok {
			return value, true
		}
	}
	return "", false
}

func TestD09ProbeEnvCarriesTheServicePinnedPodmanConfiguration(t *testing.T) {
	t.Setenv("CONTAINERS_CONF", D09ManagedPiContainersConfPath)
	t.Setenv("CONTAINERS_STORAGE_CONF", D09ManagedPiStorageConfPath)
	t.Setenv("XDG_CACHE_HOME", D09ManagedPiPodmanCacheHome)
	env := d09ProbeEnv()
	for key, want := range map[string]string{"CONTAINERS_CONF": D09ManagedPiContainersConfPath,
		"CONTAINERS_STORAGE_CONF": D09ManagedPiStorageConfPath, "XDG_CACHE_HOME": D09ManagedPiPodmanCacheHome} {
		if got, ok := envValue(env, key); !ok || got != want {
			t.Fatalf("%s = %q (present %v), want the pinned %q", key, got, ok, want)
		}
	}
}

func TestD09ProbeEnvDropsPodmanConfigurationThatIsNotThePinnedOne(t *testing.T) {
	t.Setenv("CONTAINERS_CONF", "/tmp/attacker/containers.conf")
	t.Setenv("CONTAINERS_STORAGE_CONF", "/tmp/attacker/storage.conf")
	t.Setenv("XDG_CACHE_HOME", "/tmp/attacker/cache")
	env := d09ProbeEnv()
	for _, key := range []string{"CONTAINERS_CONF", "CONTAINERS_STORAGE_CONF", "XDG_CACHE_HOME"} {
		if got, ok := envValue(env, key); ok {
			t.Fatalf("%s=%q was inherited although it is not the pinned value", key, got)
		}
	}
}

// The signed image declares ENTRYPOINT ["/usr/local/bin/tbound-pi-entrypoint"], so podman runs
// the ENTRYPOINT followed by whatever comes after the image reference. The entrypoint refuses
// unless argv[1] is the held fd3 binary (or, in command mode, the signed command).
func TestD09ContainerArgumentsDoNotRepeatTheImageEntrypointAsAnArgument(t *testing.T) {
	const image = "localhost/tbound/pi-cell@" + fakeD09Digest
	config := D09Config{UID: 1000, GID: 1000, PidsMax: 128, MemoryMB: 512, CPUs: "1", SeccompProfilePath: "/etc/tbound/seccomp.json",
		CgroupsPath: "/sys/fs/cgroup/tbound", EntrypointPath: "/usr/local/bin/tbound-pi-entrypoint", CrunPath: "/usr/bin/crun",
		LandlockABI: 1, LandlockRulesetDigest: DigestBytes([]byte("landlock")), LandlockROPath: "/usr", LandlockRWPath: "/work",
		CommandExecutable: "/bin/bash"}
	bindTestD09UserNamespace(&config, 1000, 1000, 1701, 1702)

	check := func(name string, args []string, wantFirst string) {
		t.Helper()
		imageAt := indexOfString(args, image)
		if imageAt < 0 || imageAt+1 >= len(args) {
			t.Fatalf("%s: image reference is missing or last: %q", name, args)
		}
		if args[imageAt+1] != wantFirst {
			t.Fatalf("%s: the word after the image is %q, want %q (an ENTRYPOINT image would see the entrypoint as argv[1]):\n%q",
				name, args[imageAt+1], wantFirst, args)
		}
		flag := indexOfString(args, "--entrypoint")
		if flag < 0 || flag+1 >= imageAt || args[flag+1] != config.EntrypointPath {
			t.Fatalf("%s: --entrypoint %s is not pinned before the image: %q", name, config.EntrypointPath, args)
		}
		for _, word := range args[imageAt+1:] {
			if word == config.EntrypointPath {
				t.Fatalf("%s: the entrypoint path is repeated after the image: %q", name, args)
			}
		}
	}
	worker := d09ContainerArgumentsAtWithEnv(config, "tbound-pi-cell-deadbeef", image, PiWorkerCellEnvironment(), nil,
		PiWorkerCellArguments(), PiWorkerCellWorkdir, nil, "")
	check("worker cell", worker, "/proc/self/fd/3")
	command := d09CommandCellArguments(config, "tbound-pi-cell-cmd-x", "/tmp/cid", image, "/work-view", "view-1", "lease-1", 1, 2,
		sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}})
	check("command cell", command, "/bin/bash")
}

func indexOfString(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}

// cgroup_manager=cgroupfs makes podman and crun prepend /sys/fs/cgroup themselves.
func TestD09CgroupParentIsRelativeToTheCgroup2Root(t *testing.T) {
	const scope = "/sys/fs/cgroup/system.slice/tbound-pi.service/cells/pi-wf-abc-e1"
	const want = "/system.slice/tbound-pi.service/cells/pi-wf-abc-e1"
	if got, err := d09PodmanCgroupParent(scope); err != nil || got != want {
		t.Fatalf("d09PodmanCgroupParent = %q, %v; want %q", got, err, want)
	}
	for _, bad := range []string{"", "/", "/sys/fs/cgroup", "/sys/fs/cgroup/", "/system.slice/x", "/sys/fs/cgroup/../x",
		"/sys/fs/cgroup//x", "/sys/fs/cgroup/a,b", "sys/fs/cgroup/x", "/sys/fs/cgroupx/y"} {
		if got, err := d09PodmanCgroupParent(bad); err == nil {
			t.Fatalf("%q was accepted as %q", bad, got)
		}
	}

	config := D09Config{UID: 1000, GID: 1000, PidsMax: 128, MemoryMB: 512, CPUs: "1", SeccompProfilePath: "/etc/tbound/seccomp.json",
		CgroupsPath: scope, EntrypointPath: "/usr/local/bin/tbound-pi-entrypoint", CrunPath: "/usr/bin/crun",
		LandlockABI: 1, LandlockRulesetDigest: DigestBytes([]byte("landlock")), CommandExecutable: "/bin/bash"}
	bindTestD09UserNamespace(&config, 1000, 1000, 1701, 1702)
	image := "localhost/tbound/pi-cell@" + fakeD09Digest
	vectors := map[string][]string{
		"worker": d09ContainerArgumentsAtWithEnv(config, "n", image, PiWorkerCellEnvironment(), nil, PiWorkerCellArguments(), PiWorkerCellWorkdir, nil, ""),
		"command": d09CommandCellArguments(config, "n", "/tmp/cid", image, "/view", "v", "l", 1, 2,
			sessionrepo.CommandSpec{Executable: "/bin/bash"}),
	}
	for name, vector := range vectors {
		at := indexOfString(vector, "--cgroup-parent")
		if at < 0 || at+1 >= len(vector) || vector[at+1] != want {
			t.Fatalf("%s cell: --cgroup-parent = %q, want %q", name, vector, want)
		}
		if strings.Contains(strings.Join(vector, " "), "/sys/fs/cgroup") {
			t.Fatalf("%s cell: the host cgroup mount prefix reached podman: %q", name, vector)
		}
	}
}

// StartVerified captures the host namespaces on a local copy of the config, so the launcher's base
// config has none; the per-launch record carries them to ValidateLaunchProof.
func TestValidateLaunchProofUsesTheHostNamespacesCapturedAtLaunch(t *testing.T) {
	imageDigest := DigestBytes([]byte("image"))
	templateDigest := DigestBytes([]byte("template"))
	config := D09Config{UID: 1, GID: 1, ExpectedImageDigest: imageDigest, TemplateDigest: templateDigest,
		LandlockABI: 8, LandlockRulesetDigest: DigestBytes([]byte("landlock"))}
	bindTestD09UserNamespace(&config, 1, 1, 1701, 1702)
	profile := HostProfile{Mode: ModeProduction, ContainmentProfileDigest: templateDigest}
	launcher := &D09PodmanCrunLauncher{config: config}
	cmd := exec.Command("/bin/true")
	cmd.Process = &os.Process{Pid: 4242}
	proof := D09TCBProof{
		Schema: "tbound-pi-entrypoint/v1", UID: 1, GID: 1, CapEff: d09EmptyCapabilitySet,
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: config.LandlockRulesetDigest,
		LandlockTSYNC: true, CgroupV2: true, ContainerName: "cell", LeaderPID: 9,
		ContainerLeaderPID: 1, LauncherProcessPID: 4242, ImageRef: "image@" + imageDigest,
		ImageDigest: imageDigest, TemplateDigest: templateDigest, ConsumerMountsVerified: true,
		RawSourceFDsClosed: true, WorkerMountID: 1, RuntimeMountID: 2,
		WorkerDevice: 1, WorkerInode: 2, RuntimeDevice: 1, RuntimeInode: 3,
		UserNSInode: 200, MountNSInode: 201, PIDNSInode: 202, IPCNSInode: 203, UTSNSInode: 204, NetworkNSInode: 205,
		NoExternalRoutes: true,
	}
	bindTestD09TCBProofMaps(&proof, config)
	if err := launcher.ValidateLaunchProof(proof, profile, cmd); err == nil {
		t.Fatal("a proof was validated against namespaces nobody captured")
	}
	host := D09NamespaceInodes{User: 100, Mount: 101, PID: 102, IPC: 103, UTS: 104, Network: 105}
	launcher.storeLaunchProof(cmd, proof, host, nil, nil)
	if err := launcher.ValidateLaunchProof(proof, profile, cmd); err != nil {
		t.Fatalf("a proof in private namespaces was refused against the namespaces captured at launch: %v", err)
	}
	same := proof
	same.NetworkNSInode = host.Network
	if err := launcher.ValidateLaunchProof(same, profile, cmd); err == nil {
		t.Fatal("a proof sharing the host's network namespace was accepted")
	}
	same = proof
	same.UserNSInode = host.User
	if err := launcher.ValidateLaunchProof(same, profile, cmd); err == nil {
		t.Fatal("a proof sharing the host's user namespace was accepted")
	}
}

// Pi's --mode rpc reads its requests on stdin; podman forwards the service's stdin to the
// container only with --interactive, otherwise Pi reads EOF after initialising and exits.
func TestD09WorkerCellForwardsStdinButCommandCellsDoNot(t *testing.T) {
	const image = "localhost/tbound/pi-cell@" + fakeD09Digest
	config := D09Config{UID: 1000, GID: 1000, PidsMax: 128, MemoryMB: 512, CPUs: "1", SeccompProfilePath: "/etc/tbound/seccomp.json",
		CgroupsPath: "/sys/fs/cgroup/tbound", EntrypointPath: "/usr/local/bin/tbound-pi-entrypoint", CrunPath: "/usr/bin/crun",
		LandlockABI: 1, LandlockRulesetDigest: DigestBytes([]byte("landlock")), CommandExecutable: "/bin/bash"}
	bindTestD09UserNamespace(&config, 1000, 1000, 1701, 1702)
	worker := d09ContainerArgumentsAtWithEnv(config, "n", image, PiWorkerCellEnvironment(), nil, PiWorkerCellArguments(), PiWorkerCellWorkdir, nil, "")
	at := indexOfString(worker, "--interactive")
	if at < 0 || at > indexOfString(worker, image) {
		t.Fatalf("the worker cell does not forward stdin (no --interactive before the image): %q", worker)
	}
	for _, flag := range []string{"-t", "--tty", "-it", "-ti"} {
		if indexOfString(worker, flag) >= 0 {
			t.Fatalf("the worker cell allocates a tty (%s): %q", flag, worker)
		}
	}
	command := d09CommandCellArguments(config, "n", "/tmp/cid", image, "/view", "v", "l", 1, 2,
		sessionrepo.CommandSpec{Executable: "/bin/bash"})
	if indexOfString(command, "--interactive") >= 0 || indexOfString(command, "-i") >= 0 {
		t.Fatalf("a command cell reads no stdin but forwards it: %q", command)
	}
}
