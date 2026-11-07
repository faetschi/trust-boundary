//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/wsoracle"
)

const (
	evTestCgroup    = "/sys/fs/cgroup/system.slice/tbound-pi.service/cells"
	evTestHierarchy = "/system.slice/tbound-pi.service/cells"
	evTestInode     = 4242
)

func evTestCell() evidenceCell {
	return evidenceCell{ID: evTestCgroup, CgroupPath: evTestCgroup, Hierarchy: evTestHierarchy, Inode: evTestInode, UID: 1000, GID: 1000}
}

// evTestRegistration is the registration the host would compute for the fixture cell.
func evTestRegistration() (procoracle.Registration, time.Time, time.Time) {
	start := time.Unix(1_700_000_000, 5).UTC()
	deadline := start.Add(2 * time.Second)
	return procoracle.GuestRegistration(evTestCell().doc(), start, deadline), start, deadline
}

type evTestEnv struct {
	evidenceEnv
	traces     map[string]audit.Trace
	reads      []string
	captured   []string
	monitors   int
	locks      int // monitor locks currently held
	lockErr    error
	nPolls     int         // polls the fake monitor returns (default 1)
	awaitStop  bool        // the fake monitor waits (briefly) for a stop request before it reaches its horizon
	canaries   [][2]string // (phase, tag) of every canary the fake sink was asked to journal
	canaryErr  error
	netJournal []byte
	netReads   int
}

func newEvTestEnv(uid int) *evTestEnv {
	e := &evTestEnv{traces: map[string]audit.Trace{}}
	e.evidenceEnv = evidenceEnv{
		geteuid: func() int { return uid },
		getenv:  func(string) string { return "" },
		snapshotDir: func(dir *os.File, label string) wsoracle.Manifest {
			return wsoracle.Manifest{Label: label, Complete: true, Dev: 3, Entries: []wsoracle.Entry{},
				Root: &wsoracle.Entry{Type: wsoracle.TypeDir, Mode: 0o700, UID: 995, GID: 986, Nlink: 2, Ino: 42}}
		},
		cell: func(context.Context) (evidenceCell, error) { return evTestCell(), nil },
		pinRoot: func(context.Context, string) (*os.File, uint64, uint64, error) {
			return nil, 3, 42, nil
		},
		capture: func(c evidenceCell, reg procoracle.Registration, p procoracle.Point, seq int) (procoracle.Snapshot, error) {
			e.captured = append(e.captured, string(p))
			return procoracle.Snapshot{Seq: seq, Point: p, RegistrationDigest: reg.Digest(), CellPath: c.Hierarchy, CellInode: c.Inode,
				IdentityScanned: true, ScannedPIDs: 3, Registry: procoracle.Registry{Recorded: true}, ObservedAt: time.Unix(1_700_000_003, 0).UTC()}, nil
		},
		cellChildren: func(evidenceCell) (int, error) { return 0, nil },
		netnsHeld:    func([]uint64) (bool, error) { return false, nil },
		hostNetNS:    func() (uint64, error) { return 4026531840, nil },
		monitor: func(_ context.Context, c evidenceCell, reg procoracle.Registration, d, interval time.Duration, ready func(procoracle.Snapshot),
			stop <-chan procoracle.MonitorStopRequest) ([]procoracle.Snapshot, *procoracle.MonitorStopRequest, error) {
			e.monitors++
			n := max(e.nPolls, 1)
			polls := make([]procoracle.Snapshot, n)
			for i := range polls {
				polls[i] = procoracle.Snapshot{Seq: i + 1, Point: procoracle.PointMonitor, RegistrationDigest: reg.Digest(), CellPath: c.Hierarchy, CellInode: c.Inode,
					IdentityScanned: true, ScannedPIDs: 3, Registry: procoracle.Registry{Recorded: true}, ObservedAt: time.Unix(1_700_000_001, 0).UTC()}
			}
			ready(polls[0])
			if e.awaitStop {
				select {
				case req := <-stop:
					return polls, &req, nil
				case <-time.After(300 * time.Millisecond): // the fake's horizon
				}
			}
			return polls, nil, nil
		},
		lockMonitor: func() (func(), error) {
			if e.lockErr != nil {
				return nil, e.lockErr
			}
			e.locks++
			return func() { e.locks-- }, nil
		},
		netCanary: func(_ context.Context, phase, tag string) (netoracle.CanaryAck, error) {
			if e.canaryErr != nil {
				return netoracle.CanaryAck{}, e.canaryErr
			}
			e.canaries = append(e.canaries, [2]string{phase, tag})
			return netoracle.CanaryAck{Seq: uint64(len(e.canaries)) + 10, Hash: strings.Repeat("e", 64)}, nil
		},
		netJournal: func() ([]byte, error) {
			e.netReads++
			if e.netJournal == nil {
				return nil, os.ErrNotExist
			}
			return e.netJournal, nil
		},
		readTrace: func(path string, mustExist bool) (audit.Trace, error) {
			e.reads = append(e.reads, path)
			tr, ok := e.traces[path]
			if !ok && mustExist {
				return audit.Trace{}, os.ErrNotExist
			}
			return tr, nil
		},
	}
	return e
}

// evTestTrace builds a verified audit trace of events.
func evTestTrace(t *testing.T, events ...audit.Event) audit.Trace {
	t.Helper()
	b, err := intervalJournalBytes(events)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := audit.Verify(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func runEvTest(t *testing.T, env *evTestEnv, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runEvidence(context.Background(), args, &out, env.evidenceEnv)
	return out.String(), err
}

func singleDoc(t *testing.T, out string, v any) {
	t.Helper()
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout is not exactly one line: %q", out)
	}
	if err := runner.DecodeEvidenceDoc([]byte(out), v); err != nil {
		t.Fatalf("stdout is not one evidence document: %v\n%s", err, out)
	}
}

func TestEvidenceE04SnapshotOfTheRegisteredRootOnEveryPoint(t *testing.T) {
	if !reflect.DeepEqual(evidenceLiveRoots, []string{piruntime.D09ManagedPiLiveRootPath}) || runner.EvidenceLiveRoot != piruntime.D09ManagedPiLiveRootPath {
		t.Fatalf("the root allowlist %q is not the managed live root %q", evidenceLiveRoots, piruntime.D09ManagedPiLiveRootPath)
	}
	if runner.EvidenceBinaryPath != "/usr/local/bin/tbound" {
		t.Fatal("evidence binary path")
	}
	for _, p := range evidencePoints {
		env := newEvTestEnv(0)
		out, err := runEvTest(t, env, "e04-snapshot", "--root", piruntime.D09ManagedPiLiveRootPath, "--point", p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		var doc runner.E04SnapshotDoc
		singleDoc(t, out, &doc)
		if doc.Schema != runner.EvidenceSchema || doc.Kind != "e04-snapshot" || doc.Point != p || doc.Root != piruntime.D09ManagedPiLiveRootPath ||
			doc.Manifest.Label != p || doc.Manifest.Root == nil || doc.Manifest.Root.Ino != 42 {
			t.Fatalf("%s: doc = %+v", p, doc)
		}
	}
	if len(evidencePoints) != 7 {
		t.Fatalf("points = %v", evidencePoints)
	}
	// flag order is free
	if _, err := runEvTest(t, newEvTestEnv(0), "e04-snapshot", "--point", "baseline", "--root", piruntime.D09ManagedPiLiveRootPath); err != nil {
		t.Fatalf("flags in the other order: %v", err)
	}
}

func TestOpenPinnedLiveRootRefusesARenamedRootAndADecoy(t *testing.T) {
	dir := t.TempDir()
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(filepath.Dir(dir), "decoy")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(decoy, dir); err != nil {
		t.Fatal(err)
	}
	if fd, err := openPinnedLiveRoot(dir, uint64(st.Dev), st.Ino); err == nil {
		syscall.Close(fd)
		t.Fatal("openPinnedLiveRoot accepted a decoy at the registered root's path")
	}
	fd, err := openPinnedLiveRoot(dir+"-old", uint64(st.Dev), st.Ino)
	if err != nil {
		t.Fatalf("openPinnedLiveRoot of the real root under its new name: %v", err)
	}
	syscall.Close(fd)
}

func TestEvidenceE04SnapshotRefusesARootThatIsNotPinned(t *testing.T) {
	env := newEvTestEnv(0)
	env.pinRoot = func(context.Context, string) (*os.File, uint64, uint64, error) {
		return nil, 0, 0, errors.New("device 1 inode 2, want device 3 inode 4")
	}
	if _, err := runEvTest(t, env, "e04-snapshot", "--root", piruntime.D09ManagedPiLiveRootPath, "--point", "baseline"); err == nil ||
		!strings.Contains(err.Error(), "not the registered live root") {
		t.Fatalf("e04-snapshot of an unpinned root: %v", err)
	}
}

// TestEvidenceE04SnapshotSnapshotsThroughThePinnedDescriptor is the path race: the root is pinned
// once, and the manifest is taken through that descriptor, so a decoy at the path afterwards is
// not what the snapshot reads.
func TestEvidenceE04SnapshotSnapshotsThroughThePinnedDescriptor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kept.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		t.Fatal(err)
	}
	env := newEvTestEnv(0)
	held, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	env.pinRoot = func(context.Context, string) (*os.File, uint64, uint64, error) {
		return held, uint64(st.Dev), st.Ino, nil
	}
	env.snapshotDir = wsoracle.SnapshotDir
	// The pin holds the original directory. Swapping the path now must not change what is read.
	decoy := filepath.Join(filepath.Dir(dir), "decoy")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+"-held"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(decoy, dir); err != nil {
		t.Fatal(err)
	}
	out, err := runEvTest(t, env, "e04-snapshot", "--root", piruntime.D09ManagedPiLiveRootPath, "--point", "baseline")
	if err != nil {
		t.Fatalf("e04-snapshot through the pinned descriptor: %v", err)
	}
	var doc runner.E04SnapshotDoc
	singleDoc(t, out, &doc)
	if doc.Manifest.Dev != uint64(st.Dev) || doc.Manifest.Root == nil || doc.Manifest.Root.Ino != st.Ino {
		t.Fatalf("the manifest is not the pinned root: %+v", doc.Manifest)
	}
	found := false
	for _, e := range doc.Manifest.Entries {
		if e.Path == "kept.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the snapshot did not read the pinned tree: %+v", doc.Manifest.Entries)
	}
}

// TestEvidenceE04SnapshotRefusesADecoySnapshot: if the descriptor does not read the registered
// device and inode (a decoy was opened), the snapshot is refused rather than published as the
// live root.
func TestEvidenceE04SnapshotRefusesADecoySnapshot(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(filepath.Dir(dir), "decoy")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	var original syscall.Stat_t
	if err := syscall.Stat(dir, &original); err != nil {
		t.Fatal(err)
	}
	env := newEvTestEnv(0)
	env.pinRoot = func(context.Context, string) (*os.File, uint64, uint64, error) {
		// The descriptor is the decoy, but the identity is the registered root's.
		f, err := os.Open(decoy)
		return f, uint64(original.Dev), original.Ino, err
	}
	env.snapshotDir = wsoracle.SnapshotDir
	if _, err := runEvTest(t, env, "e04-snapshot", "--root", piruntime.D09ManagedPiLiveRootPath, "--point", "baseline"); err == nil ||
		!strings.Contains(err.Error(), "not the registered live root") {
		t.Fatalf("e04-snapshot of a decoy descriptor: %v", err)
	}
}

func TestEvidenceRefusesEverythingOutsideTheAllowlists(t *testing.T) {
	live := piruntime.D09ManagedPiLiveRootPath
	cases := map[string][]string{
		"no kind":                  {},
		"unknown kind":             {"shell"},
		"kind with a flag shape":   {"--root", live},
		"help is not a kind":       {"help"},
		"root /etc":                {"e04-snapshot", "--root", "/etc", "--point", "baseline"},
		"parent of the root":       {"e04-snapshot", "--root", "/var/lib/tbound", "--point", "baseline"},
		"root with a trailing /":   {"e04-snapshot", "--root", live + "/", "--point", "baseline"},
		"root below the live root": {"e04-snapshot", "--root", live + "/sub", "--point", "baseline"},
		"dotdot root":              {"e04-snapshot", "--root", live + "/../../etc", "--point", "baseline"},
		"relative root":            {"e04-snapshot", "--root", "live", "--point", "baseline"},
		"unknown point":            {"e04-snapshot", "--root", live, "--point", "midnight"},
		"missing point":            {"e04-snapshot", "--root", live},
		"extra flag":               {"e04-snapshot", "--root", live, "--point", "baseline", "--out", "/tmp/x"},
		"extra positional":         {"e04-snapshot", "--root", live, "--point", "baseline", "x"},
		"flag given twice":         {"e04-snapshot", "--root", live, "--root", live},
		"equals form":              {"e04-snapshot", "--root=" + live, "--point", "baseline"},
		"flag of another kind":     {"e04-snapshot", "--root", live, "--interval-ref", "x"},
		"value is a flag":          {"e04-snapshot", "--root", "--point", "--point", "baseline"},
		"shell metacharacter":      {"publication-interval-journal", "--interval-ref", "a;rm -rf /"},
		"space in value":           {"publication-interval-journal", "--interval-ref", "a b"},
		"newline in value":         {"publication-interval-journal", "--interval-ref", "a\nb"},
		"empty value":              {"publication-interval-journal", "--interval-ref", ""},
		"long value":               {"publication-interval-journal", "--interval-ref", strings.Repeat("a", 257)},
		"proc point interval":      {"proc-capture", "--registration", strings.Repeat("a", 64), "--point", "interval", "--interval-start", "1", "--deadline", "2"},
		"proc point monitor":       {"proc-capture", "--registration", strings.Repeat("a", 64), "--point", "monitor", "--interval-start", "1", "--deadline", "2"},
		"cgroup flag":              {"proc-capture", "--registration", strings.Repeat("a", 64), "--point", "post_teardown", "--cell", "/sys/fs/cgroup/x", "--deadline", "2"},
		"monitor duration zero":    {"proc-monitor", "--registration", strings.Repeat("a", 64), "--interval-start", "1", "--deadline", "2", "--duration", "0", "--interval", "500"},
		"monitor duration huge":    {"proc-monitor", "--registration", strings.Repeat("a", 64), "--interval-start", "1", "--deadline", "2", "--duration", "99999", "--interval", "500"},
		"monitor interval tiny":    {"proc-monitor", "--registration", strings.Repeat("a", 64), "--interval-start", "1", "--deadline", "2", "--duration", "5", "--interval", "1"},
		"monitor signed duration":  {"proc-monitor", "--registration", strings.Repeat("a", 64), "--interval-start", "1", "--deadline", "2", "--duration", "+5", "--interval", "500"},
	}
	for name, args := range cases {
		env := newEvTestEnv(0)
		out, err := runEvTest(t, env, args...)
		if err == nil || out != "" {
			t.Errorf("%s: err=%v stdout=%q, want a refusal with no output", name, err, out)
		}
		if len(env.reads) != 0 || len(env.captured) != 0 || env.monitors != 0 {
			t.Errorf("%s: the guest was touched before the refusal", name)
		}
	}
}

func TestEvidenceRefusesAnyUidButTheEvidenceUid(t *testing.T) {
	if evidenceRequiredUID != 0 {
		t.Fatalf("the evidence uid is %d, documented as root", evidenceRequiredUID)
	}
	for _, uid := range []int{1, 994, 995, 1000, 65534} {
		env := newEvTestEnv(uid)
		for _, args := range [][]string{
			{"e04-snapshot", "--root", piruntime.D09ManagedPiLiveRootPath, "--point", "baseline"},
			{"publication-interval-journal", "--interval-ref", "a"},
			{"no-such-kind"},
		} {
			out, err := runEvTest(t, env, args...)
			if err == nil || out != "" || !strings.Contains(err.Error(), "uid") {
				t.Errorf("uid %d %v: err=%v stdout=%q", uid, args, err, out)
			}
		}
		env.getenv = func(string) string {
			return "/usr/local/bin/tbound evidence e04-snapshot --root /var/lib/tbound/live/root --point baseline"
		}
		if out, err := runEvTest(t, env, "ssh-original-command"); err == nil || out != "" {
			t.Errorf("uid %d: forced command ran: %v %q", uid, err, out)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := evidenceMain([]string{"e04-snapshot", "--root", piruntime.D09ManagedPiLiveRootPath, "--point", "baseline"}, &stdout, &stderr, newEvTestEnv(1000).evidenceEnv); code != 2 || stdout.Len() != 0 {
		t.Fatalf("exit %d, stdout %q", code, stdout.String())
	}
	// help goes to stderr and never to stdout.
	stdout.Reset()
	stderr.Reset()
	if code := evidenceMain([]string{"help"}, &stdout, &stderr, newEvTestEnv(1000).evidenceEnv); code != 0 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), "read-only") || !strings.Contains(stderr.String(), "root only") {
		t.Fatalf("help: exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestEvidenceProcCaptureAndMonitorBindToTheServiceRegistration(t *testing.T) {
	reg, start, deadline := evTestRegistration()
	capArgs := func(digest, point string) []string {
		return []string{"proc-capture", "--registration", digest, "--point", point,
			"--interval-start", procoracle.InstantArg(start), "--deadline", procoracle.InstantArg(deadline)}
	}
	for point, seq := range map[string]int{"post_teardown": 1, "final_cleanup": 2} {
		env := newEvTestEnv(0)
		out, err := runEvTest(t, env, capArgs(reg.Digest(), point)...)
		if err != nil {
			t.Fatalf("%s: %v", point, err)
		}
		var doc procoracle.CaptureDoc
		singleDoc(t, out, &doc)
		if doc.Schema != runner.EvidenceSchema || doc.Kind != "proc-capture" || string(doc.Point) != point || doc.RegistrationDigest != reg.Digest() ||
			doc.Snapshot.Seq != seq || doc.Snapshot.RegistrationDigest != reg.Digest() || doc.Snapshot.CellPath != evTestHierarchy || doc.Snapshot.CellInode != evTestInode {
			t.Fatalf("%s: doc = %+v", point, doc)
		}
	}

	// A digest that is not the service's registered cell is refused before anything is observed.
	for name, args := range map[string][]string{
		"other digest":             capArgs(strings.Repeat("0", 64), "post_teardown"),
		"digest of 2nd uid":        capArgs(evTestRegFor(func(c *evidenceCell) { c.UID = 1001 }, start, deadline), "post_teardown"),
		"digest of other cell":     capArgs(evTestRegFor(func(c *evidenceCell) { c.ID = "/sys/fs/cgroup/other" }, start, deadline), "post_teardown"),
		"digest of other deadline": capArgs(evTestRegFor(nil, start, deadline.Add(time.Second)), "post_teardown"),
		"upper-case digest":        capArgs(strings.ToUpper(reg.Digest()), "post_teardown"),
		"short digest":             capArgs(reg.Digest()[:63], "post_teardown"),
	} {
		env := newEvTestEnv(0)
		out, err := runEvTest(t, env, args...)
		if err == nil || out != "" || len(env.captured) != 0 {
			t.Errorf("%s: err=%v stdout=%q captured=%v", name, err, out, env.captured)
		}
	}
	// An unresolvable cell, and instants that are not instants, are refused.
	env := newEvTestEnv(0)
	env.cell = func(context.Context) (evidenceCell, error) { return evidenceCell{}, errors.New("no registration") }
	if out, err := runEvTest(t, env, capArgs(reg.Digest(), "post_teardown")...); err == nil || out != "" {
		t.Errorf("unresolvable cell: %v %q", err, out)
	}
	env = newEvTestEnv(0)
	bad := capArgs(reg.Digest(), "post_teardown")
	bad[5] = "12.5"
	if _, err := runEvTest(t, env, bad...); err == nil {
		t.Error("a fractional instant was accepted")
	}
	bad = capArgs(reg.Digest(), "post_teardown")
	bad[7] = procoracle.InstantArg(start.Add(-time.Second))
	if _, err := runEvTest(t, env, bad...); err == nil {
		t.Error("a deadline before the start was accepted")
	}

	// The monitor: line one acknowledges the first poll, line two is the final document.
	env = newEvTestEnv(0)
	out, err := runEvTest(t, env, "proc-monitor", "--registration", reg.Digest(),
		"--interval-start", procoracle.InstantArg(start), "--deadline", procoracle.InstantArg(deadline), "--duration", "30", "--interval", "500")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(out, "\n")
	if len(lines) != 3 || lines[2] != "" {
		t.Fatalf("monitor stdout is not exactly two lines: %q", out)
	}
	var ready procoracle.MonitorReadyDoc
	if err := runner.DecodeEvidenceDoc([]byte(lines[0]), &ready); err != nil {
		t.Fatalf("first line: %v", err)
	}
	if ready.Schema != runner.EvidenceSchema || ready.Kind != procoracle.MonitorReadyKind || ready.RegistrationDigest != reg.Digest() ||
		ready.FirstPollSeq != 1 || !ready.FirstPollAt.Equal(time.Unix(1_700_000_001, 0).UTC()) {
		t.Fatalf("ready doc = %+v", ready)
	}
	var mon procoracle.MonitorDoc
	if err := runner.DecodeEvidenceDoc([]byte(lines[1]), &mon); err != nil {
		t.Fatalf("second line: %v", err)
	}
	if mon.Kind != "proc-monitor" || mon.RegistrationDigest != reg.Digest() || len(mon.Polls) != 1 || env.monitors != 1 || env.locks != 0 {
		t.Fatalf("monitor doc = %+v (locks held after the run: %d)", mon, env.locks)
	}
	env = newEvTestEnv(0)
	if out, err := runEvTest(t, env, "proc-monitor", "--registration", strings.Repeat("1", 64),
		"--interval-start", procoracle.InstantArg(start), "--deadline", procoracle.InstantArg(deadline), "--duration", "30", "--interval", "500"); err == nil || out != "" || env.monitors != 0 {
		t.Fatalf("monitor with a wrong digest: %v %q %d", err, out, env.monitors)
	}
}

func TestEvidenceCellComesFromTheRegistrationAndTemplateOnly(t *testing.T) {
	reg := piruntime.D09ManagedLiveRootRegistration{ServiceCgroupPath: piruntime.D09ManagedPiServiceCgroupPath, Digest: "sha256:" + strings.Repeat("c", 64)}
	tmpl := piruntime.D09CellTemplate{CgroupsPath: evTestCgroup, CellUID: 1000, CellGID: 1001, ManagedLiveRootRegistrationDigest: reg.Digest}
	cell, err := evidenceCellFrom(reg, tmpl)
	if err != nil || cell.UID != 1000 || cell.GID != 1001 || cell.CgroupPath != evTestCgroup || cell.ID != evTestCgroup || cell.Hierarchy != evTestHierarchy {
		t.Fatalf("cell = %+v, %v", cell, err)
	}
	for name, mutate := range map[string]func(*piruntime.D09ManagedLiveRootRegistration, *piruntime.D09CellTemplate){
		"other cgroup": func(_ *piruntime.D09ManagedLiveRootRegistration, tm *piruntime.D09CellTemplate) {
			tm.CgroupsPath = "/sys/fs/cgroup/tbound"
		},
		"cgroup of the whole": func(_ *piruntime.D09ManagedLiveRootRegistration, tm *piruntime.D09CellTemplate) {
			tm.CgroupsPath = "/sys/fs/cgroup"
		},
		"other service": func(r *piruntime.D09ManagedLiveRootRegistration, _ *piruntime.D09CellTemplate) {
			r.ServiceCgroupPath = "/system.slice/other.service"
		},
		"uid unset": func(_ *piruntime.D09ManagedLiveRootRegistration, tm *piruntime.D09CellTemplate) { tm.CellUID = 0 },
		"gid unset": func(_ *piruntime.D09ManagedLiveRootRegistration, tm *piruntime.D09CellTemplate) { tm.CellGID = 0 },
		"unbound template": func(_ *piruntime.D09ManagedLiveRootRegistration, tm *piruntime.D09CellTemplate) {
			tm.ManagedLiveRootRegistrationDigest = "sha256:" + strings.Repeat("d", 64)
		},
	} {
		r, tm := reg, tmpl
		mutate(&r, &tm)
		if _, err := evidenceCellFrom(r, tm); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ---- publication interval journal ----

const evTestCaseID = "case-ev"

func evTestAdmission(workflow, admissionID string) audit.Event {
	entry := piServiceWorkflowEntry{SchemaVersion: workflowCatalogSchema, HostAdmissionID: admissionID, TaskID: evTestCaseID,
		WorkflowID: workflow, CorpusManifestDigest: "sha256:" + strings.Repeat("e", 64)}
	data, _ := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: strings.Repeat("f", 64), Entry: entry})
	return audit.Event{Kind: "pi_workflow_admission_reserved", ID: "reserved/" + admissionID, Data: data}
}

func evTestPublicationEvent(kind, id string, fields map[string]any) audit.Event {
	data, _ := json.Marshal(fields)
	return audit.Event{Kind: kind, ID: id, Data: data}
}

func TestEvidencePublicationJournalIsTheServicesOwnAnchorDerivation(t *testing.T) {
	ref, ok := corpusIntervalRef(evTestCaseID+"/trajectory/1", evTestCaseID)
	if !ok {
		t.Fatal("no interval ref")
	}
	catalog := evTestTrace(t,
		evTestAdmission("wf-other", evTestCaseID+"/trajectory/2"),
		evTestAdmission("wf-mine", evTestCaseID+"/trajectory/1"))
	epoch := evTestPublicationEvent("publication_owner_epoch", "wf-mine/1", map[string]any{"workflow_id": "wf-mine", "owner_epoch": 1})
	prepare := evTestPublicationEvent("publication_prepare", "tx-1", map[string]any{"workflow_id": "wf-mine", "transaction_id": "tx-1"})
	foreign := evTestPublicationEvent("publication_prepare", "tx-9", map[string]any{"workflow_id": "wf-other", "transaction_id": "tx-9"})
	terminal := evTestPublicationEvent("publication_terminal", "tx-1", map[string]any{"transaction_id": "tx-1"})

	for name, tc := range map[string]struct {
		journal []audit.Event
		count   int
	}{
		"no publication journal":     {nil, 0},
		"owner epoch only":           {[]audit.Event{epoch}, 0},
		"one transaction":            {[]audit.Event{epoch, prepare, foreign, terminal}, 2},
		"only another workflow's":    {[]audit.Event{foreign}, 0},
		"unrelated kinds in between": {[]audit.Event{{Kind: "pi_service_note", ID: "n1"}, prepare, terminal}, 2},
	} {
		env := newEvTestEnv(0)
		env.traces[piruntime.D09ManagedPiWorkflowCatalogPath] = catalog
		if tc.journal != nil {
			env.traces[filepath.Join(productionAuditRootDefault, "publication-audit.jsonl")] = evTestTrace(t, tc.journal...)
		}
		out, err := runEvTest(t, env, "publication-interval-journal", "--interval-ref", ref)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var doc runner.PublicationJournalDoc
		singleDoc(t, out, &doc)
		pubs := env.traces[filepath.Join(productionAuditRootDefault, "publication-audit.jsonl")]
		want, err := publicationAnchorFor(pubs, "wf-mine", ref)
		if err != nil {
			t.Fatal(err)
		}
		if doc.Head != want.Head || doc.Count != want.Count || doc.Count != tc.count || doc.IntervalRef != ref || doc.Kind != "publication-interval-journal" {
			t.Fatalf("%s: doc = %+v, anchor %+v", name, doc, want)
		}
		raw, err := base64.StdEncoding.DecodeString(doc.JournalB64)
		if err != nil {
			t.Fatal(err)
		}
		trace, err := audit.Verify(bytes.NewReader(raw))
		if err != nil || len(trace.Records) == 0 || trace.Records[0].Event.Kind != runner.IntervalMarkerKind || trace.Records[0].Event.ID != ref ||
			trace.Records[len(trace.Records)-1].Hash != doc.Head {
			t.Fatalf("%s: journal does not verify as the interval chain: %v", name, err)
		}
	}
}

func TestEvidencePublicationJournalRefusesWhatItCannotAnchor(t *testing.T) {
	ref, _ := corpusIntervalRef(evTestCaseID+"/trajectory/1", evTestCaseID)
	pubPath := filepath.Join(productionAuditRootDefault, "publication-audit.jsonl")
	good := evTestTrace(t, evTestAdmission("wf-mine", evTestCaseID+"/trajectory/1"))
	orphan := evTestTrace(t, evTestPublicationEvent("publication_terminal", "tx-x", map[string]any{"transaction_id": "tx-x"}))
	for name, setup := range map[string]func(*evTestEnv){
		"no catalog":   func(e *evTestEnv) {},
		"no admission": func(e *evTestEnv) { e.traces[piruntime.D09ManagedPiWorkflowCatalogPath] = evTestTrace(t) },
		"other attempt": func(e *evTestEnv) {
			e.traces[piruntime.D09ManagedPiWorkflowCatalogPath] = evTestTrace(t, evTestAdmission("w", evTestCaseID+"/trajectory/2"))
		},
		"two admissions": func(e *evTestEnv) {
			e.traces[piruntime.D09ManagedPiWorkflowCatalogPath] = evTestTrace(t, evTestAdmission("w1", evTestCaseID+"/trajectory/1"), evTestAdmission("w2", evTestCaseID+"/trajectory/1"))
		},
		"unattributable publication record": func(e *evTestEnv) {
			e.traces[piruntime.D09ManagedPiWorkflowCatalogPath] = good
			e.traces[pubPath] = orphan
		},
		"unreadable catalog record": func(e *evTestEnv) {
			e.traces[piruntime.D09ManagedPiWorkflowCatalogPath] = evTestTrace(t, audit.Event{Kind: "pi_workflow_admission_reserved", ID: "r", Data: []byte("{")})
		},
		"read error": func(e *evTestEnv) {
			e.traces[piruntime.D09ManagedPiWorkflowCatalogPath] = good
			e.readTrace = func(string, bool) (audit.Trace, error) { return audit.Trace{}, errors.New("EIO") }
		},
	} {
		env := newEvTestEnv(0)
		setup(env)
		if out, err := runEvTest(t, env, "publication-interval-journal", "--interval-ref", ref); err == nil || out != "" {
			t.Errorf("%s: err=%v stdout=%q", name, err, out)
		}
	}
}

func TestEvidenceReadsOnlyAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	j := filepath.Join(dir, "j.jsonl")
	b, _ := intervalJournalBytes([]audit.Event{{Kind: "k", ID: "1"}, {Kind: "k", ID: "2"}})
	if err := os.WriteFile(j, b, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(j)
	tr, err := readEvidenceTrace(j, true)
	if err != nil || len(tr.Records) != 2 {
		t.Fatalf("trace = %d records, %v", len(tr.Records), err)
	}
	after, _ := os.Stat(j)
	entries, _ := os.ReadDir(dir)
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() || len(entries) != 1 {
		t.Fatalf("the journal or its directory changed: %v", entries)
	}
	// Missing: empty when optional, an error when required, and never created.
	missing := filepath.Join(dir, "none.jsonl")
	if tr, err := readEvidenceTrace(missing, false); err != nil || len(tr.Records) != 0 {
		t.Fatalf("optional missing journal: %v %v", tr, err)
	}
	if _, err := readEvidenceTrace(missing, true); err == nil {
		t.Fatal("a required journal was missing without error")
	}
	if _, err := os.Lstat(missing); err == nil {
		t.Fatal("reading created the journal")
	}
	// A symlink, a directory and a corrupt or torn journal are refused.
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(j, link); err == nil {
		if _, err := readEvidenceTrace(link, true); err == nil {
			t.Fatal("a symlink was followed")
		}
	}
	if _, err := readEvidenceTrace(dir, true); err == nil {
		t.Fatal("a directory was read as a journal")
	}
	torn := filepath.Join(dir, "torn.jsonl")
	_ = os.WriteFile(torn, b[:len(b)-5], 0o600)
	if _, err := readEvidenceTrace(torn, true); err == nil {
		t.Fatal("a torn journal was accepted")
	}
	// The subcommand itself holds no write path: the env has no writer, and a whole run
	// leaves the working directory untouched.
	cwd := t.TempDir()
	t.Chdir(cwd)
	env := newEvTestEnv(0)
	if _, err := runEvTest(t, env, "e04-snapshot", "--root", piruntime.D09ManagedPiLiveRootPath, "--point", "baseline"); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(cwd); len(left) != 0 {
		t.Fatalf("evidence wrote %v", left)
	}
}

// ---- the forced-command entry ----

func TestEvidenceForcedCommandRunsOnlyAnEvidenceCommand(t *testing.T) {
	live := piruntime.D09ManagedPiLiveRootPath
	// What SSHTransport sends: every word single-quoted.
	quoted := `'/usr/local/bin/tbound' 'evidence' 'e04-snapshot' '--root' '` + live + `' '--point' 'baseline'`
	plain := `/usr/local/bin/tbound evidence e04-snapshot --root ` + live + ` --point baseline`
	for _, original := range []string{quoted, plain} {
		env := newEvTestEnv(0)
		env.getenv = func(k string) string {
			if k != "SSH_ORIGINAL_COMMAND" {
				t.Fatalf("read the environment variable %s", k)
			}
			return original
		}
		out, err := runEvTest(t, env, "ssh-original-command")
		if err != nil {
			t.Fatalf("%q: %v", original, err)
		}
		var doc runner.E04SnapshotDoc
		singleDoc(t, out, &doc)
		if doc.Point != "baseline" {
			t.Fatalf("doc = %+v", doc)
		}
	}
	for name, original := range map[string]string{
		"empty":             "",
		"a shell":           "/bin/sh",
		"other binary":      "/bin/cat evidence e04-snapshot --root " + live + " --point baseline",
		"relative binary":   "tbound evidence e04-snapshot --root " + live + " --point baseline",
		"no kind":           "/usr/local/bin/tbound evidence",
		"not evidence":      "/usr/local/bin/tbound serve --pi",
		"semicolon":         plain + "; id",
		"and":               plain + " && id",
		"pipe word":         plain + " | sh",
		"command subst":     "/usr/local/bin/tbound evidence e04-snapshot --root $(id) --point baseline",
		"backtick":          "/usr/local/bin/tbound evidence e04-snapshot --root `id` --point baseline",
		"double space":      "/usr/local/bin/tbound  evidence e04-snapshot --root " + live + " --point baseline",
		"newline":           plain + "\nid",
		"trailing space":    plain + " ",
		"redirect":          plain + " > /tmp/x",
		"double quote":      `/usr/local/bin/tbound evidence e04-snapshot --root "` + live + `" --point baseline`,
		"quote inside":      `/usr/local/bin/tbound evidence e04-snapshot --root '/var/lib/tbound'live' --point baseline`,
		"nested forced":     "/usr/local/bin/tbound evidence ssh-original-command",
		"allowlist applies": "/usr/local/bin/tbound evidence e04-snapshot --root /etc --point baseline",
		"too long":          "/usr/local/bin/tbound evidence e04-snapshot --root " + strings.Repeat("a", 3000),
	} {
		env := newEvTestEnv(0)
		env.getenv = func(string) string { return original }
		if out, err := runEvTest(t, env, "ssh-original-command"); err == nil || out != "" {
			t.Errorf("%s: err=%v stdout=%q", name, err, out)
		}
	}
	// The forced-command kind itself takes no argument.
	env := newEvTestEnv(0)
	env.getenv = func(string) string { return plain }
	if _, err := runEvTest(t, env, "ssh-original-command", "--root", live); err == nil {
		t.Fatal("ssh-original-command took an argument")
	}
}

func TestEvidenceUsageNamesEveryKindAndTheRootRequirement(t *testing.T) {
	for kind := range evidenceFlags {
		if !strings.Contains(evidenceUsage, kind) {
			t.Errorf("usage does not mention %s", kind)
		}
	}
	for _, flag := range []string{"--root", "--point", "--registration", "--interval-ref", "--duration"} {
		if !strings.Contains(evidenceUsage, flag) {
			t.Errorf("usage does not mention %s", flag)
		}
	}
}

// evTestRegFor is the registration of the fixture cell after mutate.
func evTestRegFor(mutate func(*evidenceCell), start, deadline time.Time) string {
	c := evTestCell()
	if mutate != nil {
		mutate(&c)
	}
	return procoracle.GuestRegistration(c.doc(), start, deadline).Digest()
}

func TestEvidenceProcCellIsTheServicesRegisteredCell(t *testing.T) {
	env := newEvTestEnv(0)
	out, err := runEvTest(t, env, "proc-cell")
	if err != nil {
		t.Fatal(err)
	}
	var doc procoracle.CellDoc
	singleDoc(t, out, &doc)
	if doc != evTestCell().doc() || doc.CellCgroupPath != evTestHierarchy || doc.CellCgroupInode != evTestInode || doc.CellUID != 1000 {
		t.Fatalf("doc = %+v", doc)
	}
	// It takes no argument, and a cell that cannot be resolved is an error, not an empty cell.
	if _, err := runEvTest(t, env, "proc-cell", "--cell", "/sys/fs/cgroup/x"); err == nil {
		t.Fatal("proc-cell took an argument")
	}
	env.cell = func(context.Context) (evidenceCell, error) { return evidenceCell{}, errors.New("no registration") }
	if out, err := runEvTest(t, env, "proc-cell"); err == nil || out != "" {
		t.Fatalf("unresolvable cell: %v %q", err, out)
	}
	// The registration the guest binds a capture to carries the cell's path and inode.
	reg, _, _ := evTestRegistration()
	if reg.CellCgroupPath != evTestHierarchy || reg.CellCgroupInode != evTestInode {
		t.Fatalf("registration = %+v", reg)
	}
}

// Codex 00f77c6 (4): one hour at 50 ms retained ~72k scans, unbounded in number and size,
// with any number of monitors at once.
func TestEvidenceProcMonitorIsBoundedAndExclusive(t *testing.T) {
	reg, start, deadline := evTestRegistration()
	args := func(duration, interval string) []string {
		return []string{"proc-monitor", "--registration", reg.Digest(), "--interval-start", procoracle.InstantArg(start),
			"--deadline", procoracle.InstantArg(deadline), "--duration", duration, "--interval", interval}
	}
	if evidenceMaxMonitorSeconds != 1500 || evidenceMinMonitorMillis != 200 || int(procoracle.MaxGuestMonitorDuration/time.Second) != evidenceMaxMonitorSeconds ||
		int(procoracle.MinGuestPollInterval/time.Millisecond) != evidenceMinMonitorMillis {
		t.Fatalf("guest monitor bounds %d s / %d ms disagree with the host's", evidenceMaxMonitorSeconds, evidenceMinMonitorMillis)
	}
	for name, a := range map[string][]string{
		"too long":        args("1501", "500"),
		"an hour":         args("3600", "500"),
		"too fast":        args("30", "199"),
		"50 ms":           args("30", "50"),
		"too slow":        args("30", "1001"),
		"zero seconds":    args("0", "500"),
		"leading zeros":   args("030", "500"),
		"largest allowed": args("1500", "200"), // 7500 samples is within the cap and must pass
	} {
		env := newEvTestEnv(0)
		out, err := runEvTest(t, env, a...)
		if name == "largest allowed" {
			if err != nil {
				t.Errorf("%s: the largest allowed monitor was refused: %v", name, err)
			}
			continue
		}
		if err == nil || out != "" || env.monitors != 0 || env.locks != 0 {
			t.Errorf("%s: err=%v stdout=%q monitors=%d", name, err, out, env.monitors)
		}
	}

	// A second monitor is refused while one holds the lock, before anything is polled.
	env := newEvTestEnv(0)
	env.lockErr = errors.New("another proc-monitor is running")
	if out, err := runEvTest(t, env, args("30", "500")...); err == nil || out != "" || env.monitors != 0 {
		t.Fatalf("second monitor: err=%v stdout=%q monitors=%d", err, out, env.monitors)
	}

	// A monitor that kept more samples than the cap, or more bytes, is an error, not a document.
	env = newEvTestEnv(0)
	env.nPolls = evidenceMaxMonitorSamples + 1
	if _, err := runEvTest(t, env, args("30", "500")...); err == nil {
		t.Error("an over-long poll record was written")
	}
	env = newEvTestEnv(0)
	env.nPolls = 50
	defer func(old int) { evidenceMaxMonitorBytes = old }(evidenceMaxMonitorBytes)
	evidenceMaxMonitorBytes = 1024
	if _, err := runEvTest(t, env, args("30", "500")...); err == nil {
		t.Error("an over-sized poll record was written")
	}
}

func TestEvidenceMonitorLockIsExclusiveRootOwnedAndReleased(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.lock")
	uid := uint32(os.Geteuid())
	release, err := lockEvidenceMonitorFile(path, uid)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file: %v %v", info, err)
	}
	if second, err := lockEvidenceMonitorFile(path, uid); err == nil {
		second()
		t.Fatal("a second monitor took the lock")
	}
	release()
	again, err := lockEvidenceMonitorFile(path, uid)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
	// Not owned by the required uid: refused.
	if r, err := lockEvidenceMonitorFile(path, uid+1); err == nil {
		r()
		t.Error("a lock file of another owner was accepted")
	}
	// A symlink is never followed; a loose mode is refused.
	link := filepath.Join(dir, "link.lock")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if r, err := lockEvidenceMonitorFile(link, uid); err == nil {
		r()
		t.Error("a symlink lock path was followed")
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if r, err := lockEvidenceMonitorFile(path, uid); err == nil {
		r()
		t.Error("a world-writable lock file was accepted")
	}
}
