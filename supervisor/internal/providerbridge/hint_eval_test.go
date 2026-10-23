package providerbridge

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestHintEvalFrozenFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/hint_eval.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture HintEvalFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Scenarios) != 20 {
		t.Fatal("frozen scenario count changed")
	}
	reports, err := EvaluateHintFixture(fixture, .8, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 3 || reports[0].FallbackRate != 0 || reports[0].MeanLatencyMS != 0 || reports[1].FallbackRate != .4 || reports[1].CheckRejectionRate != .2 || reports[1].MeanLatencyMS != 3.95 || reports[2].FallbackRate != .3 || reports[2].CheckRejectionRate != 0 {
		t.Fatalf("%+v", reports)
	}
	for _, r := range reports {
		if r.AlternativeAllowed+r.RetryDenied+r.NoFurtherAction != 20 || r.MaxHintBytes > 128 {
			t.Fatal(r)
		}
	}
	fixture.Scenarios[0].StaticNext = "invented"
	if _, err := EvaluateHintFixture(fixture, .8, time.Second); err == nil {
		t.Fatal("invalid continuation accepted")
	}
}
