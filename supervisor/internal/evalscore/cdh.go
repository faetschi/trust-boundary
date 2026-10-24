package evalscore

import "encoding/json"

// CDHInput is a separate descriptive ledger. Summarize never consumes it.
type CDHInput struct {
	ManifestDigest       string     `json:"manifest_digest"`
	Choosers             []string   `json:"choosers"`
	LiveAdversarialCases []string   `json:"live_adversarial_cases"`
	Trials               []CDHTrial `json:"trials"`
}

type HintFact struct {
	CandidateID    string `json:"candidate_id"`
	FallbackReason string `json:"fallback_reason"`
	LatencyNS      int64  `json:"latency_ns"`
}

type CDHTrial struct {
	Attribution          Attribution    `json:"attribution,omitempty"`
	CaseID               string         `json:"case_id"`
	Configuration        string         `json:"configuration"`
	Attempt              int            `json:"attempt"`
	Kind                 string         `json:"kind"`
	ManifestDigest       string         `json:"manifest_digest"`
	IdentityVerified     bool           `json:"identity_verified"`
	Complete             bool           `json:"complete"`
	Success              bool           `json:"success"`
	EvidenceLoss         EvidenceLoss   `json:"evidence_loss"`
	OutageEvidenceDigest string         `json:"outage_evidence_digest"`
	Effect               CEffect        `json:"effect"`
	Proposals            []ProposalFact `json:"proposals"`
	Hints                []HintFact     `json:"hints"`
}

func loadCDH(raw []byte) (*CDHInput, error) {
	var top struct {
		ManifestDigest       string            `json:"manifest_digest"`
		Choosers             []string          `json:"choosers"`
		LiveAdversarialCases []string          `json:"live_adversarial_cases"`
		Trials               []json.RawMessage `json:"trials"`
	}
	if err := decodeObject(raw, &top, "manifest_digest", "choosers", "live_adversarial_cases", "trials"); err != nil {
		return nil, err
	}
	out := &CDHInput{ManifestDigest: top.ManifestDigest, Choosers: top.Choosers, LiveAdversarialCases: top.LiveAdversarialCases, Trials: []CDHTrial{}}
	for _, raw := range top.Trials {
		var t CDHTrial
		if err := decodeObjectOptional(raw, &t, []string{"attribution"}, "case_id", "configuration", "attempt", "kind", "manifest_digest", "identity_verified", "complete", "success", "evidence_loss", "outage_evidence_digest", "effect", "proposals", "hints"); err != nil {
			return nil, err
		}
		var nested struct {
			Proposals []json.RawMessage `json:"proposals"`
			Hints     []json.RawMessage `json:"hints"`
		}
		if err := json.Unmarshal(raw, &nested); err != nil {
			return nil, err
		}
		for _, r := range nested.Proposals {
			var p ProposalFact
			if err := decodeObjectOptional(r, &p, []string{"status"}, "tool", "verdict", "reason_code"); err != nil {
				return nil, err
			}
		}
		for _, r := range nested.Hints {
			var h HintFact
			if err := decodeObject(r, &h, "candidate_id", "fallback_reason", "latency_ns"); err != nil {
				return nil, err
			}
		}
		out.Trials = append(out.Trials, t)
	}
	return out, nil
}

// CompliantAgainst applies C's disposition rules against the frozen B/V evidence.
func (t CDHTrial) CompliantAgainst(b CompliantRecord) CompliantRecord {
	b.TaskID = t.CaseID
	b.Attempt = t.Attempt
	b.IdentityVerified = b.IdentityVerified && t.IdentityVerified
	b.ObserverCoverageComplete = b.ObserverCoverageComplete && t.Complete
	b.CSuccess = t.Success
	b.EvidenceLoss = t.EvidenceLoss
	b.OutageEvidenceDigest = t.OutageEvidenceDigest
	b.Attribution = AttributionNotApplicable
	if b.BSuccess && !b.CSuccess {
		b.Attribution = t.Attribution
		if b.Attribution == "" || b.Attribution == AttributionNotApplicable {
			b.Attribution = AttributionUnresolved
		}
	}
	return b
}
