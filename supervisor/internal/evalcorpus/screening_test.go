package evalcorpus

import "testing"

func screeningManifest(perTool int) Manifest {
	m := synthManifest(Quotas{Adversarial: map[ThreatClass]int{}, Compliant: map[Tool]int{}}, synthSeed)
	for _, tool := range governedTools {
		for i := 1; i <= perTool; i++ {
			m.Cases = append(m.Cases, synthCompliant(tool, i))
		}
	}
	return m
}

func TestScreeningQuotasAcceptACompliantOnlyManifest(t *testing.T) {
	m := screeningManifest(ScreeningMinPerTool)
	q, err := ScreeningQuotas(m)
	if err != nil {
		t.Fatal(err)
	}
	wantClean(t, Validate(sealed(m), q))
}

func TestScreeningQuotasRefuse(t *testing.T) {
	short := screeningManifest(ScreeningMinPerTool)
	short.Cases = short.Cases[1:]
	drawn := screeningManifest(ScreeningMinPerTool)
	drawn.Pins.DrawDigest = synthDigest("draw")
	attacked := screeningManifest(ScreeningMinPerTool)
	attacked.Pins.AttackerDigest = synthDigest("attacker")
	for name, m := range map[string]Manifest{"24 of one tool": short, "draw pin": drawn, "attacker pin": attacked} {
		if _, err := ScreeningQuotas(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	q := smallQuotas()
	adversarial := screeningManifest(ScreeningMinPerTool)
	adversarial.Cases = append(adversarial.Cases, allocated(t, synthManifest(q, synthSeed), q).Cases[0])
	sq, err := ScreeningQuotas(adversarial)
	if err != nil {
		t.Fatal(err)
	}
	if len(Validate(sealed(adversarial), sq)) == 0 {
		t.Error("an adversarial case passed the screening validation")
	}
}
