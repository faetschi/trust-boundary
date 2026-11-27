//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"time"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runner"
)

// ErrPiServiceCorpus means the corpus manifest of a corpus-mode service is missing,
// unreadable, not the pinned one, or not admissible.
var ErrPiServiceCorpus = errors.New("corpus manifest is missing, not the pinned manifest, or not admissible")

// Test seams. Production reads the fixed root-owned manifest through the strict
// no-symlink ancestor walk and validates it against the quotas of its profile, which
// come from the D48 draw the manifest pins (corpusadmit.QuotasFor).
var (
	readCorpusManifest = piruntime.ReadD09ManagedPiCorpusManifest
	corpusQuotasFor    = func(profile string, m evalcorpus.Manifest) (evalcorpus.Quotas, error) {
		return corpusadmit.QuotasFor(profile, m, attackerArtifacts())
	}

	// attackerArtifacts is where the attacker artifact and the D48 draw named by the manifest pins are read: the
	// root-owned artifact directory the signing session installs (the same one `tbound trial reset` reads fixtures from).
	attackerArtifacts = func() fs.FS { return os.DirFS(trialArtifactDir) }
)

// The closed set of governed roles. Each names one planned governed C trial of a case
// and applies to one manifest case kind; the names are the runner's own (internal/runner
// GovernedRoles). Ambiguous cases also run C in the plan, so their role is admitted too.
const (
	corpusRoleTrajectory     = "trajectory"
	corpusRoleStaticReplay   = "static-replay"
	corpusRoleAdaptiveReplay = corpusadmit.RoleAdaptiveReplay
	corpusRoleVariance1      = "variance-1"
	corpusRoleVariance2      = "variance-2"
	corpusRoleCompliant      = "compliant"
	corpusRoleAmbiguous      = "ambiguous"

	// The roles of the P half of a pair (Configuration P under the case's registered delta).
	corpusRolePStaticReplay   = corpusadmit.RolePStaticReplay
	corpusRolePAdaptiveReplay = corpusadmit.RolePAdaptiveReplay

	// The roles of Configuration B: compliant and ambiguous cases on the real model with admit-all/v1.
	corpusRoleBCompliant = corpusadmit.RoleBCompliant
	corpusRoleBAmbiguous = corpusadmit.RoleBAmbiguous

	corpusRoleV = "v"
)

func validCorpusRole(role string) bool {
	if _, _, ok := corpusadmit.CDHRoleParts(role); ok {
		return true
	}
	switch role {
	case corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleAdaptiveReplay, corpusRoleVariance1,
		corpusRoleVariance2, corpusRoleCompliant, corpusRoleAmbiguous, corpusRolePStaticReplay, corpusRolePAdaptiveReplay,
		corpusRoleBCompliant, corpusRoleBAmbiguous, corpusRoleV:
		return true
	}
	return false
}

// corpusCase is what the service keeps of a manifest case besides its trial: the kind,
// the variance-subset membership and the P-only mark that decide which roles the case may run.
type corpusCase struct {
	kind     evalcorpus.Kind
	variance bool
	// pOnly marks a TM-C development fixture of a pilot corpus: it runs Configuration P only, never C (D41).
	pOnly bool
}

// productionCorpus is the frozen, digest-pinned set of trials a corpus-mode service
// admits. It is built once at service start and is read-only afterwards.
type productionCorpus struct {
	cdh           *runner.CDHConfig
	hintProviders map[string]providerbridge.HintProvider
	digest        string
	// quotaProfile is the profile the manifest was validated under: "thesis" or "pilot".
	quotaProfile string
	trials       map[string]evalcorpus.Trial
	cases        map[string]corpusCase
	// fixtures maps a case to its workspace_fixture_digest, for `tbound trial reset`.
	fixtures map[string]string
	// attacker is the pinned attacker artifact (Pins.AttackerDigest) that the adaptive replay roles build
	// their machine from; nil when no case carries pivots.
	attacker       *attacker.Spec
	attackerDigest string
}

// corpusAdmissionID is the one-use admission id of a trial: <case_id>/<role>/<attempt>.
func corpusAdmissionID(caseID, role string, attempt int) string {
	return corpusadmit.AdmissionID(caseID, role, attempt)
}

// roleAllowed reports whether role may run for caseID: trajectory and the replay roles (of both
// halves of the pair)
// only for adversarial cases, the variance roles only for adversarial cases in the
// variance subset, compliant, b-compliant and v only for compliant tasks, ambiguous and
// b-ambiguous only for ambiguous cases. A P-only case runs the P roles only.
func (c *productionCorpus) roleAllowed(caseID, role string) bool {
	if c != nil && c.quotaProfile == piruntime.D09ManagedPiCorpusQuotaProfileScreening && role != corpusRoleBCompliant {
		return false
	}
	if c != nil && c.cases[caseID].pOnly && role != corpusRolePStaticReplay && role != corpusRolePAdaptiveReplay {
		return false
	}
	if _, _, ok := corpusadmit.CDHRoleParts(role); ok {
		return c.cdhAllowed(caseID, role)
	}
	if c == nil {
		return false
	}
	info, ok := c.cases[caseID]
	if !ok {
		return false
	}
	switch role {
	case corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleAdaptiveReplay, corpusRolePStaticReplay, corpusRolePAdaptiveReplay:
		return info.kind == evalcorpus.KindAdversarial
	case corpusRoleVariance1, corpusRoleVariance2:
		return info.kind == evalcorpus.KindAdversarial && info.variance
	case corpusRoleCompliant, corpusRoleBCompliant, corpusRoleV:
		return info.kind == evalcorpus.KindCompliant
	case corpusRoleAmbiguous, corpusRoleBAmbiguous:
		return info.kind == evalcorpus.KindAmbiguous
	}
	return false
}

// roleServable reports whether this build can serve role for caseID once roleAllowed holds: a static
// replay role needs the frozen script of the case; an adaptive replay role needs the pivot table of the
// case and the pinned attacker artifact (the C half must never fall back to a model); a P role needs a
// delta the build implements. It never selects a provider for a role it cannot serve.
func (c *productionCorpus) roleServable(caseID, role string) bool {
	if !c.roleAllowed(caseID, role) {
		return false
	}
	trial := c.trials[caseID]
	switch role {
	case corpusRoleStaticReplay:
		return len(trial.Script) != 0
	case corpusRolePStaticReplay:
		return len(trial.Script) != 0 && implementedPDelta(trial.EffectivePDelta())
	case corpusRoleAdaptiveReplay:
		return c.adaptiveServable(trial)
	case corpusRolePAdaptiveReplay:
		return c.adaptiveServable(trial) && len(trial.Script) != 0 && implementedPDelta(trial.EffectivePDelta())
	}
	return true
}

// adaptiveServable reports whether the adaptive replay of a case can run: it has a pivot table and the
// manifest pinned an attacker artifact that loaded.
func (c *productionCorpus) adaptiveServable(trial evalcorpus.Trial) bool {
	return c.attacker != nil && len(trial.Pivots) != 0
}

// implementedPDelta lists the registered deltas this build implements: all five. net-sink/v1 and no-reap/v1
// run the P command cell under a derived template (commandCellDeltaFor, piruntime.D09CellDelta);
// net-sink+admit-all/v1 is served because both of its halves are.
func implementedPDelta(delta string) bool {
	return delta == evalcorpus.PDeltaNone || delta == evalcorpus.PDeltaAdmitAll ||
		delta == evalcorpus.PDeltaNetSink || delta == evalcorpus.PDeltaNoReap || delta == evalcorpus.PDeltaNetSinkAdmitAll
}

func (c *productionCorpus) trial(caseID string) (evalcorpus.Trial, bool) {
	if c == nil {
		return evalcorpus.Trial{}, false
	}
	trial, ok := c.trials[caseID]
	return trial, ok
}

// loadProductionCorpus reads the manifest named by a corpus-mode config and admits it
// only if all of these hold: the fixed path was read through the strict walk, the
// strict loader accepts it, evalcorpus.Digest equals the digest in the signed config,
// evalcorpus.Validate finds no violation under the registered quotas, and every case
// carries a trial that fits the host ceilings of the config. Anything else refuses the
// service before it listens.
func loadProductionCorpus(config piServiceConfig) (*productionCorpus, error) {
	if config.WorkflowAdmissionMode != piServiceAdmissionCorpus || config.CorpusManifestPath != piServiceCorpusManifestPath ||
		!validDigestText(config.CorpusManifestDigest) {
		return nil, fmt.Errorf("%w: the service config is not a corpus-mode config", ErrPiServiceCorpus)
	}
	data, err := readCorpusManifest()
	if err != nil {
		return nil, errors.Join(ErrPiServiceCorpus, err)
	}
	manifest, err := evalcorpus.Load(bytes.NewReader(data))
	if err != nil {
		return nil, errors.Join(ErrPiServiceCorpus, err)
	}
	digest, err := evalcorpus.Digest(manifest)
	if err != nil {
		return nil, errors.Join(ErrPiServiceCorpus, err)
	}
	if digest != config.CorpusManifestDigest {
		return nil, fmt.Errorf("%w: manifest digest %s differs from the signed config digest %s",
			ErrPiServiceCorpus, digest, config.CorpusManifestDigest)
	}
	profile := config.effectiveCorpusQuotaProfile()
	if profile != piruntime.D09ManagedPiCorpusQuotaProfilePilot && profile != piruntime.D09ManagedPiCorpusQuotaProfileThesis &&
		profile != piruntime.D09ManagedPiCorpusQuotaProfileScreening {
		return nil, fmt.Errorf("%w: unknown corpus quota profile %q", ErrPiServiceCorpus, profile)
	}
	// The research pilot's adversarial cases are the random pilot draw (D40 as amended by D48), so a pilot corpus on the
	// research route must pin it; the rehearsal counts are for the engineering route only.
	if profile == piruntime.D09ManagedPiCorpusQuotaProfilePilot && config.ProviderRoute == piruntime.D09ManagedPiProviderRouteResearch &&
		manifest.Pins.DrawDigest == "" {
		return nil, fmt.Errorf("%w: a pilot corpus on the research route is the research pilot and must pin its D48 pilot draw (pins.draw_digest)", ErrPiServiceCorpus)
	}
	if profile == piruntime.D09ManagedPiCorpusQuotaProfileScreening && config.ProviderRoute != piruntime.D09ManagedPiProviderRouteResearch {
		return nil, fmt.Errorf("%w: the screening profile runs on the research route only", ErrPiServiceCorpus)
	}
	quotas, err := corpusQuotasFor(profile, manifest)
	if err != nil {
		return nil, fmt.Errorf("%w: the %s quotas: %w", ErrPiServiceCorpus, profile, err)
	}
	if violations := evalcorpus.Validate(manifest, quotas); len(violations) != 0 {
		return nil, fmt.Errorf("%w: evalcorpus.Validate under the %s quotas: %d violation(s), first %s: %s",
			ErrPiServiceCorpus, profile, len(violations), violations[0].Rule, violations[0].Detail)
	}
	corpus := &productionCorpus{digest: digest, quotaProfile: profile, trials: make(map[string]evalcorpus.Trial, len(manifest.Cases)),
		cases: make(map[string]corpusCase, len(manifest.Cases)), fixtures: make(map[string]string, len(manifest.Cases))}
	if err := corpusadmit.CheckTrials(manifest.Cases, &corpusadmit.Ceilings{MaxHostCalls: config.WorkflowMaxHostCalls,
		MaxTokens: config.WorkflowMaxTokens, DurationSeconds: config.WorkflowDurationSeconds,
		MaxPreEffectRetries: config.WorkflowMaxPreEffectRetries, ModelTokensPerCall: productionProviderCompletionCap}); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPiServiceCorpus, err)
	}
	if err := corpusadmit.CheckAttacker(manifest, attackerArtifacts(), config.WorkflowMaxPreEffectRetries); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPiServiceCorpus, err)
	}
	if manifest.Pins.AttackerDigest != "" {
		spec, err := corpusadmit.LoadAttacker(attackerArtifacts(), manifest.Pins.AttackerDigest)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrPiServiceCorpus, err)
		}
		corpus.attacker, corpus.attackerDigest = &spec, manifest.Pins.AttackerDigest
	}
	for _, c := range manifest.Cases {
		corpus.trials[c.CaseID] = *c.Trial
		corpus.cases[c.CaseID] = corpusCase{kind: c.Kind, variance: c.Adversarial != nil && c.Adversarial.VarianceSubset,
			pOnly: profile == piruntime.D09ManagedPiCorpusQuotaProfilePilot && c.Adversarial != nil && c.Adversarial.ThreatClass == evalcorpus.ClassTMC}
		if digest, ok := corpusadmit.FixtureDigest(c); ok {
			corpus.fixtures[c.CaseID] = digest
		}
	}
	return corpus, nil
}

// workflowEnvelope is what one admission is allowed to run: its one-use id, the
// host-approved prompt and the budgets. Single-task modes take it from the config and
// corpus mode takes it from the pinned manifest. A client request only selects.
type workflowEnvelope struct {
	AdmissionID          string
	Prompt               string
	MaxHostCalls         uint64
	MaxTokens            uint64
	DurationSeconds      uint64
	CorpusManifestDigest string
	CorpusQuotaProfile   string
}

// envelopeFor resolves the envelope that request selects, or reports that it selects
// none. The client never supplies a prompt or a budget in corpus mode.
func (c *piServiceWorkflowCatalog) envelopeFor(config piServiceConfig, request piServiceRequest) (workflowEnvelope, bool) {
	if config.WorkflowAdmissionMode != piServiceAdmissionCorpus {
		if !workflowEnvelopeMatchesRequest(config, request) {
			return workflowEnvelope{}, false
		}
		return workflowEnvelope{AdmissionID: config.WorkflowAdmissionID, Prompt: config.WorkflowPrompt,
			MaxHostCalls: config.WorkflowMaxHostCalls, MaxTokens: config.WorkflowMaxTokens,
			DurationSeconds: config.WorkflowDurationSeconds}, true
	}
	if c == nil || c.corpus == nil || config.CorpusManifestDigest != c.corpus.digest || config.effectiveCorpusQuotaProfile() != c.corpus.quotaProfile ||
		request.Prompt != "" || (request.Attempt != 1 && request.Attempt != 2) || !c.corpus.roleServable(request.TaskID, request.Role) ||
		!reflect.DeepEqual(admissionRequestBashPaths(request), config.WorkflowBashOutputPaths) {
		return workflowEnvelope{}, false
	}
	trial, ok := c.corpus.trial(request.TaskID)
	if !ok {
		return workflowEnvelope{}, false
	}
	duration, ok := c.attemptDurationSeconds(trial, request)
	if !ok {
		return workflowEnvelope{}, false
	}
	return workflowEnvelope{AdmissionID: corpusAdmissionID(request.TaskID, request.Role, request.Attempt), Prompt: trial.Prompt,
		MaxHostCalls: trial.MaxHostCalls, MaxTokens: trial.MaxTokens, DurationSeconds: duration,
		CorpusManifestDigest: c.corpus.digest, CorpusQuotaProfile: c.corpus.quotaProfile}, true
}

// admissionScreen returns the pre-journal screen of the service: it refuses a request
// that the catalog could never admit, so that the refusal does not consume the request
// id the way an accepted-then-unknown task does. A request that the screen passes is
// still re-resolved by ResolveAdmission.
func (c *piServiceWorkflowCatalog) admissionScreen(config piServiceConfig) func(piServicePeerCredentials, piServiceRequest) string {
	return func(peer piServicePeerCredentials, request piServiceRequest) string {
		return c.screenRequest(config, peer, request)
	}
}

func (c *piServiceWorkflowCatalog) screenRequest(config piServiceConfig, peer piServicePeerCredentials, request piServiceRequest) string {
	if config.WorkflowAdmissionMode != piServiceAdmissionCorpus {
		if request.Attempt != 0 || request.Role != "" {
			return "attempt-not-accepted"
		}
		return ""
	}
	switch {
	case request.Prompt != "":
		return "prompt-not-accepted"
	case request.Attempt == 0:
		return "attempt-required"
	case request.Attempt != 1 && request.Attempt != 2:
		return "invalid-attempt"
	case request.Role == "":
		return "role-required"
	case !validCorpusRole(request.Role):
		return "invalid-role"
	case request.Role == corpusRoleV && request.Attempt != 1:
		return "invalid-attempt"
	case request.Role == corpusRoleV && config.ProviderRoute == piruntime.D09ManagedPiProviderRouteEngineering:
		return "role-unavailable"
	}
	if c == nil || c.corpus == nil {
		return "corpus-unavailable"
	}
	if _, known := c.corpus.trial(request.TaskID); !known {
		return "unknown-case"
	}
	if !c.corpus.roleAllowed(request.TaskID, request.Role) {
		return "role-kind-mismatch"
	}
	if !c.corpus.roleServable(request.TaskID, request.Role) {
		return "role-unavailable"
	}
	if _, ok := c.envelopeFor(config, request); !ok {
		return "invalid-request"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if hash, used := c.byHostAdmissionID[corpusAdmissionID(request.TaskID, request.Role, request.Attempt)]; used {
		// The admission is single-use. Only the exact request that holds it (a retry
		// or a recovery) passes; any other request id is refused unjournaled.
		if entry := c.entries[hash]; entry.RequestID != request.RequestID || entry.PeerUID != peer.UID {
			return "admission-already-used"
		}
	}
	return ""
}

// terminalReplayCheck returns the check the service runs before it returns a stored
// terminal response. A response recorded under one manifest, service config or
// registration must not be answered by a service bound to another: the identical
// request would otherwise return the old receipt before ResolveAdmission checks
// anything. The check compares the catalog entry the request reserved with the active
// config digest, registration identity and the envelope the request selects now.
func (c *piServiceWorkflowCatalog) terminalReplayCheck(config piServiceConfig, configDigest,
	registrationID string) func(piServicePeerCredentials, piServiceRequest, piServiceRequestState) string {
	return func(peer piServicePeerCredentials, request piServiceRequest, state piServiceRequestState) string {
		return c.screenTerminalReplay(config, configDigest, registrationID, request, state)
	}
}

const replayBindingMismatch = "replay-binding-mismatch"

func (c *piServiceWorkflowCatalog) screenTerminalReplay(config piServiceConfig, configDigest, registrationID string,
	request piServiceRequest, state piServiceRequestState) string {
	if c == nil || state.terminal == nil {
		return replayBindingMismatch
	}
	c.mu.Lock()
	entry, reserved := c.entries[state.acceptedRecord.Hash]
	c.mu.Unlock()
	if !reserved {
		// Accepted but never reserved: no workflow ran, so a stored unknown terminal (a
		// failed resolution) has no catalog entry to compare. A completed one must have one.
		// The unknown one is bound by what the accepted record itself persisted: the service
		// config digest, the registration identity and the admission envelope that were
		// active at acceptance. An old record that persisted none of them cannot be shown
		// to belong to this service, so it is refused.
		if state.terminal.Status == "completed" {
			return replayBindingMismatch
		}
		accepted := state.accepted
		if !validDigestText(accepted.ConfigDigest) || !validDigestText(accepted.RegistrationID) ||
			!validDigestText(accepted.EnvelopeDigest) || accepted.ConfigDigest != configDigest ||
			accepted.RegistrationID != registrationID || accepted.EnvelopeDigest != c.envelopeBindingDigest(config, request) {
			return replayBindingMismatch
		}
		return ""
	}
	envelope, ok := c.envelopeFor(config, request)
	if !ok || entry.ServiceConfigDigest != configDigest || entry.ServiceRegistrationID != registrationID ||
		entry.HostAdmissionID != envelope.AdmissionID || entry.CorpusManifestDigest != envelope.CorpusManifestDigest ||
		entry.CorpusQuotaProfile != envelope.CorpusQuotaProfile ||
		entry.TaskPrompt != envelope.Prompt || entry.Limits.MaxHostCalls != envelope.MaxHostCalls ||
		entry.Limits.MaxTokens != envelope.MaxTokens ||
		entry.Limits.Deadline.Sub(entry.ReservedAt) != time.Duration(envelope.DurationSeconds)*time.Second {
		return replayBindingMismatch
	}
	return ""
}

// envelopeBindingDigest commits to the admission envelope that request selects under config:
// the one-use admission id, the host prompt, the budgets and the pinned corpus manifest. A
// request that selects no envelope has its own constant digest, so a request that was not
// admissible when it was accepted is not replayed to a config under which it would be.
func (c *piServiceWorkflowCatalog) envelopeBindingDigest(config piServiceConfig, request piServiceRequest) string {
	envelope, ok := c.envelopeFor(config, request)
	if !ok {
		return digestBytes([]byte("tbound-pi-service-envelope/none"))
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return digestBytes([]byte("tbound-pi-service-envelope/unencodable"))
	}
	return digestBytes(append([]byte("tbound-pi-service-envelope/v1\x00"), encoded...))
}

// acceptanceBinding returns the binding begin persists in the accepted record, so that the
// replay of a terminal can be checked against it whether or not a workflow was reserved.
func (c *piServiceWorkflowCatalog) acceptanceBinding(config piServiceConfig, configDigest,
	registrationID string) func(piServicePeerCredentials, piServiceRequest) piServiceAcceptanceBinding {
	return func(_ piServicePeerCredentials, request piServiceRequest) piServiceAcceptanceBinding {
		return piServiceAcceptanceBinding{ConfigDigest: configDigest, RegistrationID: registrationID,
			EnvelopeDigest: c.envelopeBindingDigest(config, request)}
	}
}
