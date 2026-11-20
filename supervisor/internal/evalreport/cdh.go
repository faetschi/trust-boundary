package evalreport

import (
	"fmt"
	"regexp"
	"slices"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

type CDHReport struct {
	Configuration           string                        `json:"configuration"`
	ManifestDigest          string                        `json:"manifest_digest"`
	RegisteredCompliant     int                           `json:"registered_compliant"`
	BothSuccess             int                           `json:"both_success"`
	BothFailure             int                           `json:"both_failure"`
	COnlySuccess            int                           `json:"c_only_success"`
	CDHOnlySuccess          int                           `json:"cdh_only_success"`
	UnresolvedPairs         int                           `json:"unresolved_pairs"`
	PairedSuccessDifference *float64                      `json:"paired_success_difference"`
	PostDenial              PostDenialCounts              `json:"post_denial"`
	HintSelections          int                           `json:"hint_selections"`
	Fallbacks               int                           `json:"fallbacks"`
	FallbackRate            *float64                      `json:"fallback_rate"`
	MeanChooserLatencyNS    *float64                      `json:"mean_chooser_latency_ns"`
	P95ChooserLatencyNS     *int64                        `json:"p95_chooser_latency_ns"`
	Dispositions            map[evalscore.Disposition]int `json:"dispositions"`
	Safety                  []CDHSafety                   `json:"safety"`
}
type CDHSafety struct {
	CaseID string            `json:"case_id"`
	Effect evalscore.CEffect `json:"effect"`
	Result evalscore.Result  `json:"result"`
}

var cdhDigestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func canonicalCDH(d *evalscore.CDHInput) *evalscore.CDHInput {
	if d == nil {
		return nil
	}
	c := *d
	c.LiveAdversarialCases = slices.Sorted(slices.Values(d.LiveAdversarialCases))
	c.Trials = slices.Clone(d.Trials)
	slices.SortFunc(c.Trials, func(a, b evalscore.CDHTrial) int {
		if a.Configuration != b.Configuration {
			if a.Configuration < b.Configuration {
				return -1
			}
			return 1
		}
		if a.CaseID != b.CaseID {
			if a.CaseID < b.CaseID {
				return -1
			}
			return 1
		}
		return a.Attempt - b.Attempt
	})
	return &c
}

func cdhOf(in Inputs, idx *manifestIndex) ([]CDHReport, error) {
	d := in.Trials.CDH
	if d == nil {
		return nil, nil
	}
	if !cdhDigestRE.MatchString(d.ManifestDigest) || len(d.Choosers) != 2 || d.Choosers[0] != "typesafe/jev-1.14" || d.Choosers[1] == "" || d.Choosers[0] == d.Choosers[1] {
		return nil, fmt.Errorf("evalreport: invalid C-DH manifest identity")
	}
	safety := map[string]bool{}
	for _, id := range d.LiveAdversarialCases {
		if safety[id] {
			return nil, fmt.Errorf("evalreport: duplicate C-DH safety case %q", id)
		}
		found := false
		for _, c := range in.Manifest.Cases {
			if c.CaseID == id && c.Kind == evalcorpus.KindAdversarial && c.Trial != nil && len(c.Trial.Pivots) == 0 {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("evalreport: C-DH refuses unregistered or D32 safety case %q", id)
		}
		safety[id] = true
	}
	groups := map[string]map[string][]evalscore.CDHTrial{}
	for _, model := range d.Choosers {
		groups["C-DH/"+model] = map[string][]evalscore.CDHTrial{}
	}
	for _, t := range d.Trials {
		g, ok := groups[t.Configuration]
		_, compliant := idx.stratumOf[kindCompliant][t.CaseID]
		if !ok || t.ManifestDigest != d.ManifestDigest || (t.Kind == kindCompliant && !compliant) || (t.Kind == kindAdversarial && !safety[t.CaseID]) || (t.Kind != kindCompliant && t.Kind != kindAdversarial) || t.Attempt < 1 || t.Attempt > 2 {
			return nil, fmt.Errorf("evalreport: unregistered C-DH trial %q/%s", t.CaseID, t.Configuration)
		}
		for _, h := range t.Hints {
			if h.LatencyNS < 0 || (h.CandidateID != "0" && h.CandidateID != "1") || (h.FallbackReason != "" && h.CandidateID != "0") {
				return nil, fmt.Errorf("evalreport: invalid C-DH hint")
			}
		}
		g[t.CaseID] = append(g[t.CaseID], t)
	}
	baseline := map[string]evalscore.CompliantRecord{}
	for _, r := range in.Trials.Compliant {
		if r.Attempt > baseline[r.TaskID].Attempt {
			baseline[r.TaskID] = r
		}
	}
	advBaseline := map[string]evalscore.AdversarialRecord{}
	for _, r := range in.Trials.Adversarial {
		if r.Attempt > advBaseline[r.CaseID].Attempt {
			advBaseline[r.CaseID] = r
		}
	}
	var out []CDHReport
	for _, model := range d.Choosers {
		r := CDHReport{Configuration: "C-DH/" + model, ManifestDigest: d.ManifestDigest, RegisteredCompliant: len(idx.distinct[kindCompliant]), Dispositions: map[evalscore.Disposition]int{}, Safety: []CDHSafety{}}
		latest := map[string]evalscore.CDHTrial{}
		var post []evalscore.PostDenialTrial
		var latencies []int64
		for id, trials := range groups[r.Configuration] {
			slices.SortFunc(trials, func(a, b evalscore.CDHTrial) int { return a.Attempt - b.Attempt })
			// C-DH is descriptive and keeps the outage-only rerun: GovernedC types no UNKNOWN of a C-DH step as a
			// rerun cause (rerunScope), so outcome_unknown never reaches it.
			if len(trials) > 2 || trials[0].Attempt != 1 || (len(trials) == 2 && (trials[1].Attempt != 2 || trials[0].EvidenceLoss != evalscore.LossExternalOutage || trials[0].OutageEvidenceDigest == "" || !trials[0].IdentityVerified || trials[0].Effect == evalscore.CEffectObserved)) {
				return nil, fmt.Errorf("evalreport: invalid C-DH rerun for %q", id)
			}
			latest[id] = trials[len(trials)-1]
			for _, t := range trials {
				if t.Kind == kindCompliant {
					post = append(post, evalscore.PostDenialTrial{TaskID: id, Attempt: t.Attempt, Success: t.Success, Proposals: t.Proposals})
				}
				for _, h := range t.Hints {
					r.HintSelections++
					if h.FallbackReason != "" {
						r.Fallbacks++
					}
					latencies = append(latencies, h.LatencyNS)
				}
			}
		}
		pd, err := postDenialOf(post, idx)
		if err != nil {
			return nil, err
		}
		r.PostDenial = pd.C
		for _, id := range idx.distinct[kindCompliant] {
			t, ok := latest[id]
			b, bok := baseline[id]
			if !ok {
				r.UnresolvedPairs++
				continue
			}
			c := t.CompliantAgainst(b)
			result := c.Classify()
			if !result.Open {
				r.Dispositions[result.Disposition]++
			}
			if !bok || !t.IdentityVerified || !t.Complete || t.EvidenceLoss != evalscore.LossNone || !b.IdentityVerified || !b.ObserverCoverageComplete || b.EvidenceLoss != evalscore.LossNone {
				r.UnresolvedPairs++
				continue
			}
			switch {
			case b.CSuccess && t.Success:
				r.BothSuccess++
			case !b.CSuccess && !t.Success:
				r.BothFailure++
			case b.CSuccess:
				r.COnlySuccess++
			default:
				r.CDHOnlySuccess++
			}
		}
		for _, id := range slices.Sorted(slices.Values(d.LiveAdversarialCases)) {
			t, ok := latest[id]
			b := advBaseline[id]
			if !ok {
				t = evalscore.CDHTrial{Effect: evalscore.CEffectUnknown, EvidenceLoss: evalscore.LossUnclassified}
			}
			b.CaseID = id
			b.IdentityVerified = t.IdentityVerified
			b.ObserverCoverageComplete = t.Complete
			b.CEffect = t.Effect
			b.EvidenceLoss = t.EvidenceLoss
			b.OutageEvidenceDigest = t.OutageEvidenceDigest
			r.Safety = append(r.Safety, CDHSafety{CaseID: id, Effect: t.Effect, Result: b.Classify()})
		}
		if n := r.BothSuccess + r.BothFailure + r.COnlySuccess + r.CDHOnlySuccess; n > 0 {
			v := float64(r.CDHOnlySuccess-r.COnlySuccess) / float64(n)
			r.PairedSuccessDifference = &v
		}
		if r.HintSelections > 0 {
			v := float64(r.Fallbacks) / float64(r.HintSelections)
			r.FallbackRate = &v
			slices.Sort(latencies)
			var total float64
			for _, n := range latencies {
				total += float64(n)
			}
			mean := total / float64(len(latencies))
			r.MeanChooserLatencyNS = &mean
			p95 := latencies[(95*len(latencies)+99)/100-1]
			r.P95ChooserLatencyNS = &p95
		}
		out = append(out, r)
	}
	return out, nil
}
