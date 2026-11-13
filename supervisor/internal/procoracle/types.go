package procoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"tbound/supervisor/internal/runner"
)

// OracleVersion labels the snapshot and report formats and the rule set.
// Change it whenever a field or rule changes meaning: registered oracle digests
// depend on it.
// v4: Process.NSPid, Policy.LingeringInit and Report.LingeringInit; EFF-PROC-01 skips the cell init of a
// no-reap/v1 run (cellInit).
// v5: the cell identity is the user namespace (cellUserNS), not a uid: Registration.ServiceUID, Process.UserNS;
// ownerless sockets come from the cell's own network namespaces only, and any socket of a closure scan refuses it.
// The value is defined in the runner (runner.ProcOracleVersion), which checks the
// provenance of every PROC finding against it and cannot import this package.
const OracleVersion = runner.ProcOracleVersion

// Instrument names the observation method in Report.Instrument.
const Instrument = "linux-identity-scan-cgroup-fd-procfs-sockets"

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
	// PointMonitor marks a Monitor poll: an identity scan without sockets, used
	// only as the continuous-coverage witness of EFF-PROC-02.
	PointMonitor Point = "monitor"
)

// Gap domains. A gap in the socket domain does not affect EFF-PROC-01 and
// EFF-PROC-02; the coverage domain affects EFF-PROC-02 only, the deadline domain
// EFF-PROC-01 only; every other domain affects all three predicates.
const (
	DomainCell     = "cell"
	DomainRegistry = "registry"
	DomainProcess  = "process"
	DomainSocket   = "socket"
	DomainSequence = "sequence"
	DomainPoint    = "point"
	DomainDeadline = "deadline"
	// DomainIdentity: the scan of all of /proc for the cell identity was not
	// performed or not complete (hidepid, unreadable /proc). Affects all predicates.
	DomainIdentity = "identity"
	// DomainRegistration: the registration is invalid or a snapshot does not carry
	// its digest. Affects all predicates.
	DomainRegistration = "registration"
	// DomainCoverage: the monitor record does not witness the whole interval.
	// Affects EFF-PROC-02 only.
	DomainCoverage = "coverage"
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
	// UID is the real, effective and saved user id from /proc/<pid>/status.
	UID [3]uint32 `json:"uid"`
	// CellIdentity is true when the process is in a cell user namespace (cellUserNS): two or more levels
	// below the scanner's own user namespace, under a top namespace owned by Registration.ServiceUID. The
	// process belongs to the cell wherever its cgroup is and whatever its uid.
	CellIdentity bool `json:"cell_identity"`
	// UserNS are the inodes of the process's user namespace and its ancestors, own first, up to the one
	// directly below the scanner's (empty for a process in the scanner's own user namespace).
	UserNS []uint64 `json:"user_ns"`
	// Registered is true when the registry holds this pid and start time.
	Registered bool `json:"registered"`
	// OutThreads are threads that sit in another cgroup than the process.
	OutThreads []Thread `json:"out_threads"`
	// NSPid is the NSpid line of /proc/<pid>/status: the process's pid in each PID namespace from the
	// observer's down to its own, so the last entry is its pid in its innermost namespace and 1 means
	// it is the init of that namespace. Empty when the line was not there (a kernel without it).
	NSPid []int `json:"ns_pid"`
}

// CellInitComm is the comm of the cell's init, tbound-cell-entrypoint, as the kernel truncates it to
// fifteen characters.
const CellInitComm = "tbound-cell-ent"

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
	// UID is the uid column of a /proc/net inet row as the scanner's user namespace
	// shows it (a cell socket shows the service uid). It attributes nothing: 0 for unix
	// sockets, which have none.
	UID  uint32 `json:"uid"`
	Role string `json:"role"`
}

// Snapshot is the observation of one sampling point.
type Snapshot struct {
	// Seq numbers the snapshots of an interval 1..n; a missing number is a gap.
	Seq   int   `json:"seq"`
	Point Point `json:"point"`
	// ObservedAt is the observation time.
	ObservedAt time.Time `json:"observed_at"`
	// RegistrationDigest is the digest of the Registration the capture ran under
	// (Registration.Digest). The deadline and the identity are not carried here:
	// Evaluate takes them from the registration itself and rejects a snapshot
	// whose digest is missing or different.
	RegistrationDigest string `json:"registration_digest"`
	// IdentityScanned is true when the scan of all of /proc for the cell identity
	// ran (its failures are Gaps); ScannedPIDs is the number of /proc pid entries
	// it examined.
	IdentityScanned bool `json:"identity_scanned"`
	ScannedPIDs     int  `json:"scanned_pids"`
	// PIDNamespace is the namespace link of the observer.
	PIDNamespace string `json:"pid_namespace"`
	CellPath     string `json:"cell_path"`
	// CellInode is the inode of the held cell cgroup directory as the capture saw it.
	CellInode uint64 `json:"cell_inode"`
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
	s.Registry.Procs = nonNil(s.Registry.Procs)
	s.Processes = append([]Process{}, s.Processes...)
	for i := range s.Processes {
		s.Processes[i].OutThreads = nonNil(s.Processes[i].OutThreads)
		s.Processes[i].NSPid = nonNil(s.Processes[i].NSPid)
		s.Processes[i].UserNS = nonNil(s.Processes[i].UserNS)
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
	// Inode is the inode of the held cgroup directory (cgroup2 ids are inodes); it is
	// read even when the cell has been removed.
	Inode uint64
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
	// UserNS resolves a /proc/<pid>/ns/user link to that user namespace and its ancestors, own first,
	// through the outermost one the scanner may see; nil means the NS_GET_PARENT walk of the live kernel.
	UserNS func(nsFile string) ([]UserNSLevel, error)
}

// UserNSLevel is one user namespace of an ancestry: its inode and the uid of its owner as the
// scanner's user namespace sees it (NS_GET_OWNER_UID).
type UserNSLevel struct {
	Inode uint64
	Owner uint32
}

// HostProc is the view of the running host.
func HostProc() HostView { return HostView{ProcRoot: "/proc"} }

// CaptureParams are the caller-supplied inputs of one capture.
type CaptureParams struct {
	Point Point
	Seq   int
	// Registration is the immutable record the executor took from the launcher
	// or service receipt; the capture binds itself to its digest and reads the
	// cell identity from it.
	Registration Registration
	Registry     Registry
	// DrainProof licenses a removed cell as drained (drainproof.Proof.Digest).
	DrainProof string
}

// Registration is the single immutable record of what the oracle needs to know
// about one cell and one trial interval: the teardown deadline, the dedicated
// cell identity and the maximum gap the continuous-coverage witness may have.
// The executor MUST take it from the launcher or service receipt (the party that
// created the cell and set the deadline), never from the capture side or from a
// snapshot. Every Snapshot carries Digest(); Evaluate takes the Registration as
// its own input and rejects a snapshot whose digest is missing or different.
type Registration struct {
	// CellID names the cell the registration is for. The oracle treats it as
	// opaque; it binds the cell into the digest.
	CellID string `json:"cell_id"`
	// CellCgroupPath and CellCgroupInode are the verifiable identity of the cell: the
	// cgroup2 hierarchy path and the inode of the cell cgroup directory, taken from
	// the receipt of the party that created the cell. A capture that observes another
	// cgroup refuses, and Evaluate rejects any snapshot or poll that observed another.
	CellCgroupPath  string `json:"cell_cgroup_path"`
	CellCgroupInode uint64 `json:"cell_cgroup_inode"`
	// IntervalStart is when the trial interval began (cell creation).
	IntervalStart time.Time `json:"interval_start"`
	// Deadline is the registered teardown deadline (EFF-PROC-01).
	Deadline time.Time `json:"deadline"`
	// CellUID and CellGID are the identity inside the cell (the template's --user). They are bound into
	// the digest; processes are not matched by them: inside a user namespace they say nothing about the host.
	CellUID uint32 `json:"cell_uid"`
	CellGID uint32 `json:"cell_gid"`
	// ServiceUID is the host uid that runs the cells' rootless Podman: it owns the rootless user namespace
	// (Podman's pause namespace) whose descendants are the cells. The cell identity is that ancestry
	// (cellUserNS), not a uid: a process cannot move into an ancestor user namespace.
	ServiceUID uint32 `json:"service_uid"`
	// MaxGap is the largest allowed time between two Monitor polls (and between
	// IntervalStart and the first poll, and the last poll and the post-teardown
	// snapshot). A boundary crossing shorter than MaxGap can be missed: that is
	// a stated detection limit of EFF-PROC-02.
	MaxGap time.Duration `json:"max_gap_ns"`
}

// Digest is the hex SHA-256 of the registration's compact JSON (times in UTC).
func (r Registration) Digest() string {
	r.IntervalStart, r.Deadline = r.IntervalStart.UTC(), r.Deadline.UTC()
	b, _ := json.Marshal(&r)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// gaps lists what makes the registration unusable. A missing deadline is a
// deadline gap (it affects EFF-PROC-01 only); every other defect affects all
// predicates because the identity or the coverage bound is unknown.
func (r Registration) gaps() []Gap {
	var gaps []Gap
	bad := func(format string, args ...any) {
		gaps = append(gaps, gapf(DomainRegistration, "registration_invalid", format, args...))
	}
	switch {
	case r.CellID == "":
		bad("no cell id")
	case r.CellUID == 0 || r.CellGID == 0 || r.CellUID == ^uint32(0) || r.CellGID == ^uint32(0):
		bad("the cell identity %d:%d is root or unset", r.CellUID, r.CellGID)
	case r.ServiceUID == 0 || r.ServiceUID == ^uint32(0):
		bad("the service uid %d that owns the cells' user namespaces is root or unset", r.ServiceUID)
	case r.MaxGap <= 0:
		bad("no positive MaxGap")
	case r.IntervalStart.IsZero():
		bad("no interval start")
	case r.CellCgroupPath == "" || r.CellCgroupInode == 0:
		bad("the registration does not identify the cell cgroup (path and inode)")
	}
	switch {
	case r.Deadline.IsZero():
		gaps = append(gaps, gapf(DomainDeadline, "deadline_missing", "the registration has no teardown deadline"))
	case !r.IntervalStart.IsZero() && r.Deadline.Before(r.IntervalStart):
		bad("the deadline is before the interval start")
	}
	return gaps
}

// cellMatches reports whether an observed cell cgroup (hierarchy path and directory
// inode) is the registered one. A removed cell has no path left to compare, so its
// inode alone decides.
func (r Registration) cellMatches(path string, inode uint64, removed bool) bool {
	return r.CellCgroupInode != 0 && inode == r.CellCgroupInode && (removed || (r.CellCgroupPath != "" && path == r.CellCgroupPath))
}

func (r Registration) identityUsable() bool {
	return r.CellID != "" && r.CellUID != 0 && r.CellGID != 0 && r.CellUID != ^uint32(0) && r.CellGID != ^uint32(0) &&
		r.ServiceUID != 0 && r.ServiceUID != ^uint32(0)
}

// underCell is true when path is the cell cgroup or a cgroup nested below it.
func underCell(path, cell string) bool {
	return cell != "" && (path == cell || strings.HasPrefix(path, cell+"/"))
}
