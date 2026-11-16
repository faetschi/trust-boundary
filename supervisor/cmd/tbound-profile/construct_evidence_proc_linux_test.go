//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
)

// The pilot has no PROC case, so these tests turn one pilot case into a PROC witness. Every interval is synthetic and P
// only: D41 keeps Configuration C outcomes of confirmatory cases out of the import.
const constructProcCase = constructWSCase

var procT0 = time.Unix(1_800_000_000, 0).UTC()

func asProcCase(t *testing.T, pred, delta string) map[string]func(*evalcorpus.Case) {
	return map[string]func(*evalcorpus.Case){constructProcCase: func(c *evalcorpus.Case) {
		c.Adversarial.ForbiddenEffects, c.Adversarial.EffectJoin = []string{pred}, ""
		c.Adversarial.OracleID, c.Adversarial.OracleVersion = "procoracle", runner.ProcOracleVersion
		trial := *c.Trial
		trial.PDelta = delta
		c.Trial = &trial
		digest, err := evalcorpus.CalibrationPayloadDigestOf(trial.Script, trial.Pivots, delta)
		if err != nil {
			t.Fatal(err)
		}
		c.Adversarial.CalibrationPayloadDigest = digest
	}}
}

func procSnap(reg procoracle.Registration, seq int, point procoracle.Point, at time.Duration, procs []procoracle.Process, socks []procoracle.Socket) procoracle.Snapshot {
	return procoracle.Snapshot{Seq: seq, Point: point, ObservedAt: procT0.Add(at), RegistrationDigest: reg.Digest(), IdentityScanned: true, ScannedPIDs: 5,
		PIDNamespace: "pid:[4026531836]", CellPath: reg.CellCgroupPath, CellInode: reg.CellCgroupInode, Populated: true,
		Registry: procoracle.Registry{Recorded: true}, Processes: procs, Sockets: socks}
}

// procInputs is a no-reap/v1 P interval as the collector retains it: the lingering cell init in every capture and, unless
// negative, a descendant that survives the deadline, holding a named unix listener for EFF-PROC-03.
func procInputs(interval, part string, negative bool) procoracle.Inputs {
	reg := procoracle.Registration{CellID: "/sys/fs/cgroup/cells", CellCgroupPath: "/cells", CellCgroupInode: 4242, IntervalStart: procT0,
		Deadline: procT0.Add(2 * time.Second), CellUID: 1000, CellGID: 1000, ServiceUID: 995, MaxGap: procoracle.GuestMaxGap}
	init := procoracle.Process{PID: 100, StartTicks: 1, PPID: 50, State: "S", Comm: procoracle.CellInitComm, CgroupPath: "/cells", InCell: true,
		CellIdentity: true, UserNS: []uint64{11, 10}, NSPid: []int{100, 1}}
	procs := []procoracle.Process{init}
	var socks []procoracle.Socket
	if !negative {
		survivor := procoracle.Process{PID: 200, StartTicks: 2, PPID: 100, State: "S", Comm: "node", CgroupPath: "/cells", InCell: true,
			CellIdentity: true, UserNS: []uint64{11, 10}, NSPid: []int{200, 7}}
		procs = append(procs, survivor)
		if part == runner.OracleProc03 {
			socks = []procoracle.Socket{{Owner: survivor.ID(), Proto: "unix", Local: "/tmp/c7.sock", Inode: 77, Role: procoracle.RoleListener}}
		}
	}
	in := procoracle.Inputs{Schema: procoracle.InputsSchema, Interval: interval, Registration: reg,
		Snapshots: []procoracle.Snapshot{procSnap(reg, 1, procoracle.PointPostTeardown, 3200*time.Millisecond, procs, nil),
			procSnap(reg, 2, procoracle.PointFinalCleanup, 3600*time.Millisecond, procs, socks)},
		Policy: procoracle.Policy{LingeringInit: true, AuthorizedSockets: []string{}}}
	for i := 1; i <= 6; i++ {
		in.Polls = append(in.Polls, procSnap(reg, i, procoracle.PointMonitor, time.Duration(i)*500*time.Millisecond, []procoracle.Process{init}, nil))
	}
	return in
}

// fillProc retains a PROC interval the way the collector does and journals its findings under the bound reference.
func (w *constructWorld) fillProc(s runner.Step, o *runner.Outcome, part string, negative bool) (runner.Finding, runner.OracleRef) {
	t := w.t
	interval := runner.IntervalRef(s)
	in := procInputs(interval, part, negative)
	stamped := in.Registration.Digest()
	if w.opts.procCapture != nil {
		w.opts.procCapture(&in)
	}
	reg := in.Registration.Digest()
	for _, list := range [][]procoracle.Snapshot{in.Snapshots, in.Polls} {
		for i := range list {
			if list[i].RegistrationDigest == stamped {
				list[i].RegistrationDigest = reg
			}
		}
	}
	// A no-reap interval is never proven closed: its survivors keep the monitor to the horizon.
	term := procoracle.Termination{Schema: procoracle.TerminationSchema, Interval: interval, RunID: "run-1", Registration: reg,
		Unproven: []string{"a command cell outlived its command"}, ClosureDigest: strings.Repeat("c", 64),
		Stop: procoracle.MonitorStop{Reason: procoracle.StopHorizon}, PollCount: len(in.Polls)}
	rep := procoracle.Evaluate(in.Registration, in.Snapshots, in.Polls, in.Policy)
	digest, err := rep.Digest()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.ev, "run", "proc-evidence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, procoracle.InputsName(interval)), in)
	writeJSON(t, filepath.Join(dir, procoracle.RetainedName(interval)), term)
	bound := procoracle.BindDigest(digest, term.Digest())
	for key, f := range procoracle.Findings(rep) {
		o.Oracles[key], o.OracleRefs[key] = f, runner.OracleRef{Version: runner.ProcOracleVersion, Report: bound}
	}
	if w.opts.procOutcome != nil {
		w.opts.procOutcome(o)
	}
	return o.Oracles[part], o.OracleRefs[part]
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func procFiles(ev string) (inputs, term string) {
	step := runner.Step{CaseID: constructProcCase, Configuration: runner.CfgP, Role: runner.RoleStaticReplay, Attempt: 1}
	dir := filepath.Join(ev, "run", "proc-evidence")
	return filepath.Join(dir, procoracle.InputsName(runner.IntervalRef(step))), filepath.Join(dir, procoracle.RetainedName(runner.IntervalRef(step)))
}

func TestConstructProcWitnessImportsFromRetainedInputs(t *testing.T) {
	for _, pred := range []string{runner.OracleProc01, runner.OracleProc03} {
		for _, tc := range []struct {
			negative bool
			status   string
		}{{false, "pass"}, {true, "fail"}} {
			t.Run(pred+map[bool]string{false: " observed", true: " none"}[tc.negative], func(t *testing.T) {
				opts := worldOpts{witness: true, only: []string{constructProcCase}, mutate: asProcCase(t, pred, evalcorpus.PDeltaNoReap)}
				if tc.negative {
					opts.pNegative = []string{constructProcCase}
				}
				w := buildWorld(t, opts)
				r := w.apply(w.ev, constructProcCase)
				for _, check := range []string{"calibration-p", "effect-satisfiable"} {
					row := rowOf(t, r, constructProcCase, check)
					wantRow(t, row, tc.status, "recomputed from the retained process inputs")
					inputs, term := procFiles(w.ev)
					for _, f := range []string{inputs, term} {
						rel, _ := filepath.Rel(w.ev, f)
						if !slices.ContainsFunc(row.Provenance, func(s corpusadmit.ConstructSource) bool { return s.Path == filepath.ToSlash(rel) }) {
							t.Fatalf("%s: provenance %v lacks %s", check, row.Provenance, rel)
						}
					}
				}
			})
		}
	}
}

// D48: no construction reaches EFF-PROC-02, so its P witness is never imported, observed or not.
func TestConstructProc02WitnessIsNotImportable(t *testing.T) {
	w := buildWorld(t, worldOpts{witness: true, only: []string{constructProcCase}, mutate: asProcCase(t, runner.OracleProc02, evalcorpus.PDeltaNoReap)})
	wantRow(t, rowOf(t, w.apply(w.ev, constructProcCase), constructProcCase, "calibration-p"), "guest-only", "EFF-PROC-02 is untested")
}

// A changed retained file no longer recomputes the journaled reference, and a capture whose own reference agrees with
// it is still refused when it breaks a rule the run config or the case fixes.
func TestConstructProcEvidenceIsVerified(t *testing.T) {
	editInputs := func(edit func(*procoracle.Inputs)) func(*testing.T, string) {
		return func(t *testing.T, ev string) {
			path, _ := procFiles(ev)
			editFile(t, path, func(raw []byte) []byte {
				in, err := procoracle.ParseInputs(raw)
				if err != nil {
					t.Fatal(err)
				}
				edit(&in)
				out, _ := json.Marshal(in)
				return out
			})
		}
	}
	editTerm := func(edit func(*procoracle.Termination)) func(*testing.T, string) {
		return func(t *testing.T, ev string) {
			_, path := procFiles(ev)
			editFile(t, path, func(raw []byte) []byte {
				term, err := procoracle.ParseRetained(raw)
				if err != nil {
					t.Fatal(err)
				}
				edit(&term)
				out, _ := json.Marshal(term)
				return out
			})
		}
	}
	survivor := func(in *procoracle.Inputs) *procoracle.Process { return &in.Snapshots[1].Processes[1] }
	for _, tc := range []struct {
		name    string
		pred    string
		delta   string
		capture func(*procoracle.Inputs)
		journal func(*runner.Outcome)
		tamper  func(*testing.T, string)
		reason  string
	}{
		{name: "changed snapshot", tamper: editInputs(func(in *procoracle.Inputs) { survivor(in).Comm = "sleep" }), reason: "digest differs"},
		{name: "changed poll", tamper: editInputs(func(in *procoracle.Inputs) { in.Polls[2].ScannedPIDs++ }), reason: "digest differs"},
		{name: "changed policy", tamper: editInputs(func(in *procoracle.Inputs) { in.Policy.Unresolved = []string{"x"} }), reason: "digest differs"},
		{name: "socket authorized after the run", pred: runner.OracleProc03, tamper: editInputs(func(in *procoracle.Inputs) {
			in.Policy.AuthorizedSockets = []string{"unix /tmp/c7.sock"}
		}), reason: "authorized sockets"},
		{name: "inputs of another interval", tamper: editInputs(func(in *procoracle.Inputs) { in.Interval += "x" }), reason: "another interval"},
		{name: "inputs missing", tamper: func(t *testing.T, ev string) { p, _ := procFiles(ev); _ = os.Remove(p) }, reason: "proc-interval"},
		{name: "inputs of another schema", tamper: func(t *testing.T, ev string) {
			p, _ := procFiles(ev)
			editFile(t, p, func(b []byte) []byte {
				return []byte(strings.Replace(string(b), procoracle.InputsSchema, "tbound-proc-interval/v0", 1))
			})
		}, reason: "malformed"},
		{name: "termination missing", tamper: func(t *testing.T, ev string) { _, p := procFiles(ev); _ = os.Remove(p) }, reason: "proc-closure"},
		{name: "termination names another registration", tamper: editTerm(func(term *procoracle.Termination) { term.Registration = strings.Repeat("d", 64) }),
			reason: "another registration digest"},
		{name: "changed termination", tamper: editTerm(func(term *procoracle.Termination) { term.RunID = "run-9" }), reason: "digest differs"},
		{name: "journaled finding the inputs do not give", journal: func(o *runner.Outcome) { o.Oracles[runner.OracleProc02] = runner.FindingObserved },
			reason: "contradict the P oracle outcome"},
		{name: "another reference on a key outside the case", journal: func(o *runner.Outcome) {
			o.OracleRefs[runner.OracleProc03] = runner.OracleRef{Version: runner.ProcOracleVersion, Report: strings.Repeat("f", 64)}
		}, reason: "digest differs"},
		{name: "no proc_dir in the map", tamper: func(t *testing.T, ev string) {
			m := constructMap{}
			raw, _ := os.ReadFile(filepath.Join(ev, constructMapName))
			_ = json.Unmarshal(raw, &m)
			m.ProcDir = ""
			raw, _ = json.Marshal(m)
			_ = os.WriteFile(filepath.Join(ev, constructMapName), raw, 0o600)
		}, reason: "proc_dir"},
		{name: "snapshot captured under another registration", capture: func(in *procoracle.Inputs) {
			in.Snapshots[1].RegistrationDigest = strings.Repeat("e", 64)
		}, reason: "not taken under it"},
		{name: "deadline is not start plus the teardown grace", capture: func(in *procoracle.Inputs) {
			in.Registration.Deadline = in.Registration.Deadline.Add(-time.Second)
		}, reason: "deadline"},
		{name: "cell uid is not the configured one", capture: func(in *procoracle.Inputs) { in.Registration.CellUID = 1001 }, reason: "cell identity"},
		{name: "cell gid is not the configured one", capture: func(in *procoracle.Inputs) { in.Registration.CellGID = 1001 }, reason: "cell identity"},
		{name: "MaxGap is not the guest's", capture: func(in *procoracle.Inputs) { in.Registration.MaxGap = time.Minute }, reason: "MaxGap"},
		{name: "invalid registration", capture: func(in *procoracle.Inputs) { in.Registration.ServiceUID = 0 }, reason: "registration is invalid"},
		{name: "survivor in a cell user namespace without the cell identity", capture: func(in *procoracle.Inputs) {
			survivor(in).CellIdentity = false
		}, reason: "lacks the cell identity"},
		{name: "cell identity without a cell user namespace", capture: func(in *procoracle.Inputs) {
			survivor(in).UserNS = []uint64{12}
		}, reason: "two levels deep"},
		{name: "no-reap interval judged without the lingering init", capture: func(in *procoracle.Inputs) { in.Policy.LingeringInit = false }, reason: "lingering_init is false"},
		{name: "lingering init claimed outside no-reap", delta: evalcorpus.PDeltaNone, reason: "lingering_init is true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pred, delta := runner.OracleProc01, evalcorpus.PDeltaNoReap
			if tc.pred != "" {
				pred = tc.pred
			}
			if tc.delta != "" {
				delta = tc.delta
			}
			w := buildWorld(t, worldOpts{witness: true, only: []string{constructProcCase}, mutate: asProcCase(t, pred, delta), procCapture: tc.capture, procOutcome: tc.journal})
			ev := w.ev
			if tc.tamper != nil {
				ev = w.copyEvidence()
				tc.tamper(t, ev)
			}
			row := rowOf(t, w.apply(ev, constructProcCase), constructProcCase, "calibration-p")
			wantRow(t, row, "guest-only", tc.reason)
		})
	}
}
