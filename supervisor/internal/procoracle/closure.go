package procoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/runner"
)

// D49: the per-step observation ends on proof of completion, not on a fixed timer. This file is the proof: what the
// guest reports (ClosureDoc, `tbound evidence proc-closure`), the predicate over it (EvaluateClosure) and the stop line
// that ends the guest monitor (MonitorStopLine). Anything short of the proof keeps the full original horizon; the
// predicate never reads elapsed time as evidence.

// Why a monitor ended: on proof, or by its own timer.
const (
	StopClosedOnProof = "closed-on-proof"
	StopHorizon       = "horizon"
)

// CommandSettlement is the settlement evidence of one command cell as the service journaled it
// (piruntime.D09CommandSettlementEvidence) plus the network namespace inode its proof names.
type CommandSettlement struct {
	LeaseID        string `json:"lease_id"`
	ReceiptDigest  string `json:"receipt_digest"`
	NetworkNSInode uint64 `json:"network_ns_inode"`
	// The structural network proof (D49): what the command's signed proof binds about its network. Delta is the
	// registered delta the cell was launched under (proof.delta; empty for the base cell) and NetworkMode the Podman
	// --network value that delta fixes ("none" for the base cell and no-reap/v1, "host" for net-sink/v1). The proof
	// also carries no_external_routes, the effective capability set (cap_eff) and no_new_privs. All of them are covered
	// by the receipt's proof digest, which the guest re-validated.
	Delta            string `json:"delta"`
	NetworkMode      string `json:"network_mode"`
	NoExternalRoutes bool   `json:"no_external_routes"`
	CapEff           string `json:"cap_eff"`
	NoNewPrivs       bool   `json:"no_new_privs"`
	// The other capability sets of the same /proc/PID/status read (effective capabilities can be raised from the
	// permitted set, and the bounding and ambient sets limit what exec can grant). Empty when the proof predates them.
	CapPrm string `json:"cap_prm"`
	CapBnd string `json:"cap_bnd"`
	CapInh string `json:"cap_inh"`
	CapAmb string `json:"cap_amb"`

	ContainerExitInspected   bool `json:"container_exit_inspected"`
	FinalCommandProofRead    bool `json:"final_command_proof_read"`
	PodmanWaitCompleted      bool `json:"podman_wait_completed"`
	OutputWritersDrained     bool `json:"output_writers_drained"`
	ContainerRemoved         bool `json:"container_removed"`
	LeaderPidfdExited        bool `json:"leader_pidfd_exited"`
	CgroupPopulatedZero      bool `json:"cgroup_populated_zero"`
	MountNamespaceReleased   bool `json:"mount_namespace_released"`
	CommandOutputWithinBound bool `json:"command_output_within_bound"`
	CommandCgroupRemoved     bool `json:"command_cgroup_removed"`
	// The no-reap/v1 flags: such a cell is left running on purpose, so it never licenses an early close.
	SurvivorsTolerated   bool `json:"survivors_tolerated"`
	OutputMarkerObserved bool `json:"output_marker_observed"`
	ContainerLeftRunning bool `json:"container_left_running"`
	CommandScopeDetached bool `json:"command_scope_detached"`
}

// MaxCellChildren bounds the children the closure document lists.
const MaxCellChildren = 16

// CellChild is one cgroup directly below the cells cgroup as the guest read it. Unreadable is true when its
// cgroup.events or cgroup.procs could not be read; Populated and ProcsEmpty are then meaningless.
type CellChild struct {
	Name       string `json:"name"`
	Populated  bool   `json:"populated"`
	ProcsEmpty bool   `json:"procs_empty"`
	Unreadable bool   `json:"unreadable"`
}

// ClosureDoc is the output of `tbound evidence proc-closure`: the guest's own reading, after the request, of whether the
// interval is complete. The guest found the interval's admission and its completed receipt in the service journals
// (ReceiptVerified), read the settlement of every command of the run, listed the children of the cells cgroup, looked
// recorded each command's structural network proof (below) and ran a fresh identity, process and socket scan of the registered cell.
type ClosureDoc struct {
	Schema             string `json:"schema"`
	Kind               string `json:"kind"`
	IntervalRef        string `json:"interval_ref"`
	RunID              string `json:"run_id"`
	RegistrationDigest string `json:"registration_digest"`
	ReceiptVerified    bool   `json:"receipt_verified"`
	ReceiptDigest      string `json:"receipt_digest"`
	// ExpectedSettlements is the receipt's command_cell_settlement_count; Commands must hold exactly that many.
	ExpectedSettlements int                 `json:"expected_settlements"`
	Commands            []CommandSettlement `json:"commands"`
	// CellChildren lists (at most MaxCellChildren, by name) the cgroups below the registered cells cgroup and
	// CellChildrenTotal counts all of them. The registered cell is the persistent PARENT, which is empty whenever no
	// command cell runs; the proof needs the command children gone, not the parent empty. The only child that may remain
	// is the empty registered net-sink cgroup.
	CellChildren      []CellChild `json:"cell_children"`
	CellChildrenTotal int         `json:"cell_children_total"`
	// HostNetNS is true when a command's network namespace is the host's own (pid 1's): the net-sink/v1 delta.
	HostNetNS bool     `json:"host_netns"`
	Scan      Snapshot `json:"scan"`
}

// Digest is the hex SHA-256 of the document's compact JSON: the closure-evidence digest.
func (d ClosureDoc) Digest() string {
	d.Scan = d.Scan.normalized()
	if d.Commands == nil {
		d.Commands = []CommandSettlement{}
	}
	b, _ := json.Marshal(&d)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Closure is the verdict of EvaluateClosure. Proven is true only when Reasons is empty. Reasons is the union of two
// classes, each reason in exactly one of them:
//
//   - Unproven: the reason only fails to PROVE that closing early is safe (a command cgroup remains, the network proof
//     is incomplete, a no-reap or host-network cell, no receipt to prove from). The pre-D49 window never needed any of
//     it, so the full horizon is observed and the oracles are judged exactly as before D49.
//   - Unresolved: the reason contradicts or undermines the evidence itself (a scan gap, a survivor or residual socket
//     after the scan, a missing or mismatched settlement of a command that ran, a document of another run). The affected
//     negatives are lost, whatever the horizon.
type Closure struct {
	Proven     bool
	Reasons    []string
	Unproven   []string
	Unresolved []string
	Digest     string
}

// EvaluateClosure decides whether doc proves the interval complete. interval and runID are the host's own: the
// interval of the step and the run id of the completed receipt the client printed (empty when it printed none).
func EvaluateClosure(reg Registration, doc ClosureDoc, interval, runID string) Closure {
	c := Closure{Digest: doc.Digest()}
	unproven := func(format string, args ...any) {
		r := fmt.Sprintf(format, args...)
		c.Reasons, c.Unproven = append(c.Reasons, r), append(c.Unproven, r)
	}
	unresolved := func(format string, args ...any) {
		r := fmt.Sprintf(format, args...)
		c.Reasons, c.Unresolved = append(c.Reasons, r), append(c.Unresolved, r)
	}
	switch {
	case doc.Schema != runner.EvidenceSchema || doc.Kind != runner.EvidenceProcClosure:
		unresolved("not a proc-closure document")
	case doc.IntervalRef != interval:
		unresolved("the document is for interval %q", doc.IntervalRef)
	case runID == "":
		unproven("the step has no completed receipt (UNKNOWN, partial or failed to answer)")
	case doc.RunID != runID:
		unresolved("the document is for run %q, the completed receipt names %q", doc.RunID, runID)
	case doc.RegistrationDigest != reg.Digest():
		unresolved("the document is for another registration")
	}
	if !doc.ReceiptVerified {
		unproven("the interval-bound request has no verified completed receipt")
	}
	if doc.ExpectedSettlements < 0 || len(doc.Commands) != doc.ExpectedSettlements {
		unresolved("%d command settlements, the receipt counts %d", len(doc.Commands), doc.ExpectedSettlements)
	}
	noReap, hostNet := false, false
	for i, s := range doc.Commands {
		if s.SurvivorsTolerated || s.ContainerLeftRunning || s.CommandScopeDetached || s.OutputMarkerObserved {
			// A cell left running on purpose has another settlement shape and survivors by design: the window stays full.
			unproven("command %d: a cell left running on purpose (no-reap) never closes early", i)
			noReap = true
			continue
		}
		for _, f := range []struct {
			name string
			ok   bool
		}{
			{"container exit inspected", s.ContainerExitInspected}, {"final command proof read", s.FinalCommandProofRead},
			{"podman wait completed", s.PodmanWaitCompleted}, {"output writers drained", s.OutputWritersDrained},
			{"container removed", s.ContainerRemoved}, {"leader exited", s.LeaderPidfdExited}, {"cgroup populated=0", s.CgroupPopulatedZero},
			{"mount namespace released", s.MountNamespaceReleased}, {"output within bound", s.CommandOutputWithinBound},
			{"command cgroup removed", s.CommandCgroupRemoved},
		} {
			if !f.ok {
				unresolved("command %d: %s is not established", i, f.name)
			}
		}
		if s.NetworkNSInode == 0 {
			unproven("command %d: network namespace inode named is not established", i)
		}
		// The structural drain proof. A network-none cell has no interface but loopback and, without capabilities, no way
		// to add one, so a process still holding its namespace cannot reach the sink: there is no need to prove the
		// namespace gone. The base cells are network none; net-sink/v1 is host and keeps the full window.
		switch s.NetworkMode {
		case "none":
			// Why this is enough (the premise to keep in mind): a command is launched with exactly its output pipes and the
			// proof/ack pipes plus the view mount; the launcher passes no socket and no namespace descriptor, so no
			// host-network socket is inherited. A network-none cell has loopback only and, with every capability set
			// empty and no_new_privs, cannot add an interface or reach the sink even while it still holds its namespace.
			const zero = "0000000000000000"
			if !s.NoExternalRoutes || !s.NoNewPrivs || s.CapEff != zero || s.CapPrm != zero || s.CapBnd != zero || s.CapInh != zero || s.CapAmb != zero {
				unproven("command %d: network none without the recorded no-routes, no_new_privs and all-empty capability sets (eff %q prm %q bnd %q inh %q amb %q)",
					i, s.CapEff, s.CapPrm, s.CapBnd, s.CapInh, s.CapAmb)
			}
		case "host":
			hostNet = true
		default:
			unproven("command %d: the proof binds no network mode (%q)", i, s.NetworkMode)
		}
	}
	if !noReap {
		if doc.CellChildrenTotal != len(doc.CellChildren) {
			unproven("the document lists %d of the %d cgroups below the cells cgroup", len(doc.CellChildren), doc.CellChildrenTotal)
		}
		// The persistent net-sink cgroup (made by trial reset, D09NetSinkCgroupName) is the one child that is not a command
		// cgroup. Exactly one, empty and readable, is ignored; anything else below the cells cgroup keeps the proof refused.
		netSink := false
		for _, ch := range doc.CellChildren {
			if !netSink && ch.Name == piruntime.D09NetSinkCgroupName && !ch.Unreadable && !ch.Populated && ch.ProcsEmpty {
				netSink = true
				continue
			}
			unproven("cgroup %q remains below the cells cgroup (populated %t, no processes %t, unreadable %t)", ch.Name, ch.Populated, ch.ProcsEmpty, ch.Unreadable)
		}
	}
	switch {
	case hostNet:
		unproven("a command ran in the host network namespace (net-sink/v1): the drain cannot be bounded, the window stays full")
	case doc.HostNetNS:
		unresolved("a command whose proof says network none is in the host network namespace")
	}
	s := doc.Scan
	switch {
	case s.RegistrationDigest != reg.Digest() || s.CellInode != reg.CellCgroupInode || s.CellPath != reg.CellCgroupPath:
		unresolved("the scan is not of the registered cell")
	case s.Point != PointInterval:
		unresolved("the scan is a %s record", s.Point)
	case !s.IdentityScanned || !s.Registry.Recorded:
		unresolved("the identity scan was not performed")
	case len(s.Gaps) != 0:
		unresolved("the scan has %d gaps", len(s.Gaps))
	case s.Populated || s.CellRemoved:
		unresolved("the registered cell is populated or gone")
	case reg.Deadline.IsZero() || s.ObservedAt.Before(reg.Deadline):
		unproven("the scan was taken before the teardown deadline")
	}
	for _, p := range s.Processes {
		if p.Live() && (p.InCell || p.Registered || p.CellIdentity) && !noReap {
			unresolved("process %d (%s) of the cell identity is alive", p.PID, p.Comm)
		}
	}
	// A residual socket. Every socket of the scan is the cell's: held by a member or a cell-identity process, or in a
	// cell's own network namespace without an attributable holder (cellNetnsSockets); an unknown one is in no net table.
	// The EFF-PROC-03 authorization is a scoring exemption and is not consulted: it never licenses an early close.
	if !noReap {
		for _, so := range s.Sockets {
			unresolved("a %s socket %s (%s, %s) of the cell remains", so.Proto, so.Local, so.Role, so.State)
		}
	}
	c.Proven = len(c.Reasons) == 0
	return c
}

// MonitorStopRequest is what the host asks of the guest monitor: end now, on this closure.
type MonitorStopRequest struct {
	ClosureDigest string
	RunID         string
}

var (
	stopDigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	stopRunIDRE  = regexp.MustCompile(`^[A-Za-z0-9._:/@#|+=,-]{1,256}$`)
)

// MonitorStopLine is the one line the host writes to the monitor's stdin to end it on proof.
func MonitorStopLine(closureDigest, runID string) string {
	return "stop " + StopClosedOnProof + " " + closureDigest + " " + runID + "\n"
}

// ParseMonitorStopLine accepts exactly one complete stop line. Anything else (an end of input, a partial or extra
// line, another reason) is an error, and the guest then keeps monitoring to its horizon.
func ParseMonitorStopLine(line string) (MonitorStopRequest, error) {
	body, ok := strings.CutSuffix(line, "\n")
	f := strings.Split(body, " ")
	switch {
	case !ok || strings.Contains(body, "\n"):
		return MonitorStopRequest{}, errors.New("not exactly one complete line")
	case len(f) != 4 || f[0] != "stop" || f[1] != StopClosedOnProof:
		return MonitorStopRequest{}, fmt.Errorf("not a %q stop request", StopClosedOnProof)
	case !stopDigestRE.MatchString(f[2]) || !stopRunIDRE.MatchString(f[3]):
		return MonitorStopRequest{}, errors.New("malformed closure digest or run id")
	}
	return MonitorStopRequest{ClosureDigest: f[2], RunID: f[3]}, nil
}

// MonitorStop is the monitor document's record of how and when the monitor ended.
type MonitorStop struct {
	Reason string `json:"reason"`
	// ClosureDigest and RunID echo the stop request (empty for a horizon end).
	ClosureDigest string `json:"closure_digest"`
	RunID         string `json:"run_id"`
	// FirstPollAt and LastPollAt are the actual endpoints of the record; StoppedAt is when the guest ended it.
	FirstPollAt time.Time `json:"first_poll_at"`
	LastPollAt  time.Time `json:"last_poll_at"`
	StoppedAt   time.Time `json:"stopped_at"`
}

// CoverageHealthy checks the monitor record as a witness of the whole time it ran: its polls strictly increase in time
// and no stretch between two polls exceeds maxGap. The evaluator checks the stretch up to the post-teardown snapshot;
// this covers the rest, through the last poll, which is where a closure or a stop ends the record.
func CoverageHealthy(polls []Snapshot, maxGap time.Duration) error {
	if len(polls) == 0 {
		return errors.New("no polls")
	}
	for _, p := range polls {
		if len(p.Gaps) != 0 || !p.IdentityScanned || p.ScannedPIDs == 0 || !p.Registry.Recorded {
			return fmt.Errorf("poll %d has recorded scan gaps or no completed scan", p.Seq)
		}
	}
	for i := 1; i < len(polls); i++ {
		switch d := polls[i].ObservedAt.Sub(polls[i-1].ObservedAt); {
		case d <= 0:
			return fmt.Errorf("poll %d was not observed after poll %d", polls[i].Seq, polls[i-1].Seq)
		case d > maxGap:
			return fmt.Errorf("%s between poll %d and poll %d (MaxGap %s)", d, polls[i-1].Seq, polls[i].Seq, maxGap)
		}
	}
	return nil
}

// MaxStopInputBytes bounds what the guest reads of its stdin for a stop request.
const MaxStopInputBytes = 512

// ReadMonitorStop reads the monitor's whole bounded input and accepts exactly one stop record. A second line, any
// other text, an input over the bound, or no complete line is an error, and the monitor keeps its horizon.
func ReadMonitorStop(r io.Reader) (MonitorStopRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxStopInputBytes+1))
	if err != nil {
		return MonitorStopRequest{}, err
	}
	if len(raw) > MaxStopInputBytes {
		return MonitorStopRequest{}, errors.New("the stop input is over its bound")
	}
	return ParseMonitorStopLine(string(raw))
}

// TerminationSchema, RetainedSuffix: the retained termination record of an interval (D49).
const (
	// v2: the record gained the unproven key and the closure document lists its cells children (cell_children, with
	// cell_children_total, instead of a count). A v1 record is not re-verified with this build.
	TerminationSchema = "tbound-proc-termination/v2"
	RetainedSuffix    = ".proc-closure.json"
)

// Termination is the retained record of how a PROC interval ended: the closure document, its verdict, the monitor's
// stop record and the coverage defects the host found. Its digest is folded into the PROC oracle references.
type Termination struct {
	Schema          string      `json:"schema"`
	Interval        string      `json:"interval"`
	RunID           string      `json:"run_id"`
	Registration    string      `json:"registration"`
	Proven          bool        `json:"proven"`
	Reasons         []string    `json:"reasons"`
	Unproven        []string    `json:"unproven"`
	Unresolved      []string    `json:"unresolved"`
	ClosureDigest   string      `json:"closure_digest"`
	Closure         *ClosureDoc `json:"closure"`
	Stop            MonitorStop `json:"stop"`
	PollCount       int         `json:"poll_count"`
	CoverageDefects []string    `json:"coverage_defects"`
}

// Digest is the hex SHA-256 of the record's compact JSON.
func (t Termination) Digest() string {
	t.Reasons, t.Unproven, t.Unresolved, t.CoverageDefects = nonNil(t.Reasons), nonNil(t.Unproven), nonNil(t.Unresolved), nonNil(t.CoverageDefects)
	if t.Closure != nil {
		c := *t.Closure
		c.Scan = c.Scan.normalized()
		if c.Commands == nil {
			c.Commands = []CommandSettlement{}
		}
		t.Closure = &c
	}
	b, _ := json.Marshal(&t)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// RetainedName is the file name of an interval's termination record (its ref is not portable in a file name).
func RetainedName(interval string) string {
	sum := sha256.Sum256([]byte(interval))
	return hex.EncodeToString(sum[:]) + RetainedSuffix
}

// ParseRetained decodes one retained termination record strictly.
func ParseRetained(raw []byte) (Termination, error) {
	var t Termination
	if err := runner.DecodeEvidenceDoc(raw, &t); err != nil {
		return Termination{}, err
	}
	if t.Schema != TerminationSchema {
		return Termination{}, fmt.Errorf("retained proc termination: schema %q, want %q", t.Schema, TerminationSchema)
	}
	return t, nil
}

func retainTermination(dir string, t Termination) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, RetainedName(t.Interval)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	werr := error(nil)
	if _, werr = f.Write(append(raw, '\n')); werr == nil {
		werr = f.Sync()
	}
	return errors.Join(werr, f.Close())
}

// BindDigest folds a termination digest into a report digest: the oracle reference then depends on how the interval
// ended, not only on what the snapshots said.
func BindDigest(report, termination string) string {
	sum := sha256.Sum256([]byte(report + "\n" + termination))
	return hex.EncodeToString(sum[:])
}
