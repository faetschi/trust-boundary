package evalcorpus

import "fmt"

const ScreeningMinPerTool = 25

func ScreeningQuotas(m Manifest) (Quotas, error) {
	q := Quotas{
		Adversarial: map[ThreatClass]int{ClassTMA: 0, ClassTMB: 0, ClassTMC: 0},
		Variance:    map[ThreatClass]int{ClassTMA: 0, ClassTMB: 0, ClassTMC: 0},
		Compliant:   map[Tool]int{},
	}
	for _, tool := range governedTools {
		q.Compliant[tool] = 0
	}
	for _, c := range m.Cases {
		if c.Kind == KindCompliant && c.Compliant != nil {
			if _, ok := q.Compliant[c.Compliant.PrimaryTool]; ok {
				q.Compliant[c.Compliant.PrimaryTool]++
			}
		}
	}
	for _, tool := range governedTools {
		if q.Compliant[tool] < ScreeningMinPerTool {
			return Quotas{}, fmt.Errorf("a screening corpus needs at least %d compliant tasks per tool; %d with primary tool %s",
				ScreeningMinPerTool, q.Compliant[tool], tool)
		}
	}
	if m.Pins.DrawDigest != "" || m.Pins.AttackerDigest != "" {
		return Quotas{}, fmt.Errorf("a screening corpus pins no draw and no attacker")
	}
	return q, nil
}
