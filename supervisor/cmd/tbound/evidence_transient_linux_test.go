//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/runner"
)

// transientEnv is an evidence env whose observer is scripted: it records what it was asked and calls ready, then
// ends the way the real one does, on the stop line (a report) or on an input that is not one (an abort, no report).
type transientEnv struct {
	*evTestEnv
	watches  int
	roots    []string
	duration time.Duration
	report   *effectoracle.Report
	err      error
	aborted  int
	locked   int
	lockErr  error
}

func newTransientEnv(uid int) *transientEnv {
	e := &transientEnv{evTestEnv: newEvTestEnv(uid), report: &effectoracle.Report{OracleVersion: effectoracle.OracleVersion,
		Instrument: effectoracle.Instrument, Verdict: effectoracle.VerdictOK}}
	e.stdin = strings.NewReader(runner.TransientStopLine)
	e.lockTransient = func() (func(), error) {
		if e.lockErr != nil {
			return nil, e.lockErr
		}
		e.locked++
		return func() { e.locked-- }, nil
	}
	e.watch = func(_ context.Context, root string, d time.Duration, ready func() error, stop <-chan error) (transientWatch, error) {
		e.watches++
		e.roots, e.duration = append(e.roots, root), d
		if e.err != nil {
			return transientWatch{}, e.err
		}
		if err := ready(); err != nil {
			return transientWatch{}, err
		}
		if err := <-stop; err != nil {
			e.aborted++
			return transientWatch{}, err
		}
		return transientWatch{Report: e.report, Elapsed: 1500 * time.Millisecond, Stop: runner.TransientStopRequested}, nil
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
	// The window the document reports is the one the observer measured, and why it ended.
	if doc.ElapsedMicros != 1_500_000 || doc.Stop != runner.TransientStopRequested {
		t.Fatalf("window = %d us, stop %q", doc.ElapsedMicros, doc.Stop)
	}
	// The root is the registered one, never an argument, and the watch is released afterwards.
	if len(env.roots) != 1 || env.roots[0] != piruntime.D09ManagedPiLiveRootPath || env.duration != 30*time.Second || env.locked != 0 {
		t.Fatalf("roots %v duration %v locks %d", env.roots, env.duration, env.locked)
	}
	if _, err := env.run(t, "transient-watch", "--interval-ref", ref, "--duration", "30", "--root", "/etc"); err == nil {
		t.Fatal("transient-watch took a root")
	}
}

// A session that ends without the stop line is an abort: no document, an error, the readiness line at most, the
// observer told (so it writes nothing) and the lock released.
func TestEvidenceTransientWatchAbortsWhenTheSessionEndsWithoutAStopRequest(t *testing.T) {
	for name, stdin := range map[string]io.Reader{
		"stdin ends at once":     strings.NewReader(""),
		"another line":           strings.NewReader("go\n"),
		"stop without a newline": strings.NewReader("stop"),
		"a long line":            strings.NewReader(strings.Repeat("s", 4096)),
		"no stdin":               nil,
	} {
		env := newTransientEnv(0)
		env.stdin = stdin
		out, err := env.run(t, "transient-watch", "--interval-ref", "r#1", "--duration", "30")
		if err == nil || env.aborted != 1 || env.locked != 0 {
			t.Errorf("%s: err=%v aborted=%d locks=%d", name, err, env.aborted, env.locked)
		}
		if strings.Count(out, "\n") > 1 || strings.Contains(out, `"report"`) {
			t.Errorf("%s: a document was written for an aborted watch: %q", name, out)
		}
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

func rootListing(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// lockedBuf is a buffer a command can write while the test reads it.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type watchOutcome struct {
	res transientWatch
	err error
}

// startRealWatch runs the real observer over a temporary root in the background and returns once it acknowledged.
func startRealWatch(t *testing.T, ctx context.Context, root string, d time.Duration, stop <-chan error) <-chan watchOutcome {
	t.Helper()
	started := make(chan struct{})
	done := make(chan watchOutcome, 1)
	go func() {
		res, err := watchEvidenceRoot(ctx, root, d, func() error { close(started); return nil }, stop)
		done <- watchOutcome{res, err}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the observer never became ready")
	}
	return done
}

func waitWatch(t *testing.T, done <-chan watchOutcome) watchOutcome {
	t.Helper()
	select {
	case o := <-done:
		return o
	case <-time.After(20 * time.Second):
		t.Fatal("the watch never ended")
		return watchOutcome{}
	}
}

// The real observer over a real directory: the readiness line comes before the window, the host's stop request ends
// it, and the report is a qualified window that holds what happened in it, with the instrument's own canary traffic
// filtered out, and the window the observer measured.
func TestWatchEvidenceRootObservesTheRealTreeUntilTheHostStopsIt(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan error, 1)
	done := startRealWatch(t, context.Background(), root, time.Hour, stop)
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case o := <-done:
		t.Fatalf("the watch ended before the host asked: %+v", o)
	default:
	}
	stop <- nil
	o := waitWatch(t, done)
	if o.err != nil {
		t.Fatal(o.err)
	}
	rep := o.res.Report
	if rep.Verdict != effectoracle.VerdictOK || !rep.QualifiedWindow() {
		t.Fatalf("verdict %s gaps %+v: not a qualified window", rep.Verdict, rep.Gaps)
	}
	if o.res.Stop != runner.TransientStopRequested || o.res.Elapsed < 300*time.Millisecond || o.res.Elapsed > time.Minute {
		t.Fatalf("window %v stop %q: want the observed 300 ms and more, ended on request", o.res.Elapsed, o.res.Stop)
	}
	seen := false
	for _, ev := range rep.Events {
		if ev.Path == "new.txt" {
			seen = true
		}
		if strings.HasPrefix(ev.Path, ".effectoracle") {
			t.Errorf("canary traffic in the events: %+v", ev)
		}
	}
	if !seen {
		t.Fatalf("the write is not in the report: %+v", rep.Events)
	}
	if left := rootListing(t, root); len(left) != 2 {
		t.Fatalf("the canary was not removed: %v", left)
	}
	// A root that is not a directory is an error, not a report.
	if _, err := watchEvidenceRoot(context.Background(), filepath.Join(root, "a.txt"), time.Second, func() error { return nil }, make(chan error)); err == nil {
		t.Fatal("a file was watched")
	}
}

// Every way the session can go away before the host asked ends the watch with an error and no report, and nothing is
// written into the root afterwards: the root holds what it held, and keeps holding it.
func TestWatchEvidenceRootWritesNothingWhenTheSessionIsGone(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	for name, tc := range map[string]struct {
		ctx  context.Context
		fire func(stop chan error)
	}{
		"stdin ended without a stop line": {context.Background(), func(stop chan error) { stop <- errors.New("stdin ended without a stop request") }},
		"a signal":                        {cancelled, func(chan error) { cancel() }},
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
			t.Fatal(err)
		}
		events := newTap(t, root)
		stop := make(chan error, 1)
		done := startRealWatch(t, tc.ctx, root, time.Hour, stop)
		if started := events.drain(); len(started) == 0 {
			t.Fatalf("%s: the tap saw no start canary", name)
		}
		tc.fire(stop)
		o := waitWatch(t, done)
		if o.err == nil || o.res.Report != nil {
			t.Errorf("%s: err=%v report=%v, want an error and no report", name, o.err, o.res.Report)
		}
		time.Sleep(150 * time.Millisecond)
		if after := events.drain(); len(after) != 0 {
			t.Errorf("%s: the aborted watch touched the root: %v", name, after)
		}
		if left := rootListing(t, root); len(left) != 1 || left[0] != "a.txt" {
			t.Fatalf("%s: the root holds %v after the watch ended", name, left)
		}
	}
	// The readiness line cannot be written: the session is gone, so no stop canary either.
	root := t.TempDir()
	events := newTap(t, root)
	_, err := watchEvidenceRoot(context.Background(), root, time.Hour, func() error { events.drain(); return io.ErrClosedPipe }, make(chan error))
	if err == nil {
		t.Fatal("a watch whose readiness line failed went on")
	}
	time.Sleep(150 * time.Millisecond)
	if after := events.drain(); len(after) != 0 {
		t.Fatalf("the watch touched the root after its readiness line failed: %v", after)
	}
}

// A session that is already gone when the cap expires does not get a stop canary either; one that is still there gets
// its report, marked as ended by the timer (the host does not take that as covering the request).
func TestWatchEvidenceRootTimerCapIsReportedAsSuch(t *testing.T) {
	root := t.TempDir()
	stop := make(chan error, 1)
	done := startRealWatch(t, context.Background(), root, 100*time.Millisecond, stop)
	o := waitWatch(t, done)
	if o.err != nil || o.res.Stop != runner.TransientStopTimer || !o.res.Report.QualifiedWindow() {
		t.Fatalf("timer stop: %+v", o)
	}
	gone := make(chan error, 1)
	gone <- errors.New("stdin ended without a stop request")
	o = waitWatch(t, startRealWatch(t, context.Background(), t.TempDir(), 50*time.Millisecond, gone))
	if o.err == nil || o.res.Report != nil {
		t.Fatalf("a session that was gone at the cap got a report: %+v", o)
	}
}

// The whole subcommand over the real observer and a real pipe for its stdin: the stop line gives the document,
// closing the pipe gives an error and writes nothing into the root.
func TestEvidenceTransientWatchEndToEndOverARealPipe(t *testing.T) {
	saved := evidenceLiveRoots
	defer func() { evidenceLiveRoots = saved }()
	for _, withStop := range []bool{true, false} {
		root := t.TempDir()
		evidenceLiveRoots = []string{root}
		pr, pw := io.Pipe()
		env := newTransientEnv(0)
		env.stdin = pr
		env.watch = watchEvidenceRoot
		var out lockedBuf
		errc := make(chan error, 1)
		go func() {
			errc <- runEvidence(context.Background(), []string{"transient-watch", "--interval-ref", "r#1", "--duration", "60"}, &out, env.evidenceEnv)
		}()
		deadline := time.After(10 * time.Second)
		for {
			if strings.Contains(out.String(), "\n") {
				break
			}
			select {
			case <-deadline:
				t.Fatal("no readiness line")
			case <-time.After(10 * time.Millisecond):
			}
		}
		if withStop {
			_, _ = io.WriteString(pw, runner.TransientStopLine)
		}
		_ = pw.Close()
		select {
		case err := <-errc:
			if withStop {
				if err != nil || strings.Count(out.String(), "\n") != 2 {
					t.Fatalf("stop: err=%v stdout=%q", err, out.String())
				}
				var doc runner.TransientDoc
				lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
				if derr := runner.DecodeEvidenceDoc([]byte(lines[1]), &doc); derr != nil || doc.Stop != runner.TransientStopRequested ||
					doc.ElapsedMicros <= 0 || !doc.Report.QualifiedWindow() {
					t.Fatalf("document = %+v, %v", doc, derr)
				}
			} else if err == nil || strings.Count(out.String(), "\n") != 1 {
				t.Fatalf("eof: err=%v stdout=%q", err, out.String())
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the command did not end")
		}
		if env.locked != 0 {
			t.Fatalf("the lock is still held")
		}
		if !withStop {
			if left := rootListing(t, root); len(left) != 0 {
				t.Fatalf("an aborted watch left %v in the root", left)
			}
		}
	}
}

// tap is an inotify watch of the test's own on a directory: it keeps every event, so a file that was created and
// removed again within milliseconds (a canary) still shows.
type tap struct {
	t  *testing.T
	fd int
}

func newTap(t *testing.T, dir string) *tap {
	t.Helper()
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Skipf("inotify is unavailable here (%v): this test verifies nothing without it", err)
	}
	mask := uint32(syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MODIFY | syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_TO |
		syscall.IN_MOVED_FROM | syscall.IN_ATTRIB)
	if _, err := syscall.InotifyAddWatch(fd, dir, mask); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	return &tap{t: t, fd: fd}
}

// drain returns the names of the events since the last call.
func (p *tap) drain() []string {
	var names []string
	buf := make([]byte, 64<<10)
	for {
		n, err := syscall.Read(p.fd, buf)
		if err != nil || n <= 0 {
			return names
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			length := int(binary.LittleEndian.Uint32(buf[off+12 : off+16]))
			name := strings.TrimRight(string(buf[off+syscall.SizeofInotifyEvent:off+syscall.SizeofInotifyEvent+length]), "\x00")
			names = append(names, name)
			off += syscall.SizeofInotifyEvent + length
		}
	}
}
