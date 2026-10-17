//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
)

// ErrPiServiceCorpus means the corpus manifest of a corpus-mode service is missing,
// unreadable, not the pinned one, or not admissible.
var ErrPiServiceCorpus = errors.New("corpus manifest is missing, not the pinned manifest, or not admissible")

// Test seams. Production reads the fixed root-owned manifest through the strict
// no-symlink ancestor walk and validates it against the registered thesis quotas.
var (
	readCorpusManifest = piruntime.ReadD09ManagedPiCorpusManifest
	corpusQuotas       = evalcorpus.ThesisQuotas
)

// The closed set of governed roles. Each names one planned governed C trial of a case
// and applies to one manifest case kind; the names are the runner's own (internal/runner
// GovernedRoles). Ambiguous cases also run C in the plan, so their role is admitted too.
const (
	corpusRoleTrajectory     = "trajectory"
	corpusRoleStaticReplay   = "static-replay"
	corpusRoleAdaptiveReplay = "adaptive-replay"
	corpusRoleVariance1      = "variance-1"
	corpusRoleVariance2      = "variance-2"
	corpusRoleCompliant      = "compliant"
	corpusRoleAmbiguous      = "ambiguous"
)

func validCorpusRole(role string) bool {
	switch role {
	case corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleAdaptiveReplay, corpusRoleVariance1,
		corpusRoleVariance2, corpusRoleCompliant, corpusRoleAmbiguous:
		return true
	}
	return false
}

// corpusCase is what the service keeps of a manifest case besides its trial: the kind
// and the variance-subset membership that decide which roles the case may run.
type corpusCase struct {
	kind     evalcorpus.Kind
	variance bool
}

// productionCorpus is the frozen, digest-pinned set of trials a corpus-mode service
// admits. It is built once at service start and is read-only afterwards.
type productionCorpus struct {
	digest string
	trials map[string]evalcorpus.Trial
	cases  map[string]corpusCase
}

// corpusAdmissionID is the one-use admission id of a trial: <case_id>/<role>/<attempt>.
func corpusAdmissionID(caseID, role string, attempt int) string {
	return caseID + "/" + role + "/" + strconv.Itoa(attempt)
}

// roleAllowed reports whether role may run for caseID: trajectory and the replay roles
// only for adversarial cases, the variance roles only for adversarial cases in the
// variance subset, compliant only for compliant tasks, ambiguous only for ambiguous
// cases.
func (c *productionCorpus) roleAllowed(caseID, role string) bool {
	if c == nil {
		return false
	}
	info, ok := c.cases[caseID]
	if !ok {
		return false
	}
	switch role {
	case corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleAdaptiveReplay:
		return info.kind == evalcorpus.KindAdversarial
	case corpusRoleVariance1, corpusRoleVariance2:
		return info.kind == evalcorpus.KindAdversarial && info.variance
	case corpusRoleCompliant:
		return info.kind == evalcorpus.KindCompliant
	case corpusRoleAmbiguous:
		return info.kind == evalcorpus.KindAmbiguous
	}
	return false
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
	if violations := evalcorpus.Validate(manifest, corpusQuotas()); len(violations) != 0 {
		return nil, fmt.Errorf("%w: evalcorpus.Validate: %d violation(s), first %s: %s",
			ErrPiServiceCorpus, len(violations), violations[0].Rule, violations[0].Detail)
	}
	corpus := &productionCorpus{digest: digest, trials: make(map[string]evalcorpus.Trial, len(manifest.Cases)),
		cases: make(map[string]corpusCase, len(manifest.Cases))}
	for _, c := range manifest.Cases {
		if c.Trial == nil {
			return nil, fmt.Errorf("%w: case %q has no trial", ErrPiServiceCorpus, c.CaseID)
		}
		if !boundedProductionIdentity(c.CaseID, productionTaskIDMaxBytes) ||
			!boundedProductionIdentity(corpusAdmissionID(c.CaseID, corpusRoleAdaptiveReplay, 2), productionRequestMaxBytes) ||
			!boundedProductionPrompt(c.Trial.Prompt) {
			return nil, fmt.Errorf("%w: case %q is not a bounded task identity or prompt", ErrPiServiceCorpus, c.CaseID)
		}
		if c.Trial.MaxHostCalls > config.WorkflowMaxHostCalls || c.Trial.MaxTokens > config.WorkflowMaxTokens ||
			c.Trial.DurationSeconds > config.WorkflowDurationSeconds {
			return nil, fmt.Errorf("%w: case %q budget exceeds the host ceilings of the signed config", ErrPiServiceCorpus, c.CaseID)
		}
		corpus.trials[c.CaseID] = *c.Trial
		corpus.cases[c.CaseID] = corpusCase{kind: c.Kind, variance: c.Adversarial != nil && c.Adversarial.VarianceSubset}
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
	if c == nil || c.corpus == nil || config.CorpusManifestDigest != c.corpus.digest ||
		request.Prompt != "" || (request.Attempt != 1 && request.Attempt != 2) || !c.corpus.roleAllowed(request.TaskID, request.Role) ||
		!reflect.DeepEqual(admissionRequestBashPaths(request), config.WorkflowBashOutputPaths) {
		return workflowEnvelope{}, false
	}
	trial, ok := c.corpus.trial(request.TaskID)
	if !ok {
		return workflowEnvelope{}, false
	}
	return workflowEnvelope{AdmissionID: corpusAdmissionID(request.TaskID, request.Role, request.Attempt), Prompt: trial.Prompt,
		MaxHostCalls: trial.MaxHostCalls, MaxTokens: trial.MaxTokens, DurationSeconds: trial.DurationSeconds,
		CorpusManifestDigest: c.corpus.digest}, true
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
