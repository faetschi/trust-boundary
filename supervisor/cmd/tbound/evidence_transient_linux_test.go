//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/runner"
)

// transientEnv is an evidence env whose observer is scripted: it records what it was asked and calls ready.
type transientEnv struct {
	*evTestEnv
	watches  int
	roots    []string
	duration time.Duration
	report   *effectoracle.Report
	err      error
	locked   int
	lockErr  error
}

func newTransientEnv(uid int) *transientEnv {
	e := &transientEnv{evTestEnv: newEvTestEnv(uid), report: &effectoracle.Report{OracleVersion: effectoracle.OracleVersion,
		Instrument: effectoracle.Instrument, Verdict: effectoracle.VerdictOK}}
	e.lockTransient = func() (func(), error) {
		if e.lockErr != nil {
			return nil, e.lockErr
		}
		e.locked++
		return func() { e.locked-- }, nil
	}
	e.watch = func(_ context.Context, root string, d time.Duration, ready func() error) (*effectoracle.Report, error) {
		e.watches++
		e.roots, e.duration = append(e.roots, root), d
		if e.err != nil {
			return nil, e.err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		return e.report, nil
	}
	return e
}

func (e *transientEnv) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runEvidence(context.Background(), args, &out, e.evidenceEnv)
	return out.String(), err
}

func TestEvidenceTransientWatchAcknowledgesThenReportsOverTheLiveRootOnly(t *testing.T) {
	env := newTransientEnv(0)
	ref := "case-1|trajectory|C|0#1"
	out, err := env.run(t, "transient-watch", "--interval-ref", ref, "--duration", "30")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want the readiness line and the document", out)
	}
	var ready runner.TransientReadyDoc
	if err := runner.DecodeEvidenceDoc([]byte(lines[0]), &ready); err != nil ||
		ready.Kind != runner.TransientReadyKind || ready.IntervalRef != ref || ready.Root != runner.EvidenceLiveRoot {
		t.Fatalf("readiness = %+v, %v", ready, err)
	}
	var doc runner.TransientDoc
	if err := runner.DecodeEvidenceDoc([]byte(lines[1]), &doc); err != nil ||
		doc.Kind != runner.EvidenceTransientWatch || doc.IntervalRef != ref || doc.Root != runner.EvidenceLiveRoot ||
		doc.DurationSeconds != 30 || doc.Report.Verdict != effectoracle.VerdictOK {
		t.Fatalf("document = %+v, %v", doc, err)
	}
	// The root is the registered one, never an argument, and the watch is released afterwards.
	if len(env.roots) != 1 || env.roots[0] != piruntime.D09ManagedPiLiveRootPath || env.duration != 30*time.Second || env.locked != 0 {
		t.Fatalf("roots %v duration %v locks %d", env.roots, env.duration, env.locked)
	}
	if _, err := env.run(t, "transient-watch", "--interval-ref", ref, "--duration", "30", "--root", "/etc"); err == nil {
		t.Fatal("transient-watch took a root")
	}
}

func TestEvidenceTransientWatchIsBoundedExclusiveAndRootOnly(t *testing.T) {
	if runner.MaxTransientWatchSeconds != evidenceMaxMonitorSeconds {
		t.Fatal("the transient watch bound is not the monitor's")
	}
	for name, d := range map[string]string{"zero": "0", "too long": "1501", "leading zero": "030", "negative": "-1", "text": "x"} {
		env := newTransientEnv(0)
		if out, err := env.run(t, "transient-watch", "--interval-ref", "r#1", "--duration", d); err == nil || out != "" || env.watches != 0 {
			t.Errorf("%s: err=%v stdout=%q watches=%d", name, err, out, env.watches)
		}
	}
	if _, err := newTransientEnv(0).run(t, "transient-watch", "--interval-ref", "r#1", "--duration", "1500"); err != nil {
		t.Errorf("the largest allowed watch: %v", err)
	}
	env := newTransientEnv(0)
	env.lockErr = errors.New("another transient-watch is running")
	if out, err := env.run(t, "transient-watch", "--interval-ref", "r#1", "--duration", "30"); err == nil || out != "" || env.watches != 0 {
		t.Errorf("second watch: err=%v stdout=%q watches=%d", err, out, env.watches)
	}
	env = newTransientEnv(1000)
	if out, err := env.run(t, "transient-watch", "--interval-ref", "r#1", "--duration", "30"); err == nil || out != "" || env.watches != 0 {
		t.Errorf("non-root: err=%v stdout=%q", err, out)
	}
	env = newTransientEnv(0)
	env.err = errors.New("inotify unavailable")
	if out, err := env.run(t, "transient-watch", "--interval-ref", "r#1", "--duration", "30"); err == nil || out != "" {
		t.Errorf("observer failure: err=%v stdout=%q", err, out)
	}
	for name, args := range map[string][]string{
		"no ref":     {"transient-watch", "--duration", "30"},
		"unsafe ref": {"transient-watch", "--interval-ref", "a;id", "--duration", "30"},
		"dash ref":   {"transient-watch", "--interval-ref", "-x", "--duration", "30"},
		"duplicate":  {"transient-watch", "--interval-ref", "a", "--interval-ref", "b"},
	} {
		if _, err := newTransientEnv(0).run(t, args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEvidenceTransientWatchReachesTheForcedCommand(t *testing.T) {
	env := newTransientEnv(0)
	env.getenv = func(string) string {
		return `'/usr/local/bin/tbound' 'evidence' 'transient-watch' '--interval-ref' 'case-1|trajectory|C|0#1' '--duration' '30'`
	}
	out, err := env.run(t, "ssh-original-command")
	if err != nil || strings.Count(out, "\n") != 2 {
		t.Fatalf("err=%v stdout=%q", err, out)
	}
}

// The real observer over a real directory: the readiness line comes before the window, and the report holds what
// happened in it, with the instrument's own canary traffic filtered out.
func TestWatchEvidenceRootObservesTheRealTree(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	type result struct {
		rep *effectoracle.Report
		err error
	}
	done := make(chan result, 1)
	go func() {
		rep, err := watchEvidenceRoot(ctx, root, time.Hour, func() error { close(started); return nil })
		done <- result{rep, err}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the observer never became ready")
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cancel() // the window ends early: the observer is stopped and reports
	res := <-done
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.rep.Verdict != effectoracle.VerdictOK {
		t.Fatalf("verdict %s gaps %+v", res.rep.Verdict, res.rep.Gaps)
	}
	seen := false
	for _, ev := range res.rep.Events {
		if ev.Path == "new.txt" {
			seen = true
		}
		if strings.HasPrefix(ev.Path, ".effectoracle") {
			t.Errorf("canary traffic in the events: %+v", ev)
		}
	}
	if !seen {
		t.Fatalf("the write is not in the report: %+v", res.rep.Events)
	}
	if left, _ := os.ReadDir(root); len(left) != 2 {
		t.Fatalf("the canary was not removed: %v", left)
	}
	// A root that is not a directory is an error, not a report.
	if _, err := watchEvidenceRoot(context.Background(), filepath.Join(root, "a.txt"), time.Second, func() error { return nil }); err == nil {
		t.Fatal("a file was watched")
	}
}
