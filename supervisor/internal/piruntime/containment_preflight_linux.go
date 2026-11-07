//go:build linux

// containment_preflight_linux.go: D09 launcher admission, Probe, and offline image, signature and seccomp verification.

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
	"strconv"
	"strings"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
)

// admitsFrozenLinuxProfile is the admission gate consulted by the trusted host
// verifier. It refuses unless the signed profile names the frozen D09
// containment/settlement profile (and is not an unedited template), a verified
// cgroup scope is bound, and every host prerequisite is measured.
func (l *D09PodmanCrunLauncher) admitsFrozenLinuxProfile(profile HostProfile) error {
	if l == nil || l.productHost != nil || profile.Mode != ModeProduction {
		return ErrProductionLauncherUnavailable
	}
	if profile.ID == LinuxHostProfileTemplateID {
		return fmt.Errorf("%w: unsigned template profile is not admissible", ErrProductionLauncherUnavailable)
	}
	if placeholders := profile.PlaceholderFields(); len(placeholders) != 0 {
		return fmt.Errorf("%w: profile still holds template placeholders (%s)", ErrProductionLauncherUnavailable, strings.Join(placeholders, ", "))
	}
	if profile.ContainmentProfile != D09ContainmentProfile || profile.SettlementProfile != D09SettlementProfile {
		return fmt.Errorf("%w: signed profile does not select the frozen D09 containment/settlement profile", ErrProductionLauncherUnavailable)
	}
	if !validDigest(l.config.TemplateDigest) || profile.ContainmentProfileDigest != l.config.TemplateDigest {
		return fmt.Errorf("%w: registered D09 template digest does not match the signed profile", ErrProductionLauncherUnavailable)
	}
	if l.scope == nil {
		return fmt.Errorf("%w: no verified cgroup scope is bound to the D09 launcher", ErrCgroupScopeRefused)
	}
	l.managedRootMu.Lock()
	managedRootBound := l.managedRoot != nil
	l.managedRootMu.Unlock()
	if !managedRootBound {
		return fmt.Errorf("%w: trusted composition has not bound the verified managed live-root registration", ErrD09ManagedLiveRootRefused)
	}
	_, err := l.Probe(context.Background())
	if err != nil {
		return err
	}
	if l.productionRunner() {
		if profile.ChildExecutable != PiWorkerCellExecutable {
			return fmt.Errorf("%w: production D09 host fd3 executable path differs from the frozen in-image Node path", ErrHostProfileMismatch)
		}
		if err := l.verifyImageWorkerExecutable(context.Background(), profile.ChildExecutable, profile.ExecutableDigest); err != nil {
			return err
		}
	}
	l.admissionMu.Lock()
	l.admittedProfile = profile
	l.profileAdmitted = true
	l.admissionMu.Unlock()
	return nil
}

func (l *D09PodmanCrunLauncher) isProfileAdmitted(profile HostProfile) bool {
	if l == nil {
		return false
	}
	l.admissionMu.RLock()
	defer l.admissionMu.RUnlock()
	return l.profileAdmitted && l.admittedProfile == profile && l.config.TemplateDigest == profile.ContainmentProfileDigest
}

func (l *D09PodmanCrunLauncher) admittedProfileSnapshot() (HostProfile, bool) {
	if l == nil {
		return HostProfile{}, false
	}
	l.admissionMu.RLock()
	defer l.admissionMu.RUnlock()
	if l.isClosed() || !l.profileAdmitted || l.admittedProfile.Mode != ModeProduction || l.admittedProfile.ContainmentProfileDigest != l.config.TemplateDigest {
		return HostProfile{}, false
	}
	return l.admittedProfile, true
}

// BoundSettlement returns the held, verified cgroup scope so the process
// lifecycle settles through cgroup.kill and populated=0, not a container-name
// query.
func (l *D09PodmanCrunLauncher) BoundSettlement() DescendantSettler {
	if l == nil {
		return nil
	}
	return l.scope
}

// Probe measures every D09 prerequisite and returns the complete exact list of
// what is missing. Any podman info failure or unparseable rootless/crun/cgroup
// field is fatal: the probe never fails open.
func (l *D09PodmanCrunLauncher) Probe(ctx context.Context) (D09Report, error) {
	if l == nil || l.runner == nil || ctx == nil {
		return D09Report{}, &D09PrerequisiteError{Missing: []D09Prerequisite{{Name: "runner", Detail: "containment launcher is not configured"}}}
	}
	if err := l.beginOperation(); err != nil {
		return D09Report{}, err
	}
	defer l.endOperation()
	config := l.config.normalized()
	report := D09Report{}
	var missing []D09Prerequisite
	var managedRootErr error
	var identityErr error
	if l.productHost != nil {
		var err error
		config, err = l.productD09ProbeConfig(ctx)
		if err != nil {
			return report, &D09PrerequisiteError{Missing: []D09Prerequisite{{Name: "product-command-host", Detail: err.Error()}}}
		}
		identityErr = validateD09CellIdentityMeasurements(config)
	} else {
		if l.productionRunner() {
			// The template config carries no service uid/gid; they come from the signed
			// registration the managed root verifies against, so resolve it before the
			// identity check instead of waiting for BindManagedLiveRoot.
			registration, err := l.ensureManagedLiveRoot(ctx)
			managedRootErr = err
			if err == nil {
				config = withRegisteredServiceIdentity(config, registration)
			}
		}
		identityErr = validateD09CellIdentity(config)
	}
	if identityErr != nil {
		missing = append(missing, D09Prerequisite{Name: "cell-identity", Detail: identityErr.Error()})
	}
	if l.productionRunner() {
		if managedRootErr != nil {
			missing = append(missing, D09Prerequisite{Name: "managed-live-root", Detail: managedRootErr.Error()})
		}
		abi, abiErr := D09LandlockKernelABI()
		if abiErr != nil {
			missing = append(missing, D09Prerequisite{Name: "landlock-abi", Detail: abiErr.Error()})
		} else {
			report.LandlockABI = abi
			if abi != config.LandlockABI {
				missing = append(missing, D09Prerequisite{Name: "landlock-abi", Detail: fmt.Sprintf("registered exact ABI %d required; kernel reports %d", config.LandlockABI, abi)})
			}
		}
	}

	podmanPath := ""
	if config.PodmanPath != "" {
		if err := verifyPinnedToolBinary(config.PodmanPath, config.PodmanDigest); err != nil {
			missing = append(missing, D09Prerequisite{Name: "podman", Detail: "pinned podman is not the trusted root-owned immutable binary: " + err.Error()})
		} else {
			podmanPath = config.PodmanPath
		}
	} else if l.productionRunner() {
		missing = append(missing, D09Prerequisite{Name: "podman", Detail: "registered root-owned podman path+digest is not configured"})
	} else if resolved, err := l.runner.LookPath("podman"); err == nil && strings.TrimSpace(resolved) != "" {
		podmanPath = resolved
	} else {
		missing = append(missing, D09Prerequisite{Name: "podman", Detail: "podman not found in PATH and no pinned podman is configured"})
	}
	if config.CrunPath != "" {
		if err := verifyPinnedToolBinary(config.CrunPath, config.CrunDigest); err != nil {
			missing = append(missing, D09Prerequisite{Name: config.Runtime, Detail: "pinned " + config.Runtime + " is not the trusted root-owned immutable binary: " + err.Error()})
		} else {
			report.CrunPresent = true
		}
	} else if l.productionRunner() {
		missing = append(missing, D09Prerequisite{Name: config.Runtime, Detail: "registered root-owned crun path+digest is not configured"})
	} else if _, err := l.runner.LookPath(config.Runtime); err != nil {
		missing = append(missing, D09Prerequisite{Name: config.Runtime, Detail: config.Runtime + " OCI runtime not found in PATH and no pinned runtime is configured"})
	} else {
		report.CrunPresent = true
	}
	cosignBinary := ""
	if config.CosignPath != "" {
		if err := verifyPinnedToolBinary(config.CosignPath, config.CosignDigest); err != nil {
			missing = append(missing, D09Prerequisite{Name: "cosign", Detail: "pinned cosign is not the trusted root-owned immutable binary: " + err.Error()})
		} else {
			cosignBinary = config.CosignPath
			report.CosignPresent = true
		}
	} else if l.productionRunner() {
		missing = append(missing, D09Prerequisite{Name: "cosign", Detail: "registered root-owned cosign path+digest is not configured"})
	} else if _, err := l.runner.LookPath("cosign"); err != nil {
		missing = append(missing, D09Prerequisite{Name: "cosign", Detail: "cosign not found in PATH and no pinned cosign is configured"})
	} else {
		cosignBinary = "cosign"
		report.CosignPresent = true
	}

	if podmanPath != "" {
		report.PodmanPresent = true
		report.PodmanPath = podmanPath
		if result := l.runner.Run(ctx, podmanPath, "--version"); result.Err != nil {
			missing = append(missing, D09Prerequisite{Name: "podman-info", Detail: "podman --version: " + result.Err.Error()})
		} else if result.ExitCode != 0 {
			missing = append(missing, D09Prerequisite{Name: "podman-info", Detail: "podman --version exited " + strconv.Itoa(result.ExitCode)})
		} else {
			report.PodmanVersion = d09LastField(string(result.Stdout))
			if report.PodmanVersion == "" {
				missing = append(missing, D09Prerequisite{Name: "podman-info", Detail: "podman --version produced no version"})
			}
		}
		info, infoErr := l.readInfo(ctx, podmanPath)
		if infoErr != nil {
			// Unknown/unparseable identity is fatal: never continue with an
			// unmeasured runtime.
			missing = append(missing, D09Prerequisite{Name: "podman-info", Detail: infoErr.Error()})
		} else {
			report.Rootless = info.Host.Security.Rootless
			report.OCIRuntime = strings.TrimSpace(info.Host.OCIRuntime.Name)
			report.CgroupVersion = strings.TrimSpace(info.Host.CgroupVersion)
			if !report.Rootless {
				missing = append(missing, D09Prerequisite{Name: "rootless", Detail: "podman is not in rootless mode"})
			}
			if report.OCIRuntime == "" || !strings.EqualFold(report.OCIRuntime, config.Runtime) {
				missing = append(missing, D09Prerequisite{Name: "oci-runtime", Detail: "expected " + config.Runtime + ", observed " + report.OCIRuntime})
			}
			if report.CgroupVersion != "v2" {
				missing = append(missing, D09Prerequisite{Name: "cgroup-v2", Detail: "observed " + report.CgroupVersion})
			}
		}
	}

	switch {
	case strings.TrimSpace(config.ExpectedImageDigest) == "":
		missing = append(missing, D09Prerequisite{Name: "image", Detail: "frozen Pi cell image digest pin is not configured"})
	case !report.PodmanPresent:
		missing = append(missing, D09Prerequisite{Name: "image", Detail: "local image cannot be inspected without podman"})
	default:
		image, imageErr := l.selectImage(ctx, report.PodmanPath, config.Image, config.ExpectedImageDigest)
		if imageErr != nil {
			missing = append(missing, D09Prerequisite{Name: "image", Detail: imageErr.Error()})
		} else {
			report.ImageRef = image
			report.ImageDigest = config.ExpectedImageDigest
			report.ImagePinned = true
			if !report.CosignPresent {
				missing = append(missing, D09Prerequisite{Name: "image-signature", Detail: "cosign is unavailable for local offline verification"})
			} else if verifyErr := l.verifyOfflineSignatureForProbe(ctx, config, cosignBinary, l.productionRunner()); verifyErr != nil {
				missing = append(missing, D09Prerequisite{Name: "image-signature", Detail: verifyErr.Error()})
			} else {
				report.ImageVerified = true
			}
			if err := verifyD09SeccompProfile(config, l.productionRunner()); err != nil {
				missing = append(missing, D09Prerequisite{Name: "seccomp-profile", Detail: err.Error()})
			}
			if l.productionRunner() {
				if err := l.verifyEntrypointInImage(ctx, config); err != nil {
					missing = append(missing, D09Prerequisite{Name: "entrypoint", Detail: err.Error()})
				}
			}
			if config.LandlockABI != D09LandlockABI || !validDigest(config.LandlockRulesetDigest) {
				missing = append(missing, D09Prerequisite{Name: "landlock", Detail: "the exact registered Landlock ABI-8 TSYNC/ruleset profile is not configured"})
			} else if roPaths, roErr := ParseD09LandlockPaths(config.LandlockROPath); roErr != nil {
				missing = append(missing, D09Prerequisite{Name: "landlock", Detail: roErr.Error()})
			} else if rwPaths, rwErr := ParseD09LandlockPaths(config.LandlockRWPath); rwErr != nil {
				missing = append(missing, D09Prerequisite{Name: "landlock", Detail: rwErr.Error()})
			} else if observed, digestErr := DigestD09LandlockRules(roPaths, rwPaths); digestErr != nil || observed != config.LandlockRulesetDigest {
				missing = append(missing, D09Prerequisite{Name: "landlock", Detail: "measured Landlock ruleset digest differs from the registered ABI-8 policy"})
			}
			if strings.TrimSpace(config.FrozenCapabilities) != d09EmptyCapabilitySet {
				missing = append(missing, D09Prerequisite{Name: "capabilities", Detail: "frozen empty capability set is not configured"})
			}
			if config.PidsMax <= 0 || config.MemoryMB <= 0 || strings.TrimSpace(config.CPUs) == "" {
				missing = append(missing, D09Prerequisite{Name: "resource-bounds", Detail: "cpu/memory/pids bounds are not configured"})
			}
		}
	}

	if len(missing) != 0 {
		return report, &D09PrerequisiteError{Missing: missing}
	}
	return report, nil
}

// verifyEntrypointInImage hashes the entrypoint stored in the exact pinned,
// cosign-verified OCI layout against the protected template digest. Rootless
// podman cannot mount an image outside `podman unshare`, and a mount made there
// would be invisible to this process, so the bytes come from the verified
// layers (the store image is bound to the same manifest digest by selectImage).
func (l *D09PodmanCrunLauncher) verifyEntrypointInImage(ctx context.Context, config D09Config) error {
	return l.verifyImageFile(ctx, config, config.EntrypointPath, config.EntrypointDigest, "in-image entrypoint")
}

// verifyImageWorkerExecutable binds the held host fd3 Node bytes to the exact
// executable at the signed in-image path. Both identities must be the same
// digest: image signature verification alone cannot authenticate a different
// host binary which the entrypoint actually executes through fd3.
func (l *D09PodmanCrunLauncher) verifyImageWorkerExecutable(ctx context.Context, executablePath, expectedDigest string) error {
	if ctx == nil || !filepath.IsAbs(executablePath) || filepath.Clean(executablePath) != executablePath || !validDigest(expectedDigest) {
		return ErrHostProfileMismatch
	}
	return l.verifyImageFile(ctx, l.config, executablePath, expectedDigest, "in-image fd3 executable")
}

func (l *D09PodmanCrunLauncher) verifyImageFile(ctx context.Context, config D09Config, imagePath, expectedDigest, what string) error {
	readImageFile := readOCIImageFile
	if l.productHost.verified() {
		readImageFile = readProductOCIImageFile // memoized while the layout is unchanged
	}
	file, err := readImageFile(ctx, config.OCILayoutPath, config.ExpectedImageDigest, filepath.Clean(imagePath), l.productionRunner())
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if file.mode&0o111 == 0 || file.mode&0o022 != 0 {
		return fmt.Errorf("%s is not a non-writable executable regular file", what)
	}
	if file.digest != expectedDigest {
		return fmt.Errorf("%s digest mismatch: got %s want %s", what, file.digest, expectedDigest)
	}
	return nil
}

type d09PodmanInfo struct {
	Host struct {
		CgroupVersion string `json:"cgroupVersion"`
		OCIRuntime    struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"ociRuntime"`
		Security struct {
			Rootless bool `json:"rootless"`
		} `json:"security"`
	} `json:"host"`
}

func (l *D09PodmanCrunLauncher) readInfo(ctx context.Context, podmanPath string) (d09PodmanInfo, error) {
	result := l.runner.Run(ctx, podmanPath, "info", "--format", "json")
	if result.Err != nil {
		return d09PodmanInfo{}, result.Err
	}
	if result.ExitCode != 0 {
		return d09PodmanInfo{}, fmt.Errorf("podman info exited %d: %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	if len(bytes.TrimSpace(result.Stdout)) == 0 {
		return d09PodmanInfo{}, errors.New("podman info produced no output")
	}
	var info d09PodmanInfo
	if err := json.Unmarshal(result.Stdout, &info); err != nil {
		return d09PodmanInfo{}, fmt.Errorf("decode podman info: %w", err)
	}
	return info, nil
}

// selectImage resolves a local reference to a repo@digest whose digest equals
// the frozen pin. It never pulls.
func (l *D09PodmanCrunLauncher) selectImage(ctx context.Context, podmanPath, reference, expected string) (string, error) {
	exists := l.runner.Run(ctx, podmanPath, "image", "exists", reference)
	if exists.Err != nil {
		return "", exists.Err
	}
	if exists.ExitCode != 0 {
		return "", fmt.Errorf("no local image %q is present in the podman store", reference)
	}
	inspect := l.runner.Run(ctx, podmanPath, "image", "inspect", reference)
	if inspect.Err != nil {
		return "", inspect.Err
	}
	if inspect.ExitCode != 0 {
		return "", fmt.Errorf("podman image inspect %q exited %d", reference, inspect.ExitCode)
	}
	var images []struct {
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(inspect.Stdout, &images); err != nil {
		return "", fmt.Errorf("decode image inspect: %w", err)
	}
	if len(images) == 0 {
		return "", fmt.Errorf("image inspect returned no object for %q", reference)
	}
	for _, repoDigest := range images[0].RepoDigests {
		at := strings.LastIndex(repoDigest, "@")
		if at < 0 {
			continue
		}
		digest := strings.TrimSpace(repoDigest[at+1:])
		if validDigest(strings.ToLower(digest)) && strings.EqualFold(digest, expected) {
			return repoDigest, nil
		}
	}
	return "", fmt.Errorf("local image %q does not match the frozen digest %s", reference, expected)
}

// verifyOfflineSignature verifies the locally mirrored OCI image layout with
// the official `cosign verify --offline --insecure-ignore-tlog=true --key <pub> --local-image <layout>` form, then
// cryptographically binds the verified signature payload to the frozen pin and
// the local OCI bytes. It never names a registry reference and runs with a
// sanitized, proxy-free environment. --insecure-ignore-tlog is required: a key
// signature that was never uploaded to Rekor fails cosign v3 log-inclusion
// otherwise (measured, cosign v3.1.3); trust rests on the pinned public key.
func (l *D09PodmanCrunLauncher) verifyOfflineSignature(ctx context.Context, config D09Config, cosignBinary string, requireImmutable bool) error {
	if strings.TrimSpace(config.CosignPublicKeyPath) == "" {
		return errors.New("protected offline cosign public key path is not configured")
	}
	if !validDigest(config.CosignPublicKeyDigest) {
		return errors.New("offline cosign public key digest is not registered")
	}
	var keyDigest string
	var keyErr error
	if requireImmutable {
		keyDigest, keyErr = hashProtectedFile(config.CosignPublicKeyPath, 1<<20)
	} else {
		keyDigest, keyErr = hashBoundedUntrustedFile(config.CosignPublicKeyPath, 1<<20)
	}
	if keyErr != nil || keyDigest != config.CosignPublicKeyDigest {
		return errors.Join(errors.New("offline cosign public key does not match registered digest"), keyErr)
	}
	if strings.TrimSpace(config.OCILayoutPath) == "" {
		return errors.New("local OCI image layout path is not configured for local verification")
	}
	if strings.TrimSpace(cosignBinary) == "" {
		cosignBinary = "cosign"
	}
	if err := verifyLocalOCIIdentity(config.OCILayoutPath, config.ExpectedImageDigest, requireImmutable); err != nil {
		return err
	}
	result := l.runner.Run(ctx, cosignBinary, "verify", "--offline", "--insecure-ignore-tlog=true", "--key", config.CosignPublicKeyPath, "--local-image", config.OCILayoutPath)
	if result.Err != nil {
		return result.Err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("cosign local-image verification exited %d: %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	claimed, err := parseCosignImageDigest(result.Stdout)
	if err != nil {
		return err
	}
	if !strings.EqualFold(claimed, config.ExpectedImageDigest) {
		return fmt.Errorf("cosign signature binds image %s, not the frozen %s", claimed, config.ExpectedImageDigest)
	}
	return nil
}

// parseCosignImageDigest extracts the mandatory image digest from the bounded,
// strict cosign JSON payload. A missing or unknown identity is fatal: a merely
// well-shaped payload is never accepted.
func parseCosignImageDigest(stdout []byte) (string, error) {
	if len(stdout) == 0 || len(stdout) > 1<<20 {
		return "", errors.New("cosign verification produced no bounded JSON payload")
	}
	if err := protocol.ValidateStrictJSON(stdout); err != nil {
		return "", fmt.Errorf("cosign verification output is not strict JSON: %w", err)
	}
	var entries []struct {
		Critical struct {
			Image struct {
				DockerManifestDigest string `json:"docker-manifest-digest"`
			} `json:"image"`
		} `json:"critical"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	if err := decoder.Decode(&entries); err != nil {
		return "", fmt.Errorf("decode cosign verification payload: %w", err)
	}
	for _, entry := range entries {
		digest := strings.TrimSpace(entry.Critical.Image.DockerManifestDigest)
		if validDigest(strings.ToLower(digest)) {
			return digest, nil
		}
	}
	return "", errors.New("cosign verification payload has no trusted image digest claim")
}

// verifyLocalOCIIdentity binds the local OCI image layout to the frozen pin: the
// index must name a manifest whose digest equals the pin, and the manifest blob
// must actually hash to that digest. A layout whose bytes do not match the pin
// fails closed even if a signature over a different image is valid.
func verifyLocalOCIIdentity(layoutPath, expectedDigest string, requireImmutable bool) error {
	info, err := os.Lstat(layoutPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("local OCI image layout %q is not a readable directory", layoutPath)
	}
	if requireImmutable {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return errors.New("local OCI image layout must be root-owned and not group/world writable")
		}
		if err := requireReadOnlyMount(layoutPath); err != nil {
			return fmt.Errorf("local OCI image layout is not on a read-only mount: %w", err)
		}
	}
	indexRaw, err := readBoundedFile(filepath.Join(layoutPath, "index.json"), 1<<20, requireImmutable)
	if err != nil {
		return fmt.Errorf("read OCI index: %w", err)
	}
	if err := protocol.ValidateStrictJSON(indexRaw); err != nil {
		return fmt.Errorf("OCI index is not strict JSON: %w", err)
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		return fmt.Errorf("decode OCI index: %w", err)
	}
	for _, manifest := range index.Manifests {
		digest := strings.TrimSpace(manifest.Digest)
		if !strings.EqualFold(digest, expectedDigest) {
			continue
		}
		hexDigest := strings.TrimPrefix(strings.ToLower(digest), "sha256:")
		blobPath := filepath.Join(layoutPath, "blobs", "sha256", hexDigest)
		blob, err := readBoundedFile(blobPath, 64<<20, requireImmutable)
		if err != nil {
			return fmt.Errorf("read OCI manifest blob: %w", err)
		}
		if DigestBytes(blob) != strings.ToLower(digest) {
			return fmt.Errorf("local OCI manifest blob does not hash to %s", digest)
		}
		if err := protocol.ValidateStrictJSON(blob); err != nil {
			return fmt.Errorf("OCI image manifest is not strict JSON: %w", err)
		}
		var imageManifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers []struct {
				Digest string `json:"digest"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(blob, &imageManifest); err != nil || len(imageManifest.Layers) == 0 || len(imageManifest.Layers) > 4096 {
			return errors.Join(errors.New("OCI image manifest is invalid or outside layer bound"), err)
		}
		digests := make([]string, 0, len(imageManifest.Layers)+1)
		digests = append(digests, imageManifest.Config.Digest)
		for _, layer := range imageManifest.Layers {
			digests = append(digests, layer.Digest)
		}
		var total int64
		for _, childDigest := range digests {
			if !validDigest(childDigest) {
				return errors.New("OCI config/layer descriptor has invalid digest")
			}
			path := filepath.Join(layoutPath, "blobs", "sha256", strings.TrimPrefix(childDigest, "sha256:"))
			size, verifyErr := verifyOCIBlob(path, childDigest, PiRuntimeBundleMaxBytes-total, requireImmutable)
			if verifyErr != nil {
				return fmt.Errorf("verify OCI config/layer %s: %w", childDigest, verifyErr)
			}
			total += size
		}
		return nil
	}
	return fmt.Errorf("local OCI layout does not contain the frozen manifest %s", expectedDigest)
}

func readBoundedFile(path string, maximum int64, requireImmutable ...bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maximum {
		return nil, ErrD09TemplateInvalid
	}
	if len(requireImmutable) != 0 && requireImmutable[0] {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return nil, errors.New("OCI artifact must be root-owned and non-writable")
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("OCI artifact changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errors.Join(errors.New("OCI artifact read was incomplete or exceeded bound"), err)
	}
	return data, nil
}

func verifyOCIBlob(path, expected string, maximum int64, requireImmutable bool) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maximum {
		return 0, ErrD09TemplateInvalid
	}
	if requireImmutable {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return 0, errors.New("OCI blob must be root-owned and non-writable")
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return 0, errors.New("OCI blob changed while opening")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil || n != info.Size() {
		return 0, errors.Join(errors.New("OCI blob read was incomplete or exceeded bound"), err)
	}
	observed := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(observed, expected) {
		return 0, errors.New("OCI blob content digest mismatch")
	}
	return n, nil
}

// verifyD09SeccompProfile binds the static seccomp-BPF profile bytes to the
// digest recorded in the registered template. It is skipped only when the
// template left seccomp entirely unconfigured.
func verifyD09SeccompProfile(config D09Config, requireProtected bool) error {
	path, digest := strings.TrimSpace(config.SeccompProfilePath), strings.TrimSpace(config.SeccompProfileDigest)
	if path == "" && digest == "" {
		return nil
	}
	if path == "" || digest == "" {
		return errors.New("static seccomp profile path and digest must both be configured")
	}
	var observed string
	var err error
	if requireProtected {
		observed, err = hashProtectedFile(path, maxD09TemplateBytes)
	} else {
		observed, err = hashBoundedUntrustedFile(path, maxD09TemplateBytes)
	}
	if err != nil {
		return fmt.Errorf("read static seccomp profile: %w", err)
	}
	if observed != digest {
		return fmt.Errorf("static seccomp profile digest mismatch: got %s want %s", observed, digest)
	}
	return nil
}

func hashBoundedUntrustedFile(path string, maximum int64) (string, error) {
	data, err := readBoundedFile(path, maximum)
	if err != nil {
		return "", err
	}
	return DigestBytes(data), nil
}
