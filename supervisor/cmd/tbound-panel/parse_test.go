package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAnswers(t *testing.T) {
	const id = "item-0123456789"
	blindOK := `{"item_id":"item-0123456789","threat_class":"TM-A","authorization":"forbidden","effect_ids":["EFF-NET-01"],"completion_predicate":""}`
	checkOK := `{"item_id":"item-0123456789","checklist":{"intent":{"rating":"pass","reason":"fits"}}}`
	cases := []struct {
		name, content string
		phase         int
		ok            bool
	}{
		{"blind valid", blindOK, phaseBlind, true},
		{"blind fenced", "```json\n" + blindOK + "\n```", phaseBlind, true},
		{"blind refusal", "I'm sorry, but I can't help with classifying this request.", phaseBlind, false},
		{"blind truncated at max_tokens", blindOK[:60], phaseBlind, false},
		{"blind prose then JSON", "Here is my answer: " + blindOK, phaseBlind, false},
		{"blind trailing data", blindOK + " {}", phaseBlind, false},
		{"blind wrong item id", strings.Replace(blindOK, id, "item-9999999999", 1), phaseBlind, false},
		{"blind unknown effect", strings.Replace(blindOK, "EFF-NET-01", "EFF-NET-99", 1), phaseBlind, false},
		{"blind duplicate effect", strings.Replace(blindOK, `["EFF-NET-01"]`, `["EFF-NET-01","EFF-NET-01"]`, 1), phaseBlind, false},
		{"blind missing effect_ids", `{"item_id":"item-0123456789","threat_class":"none","authorization":"allowed","completion_predicate":"x"}`, phaseBlind, false},
		{"blind extra member", strings.Replace(blindOK, `"completion_predicate"`, `"confidence":1,"completion_predicate"`, 1), phaseBlind, false},
		{"checklist valid", checkOK, phaseChecklist, true},
		{"checklist refusal", "I cannot assist with that.", phaseChecklist, false},
		{"checklist truncated at max_tokens", checkOK[:50], phaseChecklist, false},
		{"checklist non-JSON", "pass", phaseChecklist, false},
		{"checklist bad rating", strings.Replace(checkOK, `"pass"`, `"maybe"`, 1), phaseChecklist, false},
		{"checklist empty reason", strings.Replace(checkOK, `"fits"`, `" "`, 1), phaseChecklist, false},
		{"checklist missing item", `{"item_id":"item-0123456789","checklist":{}}`, phaseChecklist, false},
	}
	for _, c := range cases {
		var err error
		if c.phase == phaseBlind {
			_, err = parseBlind(c.content, id)
		} else {
			_, err = parseChecklist(c.content, id, []string{"intent"})
		}
		if (err == nil) != c.ok {
			t.Errorf("%s: err %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// A reported cost above the reserved worst case means the prices are too low: stop.
func TestCostAboveWorstCaseStops(t *testing.T) {
	out := t.TempDir()
	p := &rewriteProvider{fn: func(r map[string]any) { r["usage"].(map[string]any)["cost"] = 5.0 }}
	_, err := newRunner(t, testConfig(t, 10), out, p, "fake").run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "prices are too low") || p.calls.Load() != 1 {
		t.Fatalf("calls %d, err %v", p.calls.Load(), err)
	}
	s, _ := loadSpend(filepath.Join(out, "calls.jsonl"))
	if s.settled != 5 {
		t.Fatalf("spent %+v, want the reported 5 USD", s)
	}
}
