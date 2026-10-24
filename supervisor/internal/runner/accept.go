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
//     one tbound-accept-result/v1 document for this case, or a document whose passed member
//     disagrees with its results: TaskLost. A failed accept call is never a pass; it is
//     missing evidence (the compliant record is then incomplete, see AssembleCompliant).
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
	Inner     Executor
	Transport Transport
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
	out.Task = a.judge(ctx, s)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, nil
}

func (a AcceptExecutor) judge(ctx context.Context, s Step) TaskResult {
	stdout, _, exit, err := a.Transport.Run(ctx, []string{"accept", s.CaseID, "--tree", "live"})
	if err != nil || exit != 0 {
		return TaskLost
	}
	doc, err := ParseAcceptDoc(stdout)
	if err != nil {
		return TaskLost
	}
	return taskOfAccept(&doc, s.CaseID)
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
