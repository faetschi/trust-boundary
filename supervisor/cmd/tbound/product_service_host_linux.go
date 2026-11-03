//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/effectoracle"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/productprovider"
	"tbound/supervisor/internal/productsession"
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
		identity, err := productHostIdentity(env.Registration.ProviderProfiles)
		if err != nil {
			return err
		}
		if cfg.GitDigest != env.Registration.GitDigest || identity != env.Registration.HostIdentityDigest {
			return errors.New("the executing inputs differ from the registered host profile")
		}
		bashAllowed, err := installProductGatePolicies(env, cfg)
		if err != nil {
			return err
		}
		for _, id := range env.Registration.ProviderProfiles {
			path, ok := productprovider.CredentialPath(id)
			if !ok {
				return fmt.Errorf("the registration names provider profile %q, which this build does not support", id)
			}
			if _, err := productHostFactory(id, path); err != nil {
				return fmt.Errorf("provider credential for %q: %w", id, err)
			}
		}
		for _, project := range env.Registration.Projects {
			root, err := env.openProjectRoot(project.ProjectID)
			if err != nil {
				return err
			}
			err = validateProductPublicationFence(root)
			closeErr := root.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
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
			return pinProductionGit(productionOperatorArtifacts{GitPath: cfg.GitPath, GitDigest: cfg.GitDigest, GitBoundary: productGitBoundary}, nil)
		}
		// Refuse at start, not at the first turn.
		if _, err := verifyProductGit(cfg.GitPath, cfg.GitDigest, productServiceOwners()); err != nil {
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
		// Last, so qualification runs on the host inputs every turn will use. The socket opens only after this returns.
		return installProductCommandHooks(env, cfg, bashAllowed)
	}
}

// installProductGatePolicies loads each project's installed gate policy through the shared static validator:
// protected file and registered digest. The read is structural: a policy that allows bash is admitted only by
// installProductCommandHooks, after every qualification phase passed in this process. It reports whether any does.
func installProductGatePolicies(env *productTurnEnv, cfg productServiceConfig) (bashAllowed bool, err error) {
	env.GatePolicies = make(map[string]gate.Policy, len(cfg.GatePolicyPaths))
	for _, project := range env.Registration.Projects {
		file, ok := cfg.GatePolicyPaths[project.ProjectID]
		if !ok {
			return false, fmt.Errorf("gate_policy_paths has no file for project %q", project.ProjectID)
		}
		policy, _, err := loadProductGatePolicy(project.ProjectID, project.PolicyDigest, file, productServiceOwners(), productBashStructural)
		if err != nil {
			return false, err
		}
		env.GatePolicies[project.ProjectID] = policy
		bashAllowed = bashAllowed || productPolicyAllowsBash(policy)
	}
	return bashAllowed, nil
}

// installProductCommandHooks installs the Bash hooks the qualification returns. The Bash hooks come only from
// qualifyProductCommandHost. A deny-only host reconciles any retained command journal before it listens.
func installProductCommandHooks(env *productTurnEnv, cfg productServiceConfig, bashAllowed bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), productCommandQualificationTimeout)
	defer cancel()
	hooks, err := qualifyProductCommandHost(ctx, cfg, env, bashAllowed)
	if err != nil {
		if bashAllowed {
			return fmt.Errorf("the installed gate policy allows bash, but the product host has no contained command runner: %w", err)
		}
		return err
	}
	env.NewCommandRunner, env.OpenCommandHistory = hooks.NewCommandRunner, hooks.OpenCommandHistory
	if bashAllowed && env.NewCommandRunner == nil {
		return errors.New("the installed gate policy allows bash, but the product host has no contained command runner")
	}
	return nil
}

// productHostXattr verifies the signed xattr calibration and that the filesystem, mount and scanner of
// every root the service scans match it. It mirrors the research managed-root check minus the lease.
// The result carries the calibration's bounded authority only: the administrator's signed statement
// that this filesystem, mount and scanner identity hides no trusted.* attribute. It is not a proof that
// the unprivileged scanner enumerates every attribute. The registration key signs the calibration, so
// the product has one offline key.
func productHostXattr(cfg productServiceConfig, env *productTurnEnv) (workspace.XattrVisibilityAttestation, error) {
	var none workspace.XattrVisibilityAttestation
	key, err := readProductHostBase64(cfg.RegistrationPublicKeyPath, productServiceOwners())
	if err != nil {
		return none, fmt.Errorf("read the registration public key: %w", err)
	}
	calibration, _, _, err := verifyProductCalibration(cfg.XattrCalibrationPath, ed25519.PublicKey(key), productServiceOwners())
	if err != nil {
		return none, err
	}
	// The private Store and Git trees under the state root and every managed project root are scanned.
	roots := map[string]string{env.StateRoot: ""}
	for _, project := range env.Registration.Projects {
		roots[env.ProjectRoots[project.ProjectID]] = project.ProjectID
	}
	for path, projectID := range roots {
		var dir *os.File
		if projectID == "" {
			dir, err = os.Open(path)
		} else {
			dir, err = env.openProjectRoot(projectID)
		}
		if err != nil {
			return none, err
		}
		if projectID != "" {
			if err := validateProductManagedRoot(path, dir); err != nil {
				return none, errors.Join(err, dir.Close())
			}
		}
		m, measureErr := productHostMeasureXattr(dir)
		closeErr := dir.Close()
		if measureErr != nil || closeErr != nil {
			return none, errors.Join(measureErr, closeErr)
		}
		if !productScannerMatchesCalibration(m, calibration) {
			return none, fmt.Errorf("the filesystem, mount or scanner of %s differs from the signed xattr calibration", path)
		}
	}
	return piruntime.WorkspaceXattrAttestation(calibration.Digest, calibration.Authority)
}

func validateProductPublicationFence(root *os.File) error {
	fd, err := unix.Openat(int(root.Fd()), effectoracle.FenceDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("publication fence: %w", err)
	}
	fence := os.NewFile(uintptr(fd), effectoracle.FenceDir)
	defer fence.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("publication fence: %w", err)
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0o7777 != 0o755 {
		return errors.New("publication fence must be service-owned with canonical mode 0755")
	}
	entries, err := fence.ReadDir(1)
	if len(entries) != 0 || !errors.Is(err, io.EOF) {
		return errors.New("publication fence must be empty and readable")
	}
	return nil
}

func productHostIdentity(profileIDs []string) (string, error) {
	identity := struct {
		Schema          string                    `json:"schema"`
		SystemPrompt    string                    `json:"system_prompt"`
		DeveloperPrompt string                    `json:"developer_prompt"`
		Providers       []productprovider.Profile `json:"providers"`
		WorkerKind      string                    `json:"worker_kind"`
	}{Schema: "tbound-product-host-identity/v1", SystemPrompt: productSystemPrompt,
		DeveloperPrompt: productDeveloperPrompt, WorkerKind: productsession.WorkerSupervisorLoop}
	ids := slices.Clone(profileIDs)
	slices.Sort(ids)
	for _, id := range ids {
		profile, ok := productprovider.Lookup(id)
		if !ok {
			return "", fmt.Errorf("the registration names provider profile %q, which this build does not support", id)
		}
		identity.Providers = append(identity.Providers, profile)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}
