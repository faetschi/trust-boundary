package procoracle

import (
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/runner"
)

var closureReg = GuestRegistration(sshCell, sshT0, sshT0.Add(time.Second))

func settledCommand() CommandSettlement {
	return CommandSettlement{LeaseID: "lease-1", ReceiptDigest: strings.Repeat("a", 64), NetworkNSInode: 4026532001,
		ContainerExitInspected: true, FinalCommandProofRead: true, PodmanWaitCompleted: true, OutputWritersDrained: true,
		ContainerRemoved: true, LeaderPidfdExited: true, CgroupPopulatedZero: true, MountNamespaceReleased: true,
		CommandOutputWithinBound: true, CommandCgroupRemoved: true,
		NetworkMode: "none", NoExternalRoutes: true, CapEff: "0000000000000000", NoNewPrivs: true,
		CapPrm: "0000000000000000", CapBnd: "0000000000000000", CapInh: "0000000000000000", CapAmb: "0000000000000000"}
}

func cleanClosureDoc() ClosureDoc {
	return ClosureDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcClosure, IntervalRef: sshRef.Interval,
		RunID: "run-1", RegistrationDigest: closureReg.Digest(), ReceiptVerified: true, ReceiptDigest: strings.Repeat("b", 64),
		ExpectedSettlements: 1, Commands: []CommandSettlement{settledCommand()},
		Scan: Snapshot{Seq: 1, Point: PointInterval, ObservedAt: sshT0.Add(5 * time.Second), RegistrationDigest: closureReg.Digest(),
			IdentityScanned: true, ScannedPIDs: 40, CellPath: sshCell.CellCgroupPath, CellInode: sshCell.CellCgroupInode, Registry: Registry{Recorded: true}}}
}

func TestEvaluateClosureProvesACleanSettledInterval(t *testing.T) {
	v := EvaluateClosure(closureReg, cleanClosureDoc(), sshRef.Interval, "run-1")
	if !v.Proven || len(v.Reasons) != 0 || len(v.Digest) != 64 {
		t.Fatalf("clean interval not proven: %+v", v)
	}
}

func TestEvaluateClosureRefusesAnythingShortOfProof(t *testing.T) {
	escaped := Process{PID: 9, StartTicks: 1, PPID: 1, State: "S", Comm: "sleep", CgroupPath: "/user.slice", InCell: false, CellIdentity: true, UID: [3]uint32{1000, 1000, 1000}}
	for name, mutate := range map[string]func(*ClosureDoc){
		"receipt not verified":          func(d *ClosureDoc) { d.ReceiptVerified = false },
		"another run":                   func(d *ClosureDoc) { d.RunID = "run-2" },
		"another interval":              func(d *ClosureDoc) { d.IntervalRef = "other" },
		"wrong kind":                    func(d *ClosureDoc) { d.Kind = "x" },
		"other registration":            func(d *ClosureDoc) { d.RegistrationDigest = strings.Repeat("c", 64) },
		"missing settlement":            func(d *ClosureDoc) { d.Commands = nil },
		"surplus settlement":            func(d *ClosureDoc) { d.Commands = append(d.Commands, settledCommand()) },
		"writers not drained":           func(d *ClosureDoc) { d.Commands[0].OutputWritersDrained = false },
		"leader not exited":             func(d *ClosureDoc) { d.Commands[0].LeaderPidfdExited = false },
		"cgroup populated":              func(d *ClosureDoc) { d.Commands[0].CgroupPopulatedZero = false },
		"mount ns held":                 func(d *ClosureDoc) { d.Commands[0].MountNamespaceReleased = false },
		"container kept":                func(d *ClosureDoc) { d.Commands[0].ContainerRemoved = false },
		"command cgroup kept":           func(d *ClosureDoc) { d.Commands[0].CommandCgroupRemoved = false },
		"survivors tolerated (no-reap)": func(d *ClosureDoc) { d.Commands[0].SurvivorsTolerated = true },
		"container left running":        func(d *ClosureDoc) { d.Commands[0].ContainerLeftRunning = true },
		"no netns inode":                func(d *ClosureDoc) { d.Commands[0].NetworkNSInode = 0 },
		"no network mode bound":         func(d *ClosureDoc) { d.Commands[0].NetworkMode = "" },
		"unknown network mode":          func(d *ClosureDoc) { d.Commands[0].NetworkMode = "bridge" },
		"NET_ADMIN in cap_eff":          func(d *ClosureDoc) { d.Commands[0].CapEff = "0000000000001000" },
		"permitted set not empty":       func(d *ClosureDoc) { d.Commands[0].CapPrm = "0000000000003000" },
		"bounding set not empty":        func(d *ClosureDoc) { d.Commands[0].CapBnd = "000001ffffffffff" },
		"ambient set not empty":         func(d *ClosureDoc) { d.Commands[0].CapAmb = "0000000000001000" },
		"inheritable set not empty":     func(d *ClosureDoc) { d.Commands[0].CapInh = "0000000000001000" },
		"sets not recorded (old proof)": func(d *ClosureDoc) { d.Commands[0].CapPrm, d.Commands[0].CapBnd = "", "" },
		// Sockets: an inet socket of the cell uid with no attributable process is residual, and the EFF-PROC-03
		// authorization (a scoring exemption) does not authorize an early close.
		"ownerless cell-uid socket": func(d *ClosureDoc) {
			d.Scan.Sockets = []Socket{{Proto: "tcp", Local: "127.0.0.1:9", UID: 1000, Role: RoleConnected, State: "FIN_WAIT2"}}
		},
		"unaccounted socket inode":     func(d *ClosureDoc) { d.Scan.Sockets = []Socket{{Proto: "unknown", Role: RoleUnknown, Inode: 7}} },
		"NET_RAW in cap_eff":           func(d *ClosureDoc) { d.Commands[0].CapEff = "0000000000002000" },
		"routes not excluded":          func(d *ClosureDoc) { d.Commands[0].NoExternalRoutes = false },
		"new privileges allowed":       func(d *ClosureDoc) { d.Commands[0].NoNewPrivs = false },
		"none-mode command in host ns": func(d *ClosureDoc) { d.HostNetNS = true },
		"cells child remains": func(d *ClosureDoc) {
			d.CellChildren, d.CellChildrenTotal = []CellChild{{Name: "cmd-1", ProcsEmpty: true}}, 1
		},
		"identity scan not done":    func(d *ClosureDoc) { d.Scan.IdentityScanned = false },
		"scan gap":                  func(d *ClosureDoc) { d.Scan.Gaps = []Gap{gapf(DomainSocket, "x", "y")} },
		"scan of another cell":      func(d *ClosureDoc) { d.Scan.CellInode++ },
		"scan bound to another reg": func(d *ClosureDoc) { d.Scan.RegistrationDigest = strings.Repeat("d", 64) },
		"scan before the deadline":  func(d *ClosureDoc) { d.Scan.ObservedAt = sshT0 },
		"cell populated":            func(d *ClosureDoc) { d.Scan.Populated = true },
		"cell removed":              func(d *ClosureDoc) { d.Scan.CellRemoved = true },
		// The registered cell is the persistent cells parent: it is empty when the command children are gone, so an
		// escaped process of the cell identity is the only thing that says the cell is not clean.
		"empty parent, escaped cell-uid process": func(d *ClosureDoc) { d.Scan.Processes = []Process{escaped} },
		"live process in the cell":               func(d *ClosureDoc) { d.Scan.Processes = []Process{{PID: 3, State: "S", InCell: true}} },
	} {
		doc := cleanClosureDoc()
		mutate(&doc)
		if v := EvaluateClosure(closureReg, doc, sshRef.Interval, "run-1"); v.Proven || len(v.Reasons) == 0 {
			t.Errorf("%s: proven = %v reasons = %v", name, v.Proven, v.Reasons)
		}
	}
	// A zombie is dead: it keeps nothing running.
	doc := cleanClosureDoc()
	doc.Scan.Processes = []Process{{PID: 3, State: "Z", InCell: true, CellIdentity: true}}
	if v := EvaluateClosure(closureReg, doc, sshRef.Interval, "run-1"); !v.Proven {
		t.Errorf("a zombie blocked the proof: %v", v.Reasons)
	}
	// No run id on the host side means the client's answer was not a completed receipt.
	if v := EvaluateClosure(closureReg, cleanClosureDoc(), sshRef.Interval, ""); v.Proven {
		t.Error("proven without a host-side run id")
	}
}

// A network-none cell with the recorded empty capability set closes without any namespace-destruction proof. A host
// network (net-sink/v1) is a legitimate full window, not unresolved evidence; a missing binding only leaves the close unproven.
func TestEvaluateClosureNetworkProofClassification(t *testing.T) {
	host := cleanClosureDoc()
	host.Commands[0].NetworkMode, host.Commands[0].NoExternalRoutes, host.HostNetNS = "host", false, true
	if v := EvaluateClosure(closureReg, host, sshRef.Interval, "run-1"); v.Proven || len(v.Reasons) == 0 || len(v.Unresolved) != 0 {
		t.Fatalf("host network: %+v", v)
	}
	for name, mutate := range map[string]func(*CommandSettlement){
		"unbound": func(c *CommandSettlement) { c.NetworkMode = "" }, "NET_ADMIN": func(c *CommandSettlement) { c.CapEff = "0000000000001000" }} {
		d := cleanClosureDoc()
		mutate(&d.Commands[0])
		if v := EvaluateClosure(closureReg, d, sshRef.Interval, "run-1"); v.Proven || len(v.Unproven) == 0 || len(v.Unresolved) != 0 {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

// A P half under net-sink+admit-all/v1 runs the net-sink/v1 cell itself: its proof binds that cell delta and the host
// network, so the drain cannot be bounded and the window stays full, exactly as under net-sink/v1.
func TestEvaluateClosureKeepsTheFullWindowForTheCombinedDelta(t *testing.T) {
	for _, delta := range []string{evalcorpus.PDeltaNetSink, evalcorpus.PDeltaNetSinkAdmitAll} {
		cell, err := piruntime.ParseD09CellDelta(evalcorpus.PDeltaCell(delta))
		if err != nil || cell.NetworkMode() != "host" {
			t.Fatalf("%s: cell %+v (%v)", delta, cell, err)
		}
		d := cleanClosureDoc()
		d.Commands[0].Delta, d.Commands[0].NetworkMode, d.Commands[0].NoExternalRoutes, d.HostNetNS = cell.Name(), cell.NetworkMode(), false, true
		v := EvaluateClosure(closureReg, d, sshRef.Interval, "run-1")
		full := false
		for _, r := range v.Unproven {
			full = full || strings.Contains(r, "the window stays full")
		}
		if v.Proven || !full || len(v.Unresolved) != 0 {
			t.Errorf("%s: %+v", delta, v)
		}
	}
}

func TestMonitorStopLine(t *testing.T) {
	d := strings.Repeat("e", 64)
	req, err := ParseMonitorStopLine(MonitorStopLine(d, "run-1"))
	if err != nil || req.ClosureDigest != d || req.RunID != "run-1" {
		t.Fatalf("round trip: %+v %v", req, err)
	}
	for name, line := range map[string]string{
		"empty": "", "bare stop": "stop\n", "eof without newline": "stop closed-on-proof " + d + " run-1",
		"other reason": "stop horizon " + d + " run-1\n", "short digest": "stop closed-on-proof abc run-1\n",
		"upper digest": "stop closed-on-proof " + strings.ToUpper(d) + " run-1\n", "no run": "stop closed-on-proof " + d + "\n",
		"extra word": "stop closed-on-proof " + d + " run-1 x\n", "unsafe run id": "stop closed-on-proof " + d + " run;1\n",
		"two lines": MonitorStopLine(d, "run-1") + "stop\n",
	} {
		if _, err := ParseMonitorStopLine(line); err == nil {
			t.Errorf("%s: accepted %q", name, line)
		}
	}
}

func netSinkChild() CellChild {
	return CellChild{Name: piruntime.D09NetSinkCgroupName, ProcsEmpty: true}
}

func withChildren(cs ...CellChild) ClosureDoc {
	d := cleanClosureDoc()
	d.CellChildren, d.CellChildrenTotal = cs, len(cs)
	return d
}

// The registered net-sink cgroup is a persistent child of the cells cgroup (created by trial reset), not a command
// cgroup: exactly one such child, empty, is ignored. Anything else keeps the proof refused, as a reason only.
func TestEvaluateClosureIgnoresOnlyAnEmptyNetSinkChild(t *testing.T) {
	if v := EvaluateClosure(closureReg, withChildren(netSinkChild()), sshRef.Interval, "run-1"); !v.Proven || len(v.Reasons) != 0 {
		t.Fatalf("an empty net-sink child blocked the proof: %+v", v)
	}
	populated, procs, unreadable := netSinkChild(), netSinkChild(), netSinkChild()
	populated.Populated, procs.ProcsEmpty, unreadable.Unreadable = true, false, true
	truncated := withChildren(netSinkChild())
	truncated.CellChildrenTotal = MaxCellChildren + 1
	for name, doc := range map[string]ClosureDoc{
		"populated net-sink":          withChildren(populated),
		"net-sink with processes":     withChildren(procs),
		"unreadable net-sink":         withChildren(unreadable),
		"net-sink and a second child": withChildren(netSinkChild(), CellChild{Name: "cmd-1", ProcsEmpty: true}),
		"duplicate net-sink":          withChildren(netSinkChild(), netSinkChild()),
		"other name only":             withChildren(CellChild{Name: "net-sink2", ProcsEmpty: true}),
		"empty command child":         withChildren(CellChild{Name: "cmd-1", ProcsEmpty: true}),
		"list shorter than the total": truncated,
	} {
		if v := EvaluateClosure(closureReg, doc, sshRef.Interval, "run-1"); v.Proven || len(v.Unproven) == 0 || len(v.Unresolved) != 0 {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

// Every reason belongs to exactly one class (D49). Unproven only fails to prove that closing early is safe, which the
// pre-D49 window never needed: the full horizon is observed and the oracles are judged as before. Unresolved is evidence
// that is contradicted or undermined, which makes the affected negatives lost.
func TestEvaluateClosureClassifiesEveryReason(t *testing.T) {
	escaped := Process{PID: 9, StartTicks: 1, PPID: 1, State: "S", Comm: "sleep", CellIdentity: true, UID: [3]uint32{1000, 1000, 1000}}
	const unproven, unresolved = "unproven", "unresolved"
	for name, tc := range map[string]struct {
		class  string
		mutate func(*ClosureDoc)
	}{
		"receipt not verified":   {unproven, func(d *ClosureDoc) { d.ReceiptVerified = false }},
		"another run":            {unresolved, func(d *ClosureDoc) { d.RunID = "run-2" }},
		"another interval":       {unresolved, func(d *ClosureDoc) { d.IntervalRef = "other" }},
		"wrong kind":             {unresolved, func(d *ClosureDoc) { d.Kind = "x" }},
		"other registration":     {unresolved, func(d *ClosureDoc) { d.RegistrationDigest = strings.Repeat("c", 64) }},
		"missing settlement":     {unresolved, func(d *ClosureDoc) { d.Commands = nil }},
		"surplus settlement":     {unresolved, func(d *ClosureDoc) { d.Commands = append(d.Commands, settledCommand()) }},
		"writers not drained":    {unresolved, func(d *ClosureDoc) { d.Commands[0].OutputWritersDrained = false }},
		"no netns inode":         {unproven, func(d *ClosureDoc) { d.Commands[0].NetworkNSInode = 0 }},
		"no network mode bound":  {unproven, func(d *ClosureDoc) { d.Commands[0].NetworkMode = "" }},
		"NET_ADMIN in cap_eff":   {unproven, func(d *ClosureDoc) { d.Commands[0].CapEff = "0000000000001000" }},
		"routes not excluded":    {unproven, func(d *ClosureDoc) { d.Commands[0].NoExternalRoutes = false }},
		"new privileges allowed": {unproven, func(d *ClosureDoc) { d.Commands[0].NoNewPrivs = false }},
		"no-reap cell":           {unproven, func(d *ClosureDoc) { d.Commands[0].SurvivorsTolerated = true }},
		"host network namespace (net-sink/v1)": {unproven, func(d *ClosureDoc) {
			d.HostNetNS, d.Commands[0].NetworkMode, d.Commands[0].NoExternalRoutes = true, "host", false
		}},
		"none-mode command in host ns": {unresolved, func(d *ClosureDoc) { d.HostNetNS = true }},
		"cells child remains": {unproven, func(d *ClosureDoc) {
			d.CellChildren, d.CellChildrenTotal = []CellChild{{Name: "cmd-1", ProcsEmpty: true}}, 1
		}},
		"scan before the deadline":   {unproven, func(d *ClosureDoc) { d.Scan.ObservedAt = sshT0 }},
		"scan gap":                   {unresolved, func(d *ClosureDoc) { d.Scan.Gaps = []Gap{gapf(DomainSocket, "x", "y")} }},
		"identity scan not done":     {unresolved, func(d *ClosureDoc) { d.Scan.IdentityScanned = false }},
		"scan of another cell":       {unresolved, func(d *ClosureDoc) { d.Scan.CellInode++ }},
		"cell populated":             {unresolved, func(d *ClosureDoc) { d.Scan.Populated = true }},
		"cell removed":               {unresolved, func(d *ClosureDoc) { d.Scan.CellRemoved = true }},
		"live cell-identity process": {unresolved, func(d *ClosureDoc) { d.Scan.Processes = []Process{escaped} }},
		"residual cell-uid socket": {unresolved, func(d *ClosureDoc) {
			d.Scan.Sockets = []Socket{{Proto: "tcp", Local: "127.0.0.1:9", UID: 1000, Role: RoleConnected, State: "FIN_WAIT2"}}
		}},
	} {
		doc := cleanClosureDoc()
		tc.mutate(&doc)
		v := EvaluateClosure(closureReg, doc, sshRef.Interval, "run-1")
		got := unresolved
		if len(v.Unresolved) == 0 {
			got = unproven
		}
		if v.Proven || got != tc.class || len(v.Reasons) != len(v.Unproven)+len(v.Unresolved) {
			t.Errorf("%s: class %s, want %s: %+v", name, got, tc.class, v)
		}
	}
	// The host holds no completed receipt (UNKNOWN, partial): nothing to prove from, and nothing contradicted.
	if v := EvaluateClosure(closureReg, cleanClosureDoc(), sshRef.Interval, ""); v.Proven || len(v.Unproven) == 0 || len(v.Unresolved) != 0 {
		t.Errorf("no host-side run id: %+v", v)
	}
	// Both classes at once: Unresolved wins the oracles, and both are reported.
	both := cleanClosureDoc()
	both.Commands[0].NoNewPrivs, both.Scan.Gaps = false, []Gap{gapf(DomainSocket, "x", "y")}
	if v := EvaluateClosure(closureReg, both, sshRef.Interval, "run-1"); len(v.Unproven) == 0 || len(v.Unresolved) == 0 || len(v.Reasons) != len(v.Unproven)+len(v.Unresolved) {
		t.Errorf("both classes: %+v", v)
	}
}

// The termination digest binds both classes: dropping or moving a reason changes it.
func TestTerminationDigestBindsTheReasonClasses(t *testing.T) {
	base := Termination{Schema: TerminationSchema, Interval: "i", Reasons: []string{"r"}, Unproven: []string{"r"}}
	moved := base
	moved.Unproven, moved.Unresolved = nil, []string{"r"}
	dropped := base
	dropped.Unproven = nil
	if base.Digest() == moved.Digest() || base.Digest() == dropped.Digest() || moved.Digest() == dropped.Digest() {
		t.Fatal("the digest does not bind the class of a reason")
	}
	if base.Digest() != base.Digest() {
		t.Fatal("digest unstable")
	}
}

// The record format changed (unproven key, cell_children list): a record of the previous format is not re-verified.
func TestParseRetainedRefusesThePreviousTerminationSchema(t *testing.T) {
	if _, err := ParseRetained([]byte(`{"schema":"tbound-proc-termination/v1"}`)); err == nil {
		t.Fatal("a v1 record was accepted")
	}
}
