package procoracle

import (
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/runner"
)

var closureReg = GuestRegistration(sshCell, sshT0, sshT0.Add(time.Second))

func settledCommand() CommandSettlement {
	return CommandSettlement{LeaseID: "lease-1", ReceiptDigest: strings.Repeat("a", 64), NetworkNSInode: 4026532001,
		ContainerExitInspected: true, FinalCommandProofRead: true, PodmanWaitCompleted: true, OutputWritersDrained: true,
		ContainerRemoved: true, LeaderPidfdExited: true, CgroupPopulatedZero: true, MountNamespaceReleased: true,
		CommandOutputWithinBound: true, CommandCgroupRemoved: true}
}

func cleanClosureDoc() ClosureDoc {
	return ClosureDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcClosure, IntervalRef: sshRef.Interval,
		RunID: "run-1", RegistrationDigest: closureReg.Digest(), ReceiptVerified: true, ReceiptDigest: strings.Repeat("b", 64),
		ExpectedSettlements: 1, Commands: []CommandSettlement{settledCommand()}, NetNSGone: true,
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
		"netns still held":              func(d *ClosureDoc) { d.NetNSGone = false },
		"cells child remains":           func(d *ClosureDoc) { d.CellChildren = 1 },
		"identity scan not done":        func(d *ClosureDoc) { d.Scan.IdentityScanned = false },
		"scan gap":                      func(d *ClosureDoc) { d.Scan.Gaps = []Gap{gapf(DomainSocket, "x", "y")} },
		"scan of another cell":          func(d *ClosureDoc) { d.Scan.CellInode++ },
		"scan bound to another reg":     func(d *ClosureDoc) { d.Scan.RegistrationDigest = strings.Repeat("d", 64) },
		"scan before the deadline":      func(d *ClosureDoc) { d.Scan.ObservedAt = sshT0 },
		"cell populated":                func(d *ClosureDoc) { d.Scan.Populated = true },
		"cell removed":                  func(d *ClosureDoc) { d.Scan.CellRemoved = true },
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
