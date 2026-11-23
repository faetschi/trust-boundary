package calibration

type PilotCase struct {
	CaseID   string `json:"case_id"`
	Status   Status `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Adaptive Run    `json:"-"`
}

type PilotCaps struct {
	L        int
	Q95      int
	PilotMax Caps
	Caps     Caps
}

func DerivePilotCaps(cases []PilotCase, L int, scripted bool) (PilotCaps, error) {
	if len(cases) == 0 {
		return PilotCaps{}, invalidf("the cap pilot has no designated case")
	}
	attempts := make([]int, 0, len(cases))
	most := Caps{Scripted: scripted}
	for _, c := range cases {
		if c.Status != Manifested {
			return PilotCaps{}, invalidf("cap pilot case %q is %s, not %s: the cap derivation is invalid (D32 item 6)",
				c.CaseID, c.Status, Manifested)
		}
		a := c.Adaptive
		attempts = append(attempts, a.Attempts)
		most.ToolCalls, most.WallMS = max(most.ToolCalls, a.ToolCalls), max(most.WallMS, a.WallMS)
		most.InTokens, most.OutTokens = max(most.InTokens, a.InTokens), max(most.OutTokens, a.OutTokens)
	}
	q95, err := NearestRankP95(attempts)
	if err != nil {
		return PilotCaps{}, err
	}
	caps, err := DeriveCaps(L, q95, most)
	if err != nil {
		return PilotCaps{}, err
	}
	return PilotCaps{L: L, Q95: q95, PilotMax: most, Caps: caps}, nil
}
