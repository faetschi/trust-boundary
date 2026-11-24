package evalreport

import (
	"strings"
	"testing"
)

func TestPlanVKeysTheVLineAndThePlannedUnits(t *testing.T) {
	off := fullInputs()
	on := fullInputs()
	on.PlanV = true
	for i := range on.Trials.Compliant {
		on.Trials.Compliant[i].VSuccess, on.Trials.Compliant[i].VMissing = false, true
	}
	rOff, rOn := mustBuild(t, off), mustBuild(t, on)

	if !strings.Contains(rOff.Markdown(), "- V: not planned (D58 amendment)\n") {
		t.Errorf("PlanV off: V line missing:\n%s", rOff.Markdown())
	}
	mdOn := rOn.Markdown()
	if strings.Contains(mdOn, "not planned") || !strings.Contains(mdOn, "- V: tasks 100; success 0; failure 0; missing 100\n") {
		t.Errorf("PlanV on with every V unit lost must read as lost:\n%s", mdOn)
	}
	if rOff.PlanV || !rOn.PlanV || rOff.Digest == rOn.Digest {
		t.Errorf("PlanV off %t, on %t, digests equal %t", rOff.PlanV, rOn.PlanV, rOff.Digest == rOn.Digest)
	}
	if rOff.Spend.PlannedUnits != 1835 || rOn.Spend.PlannedUnits != 1935 {
		t.Errorf("legacy planned units off %d, on %d; want 1835, 1935", rOff.Spend.PlannedUnits, rOn.Spend.PlannedUnits)
	}
}

func TestPlannedUnitsCountThreeCompliantUnitsWithV(t *testing.T) {
	in := d48Inputs()
	off, on := PlannedUnits(in.Manifest, false), PlannedUnits(in.Manifest, true)
	if on-off != 100 {
		t.Fatalf("PlannedUnits off %d, on %d; want 100 more with V", off, on)
	}
	in.PlanV = true
	if r := mustBuild(t, in); r.Spend.PlannedUnits != on {
		t.Fatalf("report plans %d units, want %d", r.Spend.PlannedUnits, on)
	}
}

func TestRegistrationRecordsPlanVOnlyWhenOn(t *testing.T) {
	r := Registration{Domain: RegistrationDomain}
	offDigest, err := r.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	r.PlanV = true
	onDigest, err := r.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	if offDigest == onDigest {
		t.Fatal("PlanV is not bound by the registration digest")
	}
	r.PlanV = false
	if again, _ := r.ComputeDigest(); again != offDigest {
		t.Fatal("PlanV off changes the registration digest")
	}
}
