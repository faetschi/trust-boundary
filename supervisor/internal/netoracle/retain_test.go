package netoracle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/runner"
)

func TestParseRetainedRejectsCaseFoldedMembers(t *testing.T) {
	for _, key := range []string{"schema", "Schema", `\u0053chema`, `\u017fchema`} {
		t.Run(key, func(t *testing.T) {
			raw := []byte(`{"schema":"foreign","` + key + `":"` + RetainedSchema + `"}`)
			if _, err := ParseRetained(raw); err == nil {
				t.Fatal("ambiguous schema accepted")
			}
		})
	}
	if _, err := ParseRetained([]byte(`{"schema":"` + RetainedSchema + `","start":{"phase":"foreign","Phase":"start"}}`)); err == nil {
		t.Fatal("nested case-folded members accepted")
	}
}

func TestRetainedNETOverOneMiBVerifies(t *testing.T) {
	g := newGuestNet(t)
	c := g.collector()
	c.RetainDir = t.TempDir()
	if err := c.BeginInterval(context.Background(), collectorRef()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		do(t, "GET", "http://"+g.s.Addr()+"/"+strings.Repeat("x", 48<<10), "")
	}
	ev, err := c.Net(context.Background(), collectorRef())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(c.RetainDir, RetainedName(collectorInterval)))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 1<<20 {
		t.Fatalf("retained doc only %d bytes", len(raw))
	}
	t.Logf("retained %d bytes", len(raw))
	r, err := ParseRetained(raw)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := VerifyInterval(r.Interval, r.Start, r.End, r.Journal)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := rep.Digest()
	if err != nil || len(rep.Gaps) != 0 || digest != ev.Refs[runner.OracleNet01].Report || len(rep.Arrivals) != 16 {
		t.Fatalf("retained report differs: digest=%s err=%v gaps=%v arrivals=%d", digest, err, rep.Gaps, len(rep.Arrivals))
	}
}
