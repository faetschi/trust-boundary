package procoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

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

// ClosureDoc is the output of `tbound evidence proc-closure`: the guest's own reading, after the request, of whether the
// interval is complete. The guest found the interval's admission and its completed receipt in the service journals
// (ReceiptVerified), read the settlement of every command of the run, listed the children of the cells cgroup, looked
// for the command cells' network namespaces and ran a fresh identity, process and socket scan of the registered cell.
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
	// CellChildren counts the cgroups below the registered cells cgroup. The registered cell is the persistent PARENT,
	// which is empty whenever no command cell runs; the proof needs the children gone, not the parent empty.
	CellChildren int `json:"cell_children"`
	// NetNSGone is true when none of the commands' network namespaces is held any more (no process, no mount).
	NetNSGone bool     `json:"netns_gone"`
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

// Closure is the verdict of EvaluateClosure. Proven is true only when Reasons is empty.
type Closure struct {
	Proven  bool
	Reasons []string
	Digest  string
}

// EvaluateClosure decides whether doc proves the interval complete. interval and runID are the host's own: the
// interval of the step and the run id of the completed receipt the client printed (empty when it printed none).
func EvaluateClosure(reg Registration, doc ClosureDoc, interval, runID string) Closure {
	c := Closure{Digest: doc.Digest()}
	no := func(format string, args ...any) { c.Reasons = append(c.Reasons, fmt.Sprintf(format, args...)) }
	switch {
	case doc.Schema != runner.EvidenceSchema || doc.Kind != runner.EvidenceProcClosure:
		no("not a proc-closure document")
	case doc.IntervalRef != interval:
		no("the document is for interval %q", doc.IntervalRef)
	case runID == "" || doc.RunID != runID:
		no("the document is for run %q, the completed receipt names %q", doc.RunID, runID)
	case doc.RegistrationDigest != reg.Digest():
		no("the document is for another registration")
	}
	if !doc.ReceiptVerified {
		no("the interval-bound request has no verified completed receipt")
	}
	if doc.ExpectedSettlements < 0 || len(doc.Commands) != doc.ExpectedSettlements {
		no("%d command settlements, the receipt counts %d", len(doc.Commands), doc.ExpectedSettlements)
	}
	for i, s := range doc.Commands {
		for _, f := range []struct {
			name string
			ok   bool
		}{
			{"container exit inspected", s.ContainerExitInspected}, {"final command proof read", s.FinalCommandProofRead},
			{"podman wait completed", s.PodmanWaitCompleted}, {"output writers drained", s.OutputWritersDrained},
			{"container removed", s.ContainerRemoved}, {"leader exited", s.LeaderPidfdExited}, {"cgroup populated=0", s.CgroupPopulatedZero},
			{"mount namespace released", s.MountNamespaceReleased}, {"output within bound", s.CommandOutputWithinBound},
			{"command cgroup removed", s.CommandCgroupRemoved}, {"network namespace inode named", s.NetworkNSInode != 0},
		} {
			if !f.ok {
				no("command %d: %s is not established", i, f.name)
			}
		}
		if s.SurvivorsTolerated || s.ContainerLeftRunning || s.CommandScopeDetached || s.OutputMarkerObserved {
			no("command %d: a cell left running on purpose (no-reap) never closes early", i)
		}
	}
	if doc.CellChildren != 0 {
		no("%d command cgroups remain below the cells cgroup", doc.CellChildren)
	}
	if !doc.NetNSGone {
		no("a command network namespace is still held")
	}
	s := doc.Scan
	switch {
	case s.RegistrationDigest != reg.Digest() || s.CellInode != reg.CellCgroupInode || s.CellPath != reg.CellCgroupPath:
		no("the scan is not of the registered cell")
	case s.Point != PointInterval:
		no("the scan is a %s record", s.Point)
	case !s.IdentityScanned || !s.Registry.Recorded:
		no("the identity scan was not performed")
	case len(s.Gaps) != 0:
		no("the scan has %d gaps", len(s.Gaps))
	case s.Populated || s.CellRemoved:
		no("the registered cell is populated or gone")
	case reg.Deadline.IsZero() || s.ObservedAt.Before(reg.Deadline):
		no("the scan was taken before the teardown deadline")
	}
	for _, p := range s.Processes {
		if p.Live() && (p.InCell || p.Registered || p.CellIdentity) {
			no("process %d (%s) of the cell identity is alive", p.PID, p.Comm)
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
