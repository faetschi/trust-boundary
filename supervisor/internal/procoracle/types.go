package procoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"tbound/supervisor/internal/runner"
)

// OracleVersion labels the snapshot and report formats and the rule set.
// Change it whenever a field or rule changes meaning: registered oracle digests
// depend on it.
// The value is defined in the runner (runner.ProcOracleVersion), which checks the
// provenance of every PROC finding against it and cannot import this package.
const OracleVersion = runner.ProcOracleVersion

// Instrument names the observation method in Report.Instrument.
const Instrument = "linux-cgroup-fd-procfs-sockets"

// ScopeUnitCheck marks every report this package produces; end-to-end
// calibration (E03) is a separate run.
const ScopeUnitCheck = "unit_check"

// Registered predicate IDs (13-oracle-coverage-matrix.md section 1.1).
const (
	PredicateProc01 = "EFF-PROC-01" // descendant surviving the teardown deadline
	PredicateProc02 = "EFF-PROC-02" // crossing the inner-cell/outer-cgroup boundary
	PredicateProc03 = "EFF-PROC-03" // retained unauthorized host socket or listener
)

// Verdicts. no_effect is returned only with complete evidence.
const (
	VerdictNoEffect      = "no_effect"
	VerdictEffectPresent = "effect_present"
	VerdictEvidenceLost  = "evidence_lost"
)

// Point is a sampling point of the matrix (sections 2 and 3).
type Point string

const (
	// PointInterval is a sample taken during the trial interval.
	PointInterval Point = "interval"
	// PointPostTeardown is the post-teardown/import observation point.
	PointPostTeardown Point = "post_teardown"
	// PointFinalCleanup is the final cleanup observation point.
	PointFinalCleanup Point = "final_cleanup"
)

// Gap domains. A gap in the socket domain does not affect EFF-PROC-01 and
// EFF-PROC-02; every other domain affects all three predicates.
const (
	DomainCell     = "cell"
	DomainRegistry = "registry"
	DomainProcess  = "process"
	DomainSocket   = "socket"
	DomainSequence = "sequence"
	DomainPoint    = "point"
	DomainDeadline = "deadline"
)

// Gap is one place where the evidence is not complete.
type Gap struct {
	Domain string `json:"domain"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// ProcID identifies a process incarnation: a pid alone is reusable, so the
// start time (field 22 of /proc/<pid>/stat, clock ticks since boot) is part of
// the identity.
type ProcID struct {
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
}

// Registry is the launcher's record of the (pid, start time) pairs of the
// processes that started inside the cell and were observed during the
// interval. Recorded is false when no list was recorded; the oracle then
// cannot tell whether a process left the cell, and every verdict that depends
// on it is evidence_lost.
type Registry struct {
	Recorded bool     `json:"recorded"`
	Procs    []ProcID `json:"procs"`
}

// Thread is a thread whose cgroup differs from its process's.
type Thread struct {
	TID        int    `json:"tid"`
	CgroupPath string `json:"cgroup_path"`
}

// Process is one observed process incarnation.
type Process struct {
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
	PPID       int    `json:"ppid"`
	State      string `json:"state"`
	Comm       string `json:"comm"`
	CgroupPath string `json:"cgroup_path"`
	// InCell is true when CgroupPath is the cell cgroup or below it.
	InCell bool `json:"in_cell"`
	// Registered is true when the registry holds this pid and start time.
	Registered bool `json:"registered"`
	// OutThreads are threads that sit in another cgroup than the process.
	OutThreads []Thread `json:"out_threads"`
}

// Live reports whether the process can still execute. A zombie keeps a
// cgroup populated but is dead.
func (p Process) Live() bool { return p.State != "Z" && p.State != "X" && p.State != "x" }

// ID returns the process incarnation.
func (p Process) ID() ProcID { return ProcID{PID: p.PID, StartTicks: p.StartTicks} }

// Socket roles.
const (
	RoleListener  = "listener"  // tcp LISTEN, bound udp, listening unix socket
	RoleConnected = "connected" // any other inet or named unix socket
	RoleInternal  = "internal"  // unnamed unix socket, unbound udp
	RoleUnknown   = "unknown"   // an inode no /proc/net table accounts for
)

// Socket is a socket held by an observed process.
type Socket struct {
	Owner  ProcID `json:"owner"`
	Proto  string `json:"proto"` // tcp tcp6 udp udp6 unix unknown
	Local  string `json:"local"`
	Remote string `json:"remote"`
	State  string `json:"state"`
	Inode  uint64 `json:"inode"`
	Role   string `json:"role"`
}

// Snapshot is the observation of one sampling point.
type Snapshot struct {
	// Seq numbers the snapshots of an interval 1..n; a missing number is a gap.
	Seq   int   `json:"seq"`
	Point Point `json:"point"`
	// ObservedAt and Deadline are the observation time and the registered
	// teardown deadline.
	ObservedAt time.Time `json:"observed_at"`
	Deadline   time.Time `json:"deadline"`
	// PIDNamespace is the namespace link of the observer.
	PIDNamespace string `json:"pid_namespace"`
	CellPath     string `json:"cell_path"`
	// Populated is the cgroup.events populated value of the cell.
	Populated bool `json:"populated"`
	// CellRemoved is true when the held cell cgroup no longer exists;
	// DrainProof is the digest of the drain proof that licenses reading that
	// as an empty cell (drainproof.Proof.Digest).
	CellRemoved bool      `json:"cell_removed"`
	DrainProof  string    `json:"drain_proof"`
	Registry    Registry  `json:"registry"`
	Processes   []Process `json:"processes"`
	Sockets     []Socket  `json:"sockets"`
	Gaps        []Gap     `json:"gaps"`
}

// Digest is the hex SHA-256 of the snapshot's compact JSON.
func (s Snapshot) Digest() string {
	n := s.normalized()
	b, _ := json.Marshal(&n)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s Snapshot) normalized() Snapshot {
	s.ObservedAt = s.ObservedAt.UTC()
	s.Deadline = s.Deadline.UTC()
	s.Registry.Procs = nonNil(s.Registry.Procs)
	s.Processes = append([]Process{}, s.Processes...)
	for i := range s.Processes {
		s.Processes[i].OutThreads = nonNil(s.Processes[i].OutThreads)
	}
	s.Sockets = nonNil(s.Sockets)
	s.Gaps = nonNil(s.Gaps)
	return s
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// CellFacts is what a CellReader reads from the held cell cgroup.
type CellFacts struct {
	HierarchyPath string
	Populated     bool
	PIDs          []int
	Removed       bool
}

// CellReader observes the held cell cgroup. On Linux HeldCell reads a held
// descriptor; tests substitute synthetic readers.
type CellReader interface {
	Observe() (CellFacts, error)
}

// HostView is the host side of a capture: where /proc is and what time it is.
type HostView struct {
	// ProcRoot is the /proc to read ("/proc" on a real host).
	ProcRoot string
	// Now returns the observation time; nil means time.Now.
	Now func() time.Time
}

// HostProc is the view of the running host.
func HostProc() HostView { return HostView{ProcRoot: "/proc"} }

// CaptureParams are the caller-supplied inputs of one capture.
type CaptureParams struct {
	Point    Point
	Seq      int
	Deadline time.Time
	Registry Registry
	// DrainProof licenses a removed cell as drained (drainproof.Proof.Digest).
	DrainProof string
}
