package runner

import (
	"fmt"
	"slices"
	"strings"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/providerbridge"
)

// CDHConfig selects a separate descriptive ledger; it never extends the H1 plan.
type CDHConfig struct {
	Manifest             providerbridge.CDHManifest `json:"manifest"`
	LiveAdversarialCases []string                   `json:"live_adversarial_cases"`
}

func CDHConfiguration(model string) Configuration { return Configuration(string(CfgCDH) + "/" + model) }
func (c Configuration) IsCDH() bool {
	return strings.HasPrefix(string(c), "C-DH/") && len(c) > len("C-DH/")
}

func (c *CDHConfig) Validate() error {
	if _, err := c.Manifest.Digest(); err != nil {
		return err
	}
	for _, model := range c.Manifest.Models {
		if strings.ContainsAny(model, "|\r\n") {
			return fmt.Errorf("runner: invalid chooser identity %q", model)
		}
	}
	seen := map[string]bool{}
	for _, id := range c.LiveAdversarialCases {
		if seen[id] {
			return fmt.Errorf("runner: duplicate C-DH safety case %q", id)
		}
		seen[id] = true
	}
	return nil
}

func planCDH(s Schedule, allocation evalcorpus.Manifest) (Schedule, error) {
	cfg := s.Config.CDH
	if len(s.Config.OnlyCases) != 0 {
		return Schedule{}, fmt.Errorf("runner: C-DH refuses OnlyCases; register live_adversarial_cases")
	}
	byID := map[string]evalcorpus.Case{}
	var compliant []string
	for _, c := range allocation.Cases {
		byID[c.CaseID] = c
		if c.Kind == evalcorpus.KindCompliant {
			compliant = append(compliant, c.CaseID)
		}
	}
	for _, id := range cfg.LiveAdversarialCases {
		c, ok := byID[id]
		// A D32 pivot table is a scripted adaptive cohort, never a live-model safety case.
		if !ok || c.Kind != evalcorpus.KindAdversarial || c.Trial == nil || len(c.Trial.Pivots) != 0 {
			return Schedule{}, fmt.Errorf("runner: C-DH safety case %q is not a registered live-model case (D32 forbidden)", id)
		}
	}
	s.Steps = nil
	for _, ids := range [][]string{rankOrder(allocation.SplitSeed, "order/compliant", compliant), rankOrder(allocation.SplitSeed, "order/cdh-safety", cfg.LiveAdversarialCases)} {
		for _, id := range ids {
			c := byID[id]
			for _, model := range cfg.Manifest.Models {
				role := RoleCompliant
				if c.Kind == evalcorpus.KindAdversarial {
					role = RoleTrajectory
				}
				if !corpusadmit.BoundedIdentity(corpusadmit.AdmissionID(id, corpusadmit.CDHRole(model, string(role)), 2), corpusadmit.RequestIDMaxBytes) {
					return Schedule{}, fmt.Errorf("runner: C-DH admission id too long for %q", id)
				}
				st := Step{Index: len(s.Steps), CaseID: id, Kind: c.Kind, Configuration: CDHConfiguration(model), Role: role, Attempt: 1}
				if c.Adversarial != nil {
					st.ThreatClass = c.Adversarial.ThreatClass
					st.WitnessID = c.Adversarial.CalibrationWitnessID
					if len(c.Adversarial.ForbiddenEffects) != 0 {
						st.Predicate = c.Adversarial.ForbiddenEffects[0]
					}
				}
				s.Steps = append(s.Steps, st)
			}
		}
	}
	if len(s.Steps) > s.Config.UnitCap {
		return Schedule{}, fmt.Errorf("%w: %d steps, cap %d", ErrOverPlanned, len(s.Steps), s.Config.UnitCap)
	}
	s.index = make(map[string]int, len(s.Steps))
	for i, st := range s.Steps {
		if _, dup := s.index[st.ID()]; dup {
			return Schedule{}, fmt.Errorf("runner: duplicate step id %q", st.ID())
		}
		s.index[st.ID()] = i
	}
	var err error
	s.Digest, err = planDigest(s)
	return s, err
}

func (c Config) cdhSlot(configuration Configuration) (int, bool) {
	if c.CDH == nil {
		return 0, false
	}
	i := slices.Index(c.CDH.Manifest.Models, strings.TrimPrefix(string(configuration), "C-DH/"))
	return i, configuration.IsCDH() && i >= 0
}

func rerunCase(s Step) string {
	if s.Configuration.IsCDH() {
		return s.CaseID + "|" + string(s.Configuration)
	}
	return s.CaseID
}
