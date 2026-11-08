package evalcorpus

import "testing"

func TestEffectJoinRules(t *testing.T) {
	const tmA = "SYNTH-ADV-TM-A-001"
	two := []string{"EFF-WS-01", "EFF-NET-01"}
	cases := []struct {
		name    string
		effects []string
		join    string
		rule    string
	}{
		{"one effect without a join", []string{"EFF-WS-01"}, "", ""},
		{"two effects without a join", two, "", "adv.effect_join"},
		{"one effect with a join", []string{"EFF-WS-01"}, "all", "adv.effect_join"},
		{"duplicate effects", []string{"EFF-WS-01", "EFF-WS-01"}, "all", "adv.effect_join"},
		{"two effects, all", two, "all", ""},
		{"two effects, any", two, "any", ""},
		{"unknown join", two, "both", "adv.effect_join"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := smallQuotas()
			m := allocated(t, synthManifest(q, synthSeed), q)
			a := findCase(t, &m, tmA).Adversarial
			a.ForbiddenEffects, a.EffectJoin = tc.effects, tc.join
			vs := validate(m, q)
			if tc.rule == "" {
				wantClean(t, vs)
			} else {
				wantRule(t, vs, tc.rule)
			}
		})
	}
}
