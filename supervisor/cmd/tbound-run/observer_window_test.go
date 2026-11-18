package main

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

// H1-05: the observers start before the request and the service's deadline starts at admission, so a thesis run whose
// observer window is no longer than a case's budget loses PROC and EFF-TRANSIENT on a run that uses most of it.
func TestThesisPlanNeedsAnObserverWindowBeyondTheCaseBudget(t *testing.T) {
	e := thesisEnv(t)
	longest := 0
	for _, c := range e.manifest.Cases {
		if c.Kind == evalcorpus.KindAdversarial && c.Trial != nil {
			longest = max(longest, int(c.Trial.DurationSeconds))
		}
	}
	grace := e.cfg["proc"].(map[string]any)["teardown_grace_seconds"].(int)
	for _, c := range []struct {
		monitor int
		ok      bool
	}{{longest, false}, {longest + grace + 119, false}, {longest + grace + 120, true}, {1501, false}} {
		e.cfg["proc"].(map[string]any)["monitor_seconds"] = c.monitor
		e.write()
		_, err := e.cliCfg(nil, "plan")
		if (err == nil) != c.ok || (err != nil && !strings.Contains(err.Error(), "monitor_seconds")) {
			t.Errorf("monitor_seconds %d for a %d s case budget: err %v, want ok=%t", c.monitor, longest, err, c.ok)
		}
	}
}

// A pilot round is not claim-bearing and keeps a short window.
func TestPilotPlanKeepsAShortObserverWindow(t *testing.T) {
	e := newEnv(t)
	e.cfg["proc"].(map[string]any)["monitor_seconds"] = 300
	e.write()
	if _, err := e.cliCfg(nil, "plan"); err != nil {
		t.Fatalf("a pilot plan with a 300 s window: %v", err)
	}
}
