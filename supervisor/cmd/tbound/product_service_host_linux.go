//go:build linux

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/productprovider"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

const (
	// The prompts are part of every turn's retained evidence: a predecessor replay rebuilds the initial
	// messages from them, so changing either text breaks the replay of turns recorded under the old one.
	// They name no tool, because the installed gate policy decides which tools run.
	productSystemPrompt    = "You are the governed TBound product assistant. Use only the registered tools, and report only effects you observed in tool results."
	productDeveloperPrompt = "Work within the admitted project scope. A denied tool call returns its reason and a hint: respect both and do not retry the denied action. Changes become part of the project only after the host's publication decision."
	// productGitBoundary labels where private Git administration runs: in the supervisor's own process,
	// not in a command cell.
	productGitBoundary = "product-supervisor-private-git-admin/v1"
)

// productHostFactory builds the real conversation factory of a provider profile. Only tests replace it,
// to read a temporary credential directory and to route the fixed endpoint to a loopback fixture.
var productHostFactory = productprovider.ConversationFactory

// productHostMeasureXattr lets an offline test describe its scanner without claiming host qualification.
var productHostMeasureXattr = piruntime.MeasureD09XattrScannerIdentity

// newProductServiceHostProfile is the host of the real command. It fills every trusted piece of a turn
// from installed inputs that the service config names, and refuses to start when one is missing or
// differs from what the signed registration commits to. Provider credentials are read per turn, so a
// rotated key needs no restart.
func newProductServiceHostProfile(cfg productServiceConfig) productServiceHost {
	return func(env *productTurnEnv) error {
		if cfg.GitPath == "" || cfg.GitDigest == "" || cfg.XattrCalibrationPath == "" || len(cfg.GatePolicyPaths) != len(env.Registration.Projects) {
			return errors.New("the product host needs git_path, git_digest, xattr_calibration_path and one gate_policy_paths entry per project in the service config")
		}
		if err := installProductGatePolicies(env, cfg.GatePolicyPaths); err != nil {
			return err
		}
		for _, id := range env.Registration.ProviderProfiles {
			if _, ok := productprovider.Lookup(id); !ok {
				return fmt.Errorf("the registration names provider profile %q, which this build does not support", id)
			}
		}
		profiles := env.Registration.ProviderProfiles
		env.NewConversation = func(c providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
			path, supported := productprovider.CredentialPath(c.ProfileID)
			if !supported || !slices.Contains(profiles, c.ProfileID) {
				return nil, errors.New("the provider profile is not in the signed registration or not supported")
			}
			factory, err := productHostFactory(c.ProfileID, path)
			if err != nil {
				return nil, err
			}
			return factory(c, journal)
		}
		env.StorePolicyDigest = digestBytes([]byte(gate.ProductionSessionPolicyLabel))
		env.MetadataPolicyDigest = digestBytes([]byte(gate.ProductionMetadataPolicyLabel))
		env.SystemPrompt, env.DeveloperPrompt = productSystemPrompt, productDeveloperPrompt
		env.PinGit = func() (privategit.PinnedGit, sessionlaunch.GitProfile, error) {
			return pinProductionGit(productionOperatorArtifacts{GitPath: cfg.GitPath, GitDigest: cfg.GitDigest, GitBoundary: productGitBoundary})
		}
		git, _, err := env.PinGit() // refuse at start, not at the first turn
		if err != nil {
			return err
		}
		if err := git.Close(); err != nil {
			return err
		}
		xattr := func() (workspace.XattrVisibilityAttestation, error) { return productHostXattr(cfg, env) }
		if env.Xattr, err = xattr(); err != nil {
			return err
		}
		env.PublicationTrust = func(a *SessionOperationAuthority) workflow.Trust {
			return &productPublicationTrust{env: env, authority: a, xattr: xattr}
		}
		// One worker step is one provider exchange, which is one host call, so the registered turn limit bounds it.
		env.MaxWorkerSteps = int(env.Registration.Limits.Turn.HostCalls)
		return nil
	}
}

// installProductGatePolicies loads each project's installed gate policy and checks it against the
// digest the signed registration commits to. No contained command runner exists yet, so a policy that
// allows bash is refused here, at startup.
func installProductGatePolicies(env *productTurnEnv, paths map[string]string) error {
	env.GatePolicies = make(map[string]gate.Policy, len(paths))
	for _, project := range env.Registration.Projects {
		file, ok := paths[project.ProjectID]
		if !ok {
			return fmt.Errorf("gate_policy_paths has no file for project %q", project.ProjectID)
		}
		raw, err := readProductHostFile(file, gate.MaxPolicyBytes)
		if err != nil {
			return fmt.Errorf("read the gate policy of project %q: %w", project.ProjectID, err)
		}
		policy, err := gate.NewPolicyFromJSON(raw)
		if err != nil {
			return fmt.Errorf("parse the gate policy of project %q: %w", project.ProjectID, err)
		}
		env.GatePolicies[project.ProjectID] = policy
		if _, err := env.gatePolicy(project.ProjectID, project.PolicyDigest); err != nil {
			return err
		}
		if productPolicyAllowsBash(policy) && env.NewCommandRunner == nil {
			return fmt.Errorf("the gate policy of project %q allows bash, but the product host has no contained command runner: "+
				"install a policy that denies bash until D09 command cells are available", project.ProjectID)
		}
	}
	return nil
}

// readProductHostFile reads an installed host input: a regular file opened without following a symlink,
// owned by root or by the service, writable by neither group nor others, and within its size bound.
func readProductHostFile(path string, limit int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || info.Size() > limit {
		return nil, errors.New("host input must be a protected bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.Join(errors.New("host input exceeds its bound"), err)
	}
	return raw, nil
}

// productHostXattr verifies the signed xattr calibration and that the filesystem, mount and scanner of
// every root the service scans match it. It mirrors the research managed-root check minus the lease.
// The result carries the calibration's bounded authority only: the administrator's signed statement
// that this filesystem, mount and scanner identity hides no trusted.* attribute. It is not a proof that
// the unprivileged scanner enumerates every attribute. The registration key signs the calibration, so
// the product has one offline key.
func productHostXattr(cfg productServiceConfig, env *productTurnEnv) (workspace.XattrVisibilityAttestation, error) {
	var none workspace.XattrVisibilityAttestation
	raw, err := readProductHostFile(cfg.XattrCalibrationPath, 64<<10)
	if err != nil {
		return none, fmt.Errorf("read the xattr calibration: %w", err)
	}
	signature, err := readProductHostBase64(cfg.XattrCalibrationPath + ".sig")
	if err != nil {
		return none, fmt.Errorf("read the xattr calibration signature: %w", err)
	}
	key, err := readProductHostBase64(cfg.RegistrationPublicKeyPath)
	if err != nil {
		return none, fmt.Errorf("read the registration public key: %w", err)
	}
	calibration, err := piruntime.VerifyD09XattrEnumerationCalibration(raw, signature, ed25519.PublicKey(key))
	if err != nil {
		return none, fmt.Errorf("verify the xattr calibration: %w", err)
	}
	if calibration.SourceAccessProfile != piruntime.D09XattrManagedRWSourceProfile {
		return none, errors.New("the xattr calibration must cover managed writable roots")
	}
	// The private Store and Git trees under the state root and every managed project root are scanned.
	roots := []string{env.StateRoot}
	for _, project := range env.Registration.Projects {
		roots = append(roots, env.ProjectRoots[project.ProjectID])
	}
	for _, path := range roots {
		dir, err := os.Open(path)
		if err != nil {
			return none, err
		}
		m, measureErr := productHostMeasureXattr(dir)
		closeErr := dir.Close()
		if measureErr != nil || closeErr != nil {
			return none, errors.Join(measureErr, closeErr)
		}
		if m.ReadOnly || m.FilesystemMagic != calibration.FilesystemMagic || m.MountOptionsDigest != calibration.MountOptionsDigest ||
			m.ScanUID != calibration.ScanUID || m.ScanGID != calibration.ScanGID || m.ScanPrivilegeProfile != calibration.ScanPrivilegeProfile ||
			m.EffectiveCapabilities != "0000000000000000" || !m.NoNewPrivs || !m.InitialUserNamespace {
			return none, fmt.Errorf("the filesystem, mount or scanner of %s differs from the signed xattr calibration", path)
		}
	}
	return piruntime.WorkspaceXattrAttestation(calibration.Digest, calibration.Authority)
}

func readProductHostBase64(path string) ([]byte, error) {
	raw, err := readProductHostFile(path, 1024)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
}
