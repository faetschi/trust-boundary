package guestevidence

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpus/route-probe is the draft governed run that makes the commands RouteProbeCommands names. Its
// prompt must contain exactly those commands, or the producer would find no attempt in the journal.
func TestRouteProbeCaseIssuesExactlyTheProbeCommands(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "corpus", "route-probe", "route-probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Schema        string            `json:"schema"`
		Status        string            `json:"status"`
		ClaimBearing  bool              `json:"claim_bearing"`
		Route         string            `json:"route"`
		Target        string            `json:"target"`
		ControlPath   string            `json:"control_path"`
		Prompt        string            `json:"prompt"`
		Commands      map[string]string `json:"commands"`
		BashNoOutputs bool              `json:"bash_no_outputs"`
		CaseID        string            `json:"case_id"`
		TaskID        string            `json:"task_id"`
		Ceilings      map[string]uint64 `json:"ceilings"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.Schema != "tbound-route-probe-case/v1" || c.ClaimBearing || c.Route != "engineering" || !c.BashNoOutputs ||
		!strings.Contains(c.Status, "owner approval required") {
		t.Fatalf("case header = %+v", c)
	}
	want := RouteProbeCommands(c.Target, c.ControlPath)
	if len(c.Commands) != len(want) {
		t.Fatalf("commands = %v, want %v", c.Commands, want)
	}
	last := -1
	for _, method := range append([]string{RouteMethodControl}, RouteMethods...) {
		if c.Commands[method] != want[method] {
			t.Errorf("%s: command %q, the producer looks for %q", method, c.Commands[method], want[method])
		}
		i := strings.Index(c.Prompt, "`"+want[method]+"`")
		if i < 0 || i < last {
			t.Errorf("%s: the prompt does not hold %q after the previous command", method, want[method])
		}
		last = i
	}
	// The target is outside every path the cell's Landlock ruleset admits.
	for _, root := range RouteRulesetRoots {
		if c.Target == root || strings.HasPrefix(c.Target, root+"/") {
			t.Errorf("target %s is inside %s", c.Target, root)
		}
	}
}
