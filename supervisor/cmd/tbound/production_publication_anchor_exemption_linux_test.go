//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/runner"
)

// These tests feed what the service produces, a receipt anchor and the interval journal derived
// from its publication journal, through the runner's own exemption decision. The runner's check
// (PublicationFree.verifies) is unexported, so the decision is observed where it counts: the
// coverage of an adversarial record assembled by Config.AssembleAdversarial, whose three C
// intervals carry no EFF-WS-05/06 report and are complete only through the exemption.

// exemptionTrial is what the service and the executor hold for one C interval.
type exemptionTrial struct {
	step    runner.Step
	receipt productionPiReceipt
	journal []byte
}

func (e exemptionTrial) evidence() *runner.PublicationFree {
	return &runner.PublicationFree{Journal: e.journal, JournalDigest: e.receipt.PublicationJournalHead,
		AnchorHead: e.receipt.PublicationJournalHead, AnchorCount: e.receipt.PublicationJournalRecordCount}
}

func exemptionStep(role runner.Role) runner.Step {
	return runner.Step{CaseID: anchorTestCase, Kind: evalcorpus.KindAdversarial, ThreatClass: evalcorpus.ClassTMA,
		Configuration: runner.CfgC, Role: role, Attempt: 1, Predicate: "EFF-WS-01", WitnessID: "w"}
}

// exemptionTrialFor runs the service's anchor derivation over a real publication journal of one
// workflow (with the given number of publication transactions in it) and takes the interval
// journal bytes from the same trace.
func exemptionTrialFor(t *testing.T, role runner.Role, publications int) exemptionTrial {
	t.Helper()
	step := exemptionStep(role)
	workflowID := "workflow-exempt-" + string(role)
	events := append(anchorNoise(workflowID), anchorOwnerEpoch(workflowID, 1))
	for i := 0; i < publications; i++ {
		events = append(events, anchorTransaction(workflowID, "pub-exempt-"+string(rune('a'+i)))...)
	}
	publicationJournal, err := audit.Open(filepath.Join(incrementalTestRoot(t), "publication-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer publicationJournal.Close()
	for _, event := range events {
		if _, err := publicationJournal.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	trace, err := publicationJournal.TraceCommitted()
	if err != nil {
		t.Fatal(err)
	}
	var receipt productionPiReceipt
	entry := anchorTestEntry(anchorTestCase + "/" + string(role) + "/1")
	entry.WorkflowID = workflowID
	admission := &productionWorkflowAdmission{Entry: entry}
	if err := applyPublicationAnchor(&receipt, admission, workflowID, trace); err != nil {
		t.Fatal(err)
	}
	if receipt.PublicationIntervalRef != runner.IntervalRef(step) {
		t.Fatalf("receipt interval %q is not the runner's %q", receipt.PublicationIntervalRef, runner.IntervalRef(step))
	}
	slice, _, err := workflowPublicationSlice(trace, workflowID)
	if err != nil {
		t.Fatal(err)
	}
	return exemptionTrial{step: step, receipt: receipt, journal: exemptionIntervalJournal(t, receipt.PublicationIntervalRef, slice)}
}

// exemptionIntervalJournal is the interval journal as a file of package audit: the runner's
// marker record, then the workflow's publication records.
func exemptionIntervalJournal(t *testing.T, interval string, slice []audit.Event) []byte {
	t.Helper()
	path := filepath.Join(incrementalTestRoot(t), "interval.jsonl")
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range append([]audit.Event{{Kind: publicationIntervalMarkerKind, ID: interval}}, slice...) {
		if _, err := journal.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

// exemptionRecord assembles the case as the runner does: the three C intervals carry
// evidence[i] and no WS-05/06 report, and the two P intervals are the fake executor's.
func exemptionRecord(t *testing.T, trials [3]exemptionTrial, evidence [3]*runner.PublicationFree) evalscore.AdversarialRecord {
	t.Helper()
	cfg := runner.Config{FreezeCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EVQObligations: []string{"EVQ-1"}}
	executor := runner.NewFakeExecutor(cfg)
	execute := func(step runner.Step) *runner.Outcome {
		outcome, err := executor.Execute(context.Background(), step)
		if err != nil {
			t.Fatal(err)
		}
		return &outcome
	}
	view := runner.AdvView{CaseID: anchorTestCase, Attempt: 1, Class: evalcorpus.ClassTMA, Predicate: "EFF-WS-01", WitnessID: "w"}
	for i, target := range []**runner.Outcome{&view.Trajectory, &view.StaticC, &view.AdaptiveC} {
		*target = execute(trials[i].step)
		delete((*target).Oracles, runner.OracleWS05)
		delete((*target).Oracles, runner.OracleWS06)
		(*target).PublicationFree = evidence[i]
	}
	pStatic, pAdaptive := trials[1].step, trials[2].step
	pStatic.Configuration, pAdaptive.Configuration = runner.CfgP, runner.CfgP
	view.StaticP, view.AdaptiveP = execute(pStatic), execute(pAdaptive)
	return cfg.AssembleAdversarial(view).Record
}

func exemptionTrials(t *testing.T, publications int) [3]exemptionTrial {
	t.Helper()
	return [3]exemptionTrial{exemptionTrialFor(t, runner.RoleTrajectory, publications),
		exemptionTrialFor(t, runner.RoleStaticReplay, publications), exemptionTrialFor(t, runner.RoleAdaptiveReplay, publications)}
}

func exemptionEvidence(trials [3]exemptionTrial) [3]*runner.PublicationFree {
	return [3]*runner.PublicationFree{trials[0].evidence(), trials[1].evidence(), trials[2].evidence()}
}

func exemptionGranted(r evalscore.AdversarialRecord) bool {
	return r.ObserverCoverageComplete && r.CEffect == evalscore.CEffectNone && r.EvidenceLoss == evalscore.LossNone
}

func TestRunnerExemptsAServiceProducedZeroPublicationInterval(t *testing.T) {
	trials := exemptionTrials(t, 0)
	for _, trial := range trials {
		if trial.receipt.PublicationJournalRecordCount != 0 || trial.receipt.PublicationJournalHead == "" {
			t.Fatalf("not a zero-publication anchor: %+v", trial.receipt)
		}
	}
	// without the evidence the intervals are not complete: the exemption is what completes them
	if r := exemptionRecord(t, trials, [3]*runner.PublicationFree{}); exemptionGranted(r) {
		t.Fatalf("complete without any evidence: %+v", r)
	}
	if r := exemptionRecord(t, trials, exemptionEvidence(trials)); !exemptionGranted(r) {
		t.Fatalf("the runner refused the service's genuine zero-publication evidence: %+v", r)
	}
}

func TestRunnerRefusesWhatTheServiceAnchorDoesNotSupport(t *testing.T) {
	trials := exemptionTrials(t, 0)
	published := exemptionTrials(t, 1)

	refused := func(what string, evidence [3]*runner.PublicationFree) {
		t.Helper()
		if r := exemptionRecord(t, trials, evidence); exemptionGranted(r) {
			t.Fatalf("%s: the runner granted the exemption: %+v", what, r)
		}
	}
	// each defect is applied to one interval at a time: a single bad interval is enough
	for name, mutate := range map[string]func(e *runner.PublicationFree){
		"anchor head altered": func(e *runner.PublicationFree) { e.AnchorHead = "0" + e.AnchorHead[1:] },
		"anchor head altered with the digest": func(e *runner.PublicationFree) {
			e.AnchorHead = "0" + e.AnchorHead[1:]
			e.JournalDigest = e.AnchorHead
		},
		"anchor head removed":                    func(e *runner.PublicationFree) { e.AnchorHead = "" },
		"anchor count altered":                   func(e *runner.PublicationFree) { e.AnchorCount = 1 },
		"journal truncated at a record boundary": func(e *runner.PublicationFree) { e.Journal = e.Journal[:nthNewline(e.Journal, 1)] },
		"journal emptied":                        func(e *runner.PublicationFree) { e.Journal = nil },
	} {
		for i := range trials {
			evidence := exemptionEvidence(trials)
			changed := *evidence[i]
			mutate(&changed)
			evidence[i] = &changed
			refused(name, evidence)
		}
	}

	// the interval ref is wrong: the evidence of another interval, anchor and journal both genuine
	evidence := exemptionEvidence(trials)
	evidence[0] = trials[1].evidence()
	refused("evidence of another interval", evidence)

	// a journal whose marker names another interval, with an anchor that matches that journal
	other := exemptionIntervalJournal(t, trials[0].receipt.PublicationIntervalRef+"x", nil)
	trace, err := audit.Verify(bytes.NewReader(other))
	if err != nil {
		t.Fatal(err)
	}
	head := trace.Records[len(trace.Records)-1].Hash
	evidence = exemptionEvidence(trials)
	evidence[0] = &runner.PublicationFree{Journal: other, JournalDigest: head, AnchorHead: head}
	refused("marker of another interval ref", evidence)

	// a publication occurred: the anchor and the journal are genuine and say so
	for i, trial := range published {
		if trial.receipt.PublicationJournalRecordCount < 1 {
			t.Fatalf("interval %d: the publication was not counted: %+v", i, trial.receipt)
		}
		evidence = exemptionEvidence(trials)
		evidence[i] = trial.evidence()
		refused("an interval with a publication", evidence)
		// the anchor count understating the journal does not hide it: the runner counts itself
		understated := trial.evidence()
		understated.AnchorCount = 0
		evidence[i] = understated
		refused("a publication journal with an anchor count of zero", evidence)
	}

	// a publication hidden by cutting the journal before it, keeping the anchor of the full journal
	cut := published[0].evidence()
	cut.Journal = cut.Journal[:nthNewline(cut.Journal, 2)]
	evidence = exemptionEvidence(trials)
	evidence[0] = cut
	refused("journal cut before the publication", evidence)
}

// nthNewline is the offset just after the n-th newline.
func nthNewline(data []byte, n int) int {
	offset := 0
	for ; n > 0; n-- {
		offset += bytes.IndexByte(data[offset:], '\n') + 1
	}
	return offset
}
