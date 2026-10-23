package runner

import (
	"fmt"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

func exportCDH(s Schedule, j *Journal, opt ExportOptions) (ExportResult, error) {
	res := ExportResult{Input: evalscore.Input{Schema: evalscore.SchemaVersion, Adversarial: []evalscore.AdversarialRecord{}, Compliant: []evalscore.CompliantRecord{}}, Incomplete: []string{}, Partial: []string{}}
	if opt.CDHBaseline != nil {
		res.Input = *opt.CDHBaseline
	}
	if res.Input.CDH != nil {
		return res, fmt.Errorf("runner: C-DH baseline must be static C")
	}
	digest, err := s.Config.CDH.Manifest.Digest()
	if err != nil {
		return res, err
	}
	res.Input.CDH = &evalscore.CDHInput{ManifestDigest: digest, Choosers: append([]string{}, s.Config.CDH.Manifest.Models...), LiveAdversarialCases: append([]string{}, s.Config.CDH.LiveAdversarialCases...), Trials: []evalscore.CDHTrial{}}
	for _, st := range s.Steps {
		if _, ok := s.Config.cdhSlot(st.Configuration); !ok {
			return res, fmt.Errorf("runner: foreign configuration in C-DH plan")
		}
		for attempt := 1; attempt <= 2; attempt++ {
			e := j.terminalOf(st.ID(), attempt)
			if e == nil {
				if attempt == 1 || j.begunOf(st.ID(), attempt) {
					res.Incomplete = append(res.Incomplete, fmt.Sprintf("%s#%d", st.ID(), attempt))
				}
				continue
			}
			t := evalscore.CDHTrial{CaseID: st.CaseID, Configuration: string(st.Configuration), Attempt: attempt, Kind: string(st.Kind), ManifestDigest: digest, Effect: evalscore.CEffectUnknown, EvidenceLoss: evalscore.LossUnclassified, Proposals: []evalscore.ProposalFact{}, Hints: []evalscore.HintFact{}}
			t.Attribution = opt.Attributions[st.CaseID]
			if o := e.Outcome; o != nil {
				if !oracleVersionOK(o) {
					return res, ErrOracleVersion
				}
				t.IdentityVerified = o.E01.Verified
				t.EvidenceLoss, t.OutageEvidenceDigest = mergeOutcomes(o)
				t.Complete = !o.Partial && o.Route.Complete && (o.Task == TaskPass || o.Task == TaskFail)
				if st.Kind == evalcorpus.KindAdversarial {
					t.Complete = intervalComplete(o, s.Config.requiredIn(o, st.ThreatClass, st.Predicate))
					if anyObserved(o) {
						t.Effect = evalscore.CEffectObserved
					} else if t.Complete {
						t.Effect = evalscore.CEffectNone
					}
				}
				t.Success = !o.Partial && o.Task == TaskPass
				if o.Receipt != nil {
					if o.Receipt.HintManifestDigest != digest || o.Receipt.HintChooser != string(st.Configuration)[len("C-DH/"):] {
						return res, fmt.Errorf("runner: C-DH receipt treatment mismatch")
					}
					t.Proposals = append(t.Proposals, o.Receipt.Proposals...)
					t.Hints = append(t.Hints, o.Receipt.Hints...)
				} else {
					t.IdentityVerified = false
					t.Complete = false
				}
			}
			res.Input.CDH.Trials = append(res.Input.CDH.Trials, t)
		}
	}
	return res, nil
}
