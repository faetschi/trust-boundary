package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/vrun"
)

// ErrVNoModelResponse means a V run exited non-zero and Pi reported no completed model
// message: the provider was not reached, so the run says nothing about the task. The outcome
// comes with it (and so is partial); the unit is spent.
var ErrVNoModelResponse = errors.New("runner: the V run ended without a model response")

// GovernedV is the guest-backed executor of Configuration V, the native Pi utility baseline
// (descriptive; it is not a security configuration and has no oracle).
//
// The mapping:
//
//   - one V step is one `tbound trial v-run <case_id>` over the trial transport. The guest
//     runs the pinned native Pi once in a plain Podman container on a fresh copy of the case
//     fixture and prints one tbound-v-run/v1 document (vrun.Doc); V resets nothing on the live
//     root and its run leaves no trace there;
//   - Task is the guest's acceptance result on the run's workspace: pass only if the document
//     carries an accept result that is consistent and passed. A missing or inconsistent result
//     is TaskLost, never a pass, and a timed-out or failed run is judged on what it left;
//   - Metrics come from the document: latency is the guest's finished minus started, tokens and
//     cost are Pi's own usage numbers (cost from Pi's price table), tool calls the finished tool
//     executions. The gate latency is zero (V has no gate);
//   - E01.Digest is the SHA-256 of the canonical encoding of the document together with its
//     image digest, so a changed run, image or fixture changes the identity. E01.Verified is
//     true only when ImageDigest is pinned and equals the document's, and, if FixtureDigest is
//     set, the case's pinned fixture digest equals the document's. Without the pin it stays
//     false: V has no receipt for Attest to compare;
//   - Route is the route coverage the host configured for the run, as Attest sets it.
//
// A document of another schema or case, an exit code of the command beside no document, and a
// refusal by the guest are setup failures (ErrSetupRefused or a malformed-output error) with an
// empty outcome. An exit of Pi other than zero or a timeout is data, except that a run which
// never got a model response is ErrVNoModelResponse.
type GovernedV struct {
	Transport Transport
	// ImageDigest is the pinned cell image digest (the registered template's expected digest).
	ImageDigest string
	// FixtureDigest returns the pinned workspace fixture digest of a case, if known.
	FixtureDigest func(caseID string) (digest string, ok bool)
	// Route is the route coverage of the run.
	Route RouteCoverage
}

// ErrVMalformed means the guest's v-run output is not exactly one document for this case.
var ErrVMalformed = errors.New("runner: v-run output is malformed")

// Prepare implements Executor. It makes no guest call.
func (g GovernedV) Prepare(_ context.Context, s Step) error {
	if g.Transport == nil {
		return ErrNotWired
	}
	if s.Configuration != CfgV || s.Kind != evalcorpus.KindCompliant {
		return fmt.Errorf("%w: V runs compliant tasks only, step %s is %s %s", ErrSetupRefused, s.ID(), s.Configuration, s.Kind)
	}
	if err := (ResetExecutor{Transport: g.Transport}).Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrSetupRefused, err)
	}
	return nil
}

// Execute implements Executor.
func (g GovernedV) Execute(ctx context.Context, s Step) (Outcome, error) {
	if g.Transport == nil {
		return Outcome{}, ErrNotWired
	}
	stdout, stderr, exit, err := g.Transport.Run(ctx, []string{"v-run", s.CaseID})
	switch {
	case err != nil:
		return Outcome{}, fmt.Errorf("%w: v-run of %s: %v", ErrSetupRefused, s.CaseID, err)
	case exit != 0:
		return Outcome{}, fmt.Errorf("%w: v-run of %s exit %d: %s", ErrSetupRefused, s.CaseID, exit, clip(stderr))
	}
	doc, err := vrun.Parse(stdout)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrVMalformed, err)
	}
	if doc.CaseID != s.CaseID {
		return Outcome{}, fmt.Errorf("%w: document is for case %q, step is %q", ErrVMalformed, doc.CaseID, s.CaseID)
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		return Outcome{}, err
	}
	sum := sha256.Sum256(append(append(append([]byte("tbound-v-e01/v1\x00"), canonical...), 0), doc.ImageDigest...))
	out := Outcome{
		E01:     E01Identity{Digest: "sha256:" + hex.EncodeToString(sum[:]), Verified: g.verified(s.CaseID, doc)},
		Oracles: map[string]Finding{},
		Task:    taskOfAccept(doc.Accept, s.CaseID),
		Route:   g.Route,
		Metrics: Metrics{
			LatencyMS: int((doc.FinishedNS - doc.StartedNS) / 1_000_000),
			InTokens:  int(doc.TokensIn), OutTokens: int(doc.TokensOut), CostMicroUSD: doc.CostMicroUSD, ToolCalls: doc.ToolCalls,
		},
	}
	if doc.Exit != 0 && !doc.TimedOut && doc.UsageEvents == 0 {
		return out, fmt.Errorf("%w: exit %d", ErrVNoModelResponse, doc.Exit)
	}
	return out, nil
}

func (g GovernedV) verified(caseID string, doc vrun.Doc) bool {
	if g.ImageDigest == "" || doc.ImageDigest != g.ImageDigest {
		return false
	}
	if g.FixtureDigest != nil {
		want, ok := g.FixtureDigest(caseID)
		return ok && want == doc.FixtureDigest
	}
	return true
}
