package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
)

// AcceptExecutor sets Outcome.Task of a compliant B or C step from the guest's task oracle
// (TASK-COMPLETE-01). After the inner executor has run the governed trial, it asks the guest
// `tbound trial accept <case_id> --tree live`, which evaluates the case's pinned acceptance
// artifact against the post-run live root with the fixture as the baseline, and maps the
// result document:
//
//   - every check passed (and the document is internally consistent): TaskPass;
//   - a check failed: TaskFail;
//   - anything else, that is the transport failing, a non-zero exit, output that is not exactly
//     one tbound-accept-result/v1 document for this case, a document whose passed member
//     disagrees with its results or lacks a passed boolean (a missing false must not become a
//     negative outcome), one not captured by the isolated acceptance cell route, or one whose
//     producer identity or spec digest differs from what the host expects (FreezeCommit, Expect):
//     TaskLost. A failed accept call is never a pass; it is missing evidence (the compliant record
//     is then incomplete, see AssembleCompliant). These are the checks the construct importer applies
//     to the same document.
//
// The document is retained in EvidenceDir, named by the interval (RetainedWSName), and its digest is
// Outcome.TaskReport, so the journaled task result can be recomputed with TaskOf.
//
// Nothing the inner executor reported about the task is kept: Task comes only from the guest.
// Steps that have no acceptance artifact (V, P, ambiguous and adversarial cases) pass through
// untouched; V brings its own acceptance result (GovernedV). A run that returned an error is
// not judged: its partial outcome and error stay as the inner executor gave them.
//
// Put it between Attest and the reset wrapper, so that the order on the guest is reset, the
// governed run, accept: AcceptExecutor{Inner: ResetExecutor{Inner: governed, ...}, ...}. The
// Transport is the trial transport (DefaultTrialCommand).
type AcceptExecutor struct {
	FreezeCommit string
	Inner        Executor
	Transport    Transport
	// Expect is the producer identity and spec digest a result must carry. An empty member is not checked.
	Expect AcceptExpect
	// EvidenceDir, if set, receives the guest's result document of every judged step.
	EvidenceDir string
}

// AcceptExpect is what the host pins for a task result: the corpus manifest digest, the guest tbound binary digest
// (pins.runner_digest), the acceptance cell image digest (v_image_digest), and per case the digest of the pinned
// acceptance artifact.
type AcceptExpect struct {
	ManifestDigest, BinarySHA256, ImageDigest string
	SpecDigest                                func(caseID string) string
}

// applies reports whether the step has an acceptance artifact to evaluate.
func (AcceptExecutor) applies(s Step) bool {
	return s.Kind == evalcorpus.KindCompliant && (s.Configuration == CfgB || s.Configuration == CfgC || s.Configuration.IsCDH())
}

// Prepare implements Executor. It makes no guest call.
func (a AcceptExecutor) Prepare(ctx context.Context, s Step) error {
	if a.Inner == nil || a.Transport == nil {
		return ErrNotWired
	}
	if err := a.Inner.Prepare(ctx, s); err != nil {
		return err
	}
	if err := (ResetExecutor{Transport: a.Transport}).Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrSetupRefused, err)
	}
	return nil
}

// Execute implements Executor.
func (a AcceptExecutor) Execute(ctx context.Context, s Step) (Outcome, error) {
	if a.Inner == nil || a.Transport == nil {
		return Outcome{}, ErrNotWired
	}
	out, err := a.Inner.Execute(ctx, s)
	if err != nil || !a.applies(s) {
		return out, err
	}
	out.Task, out.TaskReport = a.judge(ctx, s)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, nil
}

func (a AcceptExecutor) judge(ctx context.Context, s Step) (TaskResult, string) {
	args := []string{"accept", s.CaseID, "--tree", "live"}
	if a.FreezeCommit != "" {
		args = append(args, "--commit", a.FreezeCommit)
	}
	stdout, _, exit, err := a.Transport.Run(ctx, args)
	if err != nil || exit != 0 {
		return TaskLost, ""
	}
	if a.EvidenceDir != "" {
		if err := retainFile(a.EvidenceDir, RetainedWSName(IntervalRef(s)), stdout, true); err != nil {
			return TaskLost, ""
		}
	}
	return a.TaskOf(stdout, s.CaseID), taskaccept.DigestOf(stdout)
}

// TaskOf maps the guest's result document for caseID to a task result, with every check named in AcceptExecutor.
func (a AcceptExecutor) TaskOf(raw []byte, caseID string) TaskResult {
	doc, err := ParseAcceptDoc(raw)
	if err != nil || RequireAcceptBools(raw) != nil || doc.Route != taskaccept.RouteTrialAcceptCell {
		return TaskLost
	}
	e := a.Expect
	spec := ""
	if e.SpecDigest != nil {
		spec = e.SpecDigest(caseID)
	}
	for _, p := range [][2]string{{a.FreezeCommit, doc.Commit}, {e.ManifestDigest, doc.ManifestDigest}, {e.BinarySHA256, doc.BinarySHA256},
		{e.ImageDigest, doc.ImageDigest}, {spec, doc.SpecDigest}} {
		if p[0] != "" && p[0] != p[1] {
			return TaskLost
		}
	}
	return taskOfAccept(&doc, caseID)
}

// RequireAcceptBools refuses a result document whose passed members are missing: a missing false must not become a
// negative outcome.
func RequireAcceptBools(raw []byte) error {
	var doc struct {
		Passed  *bool `json:"passed"`
		Results []struct {
			Passed *bool `json:"passed"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if doc.Passed == nil {
		return errors.New("missing boolean passed")
	}
	for _, r := range doc.Results {
		if r.Passed == nil {
			return errors.New("missing boolean results.passed")
		}
	}
	return nil
}

// ParseAcceptDoc reads exactly one result document and requires end of input:
// trailing whitespace is fine, anything else is data after the document.
func ParseAcceptDoc(raw []byte) (taskaccept.AcceptDoc, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var doc taskaccept.AcceptDoc
	if err := decoder.Decode(&doc); err != nil {
		return taskaccept.AcceptDoc{}, err
	}
	// Decoder.More reports whether another array or object element follows; it
	// is false for any other junk (for example "] garbage"), so it does not
	// establish end of input. Decode again and require io.EOF.
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == nil {
		return taskaccept.AcceptDoc{}, errors.New("data after the accept document")
	} else if !errors.Is(err, io.EOF) {
		return taskaccept.AcceptDoc{}, fmt.Errorf("data after the accept document: %w", err)
	}
	return doc, nil
}

// taskOfAccept maps a guest result document for caseID to a task result. A nil document, a
// document of another schema or case, one without checks, and one whose passed member is not
// the conjunction of its results are TaskLost: they carry no verdict.
func taskOfAccept(doc *taskaccept.AcceptDoc, caseID string) TaskResult {
	if doc == nil || doc.Schema != taskaccept.ResultSchema || doc.CaseID != caseID || len(doc.Results) == 0 {
		return TaskLost
	}
	all := true
	for _, r := range doc.Results {
		all = all && r.Passed
	}
	if all != doc.Passed {
		return TaskLost
	}
	if doc.Passed {
		return TaskPass
	}
	return TaskFail
}
