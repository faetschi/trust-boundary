//go:build linux

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"tbound/supervisor/internal/runner"
)

// e04-points is reachable through the evidence entry point, directly and through the forced
// command, under the same root-only and safe-alphabet rules as every other kind.
func TestE04PointsKindIsDispatchedByRunEvidence(t *testing.T) {
	w, receipt := e04CompletedWorkflow(t)
	env := e04KindEnv(t, w, receipt, nil)
	env.geteuid = func() int { return evidenceRequiredUID }
	env.getenv = func(string) string { return "" }

	var out bytes.Buffer
	if err := runEvidence(context.Background(), []string{runner.EvidenceE04Points, "--interval-ref", e04TestInterval}, &out, env); err != nil {
		t.Fatalf("e04-points through runEvidence: %v", err)
	}
	var doc runner.E04PointsDoc
	if err := runner.DecodeEvidenceDoc(bytes.TrimSuffix(out.Bytes(), []byte("\n")), &doc); err != nil ||
		doc.Kind != runner.EvidenceE04Points || doc.Count != 7 || doc.Head != receipt.E04EvidenceHead || doc.IntervalRef != e04TestInterval {
		t.Fatalf("document = %+v, err %v", doc, err)
	}

	nonRoot := env
	nonRoot.geteuid = func() int { return 1000 }
	out.Reset()
	if err := runEvidence(context.Background(), []string{runner.EvidenceE04Points, "--interval-ref", e04TestInterval}, &out, nonRoot); err == nil || out.Len() != 0 {
		t.Fatalf("e04-points ran as a non-root uid: %v", err)
	}
	for name, args := range map[string][]string{
		"no flag":          {runner.EvidenceE04Points},
		"flag of a kind":   {runner.EvidenceE04Points, "--interval", e04TestInterval},
		"a snapshot flag":  {runner.EvidenceE04Points, "--root", "/var/lib/tbound/live", "--point", "baseline"},
		"equals form":      {runner.EvidenceE04Points, "--interval-ref=" + e04TestInterval},
		"a leading dash":   {runner.EvidenceE04Points, "--interval-ref", "-x"},
		"outside alphabet": {runner.EvidenceE04Points, "--interval-ref", "a;b"},
	} {
		out.Reset()
		if err := runEvidence(context.Background(), args, &out, env); err == nil || out.Len() != 0 {
			t.Errorf("%s: err = %v, output %d bytes", name, err, out.Len())
		}
	}
}

func TestE04PointsKindIsDispatchedByTheForcedCommand(t *testing.T) {
	w, receipt := e04CompletedWorkflow(t)
	env := e04KindEnv(t, w, receipt, nil)
	env.geteuid = func() int { return evidenceRequiredUID }
	plain := "/usr/local/bin/tbound evidence e04-points --interval-ref " + e04TestInterval
	quoted := `'/usr/local/bin/tbound' 'evidence' 'e04-points' '--interval-ref' '` + e04TestInterval + `'`
	for name, command := range map[string]string{"plain": plain, "quoted": quoted} {
		env.getenv = func(string) string { return command }
		var out bytes.Buffer
		if err := runEvidence(context.Background(), []string{runner.EvidenceSSHOriginalCommand}, &out, env); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var doc runner.E04PointsDoc
		if err := runner.DecodeEvidenceDoc(bytes.TrimSuffix(out.Bytes(), []byte("\n")), &doc); err != nil || doc.Kind != runner.EvidenceE04Points || doc.Count != 7 {
			t.Fatalf("%s: document = %+v, err %v", name, doc, err)
		}
	}
	for name, command := range map[string]string{
		"command substitution": "/usr/local/bin/tbound evidence e04-points --interval-ref $(id)",
		"backtick":             "/usr/local/bin/tbound evidence e04-points --interval-ref `id`",
		"semicolon":            "/usr/local/bin/tbound evidence e04-points --interval-ref x;id",
		"double quote":         `/usr/local/bin/tbound evidence e04-points --interval-ref "` + e04TestInterval + `"`,
		"double space":         "/usr/local/bin/tbound  evidence e04-points --interval-ref " + e04TestInterval,
		"too long":             "/usr/local/bin/tbound evidence e04-points --interval-ref " + strings.Repeat("a", 3000),
		"extra word":           plain + " extra",
		"other binary":         "/bin/cat evidence e04-points --interval-ref " + e04TestInterval,
	} {
		env.getenv = func(string) string { return command }
		var out bytes.Buffer
		if err := runEvidence(context.Background(), []string{runner.EvidenceSSHOriginalCommand}, &out, env); err == nil || out.Len() != 0 {
			t.Errorf("%s: err = %v, output %d bytes", name, err, out.Len())
		}
	}
}

func TestEvidenceUsageNamesTheE04PointsKind(t *testing.T) {
	if !strings.Contains(strings.Join(strings.Fields(evidenceUsage), " "), "e04-points --interval-ref <ref>") {
		t.Fatal("usage does not list e04-points --interval-ref <ref>")
	}
	for kind := range evidenceExtraKinds {
		if !strings.Contains(evidenceUsage, kind) {
			t.Errorf("usage does not mention %s", kind)
		}
	}
}
