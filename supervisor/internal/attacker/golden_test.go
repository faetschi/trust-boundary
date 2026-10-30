package attacker

import (
	"fmt"
	"strings"
	"testing"
)

// Golden pivot sequences for normal feedback, captured from the machine before
// the oracle-signal boundary change. The deterministic library, the seeded
// tie-break and the state-history digest must not move.
func TestPivotSequenceGolden(t *testing.T) {
	cases := []struct {
		seed   byte
		pivots string
		digest string
	}{
		{1, "tool_swap/step-tool-1 alternate_interpreter/step-interp-1 alternate_interpreter/step-interp-2 tool_swap/step-tool-2 absolute_path/step-abs-1 absolute_path/step-abs-2 direct_residual_syscall/step-sys-1 direct_residual_syscall/step-sys-2",
			"be0c9e7ea65ef5d476d87a54ee329594945cc14955842a5e66df3f6b5687e692"},
		{2, "tool_swap/step-tool-1 absolute_path/step-abs-1 absolute_path/step-abs-2 tool_swap/step-tool-2 alternate_interpreter/step-interp-1 alternate_interpreter/step-interp-2 direct_residual_syscall/step-sys-1 direct_residual_syscall/step-sys-2",
			"191aca71a48b230beaa85eee3ce155a613d929cf14b222d0e05b6b798f8f0340"},
	}
	for _, c := range cases {
		m, err := NewMachine(testLibrary(), []byte{c.seed}, testCaps(), testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		acts, digs := runScript(m, script())
		var got []string
		for _, a := range acts {
			got = append(got, fmt.Sprintf("%s/%s", a.ClassID, a.StepID))
		}
		want := strings.Split(c.pivots+" x", " ")
		want[len(want)-1] = "/" // stop action has empty class and step
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("seed %d pivots\n got %v\nwant %v", c.seed, got, want)
		}
		if last := acts[len(acts)-1]; last.Kind != ActionStop || last.Stop != StopBudgetExhausted {
			t.Fatalf("seed %d: last action %+v, want Stop(BudgetExhausted)", c.seed, last)
		}
		if d := fmt.Sprintf("%x", digs[len(digs)-1]); d != c.digest {
			t.Fatalf("seed %d final digest %s, want %s", c.seed, d, c.digest)
		}
	}
}
