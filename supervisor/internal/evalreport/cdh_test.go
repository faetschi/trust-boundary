package evalreport

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalscore"
)

func TestCDHReportSeparationAndPairing(t *testing.T) {
	in := smallInputs()
	base := mustBuild(t, in)
	d := &evalscore.CDHInput{ManifestDigest: "sha256:" + strings.Repeat("a", 64), Choosers: []string{"typesafe/jev-1.14", "synthetic/local"}, LiveAdversarialCases: []string{}}
	for i, model := range d.Choosers {
		for _, id := range []string{"C-1", "C-2"} {
			d.Trials = append(d.Trials, evalscore.CDHTrial{CaseID: id, Kind: "compliant", Configuration: "C-DH/" + model, Attempt: 1, ManifestDigest: d.ManifestDigest, IdentityVerified: true, Complete: true, Success: i == 0, EvidenceLoss: evalscore.LossNone, Proposals: []evalscore.ProposalFact{{Tool: "write", Verdict: "DENY"}, {Tool: "read", Verdict: "ALLOW"}}, Hints: []evalscore.HintFact{{CandidateID: "0", FallbackReason: "timeout", LatencyNS: 42}}})
		}
	}
	in.Trials.CDH = d
	r := mustBuild(t, in)
	if !reflect.DeepEqual(base.H1, r.H1) || !reflect.DeepEqual(base.Metrics, r.Metrics) || !reflect.DeepEqual(base.Coverage, r.Coverage) || base.PostDenialContinuation != r.PostDenialContinuation {
		t.Fatal("C-DH changed H1/C")
	}
	if len(r.CDH) != 2 || r.CDH[0].BothSuccess != 2 || r.CDH[1].COnlySuccess != 2 || r.CDH[0].PostDenial.AlternativeAllowed != 2 || r.CDH[1].Fallbacks != 2 || *r.CDH[0].MeanChooserLatencyNS != 42 {
		t.Fatalf("sections %+v", r.CDH)
	}
	if !strings.Contains(r.Markdown(), "C-DH/synthetic/local") {
		t.Fatal("missing chooser section")
	}
	slices.Reverse(d.Trials)
	reversed := mustBuild(t, in)
	if reversed.Digest != r.Digest || reversed.Markdown() != r.Markdown() {
		t.Fatal("report depends on trial order")
	}
	d.Trials[0].ManifestDigest = "sha256:" + strings.Repeat("b", 64)
	if _, err := Build(in); err == nil {
		t.Fatal("mixed treatment accepted")
	}
}

func TestCDHReportRefusesUnregisteredAndDuplicateTrials(t *testing.T) {
	for _, bad := range []evalscore.CDHTrial{
		{CaseID: "C-1", Kind: "compliant", Configuration: "C", Attempt: 1},
		{CaseID: "unknown", Kind: "compliant", Configuration: "C-DH/typesafe/jev-1.14", Attempt: 1},
		{CaseID: "C-1", Kind: "compliant", Configuration: "C-DH/typesafe/jev-1.14", Attempt: 2},
	} {
		in := smallInputs()
		bad.ManifestDigest = "sha256:" + strings.Repeat("a", 64)
		in.Trials.CDH = &evalscore.CDHInput{ManifestDigest: bad.ManifestDigest, Choosers: []string{"typesafe/jev-1.14", "synthetic/local"}, Trials: []evalscore.CDHTrial{bad}}
		if _, err := Build(in); err == nil {
			t.Fatal(bad)
		}
	}
}
